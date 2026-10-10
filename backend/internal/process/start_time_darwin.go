//go:build darwin

package process

import (
	"errors"
	"fmt"
	"time"

	"golang.org/x/sys/unix"
)

// StartTime returns a live process's kernel creation time at microsecond
// precision. Callers must compare it before acting on a captured PID.
func StartTime(pid int) (time.Time, error) {
	if pid < 1 {
		return time.Time{}, fmt.Errorf("invalid process PID %d", pid)
	}
	info, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		if errors.Is(err, unix.ESRCH) || errors.Is(err, unix.EIO) {
			err = errors.Join(err, ErrNotRunning)
		}
		return time.Time{}, fmt.Errorf("observe PID %d start time: %w", pid, err)
	}
	p := info.Proc
	if int(p.P_pid) != pid || p.P_starttime.Sec <= 0 || p.P_stat == 5 {
		return time.Time{}, fmt.Errorf("PID %d is missing, changed or a zombie: %w", pid, ErrNotRunning)
	}
	return time.Unix(p.P_starttime.Sec, int64(p.P_starttime.Usec)*1000).UTC(), nil
}
