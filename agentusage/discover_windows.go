// Copyright (C) 2026 Marcel W. Wysocki
// SPDX-License-Identifier: MIT

//go:build windows

package agentusage

// Discover lists the agent CLIs running on this machine.
//
// An empty result means "cannot tell" rather than "nothing is running", which
// callers should surface as no local agents rather than an error. Windows
// reports nothing: Win32_Process exposes a command line but not a working
// directory, and the directory is what attributes a transcript to a process
// (see Process.Dir). Reading it would mean undocumented
// NtQueryInformationProcess PEB walks.
func Discover() []Process { return nil }
