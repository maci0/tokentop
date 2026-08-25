package gpu

import (
	"slices"
	"testing"
)

// FuzzParseVendorTelemetry drives both exported vendor-output parsers —
// nvidia-smi CSV and rocm-smi JSON — with arbitrary bytes. The same parsers
// run against local CLI output and against whatever a remote SSH host sends
// back in its vitals dump, so a hostile or broken tool must not be able to
// plant negative sensor readings in a sample, mislabel a device's vendor,
// or parse differently from one call to the next.
func FuzzParseVendorTelemetry(f *testing.F) {
	for _, seed := range []string{
		nvidiaCSV,
		rocmJSON,
		"0,a,b,c,d,e,f,g\n",
		"0,Name With, Commas, 65, 12345, 24564, 98, 350.51, 550.54.14",
		"-5,x,-1,-2,-3,-4,-5,driver",
		"99999999999999999999,x,nan,[N/A],1e308,-0,0,",
		"idx,name,65,1,2,3,4,5,6,7,8",
		`{"card0":{"Temperature (Sensor edge) (C)":["1e308"],"GPU use (%)":"nan",` +
			`"VRAM Total Used Memory (B)":-42,"VRAM Total Memory (B)":{"values":[1e309]}}}`,
		`{"cardX":{},"card12":{"Temperature (Junction) (C)":null}}`,
		`{"not a card":"x"}`,
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
	})
}
