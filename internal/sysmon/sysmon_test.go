package sysmon

import (
	"os"
	"path/filepath"
	"testing"

	"tokentop/internal/core"
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

func TestParseLoadavg(t *testing.T) {
	l1, l5, l15 := ParseLoadavg("4.98 2.71 1.03 3/5123 4242")
	if l1 != 4.98 || l5 != 2.71 || l15 != 1.03 {
		t.Errorf("got %.2f %.2f %.2f", l1, l5, l15)
	}
	if a, b, c := ParseLoadavg(""); a != 0 || b != 0 || c != 0 {
		t.Error("empty loadavg must zero")
	}
}

// buildTree materializes files under a fresh temp root.
func buildTree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for path, content := range files {
		full := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestScanTempsPrefersHwmonAndClassifiesGPU(t *testing.T) {
	sysroot := buildTree(t, map[string]string{
		"class/hwmon/hwmon0/name":          "amdgpu\n",
		"class/hwmon/hwmon0/temp1_input":   "67000\n",
		"class/hwmon/hwmon0/temp1_label":   "edge\n",
		"class/hwmon/hwmon0/temp2_input":   "85000\n",
		"class/hwmon/hwmon0/temp2_label":   "junction\n",
		"class/hwmon/hwmon1/name":          "coretemp\n",
		"class/hwmon/hwmon1/temp1_input":   "52000\n",
		"class/hwmon/hwmon1/temp1_label":   "Package id 0\n",
		"class/thermal/thermal_zone0/type": "x86_pkg_temp",
		"class/thermal/thermal_zone0/temp": "99000", // must be ignored: hwmon exists
	})

	temps := ScanTemps(
		filepath.Join(sysroot, "class/hwmon"),
		filepath.Join(sysroot, "class/thermal"),
	)
	if len(temps) != 3 {
		t.Fatalf("temps = %d, want 3: %+v", len(temps), temps)
	}
	if !temps[0].IsGPU || temps[0].MilliC != 85000 {
		t.Errorf("hottest GPU should sort first, got %+v", temps[0])
	}
	if temps[2].IsGPU || temps[2].Label != "package id 0" {
		t.Errorf("cpu reading wrong: %+v", temps[2])
	}
}

func TestScanTempsFallsBackToThermalZones(t *testing.T) {
	sysroot := buildTree(t, map[string]string{
		"class/thermal/thermal_zone0/type": "soc_thermal",
		"class/thermal/thermal_zone0/temp": "45000",
	})
	temps := ScanTemps(
		filepath.Join(sysroot, "class/hwmon"), // empty
		filepath.Join(sysroot, "class/thermal"),
	)
	if len(temps) != 1 || temps[0].Label != "soc_thermal" {
		t.Fatalf("fallback failed: %+v", temps)
	}
}

func TestScanTempsEmptyDirs(t *testing.T) {
	root := t.TempDir()
	if temps := ScanTemps(root, root); len(temps) != 0 {
		t.Fatalf("expected no temps, got %+v", temps)
	}
}
