// Package gpu samples accelerator telemetry across vendors.
//
// NVIDIA/AMD/Intel are read through their vendor CLIs (nvidia-smi,
// rocm-smi, xpu-smi). We shell out deliberately: NVML and Level Zero have
// no stable in-process Go API without cgo-linking driver libraries, and the
// vendor CLIs are their documented interfaces. Tool presence is cached for
// the process lifetime.
package gpu

import (
	"context"
	"encoding/json"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"tokentop/internal/core"
)

const runTimeout = 2500 * time.Millisecond

// pipeGrace bounds Output's wait on the command's output pipes after the
// process exits or is killed by the context deadline: a grandchild inheriting
// stdout (nested shells, CLI wrappers) would otherwise hold the read end
// open past every deadline and pin the sampler's caller indefinitely.
const pipeGrace = 500 * time.Millisecond

// platformExtras lets GOOS-specific files contribute devices (amdgpu sysfs
// on Linux, system_profiler on macOS).
var platformExtras func(ctx context.Context) []core.GPUDevice

type toolInfo struct {
	path string
	ok   bool
}

var tools sync.Map // tool name -> toolInfo

func lookup(name string) (string, bool) {
	if v, ok := tools.Load(name); ok {
		ti := v.(*toolInfo)
		return ti.path, ti.ok
	}
	p, err := exec.LookPath(name)
	ti := &toolInfo{path: p, ok: err == nil}
	tools.Store(name, ti)
	return ti.path, ti.ok
}

func run(ctx context.Context, path string, args ...string) ([]byte, bool) {
	c, cancel := context.WithTimeout(ctx, runTimeout)
	defer cancel()
	cmd := exec.CommandContext(c, path, args...)
	cmd.WaitDelay = pipeGrace
	out, err := cmd.Output()
	if err != nil {
		return nil, false
	}
	return out, true
}

var vendorOrder = map[string]int{"nvidia": 0, "amd": 1, "intel": 2, "apple": 3}

// Sample collects devices from every vendor present on the host.
func Sample(ctx context.Context) []core.GPUDevice {
	var devs []core.GPUDevice

	if p, ok := lookup("nvidia-smi"); ok {
		if out, ok2 := run(ctx, p,
			"--query-gpu=index,name,temperature.gpu,memory.used,memory.total,utilization.gpu,power.draw,fan.speed,clocks.sm,driver_version,compute_cap",
			"--format=csv,noheader,nounits"); ok2 {
			devs = append(devs, ParseNvidiaSMI(out)...)
		}
	}

	amd := sampleAMD(ctx)
	devs = append(devs, amd...)

	if p, ok := lookup("xpu-smi"); ok {
		devs = append(devs, sampleXPU(ctx, p)...)
	}

	sort.SliceStable(devs, func(i, j int) bool {
		if vendorOrder[devs[i].Vendor] != vendorOrder[devs[j].Vendor] {
			return vendorOrder[devs[i].Vendor] < vendorOrder[devs[j].Vendor]
		}
		return devs[i].Index < devs[j].Index
	})
	return devs
}

func sampleAMD(ctx context.Context) []core.GPUDevice {
	if p, ok := lookup("rocm-smi"); ok {
		if out, ok2 := run(ctx, p,
			"--showtemp", "--showusemem", "--showmeminfo", "vram", "--showuse", "--json"); ok2 {
			if devs := ParseRocmSMI(out); len(devs) > 0 {
				return devs
			}
		}
	}
	if platformExtras != nil {
		return platformExtras(ctx)
	}
	return nil
}

func sampleXPU(ctx context.Context, xpu string) []core.GPUDevice {
	out, ok := run(ctx, xpu, "discovery", "-j")
	if !ok {
		return nil
	}
	type disc struct {
		ID   int
		Name string
	}
	discs, names := parseXpuDiscovery(out)
	var devs []core.GPUDevice
	for _, d := range discs {
		if len(devs) >= 4 { // bound process spawns on multi-GPU nodes
			break
		}
		mo, ok2 := run(ctx, xpu, "metrics", "-d", strconv.Itoa(d.ID), "-j")
		if !ok2 {
			continue
		}
		if dev, ok3 := parseXpuMetrics(mo, d.ID); ok3 {
			dev.Vendor = "intel"
			dev.Name = names[d.ID]
			devs = append(devs, dev)
		}
	}
	return devs
}

// ParseNvidiaSMI reads CSV rows of
// index,name,temp,memused,memtotal,util,power,fan,clocks.sm,driver,compute_cap.
// The name may contain commas; the last nine fields are always fixed.
// Exported so the remote ssh path can parse nvidia-smi output gathered from
// another host with the exact same rules.
func ParseNvidiaSMI(b []byte) []core.GPUDevice {
	const fixedTail = 9
	var devs []core.GPUDevice
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		f := strings.Split(line, ",")
		if len(f) < fixedTail+1 {
			continue
		}
		idx, err := strconv.Atoi(strings.TrimSpace(f[0]))
		if err != nil {
			continue
		}
		name := strings.Join(f[1:len(f)-fixedTail], ",")
		tail := f[len(f)-fixedTail:]
		driver := strings.TrimSpace(tail[7])
		if driver == "[N/A]" {
			driver = ""
		}
		cc := strings.TrimSpace(tail[8])
		if cc == "[N/A]" {
			cc = ""
		}
		devs = append(devs, core.GPUDevice{
			Vendor:     "nvidia",
			Index:      idx,
			Name:       strings.TrimSpace(name),
			MilliC:     int(flexF(tail[0]) * 1000),
			MemUsed:    uint64(flexF(tail[1])) << 20, // MiB
			MemTotal:   uint64(flexF(tail[2])) << 20,
			UtilPct:    flexF(tail[3]),
			PowerW:     flexF(tail[4]),
			FanRPM:     int(flexF(tail[5])),
			ClocksSM:   int(flexF(tail[6])),
			Driver:     driver,
			ComputeCap: cc,
		})
	}
	return devs
}

// flexF parses vendor-CSV numbers, tolerating "[N/A]" / "[Not Supported]".
// Non-positive results, including text like "nan" that ParseFloat accepts,
// collapse to zero so no sensor can inject NaN into temps, percentages or
// watts downstream.
func flexF(s string) float64 {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "[")
	s = strings.TrimSuffix(s, "]")
	v, _ := strconv.ParseFloat(s, 64)
	if !(v > 0) { // also catches NaN: every comparison with it is false
		return 0
	}
	return v
}

// ParseRocmSMI reads rocm-smi --json output keyed by "cardN". Values may be
// strings or single-element arrays depending on version. Exported so the
// remote ssh path can parse rocm-smi output gathered from another host.
func ParseRocmSMI(b []byte) []core.GPUDevice {
	var raw map[string]map[string]any
	if json.Unmarshal(b, &raw) != nil {
		return nil
	}
	var devs []core.GPUDevice
	for card, fields := range raw {
		idx := 0
		if _, rest, ok := strings.Cut(card, "card"); ok && len(strings.Fields(rest)) > 0 {
			idx, _ = strconv.Atoi(strings.Fields(rest)[0])
		}
		d := core.GPUDevice{Vendor: "amd", Index: idx}
		for k, v := range fields {
			lk, val := strings.ToLower(k), flatten(v)
			switch {
			case strings.Contains(lk, "temperature"):
				if strings.Contains(lk, "edge") || d.MilliC == 0 {
					d.MilliC = int(flexAny(val) * 1000)
				}
			case strings.Contains(lk, "used memory"):
				d.MemUsed = uint64(flexAny(val))
			case strings.Contains(lk, "total memory"):
				d.MemTotal = uint64(flexAny(val))
			case strings.Contains(lk, "gpu use"):
				d.UtilPct = flexAny(val)
			}
		}
		devs = append(devs, d)
	}
	return devs
}

type xpuDevice struct {
	ID   int
	Name string
}

// parseXpuDiscovery reads `xpu-smi discovery -j` output. Accepts either a
// bare device array or an object wrapping one.
func parseXpuDiscovery(b []byte) ([]xpuDevice, map[int]string) {
	type device struct {
		DeviceID   int    `json:"device_id"`
		DeviceName string `json:"device_name"`
	}
	var (
		bare []device
		wrap struct {
			Devices []device `json:"devices"`
		}
	)
	list := bare
	switch {
	case json.Unmarshal(b, &wrap) == nil && wrap.Devices != nil:
		list = wrap.Devices
	case json.Unmarshal(b, &bare) == nil:
		list = bare
	default:
		return nil, nil
	}
	names := map[int]string{}
	var order []xpuDevice
	for _, d := range list {
		names[d.DeviceID] = d.DeviceName
		order = append(order, xpuDevice{ID: d.DeviceID, Name: d.DeviceName})
	}
	return order, names
}

// parseXpuMetrics reads `xpu-smi metrics -d N -j`; values arrive as
// {"values":[x]} wrappers whose key set drifts between releases.
func parseXpuMetrics(b []byte, idx int) (core.GPUDevice, bool) {
	var raw struct {
		Metrics map[string]any `json:"metrics"`
	}
	if json.Unmarshal(b, &raw) != nil || raw.Metrics == nil {
		return core.GPUDevice{}, false
	}
	d := core.GPUDevice{Vendor: "intel", Index: idx}
	for k, v := range raw.Metrics {
		lk := strings.ToLower(k)
		val := flatten(v)
		switch {
		case strings.Contains(lk, "temperature"):
			d.MilliC = int(flexAny(val) * 1000)
		case lk == "gpu_utilization":
			d.UtilPct = flexAny(val)
		case strings.Contains(lk, "memory_used"):
			d.MemUsed = uint64(flexAny(val))
		case strings.Contains(lk, "memory_size"), strings.Contains(lk, "memory_total"):
			d.MemTotal = uint64(flexAny(val))
		case lk == "gpu_power":
			d.PowerW = flexAny(val)
		}
	}
	return d, true
}

// flatten unwraps {"values":[x]} / [x] shapes to a scalar any.
func flatten(v any) any {
	switch t := v.(type) {
	case []any:
		if len(t) > 0 {
			return flatten(t[0])
		}
	case map[string]any:
		if vals, ok := t["values"]; ok {
			return flatten(vals)
		}
	}
	return v
}

// flexAny coerces JSON scalars to float64, ignoring junk.
func flexAny(v any) float64 {
	switch t := v.(type) {
	case float64:
		return max(t, 0)
	case string:
		return flexF(t)
	}
	return 0
}
