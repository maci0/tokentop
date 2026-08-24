// Package provider implements discovery and metric scraping for local
// inference backends: Ollama, vLLM, SGLang, TRT-LLM/Triton, llama.cpp,
// LM Studio, MLX, KoboldCpp and further engines fingerprinted by their HTTP
// surface, plus a generic OpenAI-compatible fallback. Metric scraping reads
// Prometheus /metrics where engines publish it.
package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"tokentop/internal/bearer"
	"tokentop/internal/core"
	"tokentop/internal/httperr"
)

// PollTimeout bounds a single metrics scrape.
const PollTimeout = 1500 * time.Millisecond

// Metrics is the raw engine state a single poll yields. Rates are derived by
// the collector from successive samples.
type Metrics struct {
	Models   []core.ModelInfo
	OutTotal float64 // generation tokens, monotonic counter
	InTotal  float64 // prompt tokens, monotonic counter
	Running  int
	Waiting  int
	KVPct    float64 // 0..100
	HasKV    bool
	TTFTms   float64 // engine-reported mean TTFT if it publishes one

	DirectOutPS float64 // engine-reported instantaneous tok/s, if it publishes one
	Version     string  // engine software version, best effort
}

type Provider interface {
	Label() string
	Addr() string
	Poll(ctx context.Context) (*Metrics, error)
}

var httpClient = &http.Client{Timeout: PollTimeout}

func getJSON(ctx context.Context, url string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("%s: %w", url, err)
	}
	bearer.Apply(req)
	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("%s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return httperr.Status(url, resp)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(out); err != nil {
		return fmt.Errorf("%s: %w", url, err)
	}
	return nil
}

// getText fetches a URL with the given client; the caller's context bounds
// the request alongside any client timeout.
func getText(ctx context.Context, c *http.Client, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("%s: %w", url, err)
	}
	bearer.Apply(req)
	resp, err := c.Do(req)
	if err != nil {
		return "", fmt.Errorf("%s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", httperr.Status(url, resp)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return "", fmt.Errorf("%s: %w", url, err)
	}
	return string(b), nil
}

// versionCache memoizes engine version discovery across polls. Deliberately
// not a sync.Once: an engine polled while still starting answers nothing,
// and caching that miss would blank the version readout for the whole
// session. Unresolved caches retry on later polls.
type versionCache struct {
	mu       sync.Mutex
	resolved bool
	val      string
}

// fetch probes common engine version endpoints and caches the first success.
// Engines differ wildly here: /api/version (Ollama-style), /version (vLLM,
// llama.cpp), /get_server_info (SGLang embeds one).
func (c *versionCache) fetch(ctx context.Context, base string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.resolved {
		return c.val
	}
	for _, path := range []string{"/api/version", "/version", "/get_server_info"} {
		text, err := getText(ctx, httpClient, base+path)
		if err != nil {
			continue
		}
		if val := extractVersionField(text); val != "" {
			c.val, c.resolved = val, true
			break
		}
	}
	return c.val
}

// extractVersionField pulls a "version" member out of JSON-ish bodies.
func extractVersionField(body string) string {
	trimmed := strings.TrimSpace(body)
	var doc map[string]any
	if json.Unmarshal([]byte(trimmed), &doc) == nil {
		for _, key := range []string{"version", "Version"} {
			if s, ok := doc[key].(string); ok && s != "" {
				return s
			}
		}
		// nested (SGLang get_server_info)
		for _, sub := range []string{"backend_version_info", "server_info", "versions"} {
			if inner, ok := doc[sub].(map[string]any); ok {
				for _, key := range []string{"version", "sglang_version", "vllm_version"} {
					if s, ok := inner[key].(string); ok && s != "" {
						return s
					}
				}
			}
		}
		return ""
	}
	// llama.cpp /version may answer with a bare quoted string or plain text
	if trimmed == "" || len(trimmed) > 128 {
		return ""
	}
	if strings.HasPrefix(trimmed, "\"") && strings.HasSuffix(trimmed, "\"") {
		return strings.Trim(trimmed, "\"")
	}
	if !strings.ContainsAny(trimmed, "{}\n") {
		return trimmed
	}
	return ""
}

// ParseProm scrapes a minimal Prometheus text exposition into family values.
// Labeled series of the same family are summed; histograms contribute their
// _sum and _count families verbatim.
func ParseProm(text string) map[string]float64 {
	fam := map[string]float64{}
	for line := range strings.SplitSeq(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, val, ok := splitMetric(line)
		if !ok || strings.HasSuffix(name, "_bucket") {
			continue
		}
		fam[name] += val
	}
	return fam
}

func splitMetric(line string) (string, float64, bool) {
	sp := strings.LastIndexByte(line, ' ')
	if sp < 0 {
		return "", 0, false
	}
	name := strings.TrimSpace(line[:sp])
	v, err := strconv.ParseFloat(strings.TrimSpace(line[sp+1:]), 64)
	if err != nil || name == "" {
		return "", 0, false
	}
	// The exposition format allows NaN/+Inf values (0/0 gauges on engines
	// that have not served yet); they would poison the summed family and,
	// through the stored totals, every derived rate from here on.
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return "", 0, false
	}
	if i := strings.IndexByte(name, '{'); i >= 0 {
		name = name[:i]
	}
	return name, v, true
}

// classify maps scraped families onto our Metrics using fuzzy, version-tolerant
// name matching across engines (vLLM, SGLang, Triton/TRT-LLM, llama.cpp…).
func classify(fam map[string]float64, m *Metrics) {
	lower := make(map[string]float64, len(fam))
	for k, v := range fam {
		lower[strings.ToLower(k)] = v
	}
	for n, v := range lower {
		hasTok := strings.Contains(n, "token")
		switch {
		case hasTok && strings.Contains(n, "total"):
			outish := containsAny(n, "generat", "predict", "complet", "eval")
			inish := containsAny(n, "prompt", "input")
			switch {
			case inish:
				m.InTotal = v
			case outish:
				m.OutTotal = v
			}
		case strings.Contains(n, "token_usage"):
			if v >= 0 && v <= 1 { // SGLang: fraction of token pool in use
				m.KVPct = v * 100
				m.HasKV = true
			}
		case strings.Contains(n, "request") && containsAny(n, "run", "process", "active", "inflight"),
			containsAny(n, "req") && containsAny(n, "run", "process", "active", "inflight") &&
				!containsAny(n, "time", "duration", "second"):
			m.Running = int(v)
		case containsAny(n, "req") && containsAny(n, "wait", "queue", "pend") &&
			!containsAny(n, "time", "duration", "second"):
			m.Waiting = int(v) // covers vLLM requests_waiting and SGLang num_queue_reqs
		case strings.Contains(n, "cache") && containsAny(n, "usage", "util", "ratio", "perc"):
			pct := v
			if pct <= 1.0 {
				pct *= 100
			}
			if pct >= 0 && pct <= 100 {
				m.KVPct = pct
				m.HasKV = true
			}
		case strings.Contains(n, "time_to_first_token") && strings.HasSuffix(n, "_sum"):
			cnt := lower[strings.TrimSuffix(n, "_sum")+"_count"]
			if cnt > 0 {
				m.TTFTms = v / cnt * 1000
			}
		case strings.Contains(n, "throughput") && containsAny(n, "gen", "generation", "decode"):
			if v > 0 { // engines publishing instantaneous tok/s (SGLang, TRT-LLM)
				m.DirectOutPS = v
			}
		}
	}
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
