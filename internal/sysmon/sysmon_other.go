//go:build !linux && !darwin && !windows

package sysmon

import "tokentop/internal/core"

func init() {
	platformMemory = nil
	platformLoad = nil
	platformTemps = func() []core.TempReading { return nil }
	platformCPUModel = func() string { return "" }
}
