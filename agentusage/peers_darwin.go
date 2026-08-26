// Copyright (C) 2026 Marcel W. Wysocki
// SPDX-License-Identifier: MIT

//go:build darwin

package agentusage

import (
	"context"
	"net/netip"
	"os/exec"
	"strconv"
	"time"
)

// Peers asks lsof(8) for the process's internet sockets; macOS exposes no
// procfs-equivalent connection table to unprivileged readers. -n and -P keep
// every field numeric so parsing never depends on resolver or services
// databases, and parseLsofPeers does the rest.
func Peers(pid int) []netip.AddrPort {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "lsof", "-a", "-w", "-p", strconv.Itoa(pid),
		"-i", "-FnP", "-n").Output()
	if err != nil {
		return nil // exited, or another user's process
	}
	return parseLsofPeers(string(out))
}
