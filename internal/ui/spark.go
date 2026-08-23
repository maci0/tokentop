package ui

import (
	"fmt"
	"strconv"
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

// brailleBits maps (sub-row, sub-column) within a braille cell to its Unicode
// bit: dot1..8 = 0x01,0x02,0x04,0x08,0x10,0x20,0x40,0x80.
var brailleBits = [4][2]byte{
	{0x01, 0x08},
	{0x02, 0x10},
	{0x04, 0x20},
	{0x40, 0x80},
}

// ChartStyle tunes BrailleChart rendering.
type ChartStyle struct {
	Heat    func(float64) lipgloss.Color
	FadeAge bool         // bloom: older columns melt into the background
	Grid    map[int]bool // columns marked true get a faint vertical guide
}

// fadeColor blends a hex color toward black by factor f (0..1). Non-hex
// colors pass through untouched.
func fadeColor(c lipgloss.Color, f float64) string {
	s := string(c)
	if !strings.HasPrefix(s, "#") || len(s) != 7 {
		return s
	}
	v, err := strconv.ParseUint(s[1:], 16, 32)
	if err != nil {
		return s
	}
	r := uint64(float64(v>>16&0xff) * f)
	g := uint64(float64(v>>8&0xff) * f)
	b := uint64(float64(v&0xff) * f)
	return fmt.Sprintf("#%02x%02x%02x", r, g, b)
}

// BrailleChart renders an area chart as braille dot-matrix, btop-style: every
// terminal cell is a 2x4 dot grid, so a w*h chart resolves w*2 by h*4 dots -
// far finer than the block ramp. Values fill upward from the baseline. With
// FadeAge, older columns fade toward black; Grid columns draw a faint dotted
// baseline guide through empty cells (used to mark timescale boundaries).
func BrailleChart(vals []float64, w, h int, st ChartStyle) string {
	if w <= 0 || h <= 0 {
		return ""
	}
	heat := st.Heat
	if heat == nil {
		heat = func(float64) lipgloss.Color { return lipgloss.Color("") }
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
	pad := w - len(vs)
	if pad < 0 {
		pad = 0
	}
	cols := make([]float64, pad+len(vs))
	copy(cols[pad:], vs)

	dotH := h * 4
	type cacheKey struct {
		color   string
		pattern int
	}
	cache := map[cacheKey]string{}
	rows := make([]strings.Builder, h)
	for cy := 0; cy < h; cy++ {
		for cx := 0; cx < w; cx++ {
			frac := clamp01(cols[cx] / max)
			pattern := 0
			level := frac * float64(dotH)
			for sr := 0; sr < 4; sr++ {
				dy := cy*4 + sr
				if float64(dotH-dy) <= level {
					pattern |= int(brailleBits[sr][0]) | int(brailleBits[sr][1])
				}
			}
			// faint guides only where data leaves the bottom row empty
			if pattern == 0 && cy == h-1 && st.Grid[cx] {
				pattern = int(brailleBits[3][0])
			}
			col := heat(frac)
			if st.FadeAge && frac > 0.02 {
				f := 0.30 + 0.70*(float64(cx)/float64(maxi(w-1, 1)))
				col = lipgloss.Color(fadeColor(col, f))
			}
			k := cacheKey{color: string(col), pattern: pattern}
			s, ok := cache[k]
			if !ok {
				r := ' '
				if pattern != 0 {
					r = rune(0x2800 + pattern)
				}
				s = lipgloss.NewStyle().Foreground(col).Render(string(r))
				cache[k] = s
			}
			rows[cy].WriteString(s)
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
