// Package ui renders the tokentop dashboard.
package ui

import (
	"strings"
	"sync"

	"github.com/charmbracelet/lipgloss"
)

// Catppuccin-mocha-leaning palette; degrades to nearest 256/16 colors on old terminals.
var (
	cBase     = lipgloss.Color("#1e1e2e")
	cSurface  = lipgloss.Color("#313244")
	cDim      = lipgloss.Color("#6c7086")
	cText     = lipgloss.Color("#cdd6f4")
	cBorder   = lipgloss.Color("#45475a")
	cRed      = lipgloss.Color("#f38ba8")
	cGreen    = lipgloss.Color("#a6e3a1")
	cYellow   = lipgloss.Color("#f9e2af")
	cPeach    = lipgloss.Color("#fab387")
	cBlue     = lipgloss.Color("#89b4fa")
	cCyan     = lipgloss.Color("#89dceb")
	cTeal     = lipgloss.Color("#94e2d5")
	cMagenta  = lipgloss.Color("#cba6f7")
	cPink     = lipgloss.Color("#f5c2e7")
	cLavender = lipgloss.Color("#b4befe")
)

var (
	styleTitle = lipgloss.NewStyle().Bold(true).Foreground(cLavender)
	styleDim   = lipgloss.NewStyle().Foreground(cDim)
	styleValue = lipgloss.NewStyle().Bold(true).Foreground(cText)

	styleOK    = lipgloss.NewStyle().Foreground(cGreen)
	styleWarn  = lipgloss.NewStyle().Foreground(cYellow)
	styleBad   = lipgloss.NewStyle().Foreground(cRed)
	styleInfo  = lipgloss.NewStyle().Foreground(cCyan)
	styleHot   = lipgloss.NewStyle().Foreground(cPink)
	styleMagic = lipgloss.NewStyle().Foreground(cMagenta)

	dotUp   = styleOK.Render("●")
	dotBad  = styleBad.Render("●")
	dotWarn = styleWarn.Render("●")

	kindStyles = map[string]lipgloss.Style{
		"ollama":    lipgloss.NewStyle().Foreground(cPeach),
		"vllm":      lipgloss.NewStyle().Foreground(cMagenta),
		"llama.cpp": lipgloss.NewStyle().Foreground(cCyan),
		"openai":    lipgloss.NewStyle().Foreground(cBlue),
		"sglang":    lipgloss.NewStyle().Foreground(cLavender),
		"trt-llm":   lipgloss.NewStyle().Foreground(cGreen),
		"mlx":       lipgloss.NewStyle().Foreground(cPink),
		"lmstudio":  lipgloss.NewStyle().Foreground(cBlue),
		"koboldcpp": lipgloss.NewStyle().Foreground(cYellow),
		"localai":   lipgloss.NewStyle().Foreground(cTeal),
		"tgi":       lipgloss.NewStyle().Foreground(cGreen),
		"oobabooga": lipgloss.NewStyle().Foreground(cPeach),
		"tabbyapi":  lipgloss.NewStyle().Foreground(cMagenta),
		"litellm":   lipgloss.NewStyle().Foreground(cLavender),
		"gpustack":  lipgloss.NewStyle().Foreground(cTeal),
		"lemonade":  lipgloss.NewStyle().Foreground(cYellow),
		// Routing proxies share lavender (litellm); OmniRoute is detected
		// via its X-OmniRoute-Route-Class header and must not fall through
		// to the dim unknown-kind badge.
		"omnirouter": lipgloss.NewStyle().Foreground(cLavender),
		"demo":       lipgloss.NewStyle().Foreground(cPink),
	}

	panelStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("#45475a")).
			Padding(0, 1)

	helpStyle = lipgloss.NewStyle().
			Border(lipgloss.NormalBorder()).
			BorderForeground(cDim).
			Padding(1, 2).
			Background(cBase)
)

// panel wraps content in a titled rounded box. Content is padded/cut to
// innerW x innerH with plain spaces; we deliberately avoid lipgloss Width()
// here because its wrapping mishandles densely styled chart cells.
func panel(title, content string, innerW, innerH int) string {
	body := panelStyle.Render(padBlock(content, innerW, innerH))
	return styleTitle.Render(title) + "\n" + body
}

// padBlock forces content to exactly innerW columns and innerH rows.
func padBlock(content string, innerW, innerH int) string {
	lines := strings.Split(content, "\n")
	if len(lines) > innerH {
		lines = lines[:innerH]
	}
	for i, ln := range lines {
		if gap := innerW - lipgloss.Width(ln); gap > 0 {
			ln += strings.Repeat(" ", gap)
		} else {
			ln = clip(ln, innerW)
		}
		lines[i] = ln
	}
	for len(lines) < innerH {
		lines = append(lines, strings.Repeat(" ", innerW))
	}
	return strings.Join(lines, "\n")
}

// kindBadge renders a fixed-width colored tag for a backend kind.
func kindBadge(kind string) string {
	st, ok := kindStyles[kind]
	if !ok {
		st = styleDim
	}
	return st.Render(fmtKind(kind))
}

func fmtKind(k string) string {
	for len(k) < 9 {
		k += " "
	}
	return k[:9]
}

// heatColor maps 0..1 intensity onto a cold->hot ramp.
func heatColor(f float64) lipgloss.Color {
	switch {
	case f <= 0.02:
		return cSurface
	case f < 0.25:
		return cTeal
	case f < 0.5:
		return cCyan
	case f < 0.72:
		return cGreen
	case f < 0.88:
		return cYellow
	default:
		return cRed
	}
}

// wordmark renders the logo with a cyan→pink gradient. Static output:
// built once, then reused by every frame.
var wordmark = sync.OnceValue(func() string {
	letters := []rune("TOKENTOP")
	colors := []lipgloss.Color{cTeal, cCyan, cBlue, cLavender, cMagenta, cPink, cPeach, cYellow}
	var b strings.Builder
	for i, l := range letters {
		b.WriteString(lipgloss.NewStyle().Bold(true).Foreground(colors[i%len(colors)]).Render(string(l)))
	}
	return b.String()
})
