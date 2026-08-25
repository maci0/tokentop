package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/maci0/tokentop/internal/core"
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
