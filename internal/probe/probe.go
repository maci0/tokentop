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

// defaultProbeTokens sizes a probe when the caller gives no bound: a few
// dozen tokens are plenty to time first-token latency and decode rate.
const defaultProbeTokens = 32

// maxProbeTokens is the hard ceiling on one probe's generation. Request is
// an exported struct, so the cap lives here at the spender: however a future
// caller configures it, a benchmark can never turn into an unbounded run.
const maxProbeTokens = 512

const promptText = "Count from one to twenty as words."

type Request struct {
	Kind      string // core.KindOllama | openai-compatible kinds
	Base      string
	Model     string
	MaxTokens int
}

// Run performs one probe and returns its sample (OK=false with Err set on failure).
func Run(ctx context.Context, r Request) core.ProbeSample {
	s := core.ProbeSample{At: time.Now(), Addr: r.Base, Model: r.Model}
	switch {
	case r.MaxTokens <= 0:
		r.MaxTokens = defaultProbeTokens
	case r.MaxTokens > maxProbeTokens:
		r.MaxTokens = maxProbeTokens
	}
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
			"num_predict": r.MaxTokens,
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
			continue
		}
		if chunk.EvalCount > 0 {
			tokens = chunk.EvalCount
			evalDur = time.Duration(chunk.EvalDuration) * time.Nanosecond
		}
	}
	if err := sc.Err(); err != nil {
		return 0, 0, ttft, err
	}
	if tokens == 0 { // stream closed without a single token: engine is broken
		return 0, 0, ttft, fmt.Errorf("empty stream")
	}
	return tokens, evalDur, ttft, nil
}

func probeOpenAI(ctx context.Context, r Request, s *core.ProbeSample) (tokens int, ttft time.Duration, err error) {
	body, _ := json.Marshal(map[string]any{
		"model": r.Model,
		"messages": []map[string]string{
			{"role": "user", "content": promptText},
		},
		"max_tokens":     r.MaxTokens,
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
			tokens = chunk.Usage.CompletionTokens
		}
		for _, c := range chunk.Choices {
			if c.Delta.Content != "" {
				tokens++
				if ttft == 0 {
					ttft = time.Since(s.At)
				}
			}
		}
	}
	if err := sc.Err(); err != nil {
		return 0, ttft, err
	}
	if tokens == 0 {
		return 0, ttft, fmt.Errorf("empty stream")
	}
	return tokens, ttft, nil
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
