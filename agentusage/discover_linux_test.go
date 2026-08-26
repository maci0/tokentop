// Copyright (C) 2026 Marcel W. Wysocki
// SPDX-License-Identifier: MIT

//go:build linux

package agentusage

import (
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// Discover must find a real process whose argv names an agent even when the
// kernel calls it something else (the node-script case), and attribute it to
// its working directory.
func TestDiscoverFindsProcessByArgvName(t *testing.T) {
	dir := t.TempDir()
	cmd := exec.Command("sleep", "30")
	cmd.Dir = dir
	cmd.Args[0] = "claude" // /proc/PID/cmdline then reads "claude\030"
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot spawn sleep: %v", err)
	}
	defer func() {
		if cmd.Process != nil {
			cmd.Process.Kill()
			cmd.Wait()
		}
	}()

	var found *Process
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, p := range Discover() {
			if p.PID == cmd.Process.Pid {
				found = &p
			}
		}
		if found != nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if found == nil {
		t.Fatal("Discover did not report a process renamed to claude")
	}
	if found.Tool != "claude" {
		t.Errorf("Tool = %q, want claude", found.Tool)
	}
	if found.Dir == "" {
		t.Fatal("Dir is empty; attribution would be impossible")
	}
	if resolved, err := filepath.EvalSymlinks(dir); err == nil && resolved != found.Dir {
		t.Errorf("Dir = %q, want %q", found.Dir, resolved)
	}
}
