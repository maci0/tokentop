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
	s.srv = http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
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

func (s *Server) handlePost(w http.ResponseWriter, r *http.Request) {
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
		switch ev.Kind {
		case "":
			ev.Kind = "turn"
		case "turn", "tool", "error", "note":
		default:
			ev.Kind = strings.ToLower(ev.Kind)
			if len(ev.Kind) > 24 {
				ev.Kind = ev.Kind[:24]
			}
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
