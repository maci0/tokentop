package ingest

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"tokentop/internal/core"
)

type memRecorder struct{ evs []core.AgentEvent }

func (m *memRecorder) RecordAgent(ev core.AgentEvent) { m.evs = append(m.evs, ev) }

// post sends body and returns the status code, draining the response so
// keep-alive connections are reusable.
func post(t *testing.T, url, body string) int {
	t.Helper()
	resp, err := http.Post(url, "application/json", bytes.NewBufferString(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
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
	if resp != http.StatusAccepted {
		t.Fatalf("status = %d", resp)
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
	if resp != http.StatusAccepted {
		t.Fatalf("anonymous event rejected: %d", resp)
	}
	if rec.evs[0].Agent != "anonymous" || rec.evs[0].Kind != "turn" {
		t.Errorf("defaults not applied: %+v", rec.evs[0])
	}

	resp = post(t, base, `{not json`)
	if resp != http.StatusBadRequest {
		t.Fatalf("bad json status = %d", resp)
	}
}

// A body beyond the size cap is a volume problem, not a JSON problem: it
// must answer 413 so senders can tell "trim the payload" from "fix the
// encoding", instead of reading `bad json` on a perfectly encoded stream.
func TestIngestRejectsOversizedBody(t *testing.T) {
	rec := &memRecorder{}
	s, _ := New("127.0.0.1:0", rec)
	go s.Serve()
	defer s.Close()

	// The body must stay well formed up to the cap, or decoding fails with a
	// syntax error before the limit is ever reached: one event whose string
	// field runs past maxEventBody trips the size cap mid-value instead.
	body := `{"agent":"` + strings.Repeat("a", maxEventBody) + `"}`
	r := httptest.NewRequest(http.MethodPost, "/v1/events", strings.NewReader(body))
	w := httptest.NewRecorder()
	s.handlePost(w, r)

	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized body status = %d, want 413", w.Code)
	}
	if !strings.Contains(w.Body.String(), "exceeds") {
		t.Errorf("body should state the cap, got %q", w.Body.String())
	}
	if len(rec.evs) != 0 {
		t.Fatal("event from oversized body recorded")
	}
}

// Token counts are unsigned quantities; a sender pushing negatives must not
// plant junk values in the retained feed.
func TestIngestClampsNegativeTokenCounts(t *testing.T) {
	rec := &memRecorder{}
	s, _ := New("127.0.0.1:0", rec)
	go s.Serve()
	defer s.Close()

	resp := post(t, "http://"+s.Addr()+"/v1/events",
		`{"agent":"buggy","prompt_tokens":-500,"output_tokens":-99999999999}`)
	if resp != http.StatusAccepted {
		t.Fatalf("status = %d", resp)
	}
	deadline := time.Now().Add(time.Second)
	for len(rec.evs) < 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if len(rec.evs) != 1 {
		t.Fatal("event not recorded")
	}
	if rec.evs[0].PromptTokens != 0 || rec.evs[0].OutputTokens != 0 {
		t.Errorf("negative token counts retained: %+v", rec.evs[0])
	}
}

func TestHealthz(t *testing.T) {
	s, _ := New("127.0.0.1:0", &memRecorder{})
	go s.Serve()
	defer s.Close()
	resp, err := http.Get("http://" + s.Addr() + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || string(body) != "ok" {
		t.Fatalf("healthz = %d %q, want 200 ok", resp.StatusCode, body)
	}
}

func TestIngestClampsOversizedFields(t *testing.T) {
	rec := &memRecorder{}
	s, _ := New("127.0.0.1:0", rec)
	go s.Serve()
	defer s.Close()

	huge := strings.Repeat("x", 10_000)
	body := fmt.Sprintf(`{"agent":%q,"model":%q,"note":%q}`, huge, huge, huge)
	resp := post(t, "http://"+s.Addr()+"/v1/events", body)
	if resp != http.StatusAccepted {
		t.Fatalf("status = %d", resp)
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

// kind is attacker-shaped text like every other event field: escape
// sequences must be stripped before the value enters the retained feed.
func TestIngestSanitizesCustomKind(t *testing.T) {
	rec := &memRecorder{}
	s, _ := New("127.0.0.1:0", rec)
	go s.Serve()
	defer s.Close()

	resp := post(t, "http://"+s.Addr()+"/v1/events",
		`{"agent":"x","kind":"\u001b]0;pwned\u0007weird"}`)
	if resp != http.StatusAccepted {
		t.Fatalf("status = %d", resp)
	}
	deadline := time.Now().Add(time.Second)
	for len(rec.evs) < 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if len(rec.evs) != 1 {
		t.Fatal("event not recorded")
	}
	if got := rec.evs[0].Kind; strings.ContainsRune(got, 0x1b) || strings.ContainsRune(got, 0x07) {
		t.Errorf("kind retained escape sequences: %q", got)
	}
}

// startPost opens a POST whose body framing allows slow streaming: chunked
// encoding, terminated by a zero chunk.
func startPost(t *testing.T, addr string) net.Conn {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintf(conn, "POST /v1/events HTTP/1.1\r\nHost: %s\r\nContent-Type: application/json\r\nTransfer-Encoding: chunked\r\n\r\n", addr)
	return conn
}

func sendChunk(t *testing.T, conn net.Conn, data string) {
	t.Helper()
	fmt.Fprintf(conn, "%x\r\n%s\r\n", len(data), data)
}

// readResponse drains until the status line plus any following lines arrive
// or the deadline expires, returning everything received.
func readResponse(t *testing.T, conn net.Conn, within time.Duration) string {
	t.Helper()
	buf := make([]byte, 8192)
	var out []byte
	conn.SetReadDeadline(time.Now().Add(within))
	for !bytes.Contains(out, []byte("\r\n")) {
		n, err := conn.Read(buf)
		out = append(out, buf[:n]...)
		if err != nil {
			break
		}
	}
	return string(out)
}

// A sender that stalls mid-body must not pin the connection: the idle
// deadline reaps it with a 408 instead of holding fd and goroutine forever.
func TestIngestReapsStalledBody(t *testing.T) {
	oldLife, oldIdle := maxEventLifetime, bodyIdleTimeout
	maxEventLifetime, bodyIdleTimeout = time.Minute, 150*time.Millisecond
	t.Cleanup(func() { maxEventLifetime, bodyIdleTimeout = oldLife, oldIdle })

	rec := &memRecorder{}
	s, _ := New("127.0.0.1:0", rec)
	go s.Serve()
	defer s.Close()

	conn := startPost(t, s.Addr())
	defer conn.Close()
	sendChunk(t, conn, `{"agent":"slow"`) // valid prefix, then silence
	resp := readResponse(t, conn, 5*time.Second)
	if !strings.HasPrefix(resp, "HTTP/1.1 408") {
		t.Fatalf("stalled body response = %q, want 408", resp)
	}
}

// A slow but progressing NDJSON stream stays under the idle deadline and
// must be accepted in full.
func TestIngestAcceptsSlowProgressingStream(t *testing.T) {
	oldLife, oldIdle := maxEventLifetime, bodyIdleTimeout
	maxEventLifetime, bodyIdleTimeout = 30*time.Second, 500*time.Millisecond
	t.Cleanup(func() { maxEventLifetime, bodyIdleTimeout = oldLife, oldIdle })

	rec := &memRecorder{}
	s, _ := New("127.0.0.1:0", rec)
	go s.Serve()
	defer s.Close()

	conn := startPost(t, s.Addr())
	defer conn.Close()
	for _, ev := range []string{
		`{"agent":"drip","prompt_tokens":1}` + "\n",
		`{"agent":"drip","output_tokens":2}` + "\n",
	} {
		sendChunk(t, conn, ev)
		time.Sleep(100 * time.Millisecond) // well inside the idle window
	}
	fmt.Fprint(conn, "0\r\n\r\n") // end of chunks
	resp := readResponse(t, conn, 5*time.Second)
	if !strings.HasPrefix(resp, "HTTP/1.1 202") {
		t.Fatalf("progressing stream response = %q, want 202", resp)
	}
	deadline := time.Now().Add(time.Second)
	for len(rec.evs) < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if len(rec.evs) != 2 {
		t.Fatalf("events = %d, want 2", len(rec.evs))
	}
}

// Progress alone must not extend a POST forever: past the absolute lifetime
// the connection is cut even while bytes keep trickling in.
func TestIngestCutsBodyPastAbsoluteLifetime(t *testing.T) {
	oldLife, oldIdle := maxEventLifetime, bodyIdleTimeout
	maxEventLifetime, bodyIdleTimeout = 300*time.Millisecond, time.Minute // idle longer than life
	t.Cleanup(func() { maxEventLifetime, bodyIdleTimeout = oldLife, oldIdle })

	rec := &memRecorder{}
	s, _ := New("127.0.0.1:0", rec)
	go s.Serve()
	defer s.Close()

	conn := startPost(t, s.Addr())
	defer conn.Close()
	done := make(chan string, 1)
	go func() {
		done <- readResponse(t, conn, 10*time.Second)
	}()
	keepalive := time.NewTicker(50 * time.Millisecond) // steady progress
	defer keepalive.Stop()
	timeout := time.After(8 * time.Second)
	for {
		select {
		case resp := <-done:
			if !strings.Contains(resp, "408") {
				t.Fatalf("lifetime-capped body response = %q, want 408", resp)
			}
			return
		case <-timeout:
			t.Fatal("absolute lifetime did not bound a progressing body")
		case <-keepalive.C:
			sendChunk(t, conn, "\n")
		}
	}
}
