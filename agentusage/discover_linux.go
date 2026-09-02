// Copyright (C) 2026 Marcel W. Wysocki
// SPDX-License-Identifier: MIT

//go:build linux

package agentusage

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Discover lists the agent CLIs running on this machine.
//
// An empty result means "cannot tell" rather than "nothing is running", which
// callers should surface as no local agents rather than an error. On Linux this
// is a /proc walk: three reads per process, no subprocess, and every other
// user's processes simply fail the permission check.
func Discover() []Process {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	known := map[string]bool{}
	for _, a := range Agents() {
		known[a] = true
	}

	var out []Process
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		dir := filepath.Join("/proc", strconv.Itoa(pid))

		var comm string
		if name, err := os.ReadFile(filepath.Join(dir, "comm")); err == nil {
			comm = strings.TrimSpace(string(name))
		}

		raw, err := os.ReadFile(filepath.Join(dir, "cmdline"))
		if err != nil || len(raw) == 0 {
			continue
		}
		args := strings.Split(strings.TrimRight(string(raw), "\x00"), "\x00")
		tool := agentName(comm, args, known)
		if tool == "" {
			continue
		}
		cwd, err := os.Readlink(filepath.Join(dir, "cwd"))
		if err != nil {
			continue // exited, or another user's process
		}
		out = append(out, Process{PID: pid, Tool: tool, Dir: cwd, Started: startedAt(pid)})
	}
	return out
}

// startedAt reads a process's start time, falling back to the zero time when
// it cannot be determined.
func startedAt(pid int) time.Time {
	fi, err := os.Stat(filepath.Join("/proc", strconv.Itoa(pid)))
	if err != nil {
		return time.Time{}
	}
	return fi.ModTime()
}
