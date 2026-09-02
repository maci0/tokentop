package gpu

import (
	"slices"
	"testing"

	"github.com/maci0/toktop/internal/core"
)

// FuzzParseVendorTelemetry drives the vendor-output parsers (nvidia-smi
// CSV, rocm-smi JSON, xpu-smi discovery/metrics JSON) with arbitrary
// bytes. The same parsers run against local CLI output and against
// whatever a remote SSH host sends back in its vitals dump, so a hostile
// or broken tool must not be able to plant negative sensor readings in a
// sample, mislabel a device's vendor, or parse differently from one call
// to the next.
func FuzzParseVendorTelemetry(f *testing.F) {
	for _, seed := range []string{
		nvidiaCSV,
		rocmJSON,
		xpuDiscoveryJSON,
		xpuMetricsJSON,
		xpuDiscoveryBare,
		"0,a,b,c,d,e,f,g\n",
		"0,Name With, Commas, 65, 12345, 24564, 98, 350.51, 550.54.14",
		"-5,x,-1,-2,-3,-4,-5,driver",
		"99999999999999999999,x,nan,[N/A],1e308,-0,0,",
		"idx,name,65,1,2,3,4,5,6,7,8",
		`{"card0":{"Temperature (Sensor edge) (C)":["1e308"],"GPU use (%)":"nan",` +
			`"VRAM Total Used Memory (B)":-42,"VRAM Total Memory (B)":{"values":[1e309]}}}`,
		`{"cardX":{},"card12":{"Temperature (Junction) (C)":null}}`,
		`{"not a card":"x"}`,
		`{"devices":[{"device_id":-3,"device_name":"x"},{"device_id":0}]}`,
		`{"metrics":{"gpu_utilization":{"values":["nan"]},"gpu_temperature":{"values":[-9]},` +
			`"gpu_power":1e308,"memory_used":{"values":[{"values":[4]}]}}}`,
		`{"metrics":{"gpu_utilization":{"values":{"values":{"values":[1]}}}}}`,
		"not csv at all\n",
		"{broken json",
		"",
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		nv := ParseNvidiaSMI(data)
		for i, d := range nv {
			if d.Vendor != "nvidia" {
				t.Fatalf("nvidia dev %d vendor = %q", i, d.Vendor)
			}
			if d.MilliC < 0 || d.UtilPct < 0 || d.PowerW < 0 {
				t.Fatalf("nvidia dev %d has an impossible reading: %+v", i, d)
			}
		}
		if nv2 := ParseNvidiaSMI(data); !slices.Equal(nv, nv2) {
			t.Fatal("ParseNvidiaSMI is not deterministic")
		}

		amd := ParseRocmSMI(data)
		for i, d := range amd {
			if d.Vendor != "amd" {
				t.Fatalf("rocm dev %d vendor = %q", i, d.Vendor)
			}
			if d.UtilPct < 0 || d.PowerW < 0 {
				t.Fatalf("rocm dev %d has an impossible reading: %+v", i, d)
			}
		}
		if amd2 := ParseRocmSMI(data); !slices.Equal(amd, amd2) {
			t.Fatal("ParseRocmSMI is not deterministic")
		}

		intel := parseXpuDiscovery(data)
		if intel2 := parseXpuDiscovery(data); !slices.Equal(intel, intel2) {
			t.Fatal("parseXpuDiscovery is not deterministic")
		}

		dev, ok := parseXpuMetrics(data, 3)
		if !ok {
			if dev != (core.GPUDevice{}) {
				t.Fatalf("rejected xpu metrics still returned %+v", dev)
			}
		} else {
			if dev.Vendor != "intel" {
				t.Fatalf("xpu vendor = %q", dev.Vendor)
			}
			if dev.Index != 3 {
				t.Fatalf("xpu index = %d, want the caller-supplied 3", dev.Index)
			}
			if dev.MilliC < 0 || dev.UtilPct < 0 || dev.PowerW < 0 {
				t.Fatalf("xpu has an impossible reading: %+v", dev)
			}
		}
		if dev2, ok2 := parseXpuMetrics(data, 3); ok2 != ok || dev2 != dev {
			t.Fatal("parseXpuMetrics is not deterministic")
		}
	})
}
