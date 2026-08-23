//go:build linux

package procs

import (
	"os"
	"strconv"
	"strings"
)

func init() {
	platformList = listLinux
	// USER_HZ is fixed at 100 by the Linux ABI; there is no runtime probe.
	clkTck = func() float64 { return 100 }
	osGetpid = os.Getpid
}

// listLinux walks /proc: everything from plain files, zero subprocesses.
func listLinux() ([]raw, error) {
	entries, err := os.ReadDir(procRoot)
	if err != nil {
		return nil, err
	}
	var out []raw
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid <= 0 {
			continue // not a process dir
		}
		base := procPath(e.Name())
		cmdlineB, err := os.ReadFile(base + "/cmdline")
		if err != nil {
			continue // vanished or kernel thread
		}
		args := strings.Split(strings.TrimRight(string(cmdlineB), "\x00"), "\x00")
		if len(args) == 0 || args[0] == "" {
			continue
		}

		r := raw{pid: pid, name: baseName(args[0]), args: args}

		if stat, err := os.ReadFile(base + "/stat"); err == nil {
			r.ticks = procStatTicks(string(stat))
		}
		if status, err := os.ReadFile(base + "/status"); err == nil {
			r.rss = vmRSS(string(status))
		}
		out = append(out, r)
	}
	return out, nil
}

// procStatTicks sums utime+stime (fields 14,15) respecting comm parens.
func procStatTicks(stat string) uint64 {
	open := strings.LastIndexByte(stat, '(')
	closeP := strings.LastIndexByte(stat, ')')
	if open < 0 || closeP < 0 || closeP+2 > len(stat) {
		return 0
	}
	fields := strings.Fields(stat[closeP+2:]) // field 3 onward
	const utimeIdx = 11                       // field 14 overall -> idx 11 here
	if len(fields) <= utimeIdx+1 {
		return 0
	}
	u, _ := strconv.ParseUint(fields[utimeIdx], 10, 64)
	si, _ := strconv.ParseUint(fields[utimeIdx+1], 10, 64)
	return u + si
}

// vmRSS pulls "VmRSS:\t 1234 kB" out of /proc/PID/status.
func vmRSS(status string) uint64 {
	for _, line := range strings.Split(status, "\n") {
		if k, v, ok := strings.Cut(line, ":"); ok && strings.TrimSpace(k) == "VmRSS" {
			f := strings.Fields(v)
			if len(f) >= 2 && f[1] == "kB" {
				kb, err := strconv.ParseUint(f[0], 10, 64)
				if err == nil {
					return kb << 10
				}
			}
			return 0
		}
	}
	return 0
}
