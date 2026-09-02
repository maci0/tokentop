package ingest

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/maci0/toktop/internal/core"
)

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
	// Mixed Latin+Cyrillic/Greek names spoof a real agent in the feed
	// ("сlaude" vs "claude"). Collapse them to the anonymous default.
	if core.MixedScriptIdentity(ev.Agent) {
		ev.Agent = ""
	}
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
// stream of them does not 400. A Unix epoch number (JSON number or numeric
// string) is accepted too: Python time.time() and JS Date.now() otherwise
// 400 and drop every event queued behind the first one. Magnitude picks
// seconds / milliseconds / microseconds / nanoseconds; values that decode
// before 2001-09-09 stay errors so a small integer in ts is still a type
// mistake. Absent or null yields the zero Time, which the caller replaces
// with the arrival instant. An empty or whitespace-only string counts as
// absent, matching the empty-means-default behavior of every other event
// field.
func parseEventTime(raw json.RawMessage) (time.Time, error) {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return time.Time{}, nil
	}
	v, err := strconv.Unquote(s)
	if err != nil {
		if t, ok := unixEpochTime(s); ok {
			return t, nil
		}
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
	if t, ok := unixEpochTime(v); ok {
		return t, nil
	}
	return time.Time{}, errBadTS
}

// unixEpochMinSec is 2001-09-09. Smaller decoded instants are not accepted
// as Unix timestamps: a JSON 123 in ts is a type error, not 1970-01-01.
const unixEpochMinSec int64 = 1_000_000_000

func unixEpochTime(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	var t time.Time
	if strings.IndexByte(s, '.') < 0 && strings.IndexByte(s, 'e') < 0 && strings.IndexByte(s, 'E') < 0 {
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil || n <= 0 {
			return time.Time{}, false
		}
		t = unixEpochInt(n)
	} else {
		n, err := strconv.ParseFloat(s, 64)
		if err != nil || !(n > 0) || math.IsInf(n, 0) {
			return time.Time{}, false
		}
		t = unixEpochFloat(n)
	}
	if t.Unix() < unixEpochMinSec {
		return time.Time{}, false
	}
	return t, true
}

func unixEpochInt(n int64) time.Time {
	switch {
	case n < 100_000_000_000: // seconds (year 5138)
		return time.Unix(n, 0).UTC()
	case n < 100_000_000_000_000: // milliseconds
		return time.Unix(n/1000, (n%1000)*int64(time.Millisecond)).UTC()
	case n < 100_000_000_000_000_000: // microseconds
		return time.Unix(n/1_000_000, (n%1_000_000)*int64(time.Microsecond)).UTC()
	default: // nanoseconds
		return time.Unix(0, n).UTC()
	}
}

func unixEpochFloat(n float64) time.Time {
	switch {
	case n < 1e11:
		sec := math.Trunc(n)
		return time.Unix(int64(sec), int64((n-sec)*1e9)).UTC()
	case n < 1e14:
		ms := int64(n)
		return time.Unix(ms/1000, (ms%1000)*int64(time.Millisecond)).UTC()
	case n < 1e17:
		us := int64(n)
		return time.Unix(us/1_000_000, (us%1_000_000)*int64(time.Microsecond)).UTC()
	default:
		return time.Unix(0, int64(n)).UTC()
	}
}

var errBadTS = errors.New("must be an RFC 3339 string")

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

// clampField caps a free-form event field. See core.ClampField.
func clampField(s string, n int) string {
	return core.ClampField(s, n)
}
