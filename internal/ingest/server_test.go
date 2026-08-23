package ingest

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"tokentop/internal/core"
)

type memRecorder struct{ evs []core.AgentEvent }

func (m *memRecorder) RecordAgent(ev core.AgentEvent) { m.evs = append(m.evs, ev) }

func post(t *testing.T, url, body string) *http.Response {
	t.Helper()
	resp, err := http.Post(url, "application/json", bytes.NewBufferString(body))
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestIngestAcceptsEvents(t *testing.T) {
	rec := &memRecorder{}
	s, err := New("127.0.0.1:0", rec)
	if err != nil {
		t.Fatal(err)
	}
	go s.Serve()
	defer s.Close()

	resp := post(t, "http://"+s.Addr()+"/v1/events",
		`{"agent":"coder","kind":"tool","prompt_tokens":100,"output_tokens":5}`+"\n"+
			`{"agent":"coder","kind":"turn","output_tokens":50}`+"\n")
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	deadline := time.Now().Add(time.Second)
	for len(rec.evs) < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if len(rec.evs) != 2 {
		t.Fatalf("events = %d, want 2", len(rec.evs))
	}
	if rec.evs[0].Agent != "coder" || rec.evs[0].Kind != "tool" {
		t.Errorf("event 0 = %+v", rec.evs[0])
	}
	if !rec.evs[1].At.After(time.Time{}) {
		t.Error("server must stamp missing timestamps")
	}
}

func TestIngestDefaultsAndBadJSON(t *testing.T) {
	rec := &memRecorder{}
	s, _ := New("127.0.0.1:0", rec)
	go s.Serve()
	defer s.Close()
	base := "http://" + s.Addr() + "/v1/events"

	resp := post(t, base, `{"prompt_tokens":1}`)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("anonymous event rejected: %d", resp.StatusCode)
	}
	if rec.evs[0].Agent != "anonymous" || rec.evs[0].Kind != "turn" {
		t.Errorf("defaults not applied: %+v", rec.evs[0])
	}

	resp = post(t, base, `{not json`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad json status = %d", resp.StatusCode)
	}
}

func TestHealthz(t *testing.T) {
	s, _ := New("127.0.0.1:0", &memRecorder{})
	go s.Serve()
	defer s.Close()
	resp, err := http.Get("http://" + s.Addr() + "/healthz")
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("healthz: %v %v", err, resp)
	}
	var body map[string]string
	json.NewDecoder(resp.Body).Decode(&body)
	resp.Body.Close()
}
