//go:build darwin

package gpu

import (
	"context"
	"encoding/json"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"tokentop/internal/core"
)

// Apple GPU support has two halves:
//
//   - Identity (chip name, VRAM size) comes from system_profiler once per
//     process. There is no pure-Go IOKit binding without cgo, and
//     system_profiler is its documented CLI, so shelling out is deliberate.
//   - Live statistics come from ioreg's IOAccelerator PerformanceStatistics,
//     readable without root: "In use GPU memory" in bytes plus utilization.
//     This refreshes on a throttle since Sample runs every poll cycle.

func init() {
	var (
		once   sync.Once
		idents []core.GPUDevice
	)
	platformExtras = func(context.Context) []core.GPUDevice {
		once.Do(func() { idents = appleGPUs() })
		if len(idents) == 0 {
			return nil
		}
		// Hand out a copy: samples are published to the UI goroutine, and a
		// later Sample overlaying live ioreg numbers onto the shared identity
		// slice would data-race with a render of an earlier snapshot.
		devs := append([]core.GPUDevice(nil), idents...)
		applyIOAccelStats(devs)
		return devs
	}
}

// identityTimeout bounds the one-shot system_profiler call. Generous on
// purpose: the result is cached forever, so a too-tight cap would leave the
// Mac with no GPU row for the whole process lifetime.
const identityTimeout = 10 * time.Second

func appleGPUs() []core.GPUDevice {
	ctx, cancel := context.WithTimeout(context.Background(), identityTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "system_profiler", "SPDisplaysDataType", "-json")
	cmd.WaitDelay = pipeGrace // a hung profiler must not hold the caller past its deadline
	out, err := cmd.Output()
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

var (
	ioAccelMu      sync.Mutex
	ioAccelAt      time.Time
	ioAccelMemUsed uint64
	ioAccelUtil    float64
)

const ioAccelRefresh = 2 * time.Second

// applyIOAccelStats overlays live memory/utilization onto the first device:
// aggregate accelerator numbers are exact on single-GPU Macs, which dominate;
// multi-GPU Mac Pros would need device matching that ioreg output does not
// expose portably, so they keep zeros rather than wrong ones.
func applyIOAccelStats(devs []core.GPUDevice) {
	ioAccelMu.Lock()
	defer ioAccelMu.Unlock()
	if time.Since(ioAccelAt) < ioAccelRefresh {
		devs[0].MemUsed = ioAccelMemUsed
		devs[0].UtilPct = ioAccelUtil
		return
	}
	// run() caps the spawn so a hung ioreg cannot pin ioAccelMu and stall
	// every later Sample.
	out, ok := run(context.Background(), "ioreg", "-r", "-d", "1", "-w", "0", "-c", "IOAccelerator")
	if !ok {
		ioAccelMemUsed, ioAccelUtil = 0, 0
	} else {
		ioAccelMemUsed, ioAccelUtil = parseIOAccelerator(string(out))
	}
	ioAccelAt = time.Now()
	devs[0].MemUsed = ioAccelMemUsed
	devs[0].UtilPct = ioAccelUtil
}

// parseIOAccelerator sums "In use GPU memory" across accelerators and takes
// the max utilization percentage. Key spellings drift between macOS
// releases; unknown lines are simply ignored.
func parseIOAccelerator(text string) (memUsed uint64, utilPct float64) {
	for _, line := range strings.Split(text, "\n") {
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key := strings.TrimSpace(k)
		switch key {
		case `"In use GPU memory"`, `"gpumem_inuse"`:
			memUsed += parseIoregNum(v)
		case `"Device Utilization %"`, `"GPU Device Utilization %"`:
			if u := parseIoregNum(v); float64(u) > utilPct {
				utilPct = float64(u)
			}
		}
	}
	return memUsed, utilPct
}

func parseIoregNum(s string) uint64 {
	s = strings.TrimSpace(strings.Trim(s, `"`))
	v, _ := strconv.ParseUint(s, 10, 64)
	return v // 0 on parse failure, the desired zero value
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
