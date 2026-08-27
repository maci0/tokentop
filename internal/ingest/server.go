// Package ingest runs a tiny localhost HTTP endpoint so agents, harnesses and
// scripts can push token-usage events that toktop renders live.
package ingest

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/maci0/toktop/internal/core"
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
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
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
	// A POST carrying an Origin header is browser-driven: every browser
	// attaches Origin to a cross-site write, while curl, scripts and agent
	// harnesses never send one. Without this check any web page the operator
	// visits could fire a no-preflight POST here (the endpoint does not read
	// Content-Type, so text/plain sails past CORS preflight) and forge rows
	// into the live feed.
	if r.Header.Get("Origin") != "" {
		http.Error(w, "browser-originated requests are not accepted; post from a script or agent without an Origin header", http.StatusForbidden)
		return
	}
	rc := http.NewResponseController(w)
	until := time.Now().Add(maxEventLifetime)
	_ = rc.SetReadDeadline(until) // covers reads before the first progress extension
	r.Body = http.MaxBytesReader(w, &progressBody{ReadCloser: r.Body, rc: rc, until: until}, maxEventBody)
	dec := json.NewDecoder(r.Body)
	defer r.Body.Close()
	n := 0
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
	}
	for {
		var wire agentEventWire
		if err := dec.Decode(&wire); err != nil {
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
			var maxBytes *http.MaxBytesError
			if errors.As(err, &maxBytes) {
				// A size failure is not a JSON failure; senders need the
				// distinction to know trimming (not re-encoding) is the fix.
				fail(http.StatusRequestEntityTooLarge,
					fmt.Sprintf("event stream exceeds %d byte cap", maxBytes.Limit))
				return
			}
			fail(http.StatusBadRequest, "bad json: "+err.Error())
			return
		}
		at, err := parseEventTime(wire.At)
		if err != nil {
			fail(http.StatusBadRequest, "bad ts: "+err.Error())
			return
		}
		ev := core.AgentEvent{
			At:             at,
			ID:             wire.ID,
			Agent:          wire.Agent,
			Model:          wire.Model,
			Kind:           wire.Kind,
			PromptTokens:   wire.PromptTokens,
			OutputTokens:   wire.OutputTokens,
			ThinkingTokens: wire.ThinkingTokens,
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
		ev.Note = clampField(core.SanitizeText(ev.Note), 512) // free-form fields are capped so one giant event cannot dominate the retained feed
		// Token counts are unsigned quantities; negative or absurd values
		// are junk from a misbehaving sender and must not enter the
		// retained feed (summing MaxInt64 across events wraps the totals).
		ev.PromptTokens = clampTokens(ev.PromptTokens)
		ev.OutputTokens = clampTokens(ev.OutputTokens)
		ev.ThinkingTokens = clampTokens(ev.ThinkingTokens)
		switch ev.Kind {
		case "turn", "tool", "error", "note":
		default:
			ev.Kind = clampField(core.SanitizeText(strings.ToLower(ev.Kind)), 24)
		}
		if ev.Kind == "" {
			ev.Kind = "turn"
		}
		now := time.Now()
		if ev.At.IsZero() {
			ev.At = now
		} else if ev.At.Sub(now) > maxEventSkew {
			ev.At = now
		}
		s.rec.RecordAgent(ev)
		n++
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	fmt.Fprintf(w, `{"accepted":%d}`+"\n", n)
}

// agentEventWire mirrors core.AgentEvent for decoding, with the timestamp
// carried raw: encoding/json's time parser demands an explicit offset, so a
// well-formed but offset-less stamp would abort the whole stream with 400
// before parseEventTime could apply the UTC default the feed documents.
type agentEventWire struct {
	At             json.RawMessage `json:"ts"`
	ID             string          `json:"id"`
	Agent          string          `json:"agent"`
	Model          string          `json:"model"`
	Kind           string          `json:"kind"`
	PromptTokens   int64           `json:"prompt_tokens"`
	OutputTokens   int64           `json:"output_tokens"`
	ThinkingTokens int64           `json:"thinking_tokens"`
	Note           string          `json:"note"`
}

// parseEventTime decodes an event's ts field. Offset-aware RFC 3339 stamps
// are taken as sent (any zone, sub-second precision); stamps without an
// offset decode as UTC, matching what senders like Python's
// datetime.isoformat() emit. SQL-style stamps separated by a space instead
// of a T (`date '+%F %T'`, SQLite and Postgres text output) are accepted on
// the same terms: the zone is honored when present, UTC is assumed when not.
// Absent or null yields the zero Time, which the caller replaces with the
// arrival instant. An empty or whitespace-only string counts as absent,
// matching the empty-means-default behavior of every other event field.
func parseEventTime(raw json.RawMessage) (time.Time, error) {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return time.Time{}, nil
	}
	v, err := strconv.Unquote(s)
	if err != nil {
		return time.Time{}, errors.New("ts must be an RFC 3339 string")
	}
	if strings.TrimSpace(v) == "" {
		return time.Time{}, nil
	}
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t, nil
	}
	// Offset-less layouts decode as UTC; the fraction, if any, is accepted
	// after the seconds field even though the layout does not spell it out.
	layouts := []string{
		"2006-01-02T15:04:05",       // RFC 3339 without the offset
		"2006-01-02 15:04:05Z07:00", // SQL-style stamp carrying its zone
		"2006-01-02 15:04:05",       // SQL / date-style stamp without one
	}
	for _, layout := range layouts {
		if t, err := time.ParseInLocation(layout, v, time.UTC); err == nil {
			return t, nil
		}
	}
	return time.ParseInLocation(layouts[0], v, time.UTC)
}

func (s *Server) handleGet(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintln(w, `{"hint":"POST /v1/events with {id,ts,agent,kind,model,prompt_tokens,output_tokens,thinking_tokens,note}"}`)
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
// accented name never ends mid-character. Events are retained (count-capped)
// for the process lifetime, so unbounded strings would let a single oversized
// event pin memory until count-evicted.
func clampField(s string, n int) string {
	if len(s) <= n { // fast path: ASCII within cap, no scan
		return s
	}
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return core.TruncateClusters(s, n)
}
