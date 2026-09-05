//go:build windows

package conpty

import (
	"errors"
	"fmt"

	"golang.org/x/sys/windows"
)

// probePIDLiveness distinguishes conclusive process exit from an operational
// Windows probe failure. Access denied proves that a process exists; invalid
// parameter is how OpenProcess reports a PID that no longer exists.
func probePIDLiveness(pid int) (bool, error) {
	if pid <= 0 {
		return false, nil
	}
	handle, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
			return true, nil
		}
		if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
			return false, nil
		}
		return false, err
	}
	defer func() { _ = windows.CloseHandle(handle) }()

	status, err := windows.WaitForSingleObject(handle, 0)
	if err != nil {
		return false, err
	}
	switch status {
	case uint32(windows.WAIT_OBJECT_0):
		return false, nil
	case uint32(windows.WAIT_TIMEOUT):
		return true, nil
	default:
		return false, fmt.Errorf("unexpected WaitForSingleObject status %#x", status)
	}
}

// pidAlive preserves the package's existing boolean helper for platform host
// tests. Runtime recovery uses probePIDLiveness so it cannot discard errors.
func pidAlive(pid int) bool {
	alive, _ := probePIDLiveness(pid)
	return alive
}
