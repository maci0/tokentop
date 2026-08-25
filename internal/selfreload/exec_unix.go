//go:build !windows

package selfreload

import (
	"fmt"
	"os"
	"syscall"
)

// Restart replaces the current process with a fresh copy of itself, keeping
// arguments and environment. The terminal is restored by the caller before
// this is attempted. Either the process image is replaced, so this call
// never returns, or the reason is reported and toktop exits nonzero.
func Restart(selfPath string, argv, env []string) {
	if err := syscall.Exec(selfPath, argv, env); err != nil {
		fmt.Fprintln(os.Stderr, "toktop:", err)
		os.Exit(1)
	}
}
