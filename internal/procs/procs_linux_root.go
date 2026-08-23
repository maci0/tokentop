//go:build linux

package procs

import "path/filepath"

// procRoot is injectable for tests.
var procRoot = "/proc"

func swapProcRoot(newRoot string) func() {
	old := procRoot
	procRoot = newRoot
	return func() { procRoot = old }
}

func procPath(parts ...string) string {
	return filepath.Join(append([]string{procRoot}, parts...)...)
}
