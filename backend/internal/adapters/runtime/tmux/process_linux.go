//go:build linux

package tmux

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

func processBootIdentity(start string) string {
	boot, _, _ := strings.Cut(start, ":")
	return boot
}

func readOwnedProcesses(ctx context.Context) ([]ownedProcess, error) {
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	var processes []ownedProcess
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 1 {
			continue
		}
		p, err := readLinuxProcess(pid, strings.TrimSpace(string(boot)))
		if errors.Is(err, os.ErrNotExist) {
			continue // exited between the directory snapshot and stat read
		}
		if err != nil {
			return nil, fmt.Errorf("read process %d: %w", pid, err)
		}
		processes = append(processes, p)
	}
	return processes, nil
}

func readLinuxProcess(pid int, boot string) (ownedProcess, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return ownedProcess{}, err
	}
	return parseLinuxProcess(pid, string(data), boot)
}

func parseLinuxProcess(pid int, stat, boot string) (ownedProcess, error) {
	// comm is parenthesized and may itself contain spaces or parentheses.
	end := strings.LastIndexByte(stat, ')')
	if end < 0 || strings.TrimSpace(boot) == "" {
		return ownedProcess{}, errors.New("invalid process stat or boot identity")
	}
	fields := strings.Fields(stat[end+1:])
	if len(fields) < 20 {
		return ownedProcess{}, errors.New("incomplete process stat")
	}
	parent, err := strconv.Atoi(fields[1])
	if err != nil {
		return ownedProcess{}, err
	}
	session, err := strconv.Atoi(fields[3])
	if err != nil || session < 0 {
		return ownedProcess{}, errors.New("invalid process session")
	}
	if _, err := strconv.ParseUint(fields[19], 10, 64); err != nil {
		return ownedProcess{}, errors.New("invalid process start identity")
	}
	return ownedProcess{
		processIdentity: processIdentity{PID: pid, Start: boot + ":" + fields[19], Session: fields[3]},
		Parent:          parent,
		Stopped:         fields[0] == "Z" || fields[0] == "X" || fields[0] == "x",
	}, nil
}

func signalOwnedProcess(ctx context.Context, expected ownedProcess, force bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if expected.PID <= 1 {
		return errors.New("refusing to signal invalid owned pid")
	}
	// Pin the kernel process before checking its birth identity. A recycled
	// numeric PID can never redirect the subsequent signal through this fd.
	fd, err := unix.PidfdOpen(expected.PID, 0)
	if errors.Is(err, unix.ESRCH) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open process identity: %w", err)
	}
	defer func() { _ = unix.Close(fd) }()
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return err
	}
	current, err := readLinuxProcess(expected.PID, strings.TrimSpace(string(boot)))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if current.processIdentity != expected.processIdentity || current.Stopped {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	sig := unix.SIGTERM
	if force {
		sig = unix.SIGKILL
	}
	err = unix.PidfdSendSignal(fd, sig, nil, 0)
	if errors.Is(err, unix.ESRCH) {
		return nil
	}
	return err
}
