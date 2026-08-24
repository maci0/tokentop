//go:build linux

package gpu

import (
	"os"
	"path/filepath"
	"testing"
)

func TestScanAmdSysfs(t *testing.T) {
	root := t.TempDir()
	write := func(rel, content string) {
		p := filepath.Join(root, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(content), 0o644)
	}
	write("card0/device/mem_info_vram_used", "4294967296")
	write("card0/device/mem_info_vram_total", "17179869184")
	write("card0/device/gpu_busy_percent", "73")
	write("card0/device/hwmon/hwmon5/temp1_input", "58000")
	write("card0/device/hwmon/hwmon5/power1_average", "150000000")
	write("card0/device/product_name", "AMD Radeon PRO W7900\n")
	write("card1/device/error", "not an amdgpu") // no mem_info files -> skipped

	devs := scanAmdSysfs(root)
	if len(devs) != 1 {
		t.Fatalf("devices = %d: %+v", len(devs), devs)
	}
	d := devs[0]
	if d.Vendor != "amd" || d.Index != 0 || d.MilliC != 58000 ||
		d.MemUsed != 4<<30 || d.MemTotal != 16<<30 || d.UtilPct != 73 ||
		d.Name != "AMD Radeon PRO W7900" {
		t.Errorf("amdgpu device: %+v", d)
	}
	if d.PowerW != 150 {
		t.Errorf("hwmon extras: %+v", d)
	}
}
