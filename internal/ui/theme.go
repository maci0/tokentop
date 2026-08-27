package ui

import (
	"fmt"
	"math"
	"strings"
	"sync"

	"github.com/charmbracelet/lipgloss"
)

// Catppuccin-mocha-leaning palette; degrades to nearest 256/16 colors on old terminals.
var (
	cBase = lipgloss.Color("#1e1e2e")
	// Secondary text must stay >= 4.5:1 on cBase (WCAG 1.4.3): the old
	// #6c7086 measured ~3.4:1. overlay1 keeps hierarchy below cText.
	cDim      = lipgloss.Color("#9399b2")
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

	dotUp = styleOK.Render("●")
	// ✗, not a red ●: down must read without color (WCAG 1.4.1), and it
	// matches the ✓/✗ convention the probe rows already use.
	dotBad = styleBad.Render("✗")
	// Partial-up keeps the dot shape: the header spells the count out
	// numerically right beside it ("2/3 engines"), so color is redundant.
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
		"litellm":   lipgloss.NewStyle().Foreground(cLavender),
		"gpustack":  lipgloss.NewStyle().Foreground(cTeal),
		"lemonade":  lipgloss.NewStyle().Foreground(cYellow),
		// Routing proxies share lavender (litellm); OmniRoute is detected
		// via its X-OmniRoute-Route-Class header and must not fall through
		// to the dim unknown-kind badge.
		"omnirouter": lipgloss.NewStyle().Foreground(cLavender),
	}

	panelStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(cBorder).
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
	return fmt.Sprintf("%-9.9s", k)
}

// heatColor maps 0..1 intensity onto a cold->hot ramp. The quiet end floors
// at cDim, not the surface gray #313244: the ramp also colors text (header
// and per-agent rates) that must hold 4.5:1 on cBase (WCAG 1.4.3), and its
// near-zero cells are data-bearing chart marks bound by the same 3:1 floor
// as the faded columns (WCAG 1.4.11). The surface gray measured ~1.3:1,
// invisible to low-vision users exactly when the rate it labels is small
// next to the session peak.
func heatColor(f float64) lipgloss.Color {
	switch {
	case f <= 0.02:
		return cDim
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
	letters := []rune("TOKTOP")
	colors := []lipgloss.Color{cTeal, cCyan, cBlue, cLavender, cMagenta, cPink, cPeach, cYellow}
	var b strings.Builder
	for i, l := range letters {
		b.WriteString(lipgloss.NewStyle().Bold(true).Foreground(colors[i%len(colors)]).Render(string(l)))
	}
	return b.String()
})

// relLuminance computes the WCAG 2.x relative luminance of a #rrggbb hex
// color. ok is false for any other encoding (256-color names): callers must
// treat those as already visible rather than guessing at their brightness.
func relLuminance(c lipgloss.Color) (lum float64, ok bool) {
	s := string(c)
	if len(s) != 7 || s[0] != '#' {
		return 0, false
	}
	var v uint32
	if _, err := fmt.Sscanf(s[1:], "%06x", &v); err != nil {
		return 0, false
	}
	lin := func(ch uint32) float64 {
		f := float64(ch) / 255
		if f <= 0.03928 {
			return f / 12.92
		}
		return math.Pow((f+0.055)/1.055, 2.4)
	}
	return 0.2126*lin(v>>16&0xff) + 0.7152*lin(v>>8&0xff) + 0.0722*lin(v&0xff), true
}

// contrastRatio returns the WCAG contrast ratio between two colors; ok is
// false when either side is not #rrggbb (see relLuminance).
func contrastRatio(a, b lipgloss.Color) (ratio float64, ok bool) {
	la, oka := relLuminance(a)
	lb, okb := relLuminance(b)
	if !oka || !okb {
		return 0, false
	}
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05), true
}
