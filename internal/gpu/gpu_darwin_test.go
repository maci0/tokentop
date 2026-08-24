//go:build darwin

package gpu

import (
	"context"
	"testing"
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
