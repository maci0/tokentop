//go:build windows

package selfreload

import "fmt"

// ReExec is not possible on Windows without spawning a child; the caller
// prints a hint instead.
func ReExec(string, []string, []string) error {
	return fmt.Errorf("hot-reload unsupported on windows; restart toktop")
}
