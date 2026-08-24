package gpu

import (
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
	order, names := parseXpuDiscovery(disc)
	if len(order) != 2 || names[1] != "Intel(R) Data Center GPU Max" {
		t.Fatalf("discovery parse: %+v %+v", order, names)
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
	order, _ = parseXpuDiscovery(bare)
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

func TestVendorOrdering(t *testing.T) {
	if vendorOrder["nvidia"] >= vendorOrder["amd"] || vendorOrder["amd"] >= vendorOrder["intel"] {
		t.Fatal("vendor sort order drifted")
	}
	var _ core.GPUDevice // keep import honest
}
