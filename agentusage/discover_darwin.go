// Copyright (C) 2026 Marcel W. Wysocki
// SPDX-License-Identifier: MIT

//go:build darwin

package agentusage

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Discover asks ps(1) for the process table (there is no pure-Go BSD process
// list without cgo) and lsof(8) for each match's working directory, which
// macOS exposes no /proc-style symlink to readlink. Only processes that name
// a known agent pay for the second call.
func Discover() []Process {
	out, err := psTable()
	if err != nil {
		return nil
	}
	known := map[string]bool{}
	for _, a := range Agents() {
		known[a] = true
	}

	var found []Process
	for line := range strings.SplitSeq(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		pidField, rest, ok := strings.Cut(line, " ")
		pid, err := strconv.Atoi(strings.TrimSpace(pidField))
		if !ok || err != nil || pid <= 0 {
			continue
		}
		ucomm, cmd, _ := strings.Cut(strings.TrimLeft(rest, " "), " ")
		var argv []string
		if cmd != "" {
			argv = strings.Fields(cmd)
		}
		tool := agentName(ucomm, argv, known)
		if tool == "" {
			continue
		}
		dir := cwdOf(pid)
		if dir == "" {
			continue // exited, or another user's process
		}
		found = append(found, Process{PID: pid, Tool: tool, Dir: dir})
	}
	return found
}

// psTable returns "pid ucomm command…" for every process, one per line.
// The space-joined command loses quoting, which is acceptable here: matching
// scans whole path components of the first two words only.
func psTable() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ps", "-axo", "pid=,ucomm=,command=").Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// cwdOf reads a process's working directory out of lsof's machine-parseable
// output (-Fn prints one field per line; names carry an "n" prefix), or ""
// when it cannot be determined.
func cwdOf(pid int) string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "lsof", "-a", "-w", "-p", strconv.Itoa(pid),
		"-d", "cwd", "-Fn").Output()
	if err != nil {
		return ""
	}
	for line := range strings.SplitSeq(string(out), "\n") {
		if path, ok := strings.CutPrefix(line, "n"); ok && len(path) > 0 {
			return path
		}
	}
	return ""
}
