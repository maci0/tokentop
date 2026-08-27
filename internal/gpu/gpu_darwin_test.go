//go:build darwin

package gpu

import (
	"context"
	"testing"
	"time"
)

func TestParseIOAccelerator(t *testing.T) {
	text := `+-o AGXAccelerator  <class AGXAccelerator>  {
       "PerformanceStatistics" = {
         "In use GPU memory" = 4123456789
         "Device Utilization %" = 37
         "alloc GPUMemory" = 100
       }
     }

+-o IOAccelerator2  <class AGXAccelerator>  {
       "PerformanceStatistics" = {
         "In use GPU memory" = 876543211
         "GPU Device Utilization %" = 90
       }
     }`
	mem, util := parseIOAccelerator(text)
	if mem != 4123456789+876543211 {
		t.Errorf("memUsed = %d", mem)
	}
	if util != 90 {
		t.Errorf("utilPct = %v, want max across devices = 90", util)
	}

	mem, util = parseIOAccelerator("no relevant keys here")
	if mem != 0 || util != 0 {
		t.Errorf("absent keys must yield zeros, got %d %v", mem, util)
	}
}

func TestParseSizeString(t *testing.T) {
	cases := []struct {
		in   string
		want uint64
	}{
		{"128 GB", 128 << 30},
		{"8192 MB", 8192 << 20},
		{"64 KB", 64 << 10},
		{"junk GB", 0},
		{"128 XB", 0},
		{"128", 0},
		// Out-of-range magnitudes must saturate, not convert out of range
		// into an arbitrary value that the identity cache keeps forever.
		{"1e300 GB", ^uint64(0)},
	}
	for _, c := range cases {
		if got := parseSizeString(c.in); got != c.want {
			t.Errorf("parseSizeString(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestPlatformExtrasDetachesCachedDevices(t *testing.T) {
	a := platformExtras(context.Background())
	b := platformExtras(context.Background())
	if len(a) == 0 || len(b) == 0 {
		t.Skip("no apple GPUs reported by system_profiler")
	}
	// Each call must return its own backing array: callers publish returned
	// devices into snapshots that outlive the call, while applyIOAccelStats
	// keeps overlaying fresh ioreg numbers on every Sample.
	if &a[0] == &b[0] {
		t.Fatal("platformExtras handed the same device slice to two samples")
	}
	b[0].MemUsed = 12345
	if a[0].MemUsed == 12345 {
		t.Fatal("samples share a backing array; a later Sample can race an earlier snapshot's render")
	}
}

func TestNoteIOAccelKeepsLastGoodOnFailure(t *testing.T) {
	ioAccelMu.Lock()
	ioAccelMemUsed, ioAccelUtil = 0, 0
	ioAccelAt = time.Time{}
	ioAccelMu.Unlock()
	t.Cleanup(func() {
		ioAccelMu.Lock()
		ioAccelMemUsed, ioAccelUtil = 0, 0
		ioAccelAt = time.Time{}
		ioAccelMu.Unlock()
	})

	noteIOAccel(true, []byte(`"In use GPU memory" = 4123456789
"Device Utilization %" = 37
`))
	if ioAccelMemUsed != 4123456789 || ioAccelUtil != 37 {
		t.Fatalf("after success mem=%d util=%v", ioAccelMemUsed, ioAccelUtil)
	}
	noteIOAccel(false, nil)
	if ioAccelMemUsed != 4123456789 || ioAccelUtil != 37 {
		t.Fatalf("failure overwrote last good: mem=%d util=%v", ioAccelMemUsed, ioAccelUtil)
	}
}
