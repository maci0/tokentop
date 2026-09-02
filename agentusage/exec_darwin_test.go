// Copyright (C) 2026 Marcel W. Wysocki
// SPDX-License-Identifier: MIT

//go:build darwin

package agentusage

import (
	"context"
	"os/exec"
	"testing"
	"time"
)

// The context deadline kills the direct child, but Output also waits for
// stdout to reach EOF. A grandchild inheriting that pipe would hold it open
// for its whole lifetime and pin Discover / Peers far past the deadline.
func TestCommandOutputReclaimsPipesHeldByGrandchild(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh unavailable")
	}
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_, _ = commandOutput(ctx, "sh", "-c", "sleep 5 & echo hello")
	if elapsed := time.Since(start); elapsed > 4*time.Second {
		t.Fatalf("commandOutput returned after %s: pipes held by the backgrounded child were not reclaimed at the deadline", elapsed)
	}
}
