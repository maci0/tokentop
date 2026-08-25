package ui

// Per-agent throughput, measured from the agent events the collector holds.
//
// The engines report their own rate; agents do not, so it is computed here
// from what they spent and when. Nothing is extrapolated: an agent with a
// single reading has no rate yet, and one that has gone quiet decays to zero
// rather than holding its last value.

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/maci0/tokentop/internal/core"
)

// agentWindow is how far back a rate looks. Long enough that a pause between
// turns does not read as a stall, short enough to track a real change.
const agentWindow = 30 * time.Second

// agentRate is one agent's measured throughput.
type agentRate struct {
	Agent  string
	TokPS  float64
	Tokens int64
	Last   time.Time
}

// agentRates summarizes the recent event stream, busiest first.
func agentRates(events []core.AgentEvent, now time.Time) []agentRate {
	if len(events) == 0 {
		return nil
	}
	type acc struct {
		tokens int64
		first  time.Time
		last   time.Time
		n      int
	}
	by := map[string]*acc{}
	cutoff := now.Add(-agentWindow)
	for _, ev := range events {
		if ev.At.Before(cutoff) || ev.OutputTokens <= 0 {
			continue
		}
		a, ok := by[ev.Agent]
		if !ok {
			a = &acc{first: ev.At}
			by[ev.Agent] = a
		}
		a.tokens += ev.OutputTokens
		a.last = ev.At
		a.n++
	}

	out := make([]agentRate, 0, len(by))
	for name, a := range by {
		r := agentRate{Agent: name, Tokens: a.tokens, Last: a.last}
		// A rate needs a span. One event says how much, not how fast, so it
		// reports tokens without a rate.
		if span := a.last.Sub(a.first).Seconds(); a.n > 1 && span > 0 {
			r.TokPS = float64(a.tokens) / span
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].TokPS != out[j].TokPS {
			return out[i].TokPS > out[j].TokPS
		}
		return out[i].Agent < out[j].Agent
	})
	return out
}

// agentSummary renders the rates as one line, for a panel title.
func agentSummary(rates []agentRate) string {
	if len(rates) == 0 {
		return ""
	}
	parts := make([]string, 0, len(rates))
	for i, r := range rates {
		if i == 3 {
			parts = append(parts, dim(fmt.Sprintf("+%d more", len(rates)-3)))
			break
		}
		// Agent names arrive from the ingest endpoint and agent definitions
		// on disk; they pass the terminal sanitizer like every other
		// untrusted field (feedLine does the same at render time).
		name := core.SanitizeText(r.Agent)
		if r.TokPS > 0 {
			parts = append(parts, styleValue.Render(name)+" "+
				styleOK.Render(fmtRate(r.TokPS))+dim(" tok/s"))
			continue
		}
		// Measured tokens, but not yet a rate: say so rather than showing 0.
		parts = append(parts, styleValue.Render(name)+" "+
			dim(fmtCount(r.Tokens)+" tok"))
	}
	return strings.Join(parts, dim("  ·  "))
}

// renderAgentsOnly is the view for a machine with agents but no engines: the
// agents themselves, their measured throughput, and the recent feed. It is a
// real dashboard, not a placeholder, because for someone driving claude or
// codex all day this is the whole picture.
func (m Model) renderAgentsOnly() string {
	w := m.w - 4
	rates := agentRates(m.snap.Agents, m.clock)

	rows := make([]string, 0, len(rates)+1)
	nameW := 10
	names := make([]string, len(rates))
	for i, r := range rates {
		names[i] = core.SanitizeText(r.Agent)
		nameW = max(nameW, len(names[i]))
	}
	for i, r := range rates {
		rate := dim("no rate yet")
		if r.TokPS > 0 {
			rate = styleValue.Foreground(heatColor(clamp01(r.TokPS / 60))).
				Render("▲ " + fmtRate(r.TokPS) + " tok/s")
		}
		since := dim("idle " + fmtDur(m.clock.Sub(r.Last).Truncate(time.Second)))
		if m.clock.Sub(r.Last) < 3*time.Second {
			since = styleOK.Render("● live")
		}
		rows = append(rows, fmt.Sprintf("  %-*s  %-22s  %10s  %s",
			nameW, styleValue.Render(names[i]), rate,
			dim(fmtCount(r.Tokens)+" tok"), since))
	}
	if len(rows) == 0 {
		rows = append(rows, dim("  waiting for an agent to report tokens…"))
	}

	feedH := clampi(m.h-len(rows)-12, 3, 14)
	feed := make([]string, 0, feedH)
	for i := len(m.snap.Agents) - 1; i >= 0 && len(feed) < feedH; i-- {
		feed = append(feed, clip(feedLine(m.snap.Agents[i]), w))
	}
	for i, j := 0, len(feed)-1; i < j; i, j = i+1, j-1 {
		feed[i], feed[j] = feed[j], feed[i]
	}

	title := "AGENTS"
	if hint := dim("  local, read from their own session logs"); m.w >= 78 {
		title += hint
	}
	body := lipgloss.JoinVertical(lipgloss.Left,
		m.renderHeader(),
		"",
		panel(title, strings.Join(rows, "\n"), w, len(rows)),
		panel("AGENT FEED", strings.Join(feed, "\n"), w, feedH),
		"",
		dim("  no inference engines detected — tokentop --add <url> to attach one"),
	)
	footer := m.renderFooter()
	if gap := m.h - lipgloss.Height(body) - lipgloss.Height(footer) - 1; gap > 0 {
		body += strings.Repeat("\n", gap)
	}
	// Nothing may exceed the pane: bubbletea wraps an over-wide line and drags
	// every row below it out of alignment.
	out := strings.Split(body+"\n"+footer, "\n")
	for i, ln := range out {
		out[i] = clip(ln, m.w)
	}
	return strings.Join(out, "\n")
}
