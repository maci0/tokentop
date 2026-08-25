package sysmon

import (
	"testing"

	"github.com/maci0/toktop/internal/core"
)

const meminfoFixture = `MemTotal:       32872572 kB
MemFree:         4123001 kB
MemAvailable:   18345216 kB
Buffers:         1204992 kB
Cached:          8213455 kB
SwapTotal:       8388604 kB
SwapFree:        6291456 kB
HugePages_Total:       0
`

func TestParseMeminfo(t *testing.T) {
	var s core.SysSample
	ParseMeminfo([]byte(meminfoFixture), &s)

	if want := uint64(32872572) << 10; s.MemTotal != want {
		t.Errorf("MemTotal = %d, want %d", s.MemTotal, want)
	}
	wantUsed := (uint64(32872572) - 18345216) << 10
	if s.MemUsed != wantUsed {
		t.Errorf("MemUsed = %d, want %d", s.MemUsed, wantUsed)
	}
	if s.SwapTotal != uint64(8388604)<<10 {
		t.Errorf("SwapTotal = %d", s.SwapTotal)
	}
	if want := (uint64(8388604) - 6291456) << 10; s.SwapUsed != want {
		t.Errorf("SwapUsed = %d, want %d", s.SwapUsed, want)
	}
}

func TestParseMeminfoGarbage(t *testing.T) {
	var s core.SysSample
	ParseMeminfo([]byte("nonsense\n:\nMemTotal: abc kB\n"), &s)
	if s.MemTotal != 0 || s.MemUsed != 0 {
		t.Fatalf("garbage parsed as data: %+v", s)
	}
}

// Some ballooning/virtualized kernels report MemAvailable above MemTotal;
// the ssh vitals path can feed such text in from any remote. The used count
// must saturate at zero instead of wrapping to ~2^64 bytes.
func TestParseMeminfoAvailableExceedsTotal(t *testing.T) {
	var s core.SysSample
	ParseMeminfo([]byte("MemTotal:       1000000 kB\nMemAvailable:   1200000 kB\nSwapTotal:       500000 kB\nSwapFree:        600000 kB\n"), &s)
	if s.MemUsed != 0 {
		t.Errorf("MemUsed = %d, want 0 (no wrap)", s.MemUsed)
	}
	if s.SwapUsed != 0 {
		t.Errorf("SwapUsed = %d, want 0 (no wrap)", s.SwapUsed)
	}
}

func TestParseLoadavg(t *testing.T) {
	l1, l5, l15 := ParseLoadavg("4.98 2.71 1.03 3/5123 4242")
	if l1 != 4.98 || l5 != 2.71 || l15 != 1.03 {
		t.Errorf("got %.2f %.2f %.2f", l1, l5, l15)
	}
	if a, b, c := ParseLoadavg(""); a != 0 || b != 0 || c != 0 {
		t.Error("empty loadavg must zero")
	}
}
