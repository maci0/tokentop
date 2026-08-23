package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// rampRunes of 9 glyphs: index 0 is empty, 8 fully filled. Each step covers
// 1/8 cell.
var rampRunes = []rune(" ▁▂▃▄▅▆▇█")

// LevelChar maps a subcell level (0..8) to its rune.
func LevelChar(level int) rune {
	if level < 0 {
		level = 0
	}
	if level > 8 {
		level = 8
	}
	return rampRunes[level]
}

// Sparkline renders values as one row, width w, uncolored (style the result).
func Sparkline(vals []float64, w int) string {
	return AreaChart(vals, w, 1, func(float64) lipgloss.Color { return lipgloss.Color("") })
}

// AreaChart renders a multi-row column chart of the last w values scaled to
// the max, colored per column by heatColor(frac).
func AreaChart(vals []float64, w, h int, heat func(float64) lipgloss.Color) string {
	if w <= 0 || h <= 0 {
		return ""
	}
	vs := vals
	if len(vs) > w {
		vs = vs[len(vs)-w:]
	}
	max := 0.0
	for _, v := range vs {
		max = maxf(max, v)
	}
	if max <= 0 {
		max = 1
	}
	// left-pad so charts hug the right edge like a scope trace
	pad := w - len(vs)
	if pad < 0 {
		pad = 0
	}
	cols := make([]float64, pad+len(vs))
	copy(cols[pad:], vs)

	rows := make([]strings.Builder, h)
	type cacheKey struct{ band, level int }
	cache := map[cacheKey]string{}
	for r := 0; r < h; r++ {
		base := (h - 1 - r) * 8 // subcell offset of this row's bottom
		for _, v := range cols {
			frac := v / max
			band := int(frac * 6) // 6 heat bands
			level := int(frac*float64(h*8)) - base
			ch := LevelChar(level)
			key := cacheKey{band, level}
			s, ok := cache[key]
			if !ok {
				style := lipgloss.NewStyle().Foreground(heat(clamp01(frac)))
				if ch == ' ' {
					s = style.Render(" ")
				} else {
					s = style.Render(string(ch))
				}
				cache[key] = s
			}
			rows[r].WriteString(s)
		}
	}
	out := make([]string, h)
	for i := range rows {
		out[i] = rows[i].String()
	}
	return strings.Join(out, "\n")
}

// GaugeBar renders "[██████░░░░] 62%"-style meter content without label.
func GaugeBar(pct float64, w int, heat func(float64) lipgloss.Color) string {
	if w < 3 {
		w = 3
	}
	pct = clamp01(pct/100) * 100
	filled := int(pct / 100 * float64(w))
	if filled > w {
		filled = w
	}
	st := lipgloss.NewStyle().Foreground(heat(pct))
	bar := st.Render(strings.Repeat("━", filled)) + styleDim.Render(strings.Repeat("─", w-filled))
	return bar + " " + fmt.Sprintf("%.0f%%", pct)
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

func maxf(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}
