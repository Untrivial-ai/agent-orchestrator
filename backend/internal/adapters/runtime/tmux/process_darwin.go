//go:build darwin

package tmux

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"golang.org/x/sys/unix"
)

// Darwin birth timestamps identify a process, not a kernel boot.
func processBootIdentity(string) string { return "" }

func readOwnedProcesses(ctx context.Context) ([]ownedProcess, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	entries, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return nil, err
	}
	var processes []ownedProcess
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if entry.Proc.P_pid <= 1 {
			continue
		}
		birth := entry.Proc.P_starttime
		if birth.Sec == 0 {
			continue // kernel tasks have no user process birth identity
		}
		sid, err := unix.Getsid(int(entry.Proc.P_pid))
		if errors.Is(err, unix.ESRCH) {
			continue
		}
		if err != nil {
			return nil, err
		}
		processes = append(processes, ownedProcess{
			processIdentity: processIdentity{
				PID:     int(entry.Proc.P_pid),
				Start:   fmt.Sprintf("%d:%d", birth.Sec, birth.Usec),
				Session: strconv.Itoa(sid),
			},
			Parent:  int(entry.Eproc.Ppid),
			Stopped: entry.Proc.P_stat == 5, // SZOMB, already exited
		})
	}
	return processes, nil
}

func signalOwnedProcess(ctx context.Context, expected ownedProcess, force bool) error {
	// BSD pkill has no Linux -s matcher. Use the kernel's session and process
	// birth identities instead, and revalidate the exact target before signalling.
	processes, err := readOwnedProcesses(ctx)
	if err != nil {
		return err
	}
	for _, p := range processes {
		if p.PID <= 1 || p.processIdentity != expected.processIdentity || p.Stopped {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		sig := unix.SIGTERM
		if force {
			sig = unix.SIGKILL
		}
		err := unix.Kill(p.PID, sig)
		if errors.Is(err, unix.ESRCH) {
			return nil
		}
		return err
	}
	return nil
}
