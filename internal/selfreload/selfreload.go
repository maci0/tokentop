// Package selfreload watches the running executable and signals when it has
// been rebuilt, so `go build` + save gives an instantly fresh dashboard
// without losing your session.
package selfreload

import (
	"context"
	"os"
	"time"
)

type identity struct {
	dev, ino   uint64
	size       int64
	mtimeNanos int64
}

// Watch polls the executable's identity and calls onChange exactly once per
// rebuild. It never fires for the initial stat.
func Watch(ctx context.Context, exePath string, interval time.Duration, onChange func()) {
	var prev identity
	first := true
	for {
		if ctx.Err() != nil {
			return
		}
		id, err := statIdentity(exePath)
		if err == nil {
			if first {
				prev = id
				first = false
			} else if id != prev {
				onChange()
				return
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}

func statIdentity(path string) (identity, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return identity{}, err
	}
	id := identity{
		size:       fi.Size(),
		mtimeNanos: fi.ModTime().UnixNano(),
	}
	if sys, ok := fi.Sys().(syscallStat); ok {
		id.dev = statDev(sys)
		id.ino = statIno(sys)
	}
	return id, nil
}
