// Package ui renders the toktop dashboard.
package ui

import (
	"fmt"
	"math/bits"
	"sort"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/maci0/toktop/internal/core"
)

// Prober fires synthetic generation probes on demand.
type Prober interface {
	ProbeAll()
}

// Config wires the dashboard to its data source.
type Config struct {
	Version    string
	Demo       bool
	IngestAddr string
	PollEvery  time.Duration // sampling cadence; anchors the chart timescale
	Prober     Prober        // nil disables manual probing
	// Agents reports that local agent watching (--agents) is on: only then
	// may empty-feed guidance promise that running agents are picked up.
	Agents bool
	// FeedErr receives one message if the ingest endpoint dies after startup.
	// nil (or silent) means it is up: stderr is invisible under the alternate
	// screen, so without this in-band signal the UI would advertise a dead
	// endpoint forever.
	FeedErr <-chan string
}

type Model struct {
	cfg             Config
	ch              <-chan core.Snapshot
	snap            core.Snapshot
	w, h            int
	ready           bool
	paused          bool
	help            bool
	clock           time.Time
	maxAgg          float64
	lastAgg         float64 // most recent aggregate output rate across engines
	chartCompressed bool
	probeReq        time.Time // manual probe awaiting its first result
	feedDown        string    // set once the ingest endpoint has died
}

func New(cfg Config, ch <-chan core.Snapshot) Model {
	// The header clock only advances on ticks, so until the first one lands
	// (~1s in) it must show the launch time rather than a zero-value midnight.
	return Model{cfg: cfg, ch: ch, chartCompressed: chartCompressedDefault, clock: time.Now()}
}

// StaticFrame renders one snapshot for non-interactive output (--once).
func StaticFrame(cfg Config, s core.Snapshot, w, h int) string {
	m := New(cfg, nil)
	m.snap = s
	m.w, m.h = w, h
	m.ready = true
	m.clock = frameNow(s, m.clock)
	if agg := aggOutAt(s, m.clock); agg > 0 {
		m.lastAgg = agg
		m.maxAgg = agg
	}
	return m.View()
}

// --- messages -------------------------------------------------------------

type snapMsg core.Snapshot
type tickMsg time.Time

// feedDownMsg reports the ingest endpoint died after startup; the payload is
// the server's error text.
type feedDownMsg string

func waitSnap(ch <-chan core.Snapshot) tea.Cmd {
	return func() tea.Msg { return snapMsg(<-ch) }
}

// waitFeedErr blocks until the feed dies; re-issued after each delivery so a
// restart-and-resignal cycle is still observed. A nil channel never fires.
func waitFeedErr(ch <-chan string) tea.Cmd {
	return func() tea.Msg {
		err, ok := <-ch
		if !ok {
			return nil
		}
		return feedDownMsg(err)
	}
}

func tickClock() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

// --- model ----------------------------------------------------------------

func (m Model) Init() tea.Cmd {
	cmds := []tea.Cmd{tickClock(), waitSnap(m.ch)}
	if m.cfg.FeedErr != nil {
		cmds = append(cmds, waitFeedErr(m.cfg.FeedErr))
	}
	return tea.Batch(cmds...)
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		m.ready = true
		return m, nil

	case tickMsg:
		// A paused frame must be genuinely still: the header clock is the
		// only element that kept changing every second, churning the screen
		// for anyone pausing to read it with a screen reader or magnifier.
		if !m.paused {
			m.clock = time.Time(msg)
		}
		// Engines that never answer (no known model yet, all down) would
		// leave the "probing…" marker up forever without this bail-out.
		if !m.probeReq.IsZero() && m.clock.Sub(m.probeReq) > 15*time.Second {
			m.probeReq = time.Time{}
		}
		return m, tickClock()

	case feedDownMsg:
		m.feedDown = string(msg)
		return m, waitFeedErr(m.cfg.FeedErr)

	case snapMsg:
		if !m.paused {
			in := core.Snapshot(msg)
			agg := aggOutAt(in, m.clock)
			m.lastAgg = agg
			if agg > m.maxAgg {
				m.maxAgg = agg
			}
			if n := len(in.Probes); n > 0 && in.Probes[n-1].At.After(m.probeReq) {
				m.probeReq = time.Time{} // first result landed: hand over to it
			}
			m.snap = in
		}
		return m, waitSnap(m.ch)

	case tea.KeyMsg:
		key := msg.String()
		// Help is a full-screen replacement view: action keys must not act
		// blind on the dashboard it covers (space silently paused mid-read,
		// p fired real probe generations). Only the dismiss and toggle keys
		// stay live while help is up.
		if m.help && key != "q" && key != "ctrl+c" && key != "esc" &&
			key != "?" && key != "h" {
			return m, nil
		}
		switch key {
		case "q", "ctrl+c", "esc":
			if m.help {
				m.help = false
				return m, nil
			}
			return m, tea.Quit
		case " ", "space":
			m.paused = !m.paused
			return m, nil
		case "p", "P":
			if m.cfg.Prober != nil {
				go m.cfg.Prober.ProbeAll()
				if len(m.snap.Providers) > 0 {
					m.probeReq = m.clock
				}
			}
			return m, nil
		case "t", "T":
			m.chartCompressed = !m.chartCompressed
			return m, nil
		case "?", "h":
			m.help = !m.help
			return m, nil
		}
	}
	return m, nil
}

// --- view ------------------------------------------------------------------

func (m Model) View() string {
	if !m.ready {
		// Same glyph the probe panel uses for work in progress: the status
		// vocabulary stays monochrome terminal glyphs, no color emoji.
		return "\n  " + styleWarn.Render("● toktop is warming up…")
	}
	if m.help {
		return m.renderHelp()
	}
	// minDashH is the shortest pane where the full layout still covers its own
	// chrome (header, titles, borders, footer, one system strip row) plus the
	// smallest panel split from sectionHeights. Below it - or narrower than
	// minDashW - a squeezed full frame overflows and bubbletea clips it from
	// the top, hiding the header; the compact view stays honest instead.
	const (
		minDashW = 62
		minDashH = 30
	)
	if m.w < minDashW || m.h < minDashH {
		return m.renderMinimal()
	}
	if len(m.snap.Providers) == 0 {
		if len(m.snap.Agents) > 0 {
			// Engines are not the only thing that burns tokens: an agent
			// running here is real activity, and hiding it behind the
			// setup screen would be the dashboard lying by omission.
			return m.renderAgentsOnly()
		}
		return m.renderEmpty()
	}
	body := lipgloss.JoinVertical(lipgloss.Left,
		m.renderHeader(),
		"",
		m.renderCharts(),
		m.renderSystem(),
		m.renderMidRow(),
		m.renderFeed(),
	)
	footer := m.renderFooter()
	// Reserve an explicit row for the footer: concatenating onto unpadded
	// body made it ride the last content line and wrap past the pane edge.
	if gap := m.h - lipgloss.Height(body) - lipgloss.Height(footer) - 1; gap > 0 {
		body += strings.Repeat("\n", gap)
	}
	return body + "\n" + footer
}

func (m Model) renderHeader() string {
	logo := wordmark()
	segs := []headerSeg{{text: logo}, {text: dim("v" + m.cfg.Version), shed: 40}}

	up, tot := m.upCount()
	rates := agentRates(m.snap.Agents, m.clock)
	if tot == 0 {
		n := len(rates)
		if n == 0 {
			n = uniqueAgents(m.snap.Agents)
		}
		label := fmt.Sprintf("%d agents", n)
		if n == 1 {
			label = "1 agent"
		}
		st := styleOK
		if n == 0 {
			st = styleWarn
		}
		segs = append(segs, headerSeg{text: st.Render(label)})
	} else {
		dot := dotUp
		st := styleOK
		switch {
		case up == 0:
			dot, st = dotBad, styleBad
		case up < tot:
			dot, st = dotWarn, styleWarn
		}
		segs = append(segs, headerSeg{text: st.Render(fmt.Sprintf("%s %d/%d engines", strip(dot), up, tot))})
		if n := len(rates); n > 0 {
			label := fmt.Sprintf("%d agents", n)
			if n == 1 {
				label = "1 agent"
			}
			segs = append(segs, headerSeg{text: dim(label), shed: 45})
		}
	}

	outV := styleValue.Foreground(heatColor(norm(m.lastAgg, m.maxAgg))).Render("▲ " + fmtRate(m.lastAgg))
	inV := styleInfo.Render("▼ " + fmtRate(aggInAt(m.snap, m.clock)))
	segs = append(segs,
		headerSeg{text: outV + " " + dim("tok/s out"), shed: 10},
		headerSeg{text: inV + " " + dim("in"), shed: 20},
	)

	if up > 0 || tot > 0 {
		segs = append(segs, headerSeg{text: dim("up " + fmtDur(m.snap.Uptime)), shed: 50})
	}
	if m.snap.Sys != nil && m.snap.Sys.RemoteHost != "" {
		segs = append(segs, headerSeg{text: styleMagic.Render("via ssh:" + core.SanitizeText(m.snap.Sys.RemoteHost))})
	}

	right := ""
	if m.paused {
		right += styleWarn.Render("‖ PAUSED ") + dim("│ ")
	}
	right += styleMagic.Render(m.clock.Format("15:04:05"))
	left := fitSegments(segs, m.w-lipgloss.Width(right)-1)
	return joinSpread(left, right, m.w)
}

// headerSeg is one header chunk plus how eagerly it yields space on narrow
// panes: 0 pins the segment, larger numbers shed sooner.
type headerSeg struct {
	text string
	shed int
}

// fitSegments sheds the highest-numbered segments (rightmost first) until
// the dim-piped row fits avail cells. When nothing sheddable remains it hard
// clips as a last resort: even one wrapping cell drags every later frame line
// out of alignment on terminals narrower than the row.
func fitSegments(segs []headerSeg, avail int) string {
	if avail <= 0 || len(segs) == 0 {
		return ""
	}
	src := make([]headerSeg, len(segs))
	copy(src, segs)
	width := func(ss []headerSeg) int {
		n := 0
		for i, s := range ss {
			if i > 0 {
				n += lipgloss.Width(dim(" │ "))
			}
			n += lipgloss.Width(s.text)
		}
		return n
	}
	for len(src) > 1 && width(src) > avail {
		worst, idx := 0, -1
		for i, s := range src {
			if s.shed >= worst && s.shed > 0 {
				worst, idx = s.shed, i
			}
		}
		if idx < 0 {
			break
		}
		src = append(src[:idx], src[idx+1:]...)
	}
	parts := make([]string, len(src))
	for i, s := range src {
		parts[i] = s.text
	}
	line := strings.Join(parts, dim(" │ "))
	if w := lipgloss.Width(line); w > avail {
		line = clip(line, avail)
	}
	return line
}

// minIdentH is the shortest pane that still affords a second system strip
// row; below it identity and sensors yield rather than pushing the frame
// past the pane edge.
const minIdentH = 31

// stripTwoRows reports whether the pane affords the system strip's second
// row. It must agree with renderSystem's row-2 gate or the height budget
// lies by one row; erring toward fewer rendered rows than budgeted is safe
// (the leftover becomes padding), the opposite direction overflows.
func (m Model) stripTwoRows() bool {
	return m.snap.Sys != nil && m.h >= minIdentH
}

// systemStripRows is the total height of the system strip: border (2) plus
// one row of vitals, plus a second identity row when stripTwoRows says so.
func (m Model) systemStripRows() int {
	if m.stripTwoRows() {
		return 4
	}
	return 3
}

// sectionHeights splits the body into exact inner heights for the throughput
// chart, the mid-row panels and the agent feed. Fixed chrome is computed from
// the header, blank spacer, three panel titles, box borders, the prompt chart
// row, system strip and footer: outH+midIn+feedIn must sum to exactly f or
// the frame overflows the pane and bubbletea clips it from the top, hiding
// the header. Minimums reshuffle the split but never change the sum.
func (m Model) sectionHeights() (outH, midIn, feedIn int) {
	f := max(m.h-17-m.systemStripRows(), 10)
	outH = clampi(int(float64(f)*0.42), 3, 99)
	feedIn = clampi(int(float64(f)*0.22), 2, 12)
	midIn = f - outH - feedIn
	if midIn < 5 { // mid-row panels need room for three detail lines
		outH -= 5 - midIn
		midIn = 5
		if outH < 3 { // charts bottomed out: take the rest from the feed
			feedIn -= 3 - outH
			outH = 3
		}
	}
	return outH, midIn, feedIn
}

func (m Model) renderCharts() string {
	w := m.w - 4 // panel borders + padding
	cad := m.chartCadence()
	outH, _, _ := m.sectionHeights()
	agg, grid := m.outSeries(w, cad)
	out := panel(
		m.throughputTitle(),
		BrailleChart(agg, w, outH, ChartStyle{Heat: heatColor, FadeAge: true, Grid: grid}),
		w, outH,
	)
	in := panel(
		"PROMPT "+styleInfo.Render("▼ "+fmtRate(aggInAt(m.snap, m.clock))+" tok/s"),
		BrailleChart(aggHist(m.snap, false, w, cad), w, 1,
			ChartStyle{Heat: func(float64) lipgloss.Color { return cTeal }, FadeAge: true}),
		w, 1,
	)
	return out + "\n" + in
}

// chartCompressedDefault is on: recent seconds stay detailed while older
// history compresses leftward, btop-zoom style. 't' toggles it.
const chartCompressedDefault = true

// compressBlock is how many columns share the same timespan before it
// doubles moving left.
const compressBlock = 12

// outSeries produces the throughput series plus grid boundaries for the
// active timescale mode.
func (m Model) outSeries(w int, cadence time.Duration) ([]float64, map[int]bool) {
	if !m.chartCompressed {
		return aggHist(m.snap, true, w, cadence), nil
	}
	vals, bounds := compressSeries(timedSeries(m.snap, true, cadence), w, compressBlock)
	return vals, bounds
}

func (m Model) throughputTitle() string {
	kind := dim("  decode")
	if len(m.snap.Providers) == 0 {
		kind = dim("  output")
	}
	title := "THROUGHPUT " + styleHot.Render("▲ "+fmtRate(m.lastAgg)+" tok/s") + kind
	// Advertise the toggle in both modes: the hint only showing while
	// compressed hid how to get back to the uniform timescale. Brackets mark
	// the key so "compressed [t]" reads as mode plus switch rather than one
	// run-together token.
	mode := dim(" · compressed ")
	if !m.chartCompressed {
		mode = dim(" · uniform ")
	}
	return title + mode + styleInfo.Render("[t]")
}

// timedVal is one sample with its absolute timestamp, its value, and the
// engine it came from.
type timedVal struct {
	t time.Time
	v float64
	s int
}

// timedSeries flattens every provider's history onto absolute timestamps
// spaced one cadence apart, tagging each sample with its engine: compressed
// buckets must tell engines apart to average within one and sum across all.
func timedSeries(s core.Snapshot, out bool, cadence time.Duration) []timedVal {
	var tv []timedVal
	var end time.Time
	for i := range s.Providers {
		p := &s.Providers[i]
		vals, t0 := p.OutHist, p.OutT0
		if !out {
			vals, t0 = p.InHist, p.InT0
		}
		if t0.IsZero() {
			continue
		}
		for j, v := range vals {
			t := t0.Add(time.Duration(j) * cadence)
			tv = append(tv, timedVal{t: t, v: v, s: i})
			if t.After(end) {
				end = t
			}
		}
	}
	if aend := agentHistEnd(s.Agents); aend.After(end) {
		end = aend
	}
	if !end.IsZero() {
		n := core.HistoryLen
		hist := agentDenseHist(s.Agents, out, end, n, cadence)
		src := len(s.Providers)
		any := false
		for _, v := range hist {
			if v > 0 {
				any = true
				break
			}
		}
		if any {
			start := end.Add(-time.Duration(n-1) * cadence)
			for j, v := range hist {
				tv = append(tv, timedVal{t: start.Add(time.Duration(j) * cadence), v: v, s: src})
			}
		}
	}
	sort.Slice(tv, func(i, j int) bool { return tv[i].t.Before(tv[j].t) })
	return tv
}

// compressSeries maps samples onto w columns whose covered timespan doubles
// every `block` columns moving away from the newest sample: right edge shows
// per-cadence detail, the far left packs hours. bounds marks where each
// coarser block begins so charts can draw faint separators.
//
// Aggregation matches uniform mode (aggHist) and the aggregate the panel
// title prints: engines sum. Within one engine, samples sharing a coarse
// bucket average into that span's mean rate. Dividing a bucket by its total
// sample count instead would scale the chart down by the engine count and
// make the two timescale modes disagree about what a column means.
func compressSeries(tv []timedVal, w, block int) ([]float64, map[int]bool) {
	if len(tv) == 0 || w <= 0 {
		return nil, nil
	}
	end := tv[len(tv)-1].t
	spans := make([]time.Duration, w)
	total := time.Duration(0)
	maxLevel := spanCap(w)
	for j := range w { // j=0 oldest … w-1 newest
		level := min((w-1-j)/block,
			// wider shifts would overflow the span sums
			maxLevel)
		spans[j] = time.Second << level
		total += spans[j]
	}

	bounds := map[int]bool{}
	for j := range w {
		if (w-1-j)%block == 0 && j < w-1 {
			bounds[j] = true
		}
	}

	nEngines := 0
	for _, s := range tv {
		nEngines = max(nEngines, s.s+1)
	}
	// Per-engine bucket sums and counts; rows materialize only for buckets
	// samples actually land in.
	sums := make([][]float64, w)
	cnts := make([][]int, w)
	for _, s := range tv {
		offset := end.Sub(s.t)
		if offset < 0 || offset >= total {
			continue
		}
		j := 0
		acc := total
		for j < w-1 {
			if offset < acc-spans[j] {
				acc -= spans[j]
				j++
				continue
			}
			break
		}
		if sums[j] == nil {
			sums[j] = make([]float64, nEngines)
			cnts[j] = make([]int, nEngines)
		}
		sums[j][s.s] += s.v
		cnts[j][s.s]++
	}
	grid := make([]float64, w)
	for j := range grid {
		for e, cnt := range cnts[j] { // nil row for empty buckets: loop body skipped
			if cnt > 0 {
				grid[j] += sums[j][e] / float64(cnt)
			}
		}
	}
	return grid, bounds
}

// spanCap is the largest shift keeping w spans, and thus the whole summed
// window, inside a time.Duration: past it the leftward timescale stops
// doubling instead of wrapping negative and collapsing the chart's buckets.
func spanCap(w int) int {
	maxLevel := 63 - bits.Len64(uint64(w)*uint64(time.Second))
	if maxLevel < 0 {
		return 0
	}
	return maxLevel
}

// chartCadence is the sampling interval charts are drawn at.
func (m Model) chartCadence() time.Duration {
	if m.cfg.PollEvery > 0 {
		return m.cfg.PollEvery
	}
	return time.Second
}

func (m Model) renderMidRow() string {
	pw := m.w * 38 / 100
	gw := m.w * 31 / 100
	rw := m.w - pw - gw
	_, midIn, _ := m.sectionHeights()

	prov := panel("BACKENDS", m.providersBody(pw-4), pw-4, midIn)
	gaug := panel("ENGINE STATE", m.gaugesBody(gw-4), gw-4, midIn)
	prb := panel(m.probesTitle(), m.probesBody(rw-4, midIn), rw-4, midIn)

	return lipgloss.JoinHorizontal(lipgloss.Top, prov, gaug, prb)
}

// renderSystem is the two-row host strip: row 1 is live vitals (mem, gpus),
// row 2 is identity and sensors (cpu, os, drivers, temps).
func (m Model) renderSystem() string {
	w := m.w - 4
	sy := m.snap.Sys
	vitals := []string{styleTitle.Render("SYS")}
	switch {
	case sy == nil || sy.MemTotal == 0:
		vitals = append(vitals, dim("mem n/a"))
	default:
		memPct := float64(sy.MemUsed) / float64(sy.MemTotal) * 100
		vitals = append(vitals,
			"mem "+GaugeBar(memPct, clampi(w/6, 8, 18), memHeat)+
				" "+dim(humanBytesShort(sy.MemUsed)+"/"+humanBytesShort(sy.MemTotal)))
		if sy.SwapTotal > 0 {
			swPct := float64(sy.SwapUsed) / float64(sy.SwapTotal) * 100
			st := lipgloss.NewStyle().Foreground(memHeat(swPct))
			vitals = append(vitals, dim("swp ")+st.Render(fmt.Sprintf("%.0f%%", swPct)))
		}
		if sy.Load1 > 0 || sy.Load5 > 0 {
			vitals = append(vitals, dim("ld ")+styleValue.Render(fmt.Sprintf("%.2f", sy.Load1)))
		}
	}
	vitals = append(vitals, gpuSegments(sy)...)

	var ident []string
	if sy != nil && (sy.CPUModel != "" || sy.OsName != "" || len(sy.Drivers) > 0 || len(sy.NPUs) > 0) {
		ident = hostSegments(sy)
	}

	cpuTemps := sysCPUTemps(sy)
	shownTemps := 0
	for _, t := range cpuTemps {
		if shownTemps >= 4 {
			break
		}
		// Labels come from local sysfs and (for remote hosts) parsed vendor
		// tooling output; they pass the terminal sanitizer like every other
		// externally sourced string.
		label := shorten(core.SanitizeText(strings.Fields(t.Label + ",")[0]), 7)
		label = strings.TrimSuffix(label, ",")
		c := tempColor(float64(t.MilliC) / 1000)
		ident = append(ident, dim(label+" ")+
			lipgloss.NewStyle().Bold(true).Foreground(c).Render(fmtTempC(t.MilliC)))
		shownTemps++
	}
	switch {
	case sy == nil:
	case shownTemps == 0 && len(sy.GPUs) == 0 && sy.CPUModel == "" &&
		len(sy.Drivers) == 0 && sy.OsName == "":
		ident = append(ident, dim("no sensors found"))
	case len(cpuTemps) > shownTemps:
		// Counted on the filtered list: GPU readings already render as their
		// own segments, so they are neither hidden nor counted here.
		ident = append(ident, dim(fmt.Sprintf("+%d more", len(cpuTemps)-shownTemps)))
	}

	row1 := padBlock(joinSpreadLeft(vitals, w), w, 1)
	row2 := ""
	if len(ident) > 0 && m.stripTwoRows() { // must match systemStripRows' budget
		row2 = "\n" + padBlock(joinSpreadLeft(ident, w), w, 1)
	}
	return panelStyle.Render(row1 + row2)
}

// hostSegments adds CPU model, OS·kernel and driver versions to the strip.
// All values can originate from another host (ssh vitals) or vendor tooling,
// so they pass the terminal sanitizer.
func hostSegments(sy *core.SysSample) []string {
	if sy == nil {
		return nil
	}
	var segs []string
	if sy.CPUModel != "" {
		segs = append(segs, dim(shorten(core.SanitizeText(sy.CPUModel), 22)))
	}
	if sy.OsName != "" || sy.Kernel != "" {
		osPart := core.SanitizeText(sy.OsName)
		if sy.Kernel != "" {
			osPart = strings.TrimSpace(osPart + " · " + core.SanitizeText(sy.Kernel))
		}
		segs = append(segs, dim(shorten(osPart, 34)))
	}
	if len(sy.Drivers) > 0 {
		keys := make([]string, 0, len(sy.Drivers))
		for k := range sy.Drivers {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var parts []string
		for _, k := range keys {
			parts = append(parts, core.SanitizeText(k)+" "+core.SanitizeText(sy.Drivers[k]))
		}
		segs = append(segs, styleInfo.Render(shorten(strings.Join(parts, " · "), 40)))
	}
	if len(sy.NPUs) > 0 {
		segs = append(segs, styleMagic.Render("npu: "+strings.Join(sy.NPUs, ",")))
	}
	return segs
}

// gpuSegment renders one compact accelerator readout: temp util vram watts.
func gpuSegment(g core.GPUDevice) string {
	var b strings.Builder
	b.WriteString(dim(shortVendor(g.Vendor) + strconv.Itoa(g.Index) + " "))
	if g.MilliC > 0 {
		c := tempColor(float64(g.MilliC) / 1000)
		b.WriteString(lipgloss.NewStyle().Bold(true).Foreground(c).Render(fmtTempC(g.MilliC)) + " ")
	}
	if g.UtilPct > 0 {
		b.WriteString(styleInfo.Render(fmt.Sprintf("%.0f%%", g.UtilPct)) + " ")
	}
	switch {
	case g.MemTotal > 0:
		b.WriteString(dim(humanBytesShort(g.MemUsed) + "/" + humanBytesShort(g.MemTotal)))
	case g.Name != "":
		// Model names can come from a remote host's nvidia-smi output.
		b.WriteString(dim(shorten(core.SanitizeText(g.Name), 16)))
	}
	if g.PowerW > 0 {
		b.WriteString(" " + styleWarn.Render(fmt.Sprintf("%.0fW", g.PowerW)))
	}
	return b.String()
}

func gpuSegments(sy *core.SysSample) []string {
	if sy == nil {
		return nil
	}
	segs := make([]string, 0, len(sy.GPUs))
	for _, g := range sy.GPUs {
		segs = append(segs, gpuSegment(g))
	}
	return segs
}

func shortVendor(v string) string {
	switch v {
	case "nvidia":
		return "nv"
	case "amd":
		return "amd"
	case "intel":
		return "intel"
	case "apple":
		return "apple"
	default:
		return "gpu"
	}
}

// sysCPUTemps filters out GPU hwmon readings; those arrive via SysSample.GPUs.
func sysCPUTemps(sy *core.SysSample) []core.TempReading {
	if sy == nil {
		return nil
	}
	if len(sy.GPUs) > 0 {
		var cpu []core.TempReading
		for _, t := range sy.Temps {
			if !t.IsGPU {
				cpu = append(cpu, t)
			}
		}
		return cpu
	}
	return sy.Temps
}

func tempColor(celsius float64) lipgloss.Color {
	switch {
	case celsius < 60:
		return cGreen
	case celsius < 80:
		return cYellow
	default:
		return cRed
	}
}

func memHeat(v float64) lipgloss.Color {
	switch {
	case v < 70:
		return cGreen
	case v < 90:
		return cYellow
	default:
		return cRed
	}
}

func fmtTempC(milliC int) string {
	return fmt.Sprintf("%.0f°", float64(milliC)/1000)
}

func (m Model) providersBody(w int) string {
	var b strings.Builder
	for _, p := range m.snap.Providers {
		dot := dotUp
		if !p.OK {
			dot = dotBad
		}
		model := shorten(core.SanitizeText(p.PrimaryModel()), w-15)
		line1 := dot + " " + kindBadge(p.Kind) + " " + styleValue.Render(model)
		if p.Version != "" {
			line1 += " " + dim("v"+shorten(core.SanitizeText(p.Version), 12))
		}
		b.WriteString(clip(line1, w) + "\n")
		if !p.OK {
			b.WriteString(styleBad.Render("  "+clip(shorten(core.SanitizeText(p.Err), w-3), w-3)) + "\n")
		} else {
			kvg := GaugeBar(p.KVPct, clampi(w-26, 4, 14), kvHeat)
			stats := fmt.Sprintf("▲%s ▼%s r%d w%d",
				fmtRate(p.OutTokPS), fmtRate(p.InTokPS), p.Running, p.Waiting)
			line2 := "  " + kvg + " " + styleDim.Render(stats)
			b.WriteString(clip(line2, w) + "\n")
		}
	}
	return b.String()
}

func (m Model) gaugesBody(w int) string {
	var b strings.Builder
	for _, p := range m.snap.Providers {
		if !p.OK {
			continue
		}
		name := styleDim.Render(clip(shorten(core.SanitizeText(p.Label), w-6), w-6))
		kv := "kv  " + GaugeBar(p.KVPct, clampi(w-10, 4, 20), kvHeat)
		third := procLine(p)
		row := clip(name+"\n"+kv+"\n"+third, w)
		b.WriteString(row + "\n\n")
	}
	if b.Len() == 0 {
		// No engines yet is genuinely "waiting"; engines present but all
		// down never resolves, so name it instead of promising telemetry.
		if len(m.snap.Providers) == 0 {
			return dim("waiting for telemetry…")
		}
		return dim("no healthy engines (see BACKENDS)")
	}
	return b.String()
}

// procLine composes the third detail row: memory/context/process stats.
func procLine(p core.ProviderSnapshot) string {
	var parts []string
	var bytes uint64
	for _, mm := range p.Models {
		bytes += mm.SizeVRAM
	}
	if bytes > 0 {
		parts = append(parts, "mem "+humanBytes(bytes))
	} else if len(p.Models) > 0 && p.Models[0].CtxMax > 0 {
		// CtxMax is a token count; ~2 bytes/token turns it into a KV-byte estimate.
		parts = append(parts, "ctx "+humanBytesShort(p.Models[0].CtxMax*2)+"tok")
	}
	if p.ProcRSS > 0 {
		rss := "rss " + humanBytesShort(p.ProcRSS)
		if p.ProcCPU > 0 {
			rss += fmt.Sprintf(" %.0f%%", p.ProcCPU)
		}
		parts = append(parts, rss)
	}
	if p.TTFTms > 0 {
		parts = append(parts, "ttft "+fmtMs(p.TTFTms))
	}
	if len(parts) == 0 {
		return ""
	}
	return styleDim.Render(strings.Join(parts, " · "))
}

func (m Model) probesTitle() string {
	t := "PROBES"
	if last, ok := m.lastProbe(); ok {
		t += " " + dim("last") + " " + fmtMs(last.TTFTms) + " " + styleHot.Render(fmtRate(last.TokPS)+"/s")
	}
	// Pressing p fires real generations that take seconds: acknowledge the
	// keypress immediately or it reads as dead until the first result lands.
	if !m.probeReq.IsZero() {
		t += "  " + styleWarn.Render("● probing…")
	}
	return t
}

func (m Model) probesBody(w, h int) string {
	vals := probeSeries(m.snap, w, m.chartCadence())
	chartH := clampi(h-3-len(m.snap.Providers), 2, 8)
	var out strings.Builder
	out.WriteString(BrailleChart(vals, w, chartH, ChartStyle{Heat: heatColor, FadeAge: true}) + "\n")
	shown := 0
	for i := len(m.snap.Probes) - 1; i >= 0 && shown < 2; i-- {
		p := m.snap.Probes[i]
		icon, st := "✓", styleOK
		if !p.OK {
			icon, st = "✗", styleBad
		}
		line := st.Render(icon) + " " + styleDim.Render(shorten(core.SanitizeText(p.Model), w-18)) +
			" " + fmtRate(p.TokPS) + "/s " + dim("ttft") + " " + fmtMs(p.TTFTms)
		out.WriteString(clip(line, w) + "\n")
		shown++
	}
	if len(m.snap.Probes) == 0 {
		out.WriteString(dim("press ") + styleInfo.Render("p") + dim(" to fire a probe") + "\n")
		out.WriteString(dim("--probe N: auto mode"))
	}
	return out.String()
}

func (m Model) renderFeed() string {
	w := m.w - 4
	_, _, feedIn := m.sectionHeights()
	// A panel title is not clipped by the panel, and JoinVertical pads every
	// other block out to the widest one, so an over-wide title here silently
	// stretches the whole frame past the pane. Optional parts are therefore
	// added only while they fit, most useful first.
	title := "AGENT FEED"
	add := func(part string) {
		if lipgloss.Width(title)+lipgloss.Width(part) <= w {
			title += part
		}
	}
	if m.paused {
		add("  " + styleWarn.Render("(paused)"))
	}
	rates := agentRates(m.snap.Agents, m.clock)
	rows := agentRows(rates, m.clock)
	statsN := 0
	if len(rows) > 0 && feedIn > 0 {
		statsN = min(len(rows), max(feedIn/2, 1))
		if statsN >= feedIn && feedIn > 1 {
			statsN = feedIn - 1
		}
	}
	if statsN == 0 {
		if s := agentSummary(rates); s != "" {
			add("  " + s)
		}
	} else if statsN < len(rows) {
		add("  " + dim(fmt.Sprintf("+%d more", len(rows)-statsN)))
	}
	switch {
	case m.feedDown != "":
		add("  " + styleBad.Render("✗ ingest down"))
	case m.cfg.IngestAddr != "":
		add(dim("  ← POST http://" + m.cfg.IngestAddr + "/v1/events"))
	}
	var lines []string
	if statsN > 0 {
		lines = append(lines, rows[:statsN]...)
	}
	rest := feedIn - len(lines)
	lines = append(lines, feedLines(m.snap.Agents, rest, w)...)
	if len(lines) == 0 {
		switch {
		case m.feedDown != "":
			// The reason (listener failure, fd exhaustion) is otherwise only
			// on stderr, invisible under the alternate screen.
			reason := clip(shorten(core.SanitizeText("ingest stopped: "+m.feedDown), w), w)
			lines = append(lines, styleBad.Render(reason))
		case m.cfg.Agents:
			lines = append(lines, dim("no agent activity yet: agents running locally are picked up automatically"))
		case m.cfg.IngestAddr != "":
			lines = append(lines, dim("no agent activity yet: point your harness at the endpoint above"))
		default:
			// Ingest off and agent watching off: without naming a knob this
			// panel reads as a feature that silently never works.
			lines = append(lines, dim("no agent activity yet: run with --agents to watch coding agents on this machine"))
		}
	}
	content := strings.Join(lines, "\n")
	return panel(title, content, w, feedIn)
}

var kindIcons = map[string]string{"turn": "▸", "tool": "⚙", "error": "✗", "note": "✎"}

// feedLines renders the newest n events oldest-first, so a feed panel reads
// top down like a log tail with the newest line at the bottom.
func feedLines(events []core.AgentEvent, n, w int) []string {
	if n <= 0 || len(events) == 0 {
		return nil
	}
	out := make([]string, 0, min(n, len(events)))
	for _, ev := range events[max(len(events)-n, 0):] {
		out = append(out, clip(feedLine(ev), w))
	}
	return out
}

func feedLine(ev core.AgentEvent) string {
	icon := kindIcons[ev.Kind]
	if icon == "" {
		icon = "·"
	}
	st := styleDim
	switch ev.Kind {
	case "turn":
		st = styleOK
	case "tool":
		st = styleInfo
	case "error":
		st = styleBad
	case "note":
		st = styleWarn
	}
	name := shorten(core.SanitizeText(ev.Agent), 16)
	tok := fmt.Sprintf("↑%s ↓%s", fmtCount(ev.PromptTokens), fmtCount(ev.OutputTokens))
	if ev.ThinkingTokens > 0 {
		tok += " think " + fmtCount(ev.ThinkingTokens)
	}
	parts := []string{
		// Event timestamps come from external senders and may carry any
		// zone (or none, which decodes as UTC); render the viewer's clock.
		styleDim.Render(ev.At.Local().Format("15:04:05")),
		st.Render(icon + " " + name),
		styleDim.Render(shorten(core.SanitizeText(ev.Model), 20)),
		tok,
	}
	if ev.ViaEngine != "" {
		parts = append(parts, dim("via "+shorten(core.SanitizeText(ev.ViaEngine), 18)))
	}
	if ev.Note != "" {
		parts = append(parts, styleWarn.Render(shorten(core.SanitizeText(ev.Note), 28)))
	}
	return strings.Join(parts, "  ")
}

func (m Model) renderFooter() string {
	foot := styleInfo.Render("q") + dim(" quit  ") +
		styleInfo.Render("space") + dim(" pause  ") +
		styleInfo.Render("p") + dim(" probe  ") +
		styleInfo.Render("t") + dim(" timescale  ") +
		styleInfo.Render("?") + dim(" help")
	tag := ""
	if m.cfg.Demo {
		tag = styleHot.Render(" DEMO ") + " "
	}
	return tag + foot
}

func (m Model) renderEmpty() string {
	logo := wordmark()
	lines := []string{
		logo,
		"",
		styleWarn.Render("no inference engines detected"),
		dim("scanned localhost :11434 :30000 :8000 :8080 :1234 …"),
		"",
		"attach anything openai-compatible:",
		styleInfo.Render("  toktop --add http://127.0.0.1:9999"),
		"or watch coding agents on this machine:",
		styleInfo.Render("  toktop --agents"),
		"or preview the dashboard:",
		styleHot.Render("  toktop --demo"),
	}
	card := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(cBorder).
		Padding(1, 3).
		Render(strings.Join(lines, "\n"))
	return lipgloss.Place(m.w, m.h, lipgloss.Center, lipgloss.Center, card)
}

func (m Model) renderHelp() string {
	rows := [][2]string{
		{"q / ctrl+c", "quit"},
		{"esc", "close help / quit"},
		{"space", "pause / resume streaming"},
		{"p", "fire synthetic probe at every backend"},
		{"t", "toggle compressed timescale + grid"},
		{"? / h", "toggle this help"},
		{"", ""},
		{"--demo", "simulated fleet, zero setup"},
		{"--add URL", "attach an openai-compatible endpoint"},
		{"ssh://host", "watch engines on another host"},
		{"--agents", "also watch coding agents on this machine"},
		{"--probe N", "auto-probe every N seconds"},
		{"--once", "print one frame and exit"},
		{"--plain", "with --once: linear text report"},
	}
	var b strings.Builder
	for _, r := range rows {
		key := styleInfo.Render(padTo(r[0], 12))
		b.WriteString(key + dim(r[1]) + "\n")
	}
	box := helpStyle.Render(strings.TrimSuffix(b.String(), "\n"))
	placed := lipgloss.Place(m.w, m.h, lipgloss.Center, lipgloss.Center, box)
	// The minimal view advertises ? on panes far narrower than this box: an
	// over-wide help line wraps and drags every row below it out of alignment,
	// so clip like renderAgentsOnly does.
	out := strings.Split(placed, "\n")
	for i, ln := range out {
		out[i] = clip(ln, m.w)
	}
	return strings.Join(out, "\n")
}

// renderMinimal is the degraded view for panes too small for the dashboard:
// one line per engine, plus orientation the compact layout must carry on its
// own because the footer and header are not rendered here.
func (m Model) renderMinimal() string {
	var b strings.Builder
	b.WriteString(dim(clip("enlarge window for the full dashboard", m.w)) + "\n")
	// space pauses here too: without a badge a frozen strip is
	// indistinguishable from a feed that stalled.
	if m.paused {
		b.WriteString(styleWarn.Render("‖ PAUSED") + "\n")
	}
	if len(m.snap.Providers) == 0 {
		rates := agentRates(m.snap.Agents, m.clock)
		if len(rates) == 0 {
			b.WriteString(styleWarn.Render("no inference engines detected") + "\n")
			b.WriteString(dim(clip("try toktop --demo or --add URL", m.w)) + "\n")
		}
		for _, r := range rates {
			b.WriteString(clip(agentMiniLine(r), m.w) + "\n")
		}
	}
	for _, p := range m.snap.Providers {
		st := styleOK
		if !p.OK {
			st = styleBad
		}
		line := st.Render("●") + " " + core.SanitizeText(p.Label) + " " + fmtRate(p.OutTokPS) + " tok/s"
		if !p.OK {
			// The dot's color alone must not carry engine state: name the
			// failure in text and surface the reason the full view shows.
			line += " " + st.Render("down")
			if msg := strings.TrimSpace(core.SanitizeText(p.Err)); msg != "" {
				line += " " + dim(shorten(msg, 32))
			}
		}
		b.WriteString(line + "\n")
	}
	if len(m.snap.Providers) > 0 {
		for _, r := range agentRates(m.snap.Agents, m.clock) {
			b.WriteString(clip(agentMiniLine(r), m.w) + "\n")
		}
	}
	// Only keys with a visible effect in this layout are advertised: p and t
	// still work, but their results (probe rows, chart timescale) render only
	// in the full dashboard, where ? help points to them.
	keys := "q quit · space pause · ? help"
	return b.String() + dim(clip(keys, m.w))
}

// --- helpers ---------------------------------------------------------------

func (m Model) upCount() (up, total int) {
	for _, p := range m.snap.Providers {
		total++
		if p.OK {
			up++
		}
	}
	return up, total
}

func (m Model) lastProbe() (core.ProbeSample, bool) {
	if n := len(m.snap.Probes); n > 0 {
		return m.snap.Probes[n-1], true
	}
	return core.ProbeSample{}, false
}

func kvHeat(v float64) lipgloss.Color {
	switch {
	case v < 60:
		return cGreen
	case v < 85:
		return cYellow
	default:
		return cRed
	}
}

// frameNow is the instant a snapshot treats as "now": its own stamp when
// the collector filled one in, otherwise fallback (the UI clock, or wall
// time if that is zero). Renderers use this so --once output and paused
// frames do not slide the agent window against real time.
func frameNow(s core.Snapshot, fallback time.Time) time.Time {
	if !s.At.IsZero() {
		return s.At
	}
	if !fallback.IsZero() {
		return fallback
	}
	return time.Now()
}

func aggOutAt(s core.Snapshot, now time.Time) float64 {
	t := 0.0
	for _, p := range s.Providers {
		t += p.OutTokPS
	}
	out, _ := agentOwnTokPS(agentRates(s.Agents, now))
	return t + out
}

func aggInAt(s core.Snapshot, now time.Time) float64 {
	t := 0.0
	for _, p := range s.Providers {
		t += p.InTokPS
	}
	_, in := agentOwnTokPS(agentRates(s.Agents, now))
	return t + in
}

func uniqueAgents(events []core.AgentEvent) int {
	seen := map[string]bool{}
	for _, ev := range events {
		if ev.Agent != "" {
			seen[ev.Agent] = true
		}
	}
	return len(seen)
}

// aggHist sums every provider's history onto one absolute time grid of w
// columns ending at the newest sample anywhere. Because samples carry their
// own timestamps (OutT0/InT0), engines that joined late or dropped out for a
// while cannot stretch or compress the visible window.
func aggHist(s core.Snapshot, out bool, w int, cadence time.Duration) []float64 {
	type src struct {
		vals []float64
		t0   time.Time
	}
	var srcs []src
	var end time.Time
	for i := range s.Providers {
		p := &s.Providers[i]
		vals, t0 := p.OutHist, p.OutT0
		if !out {
			vals, t0 = p.InHist, p.InT0
		}
		if len(vals) == 0 || t0.IsZero() {
			continue
		}
		srcs = append(srcs, src{vals, t0})
		last := t0.Add(time.Duration(len(vals)-1) * cadence)
		if last.After(end) {
			end = last
		}
	}
	if aend := agentHistEnd(s.Agents); aend.After(end) {
		end = aend
	}
	if end.IsZero() || w <= 0 {
		return nil
	}
	grid := make([]float64, w)
	start := end.Add(-time.Duration(w-1) * cadence)
	half := cadence / 2
	for j := range grid {
		ts := start.Add(time.Duration(j) * cadence)
		var sum float64
		for _, sr := range srcs {
			d := ts.Sub(sr.t0)
			idx := int((d + cadence/2) / cadence) // nearest sample
			if idx < 0 || idx >= len(sr.vals) {
				continue
			}
			sampleT := sr.t0.Add(time.Duration(idx) * cadence)
			if d := sampleT.Sub(ts); d > half || d < -half {
				continue // no sample near this bucket: engine was silent
			}
			sum += sr.vals[idx]
		}
		grid[j] = sum
	}
	for i, v := range agentDenseHist(s.Agents, out, end, w, cadence) {
		grid[i] += v
	}
	return grid
}

// probeSeries turns irregularly timed probe results into a step-hold series
// on a uniform grid ending at the newest probe: each bucket carries the most
// recently measured tok/s.
func probeSeries(s core.Snapshot, w int, cadence time.Duration) []float64 {
	if len(s.Probes) == 0 {
		return nil
	}
	end := s.Probes[len(s.Probes)-1].At
	grid := make([]float64, w)
	start := end.Add(-time.Duration(w-1) * cadence)
	last := 0.0
	seen := false
	next := 0
	for j := range grid {
		bucketEnd := start.Add(time.Duration(j+1) * cadence)
		for next < len(s.Probes) && s.Probes[next].At.Before(bucketEnd) {
			last, seen = s.Probes[next].TokPS, true // hold even pre-window probes
			next++
		}
		if seen {
			grid[j] = last
		}
	}
	return grid
}
