//go:build !windows

package gpu

import (
	"context"
	"os/exec"
	"testing"
	"time"
)

// The context deadline kills the direct child, but Output also waits for the
// command's stdout to reach EOF. A grandchild inheriting that pipe (here: a
// backgrounded sleep) would hold it open for its whole lifetime and pin
// run(), plus whatever lock the caller holds, far past the deadline.
// WaitDelay must reclaim the pipes instead.
func TestRunReclaimsPipesHeldByGrandchild(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh unavailable")
	}
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	run(ctx, "sh", "-c", "sleep 5 & echo hello")
	if elapsed := time.Since(start); elapsed > 4*time.Second {
		t.Fatalf("run returned after %s: pipes held by the backgrounded child were not reclaimed at the deadline", elapsed)
	}
}
