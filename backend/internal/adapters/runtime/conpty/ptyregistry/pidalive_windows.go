//go:build windows

package ptyregistry

import (
	"golang.org/x/sys/windows"
)

// defaultPidAlive probes PID liveness on Windows. OpenProcess success alone
// does NOT mean the process is alive: a terminated process's PID can still be
// opened successfully on Windows (the handle just refers to a dead process
// object until it's fully reaped). We must additionally wait on the handle:
// WAIT_TIMEOUT means the process has not signaled (i.e. it's still running).
// Any other result (WAIT_OBJECT_0, etc.) means it has exited.
// ERROR_ACCESS_DENIED mirrors EPERM: the process exists but cannot be
// queried, so treat as alive.
func defaultPidAlive(pid int) bool {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return err == windows.ERROR_ACCESS_DENIED
	}
	defer windows.CloseHandle(h)

	status, err := windows.WaitForSingleObject(h, 0)
	if err != nil {
		return false
	}
	return status == uint32(windows.WAIT_TIMEOUT)
}
