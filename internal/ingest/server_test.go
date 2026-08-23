package ingest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

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

func TestIngestClampsOversizedFields(t *testing.T) {
	rec := &memRecorder{}
	s, _ := New("127.0.0.1:0", rec)
	go s.Serve()
	defer s.Close()

	huge := strings.Repeat("x", 10_000)
	body := fmt.Sprintf(`{"agent":%q,"model":%q,"note":%q}`, huge, huge, huge)
	resp := post(t, "http://"+s.Addr()+"/v1/events", body)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	deadline := time.Now().Add(time.Second)
	for len(rec.evs) < 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if len(rec.evs) != 1 {
		t.Fatal("event not recorded")
	}
	ev := rec.evs[0]
	if len(ev.Agent) > 64 || len(ev.Model) > 128 || utf8.RuneCountInString(ev.Note) > 512 {
		t.Errorf("oversized fields retained: agent=%d model=%d note=%d",
			len(ev.Agent), len(ev.Model), utf8.RuneCountInString(ev.Note))
	}
}

// Keep-alive connections idle between requests must be reaped; otherwise
// vanished peers hold an fd and a goroutine each for the process lifetime.
func TestIdleKeepAliveConnsReaped(t *testing.T) {
	old := idleTimeout
	idleTimeout = 50 * time.Millisecond
	t.Cleanup(func() { idleTimeout = old })

	s, err := New("127.0.0.1:0", &memRecorder{})
	if err != nil {
		t.Fatal(err)
	}
	go s.Serve()
	defer s.Close()

	conn, err := net.Dial("tcp", s.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprintf(conn, "GET /healthz HTTP/1.1\r\nHost: %s\r\nConnection: keep-alive\r\n\r\n", s.Addr())

	buf := make([]byte, 4096)
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	for {
		_, err = conn.Read(buf)
		if err == io.EOF {
			return // server closed the now-idle connection
		}
		if ne, ok := err.(net.Error); ok && ne.Timeout() {
			t.Fatal("idle keep-alive connection still open after idleTimeout")
		}
		if err != nil {
			t.Fatalf("read: %v", err)
		}
	}
}
