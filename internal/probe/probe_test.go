package probe

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"tokentop/internal/core"
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
