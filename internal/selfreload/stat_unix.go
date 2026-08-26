//go:build !windows

package selfreload

import (
	"os"
	"syscall"
)

// fileID reads the device and inode already sitting in the stat data, so no
// second syscall is needed. A file system that reports neither (some FUSE
// mounts) leaves both zero, and size and mtime carry the identity alone.
func fileID(_ string, fi os.FileInfo) (dev, ino uint64) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0
	}
	return uint64(st.Dev), uint64(st.Ino)
}
