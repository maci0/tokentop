//go:build !windows

package selfreload

import "syscall"

// ReExec replaces the current process with a fresh copy of itself, keeping
// arguments and environment. The terminal is restored by the caller before
// this is attempted.
func ReExec(selfPath string, argv, env []string) error {
	return syscall.Exec(selfPath, argv, env)
}
