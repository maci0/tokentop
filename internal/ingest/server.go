// Package ingest runs a tiny localhost HTTP endpoint so agents, harnesses and
// scripts can push token-usage events that toktop renders live.
package ingest

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"

	"github.com/maci0/toktop/internal/core"
)

// Server accepts POST /v1/events (single object or newline-delimited stream),
// GET /v1/events as a schema hint, and GET /healthz.
type Server struct {
	rec  Recorder
	now  func() time.Time // event stamps; nil means time.Now. I/O deadlines stay wall-clock.
	srv  http.Server
	ln   net.Listener
	addr string
	log  *slog.Logger
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

// New binds addr and returns a server that Serve will accept on. The listen
// happens here so Addr reports the actual bound port (including :0) before
// Serve runs.
func New(addr string, rec Recorder) (*Server, error) {
	return newServer(addr, rec, newIngestLogger())
}

func newServer(addr string, rec Recorder, lg *slog.Logger) (*Server, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	if lg == nil {
		lg = slog.New(slog.DiscardHandler)
	}
	s := &Server{rec: rec, ln: ln, addr: ln.Addr().String(), log: lg}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/events", s.handlePost)
	mux.HandleFunc("GET /v1/events", s.handleGet)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "ok")
	})
	s.srv = http.Server{
		Handler:           withSecurityHeaders(withRequestID(mux)),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       idleTimeout,
		ErrorLog:          slog.NewLogLogger(lg.Handler(), slog.LevelError),
	}
	return s, nil
}

// withSecurityHeaders sets browser-facing controls on every ingest
// response. The endpoint is HTTP (loopback by default, optionally a
// routable bind); HSTS is omitted because it would pin HTTPS on a
// cleartext listener. nosniff/frame/CSP stop a fetched JSON body from
// being sniffed as HTML or framed when --ingest is exposed.
func withSecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
		h.Set("Cache-Control", "no-store")
		h.Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

type ctxReqID struct{}

// withRequestID stamps every ingest response with X-Request-Id so a harness
// can join its send with the stderr audit line. A caller-supplied header is
// honored after sanitizing; otherwise a random id is minted.
func withRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := incomingRequestID(r)
		w.Header().Set("X-Request-Id", id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxReqID{}, id)))
	})
}

func incomingRequestID(r *http.Request) string {
	if v := logField(r.Header.Get("X-Request-Id"), 64); v != "" {
		return v
	}
	return rand.Text()
}

// clientEventKey is the caller-supplied idempotency token for this POST, if
// any. Only Idempotency-Key counts: X-Request-Id is a correlation id, often
// reused across distinct sends or minted per attempt, so treating it as an
// event id would collapse unrelated turns or fail to collapse retries.
func clientEventKey(r *http.Request) string {
	return logField(r.Header.Get("Idempotency-Key"), 128)
}

// derivedEventID maps one line of a POST onto a stable event id so a replay
// of the same stream (lost 202, retry after a mid-stream 400) lands on the
// same keys the collector already ignores. seq is 1-based within the POST.
func derivedEventID(key string, seq int) string {
	if key == "" || seq < 1 {
		return ""
	}
	suffix := ":" + strconv.Itoa(seq)
	head := clampField(key, 128-utf8.RuneCountInString(suffix))
	if head == "" {
		return ""
	}
	return head + suffix
}

func requestID(r *http.Request) string {
	if v, ok := r.Context().Value(ctxReqID{}).(string); ok && v != "" {
		return v
	}
	return incomingRequestID(r)
}

func newIngestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		ReplaceAttr: utcLogTime,
	}))
}

func utcLogTime(_ []string, a slog.Attr) slog.Attr {
	if a.Key == slog.TimeKey && a.Value.Kind() == slog.KindTime {
		return slog.String(slog.TimeKey, a.Value.Time().UTC().Format(time.RFC3339Nano))
	}
	return a
}

// logField prepares attacker-shaped text for a single-line log attribute:
// terminal escapes stripped, whitespace collapsed so a payload cannot split
// the line, then capped.
func logField(s string, n int) string {
	return clampField(strings.Join(strings.Fields(core.SanitizeText(s)), " "), n)
}

// logRemote prepares a peer address for the ingest audit line. Loopback
// keeps the port so a local sender can be told apart; any other IP is
// dropped. The address is personal data when --ingest is bound off loopback.
func logRemote(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "unknown"
	}
	ip := net.ParseIP(host)
	if ip != nil && ip.IsLoopback() {
		return net.JoinHostPort("loopback", port)
	}
	return "remote"
}

func (s *Server) logPost(r *http.Request, reqID string, status, accepted int, d time.Duration, errMsg string) {
	if s.log == nil {
		return
	}
	attrs := []any{
		"req", reqID,
		"remote", logRemote(r.RemoteAddr),
		"status", status,
		"accepted", accepted,
		"duration", d.Round(time.Microsecond),
	}
	if errMsg != "" {
		attrs = append(attrs, "error", logField(errMsg, 256))
	}
	level := slog.LevelInfo
	if status >= 400 {
		level = slog.LevelWarn
	}
	s.log.Log(r.Context(), level, "toktop: ingest", attrs...)
}

// SetNow overrides the clock used to stamp events that arrive without a
// timestamp and to clamp far-future stamps. Request timeouts still use
// wall time. Call before Serve. Demo mode passes the simulated clock so
// harness POSTs stay on the seeded timeline.
func (s *Server) SetNow(fn func() time.Time) {
	if fn == nil {
		fn = time.Now
	}
	s.now = fn
}

func (s *Server) instant() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
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

// maxEventSkew bounds how far ahead of arrival a claimed event timestamp may
// sit before it is clamped to the arrival instant. The stamp is a sender's
// word: a wrong clock (or a forged event, since this endpoint authenticates
// nothing) can claim an instant hours ahead, which would pin the agent view's
// "● live" marker (a negative idle duration reads as fresh) and render a
// future wall-clock time in the feed until real time caught up. Modest skew
// between machines stays honored.
const maxEventSkew = 2 * time.Minute

// maxEventLifetime and bodyIdleTimeout bound how long one POST may hold the
// connection. The byte cap above limits volume, not time: a peer that sends
// headers and then drips bytes (or goes silent mid-body) would otherwise pin
// an fd and a goroutine apiece until it finishes, and IdleTimeout does not
// apply mid-request. The absolute deadline caps total lifetime; each
// successful read extends the deadline up to that end, so slow-but-alive
// NDJSON streams keep working while silent ones are reaped. Both are vars so
// tests can shrink them.
var (
	maxEventLifetime = 10 * time.Minute
	bodyIdleTimeout  = time.Minute
)

// progressBody arms the read deadline before every read: no progress within
// bodyIdleTimeout, or past the absolute end, surfaces as an i/o timeout from
// Decode. Deadline setting is best effort; on ResponseWriters without
// support the body degrades to volume-only capping.
type progressBody struct {
	io.ReadCloser
	rc    *http.ResponseController
	until time.Time
}

func (b *progressBody) Read(p []byte) (int, error) {
	next := time.Now().Add(bodyIdleTimeout)
	if next.After(b.until) {
		next = b.until
	}
	_ = b.rc.SetReadDeadline(next)
	return b.ReadCloser.Read(p)
}

func (s *Server) handlePost(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	reqID := requestID(r)
	if w.Header().Get("X-Request-Id") == "" {
		w.Header().Set("X-Request-Id", reqID)
	}
	done := func(status, accepted int, errMsg string) {
		s.logPost(r, reqID, status, accepted, time.Since(start), errMsg)
	}

	// A POST carrying an Origin header is browser-driven: every browser
	// attaches Origin to a cross-site write, while curl, scripts and agent
	// harnesses never send one. Without this check any web page the operator
	// visits could fire a no-preflight POST here (the endpoint does not read
	// Content-Type, so text/plain sails past CORS preflight) and forge rows
	// into the live feed.
	if r.Header.Get("Origin") != "" {
		msg := "browser-originated requests are not accepted; post from a script or agent without an Origin header"
		http.Error(w, msg, http.StatusForbidden)
		done(http.StatusForbidden, 0, msg)
		return
	}
	rc := http.NewResponseController(w)
	until := time.Now().Add(maxEventLifetime)
	_ = rc.SetReadDeadline(until) // covers reads before the first progress extension
	r.Body = http.MaxBytesReader(w, &progressBody{ReadCloser: r.Body, rc: rc, until: until}, maxEventBody)
	dec := json.NewDecoder(r.Body)
	defer r.Body.Close()
	n := 0
	replayKey := clientEventKey(r)
	// fail reports a stream-level error. Events decode-and-record one by one,
	// so everything before the failing line is already in the feed; saying so
	// lets senders resume after the failure instead of replaying the whole
	// stream and duplicating what was kept.
	fail := func(status int, msg string) {
		if n > 0 {
			if n == 1 {
				msg += "; 1 earlier event in this stream was recorded"
			} else {
				msg += fmt.Sprintf("; %d earlier events in this stream were recorded", n)
			}
		}
		http.Error(w, msg, status)
		done(status, n, msg)
	}
	for {
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			if n > 0 && errors.Is(err, io.EOF) {
				break // clean end of stream after at least one event
			}
			if errors.Is(err, io.EOF) {
				fail(http.StatusBadRequest, "empty body: expected one JSON object or an NDJSON stream")
				return
			}
			if errors.Is(err, os.ErrDeadlineExceeded) {
				fail(http.StatusRequestTimeout, "request stalled")
				return
			}
			if maxBytes, ok := errors.AsType[*http.MaxBytesError](err); ok {
				// A size failure is not a JSON failure; senders need the
				// distinction to know trimming (not re-encoding) is the fix.
				fail(http.StatusRequestEntityTooLarge,
					fmt.Sprintf("event stream exceeds %d byte cap", maxBytes.Limit))
				return
			}
			fail(http.StatusBadRequest, clientJSONError(err))
			return
		}
		// Null unmarshals into a struct as zeros; the body must be an object.
		if kind := jsonRootKind(raw); kind != "object" {
			fail(http.StatusBadRequest, "bad json: expected a JSON object or NDJSON stream, got "+kind)
			return
		}
		var wire agentEventWire
		if err := json.Unmarshal(raw, &wire); err != nil {
			fail(http.StatusBadRequest, clientJSONError(err))
			return
		}
		ev, err := eventFromWire(wire)
		if err != nil {
			msg := err.Error()
			if errors.Is(err, errBadTS) {
				msg = "bad ts: " + msg
			}
			fail(http.StatusBadRequest, msg)
			return
		}
		now := s.instant()
		if ev.At.IsZero() {
			ev.At = now
		} else if ev.At.Sub(now) > maxEventSkew {
			ev.At = now
		}
		// Body id wins: that is the event's own identity. When the sender
		// omitted one, the POST-level key plus this line's index stands in,
		// so retrying the whole request does not double-count.
		if ev.ID == "" {
			ev.ID = derivedEventID(replayKey, n+1)
		}
		s.rec.RecordAgent(ev)
		n++
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	fmt.Fprintf(w, `{"accepted":%d}`+"\n", n)
	done(http.StatusAccepted, n, "")
}

// eventFromWire sanitizes one decoded event. Timestamp clamping against
// arrival time stays in handlePost: that bound is a property of the request,
// not of the wire object.
func eventFromWire(wire agentEventWire) (core.AgentEvent, error) {
	at, err := parseEventTime(wire.At)
	if err != nil {
		return core.AgentEvent{}, err
	}
	prompt, output, thinking, err := parseTokenFields(wire)
	if err != nil {
		return core.AgentEvent{}, err
	}
	ev := core.AgentEvent{
		At:             at,
		ID:             wire.ID,
		Agent:          wire.Agent,
		Model:          wire.Model,
		Kind:           wire.Kind,
		PromptTokens:   prompt,
		OutputTokens:   output,
		ThinkingTokens: thinking,
		ViaEngine:      wire.ViaEngine,
		Note:           wire.Note,
	}
	// Event fields are attacker-shaped text (any local process or peer
	// able to reach this endpoint): strip terminal escape sequences and
	// control characters before the values are stored and later rendered.
	// Defaults come after sanitization: a value the sanitizer empties
	// (pure escape sequences) must not slip past the fallback.
	ev.ID = clampField(core.SanitizeText(ev.ID), 128)
	ev.Agent = clampField(core.SanitizeText(ev.Agent), 64)
	if ev.Agent == "" {
		ev.Agent = "anonymous"
	}
	ev.Model = clampField(core.SanitizeText(ev.Model), 128)
	ev.ViaEngine = clampField(core.SanitizeText(ev.ViaEngine), 128)
	ev.Note = clampField(core.SanitizeText(ev.Note), 512) // free-form fields are capped so one giant event cannot dominate the retained feed
	// Token counts are unsigned quantities; negative or absurd values
	// are junk from a misbehaving sender and must not enter the
	// retained feed (summing MaxInt64 across events wraps the totals).
	ev.PromptTokens = clampTokens(ev.PromptTokens)
	ev.OutputTokens = clampTokens(ev.OutputTokens)
	ev.ThinkingTokens = clampTokens(ev.ThinkingTokens)
	switch ev.Kind {
	case core.AgentKindTurn, core.AgentKindTool, core.AgentKindError, core.AgentKindNote:
	default:
		ev.Kind = clampField(core.SanitizeText(strings.ToLower(ev.Kind)), 24)
	}
	if ev.Kind == "" {
		ev.Kind = core.AgentKindTurn
	}
	return ev, nil
}

// agentEventWire mirrors core.AgentEvent for decoding, with ts and token
// counts carried raw: encoding/json's time parser demands an explicit offset,
// and int64 rejects whole JSON numbers such as 100.0, so a well-formed but
// offset-less stamp or a Python-dumped float would abort the whole stream
// with 400 before parseEventTime / parseTokenJSON could apply the documented
// forms.
type agentEventWire struct {
	At             json.RawMessage `json:"ts"`
	ID             string          `json:"id"`
	Agent          string          `json:"agent"`
	Model          string          `json:"model"`
	Kind           string          `json:"kind"`
	PromptTokens   json.RawMessage `json:"prompt_tokens"`
	OutputTokens   json.RawMessage `json:"output_tokens"`
	ThinkingTokens json.RawMessage `json:"thinking_tokens"`
	ViaEngine      string          `json:"via_engine"`
	Note           string          `json:"note"`
}

// parseEventTime decodes an event's ts field. Offset-aware RFC 3339 stamps
// are taken as sent (any zone, sub-second precision); stamps without an
// offset decode as UTC, matching what senders like Python's
// datetime.isoformat() emit. SQL-style stamps separated by a space instead
// of a T (`date '+%F %T'`, SQLite and Postgres text output) are accepted on
// the same terms: the zone is honored when present, UTC is assumed when not.
// Colon-less numeric offsets (`date '+%z'`, `-0700`) are ISO 8601 but not
// RFC 3339; they are accepted as the same instant as the colon form so a
// stream of them does not 400. Absent or null yields the zero Time, which
// the caller replaces with the arrival instant. An empty or whitespace-only
// string counts as absent, matching the empty-means-default behavior of
// every other event field.
func parseEventTime(raw json.RawMessage) (time.Time, error) {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return time.Time{}, nil
	}
	v, err := strconv.Unquote(s)
	if err != nil {
		return time.Time{}, errBadTS
	}
	if strings.TrimSpace(v) == "" {
		return time.Time{}, nil
	}
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t, nil
	}
	// Offset-less layouts decode as UTC; the fraction, if any, is accepted
	// after the seconds field even though the layout does not spell it out.
	// Z0700 is the colon-less numeric offset `date '+%z'` and many ISO 8601
	// profiles emit; RFC 3339 requires the colon, so a stream of those
	// stamps used to 400 and drop every event queued behind the first one.
	layouts := []string{
		"2006-01-02T15:04:05Z0700",  // RFC 3339-style offset without the colon
		"2006-01-02T15:04:05",       // RFC 3339 without the offset
		"2006-01-02 15:04:05Z07:00", // SQL-style stamp carrying its zone
		"2006-01-02 15:04:05Z0700",  // SQL-style with a colon-less offset
		"2006-01-02 15:04:05",       // SQL / date-style stamp without one
	}
	for _, layout := range layouts {
		if t, err := time.ParseInLocation(layout, v, time.UTC); err == nil {
			return t, nil
		}
	}
	return time.Time{}, errBadTS
}

var errBadTS = errors.New("must be an RFC 3339 string")

// clientJSONError turns an encoding/json decode failure into a sender-facing
// reason: JSON field names, no Go type names.
func clientJSONError(err error) string {
	if ut, ok := errors.AsType[*json.UnmarshalTypeError](err); ok {
		if ut.Field != "" {
			return fmt.Sprintf("bad json: %s must be %s, not %s", ut.Field, wantJSONType(ut), ut.Value)
		}
		return fmt.Sprintf("bad json: expected a JSON object or NDJSON stream, got %s", ut.Value)
	}
	if errors.Is(err, io.ErrUnexpectedEOF) {
		return "bad json: truncated"
	}
	return "bad json: " + err.Error()
}

func wantJSONType(ut *json.UnmarshalTypeError) string {
	if ut.Type != nil && ut.Type.String() == "string" {
		return "a string"
	}
	return "the documented type"
}

// parseTokenFields reads the three token counts. Whole JSON numbers (100.0,
// 1e2) count as integers; a fractional remainder or a non-number is 400.
func parseTokenFields(w agentEventWire) (prompt, output, thinking int64, err error) {
	if prompt, err = parseTokenJSON(w.PromptTokens, "prompt_tokens"); err != nil {
		return
	}
	if output, err = parseTokenJSON(w.OutputTokens, "output_tokens"); err != nil {
		return
	}
	thinking, err = parseTokenJSON(w.ThinkingTokens, "thinking_tokens")
	return
}

func parseTokenJSON(raw json.RawMessage, field string) (int64, error) {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return 0, nil
	}
	if i, err := strconv.ParseInt(s, 10, 64); err == nil {
		return i, nil
	}
	f, err := strconv.ParseFloat(s, 64)
	if err == nil && !math.IsNaN(f) && !math.IsInf(f, 0) && math.Trunc(f) == f {
		if f > math.MaxInt64 || f < math.MinInt64 {
			return 0, fmt.Errorf("bad json: %s is out of range", field)
		}
		return int64(f), nil
	}
	return 0, fmt.Errorf("bad json: %s must be an integer", field)
}

// jsonRootKind names the JSON value kind of a decoded raw message so a
// non-object root (null, array, string, number) can be refused with the
// same "expected a JSON object" wording Decode-into-struct already used
// for arrays. encoding/json treats null as a zero struct, which is why
// this check sits in front of Unmarshal.
func jsonRootKind(raw json.RawMessage) string {
	s := strings.TrimSpace(string(raw))
	if s == "" {
		return "empty"
	}
	switch s[0] {
	case '{':
		return "object"
	case '[':
		return "array"
	case '"':
		return "string"
	case 't', 'f':
		return "bool"
	case 'n':
		return "null"
	default:
		return "number"
	}
}

func (s *Server) handleGet(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintln(w, `{"hint":"POST /v1/events with {id,ts,agent,kind,model,prompt_tokens,output_tokens,thinking_tokens,via_engine,note}"}`)
}

// maxEventTokens bounds a token count on one event. Real usage never
// approaches it; a sender claiming more is lying or broken, and summing
// MaxInt64 values across the retained feed would wrap the agent totals.
const maxEventTokens = 1 << 40

func clampTokens(n int64) int64 {
	if n < 0 || n > maxEventTokens {
		return 0
	}
	return n
}

// clampField caps a free-form event field at n user-perceived characters
// (grapheme clusters), cutting between characters so a retained emoji or
// accented name never ends mid-character. The field is composed to NFC
// first: "café" spelled NFD (e + combining acute) and NFC (precomposed)
// would otherwise be two agents and two event IDs. Events are retained
// (count-capped) for the process lifetime, so unbounded strings would let
// a single oversized event pin memory until count-evicted.
func clampField(s string, n int) string {
	s = norm.NFC.String(s)
	if len(s) <= n { // fast path: ASCII within cap, no scan
		return s
	}
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return core.TruncateClusters(s, n)
}
