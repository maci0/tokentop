// Package probe fires small streaming generations at backends to measure
// time-to-first-token and decode throughput the way clients experience it.
package probe

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/maci0/toktop/internal/bearer"
	"github.com/maci0/toktop/internal/core"
	"github.com/maci0/toktop/internal/httperr"
)

var client = &http.Client{Timeout: 30 * time.Second}

// probeTokens sizes a probe: a few dozen tokens are plenty to time
// first-token latency and decode rate without turning a benchmark into an
// unbounded generation. The request asks the engine to stop there; the
// client also stops reading after this many content frames, because some
// gateways ignore max_tokens/num_predict and would otherwise generate (and
// bill) until the HTTP timeout.
const probeTokens = 32

// probeTokenTrust is the highest engine-reported eval_count /
// completion_tokens we believe. Tokenizers can overshoot the request a
// little; a billion-token usage field is junk that would poison tok/s.
const probeTokenTrust = probeTokens * 4

const promptText = "Count from one to twenty as words."

type Request struct {
	Kind  string // core.KindOllama | openai-compatible kinds
	Base  string
	Model string
}

// Run performs one probe and returns its sample (OK=false with Err set on failure).
func Run(ctx context.Context, r Request) core.ProbeSample {
	s := core.ProbeSample{At: time.Now(), Addr: r.Base, Model: r.Model}
	start := time.Now()
	var (
		ttft    time.Duration
		tokens  int
		evalDur time.Duration
		err     error
	)
	if r.Kind == core.KindOllama {
		tokens, evalDur, ttft, err = probeOllama(ctx, r, &s)
	} else {
		tokens, ttft, err = probeOpenAI(ctx, r, &s)
	}
	total := time.Since(start)
	if err != nil {
		s.Err = err.Error()
		return s
	}
	// Windows clocks tick coarsely; instant local servers can land the whole
	// exchange inside one tick. Fall back to full-duration accounting.
	if ttft <= 0 {
		ttft = total
	}
	s.OK = true
	s.Tokens = tokens
	s.TTFTms = float64(ttft.Microseconds()) / 1000.0
	switch {
	case evalDur > 0:
		s.TokPS = float64(tokens) / evalDur.Seconds()
	case total > ttft && tokens > 0:
		s.TokPS = float64(tokens) / (total - ttft).Seconds()
	case tokens > 0 && total > 0:
		s.TokPS = float64(tokens) / total.Seconds()
	}
	return s
}

func probeOllama(ctx context.Context, r Request, s *core.ProbeSample) (tokens int, evalDur, ttft time.Duration, err error) {
	body, _ := json.Marshal(map[string]any{
		"model":  r.Model,
		"prompt": promptText,
		"stream": true,
		"options": map[string]any{
			"num_predict": probeTokens,
			"temperature": 0.2,
		},
	})
	resp, err := postJSON(ctx, r.Base+"/api/generate", body)
	if err != nil {
		return 0, 0, 0, err
	}
	defer resp.Body.Close()
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	var reported int
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var chunk struct {
			Response     string `json:"response"`
			Done         bool   `json:"done"`
			EvalCount    int    `json:"eval_count"`
			EvalDuration int64  `json:"eval_duration"`
			Error        string `json:"error"`
		}
		if json.Unmarshal(line, &chunk) != nil {
			continue
		}
		// Ollama streams failures as {"error":…} lines with HTTP 200; decoding
		// them as content would report a green probe with invented throughput.
		if chunk.Error != "" {
			return 0, 0, ttft, fmt.Errorf("engine error: %s", httperr.Snippet([]byte(chunk.Error)))
		}
		if !chunk.Done {
			if chunk.Response == "" { // keep-alive frames carry no content and are not tokens
				continue
			}
			tokens++
			if ttft == 0 {
				ttft = time.Since(s.At)
			}
			if tokens >= probeTokens { // engine ignored num_predict: hang up
				break
			}
			continue
		}
		if chunk.EvalCount > 0 {
			reported = chunk.EvalCount
			evalDur = time.Duration(chunk.EvalDuration) * time.Nanosecond
		}
	}
	if err := streamReadErr(ctx, sc.Err(), tokens); err != nil {
		return 0, 0, ttft, err
	}
	n, trust := resolveTokens(tokens, reported)
	if !trust {
		evalDur = 0 // eval_duration is paired with the rejected count
	}
	if n == 0 { // stream closed without a single token: engine is broken
		return 0, 0, ttft, fmt.Errorf("empty stream")
	}
	return n, evalDur, ttft, nil
}

func probeOpenAI(ctx context.Context, r Request, s *core.ProbeSample) (tokens int, ttft time.Duration, err error) {
	body, _ := json.Marshal(map[string]any{
		"model": r.Model,
		"messages": []map[string]string{
			{"role": "user", "content": promptText},
		},
		"max_tokens":     probeTokens,
		"temperature":    0.2,
		"stream":         true,
		"stream_options": map[string]bool{"include_usage": true},
	})
	resp, err := postJSON(ctx, r.Base+"/v1/chat/completions", body)
	if err != nil {
		return 0, 0, err
	}
	defer resp.Body.Close()
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	var reported int
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "[DONE]" {
			break
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
			Usage *struct {
				CompletionTokens int `json:"completion_tokens"`
			} `json:"usage"`
			Error json.RawMessage `json:"error"`
		}
		if json.Unmarshal([]byte(payload), &chunk) != nil {
			continue
		}
		// llama.cpp, LiteLLM and other gateways emit SSE error events with
		// HTTP 200; skipping them passed broken generations off as partial
		// successes (or as a context-free "empty stream").
		if msg := sseErrorMessage(chunk.Error); msg != "" {
			return 0, ttft, fmt.Errorf("engine error: %s", msg)
		}
		if chunk.Usage != nil && chunk.Usage.CompletionTokens > 0 {
			reported = chunk.Usage.CompletionTokens
		}
		for _, c := range chunk.Choices {
			if c.Delta.Content != "" {
				tokens++
				if ttft == 0 {
					ttft = time.Since(s.At)
				}
			}
		}
		if tokens >= probeTokens { // engine ignored max_tokens: hang up
			break
		}
	}
	if err := streamReadErr(ctx, sc.Err(), tokens); err != nil {
		return 0, ttft, err
	}
	n, _ := resolveTokens(tokens, reported)
	if n == 0 {
		return 0, ttft, fmt.Errorf("empty stream")
	}
	return n, ttft, nil
}

// resolveTokens picks a probe's token count. Engine-reported usage is
// preferred when it sits in a plausible band around the requested
// generation; anything outside is ignored in favour of content frames
// actually observed. Observed counts are capped at probeTokens because
// the client stops reading there.
func resolveTokens(observed, reported int) (tokens int, trustReported bool) {
	if reported > 0 && reported <= probeTokenTrust {
		return reported, true
	}
	if observed > probeTokens {
		return probeTokens, false
	}
	return observed, false
}

// streamReadErr maps a scanner/body error onto a probe outcome. A cancelled
// caller's context is a real failure (shutdown must not mint a sample). A
// mid-stream drop after at least one token still yields a timed sample:
// hanging up is how we bound engines that ignore max_tokens, and a client
// timeout would otherwise throw away TTFT already measured.
func streamReadErr(ctx context.Context, err error, tokens int) error {
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		return err
	}
	if tokens > 0 {
		return nil
	}
	return err
}

// sseErrorMessage extracts an engine-reported failure from a streaming data
// payload. Gateways disagree on the shape: {"error":{"message":…}},
// {"error":"…"}, or other junk; null and absent mean no error. Unrecognized
// junk is capped by httperr.Snippet; a recognized message passes through as
// sent, clipped to the readout's line width at render time.
func sseErrorMessage(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var obj struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(raw, &obj) == nil && obj.Message != "" {
		return obj.Message
	}
	var s string
	if json.Unmarshal(raw, &s) == nil && s != "" {
		return s
	}
	return httperr.Snippet(raw)
}

func postJSON(ctx context.Context, url string, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", url, err)
	}
	req.Header.Set("Content-Type", "application/json")
	bearer.Apply(req)
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", url, err)
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		return nil, httperr.Status(url, resp)
	}
	return resp, nil
}
