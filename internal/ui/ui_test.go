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
	if stripANSI(rows[2]) == "" {
		t.Fatal("bottom row empty")
	}
}

func TestAreaChartZeroData(t *testing.T) {
	out := AreaChart(nil, 8, 2, heatColor)
	if n := strings.Count(out, "\n") + 1; n != 2 {
		t.Fatalf("zero-data rows = %d", n)
	}
	if strings.Contains(strings.NewReplacer(" ", "", "\n", "").Replace(stripANSI(out)), "█") {
		t.Fatal("zero data rendered filled cells")
	}
}

// stripANSI removes escape sequences (test-local copy of ui.strip).
func stripANSI(s string) string {
	var b strings.Builder
	esc := false
	for _, r := range s {
		switch {
		case esc:
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
				esc = false
			}
		case r == '\x1b':
			esc = true
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
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

func TestAggHistTailAligned(t *testing.T) {
	s := core.Snapshot{Providers: []core.ProviderSnapshot{
		{OutHist: []float64{1, 2, 3}},
		{OutHist: []float64{10, 20}},
	}}
	got := aggHist(s, true) // tails align: [12, 23]
	if len(got) != 2 || got[0] != 12 || got[1] != 23 {
		t.Fatalf("aggHist = %v", got)
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
		if !strings.Contains(stripANSI(out), want) {
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
	plain := stripANSI(out)
	for _, want := range []string{"SYS", "mem ", "50%", "swp ", "ld ",
		"nv0 71°", "42%", "20G/80G", "310W"} {
		if !strings.Contains(plain, want) {
			t.Errorf("system strip missing %q in:\n%s", want, plain)
		}
	}
	// wider terminals fit the second GPU and CPU temps too
	wide := stripANSI(StaticFrame(Config{Version: "t"}, snap, 170, 30))
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
	out := stripANSI(StaticFrame(Config{Version: "t"}, snap, 110, 30))
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
	out := stripANSI(StaticFrame(Config{Version: "t"}, snap, 110, 30))
	if !strings.Contains(out, "mem n/a") || !strings.Contains(out, "no sensors found") {
		t.Errorf("empty sys sample not handled:\n%s", out)
	}
}

func TestStaticFrameEmptyState(t *testing.T) {
	out := StaticFrame(Config{Version: "t"}, core.Snapshot{}, 90, 30)
	if !strings.Contains(stripANSI(out), "no inference engines detected") {
		t.Error("empty state hint missing")
	}
}
