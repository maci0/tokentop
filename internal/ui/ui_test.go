package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"

	"tokentop/internal/core"
)

func TestLevelCharBounds(t *testing.T) {
	cases := map[int]rune{-5: ' ', 0: ' ', 4: '▄', 8: '█', 99: '█'}
	for in, want := range cases {
		if got := LevelChar(in); got != want {
			t.Errorf("LevelChar(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestAreaChartShape(t *testing.T) {
	vals := []float64{1, 2, 3, 4, 5}
	out := AreaChart(vals, 10, 3, heatColor)
	rows := strings.Split(out, "\n")
	if len(rows) != 3 {
		t.Fatalf("rows = %d", len(rows))
	}
	for i, r := range rows {
		if w := lipgloss.Width(r); w != 10 {
			t.Errorf("row %d width = %d, want 10", i, w)
		}
	}
	// bottom row must be the fullest
	if strip(rows[2]) == "" {
		t.Fatal("bottom row empty")
	}
}

func TestAreaChartZeroData(t *testing.T) {
	out := AreaChart(nil, 8, 2, heatColor)
	if n := strings.Count(out, "\n") + 1; n != 2 {
		t.Fatalf("zero-data rows = %d", n)
	}
	if strings.Contains(strings.NewReplacer(" ", "", "\n", "").Replace(strip(out)), "█") {
		t.Fatal("zero data rendered filled cells")
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
	for _, want := range []string{"TOKENTOP", "BACKENDS", "ENGINE STATE", "PROBES", "AGENT FEED", "SYS", "llama3"} {
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
	out := StaticFrame(Config{Version: "t"}, snap, 110, 30)
	plain := strip(out)
	for _, want := range []string{"SYS", "mem ", "50%", "swp ", "ld ",
		"nv0 71°", "42%", "20G/80G", "310W"} {
		if !strings.Contains(plain, want) {
			t.Errorf("system strip missing %q in:\n%s", want, plain)
		}
	}
	// wider terminals fit the second GPU and CPU temps too
	wide := strip(StaticFrame(Config{Version: "t"}, snap, 170, 30))
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
	out := strip(StaticFrame(Config{Version: "t"}, snap, 110, 30))
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
	out := strip(StaticFrame(Config{Version: "t"}, snap, 110, 30))
	if !strings.Contains(out, "mem n/a") || !strings.Contains(out, "no sensors found") {
		t.Errorf("empty sys sample not handled:\n%s", out)
	}
}

func TestStaticFrameEmptyState(t *testing.T) {
	out := StaticFrame(Config{Version: "t"}, core.Snapshot{}, 90, 30)
	if !strings.Contains(strip(out), "no inference engines detected") {
		t.Error("empty state hint missing")
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
