package gpu

import (
	"math"
	"testing"

	"tokentop/internal/core"
)

const nvidiaCSV = `0, NVIDIA GeForce RTX 4090, 65, 12345, 24564, 98, 350.51, 550.54.14
1, NVIDIA A100-SXM4-80GB, 71, 64000, 81920, 42, 275.00, [N/A]
2, Some, Name With Commas, 55, 100, 200, [N/A], [N/A], [N/A]
`

func TestParseNvidiaSMI(t *testing.T) {
	devs := ParseNvidiaSMI([]byte(nvidiaCSV))
	if len(devs) != 3 {
		t.Fatalf("devices = %d", len(devs))
	}
	d := devs[0]
	if d.Index != 0 || d.Name != "NVIDIA GeForce RTX 4090" || d.MilliC != 65000 {
		t.Errorf("dev0 = %+v", d)
	}
	if want := uint64(12345) << 20; d.MemUsed != want {
		t.Errorf("memused = %d want %d", d.MemUsed, want)
	}
	if d.UtilPct != 98 || d.PowerW != 350.51 {
		t.Errorf("util/power = %v/%v", d.UtilPct, d.PowerW)
	}
	if d.Driver != "550.54.14" {
		t.Errorf("extended fields: %+v", d)
	}
	if devs[1].Driver != "" { // "[N/A]" degrades to empty
		t.Errorf("driver N/A handling: %+v", devs[1])
	}
	if devs[2].Name != "Some, Name With Commas" || devs[2].PowerW != 0 ||
		devs[2].UtilPct != 0 || devs[2].Driver != "" {
		t.Errorf("comma-name / N/A handling: %+v", devs[2])
	}
}

const rocmJSON = `{
  "card0": {
    "Temperature (Sensor edge) (C)": "52.0",
    "Temperature (Sensor junction) (C)": ["61.0"],
    "VRAM Total Used Memory (B)": "17179869184",
    "VRAM Total Memory (B)": "68719476736",
    "GPU use (%)": "87"
  }
}`

func TestParseRocmSMI(t *testing.T) {
	devs := ParseRocmSMI([]byte(rocmJSON))
	if len(devs) != 1 {
		t.Fatalf("devices = %d", len(devs))
	}
	d := devs[0]
	if d.Vendor != "amd" || d.Index != 0 {
		t.Errorf("identity: %+v", d)
	}
	if d.MilliC != 52000 { // edge preferred over junction
		t.Errorf("temp = %d", d.MilliC)
	}
	if d.MemUsed != 16<<30 || d.MemTotal != 64<<30 {
		t.Errorf("vram = %d/%d", d.MemUsed, d.MemTotal)
	}
	if d.UtilPct != 87 {
		t.Errorf("util = %v", d.UtilPct)
	}
}

func TestParseXpuDiscoveryAndMetrics(t *testing.T) {
	disc := []byte(`{"devices":[{"device_id":0,"device_name":"Intel(R) Arc(TM) A770"},{"device_id":1,"device_name":"Intel(R) Data Center GPU Max"}]}`)
	order := parseXpuDiscovery(disc)
	if len(order) != 2 || order[1].Name != "Intel(R) Data Center GPU Max" {
		t.Fatalf("discovery parse: %+v", order)
	}
	metrics := []byte(`{"device_id":"0","metrics":{
		"gpu_utilization":{"values":[63.5]},
		"gpu_temperature":{"values":[58]},
		"memory_used":{"values":[1024]},
		"gpu_power":{"values":[180.5]}
	}}`)
	dev, ok := parseXpuMetrics(metrics, 0)
	if !ok {
		t.Fatal("metrics parse failed")
	}
	if dev.MilliC != 58000 || dev.UtilPct != 63.5 || dev.MemUsed != 1024 || dev.PowerW != 180.5 {
		t.Errorf("metrics: %+v", dev)
	}

	bare := []byte(`[{"device_id":3,"device_name":"iGPU"}]`)
	order = parseXpuDiscovery(bare)
	if len(order) != 1 || order[0].ID != 3 {
		t.Errorf("bare array discovery: %+v", order)
	}
}

func TestFlexF(t *testing.T) {
	cases := map[string]float64{
		"[N/A]": 0, "[Not Supported]": 0, "42.5": 42.5, "-1": 0, " 12 ": 12,
		"NaN": 0, "nan": 0,
	}
	for in, want := range cases {
		if got := flexF(in); got != want {
			t.Errorf("flexF(%q) = %v, want %v", in, got, want)
		}
	}
}

// Vendor output is untrusted: a broken CLI or JSON feed may report any
// finite magnitude, and the float->int conversion behind MilliC/MemUsed is
// implementation-defined past the type's range (on amd64 a huge temp
// renders as a huge negative number). Absurd magnitudes must saturate.
func TestSaturatesAbsurdVendorNumbers(t *testing.T) {
	devs := ParseNvidiaSMI([]byte("0, GPU, 1e300, 24564, 81920, 50, 300, 550.54.14\n" +
		"1, GPU2, 55, 1e300, 81920, 50, 300, 550.54.14\n"))
	if len(devs) != 2 {
		t.Fatalf("devices = %d", len(devs))
	}
	if devs[0].MilliC != math.MaxInt {
		t.Errorf("MilliC = %d, want saturation", devs[0].MilliC)
	}
	if devs[0].MemTotal != 81920<<20 || devs[0].UtilPct != 50 || devs[0].PowerW != 300 {
		t.Errorf("sane columns disturbed: %+v", devs[0])
	}
	if devs[1].MemUsed != math.MaxUint64 {
		t.Errorf("MemUsed = %d, want saturation", devs[1].MemUsed)
	}

	if got := satInt(2.7); got != 2 {
		t.Errorf("satInt(2.7) = %d, want 2", got)
	}
	for _, v := range []float64{0, -5} {
		if satInt(v) != 0 || satUint(v) != 0 {
			t.Errorf("satInt/satUint(%v) must collapse to zero", v)
		}
	}
	if satInt(1e300) != math.MaxInt || satUint(1e300) != math.MaxUint64 {
		t.Error("huge magnitudes must saturate, not wrap")
	}
}

func TestVendorOrdering(t *testing.T) {
	if vendorOrder["nvidia"] >= vendorOrder["amd"] || vendorOrder["amd"] >= vendorOrder["intel"] {
		t.Fatal("vendor sort order drifted")
	}
	var _ core.GPUDevice // keep import honest
}
