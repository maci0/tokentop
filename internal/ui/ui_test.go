package ui

import (
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/maci0/toktop/internal/core"
)

type proberFunc func()

func (f proberFunc) ProbeAll() { f() }

func keyMsg(s string) tea.KeyMsg {
	switch s {
	case " ":
		return tea.KeyMsg{Type: tea.KeySpace}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEscape}
	default:
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
	}
}

// The key map is the product surface: q quits (help first), space freezes the
// stream so incoming snapshots are dropped rather than merged, p fires
// probes, t flips the timescale, and a paused frame must advertise itself.
func TestUpdateKeyMap(t *testing.T) {
	var probes atomic.Int32
	m := New(Config{Version: "t", Prober: proberFunc(func() { probes.Add(1) })}, nil)
	key := func(s string) tea.Cmd {
		nm, cmd := m.Update(keyMsg(s))
		m = nm.(Model)
		return cmd
	}
	sendSnap := func(label string) {
		nm, _ := m.Update(snapMsg(core.Snapshot{Providers: []core.ProviderSnapshot{{Label: label}}}))
		m = nm.(Model)
	}

	if key("q") == nil {
		t.Error("q must return a quit command")
	}

	key("?")
	if !m.help {
		t.Fatal("? did not open help")
	}
	if key("q") != nil {
		t.Error("q with help open must close help, not quit")
	}
	if m.help {
		t.Error("q with help open did not close help")
	}
	key("?")
	if !m.help {
		t.Fatal("? did not reopen help")
	}
	// Help is a full-screen replacement view: it must actually render its
	// content, not just flip the flag.
	m.w, m.h, m.ready = 110, 36, true
	out := strip(m.View())
	if !strings.Contains(out, "pause / resume streaming") {
		t.Errorf("help view missing key rows:\n%s", out)
	}
	// Every key the footer advertises must be documented here too: t was
	// missing for a long time and users had no discoverable path back from
	// the compressed timescale.
	for _, want := range []string{"compressed timescale", "esc"} {
		if !strings.Contains(out, want) {
			t.Errorf("help view missing %q:\n%s", want, out)
		}
	}
	if key("esc") != nil {
		t.Error("esc with help open must close help, not quit")
	}
	if m.help {
		t.Error("esc with help open did not close help")
	}

	if key(" "); !m.paused {
		t.Fatal("space did not pause")
	}
	sendSnap("late")
	if len(m.snap.Providers) != 0 {
		t.Error("paused model absorbed a snapshot")
	}
	key(" ")
	if m.paused {
		t.Fatal("second space did not resume")
	}
	sendSnap("late")
	if len(m.snap.Providers) != 1 || m.snap.Providers[0].Label != "late" {
		t.Error("resumed model dropped the snapshot")
	}

	before := m.chartCompressed
	key("t")
	if m.chartCompressed == before {
		t.Error("t did not toggle the timescale")
	}

	n := probes.Load()
	key("p") // the prober fires on its own goroutine
	deadline := time.Now().Add(time.Second)
	for probes.Load() == n && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := probes.Load(); got != n+1 {
		t.Errorf("p fired %d probes, want 1", got-n)
	}

	m.paused = true
	m.w, m.h, m.ready, m.clock = 110, 36, true, time.Now()
	if out := m.View(); !strings.Contains(strip(out), "PAUSED") {
		t.Error("paused frame lacks PAUSED badge")
	}
}

// Terminals below the minimum geometry degrade to a one-line-per-engine
// strip; it must still carry each engine's label and live rate.
func TestMinimalViewRendersRates(t *testing.T) {
	m := New(Config{Version: "t"}, nil)
	nm, _ := m.Update(snapMsg(core.Snapshot{Providers: []core.ProviderSnapshot{
		{Label: "ollama", OK: true, OutTokPS: 42},
	}}))
	m = nm.(Model)
	m.w, m.h, m.ready = 40, 10, true
	out := strip(m.View())
	if !strings.Contains(out, "ollama") || !strings.Contains(out, "42") || !strings.Contains(out, "tok/s") {
		t.Errorf("minimal view missing engine rate:\n%s", out)
	}
}

// A down engine cannot be signaled by the dot's color alone (WCAG 1.4.1):
// the minimal strip must say "down" and keep the error reason visible.
func TestMinimalViewNamesDownEngines(t *testing.T) {
	m := New(Config{Version: "t"}, nil)
	nm, _ := m.Update(snapMsg(core.Snapshot{Providers: []core.ProviderSnapshot{
		{Label: "vllm", OK: false, Err: "connection refused"},
	}}))
	m = nm.(Model)
	m.w, m.h, m.ready = 40, 10, true
	out := strip(m.View())
	if !strings.Contains(out, "vllm") || !strings.Contains(out, "down") || !strings.Contains(out, "connection refused") {
		t.Errorf("minimal view hides engine failure:\n%s", out)
	}
}

// feedLine must render event timestamps in the viewer's zone: ingest events
// carry sender-supplied RFC 3339 stamps whose offset (or absent offset,
// decoded as UTC) is otherwise shown as-is.
func TestFeedLineRendersEventTimeInLocalZone(t *testing.T) {
	at := time.Date(2026, 8, 24, 23, 30, 5, 0, time.FixedZone("sender", 5*3600+1800))
	ev := core.AgentEvent{At: at, Agent: "ci-bot", Kind: "turn"}
	line := strip(feedLine(ev))
	want := at.Local().Format("15:04:05")
	if !strings.Contains(line, want) {
		t.Fatalf("feedLine time = line %q, want it to show local %q", line, want)
	}
}

func TestGaugeBar(t *testing.T) {
	g := GaugeBar(50, 10, kvHeat)
	if !strings.Contains(g, "50%") {
		t.Errorf("missing label: %q", g)
	}
	if w := lipgloss.Width(GaugeBar(50, 10, kvHeat)); w != 14 { // 10 + space + "50%"
		t.Errorf("width = %d, want 14", w)
	}
	if w := lipgloss.Width(GaugeBar(120, 10, kvHeat)); w != 15 { // clamped label "100%"
		t.Errorf("overflow width = %d, want 15", w)
	}
	if lipgloss.Width(GaugeBar(-5, 10, kvHeat)) != 13 { // clamps to "0%"
		t.Error("negative pct not clamped")
	}
}

func TestShortenAndClip(t *testing.T) {
	if got := shorten("abcdef", 4); got != "abc…" {
		t.Errorf("shorten = %q", got)
	}
	if shorten("ab", 0) != "" || shorten("ab", 1) != "…" {
		t.Error("degenerate widths mishandled")
	}
	styled := lipgloss.NewStyle().Foreground(cRed).Render("hello world")
	if got := clip(styled, 6); got != "hello…" {
		t.Errorf("clip styled = %q", got)
	}
	// Wide glyphs count two cells: the result must never render wider than n.
	wide := "世界世界世界"
	if got := shorten(wide, 5); lipgloss.Width(got) > 5 {
		t.Errorf("shorten wide = %q renders %d cells, want <= 5", got, lipgloss.Width(got))
	}
	if got := clip(wide, 6); lipgloss.Width(got) > 6 || !strings.HasSuffix(got, "…") {
		t.Errorf("clip wide = %q (width %d)", got, lipgloss.Width(got))
	}
}

func TestFmtRateAndCount(t *testing.T) {
	if fmtRate(1234.5) != "1.2k" || fmtRate(42.3) != "42.3" || fmtRate(250) != "250" ||
		fmtRate(15000) != "15k" {
		t.Error("fmtRate drift")
	}
	if fmtCount(999) != "999" || fmtCount(2000) != "2.0k" || fmtCount(3_400_000) != "3.4M" {
		t.Error("fmtCount drift")
	}
	if fmtMs(210.4) != "210ms" || fmtMs(1400) != "1.40s" || fmtMs(0) != "-" {
		t.Error("fmtMs drift")
	}
}

func TestAggHistTimeAligned(t *testing.T) {
	// Provider B joins 3 cadences after A: tail-index alignment would smear
	// the window; absolute-time alignment must not.
	t0 := time.Now()
	s := core.Snapshot{Providers: []core.ProviderSnapshot{
		{OutT0: t0, OutHist: []float64{1, 1, 1, 1, 1, 1}},             // samples at t0..t0+5
		{OutT0: t0.Add(3 * time.Second), OutHist: []float64{2, 2, 2}}, // t0+3..t0+5
	}}
	got := aggHist(s, true, 6, time.Second)
	want := []float64{1, 1, 1, 3, 3, 3}
	if len(got) != len(want) {
		t.Fatalf("aggHist len = %d", len(got))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("aggHist[%d] = %v, want %v (full: %v)", i, got[i], want[i], got)
		}
	}
}

// An engine that stopped reporting must leave its buckets empty rather than
// dragging the whole window leftward.
func TestAggHistIgnoresStaleEngine(t *testing.T) {
	now := time.Now()
	old := now.Add(-30 * time.Second)
	s := core.Snapshot{Providers: []core.ProviderSnapshot{
		{OutT0: old, OutHist: []float64{9, 9}},
		{OutT0: now.Add(-2 * time.Second), OutHist: []float64{4, 4}},
	}}
	got := aggHist(s, true, 4, time.Second)
	// window = [now-3s .. now]; the stale engine's last sample is 29s old
	want := []float64{0, 0, 4, 4}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

// At w=500 the doubling spans overflow a time.Duration partway through the
// accumulation: total wraps, the bucket walk collapses, and every sample
// bunches into one middle column leaving the rest of the chart empty.
func TestCompressSeriesWideTerminalKeepsSamples(t *testing.T) {
	const w = 500 // wide enough that naive doubling overflows
	end := time.Unix(1_000_000_000, 0)
	tv := []timedVal{
		{t: end.Add(-300 * time.Second), v: 7},
		{t: end.Add(-time.Second), v: 1},
	}
	grid, bounds := compressSeries(tv, w, compressBlock)
	if len(grid) != w || len(bounds) == 0 {
		t.Fatalf("grid=%d bounds=%d", len(grid), len(bounds))
	}
	sum := 0.0
	for _, g := range grid {
		sum += g
	}
	if sum != 8 {
		t.Fatalf("samples lost or bunched on wide chart: sum=%v", sum)
	}
	if grid[w-1] != 1 {
		t.Fatalf("newest sample misplaced: grid[w-1]=%v", grid[w-1])
	}
}

func TestProbeSeriesStepHold(t *testing.T) {
	base := time.Now()
	s := core.Snapshot{Probes: []core.ProbeSample{
		{At: base.Add(-4 * time.Second), TokPS: 100},
		{At: base.Add(-1 * time.Second), TokPS: 300},
	}}
	got := probeSeries(s, 6, time.Second)
	// grid ends at the newest probe: buckets [-6..-1s]; probes hold forward
	want := []float64{0, 0, 100, 100, 100, 300}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("probeSeries = %v, want %v", got, want)
		}
	}
}

func TestStaticFrameRenders(t *testing.T) {
	snap := core.Snapshot{
		At:     time.Now(),
		Uptime: time.Minute,
		Providers: []core.ProviderSnapshot{{
			Label: "ollama", Kind: core.KindOllama, Addr: "http://127.0.0.1:11434", OK: true,
			Models:   []core.ModelInfo{{Name: "llama3"}},
			OutTokPS: 42, InTokPS: 100, KVPct: 55,
			OutHist: []float64{1, 2, 3, 4},
			InHist:  []float64{2, 4, 6, 8},
		}},
		Agents: []core.AgentEvent{{At: time.Now(), Agent: "tester", Kind: "turn",
			PromptTokens: 10, OutputTokens: 5}},
	}
	out := StaticFrame(Config{Version: "t"}, snap, 110, 36)
	for _, want := range []string{"TOKTOP", "BACKENDS", "ENGINE STATE", "PROBES", "AGENT FEED", "SYS", "llama3"} {
		if !strings.Contains(strip(out), want) {
			t.Errorf("frame missing %q", want)
		}
	}
}

func TestStaticFrameSystemStrip(t *testing.T) {
	snap := core.Snapshot{
		At: time.Now(),
		Providers: []core.ProviderSnapshot{{
			Label: "ollama", Kind: core.KindOllama, OK: true,
			Models: []core.ModelInfo{{Name: "llama3"}},
		}},
		Sys: &core.SysSample{
			MemTotal: 32 << 30, MemUsed: 16 << 30,
			SwapTotal: 8 << 30, SwapUsed: 1 << 30,
			Load1: 1.5, Load5: 1.2, Load15: 0.9,
			CPUModel: "Test CPU",
			Temps: []core.TempReading{
				{Label: "package", MilliC: 64000},
			},
			GPUs: []core.GPUDevice{
				{Vendor: "nvidia", Index: 0, Name: "A100", MilliC: 71000,
					MemTotal: 80 << 30, MemUsed: 20 << 30, UtilPct: 42, PowerW: 310},
				{Vendor: "amd", Index: 0, Name: "MI210", MilliC: 55000,
					MemTotal: 64 << 30, MemUsed: 8 << 30},
			},
		},
	}
	out := StaticFrame(Config{Version: "t"}, snap, 110, 34)
	plain := strip(out)
	for _, want := range []string{"SYS", "mem ", "50%", "swp ", "ld ",
		"nv0 71°", "42%", "20G/80G", "310W"} {
		if !strings.Contains(plain, want) {
			t.Errorf("system strip missing %q in:\n%s", want, plain)
		}
	}
	// wider terminals fit the second GPU and CPU temps too
	wide := strip(StaticFrame(Config{Version: "t"}, snap, 170, 34))
	for _, want := range []string{"amd0 55°", "8.0G/64G", "64°"} {
		if !strings.Contains(wide, want) {
			t.Errorf("wide strip missing %q in:\n%s", want, wide)
		}
	}
}

// GPU temps from hwmon must be suppressed once vendor GPU devices exist.
func TestSystemStripSuppressesHwmonGPUDupes(t *testing.T) {
	snap := core.Snapshot{
		At:        time.Now(),
		Providers: []core.ProviderSnapshot{{Label: "x", Kind: core.KindOllama, OK: true}},
		Sys: &core.SysSample{
			MemTotal: 32 << 30, MemUsed: 16 << 30,
			Temps: []core.TempReading{
				{Label: "edge", MilliC: 71000, IsGPU: true}, // would duplicate nv0
				{Label: "Tctl", MilliC: 66000},
			},
			GPUs: []core.GPUDevice{{Vendor: "nvidia", Index: 0, MilliC: 70000}},
		},
	}
	out := strip(StaticFrame(Config{Version: "t"}, snap, 110, 34))
	if strings.Contains(out, "edge") {
		t.Errorf("hwmon GPU temp leaked into strip:\n%s", out)
	}
	if !strings.Contains(out, "nv0 70°") || !strings.Contains(out, "66°") {
		t.Errorf("expected nv GPU seg + cpu temp:\n%s", out)
	}
}

func TestStaticFrameNoSensors(t *testing.T) {
	snap := core.Snapshot{
		At:        time.Now(),
		Providers: []core.ProviderSnapshot{{Label: "x", Kind: core.KindOllama, OK: true}},
		Sys:       &core.SysSample{},
	}
	out := strip(StaticFrame(Config{Version: "t"}, snap, 110, 34))
	if !strings.Contains(out, "mem n/a") || !strings.Contains(out, "no sensors found") {
		t.Errorf("empty sys sample not handled:\n%s", out)
	}
}

// The timescale toggle lives in the chart title; both modes must show the
// current one plus a clearly delimited key, not a run-together "←t".
func TestThroughputTitleAdvertisesTimescaleToggle(t *testing.T) {
	m := New(Config{Version: "t"}, nil)
	if got := strip(m.throughputTitle()); !strings.Contains(got, "compressed") || !strings.Contains(got, "[t]") {
		t.Errorf("compressed title = %q, want mode word plus [t] switch", got)
	}
	m.chartCompressed = false
	if got := strip(m.throughputTitle()); !strings.Contains(got, "uniform") || !strings.Contains(got, "[t]") {
		t.Errorf("uniform title = %q, want mode word plus [t] switch", got)
	}
}

// The help screen is the only in-app reference: both ways of pointing
// toktop at engines away from localhost must be discoverable there.
func TestHelpCoversAttachModes(t *testing.T) {
	m := New(Config{Version: "t"}, nil)
	m.help = true
	m.w, m.h, m.ready = 110, 36, true
	out := strip(m.View())
	for _, want := range []string{"ssh://host", "--agents"} {
		if !strings.Contains(out, want) {
			t.Errorf("help missing %q:\n%s", want, out)
		}
	}
}

// An empty AGENT FEED may promise automatic pickup only when --agents is on;
// otherwise it must name the knob (or the POST target) that fills it.
func TestFeedEmptyStateGuidesByMode(t *testing.T) {
	view := func(cfg Config) string {
		m := New(cfg, nil)
		m.w, m.h, m.ready = 110, 36, true
		return strip(m.renderFeed())
	}
	if out := view(Config{Version: "t", Agents: true}); !strings.Contains(out, "picked up automatically") {
		t.Errorf("--agents on, but the feed does not say so:\n%s", out)
	}
	out := view(Config{Version: "t", IngestAddr: "127.0.0.1:8420"})
	if !strings.Contains(out, "point your harness") {
		t.Errorf("ingest-only run lost the POST hint:\n%s", out)
	}
	if strings.Contains(out, "picked up automatically") {
		t.Errorf("feed promises automatic pickup with --agents off:\n%s", out)
	}
	if out := view(Config{Version: "t"}); !strings.Contains(out, "--agents") {
		t.Errorf("nothing watching: feed must point at --agents:\n%s", out)
	}
}

func TestStaticFrameEmptyState(t *testing.T) {
	out := StaticFrame(Config{Version: "t"}, core.Snapshot{}, 90, 30)
	plain := strip(out)
	if !strings.Contains(plain, "no inference engines detected") {
		t.Error("empty state hint missing")
	}
	// Every recovery path is named: attaching an endpoint, agent watching,
	// and the zero-setup demo.
	for _, want := range []string{"--add", "--agents", "--demo"} {
		if !strings.Contains(plain, want) {
			t.Errorf("empty state missing %q recovery hint", want)
		}
	}
}

func TestFmtDurMinuteRollover(t *testing.T) {
	cases := map[time.Duration]string{
		42 * time.Second:                          "42s",
		5*time.Minute + 30*time.Second:            "5m30s",
		time.Hour + 2*time.Minute + 3*time.Second: "62m03s",
	}
	for d, want := range cases {
		if got := fmtDur(d); got != want {
			t.Errorf("fmtDur(%v) = %q, want %q", d, got, want)
		}
	}
}

// Vendor tags must cover every vendor the samplers emit (gpu.go vendorOrder,
// gpu_darwin's apple devices): an uncovered variant degrades to the anonymous
// "gpu" tag and the strip loses the vendor identity it already carries.
func TestShortVendorCoversKnownVendors(t *testing.T) {
	cases := map[string]string{
		"nvidia": "nv",
		"amd":    "amd",
		"intel":  "intel",
		"apple":  "apple",
		"acme":   "gpu", // unknown vendors stay anonymous
	}
	for v, want := range cases {
		if got := shortVendor(v); got != want {
			t.Errorf("shortVendor(%q) = %q, want %q", v, got, want)
		}
	}
}

// Every engine kind the core model defines must have a badge style; a
// missing entry silently downgrades that backend's row to dim text.
func TestKindStylesCoverCoreKinds(t *testing.T) {
	for _, k := range []string{
		core.KindOllama, core.KindVLLM, core.KindLlamaCPP, core.KindOpenAI,
		core.KindSGLang, core.KindTRTLLM, core.KindMLX, core.KindLMStudio,
		core.KindKoboldCPP, core.KindLocalAI, core.KindTGI, core.KindLiteLLM,
		core.KindGPUStack, core.KindLemonade, core.KindOmniRoute,
	} {
		if _, ok := kindStyles[k]; !ok {
			t.Errorf("kindStyles missing %q", k)
		}
	}
}

// Sample spacing on the compressed timescale must follow the poll cadence,
// not an assumed 1s: --interval 2s covers twice the wall-clock window.
func TestTimedSeriesHonorsCadence(t *testing.T) {
	t0 := time.Now()
	s := core.Snapshot{Providers: []core.ProviderSnapshot{
		{OutT0: t0, OutHist: []float64{1, 2}},
	}}
	tv := timedSeries(s, true, 2*time.Second)
	if len(tv) != 2 {
		t.Fatalf("len = %d, want 2", len(tv))
	}
	if !tv[0].t.Equal(t0) || !tv[1].t.Equal(t0.Add(2*time.Second)) {
		t.Fatalf("timestamps %v..%v, want %v..%v", tv[0].t, tv[1].t, t0, t0.Add(2*time.Second))
	}
}

// GPU names and driver strings can originate on a remote host (ssh vitals
// relay nvidia-smi output verbatim); they must pass the terminal sanitizer
// like every other externally sourced value in the host strip.
func TestGPUSegmentSanitizesName(t *testing.T) {
	g := core.GPUDevice{Vendor: "nvidia", Index: 0, Name: "A\x1b[31mB"} // no VRAM: name row renders
	out := gpuSegment(g)
	if strings.ContainsRune(strip(out), '\x1b') {
		t.Errorf("gpuSegment leaked escape bytes: %q", strip(out))
	}
	if !strings.Contains(strip(out), "AB") {
		t.Errorf("gpuSegment lost model name: %q", strip(out))
	}
}

func TestHostSegmentsSanitizeDrivers(t *testing.T) {
	sy := &core.SysSample{Drivers: map[string]string{"nv\x1b]0;title": "5\x1b[35m50"}}
	segs := hostSegments(sy)
	for _, s := range segs {
		if strings.ContainsRune(strip(s), '\x1b') {
			t.Errorf("hostSegments leaked escape bytes: %q", strip(s))
		}
	}
}

// busySnap packs long labels, identity data, sensors and history so layout
// tests exercise near-worst-case line widths and heights.
func busySnap() core.Snapshot {
	return core.Snapshot{
		At: time.Now(), Uptime: 90 * time.Second,
		Providers: []core.ProviderSnapshot{
			{Label: "ollama", Kind: core.KindOllama, OK: true,
				Models: []core.ModelInfo{{Name: "llama3:8b-instruct-q5_K_M"}}, Version: "0.12.1",
				OutTokPS: 42.7, InTokPS: 1200.5, KVPct: 55, Running: 1, Waiting: 2,
				OutT0: time.Now(), InT0: time.Now(),
				OutHist: []float64{1, 2, 3}, InHist: []float64{1, 2, 3}},
			{Label: "vllm", Kind: core.KindVLLM, OK: false, Err: "connection refused"},
		},
		Sys: &core.SysSample{
			MemTotal: 32 << 30, MemUsed: 16 << 30, CPUModel: "AMD Ryzen 9 7950X",
			OsName: "Fedora Linux", Kernel: "6.11.0",
			Temps: []core.TempReading{{Label: "Tctl", MilliC: 64000}},
			GPUs:  []core.GPUDevice{{Vendor: "nvidia", Index: 0, MilliC: 70000}},
		},
		Agents: []core.AgentEvent{{At: time.Now(), Agent: "ci-bot", Kind: "turn",
			PromptTokens: 10, OutputTokens: 5}},
	}
}

// The frame must fit its pane exactly: bubbletea clips overflow from the
// top, which hides the header, and any line wider than the pane wraps and
// drags every later row out of alignment.
func TestFrameFitsPaneAtCommonSizes(t *testing.T) {
	for _, sz := range [][2]int{{62, 30}, {70, 31}, {80, 32}, {100, 34}, {120, 38}, {160, 44}} {
		w, h := sz[0], sz[1]
		out := StaticFrame(Config{Version: "0.1.0", IngestAddr: "127.0.0.1:8420"}, busySnap(), w, h)
		if got := lipgloss.Height(out); got > h {
			t.Errorf("%dx%d: frame is %d lines, overflows pane", w, h, got)
		}
		for i, ln := range strings.Split(out, "\n") {
			if lw := lipgloss.Width(ln); lw > w {
				t.Fatalf("%dx%d: line %d renders %d cells, want <= %d:\n%s", w, h, i, lw, w, ln)
			}
		}
	}
	// The header must survive intact on roomy panes.
	out := strip(StaticFrame(Config{Version: "t"}, busySnap(), 160, 40))
	if !strings.Contains(out, "engines") || !strings.Contains(out, "tok/s") {
		t.Errorf("full header lost on wide pane:\n%s", out)
	}
}

// Narrow panes shed decorative header segments before letting the row wrap:
// uptime goes first, then version, then inbound rate, then outbound; pinned
// segments are hard-clipped only as a last resort.
func TestFitSegmentsShedsByPriority(t *testing.T) {
	segs := []headerSeg{
		{text: "AAAA"},
		{text: "BBBB", shed: 40},
		{text: "CCCC", shed: 50},
		{text: "DDDD"},
	}
	if got := fitSegments(segs, 60); !strings.Contains(got, "CCCC") {
		t.Errorf("wide enough: nothing should be shed: %q", got)
	}
	got := fitSegments(segs, 17)
	if strings.Contains(got, "CCCC") || strings.Contains(got, "BBBB") || !strings.Contains(got, "DDDD") {
		t.Errorf("tight: want CCCC then BBBB shed first, pins kept: %q", got)
	}
	if got := fitSegments(segs, 6); lipgloss.Width(got) > 6 || !strings.HasPrefix(got, "AAAA") {
		t.Errorf("overflowing pins must hard-clip from the left: %q", got)
	}
}

// Pressing p fires generations that take seconds: the keypress must be
// acknowledged immediately, then hand over once a result lands.
func TestProbePressAcknowledgesUntilResult(t *testing.T) {
	m := New(Config{Version: "t", Prober: proberFunc(func() {})}, nil)
	nm, _ := m.Update(snapMsg(core.Snapshot{Providers: []core.ProviderSnapshot{{Label: "ollama", OK: true}}}))
	m = nm.(Model)
	m.w, m.h, m.ready, m.clock = 110, 36, true, time.Now()

	nm, _ = m.Update(keyMsg("p"))
	m = nm.(Model)
	if out := strip(m.View()); !strings.Contains(out, "probing") {
		t.Fatalf("pressing p gave no feedback:\n%s", out)
	}
	nm, _ = m.Update(snapMsg(core.Snapshot{
		Providers: []core.ProviderSnapshot{{Label: "ollama", OK: true}},
		Probes:    []core.ProbeSample{{At: time.Now().Add(time.Second), OK: true, TokPS: 10, TTFTms: 5}},
	}))
	m = nm.(Model)
	if out := strip(m.View()); strings.Contains(out, "probing") {
		t.Errorf("probe result did not clear the pending marker:\n%s", out)
	}
}

// All backends down: ENGINE STATE must say so instead of promising telemetry
// that will never arrive.
func TestEngineStateNamesAllDownEngines(t *testing.T) {
	m := New(Config{Version: "t"}, nil)
	m.snap = core.Snapshot{Providers: []core.ProviderSnapshot{
		{Label: "a", OK: false}, {Label: "b", OK: false},
	}}
	if out := strip(m.gaugesBody(30)); !strings.Contains(out, "no healthy engines") {
		t.Errorf("gaugesBody hides all-down state: %q", out)
	}
	m.snap = core.Snapshot{}
	if out := strip(m.gaugesBody(30)); !strings.Contains(out, "waiting for telemetry") {
		t.Errorf("gaugesBody lost the pre-discovery message: %q", out)
	}
}

// The compact view stands alone: it must explain why it is compact, how to
// quit, and what to do when no engines are found (previously a blank pane).
func TestMinimalViewGuidesRecovery(t *testing.T) {
	m := New(Config{Version: "t"}, nil)
	nm, _ := m.Update(snapMsg(core.Snapshot{Providers: []core.ProviderSnapshot{
		{Label: "ollama", OK: true, OutTokPS: 42},
	}}))
	m = nm.(Model)
	m.w, m.h, m.ready = 40, 12, true
	out := strip(m.View())
	for _, want := range []string{"enlarge window", "q quit", "ollama"} {
		if !strings.Contains(out, want) {
			t.Errorf("minimal view missing %q:\n%s", want, out)
		}
	}

	empty := New(Config{Version: "t"}, nil)
	empty.w, empty.h, empty.ready = 40, 12, true
	out = strip(empty.View())
	for _, want := range []string{"no inference engines detected", "--demo"} {
		if !strings.Contains(out, want) {
			t.Errorf("empty minimal view missing %q:\n%s", want, out)
		}
	}
}

// A dead ingest endpoint must be visible in-band: stderr is hidden under the
// alternate screen, so without this the UI advertises a dead endpoint (and a
// POST target that swallows events) forever.
func TestFeedDeathSurfacesInDashboard(t *testing.T) {
	feedErr := make(chan string, 1)
	m := New(Config{Version: "t", IngestAddr: "127.0.0.1:8420", FeedErr: feedErr}, nil)

	init := m.Init()
	if init == nil {
		t.Fatal("Init must watch the feed-error channel")
	}
	m.w, m.h, m.ready, m.clock = 110, 36, true, time.Now()
	nm, _ := m.Update(snapMsg(core.Snapshot{
		Providers: []core.ProviderSnapshot{{Label: "ollama", OK: true}},
	}))
	m = nm.(Model)
	// A live endpoint advertises its POST target.
	if out := strip(m.View()); !strings.Contains(out, "POST http://127.0.0.1:8420/v1/events") {
		t.Fatalf("live feed lost its POST hint:\n%s", out)
	}

	feedErr <- "http: Server closed"
	cmds := batchCmds(init)
	if len(cmds) == 0 {
		t.Fatal("Init returned no commands")
	}
	// Run every leaf: waitSnap/tickClock legitimately idle or sleep, but the
	// pre-loaded feed channel must deliver immediately.
	done := make(chan tea.Msg, len(cmds))
	for _, c := range cmds {
		go func(c tea.Cmd) { done <- c() }(c)
	}
	var got tea.Msg
	select {
	case got = <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("no Init command produced a message")
	}
	if _, ok := got.(feedDownMsg); !ok {
		t.Fatalf("waiting on the feed channel produced %v, want feedDownMsg", got)
	}
	nm, again := m.Update(got)
	m = nm.(Model)
	if m.feedDown != "http: Server closed" {
		t.Fatalf("feedDownMsg not recorded: %q", m.feedDown)
	}
	if again == nil {
		t.Error("Update must re-arm the feed watcher for later signals")
	}

	out := strip(m.View())
	if strings.Contains(out, "POST http://127.0.0.1:8420") {
		t.Errorf("dead endpoint still advertised:\n%s", out)
	}
	for _, want := range []string{"ingest down", "ingest stopped: http: Server closed"} {
		if !strings.Contains(out, want) {
			t.Errorf("dashboard missing %q after feed death:\n%s", want, out)
		}
	}
}

// batchCmds flattens a tea.Cmd (possibly tea.Batch) into its leaves.
func batchCmds(cmd tea.Cmd) []tea.Cmd {
	msg := cmd()
	batch, ok := msg.(tea.BatchMsg)
	if !ok {
		return []tea.Cmd{cmd}
	}
	return []tea.Cmd(batch)
}

// Pause is a stability affordance for screen-reader and magnifier users:
// while paused nothing on screen may keep moving, and the header clock was
// the one element still churning every second.
func TestPauseFreezesHeaderClock(t *testing.T) {
	m := New(Config{Version: "t"}, nil)
	nm, _ := m.Update(snapMsg(core.Snapshot{
		Providers: []core.ProviderSnapshot{{Label: "ollama", OK: true}},
	}))
	m = nm.(Model)
	m.w, m.h, m.ready = 110, 36, true
	t0 := time.Date(2026, 8, 25, 12, 0, 0, 0, time.Local)
	nm, _ = m.Update(tickMsg(t0))
	m = nm.(Model)
	if out := strip(m.View()); !strings.Contains(out, "12:00:00") {
		t.Fatalf("header clock missing before pause:\n%s", out)
	}

	nm, _ = m.Update(keyMsg(" "))
	m = nm.(Model)
	later := t0.Add(11 * time.Second)
	nm, _ = m.Update(tickMsg(later))
	m = nm.(Model)
	if out := strip(m.View()); strings.Contains(out, "12:00:11") {
		t.Error("paused frame kept ticking the clock")
	}

	nm, _ = m.Update(keyMsg(" "))
	m = nm.(Model)
	nm, _ = m.Update(tickMsg(later))
	m = nm.(Model)
	if out := strip(m.View()); !strings.Contains(out, "12:00:11") {
		t.Error("resumed frame did not pick the clock back up")
	}
}

// The BACKENDS panel must mark down engines by more than color (WCAG 1.4.1):
// the ✗ glyph matches the probe feed's convention and survives greyscale.
func TestBackendsPanelMarksDownEnginesWithoutColor(t *testing.T) {
	m := New(Config{Version: "t"}, nil)
	nm, _ := m.Update(snapMsg(core.Snapshot{Providers: []core.ProviderSnapshot{
		{Label: "vllm", OK: false, Err: "connection refused"},
	}}))
	m = nm.(Model)
	m.w, m.h, m.ready = 110, 36, true
	out := strip(m.View())
	if !strings.Contains(out, "✗") || !strings.Contains(out, "connection refused") {
		t.Errorf("down engine lacks shape marker or error text:\n%s", out)
	}
}
