package ui

import (
	"fmt"
	"math"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/maci0/toktop/internal/core"
)

// Palette matches the toktop.ai tokens in site/worker.js (cool dark, green
// accent, amber pressure). Degrades to nearest 256/16 colors on old terminals.
var (
	cBase = lipgloss.Color("#0d1117")
	// Secondary text must stay >= 4.5:1 on cBase (WCAG 1.4.3). Site --dim
	// #7d8895 is 5.25:1 here; do not drop it back under the floor.
	cDim    = lipgloss.Color("#7d8895")
	cText   = lipgloss.Color("#d7dde5")
	cBorder = lipgloss.Color("#4a5563")
	cRed    = lipgloss.Color("#e36d6d")
	cGreen  = lipgloss.Color("#4cc38a") // --accent
	cYellow = lipgloss.Color("#e3b341") // --warm
	cBlue   = lipgloss.Color("#7aa2d4")
	cCyan   = lipgloss.Color("#5ec8d8")
)

var (
	styleTitle = lipgloss.NewStyle().Bold(true).Foreground(cText)
	styleDim   = lipgloss.NewStyle().Foreground(cDim)
	styleValue = lipgloss.NewStyle().Bold(true).Foreground(cText)

	styleOK   = lipgloss.NewStyle().Foreground(cGreen)
	styleWarn = lipgloss.NewStyle().Foreground(cYellow)
	styleBad  = lipgloss.NewStyle().Foreground(cRed)
	styleInfo = lipgloss.NewStyle().Foreground(cCyan)

	dotUp = styleOK.Render("●")
	// ✗, not a red ●: down must read without color (WCAG 1.4.1), and it
	// matches the ✓/✗ convention the probe rows already use.
	dotBad = styleBad.Render("✗")
	// Partial-up keeps the dot shape: the header spells the count out
	// numerically right beside it ("2/3 engines"), so color is redundant.
	dotWarn = styleWarn.Render("●")

	kindStyles = map[string]lipgloss.Style{
		core.KindOllama:    lipgloss.NewStyle().Foreground(cYellow),
		core.KindVLLM:      lipgloss.NewStyle().Foreground(cGreen),
		core.KindLlamaCPP:  lipgloss.NewStyle().Foreground(cCyan),
		core.KindOpenAI:    lipgloss.NewStyle().Foreground(cBlue),
		core.KindSGLang:    lipgloss.NewStyle().Foreground(cBlue),
		core.KindTRTLLM:    lipgloss.NewStyle().Foreground(cGreen),
		core.KindMLX:       lipgloss.NewStyle().Foreground(cCyan),
		core.KindLMStudio:  lipgloss.NewStyle().Foreground(cBlue),
		core.KindKoboldCPP: lipgloss.NewStyle().Foreground(cYellow),
		core.KindLocalAI:   lipgloss.NewStyle().Foreground(cCyan),
		core.KindTGI:       lipgloss.NewStyle().Foreground(cGreen),
		core.KindLiteLLM:   lipgloss.NewStyle().Foreground(cBlue),
		core.KindGPUStack:  lipgloss.NewStyle().Foreground(cGreen),
		core.KindLemonade:  lipgloss.NewStyle().Foreground(cYellow),
		// Routing proxies share blue (litellm); OmniRoute is detected
		// via its X-OmniRoute-Route-Class header and must not fall through
		// to the dim unknown-kind badge.
		core.KindOmniRoute: lipgloss.NewStyle().Foreground(cBlue),
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

// heatColor maps 0..1 intensity onto a cool-to-hot ramp (cyan, green, amber,
// red). The quiet end floors at cDim: the ramp also colors text (header
// and per-agent rates) that must hold 4.5:1 on cBase (WCAG 1.4.3), and its
// near-zero cells are data-bearing chart marks bound by the same 3:1 floor
// as the faded columns (WCAG 1.4.11). A surface-gray floor measured ~1.3:1,
// invisible to low-vision users exactly when the rate it labels is small
// next to the session peak.
func heatColor(f float64) lipgloss.Color {
	switch {
	case f <= 0.02:
		return cDim
	case f < 0.30:
		return cCyan
	case f < 0.58:
		return cGreen
	case f < 0.82:
		return cYellow
	default:
		return cRed
	}
}

// wordmark is TOKTOP in the site accent. Static: built once, reused every frame.
var wordmark = lipgloss.NewStyle().Bold(true).Foreground(cGreen).Render("TOKTOP")

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
