//go:build windows

package datadirlock

import (
	"os"

	"golang.org/x/sys/windows"
)

func tryLock(f *os.File) error {
	// Lock the first byte of the file exclusively, non-blocking.
	var ol windows.Overlapped
	err := windows.LockFileEx(
		windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0,
		1,
		0,
		&ol,
	)
	if err == nil {
		return nil
	}
	if err == windows.ERROR_LOCK_VIOLATION || err == windows.ERROR_IO_PENDING {
		return ErrLocked
	}
	return err
}

func unlockAndClose(f *os.File) error {
	var ol windows.Overlapped
	_ = windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, &ol)
	return f.Close()
}
