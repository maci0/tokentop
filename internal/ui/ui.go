package ui

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"tokentop/internal/core"
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
}

type Model struct {
	cfg    Config
	ch     <-chan core.Snapshot
	snap   core.Snapshot
	w, h   int
	ready  bool
	paused bool
	help   bool
	clock  time.Time
	maxAgg float64
	prevAgg float64
	trendUp bool
	chartCompressed bool
}

func New(cfg Config, ch <-chan core.Snapshot) Model {
	return Model{cfg: cfg, ch: ch, chartCompressed: chartCompressedDefault}
}

// StaticFrame renders one snapshot for non-interactive output (--once).
func StaticFrame(cfg Config, s core.Snapshot, w, h int) string {
	m := New(cfg, nil)
	m.snap = s
	m.w, m.h = w, h
	m.ready = true
	m.clock = time.Now()
	if agg := aggOut(s); agg > 0 {
		m.prevAgg = agg
		m.maxAgg = agg
	}
	return m.View()
}

// --- messages -------------------------------------------------------------

type snapMsg core.Snapshot
type tickMsg time.Time

func waitSnap(ch <-chan core.Snapshot) tea.Cmd {
	return func() tea.Msg { return snapMsg(<-ch) }
}

func tickClock() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

// --- model ----------------------------------------------------------------

func (m Model) Init() tea.Cmd {
	return tea.Batch(tickClock(), waitSnap(m.ch))
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		m.ready = true
		return m, nil

	case tickMsg:
		m.clock = time.Time(msg)
		return m, tickClock()

	case snapMsg:
		if !m.paused {
			agg := aggOut(core.Snapshot(msg))
			m.trendUp = agg >= m.prevAgg
			m.prevAgg = agg
			if agg > m.maxAgg {
				m.maxAgg = agg
			}
			m.snap = core.Snapshot(msg)
		}
		return m, waitSnap(m.ch)

	case tea.KeyMsg:
		switch msg.String() {
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
		return "\n  ⏳ tokentop is warming up…"
	}
	if m.help {
		return m.renderHelp()
	}
	if m.w < 62 || m.h < 16 {
		return m.renderMinimal()
	}
	if len(m.snap.Providers) == 0 {
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
	for lipgloss.Height(body)+lipgloss.Height(footer) < m.h {
		body += "\n"
	}
	return body + footer
}

func (m Model) renderHeader() string {
	logo := wordmark()
	segs := []string{logo, dim("v" + m.cfg.Version)}

	up, tot := m.upCount()
	dot := dotUp
	st := styleOK
	switch {
	case up == 0:
		dot, st = dotBad, styleBad
	case up < tot:
		dot, st = dotWarn, styleWarn
	}
	segs = append(segs, st.Render(fmt.Sprintf("%s %d/%d engines", strip(dot), up, tot)))

	outV := styleValue.Foreground(heatColor(norm(m.prevAgg, m.maxAgg))).Render("▲ " + fmtRate(m.prevAgg))
	inV := styleInfo.Render("▼ " + fmtRate(aggIn(m.snap)))
	segs = append(segs, outV+" "+dim("tok/s out"), inV+" "+dim("in"))

	if up > 0 || tot > 0 {
		segs = append(segs, dim("up "+fmtDur(m.snap.Uptime)))
	}
	if m.snap.Sys != nil && m.snap.Sys.RemoteHost != "" {
		segs = append(segs, styleMagic.Render("via ssh:"+m.snap.Sys.RemoteHost))
	}
	right := ""
	if m.paused {
		right += styleWarn.Render("‖ PAUSED ") + dim("│ ")
	}
	right += styleMagic.Render(m.clock.Format("15:04:05"))
	return joinSpread(segs, right, m.w)
}

// systemStripRows is the total height of the system strip: border (2) plus
// one row of vitals, plus a second identity row when the host reports any.
func (m Model) systemStripRows() int {
	if sy := m.snap.Sys; sy != nil && (sy.CPUModel != "" || sy.OsName != "" ||
		len(sy.Drivers) > 0 || len(sy.NPUs) > 0 || len(sysCPUTemps(sy)) > 0) {
		return 4
	}
	return 3
}

// sectionHeights splits the body into exact inner heights for the throughput
// chart, the mid-row panels and the agent feed. Fixed chrome is computed from
// the header, chart titles/borders, system strip, feed title and footer.
func (m Model) sectionHeights() (outH, midIn, feedIn int) {
	f := m.h - 13 - m.systemStripRows()
	if f < 10 {
		f = 10
	}
	outH = clampi(int(float64(f)*0.42), 4, 99)
	feedIn = clampi(int(float64(f)*0.22), 3, 12)
	midIn = f - outH - feedIn
	if midIn < 5 {
		outH -= 5 - midIn
		midIn = 5
		outH = maxi(outH, 3)
	}
	return outH, midIn, feedIn
}

func maxi(a, b int) int {
	if a > b {
		return a
	}
	return b
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
		"PROMPT "+styleInfo.Render("▼ "+fmtRate(aggIn(m.snap))+" tok/s"),
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
	vals, bounds := compressSeries(timedSeries(m.snap, true), w, compressBlock)
	return vals, bounds
}

func (m Model) throughputTitle() string {
	title := "THROUGHPUT " + styleHot.Render("▲ "+fmtRate(m.prevAgg)+" tok/s") + dim("  decode")
	if m.chartCompressed {
		title += dim(" · compressed ←") + styleInfo.Render("t")
	}
	return title
}

// timedVal is one sample with its absolute timestamp.
type timedVal struct {
	t time.Time
	v float64
}

// timedSeries flattens every provider's history onto absolute timestamps.
func timedSeries(s core.Snapshot, out bool) []timedVal {
	var tv []timedVal
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
			tv = append(tv, timedVal{t0.Add(time.Duration(j) * time.Second), v})
		}
	}
	sort.Slice(tv, func(i, j int) bool { return tv[i].t.Before(tv[j].t) })
	return tv
}

// compressSeries maps samples onto w columns whose covered timespan doubles
// every `block` columns moving away from the newest sample: right edge shows
// per-cadence detail, the far left packs hours. bounds marks where each
// coarser block begins so charts can draw faint separators.
func compressSeries(tv []timedVal, w, block int) ([]float64, map[int]bool) {
	if len(tv) == 0 || w <= 0 {
		return nil, nil
	}
	end := tv[len(tv)-1].t
	spans := make([]time.Duration, w)
	total := time.Duration(0)
	for j := 0; j < w; j++ { // j=0 oldest … w-1 newest
		level := (w - 1 - j) / block
		spans[j] = time.Second << level
		if spans[j] < 1*time.Second {
			spans[j] = time.Second // guard against cadence << 30 overflow
		}
		total += spans[j]
	}

	grid := make([]float64, w)
	counts := make([]int, w)
	bounds := map[int]bool{}
	for j := range grid {
		if (w-1-j)%block == 0 && j < w-1 {
			bounds[j] = true
		}
	}
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
		grid[j] += s.v
		counts[j]++
	}
	for j := range grid {
		if counts[j] > 0 {
			grid[j] /= float64(counts[j])
		}
	}
	return grid, bounds
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

	prov := panel("BACKENDS", m.providersBody(pw-4, midIn), pw-4, midIn)
	gaug := panel("ENGINE STATE", m.gaugesBody(gw-4, midIn), gw-4, midIn)
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
			st := lipgloss.NewStyle().Foreground(tempColor(swPct * 1.2))
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

	shownTemps := 0
	for _, t := range sysCPUTemps(sy) {
		if shownTemps >= 4 {
			break
		}
		label := shorten(strings.Fields(t.Label + ",")[0], 7)
		label = strings.TrimSuffix(label, ",")
		c := tempColor(float64(t.MilliC) / 1000)
		ident = append(ident, dim(label+" ")+
			lipgloss.NewStyle().Bold(true).Foreground(c).Render(fmtTempC(t.MilliC)))
		shownTemps++
	}
	switch {
	case sy == nil:
	case shownTemps == 0 && len(sysGPUs(sy)) == 0 && sy.CPUModel == "" &&
		len(sy.Drivers) == 0 && sy.OsName == "":
		ident = append(ident, dim("no sensors found"))
	case len(sysGPUs(sy)) == 0 && len(sy.Temps) > shownTemps:
		ident = append(ident, dim(fmt.Sprintf("+%d more", len(sy.Temps)-shownTemps)))
	}

	row1 := padBlock(joinSpreadLeft(vitals, w), w, 1)
	row2 := ""
	if len(ident) > 0 {
		row2 = "\n" + padBlock(joinSpreadLeft(ident, w), w, 1)
	}
	return panelStyle.Render(row1 + row2)
}

// hostSegments adds CPU model, OS·kernel and driver versions to the strip.
func hostSegments(sy *core.SysSample) []string {
	if sy == nil {
		return nil
	}
	var segs []string
	if sy.CPUModel != "" {
		segs = append(segs, dim(shorten(sy.CPUModel, 22)))
	}
	if sy.OsName != "" || sy.Kernel != "" {
		osPart := sy.OsName
		if sy.Kernel != "" {
			osPart = strings.TrimSpace(osPart + " · " + sy.Kernel)
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
			parts = append(parts, k+" "+sy.Drivers[k])
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
		b.WriteString(dim(shorten(g.Name, 16)))
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

func sysGPUs(sy *core.SysSample) []core.GPUDevice {
	if sy == nil {
		return nil
	}
	return sy.GPUs
}

// joinSpreadLeft packs segments left-to-right up to w visible cells.
func joinSpreadLeft(segs []string, w int) string {
	var b strings.Builder
	used := 0
	for i, s := range segs {
		seg := dim(" │ ") + s
		if i == 0 {
			seg = s
		}
		if used+lipgloss.Width(seg) > w {
			break
		}
		b.WriteString(seg)
		used += lipgloss.Width(seg)
	}
	return b.String()
}

// sysTemps returns sensor readings or nil when unavailable.
func sysTemps(sy *core.SysSample) []core.TempReading {
	if sy == nil {
		return nil
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

func (m Model) providersBody(w, h int) string {
	var b strings.Builder
	for _, p := range m.snap.Providers {
		dot := dotUp
		if !p.OK {
			dot = dotBad
		}
		model := shorten(p.PrimaryModel(), w-15)
		line1 := dot + " " + kindBadge(p.Kind) + " " + styleValue.Render(model)
		if p.Version != "" {
			line1 += " " + dim("v"+shorten(p.Version, 12))
		}
		b.WriteString(clip(line1, w) + "\n")
		if !p.OK {
			b.WriteString(styleBad.Render("  "+clip(shorten(p.Err, w-3), w-3)) + "\n")
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

func (m Model) gaugesBody(w, h int) string {
	var b strings.Builder
	for _, p := range m.snap.Providers {
		if !p.OK {
			continue
		}
		name := styleDim.Render(clip(shorten(p.Label, w-6), w-6))
		kv := "kv  " + GaugeBar(p.KVPct, clampi(w-10, 4, 20), kvHeat)
		third := procLine(p, w)
		row := clip(name+"\n"+kv+"\n"+third, w)
		b.WriteString(row + "\n\n")
	}
	if b.Len() == 0 {
		return dim("waiting for telemetry…")
	}
	return b.String()
}

// procLine composes the third detail row: memory/context/process stats.
func procLine(p core.ProviderSnapshot, w int) string {
	var parts []string
	var bytes uint64
	for _, mm := range p.Models {
		bytes += mm.SizeVRAM
	}
	if bytes > 0 {
		parts = append(parts, "mem "+humanBytes(bytes))
	} else if len(p.Models) > 0 && p.Models[0].CtxMax > 0 {
		parts = append(parts, "ctx "+humanBytesShort(p.Models[0].CtxMax*2)+"tok")
	}
	if p.ProcRSS > 0 {
		rss := "rss " + humanBytesShort(p.ProcRSS)
		if p.ProcCPU > 0 {
			rss += fmt.Sprintf(" %.0f%%", p.ProcCPU)
		}
		parts = append(parts, rss)
	}
	if len(parts) == 0 && p.TTFTms > 0 {
		parts = append(parts, fmt.Sprintf("ttft %s", fmtMs(p.TTFTms)))
	} else if p.TTFTms > 0 {
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
	return t
}

func (m Model) probesBody(w, h int) string {
	vals := probeSeries(m.snap, w, m.chartCadence())
	chartH := clampi(h-3-len(m.snap.Providers), 2, 8)
	out := BrailleChart(vals, w, chartH, ChartStyle{Heat: heatColor, FadeAge: true}) + "\n"
	shown := 0
	for i := len(m.snap.Probes) - 1; i >= 0 && shown < 2; i-- {
		p := m.snap.Probes[i]
		icon, st := "✓", styleOK
		if !p.OK {
			icon, st = "✗", styleBad
		}
		line := st.Render(icon) + " " + styleDim.Render(shorten(p.Model, w-18)) +
			" " + fmtRate(p.TokPS) + "/s " + dim("ttft") + " " + fmtMs(p.TTFTms)
		out += clip(line, w) + "\n"
		shown++
	}
	if len(m.snap.Probes) == 0 {
		out += dim("press ") + styleInfo.Render("p") + dim(" to fire a probe") + "\n"
		out += dim("--probe N: auto mode")
	}
	return out
}

func (m Model) renderFeed() string {
	w := m.w - 4
	_, _, feedIn := m.sectionHeights()
	title := "AGENT FEED" + dim("  ← POST http://"+m.cfg.IngestAddr+"/v1/events")
	if m.paused {
		title += "  " + styleWarn.Render("(paused)")
	}
	lines := make([]string, 0, feedIn)
	n := len(m.snap.Agents)
	for i := n - 1; i >= 0 && len(lines) < feedIn; i-- {
		ev := m.snap.Agents[i]
		lines = append(lines, clip(feedLine(ev), w))
	}
	// newest at bottom
	for i, j := 0, len(lines)-1; i < j; i, j = i+1, j-1 {
		lines[i], lines[j] = lines[j], lines[i]
	}
	if len(lines) == 0 {
		lines = append(lines, dim("no agent activity yet — point your harness at the endpoint above"))
	}
	content := strings.Join(lines, "\n")
	return panel(title, content, w, feedIn)
}

var kindIcons = map[string]string{"turn": "▸", "tool": "⚙", "error": "✗", "note": "✎"}

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
	name := shorten(ev.Agent, 16)
	tok := fmt.Sprintf("↑%s ↓%s", fmtCount(ev.PromptTokens), fmtCount(ev.OutputTokens))
	parts := []string{
		styleDim.Render(ev.At.Format("15:04:05")),
		st.Render(icon + " " + name),
		styleDim.Render(shorten(ev.Model, 20)),
		tok,
	}
	if ev.Note != "" {
		parts = append(parts, styleWarn.Render(shorten(ev.Note, 28)))
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
		styleInfo.Render("  tokentop --add http://127.0.0.1:9999"),
		"or preview the dashboard:",
		styleHot.Render("  tokentop --demo"),
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
		{"space", "pause / resume streaming"},
		{"p", "fire synthetic probe at every backend"},
		{"?", "toggle this help"},
		{"", ""},
		{"--demo", "simulated fleet, zero setup"},
		{"--add URL", "attach an openai-compatible endpoint"},
		{"--probe N", "auto-probe every N seconds"},
		{"--once", "print one frame and exit"},
	}
	var b strings.Builder
	for _, r := range rows {
		key := styleInfo.Render(padTo(r[0], 12))
		b.WriteString(key + dim(r[1]) + "\n")
	}
	box := helpStyle.Render(strings.TrimSuffix(b.String(), "\n"))
	return lipgloss.Place(m.w, m.h, lipgloss.Center, lipgloss.Center, box)
}

func (m Model) renderMinimal() string {
	var b strings.Builder
	for _, p := range m.snap.Providers {
		st := styleOK
		if !p.OK {
			st = styleBad
		}
		b.WriteString(st.Render("●") + " " + p.Label + " " + fmtRate(p.OutTokPS) + " tok/s\n")
	}
	return b.String()
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

func aggOut(s core.Snapshot) float64 {
	t := 0.0
	for _, p := range s.Providers {
		t += p.OutTokPS
	}
	return t
}

func aggIn(s core.Snapshot) float64 {
	t := 0.0
	for _, p := range s.Providers {
		t += p.InTokPS
	}
	return t
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
	if len(srcs) == 0 || end.IsZero() {
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

func norm(v, maxV float64) float64 {
	if maxV <= 0 {
		return 0
	}
	return v / maxV
}

func fmtRate(v float64) string {
	switch {
	case v >= 10000:
		return fmt.Sprintf("%.0fk", v/1000)
	case v >= 1000:
		return fmt.Sprintf("%.1fk", v/1000)
	case v >= 100:
		return fmt.Sprintf("%.0f", v)
	default:
		return fmt.Sprintf("%.1f", v)
	}
}

func fmtCount(n int64) string {
	switch {
	case n >= 1000000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 1000:
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	default:
		return fmt.Sprintf("%d", n)
	}
}

func fmtMs(ms float64) string {
	if ms <= 0 {
		return "-"
	}
	if ms >= 1000 {
		return fmt.Sprintf("%.2fs", ms/1000)
	}
	return fmt.Sprintf("%.0fms", ms)
}

func fmtDur(d time.Duration) string {
	d = d.Truncate(time.Second)
	if d < time.Minute {
		return d.String()
	}
	return (d / time.Minute).String() + "m" + fmt.Sprintf("%02ds", int(d/time.Second)%60)
}

func humanBytes(b uint64) string {
	const g = 1 << 30
	if b >= g {
		return fmt.Sprintf("%.1fGiB", float64(b)/g)
	}
	return fmt.Sprintf("%.0fMiB", float64(b)/(1<<20))
}

// humanBytesShort is the compact form used in the system strip.
func humanBytesShort(b uint64) string {
	const m = 1 << 20
	if b >= 10<<30 {
		return fmt.Sprintf("%.0fG", float64(b)/(1<<30))
	}
	if b >= 1<<30 {
		return fmt.Sprintf("%.1fG", float64(b)/(1<<30))
	}
	return fmt.Sprintf("%.0fM", float64(b)/m)
}

// shorten truncates s to n visible cells with an ellipsis.
func shorten(s string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n == 1 {
		return "…"
	}
	return string(r[:n-1]) + "…"
}

// clip hard-cuts a rendered (possibly styled) line to w visible cells.
// Styling is dropped past the cut; good enough for our own strings.
func clip(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= w {
		return s
	}
	return shorten(strip(s), w)
}

// strip removes ANSI escapes so clip can cut safely.
func strip(s string) string {
	var b strings.Builder
	inEsc := false
	for _, r := range s {
		if inEsc {
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
				inEsc = false
			}
			continue
		}
		if r == '\x1b' {
			inEsc = true
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func padTo(s string, w int) string {
	for lipgloss.Width(s) < w {
		s += " "
	}
	return s
}

// joinSpread places left segments and right segment on one padded line.
func joinSpread(left []string, right string, width int) string {
	l := strings.Join(left, dim(" │ "))
	lw := lipgloss.Width(l)
	rw := lipgloss.Width(right)
	gap := width - lw - rw
	if gap < 1 {
		gap = 1
	}
	return l + strings.Repeat(" ", gap) + right
}

func interleave(items []string, sep string) []string {
	out := make([]string, 0, len(items)*2)
	for i, it := range items {
		if i > 0 {
			out = append(out, sep)
		}
		out = append(out, it)
	}
	return out
}

func dim(s string) string { return styleDim.Render(s) }

func clampi(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// wordmark renders the logo with a cyan→pink gradient.
func wordmark() string {
	letters := []rune("TOKENTOP")
	colors := []lipgloss.Color{cTeal, cCyan, cBlue, cLavender, cMagenta, cPink, cPeach, cYellow}
	var b strings.Builder
	for i, l := range letters {
		b.WriteString(lipgloss.NewStyle().Bold(true).Foreground(colors[i%len(colors)]).Render(string(l)))
	}
	return b.String()
}
