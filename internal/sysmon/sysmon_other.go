//go:build !linux && !darwin && !windows

package sysmon

import "github.com/maci0/toktop/internal/core"

func init() {
	platformMemory = nil
	platformLoad = nil
	platformTemps = func() []core.TempReading { return nil }
	platformCPUModel = func() string { return "" }
}
