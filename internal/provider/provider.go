// Package provider implements discovery and metric scraping for common
// local inference backends: Ollama, vLLM, llama.cpp-server and any
// OpenAI-compatible HTTP server.
package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"tokentop/internal/bearer"
	"tokentop/internal/core"
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
		return err
	}
	bearer.Apply(req)
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("http %s", resp.Status)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(out)
}

// getText fetches a URL with the given client; callers enforce deadlines via
// the client timeout or request context.
func getText(c *http.Client, url string) (string, error) {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	bearer.Apply(req)
	resp, err := c.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("http %s", resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	return string(b), err
}

// fetchVersion probes common engine version endpoints once and caches the
// result. Engines differ wildly here: /api/version (Ollama-style),
// /version (vLLM, llama.cpp), /get_server_info (SGLang embeds one).
func fetchVersion(cache *sync.Once, base string) string {
	var v string
	cache.Do(func() {
		for _, path := range []string{"/api/version", "/version", "/get_server_info"} {
			text, err := getText(httpClient, base+path)
			if err != nil {
				continue
			}
			if val := extractVersionField(text); val != "" {
				v = val
				return
			}
		}
	})
	return v
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
	for _, line := range strings.Split(text, "\n") {
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
			cnt := famByKey(lower, trimSuffix(n, "_sum")+"_count")
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

func famByKey(m map[string]float64, k string) float64 { return m[k] }

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

func trimSuffix(s, suf string) string {
	return strings.TrimSuffix(s, suf)
}
