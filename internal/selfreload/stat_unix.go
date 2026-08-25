//go:build !windows

package selfreload

import "syscall"

type syscallStat = syscall.Stat_t

func statDev(s *syscallStat) uint64 { return uint64(s.Dev) }
func statIno(s *syscallStat) uint64 { return uint64(s.Ino) }
