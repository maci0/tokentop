//go:build windows

package selfreload

import (
	"os"
	"syscall"
)

// Windows carries no inode in what os.Stat returns, so identity comes from
// the volume serial number and the file index, the pair NTFS names a file by.
// Both need an open handle, which is what GetFileInformationByHandle takes;
// there is no in-process alternative that reads them from a path.
func fileID(path string, _ os.FileInfo) (dev, ino uint64) {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return 0, 0
	}
	// No access bits are requested: the handle exists to be asked about the
	// file, and asking for read access would fail on an image being executed.
	h, err := syscall.CreateFile(p, 0,
		syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE,
		nil, syscall.OPEN_EXISTING, syscall.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return 0, 0
	}
	defer syscall.CloseHandle(h)
	var info syscall.ByHandleFileInformation
	if err := syscall.GetFileInformationByHandle(h, &info); err != nil {
		return 0, 0
	}
	return uint64(info.VolumeSerialNumber), uint64(info.FileIndexHigh)<<32 | uint64(info.FileIndexLow)
}
