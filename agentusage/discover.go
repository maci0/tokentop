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

// Discover lists the agent CLIs running on this machine, so a monitor can show
// what is generating right now without being told. It has one implementation
// per platform: discover_linux.go walks /proc, discover_darwin.go asks ps(1)
// and lsof(8), and platforms where a process's working directory cannot be
// read without native calls report nothing at all.
//
// An empty result means "cannot tell" rather than "nothing is running", which
// callers should surface as no local agents rather than an error.
//
// None of these implementations shell out to pgrep-style helpers to find the
// processes themselves: spawning a process to count processes is how a monitor
// ends up measuring itself.

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
