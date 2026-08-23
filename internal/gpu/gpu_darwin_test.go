//go:build darwin

package gpu

import "testing"

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
