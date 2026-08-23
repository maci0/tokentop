//go:build darwin

package sysmon

import (
	"encoding/binary"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"

	"tokentop/internal/core"
)

func init() {
	platformMemory = sampleMemoryDarwin
	platformLoad = sampleLoadDarwin
	platformTemps = func() []core.TempReading { return nil } // needs root powermetrics
	platformCPUModel = func() string { s, _ := unix.Sysctl("machdep.cpu.brand_string"); return s }
}

// sampleMemoryDarwin derives RAM usage from vm.page_* sysctls. This is the
// same accounting Activity Monitor uses: wired + compressed + app (active +
// inactive) memory, excluding free, speculative and purgeable pages.
func sampleMemoryDarwin(s *core.SysSample) {
	total, err := unix.SysctlUint64("hw.memsize")
	if err != nil {
		return
	}
	ps := uint64(0)
	if v, err := unix.SysctlUint32("hw.pagesize"); err == nil {
		ps = uint64(v)
	}
	if ps == 0 {
		ps = 4096
	}
	page := func(name string) uint64 {
		v, err := unix.SysctlUint32(name)
		if err != nil {
			return 0
		}
		return uint64(v)
	}
	s.MemTotal = total
	s.MemUsed = (page("vm.pages_wired") + page("vm.pages_active") +
		page("vm.pages_inactive") + page("vm.pages_compressed")) * ps

	if tu, uu, ok := swapUsage(); ok {
		s.SwapTotal, s.SwapUsed = tu, uu
	}
}

func sampleLoadDarwin(s *core.SysSample) {
	if b, err := unix.SysctlRaw("kern.loadavg"); err == nil {
		s.Load1, s.Load5, s.Load15 = decodeLoadavg(b)
	}
}

// decodeLoadavg parses kern.loadavg: struct loadavg { int32 ldavg[3]; int32 scale }.
func decodeLoadavg(b []byte) (l1, l5, l15 float64) {
	if len(b) < 16 {
		return 0, 0, 0
	}
	scale := binary.LittleEndian.Uint32(b[12:16])
	if scale == 0 {
		return 0, 0, 0
	}
	get := func(off int) float64 {
		return float64(int32(binary.LittleEndian.Uint32(b[off:off+4]))) / float64(scale)
	}
	return get(0), get(4), get(8)
}

// swapUsage reads macOS's vm.swapusage string.
func swapUsage() (total, used uint64, ok bool) {
	raw, err := unix.Sysctl("vm.swapusage")
	if err != nil {
		return 0, 0, false
	}
	total, used = parseSwapUsage(raw)
	return total, used, total > 0
}

// parseSwapUsage decodes a vm.swapusage string into bytes:
// "total = 2048.00M used = 512.00M free = 1536.00M".
func parseSwapUsage(s string) (total, used uint64) {
	last := ""
	for _, tok := range strings.Fields(strings.ReplaceAll(s, "=", " ")) {
		switch strings.ToLower(tok) {
		case "total":
			last = "total"
			continue
		case "used":
			last = "used"
			continue
		case "free":
			last = ""
			continue
		}
		v, _ := splitSizeToken(tok)
		if v == 0 {
			continue
		}
		switch last {
		case "total":
			total = v
		case "used":
			used = v
		}
	}
	return total, used
}

// splitSizeToken splits "512.00M" into bytes.
func splitSizeToken(tok string) (uint64, string) {
	if len(tok) < 2 {
		return 0, ""
	}
	unit := strings.ToUpper(string(tok[len(tok)-1]))
	num, err := strconv.ParseFloat(tok[:len(tok)-1], 64)
	if err != nil {
		return 0, unit
	}
	switch unit {
	case "G":
		return uint64(num * 1024 * 1024 * 1024), unit
	case "M":
		return uint64(num * 1024 * 1024), unit
	case "K":
		return uint64(num * 1024), unit
	default:
		return 0, unit
	}
}
