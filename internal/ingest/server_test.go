package ingest

import (
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/rivo/uniseg"

	"github.com/maci0/toktop/internal/core"
)

type memRecorder struct{ evs []core.AgentEvent }

func (m *memRecorder) RecordAgent(ev core.AgentEvent) { m.evs = append(m.evs, ev) }

// post sends body and returns the status code, draining the response so
// keep-alive connections are reusable.
func post(t *testing.T, url, body string) int {
	t.Helper()
	code, _ := postBody(t, url, body)
	return code
}

// postBody sends body and returns the status code plus the response text,
// draining the response so keep-alive connections are reusable.
func postBody(t *testing.T, url, body string) (int, string) {
	t.Helper()
	resp, err := http.Post(url, "application/json", bytes.NewBufferString(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(b)
}

// startIngest serves on an ephemeral port backed by rec and closes it at
// cleanup.
func startIngest(t *testing.T, rec *memRecorder) *Server {
	t.Helper()
	return startIngestLog(t, rec, slog.New(slog.DiscardHandler))
}

// awaitEvents waits up to a second for rec to hold n events, failing if they
// never arrive.
func awaitEvents(t *testing.T, rec *memRecorder, n int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for len(rec.evs) < n && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if len(rec.evs) != n {
		t.Fatalf("events = %d, want %d", len(rec.evs), n)
	}
}

func TestIngestAcceptsEvents(t *testing.T) {
	rec := &memRecorder{}
	s := startIngest(t, rec)

	resp := post(t, "http://"+s.Addr()+"/v1/events",
		`{"agent":"coder","kind":"tool","prompt_tokens":100,"output_tokens":5,"thinking_tokens":2}`+"\n"+
			`{"agent":"coder","kind":"turn","output_tokens":50}`+"\n")
	if resp != http.StatusAccepted {
		t.Fatalf("status = %d", resp)
	}
	awaitEvents(t, rec, 2)
	ev0 := rec.evs[0]
	if ev0.Agent != "coder" || ev0.Kind != "tool" ||
		ev0.PromptTokens != 100 || ev0.OutputTokens != 5 || ev0.ThinkingTokens != 2 {
		t.Errorf("event 0 = %+v, want coder/tool 100/5/2", ev0)
	}
	ev1 := rec.evs[1]
	if ev1.Agent != "coder" || ev1.Kind != "turn" || ev1.OutputTokens != 50 ||
		ev1.PromptTokens != 0 || ev1.ThinkingTokens != 0 {
		t.Errorf("event 1 = %+v, want coder/turn 0/50/0", ev1)
	}
	if !ev1.At.After(time.Time{}) {
		t.Error("server must stamp missing timestamps")
	}
}

// An id is a caller-chosen key for retries: it must survive ingest as sent
// (after sanitization) so the collector can ignore a replay of the same event.
func TestIngestForwardsId(t *testing.T) {
	rec := &memRecorder{}
	s := startIngest(t, rec)

	resp := post(t, "http://"+s.Addr()+"/v1/events",
		`{"id":"turn-1","agent":"coder","output_tokens":50}`)
	if resp != http.StatusAccepted {
		t.Fatalf("status = %d", resp)
	}
	awaitEvents(t, rec, 1)
	if rec.evs[0].ID != "turn-1" {
		t.Errorf("id = %q, want turn-1", rec.evs[0].ID)
	}
}

// via_engine is the same attribution agentwatch stamps when an agent is
// generating through a monitored engine: without it, a harness POST would
// double-count those tokens in header and chart totals.
func TestIngestForwardsViaEngine(t *testing.T) {
	rec := &memRecorder{}
	s := startIngest(t, rec)

	resp := post(t, "http://"+s.Addr()+"/v1/events",
		`{"agent":"coder","output_tokens":50,"via_engine":"127.0.0.1:11434"}`)
	if resp != http.StatusAccepted {
		t.Fatalf("status = %d", resp)
	}
	awaitEvents(t, rec, 1)
	if rec.evs[0].ViaEngine != "127.0.0.1:11434" {
		t.Errorf("via_engine = %q, want 127.0.0.1:11434", rec.evs[0].ViaEngine)
	}
}

// Offset-less RFC 3339 stamps (Python datetime.isoformat() without tzinfo,
// hand-rolled harnesses) decode as UTC: encoding/json alone would reject
// them and drop every event queued behind the bad one in the same batch.
// Offset-aware stamps keep their zone; all forms must land on the instant.
func TestIngestAcceptsNaiveTimestampsAsUTC(t *testing.T) {
	rec := &memRecorder{}
	s := startIngest(t, rec)

	resp := post(t, "http://"+s.Addr()+"/v1/events",
		`{"agent":"py","ts":"2026-01-02T03:04:05"}`+"\n"+
			`{"agent":"py","ts":"2026-01-02T03:04:05.123456"}`+"\n"+
			`{"agent":"rfc","ts":"2026-01-02T05:04:05+02:00"}`+"\n")
	if resp != http.StatusAccepted {
		t.Fatalf("status = %d", resp)
	}
	awaitEvents(t, rec, 3)
	want := time.Date(2026, 1, 2, 3, 4, 5, 123456000, time.UTC)
	if got := rec.evs[0].At; !got.Equal(want.Truncate(time.Second)) || got.Location() != time.UTC {
		t.Errorf("naive stamp = %v (%v), want 03:04:05 UTC", got, got.Location())
	}
	if got := rec.evs[1].At; !got.Equal(want) {
		t.Errorf("naive fractional stamp = %v, want %v", got, want)
	}
	if got := rec.evs[2].At; !got.Equal(want.Truncate(time.Second)) {
		t.Errorf("+02:00 stamp = %v, want same instant as %v", got, want)
	}
}

// Colon-less numeric offsets (`date '+%Y-%m-%dT%H:%M:%S%z'` -> -0700) are
// ISO 8601 but not RFC 3339. They used to fail every accepted layout and
// abort the NDJSON batch with 400, dropping every event queued behind the
// first one. They must land on the same instant as the colon form.
func TestIngestAcceptsColonlessNumericOffsets(t *testing.T) {
	rec := &memRecorder{}
	s := startIngest(t, rec)

	resp := post(t, "http://"+s.Addr()+"/v1/events",
		`{"agent":"date","ts":"2026-01-02T03:04:05-0700"}`+"\n"+
			`{"agent":"date","ts":"2026-01-02 03:04:05-0700"}`+"\n"+
			`{"agent":"rfc","ts":"2026-01-02T03:04:05-07:00"}`)
	if resp != http.StatusAccepted {
		t.Fatalf("status = %d", resp)
	}
	awaitEvents(t, rec, 3)
	want := time.Date(2026, 1, 2, 3, 4, 5, 0, time.FixedZone("", -7*3600))
	for i, ev := range rec.evs {
		if !ev.At.Equal(want) {
			t.Errorf("event %d At = %v, want same instant as %v", i, ev.At, want)
		}
	}
}

// SQL-style stamps separate date and time with a space (`date '+%F %T'`,
// SQLite and Postgres text output). One of them used to fail both accepted
// shapes and abort the whole NDJSON batch with 400, dropping every event
// queued behind it; they must parse on the same terms as T-separated ones:
// an offset is honored, its absence decodes as UTC.
func TestIngestAcceptsSpaceSeparatedTimestamps(t *testing.T) {
	rec := &memRecorder{}
	s := startIngest(t, rec)

	resp := post(t, "http://"+s.Addr()+"/v1/events",
		`{"agent":"sql","ts":"2026-01-02 03:04:05"}`+"\n"+
			`{"agent":"sql","ts":"2026-01-02 03:04:05.25"}`+"\n"+
			`{"agent":"pg","ts":"2026-01-02 05:04:05+02:00"}`)
	if resp != http.StatusAccepted {
		t.Fatalf("status = %d", resp)
	}
	awaitEvents(t, rec, 3)
	want := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	if got := rec.evs[0].At; !got.Equal(want) || got.Location() != time.UTC {
		t.Errorf("space-separated naive stamp = %v (%v), want 03:04:05 UTC", got, got.Location())
	}
	if got := rec.evs[1].At; !got.Equal(want.Add(250 * time.Millisecond)) {
		t.Errorf("fractional stamp = %v, want %v", got, want.Add(250*time.Millisecond))
	}
	if got := rec.evs[2].At; !got.Equal(want) {
		t.Errorf("+02:00 space-separated stamp = %v, want same instant as %v", got, want)
	}
}

// A ts that is neither RFC 3339 nor an offset-less variant stays a hard
// error so sender bugs surface instead of silently becoming "now".
func TestIngestRejectsGarbageTimestamp(t *testing.T) {
	rec := &memRecorder{}
	s := startIngest(t, rec)

	code, body := postBody(t, "http://"+s.Addr()+"/v1/events", `{"agent":"x","ts":"yesterday"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("garbage ts status = %d, want 400", code)
	}
	if len(rec.evs) != 0 {
		t.Fatal("garbage-timestamped event recorded")
	}
	if !strings.Contains(body, "ts") || strings.Contains(body, "2006-01-02") {
		t.Errorf("ts error should name the field, not the parser layout, got %q", body)
	}
}

// Type mistakes must name the JSON field (or the expected shape) so a harness
// can fix the payload. encoding/json's default text names the Go type.
func TestIngestJSONTypeErrorsNameTheField(t *testing.T) {
	s := startIngest(t, &memRecorder{})
	base := "http://" + s.Addr() + "/v1/events"
	cases := []struct {
		body string
		want []string
	}{
		{`[{"agent":"a"}]`, []string{"object", "NDJSON", "array"}},
		{`{"prompt_tokens":"100"}`, []string{"prompt_tokens", "integer"}},
		{`{"output_tokens":true}`, []string{"output_tokens", "integer"}},
		{`{"thinking_tokens":[1]}`, []string{"thinking_tokens", "integer"}},
		{`{"agent":["x"]}`, []string{"agent", "string"}},
		{`{"prompt_tokens":100.5}`, []string{"prompt_tokens", "integer"}},
		{`{"agent":"x","ts":123}`, []string{"ts"}},
	}
	for _, tc := range cases {
		code, body := postBody(t, base, tc.body)
		if code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", tc.body, code)
			continue
		}
		for _, w := range tc.want {
			if !strings.Contains(body, w) {
				t.Errorf("%s: body %q missing %q", tc.body, body, w)
			}
		}
		for _, leaked := range []string{"agentEventWire", "Go struct field", "Go value of type", "2006-01-02"} {
			if strings.Contains(body, leaked) {
				t.Errorf("%s: body %q leaked %q", tc.body, body, leaked)
			}
		}
	}
}

// Python json.dumps of a float emits 100.0; scientific notation is valid JSON.
// Both are whole numbers and must record as integers, not 400.
func TestIngestAcceptsWholeJSONNumberTokenCounts(t *testing.T) {
	rec := &memRecorder{}
	s := startIngest(t, rec)

	resp := post(t, "http://"+s.Addr()+"/v1/events",
		`{"agent":"py","prompt_tokens":100.0,"output_tokens":1e2,"thinking_tokens":3}`)
	if resp != http.StatusAccepted {
		t.Fatalf("status = %d", resp)
	}
	awaitEvents(t, rec, 1)
	if rec.evs[0].PromptTokens != 100 || rec.evs[0].OutputTokens != 100 || rec.evs[0].ThinkingTokens != 3 {
		t.Errorf("tokens = %+v", rec.evs[0])
	}
}

func TestIngestMethodNotAllowedSetsAllow(t *testing.T) {
	s := startIngest(t, &memRecorder{})

	req, err := http.NewRequest(http.MethodPut, "http://"+s.Addr()+"/v1/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", resp.StatusCode)
	}
	allow := resp.Header.Get("Allow")
	for _, m := range []string{http.MethodGet, http.MethodHead, http.MethodPost} {
		if !strings.Contains(allow, m) {
			t.Errorf("Allow = %q, missing %s", allow, m)
		}
	}
}

func TestIngestDefaultsAndBadJSON(t *testing.T) {
	rec := &memRecorder{}
	s := startIngest(t, rec)
	base := "http://" + s.Addr() + "/v1/events"

	resp := post(t, base, `{"prompt_tokens":1}`)
	if resp != http.StatusAccepted {
		t.Fatalf("anonymous event rejected: %d", resp)
	}
	awaitEvents(t, rec, 1)
	if rec.evs[0].Agent != "anonymous" || rec.evs[0].Kind != "turn" || rec.evs[0].PromptTokens != 1 {
		t.Errorf("defaults not applied: %+v", rec.evs[0])
	}

	resp = post(t, base, `{not json`)
	if resp != http.StatusBadRequest {
		t.Fatalf("bad json status = %d", resp)
	}
}

// An empty body carries no event at all; the error must say so instead of
// the cryptic decode-level "bad json: EOF".
func TestIngestEmptyBodyRejected(t *testing.T) {
	s := startIngest(t, &memRecorder{})

	code, body := postBody(t, "http://"+s.Addr()+"/v1/events", "")
	if code != http.StatusBadRequest {
		t.Fatalf("empty body status = %d, want 400", code)
	}
	if !strings.Contains(body, "empty body") {
		t.Errorf("body should name the problem, got %q", body)
	}
}

// Streams record incrementally: when a later line fails, events before it
// stay recorded and the error must say how many, so senders resume instead
// of replaying the whole stream and duplicating what was kept.
func TestIngestPartialStreamReportsRecordedCount(t *testing.T) {
	rec := &memRecorder{}
	s := startIngest(t, rec)

	code, body := postBody(t, "http://"+s.Addr()+"/v1/events",
		`{"agent":"kept"}`+"\n"+`{"agent":"dropped","ts":"yesterday"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", code)
	}
	if !strings.Contains(body, "; 1 earlier event in this stream was recorded") {
		t.Errorf("error should state the recorded count, got %q", body)
	}
	if len(rec.evs) != 1 || rec.evs[0].Agent != "kept" {
		t.Fatalf("events = %+v, want only the first line kept", rec.evs)
	}
}

// The recorded-count note rides along on every stream-level failure,
// including the size cap.
func TestIngestOversizedAfterEventsReportsRecordedCount(t *testing.T) {
	rec := &memRecorder{}
	s := &Server{rec: rec}

	body := `{"agent":"kept"}` + "\n" + `{"agent":"` + strings.Repeat("a", maxEventBody) + `"}`
	r := httptest.NewRequest(http.MethodPost, "/v1/events", strings.NewReader(body))
	w := httptest.NewRecorder()
	s.handlePost(w, r)

	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized body status = %d, want 413", w.Code)
	}
	if !strings.Contains(w.Body.String(), "; 1 earlier event in this stream was recorded") {
		t.Errorf("error should state the recorded count, got %q", w.Body.String())
	}
	if len(rec.evs) != 1 {
		t.Fatalf("events = %d, want 1", len(rec.evs))
	}
}

// A body beyond the size cap is a volume problem, not a JSON problem: it
// must answer 413 so senders can tell "trim the payload" from "fix the
// encoding", instead of reading `bad json` on a perfectly encoded stream.
func TestIngestRejectsOversizedBody(t *testing.T) {
	rec := &memRecorder{}
	s := &Server{rec: rec}

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

// A POST with an Origin header is browser-driven, and the only way a browser
// reaches this endpoint is a drive-by write from a page the operator is
// visiting (no preflight: text/plain is a simple request). Such requests are
// refused outright; documented senders never set Origin.
func TestIngestRejectsBrowserOriginatedPost(t *testing.T) {
	rec := &memRecorder{}
	s := startIngest(t, rec)

	req, err := http.NewRequest(http.MethodPost, "http://"+s.Addr()+"/v1/events",
		strings.NewReader(`{"agent":"forger","kind":"turn","output_tokens":9999}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Origin", "https://evil.example")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}
	if len(rec.evs) != 0 {
		t.Fatalf("events = %d, want none from a browser-originated post", len(rec.evs))
	}
}

// Token counts are unsigned quantities; a sender pushing negatives must not
// plant junk values in the retained feed.
func TestIngestClampsNegativeTokenCounts(t *testing.T) {
	rec := &memRecorder{}
	s := startIngest(t, rec)

	resp := post(t, "http://"+s.Addr()+"/v1/events",
		`{"agent":"buggy","prompt_tokens":-500,"output_tokens":-99999999999,"thinking_tokens":-3}`)
	if resp != http.StatusAccepted {
		t.Fatalf("status = %d", resp)
	}
	awaitEvents(t, rec, 1)
	if rec.evs[0].PromptTokens != 0 || rec.evs[0].OutputTokens != 0 || rec.evs[0].ThinkingTokens != 0 {
		t.Errorf("negative token counts retained: %+v", rec.evs[0])
	}
}

// A sender claiming MaxInt64 tokens would wrap the agent totals when two
// such events are summed. Anything past maxEventTokens is junk, same as
// a negative.
func TestIngestDropsAbsurdTokenCounts(t *testing.T) {
	rec := &memRecorder{}
	s := startIngest(t, rec)

	resp := post(t, "http://"+s.Addr()+"/v1/events",
		`{"agent":"buggy","prompt_tokens":1099511627777,"output_tokens":9223372036854775807,"thinking_tokens":1}`)
	if resp != http.StatusAccepted {
		t.Fatalf("status = %d", resp)
	}
	awaitEvents(t, rec, 1)
	if rec.evs[0].PromptTokens != 0 || rec.evs[0].OutputTokens != 0 || rec.evs[0].ThinkingTokens != 1 {
		t.Errorf("absurd token counts retained: %+v", rec.evs[0])
	}
}

// A claimed event timestamp far ahead of arrival is a wrong clock or a
// forgery; it must not enter the retained feed as a future instant, where
// it would pin the UI's "live" marker and render a future wall-clock time.
// Modest skew stays honored.
func TestIngestClampsFarFutureTimestamps(t *testing.T) {
	rec := &memRecorder{}
	s, err := newServer("127.0.0.1:0", rec, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	frozen := time.Unix(1_700_000_000, 0).UTC()
	s.SetNow(func() time.Time { return frozen })
	go s.Serve()
	t.Cleanup(func() { s.Close() })

	farFuture := frozen.Add(time.Hour).Format(time.RFC3339)
	nearFuture := frozen.Add(10 * time.Second).Format(time.RFC3339)
	resp := post(t, "http://"+s.Addr()+"/v1/events",
		`{"agent":"skewed","ts":"`+farFuture+`"}`+"\n"+
			`{"agent":"skewed","ts":"`+nearFuture+`"}`)
	if resp != http.StatusAccepted {
		t.Fatalf("status = %d", resp)
	}
	awaitEvents(t, rec, 2)
	if got := rec.evs[0].At; !got.Equal(frozen) {
		t.Errorf("far-future stamp retained: %v, want clamped to %v", got, frozen)
	}
	want := frozen.Add(10 * time.Second)
	if got := rec.evs[1].At; !got.Equal(want) {
		t.Errorf("modest skew not honored: %v, want %v", got, want)
	}
}

// An empty ts means "absent": every other event field defaults when empty,
// so an empty string must not abort the stream with 400 while null and a
// missing field both decode to "stamp on arrival".
func TestIngestTreatsEmptyTimestampAsAbsent(t *testing.T) {
	rec := &memRecorder{}
	s := startIngest(t, rec)

	resp := post(t, "http://"+s.Addr()+"/v1/events",
		`{"agent":"a","ts":""}`+"\n"+`{"agent":"b","ts":"   "}`)
	if resp != http.StatusAccepted {
		t.Fatalf("status = %d", resp)
	}
	awaitEvents(t, rec, 2)
	for i, ev := range rec.evs {
		if ev.At.IsZero() {
			t.Errorf("event %d: empty ts not stamped", i)
		}
	}
}

// Event stamps that ingest fills in (missing ts, far-future clamp) follow
// an injected clock so a demo recorder's simulated instant is what lands
// in the feed, not a second wall-clock read.
func TestIngestStampsWithInjectedClock(t *testing.T) {
	rec := &memRecorder{}
	s, err := newServer("127.0.0.1:0", rec, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	frozen := time.Unix(1_700_000_000, 0).UTC()
	s.SetNow(func() time.Time { return frozen })
	go s.Serve()
	t.Cleanup(func() { s.Close() })

	farFuture := frozen.Add(time.Hour).Format(time.RFC3339)
	resp := post(t, "http://"+s.Addr()+"/v1/events",
		`{"agent":"a"}`+"\n"+
			`{"agent":"b","ts":"`+farFuture+`"}`)
	if resp != http.StatusAccepted {
		t.Fatalf("status = %d", resp)
	}
	awaitEvents(t, rec, 2)
	for i, ev := range rec.evs {
		if !ev.At.Equal(frozen) {
			t.Errorf("event %d At = %v, want injected %v", i, ev.At, frozen)
		}
	}
}

// The 202 acknowledgment carries a JSON body, so it must advertise
// application/json like every other JSON response from this server,
// leaving clients nothing to sniff.
func TestIngestAckContentType(t *testing.T) {
	s := startIngest(t, &memRecorder{})

	resp, err := http.Post("http://"+s.Addr()+"/v1/events", "application/json",
		strings.NewReader(`{"agent":"x"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if got := resp.Header.Get("Content-Type"); got != "application/json" {
		t.Errorf("ack content-type = %q, want application/json", got)
	}
}

// The GET hint is the schema documentation senders see first; it must list
// every accepted field, including ts.
func TestIngestHintListsAllFields(t *testing.T) {
	s := startIngest(t, &memRecorder{})

	resp, err := http.Get("http://" + s.Addr() + "/v1/events")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"id", "ts", "agent", "kind", "model", "prompt_tokens", "output_tokens", "thinking_tokens", "via_engine", "note"} {
		if !strings.Contains(string(body), field) {
			t.Errorf("hint omits accepted field %s: %s", field, body)
		}
	}
}

func TestHealthz(t *testing.T) {
	s := startIngest(t, &memRecorder{})
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
	if got := resp.Header.Get("Content-Type"); got != "text/plain; charset=utf-8" {
		t.Errorf("healthz content-type = %q, want text/plain; charset=utf-8", got)
	}
}

func TestHealthzHEADHasNoBody(t *testing.T) {
	s := startIngest(t, &memRecorder{})
	req, err := http.NewRequest(http.MethodHead, "http://"+s.Addr()+"/healthz", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("HEAD /healthz = %d, want 200", resp.StatusCode)
	}
	if len(body) != 0 {
		t.Errorf("HEAD /healthz body = %q, want empty", body)
	}
	if got := resp.Header.Get("Content-Type"); got != "text/plain; charset=utf-8" {
		t.Errorf("HEAD /healthz content-type = %q, want text/plain; charset=utf-8", got)
	}
}

func TestIngestSecurityHeaders(t *testing.T) {
	s := startIngest(t, &memRecorder{})
	want := map[string]string{
		"X-Content-Type-Options":  "nosniff",
		"X-Frame-Options":         "DENY",
		"Content-Security-Policy": "default-src 'none'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'",
		"Cache-Control":           "no-store",
		"Referrer-Policy":         "no-referrer",
	}
	for _, path := range []string{"/healthz", "/v1/events"} {
		resp, err := http.Get("http://" + s.Addr() + path)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		for k, v := range want {
			if got := resp.Header.Get(k); got != v {
				t.Errorf("GET %s %s = %q, want %q", path, k, got, v)
			}
		}
	}
	resp, err := http.Post("http://"+s.Addr()+"/v1/events", "application/json",
		strings.NewReader(`{"agent":"x"}`))
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	for k, v := range want {
		if got := resp.Header.Get(k); got != v {
			t.Errorf("POST /v1/events %s = %q, want %q", k, got, v)
		}
	}
}

func TestIngestClampsOversizedFields(t *testing.T) {
	rec := &memRecorder{}
	s := startIngest(t, rec)

	huge := strings.Repeat("x", 10_000)
	body := fmt.Sprintf(`{"id":%q,"agent":%q,"model":%q,"note":%q}`, huge, huge, huge, huge)
	resp := post(t, "http://"+s.Addr()+"/v1/events", body)
	if resp != http.StatusAccepted {
		t.Fatalf("status = %d", resp)
	}
	awaitEvents(t, rec, 1)
	ev := rec.evs[0]
	if len(ev.ID) > 128 || len(ev.Agent) > 64 || len(ev.Model) > 128 || utf8.RuneCountInString(ev.Note) > 512 {
		t.Errorf("oversized fields retained: id=%d agent=%d model=%d note=%d",
			len(ev.ID), len(ev.Agent), len(ev.Model), utf8.RuneCountInString(ev.Note))
	}
}

// A retained field cut mid-character renders garbage downstream: half a flag
// emoji is a lone regional indicator, a cut after U+200D leaves a dangling
// joiner. Caps are counted in characters, so the cut must land between them.
func TestIngestClampKeepsCharactersWhole(t *testing.T) {
	rec := &memRecorder{}
	s := startIngest(t, rec)

	agent := strings.Repeat("\U0001F1E9\U0001F1EA", 48) // 96 flags: past the 64-character cap
	note := strings.Repeat("👩‍💻", 300)                  // ZWJ sequences: past the 512-character cap
	body := fmt.Sprintf(`{"agent":%q,"note":%q}`, agent, note)
	resp := post(t, "http://"+s.Addr()+"/v1/events", body)
	if resp != http.StatusAccepted {
		t.Fatalf("status = %d", resp)
	}
	awaitEvents(t, rec, 1)
	ev := rec.evs[0]

	// The first n whole grapheme clusters, which is what a character-safe cap
	// must retain.
	wholeClusters := func(v string, n int) string {
		var b strings.Builder
		state := -1
		for rest := v; n > 0 && rest != ""; n-- {
			var c string
			c, rest, _, state = uniseg.FirstGraphemeClusterInString(rest, state)
			b.WriteString(c)
		}
		return b.String()
	}
	if want := wholeClusters(agent, 64); ev.Agent != want {
		t.Errorf("retained agent was cut mid-character: got %q, want the whole clusters %q", ev.Agent, want)
	}
	if want := wholeClusters(note, 512); ev.Note != want {
		t.Errorf("retained note was cut mid-character: got %q, want the whole clusters %q", ev.Note, want)
	}
}

// Keep-alive connections idle between requests must be reaped; otherwise
// vanished peers hold an fd and a goroutine each for the process lifetime.
func TestIdleKeepAliveConnsReaped(t *testing.T) {
	old := idleTimeout
	idleTimeout = 50 * time.Millisecond
	t.Cleanup(func() { idleTimeout = old })

	s := startIngest(t, &memRecorder{})

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
func TestIngestNormalizesAgentToNFC(t *testing.T) {
	rec := &memRecorder{}
	s := startIngest(t, rec)

	// NFD "café" (e + combining acute) and NFC "café" must land as one
	// identity: otherwise a macOS-typed name and a JSON-NFC name split
	// the agent list and duplicate-id check.
	resp := post(t, "http://"+s.Addr()+"/v1/events",
		`{"id":"cafe\u0301","agent":"cafe\u0301","note":"cafe\u0301"}`)
	if resp != http.StatusAccepted {
		t.Fatalf("status = %d", resp)
	}
	awaitEvents(t, rec, 1)
	want := "caf\u00e9"
	ev := rec.evs[0]
	if ev.ID != want || ev.Agent != want || ev.Note != want {
		t.Errorf("id/agent/note = %q %q %q, want NFC %q", ev.ID, ev.Agent, ev.Note, want)
	}
}

func TestIngestStripsBidiFromAgent(t *testing.T) {
	rec := &memRecorder{}
	s := startIngest(t, rec)

	resp := post(t, "http://"+s.Addr()+"/v1/events",
		`{"agent":"clau\u200bde\u202e"}`)
	if resp != http.StatusAccepted {
		t.Fatalf("status = %d", resp)
	}
	awaitEvents(t, rec, 1)
	if rec.evs[0].Agent != "claude" {
		t.Errorf("agent = %q, want claude with bidi/zwsp stripped", rec.evs[0].Agent)
	}
}

func TestIngestSanitizesCustomKind(t *testing.T) {
	rec := &memRecorder{}
	s := startIngest(t, rec)

	resp := post(t, "http://"+s.Addr()+"/v1/events",
		`{"agent":"x","kind":"\u001b]0;pwned\u0007weird"}`)
	if resp != http.StatusAccepted {
		t.Fatalf("status = %d", resp)
	}
	awaitEvents(t, rec, 1)
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

	s := startIngest(t, &memRecorder{})

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
	s := startIngest(t, rec)

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
	awaitEvents(t, rec, 2)
}

// Progress alone must not extend a POST forever: past the absolute lifetime
// the connection is cut even while bytes keep trickling in.
func TestIngestCutsBodyPastAbsoluteLifetime(t *testing.T) {
	oldLife, oldIdle := maxEventLifetime, bodyIdleTimeout
	maxEventLifetime, bodyIdleTimeout = 300*time.Millisecond, time.Minute // idle longer than life
	t.Cleanup(func() { maxEventLifetime, bodyIdleTimeout = oldLife, oldIdle })

	s := startIngest(t, &memRecorder{})

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

func captureLogger() (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	lg := slog.New(slog.NewTextHandler(&buf, nil))
	return lg, &buf
}

func startIngestLog(t *testing.T, rec *memRecorder, lg *slog.Logger) *Server {
	t.Helper()
	s, err := newServer("127.0.0.1:0", rec, lg)
	if err != nil {
		t.Fatal(err)
	}
	go s.Serve()
	t.Cleanup(func() { s.Close() })
	return s
}

func countLogLines(s string) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	return len(strings.Split(s, "\n"))
}

// POST /v1/events is the only request path an operator cannot see on the
// dashboard when it fails: a structured stderr line has to answer whether it
// succeeded, how long it took, and why it was refused. Event bodies stay off
// the log; they are attacker-shaped and the retained feed already holds them.
func TestIngestLogsPostOutcome(t *testing.T) {
	lg, buf := captureLogger()
	rec := &memRecorder{}
	s := startIngestLog(t, rec, lg)

	resp, err := http.Post("http://"+s.Addr()+"/v1/events", "application/json",
		strings.NewReader(`{"agent":"coder","note":"secret-note-value","output_tokens":7}`))
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	reqID := resp.Header.Get("X-Request-Id")
	if reqID == "" {
		t.Fatal("missing X-Request-Id on 202")
	}

	got := buf.String()
	if countLogLines(got) != 1 {
		t.Fatalf("success log lines = %d (%q), want 1", countLogLines(got), got)
	}
	for _, want := range []string{
		`msg="toktop: ingest"`,
		"level=INFO",
		"req=" + reqID,
		"status=202",
		"accepted=1",
		"duration=",
		"remote=",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("success log missing %q: %s", want, got)
		}
	}
	if strings.Contains(got, "secret-note-value") || strings.Contains(got, "coder") {
		t.Errorf("log leaked event body: %s", got)
	}
	if strings.Contains(got, "127.0.0.1") || strings.Contains(got, "::1") {
		t.Errorf("log leaked loopback IP: %s", got)
	}

	buf.Reset()
	code, _ := postBody(t, "http://"+s.Addr()+"/v1/events", `{not json`)
	if code != http.StatusBadRequest {
		t.Fatalf("bad json status = %d", code)
	}
	got = buf.String()
	if countLogLines(got) != 1 {
		t.Fatalf("failure log lines = %d (%q), want 1", countLogLines(got), got)
	}
	for _, want := range []string{
		"level=WARN",
		"status=400",
		"accepted=0",
		`error="`,
		"bad json",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("failure log missing %q: %s", want, got)
		}
	}
}

func TestIngestLogRedactsPeerAddress(t *testing.T) {
	lg, buf := captureLogger()
	s := &Server{rec: &memRecorder{}, log: lg}

	r := httptest.NewRequest(http.MethodPost, "/v1/events",
		strings.NewReader(`{"agent":"x"}`))
	r.RemoteAddr = "203.0.113.9:54321"
	w := httptest.NewRecorder()
	s.handlePost(w, r)

	got := buf.String()
	if strings.Contains(got, "203.0.113.9") {
		t.Errorf("logged peer IP: %s", got)
	}
	if !strings.Contains(got, "remote=remote") {
		t.Errorf("want redacted remote, got %s", got)
	}

	buf.Reset()
	r = httptest.NewRequest(http.MethodPost, "/v1/events",
		strings.NewReader(`{"agent":"x"}`))
	r.RemoteAddr = "127.0.0.1:9999"
	w = httptest.NewRecorder()
	s.handlePost(w, r)
	got = buf.String()
	if strings.Contains(got, "127.0.0.1") {
		t.Errorf("logged loopback IP: %s", got)
	}
	if !strings.Contains(got, "remote=loopback:9999") {
		t.Errorf("want loopback port, got %s", got)
	}
}

func TestIngestEchoesRequestID(t *testing.T) {
	lg, buf := captureLogger()
	s := startIngestLog(t, &memRecorder{}, lg)

	req, err := http.NewRequest(http.MethodPost, "http://"+s.Addr()+"/v1/events",
		strings.NewReader(`{"agent":"x"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Request-Id", "harness-turn-9")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if got := resp.Header.Get("X-Request-Id"); got != "harness-turn-9" {
		t.Errorf("X-Request-Id = %q, want harness-turn-9", got)
	}
	if !strings.Contains(buf.String(), "req=harness-turn-9") {
		t.Errorf("log missing echoed req id: %s", buf.String())
	}
}

// A request-id header is attacker-shaped: newlines would split the audit line
// and let a sender forge a second slog record. net/http rejects CR/LF on the
// wire, so this drives the handler directly the way fuzz does.
func TestIngestRequestIDStaysOneLogLine(t *testing.T) {
	lg, buf := captureLogger()
	s := &Server{rec: &memRecorder{}, log: lg}

	r := httptest.NewRequest(http.MethodPost, "/v1/events",
		strings.NewReader(`{"agent":"x"}`))
	r.Header["X-Request-Id"] = []string{"id-1\nlevel=INFO forged"}
	w := httptest.NewRecorder()
	s.handlePost(w, r)

	got := buf.String()
	if countLogLines(got) != 1 {
		t.Fatalf("injected request-id split the log: %q", got)
	}
	if strings.Contains(got, "\nlevel=INFO forged") {
		t.Errorf("newline survived into log: %q", got)
	}
	echo := w.Header().Get("X-Request-Id")
	if strings.ContainsAny(echo, "\r\n") {
		t.Errorf("X-Request-Id echoed a newline: %q", echo)
	}
}

func TestHealthzIsNotLogged(t *testing.T) {
	lg, buf := captureLogger()
	s := startIngestLog(t, &memRecorder{}, lg)

	resp, err := http.Get("http://" + s.Addr() + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("healthz = %d", resp.StatusCode)
	}
	if resp.Header.Get("X-Request-Id") == "" {
		t.Error("healthz missing X-Request-Id")
	}
	if buf.Len() != 0 {
		t.Errorf("healthz must not log, got %q", buf.String())
	}
}

func TestIngestLogsBrowserOriginRefusal(t *testing.T) {
	lg, buf := captureLogger()
	s := startIngestLog(t, &memRecorder{}, lg)

	req, err := http.NewRequest(http.MethodPost, "http://"+s.Addr()+"/v1/events",
		strings.NewReader(`{"agent":"forger"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Origin", "https://evil.example")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}
	got := buf.String()
	if !strings.Contains(got, "status=403") || !strings.Contains(got, "level=WARN") {
		t.Errorf("403 not logged as warn: %s", got)
	}
}
