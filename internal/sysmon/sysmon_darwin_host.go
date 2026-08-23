//go:build darwin

package sysmon

import (
	"encoding/binary"

	"strings"
	"time"

	"golang.org/x/sys/unix"

	"tokentop/internal/core"
)

func init() {
	platformHost = hostInfoDarwin
}

// hostInfoDarwin uses only sysctls: product version, kernel release and boot
// time. No powermetrics/root required.
func hostInfoDarwin(s *core.SysSample) {
	if v, err := unix.Sysctl("kern.osproductversion"); err == nil && v != "" {
		s.OsName = "macOS " + v
	}
	var un unix.Utsname
	if err := unix.Uname(&un); err == nil {
		s.Kernel = utsFieldByte(un.Release[:])
	}
	s.HostUptime = boottimeUptime()
}

func boottimeUptime() time.Duration {
	b, err := unix.SysctlRaw("kern.boottime")
	if err != nil || len(b) < 8 {
		return 0
	}
	sec := int64(binary.LittleEndian.Uint64(b[:8]))
	if sec <= 0 {
		return 0
	}
	return time.Since(time.Unix(sec, 0))
}

// utsFieldByte converts a NUL-padded Utsname array to a string.
func utsFieldByte(b []byte) string {
	for i, c := range b {
		if c == 0 {
			return string(b[:i])
		}
	}
	return strings.TrimSpace(string(b))
}
