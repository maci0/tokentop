// Copyright (C) 2026 Marcel W. Wysocki
// SPDX-License-Identifier: MIT

package agentusage

import (
	"path/filepath"
	"strings"
	"time"
)

// Process is one running agent CLI.
type Process struct {
	// PID is the OS process identifier.
	PID int
	// Tool is the agent name (claude, codex, pi, …).
	Tool string
	// Dir is the process's working directory, which is what attributes a
	// transcript to it.
	Dir string
	// Started is when the process began, as far as the OS reports it; the
	// zero time when the platform does not report it.
	Started time.Time
}

// Watch starts reading usage for this process, using its agent name and
// working directory. Equivalent to [Watch](p.Tool, p.Dir, since).
func (p Process) Watch(since time.Time) *Watcher {
	return Watch(p.Tool, p.Dir, since)
}

// agentName names the known agent a process is running, or "" when it is not
// one.
//
// The executable name alone is not enough: agents ship as node and bun scripts,
// so the process is called "node" and the agent name is in the command line.
// Both the kernel's short name (comm / ucomm) and the first two command-line
// words are checked, and only whole path components count, so a shell that
// merely mentions an agent in a later argument is not mistaken for one.
func agentName(comm string, argv []string, known map[string]bool) string {
	if t := strings.TrimSpace(comm); known[t] {
		return t
	}
	for i, a := range argv {
		if i > 1 || a == "" {
			break
		}
		if t := filepath.Base(a); known[t] {
			return t
		}
	}
	return ""
}
