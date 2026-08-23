//go:build darwin

package gpu

import (
	"context"
	"encoding/json"
	"os/exec"
	"strconv"
	"strings"
	"sync"

	"tokentop/internal/core"
)

// init wires Apple GPU discovery via system_profiler. This shells out because
// IOKit/Metal device queries have no pure-Go API; it is slow (~1-2s) so the
// result is cached for the process lifetime. Utilization and temperatures are
// not exposed without root (powermetrics), so they stay zero.
func init() {
	var (
		once   sync.Once
		cached []core.GPUDevice
	)
	platformExtras = func(context.Context) []core.GPUDevice {
		once.Do(func() { cached = appleGPUs() })
		return cached
	}
}

func appleGPUs() []core.GPUDevice {
	out, err := exec.Command("system_profiler", "SPDisplaysDataType", "-json").Output()
	if err != nil {
		return nil
	}
	var doc struct {
		Displays []map[string]any `json:"SPDisplaysDataType"`
	}
	if json.Unmarshal(out, &doc) != nil {
		return nil
	}
	var devs []core.GPUDevice
	for _, d := range doc.Displays {
		dev := core.GPUDevice{Vendor: "apple"}
		if name, ok := d["_name"].(string); ok {
			dev.Name = name
		}
		for k, v := range d {
			lk := strings.ToLower(k)
			if strings.Contains(lk, "vram") {
				if s, ok := v.(string); ok {
					dev.MemTotal = parseSizeString(s)
				}
			}
			if dev.Name == "" && k == "sppci_model" {
				if s, ok := v.(string); ok {
					dev.Name = s
				}
			}
		}
		if dev.Name == "" {
			continue // skip anonymous entries
		}
		dev.Index = len(devs)
		devs = append(devs, dev)
	}
	return devs
}

// parseSizeString reads vendor sizes like "128 GB", "8192 MB".
func parseSizeString(s string) uint64 {
	f := strings.Fields(s)
	if len(f) < 2 {
		return 0
	}
	n, err := strconv.ParseFloat(f[0], 64)
	if err != nil {
		return 0
	}
	switch strings.ToLower(strings.TrimSuffix(f[1], "B")) {
	case "k":
		return uint64(n * 1024)
	case "m":
		return uint64(n * 1024 * 1024)
	case "g":
		return uint64(n * 1024 * 1024 * 1024)
	default:
		return 0
	}
}
