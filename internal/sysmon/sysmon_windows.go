//go:build windows

package sysmon

import (
	"os"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/maci0/toktop/internal/core"
)

var (
	kernel32                 = windows.NewLazySystemDLL("kernel32.dll")
	procGlobalMemoryStatusEx = kernel32.NewProc("GlobalMemoryStatusEx")
)

// memoryStatusEx mirrors MEMORYSTATUSEX (memoryapi.h).
type memoryStatusEx struct {
	Length               uint32
	MemoryLoad           uint32
	TotalPhys            uint64
	AvailPhys            uint64
	TotalPageFile        uint64
	AvailPageFile        uint64
	TotalVirtual         uint64
	AvailVirtual         uint64
	AvailExtendedVirtual uint64
}

func init() {
	platformMemory = sampleMemoryWindows
	// No loadavg; WMI thermal zones are rarely populated by vendors.
	platformCPUModel = func() string { return os.Getenv("PROCESSOR_IDENTIFIER") }
}

// sampleMemoryWindows uses GlobalMemoryStatusEx, which also reports
// commit-limit page-file accounting used here as swap.
func sampleMemoryWindows(s *core.SysSample) {
	var ms memoryStatusEx
	ms.Length = uint32(unsafe.Sizeof(ms))
	r1, _, _ := procGlobalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&ms)))
	if r1 == 0 {
		return
	}
	s.MemTotal = ms.TotalPhys
	s.MemUsed = satSub(ms.TotalPhys, ms.AvailPhys)
	s.SwapTotal = ms.TotalPageFile
	s.SwapUsed = satSub(ms.TotalPageFile, ms.AvailPageFile)
}
