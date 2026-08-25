//go:build windows

package selfreload

import (
	"fmt"
	"os"
)

// Restart cannot swap the process image on Windows the way exec(2) allows,
// so it reports the manual step instead and lets the caller exit normally.
func Restart(string, []string, []string) {
	fmt.Fprintln(os.Stderr, "toktop: hot-reload unsupported on windows; restart toktop")
}
