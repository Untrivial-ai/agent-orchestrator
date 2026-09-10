package tmux

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// Start is a kernel process birth identity, including the boot identity on
// Linux. A PID by itself is never sufficient authority to signal a retry.
type processIdentity struct {
	PID     int    `json:"pid"`
	Start   string `json:"start"`
	Session string `json:"session"`
}

type ownedProcess struct {
	processIdentity
	Parent  int
	Stopped bool // exited or zombie; a stopped job is still a live workload
}

type paneState struct {
	PID  int
	Dead bool
}

type pendingTeardown struct {
	Version   int               `json:"version"`
	ID        string            `json:"id"`
	Socket    string            `json:"socket"`
	Boot      string            `json:"boot,omitempty"`
	Panes     []processIdentity `json:"panes"`
	Processes []processIdentity `json:"processes"`
	// Session labels from retained dead panes cannot grant signal authority.
	// Persist them so unresolved cleanup also fences Create and Restart.
	UnconfirmedSessions []string `json:"unconfirmed_sessions,omitempty"`
}

func cleanupDirectory(runFile string) string {
	if runFile == "" {
		runFile = getenv("AO_RUN_FILE")
	}
	if runFile != "" {
		return filepath.Join(filepath.Dir(runFile), "tmux-cleanup")
	}
	if dataDir := getenv("AO_DATA_DIR"); dataDir != "" {
		return filepath.Join(dataDir, "tmux-cleanup")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "" // fail closed when cleanup first needs to persist ownership
	}
	return filepath.Join(home, ".ao", "tmux-cleanup")
}

// Destroy records process ownership before removing the pane, then waits for
// the owned workload to exit. Errors retain that evidence for the next retry,
// including a retry by a freshly started daemon with no tmux pane left to query.
func (r *Runtime) Destroy(ctx context.Context, handle ports.RuntimeHandle) error {
	id, err := handleID(handle)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, r.reapGrace+5*time.Second+2*r.timeout)
	defer cancel()
	unlock, err := r.lockMutation(ctx, id)
	if err != nil {
		return err
	}
	defer unlock()
	return r.destroyLocked(ctx, id)
}

// The same lock guards Create and Restart, so a new pane generation cannot
// replace the captured one between process discovery and kill-session.
func (r *Runtime) lockMutation(ctx context.Context, id string) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	entry, _ := r.cleanupMu.LoadOrStore(id, make(chan struct{}, 1))
	gate, ok := entry.(chan struct{})
	if !ok {
		return nil, errors.New("tmux runtime: invalid mutation gate")
	}
	select {
	case gate <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-gate
			return nil, err
		}
		return func() { <-gate }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (r *Runtime) requireCompletedTeardown(id string) error {
	pending, err := r.loadTeardown(id)
	if err != nil {
		return fmt.Errorf("tmux runtime: inspect pending cleanup %s: %w", id, err)
	}
	if pending != nil {
		return fmt.Errorf("tmux runtime: process cleanup pending for %s; retry teardown before launching", id)
	}
	return nil
}

func (r *Runtime) destroyLocked(ctx context.Context, id string) error {
	ctx, cancel := context.WithTimeout(ctx, r.reapGrace+5*time.Second+2*r.timeout)
	defer cancel()

	pending, err := r.loadTeardown(id)
	if err != nil {
		return fmt.Errorf("tmux runtime: load cleanup %s: %w", id, err)
	}
	if pending != nil {
		// Do not rediscover a similarly named pane on another socket after restart.
		r.rememberSessionSocket(id, pending.Socket)
	}
	panes, err := r.paneStates(ctx, id)
	if err != nil {
		return err
	}
	if len(panes) > 0 {
		table, err := r.processes(ctx)
		if err != nil {
			return fmt.Errorf("tmux runtime: discover processes %s: %w", id, err)
		}
		roots, err := paneIdentities(table, panes)
		if err != nil {
			return fmt.Errorf("tmux runtime: discover ownership %s: %w", id, err)
		}
		if pending != nil {
			for _, root := range roots {
				if !containsIdentity(pending.Panes, root) {
					return fmt.Errorf("tmux runtime: cleanup %s: pane generation changed; retained previous process ownership", id)
				}
			}
		} else {
			socket, err := r.socketForSession(ctx, id)
			if err != nil {
				return err
			}
			pending = &pendingTeardown{Version: 1, ID: id, Socket: socket, Panes: roots, Processes: roots}
			if len(table) > 0 {
				pending.Boot = processBootIdentity(table[0].Start)
			}
		}
		for _, pane := range panes {
			session := strconv.Itoa(pane.PID)
			if pane.Dead && !slices.Contains(pending.UnconfirmedSessions, session) {
				pending.UnconfirmedSessions = append(pending.UnconfirmedSessions, session)
			}
		}
		pending.Processes = retainIdentities(pending.Processes, ownedDescendants(table, pending.Processes))
		if err := r.saveTeardown(ctx, pending); err != nil {
			return fmt.Errorf("tmux runtime: retain cleanup %s: %w", id, err)
		}
	}

	out, killErr := r.runForSession(ctx, id, killSessionArgs(id)...)
	if killErr != nil {
		var exitErr *exec.ExitError
		if errors.As(killErr, &exitErr) && killSessionMissingOutput(string(out)) {
			killErr = nil
		} else {
			killErr = fmt.Errorf("tmux runtime: destroy session %s: %w", id, killErr)
		}
	}
	var reapErr error
	if pending != nil {
		reapErr = r.reapOwnedProcesses(ctx, pending)
	}
	if err := errors.Join(killErr, reapErr); err != nil {
		return err
	}
	if err := os.Remove(r.teardownPath(id)); err == nil {
		if err := r.syncCleanupDirectory(); err != nil {
			return fmt.Errorf("tmux runtime: clear cleanup %s: %w", id, err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("tmux runtime: clear cleanup %s: %w", id, err)
	}
	r.forgetSessionSocket(id)
	return nil
}

func (r *Runtime) paneStates(ctx context.Context, id string) ([]paneState, error) {
	out, err := r.runForSession(ctx, id, listPaneStatesArgs(id)...)
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && killSessionMissingOutput(string(out)) {
			return nil, nil
		}
		return nil, fmt.Errorf("tmux runtime: discover panes %s: %w", id, err)
	}
	var panes []paneState
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 || (fields[1] != "0" && fields[1] != "1") {
			return nil, fmt.Errorf("tmux runtime: discover panes %s: invalid pane state %q", id, line)
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil || pid <= 1 {
			return nil, fmt.Errorf("tmux runtime: discover panes %s: invalid pid %q", id, line)
		}
		panes = append(panes, paneState{PID: pid, Dead: fields[1] == "1"})
	}
	return panes, nil
}

func paneIdentities(table []ownedProcess, panes []paneState) ([]processIdentity, error) {
	var roots []processIdentity
	for _, pane := range panes {
		// A retained dead pane's numeric PID can already belong to another
		// process. It must never establish new signalling authority.
		if pane.Dead {
			continue
		}
		found := false
		for _, p := range table {
			if p.PID == pane.PID && p.Start != "" && p.Session != "" {
				roots = append(roots, p.processIdentity)
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("pane pid %d absent from process inventory", pane.PID)
		}
	}
	return roots, nil
}

func containsIdentity(known []processIdentity, p processIdentity) bool {
	for _, k := range known {
		if k.PID == p.PID && k.Start == p.Start {
			return true
		}
	}
	return false
}

// A live, matching identity anchors both descendant and OS-session membership.
// If all recorded identities have disappeared, their numeric PIDs/session IDs
// confer no authority over new processes that happen to reuse those numbers.
func ownedDescendants(table []ownedProcess, known []processIdentity) []ownedProcess {
	pids := make(map[int]bool)
	sessions := make(map[string]bool)
	for _, p := range table {
		if containsIdentity(known, p.processIdentity) {
			pids[p.PID] = true
			if p.Session != "" {
				sessions[p.Session] = true
			}
		}
	}
	for changed := true; changed; {
		changed = false
		for _, p := range table {
			if p.PID <= 1 || pids[p.PID] || (!pids[p.Parent] && !sessions[p.Session]) {
				continue
			}
			pids[p.PID] = true
			changed = true
		}
	}
	var owned []ownedProcess
	for _, p := range table {
		if pids[p.PID] {
			owned = append(owned, p)
		}
	}
	return owned
}

// Keep vanished identities as evidence of sessions whose membership still
// needs to be checked. They do not grant authority to signal a new process.
func retainIdentities(known []processIdentity, processes []ownedProcess) []processIdentity {
	next := append([]processIdentity(nil), known...)
	for _, p := range processes {
		if !slices.Contains(next, p.processIdentity) {
			next = append(next, p.processIdentity)
		}
	}
	return next
}

func unconfirmedSessionMember(table []ownedProcess, pending *pendingTeardown) bool {
	for _, p := range table {
		if p.Stopped {
			continue
		}
		boot := processBootIdentity(p.Start)
		if slices.Contains(pending.UnconfirmedSessions, p.Session) && (pending.Boot == "" || boot == "" || pending.Boot == boot) {
			return true
		}
		for _, k := range pending.Processes {
			previousBoot := processBootIdentity(k.Start)
			if p.Session == k.Session && (previousBoot == "" || boot == "" || previousBoot == boot) {
				return true
			}
		}
	}
	return false
}

func (r *Runtime) reapOwnedProcesses(ctx context.Context, pending *pendingTeardown) error {
	deadline := time.Now().Add(r.reapGrace)
	ctx, cancel := context.WithTimeout(ctx, r.reapGrace+5*time.Second)
	defer cancel()
	sent := make(map[processIdentity]bool)
	forced := false
	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("tmux runtime: verify process exit %s: %w", pending.ID, err)
		}
		table, err := r.processes(ctx)
		if err != nil {
			return fmt.Errorf("tmux runtime: verify process exit %s: %w", pending.ID, err)
		}
		owned := ownedDescendants(table, pending.Processes)
		live := make([]ownedProcess, 0, len(owned))
		for _, p := range owned {
			if !p.Stopped {
				live = append(live, p)
			}
		}
		if len(live) == 0 {
			// An unobserved child may outlive the last sampled parent. The
			// session label is enough to withhold success, but not enough to
			// signal it after identities may have been recycled during downtime.
			if unconfirmedSessionMember(table, pending) {
				return fmt.Errorf("tmux runtime: cleanup unconfirmed for %s: session members remain without a live ownership anchor", pending.ID)
			}
			return nil
		}
		// A process may fork during shutdown. Persist each newly proven child
		// before signalling it so cancellation or a crash cannot lose ownership.
		next := retainIdentities(pending.Processes, owned)
		if !slices.Equal(pending.Processes, next) {
			pending.Processes = next
			if err := r.saveTeardown(ctx, pending); err != nil {
				return fmt.Errorf("tmux runtime: retain cleanup %s: %w", pending.ID, err)
			}
		}
		if !forced && !time.Now().Before(deadline) {
			forced = true
			clear(sent)
		}
		for _, p := range live {
			if sent[p.processIdentity] {
				continue
			}
			if err := r.signalProcess(ctx, p, forced); err != nil {
				return fmt.Errorf("tmux runtime: signal owned pid %d for %s: %w", p.PID, pending.ID, err)
			}
			sent[p.processIdentity] = true
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("tmux runtime: verify process exit %s: %w", pending.ID, ctx.Err())
		case <-time.After(reapPollInterval):
		}
	}
}

func (r *Runtime) teardownPath(id string) string {
	key := sha256.Sum256([]byte(r.socketName + "\x00" + id))
	return filepath.Join(r.cleanupDir, hex.EncodeToString(key[:])+".json")
}

func (r *Runtime) loadTeardown(id string) (*pendingTeardown, error) {
	if r.cleanupDir == "" {
		return nil, errors.New("process cleanup directory unavailable")
	}
	data, err := os.ReadFile(r.teardownPath(id))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var pending pendingTeardown
	if err := json.Unmarshal(data, &pending); err != nil {
		return nil, err
	}
	if pending.Version != 1 || pending.ID != id || (len(pending.Panes) == 0 && len(pending.UnconfirmedSessions) == 0) {
		return nil, errors.New("invalid process cleanup record")
	}
	for _, session := range pending.UnconfirmedSessions {
		if pid, err := strconv.Atoi(session); err != nil || pid <= 1 {
			return nil, errors.New("invalid unconfirmed process session")
		}
	}
	for _, p := range append(append([]processIdentity(nil), pending.Panes...), pending.Processes...) {
		if p.PID <= 1 || p.Start == "" || p.Session == "" {
			return nil, errors.New("invalid retained process identity")
		}
	}
	return &pending, nil
}

func (r *Runtime) saveTeardown(ctx context.Context, pending *pendingTeardown) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.MkdirAll(r.cleanupDir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(r.cleanupDir, ".pending-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(f.Name()) }()
	err = json.NewEncoder(f).Encode(pending)
	if err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Rename(f.Name(), r.teardownPath(pending.ID)); err != nil {
		return err
	}
	return r.syncCleanupDirectory()
}

func (r *Runtime) syncCleanupDirectory() error {
	dir, err := os.Open(r.cleanupDir)
	if err != nil {
		return err
	}
	return errors.Join(dir.Sync(), dir.Close())
}
