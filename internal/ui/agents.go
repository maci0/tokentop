package ui

// Per-agent throughput, measured from the agent events the collector holds.
//
// The engines report their own rate; agents do not, so it is computed here
// from what they spent and when. Nothing is extrapolated: an agent with a
// single reading has no rate yet, and one that has gone quiet falls out of
// the window (and the list) rather than holding its last value forever.
//
// An event with ViaEngine set is this agent's share of an engine already on
// the dashboard. It still appears in the list (who is working) but is left
// out of header and chart totals so those tokens are not counted twice.

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/maci0/toktop/internal/core"
)

// agentWindow is how far back a rate looks. Long enough that a pause between
// turns does not read as a stall, short enough to track a real change.
const agentWindow = 30 * time.Second

// agentRate is one agent's measured throughput.
type agentRate struct {
	Agent     string
	TokPS     float64
	PromptPS  float64
	Tokens    int64
	Prompt    int64
	Thinking  int64
	Last      time.Time
	ViaEngine string
}

// agentRates summarizes the recent event stream, busiest first.
func agentRates(events []core.AgentEvent, now time.Time) []agentRate {
	if len(events) == 0 {
		return nil
	}
	type acc struct {
		tokens   int64
		prompt   int64
		thinking int64
		first    time.Time
		last     time.Time
		via      string
		n        int
	}
	by := map[string]*acc{}
	cutoff := now.Add(-agentWindow)
	for _, ev := range events {
		if ev.At.Before(cutoff) {
			continue
		}
		if ev.OutputTokens <= 0 && ev.PromptTokens <= 0 && ev.ThinkingTokens <= 0 && ev.ViaEngine == "" {
			continue
		}
		a, ok := by[ev.Agent]
		if !ok {
			a = &acc{first: ev.At}
			by[ev.Agent] = a
		}
		a.tokens += ev.OutputTokens
		a.prompt += ev.PromptTokens
		a.thinking += ev.ThinkingTokens
		a.last = ev.At
		a.via = ev.ViaEngine
		a.n++
	}

	out := make([]agentRate, 0, len(by))
	for name, a := range by {
		r := agentRate{
			Agent:     name,
			Tokens:    a.tokens,
			Prompt:    a.prompt,
			Thinking:  a.thinking,
			Last:      a.last,
			ViaEngine: a.via,
		}
		// A rate needs a span. One event says how much, not how fast, so it
		// reports tokens without a rate.
		if span := a.last.Sub(a.first).Seconds(); a.n > 1 && span > 0 {
			r.TokPS = float64(a.tokens) / span
			r.PromptPS = float64(a.prompt) / span
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

// agentOwnTokPS is the output/prompt rate of tokens not already in an
// engine's totals. Skip is per event, not per agent: an agent that
// connects to (or leaves) a monitored engine mid-window still contributes
// the unattributed slice. The per-agent row keeps the last ViaEngine so
// it shows who they are talking to now.
func agentOwnTokPS(events []core.AgentEvent, now time.Time) (outPS, inPS float64) {
	own := make([]core.AgentEvent, 0, len(events))
	for _, ev := range events {
		if ev.ViaEngine == "" {
			own = append(own, ev)
		}
	}
	for _, r := range agentRates(own, now) {
		outPS += r.TokPS
		inPS += r.PromptPS
	}
	return
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
		cell := styleValue.Render(name)
		switch {
		case r.ViaEngine != "":
			cell += " " + dim("via "+shorten(core.SanitizeText(r.ViaEngine), 16))
		case r.TokPS > 0:
			cell += " " + styleOK.Render(fmtRate(r.TokPS)) + dim(" tok/s")
		default:
			// Measured tokens, but not yet a rate: say so rather than showing 0.
			cell += " " + dim(fmtCount(r.Tokens)+" tok")
		}
		parts = append(parts, cell)
	}
	return strings.Join(parts, dim("  ·  "))
}

// agentRows lays out one row per agent: name, rate, tokens, recency. Cells
// are padded by visible cells (padTo/padStart), never %-Ns width verbs:
// styled cells carry ANSI bytes whose rune counts would skew the columns.
func agentRows(rates []agentRate, now time.Time) []string {
	names := make([]string, len(rates))
	nameW := 10
	for i, r := range rates {
		names[i] = core.SanitizeText(r.Agent)
		nameW = max(nameW, lipgloss.Width(names[i]))
	}
	out := make([]string, 0, len(rates))
	for i, r := range rates {
		rate := dim("no rate yet")
		if r.TokPS > 0 {
			rate = styleValue.Foreground(heatColor(clamp01(r.TokPS / 60))).
				Render("▲ " + fmtRate(r.TokPS) + " tok/s")
		} else if r.ViaEngine != "" {
			rate = dim("via engine")
		}
		tok := dim("↓" + fmtCount(r.Tokens))
		if r.Prompt > 0 {
			tok += dim(" ↑" + fmtCount(r.Prompt))
		}
		if r.Thinking > 0 {
			tok += dim(" " + fmtCount(r.Thinking) + " think")
		}
		since := dim("idle " + fmtDur(now.Sub(r.Last).Truncate(time.Second)))
		if now.Sub(r.Last) < 3*time.Second {
			since = styleOK.Render("● live")
		}
		if r.ViaEngine != "" {
			since = dim("via "+shorten(core.SanitizeText(r.ViaEngine), 18)) + "  " + since
		}
		out = append(out, "  "+padTo(styleValue.Render(names[i]), nameW)+
			"  "+padTo(rate, 22)+"  "+padStart(tok, 18)+"  "+since)
	}
	return out
}

// agentMiniLine is the compact-strip counterpart of one agentRows cell.
func agentMiniLine(r agentRate) string {
	name := core.SanitizeText(r.Agent)
	line := styleValue.Render(name) + " "
	switch {
	case r.TokPS > 0:
		line += fmtRate(r.TokPS) + " tok/s"
	default:
		line += dim(fmtCount(r.Tokens) + " tok")
	}
	if r.ViaEngine != "" {
		line += " " + dim("via "+shorten(core.SanitizeText(r.ViaEngine), 16))
	}
	return line
}

// renderAgentsOnly is the view for a machine with agents but no engines: the
// same dashboard chrome (header, throughput, host strip) with the agents
// themselves in place of the backend panels. It is a real dashboard, not a
// placeholder, because for someone driving claude or codex all day this is
// the whole picture.
func (m Model) renderAgentsOnly() string {
	w := m.w - 4
	rates := agentRates(m.snap.Agents, m.clock)
	_, midIn, feedIn := m.sectionHeights()

	rows := agentRows(rates, m.clock)
	if len(rows) == 0 {
		rows = append(rows, dim("  waiting for an agent to report tokens…"))
	}

	title := "AGENTS"
	if hint := dim("  local, read from their own session logs"); m.w >= 78 {
		title += hint
	}

	feed := feedLines(m.snap.Agents, feedIn, w)
	if len(feed) == 0 {
		feed = append(feed, dim("no agent activity yet"))
	}

	body := lipgloss.JoinVertical(lipgloss.Left,
		m.renderHeader(),
		"",
		m.renderCharts(),
		m.renderSystem(),
		panel(title, strings.Join(rows, "\n"), w, midIn),
		panel("AGENT FEED", strings.Join(feed, "\n"), w, feedIn),
	)
	return composeFrame(body, m.renderFooter(), m.w, m.h)
}

// agentDenseHist buckets unattributed agent tokens onto a uniform cadence
// grid ending at `end`: each column is tok/s for that interval. Events marked
// ViaEngine are skipped; the engine's own history already carries them.
func agentDenseHist(events []core.AgentEvent, out bool, end time.Time, n int, cadence time.Duration) []float64 {
	if n <= 0 || cadence <= 0 || end.IsZero() {
		return nil
	}
	grid := make([]float64, n)
	start := end.Add(-time.Duration(n-1) * cadence)
	sec := cadence.Seconds()
	if sec <= 0 {
		return grid
	}
	half := cadence / 2
	for _, ev := range events {
		if ev.ViaEngine != "" {
			continue
		}
		tok := ev.OutputTokens
		if !out {
			tok = ev.PromptTokens
		}
		if tok <= 0 {
			continue
		}
		d := ev.At.Sub(start)
		idx := int((d + half) / cadence)
		if idx < 0 || idx >= n {
			continue
		}
		grid[idx] += float64(tok) / sec
	}
	return grid
}

func agentHistEnd(events []core.AgentEvent) time.Time {
	var end time.Time
	for _, ev := range events {
		if ev.ViaEngine != "" {
			continue
		}
		if ev.At.After(end) {
			end = ev.At
		}
	}
	return end
}
