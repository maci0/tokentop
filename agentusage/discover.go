// Copyright (C) 2026 Marcel W. Wysocki
// SPDX-License-Identifier: AGPL-3.0-or-later

package agentusage

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Process is one running agent CLI.
type Process struct {
	PID int
	// Tool is the agent name (claude, codex, pi, …).
	Tool string
	// Dir is the process's working directory, which is what attributes a
	// transcript to it.
	Dir string
	// Started is when the process began, as far as the OS reports it.
	Started time.Time
}

// Discover lists the agent CLIs running on this machine, so a monitor can show
// what is generating right now without being told.
//
// It walks /proc rather than shelling out to pgrep: the answer is three reads
// per process, and spawning a process to count processes is how a monitor ends
// up measuring itself. On systems without /proc it returns nothing, which
// callers should treat as "cannot tell" rather than "nothing is running".
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
		tool := toolOf(pid, known)
		if tool == "" {
			continue
		}
		dir, err := os.Readlink(filepath.Join("/proc", e.Name(), "cwd"))
		if err != nil {
			continue // exited, or another user's process
		}
		out = append(out, Process{PID: pid, Tool: tool, Dir: dir, Started: startedAt(pid)})
	}
	return out
}

// toolOf names the agent a process is running, or "" when it is not one.
//
// The executable name alone is not enough: agents ship as node and bun scripts,
// so the process is called "node" and the agent name is in the command line.
// Both are checked, and only whole path components count, so a shell that
// merely mentions an agent in an argument is not mistaken for one.
func toolOf(pid int, known map[string]bool) string {
	dir := filepath.Join("/proc", strconv.Itoa(pid))

	if name, err := os.ReadFile(filepath.Join(dir, "comm")); err == nil {
		if t := strings.TrimSpace(string(name)); known[t] {
			return t
		}
	}

	raw, err := os.ReadFile(filepath.Join(dir, "cmdline"))
	if err != nil || len(raw) == 0 {
		return ""
	}
	args := strings.Split(strings.TrimRight(string(raw), "\x00"), "\x00")
	// Only the program and its immediate subject are considered: `node
	// /path/to/claude ...` names the agent in argv[1], while a later argument
	// is just text that happens to contain the word.
	for i, a := range args {
		if i > 1 || a == "" {
			break
		}
		if t := filepath.Base(a); known[t] {
			return t
		}
	}
	return ""
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
