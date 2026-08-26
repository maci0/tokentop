// Copyright (C) 2026 Marcel W. Wysocki
// SPDX-License-Identifier: MIT

//go:build windows

package agentusage

// Discover reports nothing on Windows: Win32_Process (the documented
// interface this project uses elsewhere) exposes a process's command line but
// not its working directory, and the directory is what attributes a transcript
// to a process (see Process.Dir). Reading it would mean undocumented
// NtQueryInformationProcess PEB walks, so this is an explicit "cannot tell":
// callers show no local agents rather than agents attributed to nothing.
func Discover() []Process { return nil }
