package ui

import (
	"fmt"
	"math"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

// wcagLuminance parses #rrggbb into its WCAG 2.x relative luminance.
func wcagLuminance(t testing.TB, c lipgloss.Color) float64 {
	t.Helper()
	s := string(c)
	if len(s) != 7 || s[0] != '#' {
		t.Fatalf("color %q is not #rrggbb", s)
	}
	var v uint32
	if _, err := fmt.Sscanf(s[1:], "%06x", &v); err != nil {
		t.Fatalf("bad hex %q: %v", s, err)
	}
	lin := func(ch uint32) float64 {
		f := float64(ch) / 255
		if f <= 0.03928 {
			return f / 12.92
		}
		return math.Pow((f+0.055)/1.055, 2.4)
	}
	r, g, b := lin(uint32(v>>16)&0xff), lin(uint32(v>>8)&0xff), lin(uint32(v)&0xff)
	return 0.2126*r + 0.7152*g + 0.0722*b
}

// contrastRatio orders two colors and returns their WCAG contrast ratio.
func contrastRatio(t testing.TB, a, b lipgloss.Color) float64 {
	t.Helper()
	la, lb := wcagLuminance(t, a), wcagLuminance(t, b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

// Secondary text rides on the base background everywhere: footer key hints,
// help rows, gauge tracks, unit labels. WCAG 1.4.3 needs 4.5:1 there; the
// palette must not drift back under it.
func TestDimTextMeetsAAContrastOnBase(t *testing.T) {
	if got := contrastRatio(t, cDim, cBase); got < 4.5 {
		t.Errorf("cDim on cBase = %.2f:1, want >= 4.5:1", got)
	}
}

// Body text and the status colors carry primary values; keep them comfortably
// above AA too so future palette edits fail loudly here rather than on screen.
func TestStatusTextMeetsAAContrastOnBase(t *testing.T) {
	for name, c := range map[string]lipgloss.Color{
		"cText":     cText,
		"cGreen":    cGreen,
		"cYellow":   cYellow,
		"cRed":      cRed,
		"cCyan":     cCyan,
		"cPink":     cPink,
		"cMagenta":  cMagenta,
		"cBlue":     cBlue,
		"cPeach":    cPeach,
		"cLavender": cLavender,
		"cTeal":     cTeal,
	} {
		if got := contrastRatio(t, c, cBase); got < 4.5 {
			t.Errorf("%s on cBase = %.2f:1, want >= 4.5:1", name, got)
		}
	}
}
