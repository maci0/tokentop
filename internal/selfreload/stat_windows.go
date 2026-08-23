//go:build windows

package selfreload

type syscallStat struct{}

func statDev(syscallStat) uint64 { return 0 }
func statIno(syscallStat) uint64 { return 0 }
