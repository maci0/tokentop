//go:build linux

package gpu

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/maci0/toktop/internal/core"
)

// init wires the amdgpu sysfs walker as the platform extra. amdgpu exposes
// VRAM accounting, busy percentage and hwmon temperatures with no tooling,
// which covers AMD GPUs when rocm-smi is absent (common on desktops).
func init() {
	platformExtras = func(context.Context) []core.GPUDevice { return scanAmdSysfs("/sys/class/drm") }
}

func scanAmdSysfs(drmRoot string) []core.GPUDevice {
	cards, err := filepath.Glob(filepath.Join(drmRoot, "card[0-9]*"))
	if err != nil {
		return nil
	}
	var devs []core.GPUDevice
	for _, card := range cards {
		dev := filepath.Join(card, "device")
		if _, err := os.Stat(filepath.Join(dev, "mem_info_vram_used")); err != nil {
			continue // not an amdgpu-bound card
		}
		d := core.GPUDevice{Vendor: "amd"}
		if _, rest, ok := strings.Cut(filepath.Base(card), "card"); ok {
			d.Index, _ = strconv.Atoi(rest)
		}
		if b, err := os.ReadFile(filepath.Join(dev, "product_name")); err == nil {
			d.Name = strings.TrimSpace(string(b))
		}
		if u, err := readU64(dev, "mem_info_vram_used"); err == nil {
			d.MemUsed = u
		}
		if t, err := readU64(dev, "mem_info_vram_total"); err == nil {
			d.MemTotal = t
		}
		if b, err := os.ReadFile(filepath.Join(dev, "gpu_busy_percent")); err == nil {
			d.UtilPct, _ = strconv.ParseFloat(strings.TrimSpace(string(b)), 64)
		}
		scanAmdHwmon(filepath.Join(dev, "hwmon"), &d)
		devs = append(devs, d)
	}
	return devs
}

// scanAmdHwmon lifts temperature and power readings out of the card's hwmon
// dir.
func scanAmdHwmon(hwmonDir string, d *core.GPUDevice) {
	matches, _ := filepath.Glob(filepath.Join(hwmonDir, "hwmon*"))
	for _, h := range matches {
		if v, ok := readI(h, "temp1_input"); ok && d.MilliC == 0 {
			d.MilliC = v
		}
		if b, err := os.ReadFile(filepath.Join(h, "power1_average")); err == nil {
			if uw, err := strconv.ParseFloat(strings.TrimSpace(string(b)), 64); err == nil && uw > 0 {
				d.PowerW = uw / 1e6 // microwatts -> watts
			}
		}
	}
}

func readI(dir, name string) (int, bool) {
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return 0, false
	}
	v, err := strconv.Atoi(strings.TrimSpace(string(b)))
	return v, err == nil
}

func readU64(dir, name string) (uint64, error) {
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return 0, err
	}
	return strconv.ParseUint(strings.TrimSpace(string(b)), 10, 64)
}
