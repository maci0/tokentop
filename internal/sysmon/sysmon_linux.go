//go:build linux

package sysmon

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"

	"tokentop/internal/core"
)

const (
	procMeminfo = "/proc/meminfo"
	procLoadavg = "/proc/loadavg"
	sysHwmon    = "/sys/class/hwmon"
	sysThermal  = "/sys/class/thermal"
)

func init() {
	platformMemory = sampleMemoryLinux
	platformLoad = func(s *core.SysSample) {
		if b, err := os.ReadFile(procLoadavg); err == nil {
			s.Load1, s.Load5, s.Load15 = ParseLoadavg(string(b))
		}
	}
	platformTemps = func() []core.TempReading { return ScanTemps(sysHwmon, sysThermal) }
	platformCPUModel = cpuModelOnce // /proc/cpuinfo never changes: resolve once
	platformHost = hostInfoLinux
}

// cpuModelOnce memoizes the brand string; /proc/cpuinfo can be hundreds of
// kilobytes on many-core hosts and re-parsing it every poll is pure waste.
var cpuModelOnce = sync.OnceValue(cpuModelLinux)

func sampleMemoryLinux(s *core.SysSample) {
	if b, err := os.ReadFile(procMeminfo); err == nil {
		ParseMeminfo(b, s)
	}
}

// hostStaticInfo gathers everything that cannot change while the process
// runs: distro name, kernel release, driver versions and the NPU inventory.
// Without the cache each poll would re-read five files and re-walk the
// accel class for identical answers; all of it is resolved once and copied
// into each fresh sample.
type hostStatic struct {
	osName    string
	kernel    string
	nvidiaDrv string
	cuda      string
	amdgpu    string
	npus      []string
}

var hostStaticInfo = sync.OnceValue(func() hostStatic {
	var h hostStatic
	h.osName = prettyOSName()
	var un unix.Utsname
	if err := unix.Uname(&un); err == nil {
		h.kernel = strings.TrimSpace(utsField(un.Release[:]))
		if h.osName == "" {
			h.osName = strings.TrimSpace(utsField(un.Sysname[:]))
		}
	}
	if b, err := os.ReadFile("/proc/driver/nvidia/version"); err == nil {
		h.nvidiaDrv, h.cuda = parseNvidiaVersion(string(b))
	}
	h.amdgpu = sysModuleVersion("amdgpu")
	h.npus = scanAccelDrivers("/sys/class/accel")
	return h
})

func hostInfoLinux(s *core.SysSample) {
	h := hostStaticInfo()
	s.OsName = h.osName
	s.Kernel = h.kernel
	s.HostUptime = linuxUptime()
	if s.Drivers == nil { // Sample initializes late; never write a nil map
		s.Drivers = map[string]string{}
	}
	if h.nvidiaDrv != "" {
		s.Drivers["nvidia"] = h.nvidiaDrv
	}
	if h.cuda != "" {
		s.Drivers["cuda"] = h.cuda
	}
	if h.amdgpu != "" {
		s.Drivers["amdgpu"] = h.amdgpu
	}
	s.NPUs = h.npus
}

func prettyOSName() string {
	b, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		if k, v, ok := strings.Cut(line, "="); ok && k == "PRETTY_NAME" {
			return strings.Trim(v, `"`)
		}
	}
	return ""
}

func linuxUptime() time.Duration {
	b, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0
	}
	f := strings.Fields(string(b))
	if len(f) == 0 {
		return 0
	}
	secs, err := strconv.ParseFloat(f[0], 64)
	if err != nil {
		return 0
	}
	return time.Duration(secs * float64(time.Second))
}

// parseNvidiaVersion extracts "Driver Version: 550.54.14" and the trailing
// "CUDA Version: 12.4" from /proc/driver/nvidia/version.
func parseNvidiaVersion(text string) (driver, cuda string) {
	for _, line := range strings.Split(text, "\n") {
		if i := strings.Index(line, "Driver Version:"); i >= 0 {
			fields := strings.Fields(line[i+len("Driver Version:"):])
			if len(fields) > 0 {
				driver = fields[0]
			}
			if j := strings.Index(line, "CUDA Version:"); j >= 0 {
				cfields := strings.Fields(line[j+len("CUDA Version:"):])
				if len(cfields) > 0 {
					cuda = cfields[0]
				}
			}
			return driver, cuda
		}
	}
	return "", ""
}

func sysModuleVersion(module string) string {
	b, err := os.ReadFile(filepath.Join("/sys/module", module, "version"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// utsField converts a NUL-padded Utsname char array to a string.
func utsField(b []byte) string {
	out := make([]byte, 0, len(b))
	for _, c := range b {
		if c == 0 {
			break
		}
		out = append(out, byte(c))
	}
	return string(out)
}

// scanAccelDrivers enumerates the accelerator class (/sys/class/accel):
// NPUs like Intel's NPU, AMD XDNA and Qualcomm Cloud AI100. Same-driver
// devices collapse into one entry with a count suffix.
func scanAccelDrivers(root string) []string {
	entries, err := filepath.Glob(filepath.Join(root, "accel*"))
	if err != nil {
		return nil
	}
	counts := map[string]int{}
	var order []string
	for _, e := range entries {
		link, err := os.Readlink(filepath.Join(e, "device", "driver"))
		if err != nil {
			continue
		}
		name := npuDisplayName(filepath.Base(link))
		if counts[name] == 0 {
			order = append(order, name)
		}
		counts[name]++
	}
	out := make([]string, 0, len(order))
	for _, name := range order {
		if counts[name] > 1 {
			name += fmt.Sprintf(" x%d", counts[name])
		}
		out = append(out, name)
	}
	return out
}

// npuDisplayName maps kernel driver names to human-friendly accelerators.
func npuDisplayName(driver string) string {
	switch driver {
	case "intel_vpu", "ivpu":
		return "Intel NPU"
	case "amdxdna":
		return "AMD XDNA NPU"
	case "qaic":
		return "Qualcomm Cloud AI100"
	default:
		return driver
	}
}

func cpuModelLinux() string {
	b, err := os.ReadFile("/proc/cpuinfo")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		if k, v, ok := strings.Cut(line, ":"); ok && strings.HasPrefix(strings.TrimSpace(k), "model name") {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

var gpuChips = []string{"amdgpu", "radeon", "nouveau", "nvidia", "i915", "xe"}

// ScanTemps gathers readings, preferring hwmon chips and falling back to
// thermal zones only when hwmon yields nothing (they often duplicate).
func ScanTemps(hwmonRoot, thermalRoot string) []core.TempReading {
	temps := scanHwmon(hwmonRoot)
	if len(temps) == 0 {
		temps = scanThermalZones(thermalRoot)
	}
	sort.SliceStable(temps, func(i, j int) bool {
		if temps[i].IsGPU != temps[j].IsGPU {
			return temps[i].IsGPU
		}
		return temps[i].MilliC > temps[j].MilliC
	})
	if len(temps) > 16 { // keep the frame cheap on sensor-farm machines
		temps = temps[:16]
	}
	return temps
}

func scanHwmon(root string) []core.TempReading {
	chips, err := filepath.Glob(filepath.Join(root, "hwmon*"))
	if err != nil {
		return nil
	}
	var out []core.TempReading
	for _, chip := range chips {
		nameB, err := os.ReadFile(filepath.Join(chip, "name"))
		chipName := strings.ToLower(strings.TrimSpace(string(nameB)))
		if err != nil {
			continue
		}
		isGPUChip := containsAny(chipName, gpuChips...)
		inputs, _ := filepath.Glob(filepath.Join(chip, "temp*_input"))
		for _, in := range inputs {
			mc, ok := readMilliC(in)
			if !ok {
				continue
			}
			label := chipName
			base := strings.TrimSuffix(filepath.Base(in), "_input")
			if lb, err := os.ReadFile(filepath.Join(chip, base+"_label")); err == nil {
				label = strings.ToLower(strings.TrimSpace(string(lb)))
			}
			gpu := isGPUChip || containsAny(label, "gpu", "junction", "hotspot", "edge")
			out = append(out, core.TempReading{Label: label, MilliC: mc, IsGPU: gpu})
		}
	}
	return out
}

func scanThermalZones(root string) []core.TempReading {
	zones, err := filepath.Glob(filepath.Join(root, "thermal_zone*"))
	if err != nil {
		return nil
	}
	var out []core.TempReading
	for _, z := range zones {
		tb, err := os.ReadFile(filepath.Join(z, "type"))
		if err != nil {
			continue
		}
		mc, ok := readMilliC(filepath.Join(z, "temp"))
		if !ok {
			continue
		}
		typ := strings.ToLower(strings.TrimSpace(string(tb)))
		out = append(out, core.TempReading{
			Label:  typ,
			MilliC: mc,
			IsGPU:  containsAny(typ, "gpu"),
		})
	}
	return out
}

func readMilliC(path string) (int, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	v, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		return 0, false
	}
	return v, true
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
