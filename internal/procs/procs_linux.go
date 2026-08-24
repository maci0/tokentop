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

		// One stat read yields both CPU ticks and RSS; a second read of
		// status would double the per-PID syscalls on every poll.
		if stat, err := os.ReadFile(base + "/stat"); err == nil {
			r.ticks, r.rss = procStatCPUAndRSS(string(stat))
		}
		out = append(out, r)
	}
	return out, nil
}

// procStatCPUAndRSS sums utime+stime jiffies (fields 14,15) and reads the
// resident set in pages (field 24) from one /proc/PID/stat body, respecting
// the comm parens (a comm may contain spaces).
func procStatCPUAndRSS(stat string) (ticks uint64, rssBytes uint64) {
	open := strings.LastIndexByte(stat, '(')
	closeP := strings.LastIndexByte(stat, ')')
	if open < 0 || closeP < 0 || closeP+2 > len(stat) {
		return 0, 0
	}
	fields := strings.Fields(stat[closeP+2:]) // field 3 onward
	const (
		utimeIdx = 11 // field 14 overall -> idx 11 here
		stimeIdx = 12 // field 15 overall -> idx 12 here
		rssIdx   = 21 // field 24 overall (rss, pages) -> idx 21 here
	)
	if len(fields) > stimeIdx {
		u, _ := strconv.ParseUint(fields[utimeIdx], 10, 64)
		si, _ := strconv.ParseUint(fields[stimeIdx], 10, 64)
		ticks = u + si
	}
	if len(fields) > rssIdx {
		pages, _ := strconv.ParseUint(fields[rssIdx], 10, 64)
		rssBytes = pages * uint64(os.Getpagesize())
	}
	return ticks, rssBytes
}
