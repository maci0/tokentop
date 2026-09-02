// Copyright (C) 2026 Marcel W. Wysocki
// SPDX-License-Identifier: MIT

//go:build !linux && !darwin && !windows

package agentusage

// Discover lists the agent CLIs running on this machine.
//
// An empty result means "cannot tell" rather than "nothing is running", which
// callers should surface as no local agents rather than an error. Platforms
// other than Linux, macOS, and Windows have no implementation: a process's
// working directory cannot be read here without native calls, and the
// directory is what attributes a transcript to a process (see Process.Dir).
func Discover() []Process { return nil }
