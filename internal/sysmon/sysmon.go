// Package sysmon samples host vitals (RAM, swap, load, temperatures, CPU
// model) using each platform's native interfaces. Platform implementations
// live in sysmon_<goos>.go files selected by build tags.
package sysmon

import (
	"context"
	"strconv"
	"strings"
	"time"

	"tokentop/internal/core"
	"tokentop/internal/gpu"
)

// gpuBudget bounds the whole vendor-tool sweep per poll cycle.
const gpuBudget = 3 * time.Second

// Hooks implemented by each platform file.
var (
	platformMemory   func(*core.SysSample)
	platformLoad     func(*core.SysSample)
	platformTemps    func() []core.TempReading
	platformCPUModel func() string
	platformHost     func(*core.SysSample) // os name, kernel, uptime, drivers
)

// Sample collects a best-effort snapshot of host vitals; missing sources are
// simply absent from the result. Never returns nil.
func Sample() core.SysSample {
	var s core.SysSample
	if platformMemory != nil {
		platformMemory(&s)
	}
	if platformLoad != nil {
		platformLoad(&s)
	}
	if platformTemps != nil {
		s.Temps = platformTemps()
	}
	if platformHost != nil {
		platformHost(&s)
	}
	ctx, cancel := context.WithTimeout(context.Background(), gpuBudget)
	defer cancel()
	s.GPUs = gpu.Sample(ctx)
	if s.Drivers == nil {
		s.Drivers = map[string]string{}
	}
	for _, g := range s.GPUs { // vendor tools often know their own driver
		if g.Vendor == "nvidia" && g.Driver != "" && s.Drivers["nvidia"] == "" {
			s.Drivers["nvidia"] = g.Driver
		}
	}
	if platformCPUModel != nil {
		s.CPUModel = platformCPUModel()
	}
	return s
}

// ParseMeminfo fills memory fields from the Linux /proc/meminfo format.
func ParseMeminfo(b []byte, s *core.SysSample) {
	vals := map[string]uint64{}
	for _, line := range strings.Split(string(b), "\n") {
		k, v, ok := cutMeminfoLine(line)
		if ok {
			vals[k] = v
		}
	}
	total := vals["MemTotal"]
	s.MemTotal = total << 10 // meminfo reports KiB
	s.MemUsed = (total - vals["MemAvailable"]) << 10
	s.SwapTotal = vals["SwapTotal"] << 10
	s.SwapUsed = (vals["SwapTotal"] - vals["SwapFree"]) << 10
}

func cutMeminfoLine(line string) (string, uint64, bool) {
	k, rest, ok := strings.Cut(line, ":")
	if !ok {
		return "", 0, false
	}
	f := strings.Fields(rest)
	if len(f) == 0 {
		return "", 0, false
	}
	v, err := strconv.ParseUint(f[0], 10, 64)
	if err != nil {
		return "", 0, false
	}
	return strings.TrimSpace(k), v, true
}

// ParseLoadavg reads "1.5 0.7 0.3 extra..." into three load averages.
func ParseLoadavg(s string) (l1, l5, l15 float64) {
	f := strings.Fields(s)
	at := func(i int) float64 {
		if i >= len(f) {
			return 0
		}
		v, _ := strconv.ParseFloat(f[i], 64)
		return v
	}
	return at(0), at(1), at(2)
}
