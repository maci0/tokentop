// Package ingest runs a tiny localhost HTTP endpoint so agents, harnesses and
// scripts can push token-usage events that tokentop renders live.
package ingest

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"tokentop/internal/core"
)

// Server accepts POST /v1/events (single object or newline-delimited stream)
// and GET /v1/events for debugging.
type Server struct {
	rec  Recorder
	srv  http.Server
	ln   net.Listener
	addr string
}

// Recorder is the sink for incoming events.
type Recorder interface {
	RecordAgent(ev core.AgentEvent)
}

// idleTimeout reaps keep-alive connections that sit between requests. Both
// timeouts zero would let vanished peers hold an fd and a goroutine apiece
// for the life of the dashboard; the endpoint is localhost-bound by default
// but can be exposed via --ingest.
var idleTimeout = 2 * time.Minute

func New(addr string, rec Recorder) (*Server, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	s := &Server{rec: rec, ln: ln, addr: ln.Addr().String()}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/events", s.handlePost)
	mux.HandleFunc("GET /v1/events", s.handleGet)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "ok")
	})
	s.srv = http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       idleTimeout,
	}
	return s, nil
}

// Addr returns the actual bound address (useful when starting on :0).
func (s *Server) Addr() string { return s.addr }

func (s *Server) Serve() error {
	err := s.srv.Serve(s.ln)
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

func (s *Server) Close() error { return s.srv.Close() }

// maxEventBody caps one POST (single object or NDJSON stream). Legit events
// are tiny; without a cap a client can stream unbounded bytes into the
// decode loop for as long as the connection stays up.
const maxEventBody = 1 << 20

func (s *Server) handlePost(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxEventBody)
	dec := json.NewDecoder(r.Body)
	defer r.Body.Close()
	n := 0
	for {
		var ev core.AgentEvent
		if err := dec.Decode(&ev); err != nil {
			if n > 0 && errors.Is(err, io.EOF) {
				break // clean end of stream after at least one event
			}
			http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
			return
		}
		if ev.Agent == "" {
			ev.Agent = "anonymous"
		}
		// Event fields are attacker-shaped text (any local process or peer
		// able to reach this endpoint): strip terminal escape sequences and
		// control characters before the values are stored and later rendered.
		ev.Agent = clampField(core.SanitizeText(ev.Agent), 64)
		ev.Model = clampField(core.SanitizeText(ev.Model), 128)
		ev.Note = clampField(core.SanitizeText(ev.Note), 512) // free-form fields are capped so one giant event cannot dominate the retained feed
		// Token counts are unsigned quantities; negative values are junk
		// from a misbehaving sender and must not enter the retained feed.
		if ev.PromptTokens < 0 {
			ev.PromptTokens = 0
		}
		if ev.OutputTokens < 0 {
			ev.OutputTokens = 0
		}
		switch ev.Kind {
		case "":
			ev.Kind = "turn"
		case "turn", "tool", "error", "note":
		default:
			ev.Kind = clampField(strings.ToLower(ev.Kind), 24)
		}
		if ev.At.IsZero() {
			ev.At = time.Now()
		}
		s.rec.RecordAgent(ev)
		n++
	}
	w.WriteHeader(http.StatusAccepted)
	fmt.Fprintf(w, `{"accepted":%d}`+"\n", n)
}

func (s *Server) handleGet(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintln(w, `{"hint":"POST /v1/events with {agent,kind,model,prompt_tokens,output_tokens,note}"}`)
}

// clampField caps a free-form event field at n runes. Events are retained
// (count-capped) for the process lifetime, so unbounded strings would let a
// single oversized event pin memory until count-evicted.
func clampField(s string, n int) string {
	if len(s) <= n { // fast path: ASCII within cap, no scan
		return s
	}
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}
