package probe

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/maci0/toktop/internal/core"
)

func TestRunOpenAIStream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path = %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		f := http.NewResponseController(w)
		w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"He\"}}]}\n\n"))
		f.Flush()
		w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"llo\"}}]}\n\n"))
		f.Flush()
		w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"!\"}}]}\n\n"))
		f.Flush()
		w.Write([]byte("data: {\"usage\":{\"completion_tokens\":42}}\n\n"))
		f.Flush()
		w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer srv.Close()

	s := Run(context.Background(), Request{Kind: core.KindVLLM, Base: srv.URL, Model: "m"})
	if !s.OK || s.Err != "" {
		t.Fatalf("probe failed: %+v", s)
	}
	if s.Tokens != 42 { // usage wins over delta counting
		t.Errorf("tokens = %d, want 42", s.Tokens)
	}
	if s.TTFTms <= 0 {
		t.Errorf("ttft not measured: %v", s.TTFTms)
	}
	if s.TokPS <= 0 {
		t.Errorf("tokps not derived: %v", s.TokPS)
	}
}

func TestRunOllamaStream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.Write([]byte(
			`{"response":"one","done":false}` + "\n" +
				`{"response":"two","done":false}` + "\n" +
				`{"response":"","done":true,"eval_count":9,"eval_duration":3000000000}` + "\n"))
	}))
	defer srv.Close()

	s := Run(context.Background(), Request{Kind: core.KindOllama, Base: srv.URL, Model: "m"})
	if !s.OK {
		t.Fatalf("probe failed: %+v", s)
	}
	if s.Tokens != 9 {
		t.Errorf("tokens = %d, want eval_count 9", s.Tokens)
	}
	want := float64(9) / 3.0
	if diff := s.TokPS - want; diff > 0.01 || diff < -0.01 {
		t.Errorf("tokps = %v, want ~%v", s.TokPS, want)
	}
}

func TestRunHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	s := Run(context.Background(), Request{Kind: core.KindVLLM, Base: srv.URL, Model: "m"})
	if s.OK || s.Err == "" {
		t.Fatalf("expected failure sample, got %+v", s)
	}
}

// Engines explain rejections in the error body ("model not found", bad api
// key, OOM); the surfaced Err must carry that text, not just the status.
func TestRunHTTPErrorCarriesEngineBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, "{\"error\":\"model 'm' not found, try pulling it first\"}")
	}))
	defer srv.Close()

	s := Run(context.Background(), Request{Kind: core.KindVLLM, Base: srv.URL, Model: "m"})
	if s.OK || s.Err == "" {
		t.Fatalf("expected failure sample, got %+v", s)
	}
	if !strings.Contains(s.Err, "400") {
		t.Errorf("err missing status: %q", s.Err)
	}
	if !strings.Contains(s.Err, "not found") {
		t.Errorf("err missing engine explanation: %q", s.Err)
	}
}

func TestRunEmptyStream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer srv.Close()

	s := Run(context.Background(), Request{Kind: core.KindVLLM, Base: srv.URL, Model: "m"})
	if s.OK || s.Err == "" {
		t.Fatalf("empty stream should fail, got %+v", s)
	}
}

// An Ollama engine closing the stream without tokens (crashed model, OOM)
// must surface as a failed probe, not a silent zero-throughput success.
func TestRunOllamaEmptyStream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"response":"","done":true}` + "\n"))
	}))
	defer srv.Close()

	s := Run(context.Background(), Request{Kind: core.KindOllama, Base: srv.URL, Model: "m"})
	if s.OK || s.Err == "" {
		t.Fatalf("empty ollama stream should fail, got %+v", s)
	}
}

// Ollama streams mid-stream failures as {"error":…} NDJSON lines with HTTP
// 200. Counting them as tokens reported a green probe with invented TTFT and
// throughput; the engine's own explanation must surface as Err instead.
func TestRunOllamaStreamError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"error":"model 'm' requires more system memory (18 GiB) than is available"}` + "\n"))
	}))
	defer srv.Close()

	s := Run(context.Background(), Request{Kind: core.KindOllama, Base: srv.URL, Model: "m"})
	if s.OK || s.Err == "" {
		t.Fatalf("streamed engine error should fail the probe, got %+v", s)
	}
	if !strings.Contains(s.Err, "more system memory") {
		t.Errorf("err missing engine explanation: %q", s.Err)
	}
}

// Keep-alive frames without content carry no tokens: counting every non-done
// line fabricated throughput out of empty chunks.
func TestRunOllamaEmptyChunksAreNotTokens(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(
			`{"response":"","done":false}` + "\n" +
				`{"response":"","done":true}` + "\n"))
	}))
	defer srv.Close()

	s := Run(context.Background(), Request{Kind: core.KindOllama, Base: srv.URL, Model: "m"})
	if s.OK || s.Err == "" {
		t.Fatalf("contentless stream should fail, got %+v", s)
	}
}

// OpenAI-compatible gateways (llama.cpp, LiteLLM) emit SSE error events with
// HTTP 200. Arriving after real tokens they previously passed as a partial
// success; the generation failed and must be reported as such.
func TestRunOpenAIStreamErrorObject(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"He\"}}]}\n\n" +
			"data: {\"error\":{\"message\":\"context length exceeded\"}}\n\n" +
			"data: [DONE]\n\n"))
	}))
	defer srv.Close()

	s := Run(context.Background(), Request{Kind: core.KindVLLM, Base: srv.URL, Model: "m"})
	if s.OK || s.Err == "" {
		t.Fatalf("streamed error event should fail the probe, got %+v", s)
	}
	if !strings.Contains(s.Err, "context length exceeded") {
		t.Errorf("err missing engine explanation: %q", s.Err)
	}
}

// The string flavor of SSE error events must be recognized too.
func TestRunOpenAIStreamErrorString(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("data: {\"error\":\"quota exceeded\"}\n\n" +
			"data: [DONE]\n\n"))
	}))
	defer srv.Close()

	s := Run(context.Background(), Request{Kind: core.KindVLLM, Base: srv.URL, Model: "m"})
	if s.OK || s.Err == "" {
		t.Fatalf("streamed error event should fail the probe, got %+v", s)
	}
	if !strings.Contains(s.Err, "quota exceeded") {
		t.Errorf("err missing engine explanation: %q", s.Err)
	}
}

// A usage-bearing chunk with an explicit null error member is a normal final
// frame, not a failure.
func TestRunOpenAINullErrorIsNotFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}],\"error\":null}\n\n" +
			"data: {\"usage\":{\"completion_tokens\":2}}\n\n" +
			"data: [DONE]\n\n"))
	}))
	defer srv.Close()

	s := Run(context.Background(), Request{Kind: core.KindVLLM, Base: srv.URL, Model: "m"})
	if !s.OK || s.Tokens != 2 {
		t.Fatalf("null error must not fail the probe, got %+v", s)
	}
}

// An empty or whitespace model id must not POST: some engines treat "" as
// "load the default", which is VRAM and (on a billed gateway) tokens.
func TestRunEmptyModelDoesNotPost(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		hits++
	}))
	defer srv.Close()

	for _, model := range []string{"", "  ", "\t"} {
		s := Run(context.Background(), Request{Kind: core.KindVLLM, Base: srv.URL, Model: model})
		if s.OK || s.Err == "" {
			t.Fatalf("model %q: expected failure sample, got %+v", model, s)
		}
		if !strings.Contains(s.Err, "no model") {
			t.Errorf("model %q err = %q, want no model", model, s.Err)
		}
	}
	if hits != 0 {
		t.Fatalf("engine was hit %d times, want 0", hits)
	}
}

// Engine-supplied model ids are untrusted and can be megabytes from
// /v1/models. The request must carry a capped id, never the raw string.
func TestRunCapsModelName(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&got)
		w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer srv.Close()

	huge := strings.Repeat("m", ModelNameMax+64)
	Run(context.Background(), Request{Kind: core.KindVLLM, Base: srv.URL, Model: huge})
	name, _ := got["model"].(string)
	if name != strings.Repeat("m", ModelNameMax) {
		t.Errorf("model id len = %d, want %d", len(name), ModelNameMax)
	}
}

func TestRunRequestsBoundedGeneration(t *testing.T) {
	var gotOpenAI map[string]any
	openai := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&gotOpenAI)
		w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer openai.Close()
	Run(context.Background(), Request{Kind: core.KindVLLM, Base: openai.URL, Model: "m"})
	if n := gotOpenAI["max_tokens"]; n != float64(probeTokens) {
		t.Errorf("openai max_tokens = %v, want %d", n, probeTokens)
	}

	var gotOllama map[string]any
	ollama := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&gotOllama)
		w.Write([]byte(`{"response":"","done":true}` + "\n"))
	}))
	defer ollama.Close()
	Run(context.Background(), Request{Kind: core.KindOllama, Base: ollama.URL, Model: "m"})
	opts := gotOllama["options"].(map[string]any)
	if n := opts["num_predict"]; n != float64(probeTokens) {
		t.Errorf("ollama num_predict = %v, want %d", n, probeTokens)
	}
}

// Engines that ignore max_tokens keep streaming; the client must hang up
// after probeTokens content frames so a billed gateway cannot run until the
// HTTP timeout.
func TestRunOpenAIStopsAfterProbeTokens(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		f := http.NewResponseController(w)
		for range probeTokens * 8 {
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":\"x\"}}]}\n\n")
			f.Flush()
		}
	}))
	defer srv.Close()

	s := Run(context.Background(), Request{Kind: core.KindVLLM, Base: srv.URL, Model: "m"})
	if !s.OK {
		t.Fatalf("probe failed: %+v", s)
	}
	if s.Tokens != probeTokens {
		t.Errorf("tokens = %d, want client cap %d", s.Tokens, probeTokens)
	}
}

// A single huge content delta counts as one frame, so the frame cap would
// not hang up. Byte budget must.
func TestRunOpenAIStopsOnContentBytes(t *testing.T) {
	payload := strings.Repeat("x", probeContentBytes+8)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		f := http.NewResponseController(w)
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q}}]}\n\n", payload)
		f.Flush()
		for range 8 {
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":\"more\"}}]}\n\n")
			f.Flush()
		}
	}))
	defer srv.Close()

	s := Run(context.Background(), Request{Kind: core.KindVLLM, Base: srv.URL, Model: "m"})
	if !s.OK {
		t.Fatalf("probe failed: %+v", s)
	}
	if s.Tokens != 1 {
		t.Errorf("tokens = %d, want hang-up after the first oversized frame", s.Tokens)
	}
}

func TestRunOllamaStopsOnContentBytes(t *testing.T) {
	payload := strings.Repeat("x", probeContentBytes+8)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		f := http.NewResponseController(w)
		fmt.Fprintf(w, `{"response":%q,"done":false}`+"\n", payload)
		f.Flush()
		for range 8 {
			fmt.Fprintf(w, `{"response":"more","done":false}`+"\n")
			f.Flush()
		}
	}))
	defer srv.Close()

	s := Run(context.Background(), Request{Kind: core.KindOllama, Base: srv.URL, Model: "m"})
	if !s.OK {
		t.Fatalf("probe failed: %+v", s)
	}
	if s.Tokens != 1 {
		t.Errorf("tokens = %d, want hang-up after the first oversized frame", s.Tokens)
	}
}

func TestRunOllamaStopsAfterProbeTokens(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		f := http.NewResponseController(w)
		for range probeTokens * 8 {
			fmt.Fprintf(w, `{"response":"x","done":false}`+"\n")
			f.Flush()
		}
		fmt.Fprintf(w, `{"response":"","done":true,"eval_count":999,"eval_duration":1000000}`+"\n")
	}))
	defer srv.Close()

	s := Run(context.Background(), Request{Kind: core.KindOllama, Base: srv.URL, Model: "m"})
	if !s.OK {
		t.Fatalf("probe failed: %+v", s)
	}
	if s.Tokens != probeTokens {
		t.Errorf("tokens = %d, want client cap %d", s.Tokens, probeTokens)
	}
}

// A usage field far past the requested generation is engine junk, not a
// measurement. Fall back to the content frames actually observed.
func TestRunOpenAIRejectsUnboundedUsage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n" +
			"data: {\"usage\":{\"completion_tokens\":1000000000}}\n\n" +
			"data: [DONE]\n\n"))
	}))
	defer srv.Close()

	s := Run(context.Background(), Request{Kind: core.KindVLLM, Base: srv.URL, Model: "m"})
	if !s.OK || s.Tokens != 1 {
		t.Fatalf("unbounded usage must not win, got %+v", s)
	}
}

func TestRunOllamaRejectsUnboundedEvalCount(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(
			`{"response":"one","done":false}` + "\n" +
				`{"response":"two","done":false}` + "\n" +
				`{"response":"","done":true,"eval_count":1000000000,"eval_duration":1}` + "\n"))
	}))
	defer srv.Close()

	s := Run(context.Background(), Request{Kind: core.KindOllama, Base: srv.URL, Model: "m"})
	if !s.OK || s.Tokens != 2 {
		t.Fatalf("unbounded eval_count must not win, got %+v", s)
	}
}

// Usage on the last content chunk must replace the delta count, not add to it.
func TestRunOpenAIUsageNotStackedOnContent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}],\"usage\":{\"completion_tokens\":4}}\n\n" +
			"data: [DONE]\n\n"))
	}))
	defer srv.Close()

	s := Run(context.Background(), Request{Kind: core.KindVLLM, Base: srv.URL, Model: "m"})
	if !s.OK || s.Tokens != 4 {
		t.Fatalf("usage must replace delta count, got %+v", s)
	}
}

// A mid-stream transport drop after the first token still yields a sample:
// throwing it away would hide TTFT already measured.
func TestRunOpenAIKeepsPartialOnReadError(t *testing.T) {
	old := client.Timeout
	client.Timeout = 200 * time.Millisecond
	defer func() { client.Timeout = old }()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		f := http.NewResponseController(w)
		w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n"))
		f.Flush()
		time.Sleep(time.Second)
	}))
	defer srv.Close()

	s := Run(context.Background(), Request{Kind: core.KindVLLM, Base: srv.URL, Model: "m"})
	if !s.OK || s.Tokens < 1 {
		t.Fatalf("partial stream should still sample, got %+v", s)
	}
}

// Shutdown must not mint a success from a stream cancelled under us.
func TestRunCanceledContextFails(t *testing.T) {
	started := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		f := http.NewResponseController(w)
		w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n"))
		f.Flush()
		close(started)
		<-r.Context().Done()
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-started
		cancel()
	}()
	s := Run(ctx, Request{Kind: core.KindVLLM, Base: srv.URL, Model: "m"})
	if s.OK {
		t.Fatalf("canceled probe should fail, got %+v", s)
	}
}
