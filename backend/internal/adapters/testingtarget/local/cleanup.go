package local

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	processutil "github.com/aoagents/agent-orchestrator/backend/internal/process"
	"github.com/aoagents/agent-orchestrator/backend/internal/runfile"
)

// captureTree records descendants only after checking their parent's ownership.
// Previously captured orphans remain owned by their exact process start time.
func (a *Adapter) captureTree(ctx context.Context, s *launch) error {
	processes, err := a.ops.processes(ctx)
	if err != nil {
		return err
	}
	live := make(map[int]processInfo, len(processes))
	for _, process := range processes {
		live[process.PID] = process
	}
	parents := make(map[int]bool)
	gone := make(map[int]bool)
	for pid, started := range s.owned {
		if _, exists := live[pid]; !exists {
			if !s.closing && (pid == s.target.ElectronPID || pid == s.target.DaemonPID) {
				return fmt.Errorf("required target PID %d vanished", pid)
			}
			continue
		}
		current, err := a.ops.startTime(pid)
		if err != nil && (s.closing || (pid != s.target.ElectronPID && pid != s.target.DaemonPID)) {
			skipped, checkErr := a.skipVanished(ctx, s, pid, err)
			if checkErr != nil {
				return checkErr
			}
			if skipped {
				gone[pid] = true
				continue
			}
		}
		if err != nil || !current.Equal(started) {
			return errors.Join(fmt.Errorf("owned PID %d changed identity", pid), err)
		}
		parents[pid] = true
	}
	for {
		added := false
		for _, process := range processes {
			if parents[process.PID] || gone[process.PID] || !parents[process.Parent] {
				continue
			}
			started, err := a.ops.startTime(process.PID)
			if err != nil {
				if s.closing || (process.PID != s.target.ElectronPID && process.PID != s.target.DaemonPID) {
					skipped, checkErr := a.skipVanished(ctx, s, process.PID, err)
					if checkErr != nil {
						return checkErr
					}
					if skipped {
						gone[process.PID] = true
						continue
					}
				}
				return fmt.Errorf("observe child PID %d: %w", process.PID, err)
			}
			s.owned[process.PID], parents[process.PID], added = started, true, true
		}
		if !added {
			return nil
		}
	}
}

// skipVanished tolerates only a missing descendant confirmed by a fresh
// inventory. A live but unreadable PID, reuse, and required target PIDs fail.
func (a *Adapter) skipVanished(ctx context.Context, s *launch, pid int, lookupErr error) (bool, error) {
	if !errors.Is(lookupErr, processutil.ErrNotRunning) && !errors.Is(lookupErr, syscall.ESRCH) && !errors.Is(lookupErr, syscall.EIO) {
		return false, nil
	}
	var err error
	// A kernel-confirmed zombie is not running even while ps still lists it.
	// Bare syscall failures need a second inventory to establish absence.
	if !errors.Is(lookupErr, processutil.ErrNotRunning) {
		var processes []processInfo
		processes, err = a.ops.processes(ctx)
		if err != nil {
			return false, err
		}
		for _, process := range processes {
			if process.PID == pid {
				return false, nil
			}
		}
	}
	if s.vanished == nil {
		s.vanished = make(map[int]bool)
	}
	if !s.vanished[pid] {
		s.vanished[pid] = true
		if s.log != nil {
			_, err = fmt.Fprintf(s.log, "[target launcher] descendant PID %d vanished during inventory\n", pid)
		}
	}
	return true, err
}

// Stop signals only captured PIDs whose OS start times still match. Logs remain;
// private data, profiles and fixtures are deleted only after absence is proved.
func (a *Adapter) Stop(ctx context.Context, target domain.TestTargetIdentity) (ports.TestingCleanupResult, error) {
	failed := ports.TestingCleanupResult{State: domain.TestCleanupFailed}
	s, err := a.find(target)
	if err != nil {
		failed.Leftovers = []string{err.Error()}
		return failed, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if s.setupGroup != 0 {
		if err := waitSetupGroup(ctx, s.setupGroup); err != nil {
			failed.Leftovers = []string{err.Error()}
			return failed, err
		}
		s.setupGroup = 0
	}
	if s.stopped && s.leaseInfo == nil {
		return ports.TestingCleanupResult{State: domain.TestCleanupComplete}, nil
	}
	if s.target.ElectronPID == 0 {
		if s.leaseInfo == nil {
			err := errors.New("preparation checkout reservation identity unavailable")
			failed.Leftovers = []string{err.Error()}
			return failed, err
		}
		if err := removePrivateState(s); err != nil {
			failed.Leftovers = []string{err.Error()}
			return failed, err
		}
		s.stopped = true
		return ports.TestingCleanupResult{State: domain.TestCleanupComplete}, nil
	}
	s.closing = true
	var problems []error
	if err := a.captureTree(ctx, s); err != nil {
		problems = append(problems, err)
	}
	if !s.stopped {
		// Root first: Electron's own shutdown path gets a chance to reap its
		// managed daemon. Remaining children are still independently fenced.
		pids := make([]int, 0, len(s.owned))
		for pid := range s.owned {
			pids = append(pids, pid)
		}
		sort.Ints(pids)
		if err := a.terminate(ctx, s, s.target.ElectronPID); err != nil {
			problems = append(problems, err)
		}
		for _, pid := range pids {
			if pid == s.target.ElectronPID {
				continue
			}
			if err := a.terminate(ctx, s, pid); err != nil {
				problems = append(problems, err)
			}
		}
	}
	if len(problems) == 0 {
		grace, graceCancel := context.WithTimeout(ctx, 2*time.Second)
		defer graceCancel()
		escalated := false
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			remaining, err := a.remaining(ctx, s)
			if err != nil || len(remaining) == 0 {
				if err != nil {
					problems = append(problems, err)
				}
				break
			}
			if !escalated && grace.Err() != nil && ctx.Err() == nil {
				// Capture any last descendants, then recheck each exact kernel
				// birth immediately before escalation. Reused PIDs stay untouched.
				if err := a.captureTree(ctx, s); err != nil {
					problems = append(problems, err)
					break
				}
				for pid := range s.owned {
					if err := a.signalOwned(ctx, s, pid, syscall.SIGKILL); err != nil {
						problems = append(problems, err)
					}
				}
				escalated = true
			}
			select {
			case <-ctx.Done():
				problems = append(problems, ctx.Err())
			case <-ticker.C:
			}
			if ctx.Err() != nil {
				break
			}
		}
	}
	if _, err := a.tmuxCommand(ctx, s, "kill-server"); err != nil {
		problems = append(problems, err)
	}
	leftovers, err := a.remaining(ctx, s)
	if err != nil {
		problems = append(problems, err)
		leftovers = append(leftovers, "process inventory unavailable")
	}
	socket := "tmux socket testing-" + s.target.ID
	if alive, err := a.tmuxCommand(ctx, s, "list-sessions"); err != nil {
		problems = append(problems, err)
		leftovers = append(leftovers, socket+" inventory unavailable")
	} else if alive {
		// A successful list means the server still exists, even if its process
		// daemonized before the descendant inventory could capture it.
		leftovers = append(leftovers, socket)
	}
	windows, err := a.ops.windows(ctx, s.target.ElectronPID)
	if err != nil {
		problems = append(problems, err)
		leftovers = append(leftovers, "window inventory unavailable")
	} else {
		for _, window := range windows {
			leftovers = append(leftovers, "window "+window)
		}
	}
	if err := a.ops.listenerGone(ctx, s.port); err != nil {
		problems = append(problems, err)
		leftovers = append(leftovers, "listener 127.0.0.1:"+strconv.Itoa(s.port))
	}
	info, err := runfile.Read(filepath.Join(s.root, "running.json"))
	if err != nil {
		problems = append(problems, err)
		leftovers = append(leftovers, "run file unreadable")
	} else if info != nil {
		// A hard stop can leave its own handshake. Remove it only after every
		// process/window/listener observation proves this launch is gone.
		if len(leftovers) == 0 && len(problems) == 0 && info.PID == s.target.DaemonPID && info.AppRunID == s.target.LaunchID && info.Port == s.port {
			if err := os.Remove(filepath.Join(s.root, "running.json")); err != nil {
				problems = append(problems, err)
				leftovers = append(leftovers, "run file")
			}
		} else {
			leftovers = append(leftovers, "run file")
		}
	}
	if err := a.checkLiveDaemon(s.liveGuard); err != nil {
		problems = append(problems, err)
		leftovers = append(leftovers, "live AO daemon identity changed")
	}
	if s.log != nil {
		if err := s.log.Sync(); err != nil {
			problems = append(problems, err)
		}
	}
	if len(problems) != 0 || len(leftovers) != 0 {
		failed.Leftovers = leftovers
		if len(failed.Leftovers) == 0 {
			failed.Leftovers = []string{"cleanup could not be established"}
		}
		return failed, errors.Join(append(problems, errors.New("target cleanup incomplete"))...)
	}
	if s.log != nil {
		if err := s.log.Close(); err != nil {
			return failed, err
		}
		s.log = nil
	}
	if err := removePrivateState(s); err != nil {
		failed.Leftovers = []string{"private target state or checkout reservation"}
		return failed, err
	}
	s.stopped = true
	return ports.TestingCleanupResult{State: domain.TestCleanupComplete}, nil
}

func removePrivateState(s *launch) error {
	info, err := os.Lstat(s.root)
	if err != nil || s.rootInfo == nil || !os.SameFile(s.rootInfo, info) {
		return errors.Join(errors.New("target state directory ownership changed"), err)
	}
	root, err := os.OpenRoot(s.root)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	for _, name := range []string{"data", "electron", "fixtures", "daemon", "setup", "app-state.json", "update-settings.json", "target-ao"} {
		if err := root.RemoveAll(name); err != nil {
			return err
		}
	}
	if s.leaseInfo != nil {
		path := filepath.Join(filepath.Dir(s.frontend), ".ao-testing-active")
		info, err := os.Lstat(path)
		if !errors.Is(err, os.ErrNotExist) {
			if err != nil || !os.SameFile(s.leaseInfo, info) {
				return errors.Join(errors.New("target checkout reservation changed"), err)
			}
			if err := os.Remove(path); err != nil {
				return err
			}
		}
		s.leaseInfo = nil
	}
	return nil
}

func (a *Adapter) tmuxCommand(ctx context.Context, s *launch, command string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	output, err := a.ops.run(ctx, s.tmux, []string{"-L", "testing-" + s.target.ID, command}, s.env)
	if err != nil {
		message := strings.TrimSpace(string(output))
		if ctx.Err() == nil && (strings.HasPrefix(message, "no server running on ") ||
			(strings.HasPrefix(message, "error connecting to ") && strings.HasSuffix(message, " (No such file or directory)"))) {
			return false, nil
		}
		return false, fmt.Errorf("target tmux %s: %w", command, err)
	}
	return true, nil
}

func (a *Adapter) terminate(ctx context.Context, s *launch, pid int) error {
	return a.signalOwned(ctx, s, pid, syscall.SIGTERM)
}

func (a *Adapter) signalOwned(ctx context.Context, s *launch, pid int, signal syscall.Signal) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	remaining, err := a.remaining(ctx, s)
	if err != nil {
		return err
	}
	if len(remaining) == 0 {
		return nil
	}
	processes, err := a.ops.processes(ctx)
	if err != nil {
		return err
	}
	exists := false
	for _, process := range processes {
		if process.PID == pid {
			exists = true
		}
	}
	if !exists {
		return nil
	}
	started, ok := s.owned[pid]
	if !ok {
		return fmt.Errorf("PID %d has no captured start time", pid)
	}
	current, err := a.ops.startTime(pid)
	core := pid == s.target.ElectronPID || pid == s.target.DaemonPID
	if errors.Is(err, processutil.ErrNotRunning) || (!core && (errors.Is(err, syscall.ESRCH) || errors.Is(err, syscall.EIO))) {
		return nil
	}
	if err != nil || !current.Equal(started) {
		return errors.Join(fmt.Errorf("refusing signal to changed PID %d", pid), err)
	}
	if err := a.ops.signal(pid, signal); err != nil && !errors.Is(err, syscall.ESRCH) && (!errors.Is(err, syscall.EIO) || core) {
		return err
	}
	return nil
}

func (a *Adapter) remaining(ctx context.Context, s *launch) ([]string, error) {
	processes, err := a.ops.processes(ctx)
	if err != nil {
		return nil, err
	}
	var leftovers []string
	for _, process := range processes {
		_, owned := s.owned[process.PID]
		if owned || process.PID == s.target.ElectronPID || process.PID == s.target.DaemonPID {
			_, err := a.ops.startTime(process.PID)
			if errors.Is(err, processutil.ErrNotRunning) {
				continue
			}
			if err != nil {
				return nil, err
			}
			leftovers = append(leftovers, "PID "+strconv.Itoa(process.PID))
		}
	}
	sort.Strings(leftovers)
	return leftovers, nil
}

func listenerGone(ctx context.Context, port int) error {
	dialer := net.Dialer{Timeout: 250 * time.Millisecond}
	connection, err := dialer.DialContext(ctx, "tcp", "127.0.0.1:"+strconv.Itoa(port))
	if err == nil {
		_ = connection.Close()
		return errors.New("target port still has a listener")
	}
	if !errors.Is(err, syscall.ECONNREFUSED) {
		return fmt.Errorf("target listener absence is uncertain: %w", err)
	}
	return nil
}
