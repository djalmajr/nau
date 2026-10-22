//go:build windows

package config

import (
	"os"

	"golang.org/x/sys/windows"
)

func tryConfigLock(f *os.File) (bool, error) {
	err := windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, ^uint32(0), ^uint32(0), &windows.Overlapped{})
	if err == windows.ERROR_LOCK_VIOLATION || err == windows.ERROR_IO_PENDING {
		return false, nil
	}
	return err == nil, err
}

func releaseConfigLock(f *os.File) {
	_ = windows.UnlockFileEx(windows.Handle(f.Fd()), 0, ^uint32(0), ^uint32(0), &windows.Overlapped{})
}
