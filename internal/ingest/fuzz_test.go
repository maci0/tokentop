package ingest

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"
)

// FuzzHandlePost drives the full POST /v1/events pipeline with arbitrary
// bytes: JSON/NDJSON decode, timestamp parsing, sanitization, clamping and
// recording, including every error path (bad JSON, bad ts, size cap). The
// event feed is retained for the process lifetime and rendered to the
// terminal, so anything recorded must obey the boundary guarantees: fields
// are escape-free and rune-capped, token counts non-negative, defaults
// applied, and only documented status codes leave the handler.
func FuzzHandlePost(f *testing.F) {
	for _, seed := range [][]byte{
		[]byte(`{"agent":"coder","kind":"tool","prompt_tokens":4200,"output_tokens":310,"note":"shell(git status)"}`),
		[]byte(`{"id":"turn-1","agent":"coder","output_tokens":50}`),
		[]byte(`{"id":"` + strings.Repeat("a", 300) + `","agent":"x"}`),
		[]byte("{\"agent\":\"a\",\"ts\":\"2026-01-02T03:04:05\"}\n{\"agent\":\"b\",\"ts\":\"2026-01-02T05:04:05+02:00\"}"),
		[]byte(`{"agent":"py","ts":"2026-01-02T03:04:05.123456"}`),
		[]byte(`{"agent":"x","ts":"yesterday"}`),
		[]byte(`{"agent":"x","ts":""}`),
		[]byte(`{"agent":"x","ts":null}`),
		[]byte(`{"agent":"x","ts":123}`),
		[]byte(`{not json`),
		nil,
		[]byte("\n\n"),
		[]byte(`{"kind":"\u001b]0;pwned\u0007weird"}`),
		[]byte(`{"agent":"esc\u001b[2Jclear","model":"\u009bhidden"}`),
		[]byte(`{"agent":"` + strings.Repeat("a", 300) + `"}`),
		[]byte(`{"agent":"` + strings.Repeat("é", 300) + `"}`),
		[]byte(`{"prompt_tokens":-500,"output_tokens":-99999999999}`),
		[]byte(`{"agent":["array"],"note":{"obj":true}}`),
		[]byte(`{"agent":"x"} trailing garbage`),
		[]byte(`[[[[[[[[["deep"]]]]]]]]]`),
		[]byte("{\"agent\":\"\xff\xfe\"}"),
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, body []byte) {
		rec := &memRecorder{}
		s := &Server{rec: rec}

		r := httptest.NewRequest(http.MethodPost, "/v1/events", bytes.NewReader(body))
		w := httptest.NewRecorder()
		s.handlePost(w, r)

		switch w.Code {
		case http.StatusAccepted:
			if len(rec.evs) == 0 {
				t.Fatal("202 accepted but no event recorded")
			}
			// Events written to the wire must match those actually recorded.
			var ack int
			if n, _ := fmt.Sscanf(w.Body.String(), `{"accepted":%d}`, &ack); n != 1 || ack != len(rec.evs) {
				t.Fatalf("ack %q vs %d recorded events", w.Body.String(), len(rec.evs))
			}
		case http.StatusBadRequest, http.StatusRequestEntityTooLarge:
			// A mid-stream failure keeps the events decoded before it; each
			// one still has to satisfy the boundary checks below.
		default:
			t.Fatalf("unexpected status %d for body %q", w.Code, body)
		}

		for i, ev := range rec.evs {
			if ev.Agent == "" {
				t.Errorf("event %d: empty agent must default", i)
			}
			if ev.Kind == "" {
				t.Errorf("event %d: empty kind must default", i)
			}
			if ev.At.IsZero() {
				t.Errorf("event %d: zero timestamp must be stamped", i)
			}
			if ev.PromptTokens < 0 || ev.OutputTokens < 0 || ev.ThinkingTokens < 0 {
				t.Errorf("event %d: negative token counts retained: %+v", i, ev)
			}
			if n := utf8.RuneCountInString(ev.Agent); n > 64 {
				t.Errorf("event %d: agent = %d runes, cap 64", i, n)
			}
			if n := utf8.RuneCountInString(ev.Model); n > 128 {
				t.Errorf("event %d: model = %d runes, cap 128", i, n)
			}
			if n := utf8.RuneCountInString(ev.Note); n > 512 {
				t.Errorf("event %d: note = %d runes, cap 512", i, n)
			}
			if n := utf8.RuneCountInString(ev.Kind); n > 24 {
				t.Errorf("event %d: kind = %d runes, cap 24", i, n)
			}
			if n := utf8.RuneCountInString(ev.ID); n > 128 {
				t.Errorf("event %d: id = %d runes, cap 128", i, n)
			}
			assertRenderSafe(t, i, "agent", ev.Agent)
			assertRenderSafe(t, i, "model", ev.Model)
			assertRenderSafe(t, i, "note", ev.Note)
			assertRenderSafe(t, i, "kind", ev.Kind)
			assertRenderSafe(t, i, "id", ev.ID)
		}
	})
}

// assertRenderSafe fails if s still holds anything SanitizeText is supposed
// to strip: ESC bytes, C0 controls other than the preserved newline/tab,
// DEL, or C1 control runes.
func assertRenderSafe(t *testing.T, i int, field, s string) {
	t.Helper()
	for j := 0; j < len(s); j++ {
		c := s[j]
		if c == 0x1b || c == 0x7f || (c < 0x20 && c != '\n' && c != '\t') {
			t.Fatalf("event %d: %s retains control byte %#02x at %d: %q", i, field, c, j, s)
		}
	}
	for _, r := range s {
		if r >= 0x80 && r <= 0x9f {
			t.Fatalf("event %d: %s retains C1 control %U: %q", i, field, r, s)
		}
	}
}
