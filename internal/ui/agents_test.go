package ui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/maci0/toktop/internal/core"
)

// Agent names arrive from the ingest endpoint (attacker-shaped: any local
// process able to reach the endpoint) and from agent definitions on disk.
// The agents-only view and the panel summary must pass them through the same
// terminal sanitizer feedLine uses, so an escape payload in a name can never
// reach the raw terminal.
func TestAgentSummarySanitizesNames(t *testing.T) {
	rates := []agentRate{
		{Agent: "claude\x1b]52;c;QUJD\x07", TokPS: 12, Last: time.Now()},
		{Agent: "codex\x1b[2J", Tokens: 300, Last: time.Now()},
	}
	out := strip(agentSummary(rates))
	for _, b := range []byte{'\x1b', '\x07'} {
		if strings.ContainsRune(out, rune(b)) {
			t.Errorf("agentSummary output contains control byte %#x:\n%s", b, out)
		}
	}
	if !strings.Contains(out, "claude") || !strings.Contains(out, "codex") {
		t.Errorf("agentSummary lost visible names:\n%s", out)
	}
}

func TestRenderAgentsOnlySanitizesNames(t *testing.T) {
	m := New(Config{Version: "t"}, nil)
	m.snap = core.Snapshot{Agents: []core.AgentEvent{{
		At:           time.Now(),
		Agent:        "evil\x1b]0;pwned\x07agent",
		OutputTokens: 42,
	}}}
	m.w, m.h, m.ready = 100, 40, true
	m.clock = time.Now()
	out := strip(m.renderAgentsOnly())
	if strings.ContainsAny(out, "\x1b\x07") {
		t.Errorf("agents-only view leaked escape bytes:\n%s", out)
	}
	if !strings.Contains(out, "evil") {
		t.Errorf("agents-only view lost the visible name:\n%s", out)
	}
}

// The rate column must start at the same cell in every row. Padding by rune
// or byte counts (fmt width verbs, len) counts the ANSI bytes of styled
// cells and drifts whenever a name's visible width differs from either.
func TestAgentRowsAlignRateColumn(t *testing.T) {
	now := time.Now()
	ev := func(agent string, at time.Time) core.AgentEvent {
		return core.AgentEvent{At: at, Agent: agent, Kind: "turn", OutputTokens: 10}
	}
	events := []core.AgentEvent{
		ev("a", now.Add(-3*time.Second)), ev("a", now.Add(-2*time.Second)),
		ev("日本語エージェント", now.Add(-3*time.Second)), ev("日本語エージェント", now.Add(-2*time.Second)),
	}
	rows := agentRows(agentRates(events, now), now)
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	rateCol := func(row string) int {
		i := strings.Index(row, "▲")
		if i < 0 {
			t.Fatalf("row lacks a rate cell: %q", strip(row))
		}
		return lipgloss.Width(row[:i])
	}
	if got := rateCol(rows[0]); got != rateCol(rows[1]) {
		t.Errorf("rate columns misaligned: %d vs %d cells\n%s\n%s",
			got, rateCol(rows[1]), strip(rows[0]), strip(rows[1]))
	}
}

// feedLines renders the newest n events oldest-first, so the feed reads like
// a log tail; it must never reorder or drop within the tail.
func TestFeedLinesNewestLast(t *testing.T) {
	base := time.Now()
	var events []core.AgentEvent
	for i := range 6 {
		events = append(events, core.AgentEvent{
			At: base.Add(time.Duration(i) * time.Second), Agent: fmt.Sprint(i), Kind: "turn",
		})
	}
	lines := feedLines(events, 4, 80)
	if len(lines) != 4 {
		t.Fatalf("lines = %d, want 4", len(lines))
	}
	for i, want := range []string{"2", "3", "4", "5"} {
		if got := strip(lines[i]); !strings.Contains(got, want) {
			t.Errorf("line %d = %q, want oldest-first event %s", i, got, want)
		}
	}
}
