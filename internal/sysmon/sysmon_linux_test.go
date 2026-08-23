//go:build linux

package sysmon

import (
	"os"
	"path/filepath"
	"testing"
)

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
		"class/thermal/thermal_zone0/temp": "99000", // ignored: hwmon exists
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

func TestScanAccelDriversCountsDevices(t *testing.T) {
	root := t.TempDir()
	mk := func(accel, driver string) {
		full := filepath.Join(root, "class", "accel", accel, "device")
		if err := os.MkdirAll(full, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(root, "drivers", driver), filepath.Join(full, "driver")); err != nil {
			t.Fatal(err)
		}
	}
	mk("accel0", "amdxdna")
	mk("accel1", "amdxdna")
	mk("accel2", "intel_vpu")

	got := scanAccelDrivers(filepath.Join(root, "class", "accel"))
	want := []string{"AMD XDNA NPU x2", "Intel NPU"} // accel0/1 are amdxdna
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d] = %q want %q", i, got[i], want[i])
		}
	}

	if n := npuDisplayName("ivpu"); n != "Intel NPU" {
		t.Errorf("ivpu alias = %q", n)
	}
}
