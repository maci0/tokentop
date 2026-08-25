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

	temps := scanTemps(
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
	temps := scanTemps(
		filepath.Join(sysroot, "class/hwmon"), // empty
		filepath.Join(sysroot, "class/thermal"),
	)
	if len(temps) != 1 || temps[0].Label != "soc_thermal" {
		t.Fatalf("fallback failed: %+v", temps)
	}
}

func TestScanTempsEmptyDirs(t *testing.T) {
	root := t.TempDir()
	if temps := scanTemps(root, root); len(temps) != 0 {
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
	if n := npuDisplayName("qaic"); n != "Qualcomm Cloud AI100" {
		t.Errorf("qaic = %q", n)
	}
	if n := npuDisplayName("somethingelse"); n != "somethingelse" {
		t.Errorf("unknown driver must pass through: %q", n)
	}
}

// parseNvidiaVersion feeds the identity row's driver/CUDA readout; pin the
// accepted layout and the missing-field fallbacks.
func TestParseNvidiaVersion(t *testing.T) {
	drv, cuda := parseNvidiaVersion(
		"NVRM version: NVIDIA UNIX x86_64 Kernel Module  550.54.14  Wed May  1 23:26:36 UTC 2024\n" +
			"NVRM: Driver Version: 550.54.14         CUDA Version: 12.4\n")
	if drv != "550.54.14" || cuda != "12.4" {
		t.Errorf("drv=%q cuda=%q", drv, cuda)
	}
	drv, cuda = parseNvidiaVersion("NVRM: Driver Version: 535.104.05\n") // CUDA field absent
	if drv != "535.104.05" || cuda != "" {
		t.Errorf("drv=%q cuda=%q, want driver only", drv, cuda)
	}
	if drv, cuda := parseNvidiaVersion("no version data here"); drv != "" || cuda != "" {
		t.Errorf("unrelated text parsed as %q/%q", drv, cuda)
	}
}

// utsField must stop at the NUL padding of a Utsname char array.
func TestUtsField(t *testing.T) {
	b := make([]byte, 65)
	copy(b, "6.1.0-18-amd64")
	if got := utsField(b); got != "6.1.0-18-amd64" {
		t.Errorf("utsField = %q", got)
	}
	if got := utsField(make([]byte, 65)); got != "" {
		t.Errorf("all-NUL array = %q", got)
	}
}
