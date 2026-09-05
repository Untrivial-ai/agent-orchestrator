//go:build !windows

package conpty

import (
	"errors"
	"syscall"
)

// probePIDLiveness probes PID liveness via signal 0. nil and EPERM both mean
// alive (the process exists but may not be signallable). Only an invalid PID
// or ESRCH is conclusive death; every other OS failure remains inconclusive.
func probePIDLiveness(pid int) (bool, error) {
	if pid <= 0 {
		return false, nil
	}
	err := syscall.Kill(pid, 0)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, syscall.EPERM) {
		return true, nil
	}
	if errors.Is(err, syscall.ESRCH) {
		return false, nil
	}
	return false, err
}

// pidAlive preserves the package's existing boolean helper for host process
// tests. Runtime recovery uses probePIDLiveness so it cannot discard errors.
func pidAlive(pid int) bool {
	alive, _ := probePIDLiveness(pid)
	return alive
}
