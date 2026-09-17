//go:build linux

package conpty

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
)

// linuxPTYConn is a native Linux pseudoterminal owned by the detached host.
// The child starts in its own session/process group (creack/pty's StartWithSize
// sets Setsid: true), so teardown can reap the whole launched process tree
// and session rather than leaving background jobs or altered process groups behind.
type linuxPTYConn struct {
	pty *os.File
	cmd *exec.Cmd

	closeOnce sync.Once
	doneC     chan struct{}
	exitMu    sync.Mutex
	exitCode  int
	exited    bool

	leaderPID       int
	leaderStartTime uint64
}

const linuxPTYCloseGrace = 250 * time.Millisecond

func newConPTY(cwd, shellCmd string, shellArgs []string) (ptyConn, error) {
	// shellCmd and shellArgs are the runtime launch argv assembled by AO's
	// trusted agent adapter, not input interpreted by a shell.
	cmd := exec.Command(shellCmd, shellArgs...) // #nosec G702 -- intentional direct argv execution
	cmd.Dir = cwd
	cmd.Env = os.Environ()

	f, err := pty.StartWithSize(cmd, &pty.Winsize{
		Cols: initialConPTYColumns,
		Rows: initialConPTYRows,
	})
	if err != nil {
		return nil, fmt.Errorf("linux pty: start command: %w", err)
	}

	leaderPID := 0
	var leaderStartTime uint64
	if cmd.Process != nil {
		leaderPID = cmd.Process.Pid
		if info, err := readLinuxProcInfo(leaderPID); err == nil {
			leaderStartTime = info.startTime
		}
	}

	c := &linuxPTYConn{
		pty:             f,
		cmd:             cmd,
		doneC:           make(chan struct{}),
		leaderPID:       leaderPID,
		leaderStartTime: leaderStartTime,
	}
	go c.wait()
	return c, nil
}

func (c *linuxPTYConn) wait() {
	err := c.cmd.Wait()
	code := 0
	if c.cmd.ProcessState != nil {
		code = c.cmd.ProcessState.ExitCode()
	} else if err != nil {
		code = -1
	}
	c.exitMu.Lock()
	c.exitCode = code
	c.exited = true
	c.exitMu.Unlock()
	close(c.doneC)
}

func (c *linuxPTYConn) Read(b []byte) (int, error)  { return c.pty.Read(b) }
func (c *linuxPTYConn) Write(b []byte) (int, error) { return c.pty.Write(b) }

func (c *linuxPTYConn) Resize(cols, rows int) error {
	if cols <= 0 || rows <= 0 || cols > math.MaxUint16 || rows > math.MaxUint16 {
		return fmt.Errorf("linux pty: invalid size %dx%d", cols, rows)
	}
	return pty.Setsize(c.pty, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
}

func (c *linuxPTYConn) Close() error {
	var closeErr error
	c.closeOnce.Do(func() {
		if c.leaderPID > 0 && c.leaderStartTime > 0 {
			// Phase 1: Discover session members and send graceful SIGTERM.
			procs := linuxFindSessionProcesses(c.leaderPID, c.leaderStartTime)
			if len(procs) > 0 {
				signalValidatedSessionProcs(procs, syscall.SIGTERM)
				_ = waitForProcIdentitiesExit(procs, linuxPTYCloseGrace)
			}

			// Phase 2: Unconditionally rediscover validated session members.
			// This catches:
			// (a) Processes that ignored or were slow to handle SIGTERM.
			// (b) Late children spawned by a SIGTERM trap/exit handler before the parent exited.
			// A bounded loop ensures repeated sweeps with SIGKILL until the session is empty.
			for attempt := 0; attempt < 3; attempt++ {
				remaining := linuxFindSessionProcesses(c.leaderPID, c.leaderStartTime)
				if len(remaining) == 0 {
					break
				}
				signalValidatedSessionProcs(remaining, syscall.SIGKILL)
				_ = waitForProcIdentitiesExit(remaining, linuxPTYCloseGrace)
			}
		}
		if c.pty != nil {
			closeErr = c.pty.Close()
		}
	})
	return closeErr
}

type linuxProcInfo struct {
	pid       int
	ppid      int
	pgrp      int
	sid       int
	startTime uint64
	zombie    bool
}

func readLinuxProcInfo(pid int) (linuxProcInfo, error) {
	if pid <= 0 {
		return linuxProcInfo{}, errors.New("invalid pid")
	}
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return linuxProcInfo{}, err
	}
	// /proc/[pid]/stat format: pid (comm) state ppid pgrp session ...
	// The comm field is enclosed in parentheses and may contain spaces or ')'.
	closeIdx := bytes.LastIndexByte(data, ')')
	if closeIdx == -1 || closeIdx+2 >= len(data) {
		return linuxProcInfo{}, errors.New("malformed /proc stat")
	}
	fields := strings.Fields(string(data[closeIdx+2:]))
	if len(fields) < 20 {
		return linuxProcInfo{}, errors.New("truncated /proc stat")
	}
	state := fields[0]
	ppid, err1 := strconv.Atoi(fields[1])
	pgrp, err2 := strconv.Atoi(fields[2])
	sid, err3 := strconv.Atoi(fields[3])
	startTime, err4 := strconv.ParseUint(fields[19], 10, 64)
	if err1 != nil || err2 != nil || err3 != nil || err4 != nil {
		return linuxProcInfo{}, errors.New("invalid /proc stat numeric fields")
	}
	return linuxProcInfo{
		pid:       pid,
		ppid:      ppid,
		pgrp:      pgrp,
		sid:       sid,
		startTime: startTime,
		zombie:    state == "Z",
	}, nil
}

func linuxFindSessionProcesses(sid int, leaderStartTime uint64) []linuxProcInfo {
	if sid <= 0 || leaderStartTime == 0 {
		return nil
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	selfPID := os.Getpid()
	procMap := make(map[int]linuxProcInfo)
	var allProcs []linuxProcInfo
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 1 || pid == selfPID {
			continue
		}
		info, err := readLinuxProcInfo(pid)
		if err != nil {
			continue
		}
		procMap[pid] = info
		allProcs = append(allProcs, info)
	}

	// Validate the session leader identity if a process with PID sid is currently in /proc.
	if leaderProc, ok := procMap[sid]; ok {
		// If PID sid is occupied by a process with a different start time than our recorded
		// leader start time, the PID has been recycled for an unrelated process or session.
		if leaderProc.startTime != leaderStartTime {
			return nil
		}
	}

	inSession := make(map[int]bool)
	for _, p := range allProcs {
		if p.sid == sid {
			if p.startTime >= leaderStartTime {
				inSession[p.pid] = true
			}
		}
	}

	for {
		added := false
		for _, p := range allProcs {
			if !inSession[p.pid] && inSession[p.ppid] {
				if p.startTime >= leaderStartTime {
					inSession[p.pid] = true
					added = true
				}
			}
		}
		if !added {
			break
		}
	}

	var sessionProcs []linuxProcInfo
	for _, p := range allProcs {
		if inSession[p.pid] && !p.zombie {
			sessionProcs = append(sessionProcs, p)
		}
	}
	return sessionProcs
}

// processAliveWithIdentity verifies that the process at pid is currently alive,
// is not a zombie, and has the exact expected startTime.
func processAliveWithIdentity(pid int, expectedStartTime uint64) bool {
	if pid <= 0 || expectedStartTime == 0 {
		return false
	}
	info, err := readLinuxProcInfo(pid)
	if err != nil || info.zombie {
		return false
	}
	return info.startTime == expectedStartTime
}

// signalProcessIfIdentityMatches verifies the process's current startTime immediately
// before dispatching the signal to prevent killing a recycled PID (TOCTOU guard).
func signalProcessIfIdentityMatches(pid int, expectedStartTime uint64, sig syscall.Signal) bool {
	if !processAliveWithIdentity(pid, expectedStartTime) {
		return false
	}
	_ = syscall.Kill(pid, sig)
	return true
}

// signalProcessGroupIfIdentityMatches verifies the group leader's current startTime
// immediately before dispatching the signal to the process group.
func signalProcessGroupIfIdentityMatches(pgid int, expectedStartTime uint64, sig syscall.Signal) bool {
	if !processAliveWithIdentity(pgid, expectedStartTime) {
		return false
	}
	_ = syscall.Kill(-pgid, sig)
	return true
}

func signalValidatedSessionProcs(procs []linuxProcInfo, sig syscall.Signal) {
	validPIDs := make(map[int]uint64, len(procs))
	for _, p := range procs {
		validPIDs[p.pid] = p.startTime
	}

	// 1. Signal each individual process directly with pre-signal start-time validation
	for _, p := range procs {
		signalProcessIfIdentityMatches(p.pid, p.startTime, sig)
	}

	// 2. Only signal process groups whose group leader identity is verified right now
	signaledPGRPs := make(map[int]bool)
	for _, p := range procs {
		if p.pgrp > 1 && !signaledPGRPs[p.pgrp] {
			if leaderStartTime, ok := validPIDs[p.pgrp]; ok {
				signaledPGRPs[p.pgrp] = true
				signalProcessGroupIfIdentityMatches(p.pgrp, leaderStartTime, sig)
			}
		}
	}
}

func waitForProcIdentitiesExit(procs []linuxProcInfo, timeout time.Duration) bool {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		alive := false
		for _, p := range procs {
			if processAliveWithIdentity(p.pid, p.startTime) {
				alive = true
				break
			}
		}
		if !alive {
			return true
		}
		select {
		case <-deadline.C:
			for _, p := range procs {
				if processAliveWithIdentity(p.pid, p.startTime) {
					return false
				}
			}
			return true
		case <-ticker.C:
		}
	}
}

func linuxSessionAlive(sid int, leaderStartTime uint64) bool {
	if sid <= 0 || leaderStartTime == 0 {
		return false
	}
	procs := linuxFindSessionProcesses(sid, leaderStartTime)
	return len(procs) > 0
}

func (c *linuxPTYConn) Done() <-chan struct{} { return c.doneC }

func (c *linuxPTYConn) PID() int {
	return c.leaderPID
}

func (c *linuxPTYConn) ExitCode() (int, bool) {
	c.exitMu.Lock()
	defer c.exitMu.Unlock()
	return c.exitCode, c.exited
}
