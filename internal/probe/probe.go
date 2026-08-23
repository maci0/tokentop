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

	"tokentop/internal/core"
)

var client = &http.Client{Timeout: 30 * time.Second}

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
	if r.MaxTokens <= 0 {
		r.MaxTokens = 32
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
	s.OK = true
	s.Tokens = tokens
	if ttft > 0 {
		s.TTFTms = float64(ttft.Microseconds()) / 1000.0
	}
	switch {
	case evalDur > 0:
		s.TokPS = float64(tokens) / evalDur.Seconds()
	case total > ttft && tokens > 0:
		s.TokPS = float64(tokens) / (total - ttft).Seconds()
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
		}
		if json.Unmarshal(line, &chunk) != nil {
			continue
		}
		if !chunk.Done {
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
	return tokens, evalDur, ttft, sc.Err()
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
		}
		if json.Unmarshal([]byte(payload), &chunk) != nil {
			continue
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
	if tokens == 0 {
		return 0, ttft, fmt.Errorf("empty stream")
	}
	return tokens, ttft, sc.Err()
}

func postJSON(ctx context.Context, url string, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		return nil, fmt.Errorf("http %s", resp.Status)
	}
	return resp, nil
}
