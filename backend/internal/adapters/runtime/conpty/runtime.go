// runtime.go - conpty Runtime adapter. Implements ports.Runtime and
// ports.Attacher (see attach.go). Drives sessions via the B3 pty-host over
// loopback TCP, using the B1 protocol and the B2 registry for restart recovery.
package conpty

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sync"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/runtime/conpty/ptyregistry"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const runtimeLaunchIDEnv = "AO_RUNTIME_LAUNCH_ID"

// Ensure Runtime satisfies the port at compile time (Attach in attach.go).
var _ ports.Runtime = (*Runtime)(nil)
var _ ports.StyledTerminalOutputReader = (*Runtime)(nil)

// validSessionID matches agent-orchestrator's assertValidSessionId.
var validSessionID = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// hostSession is the in-memory state for a live pty-host connection.
type hostSession struct {
	addr             string
	pid              int
	launchID         string
	protocolVersion  int
	protocolResolved bool
}

// Options configures the Runtime. All fields are optional; zero values use
// sensible defaults. The Spawner field is injectable for tests.
type Options struct {
	// Spawner overrides the default OS-level process spawner. If nil,
	// defaultSpawnHost is used (Windows-only; returns an error on other OSes).
	Spawner hostSpawner

	// RunFilePath is this daemon instance's running.json path (config.Config.
	// RunFilePath). It scopes the B2 pty-host registry to the same directory,
	// so two AO instances on one machine with different AO_RUN_FILE/
	// AO_DATA_DIR overrides never share one registry -- see
	// ptyregistry.SetRunFilePath. Empty uses the ~/.ao default.
	RunFilePath string
}

// Runtime is the conpty runtime adapter.
type Runtime struct {
	spawner       hostSpawner
	killHost      func(string) error
	pidIsAlive    func(int) bool
	processFinder func(int) (processKiller, error)
	destroyWait   time.Duration
	destroyPoll   time.Duration

	mu       sync.Mutex
	sessions map[string]*hostSession // sessionID -> live session
}

// New creates a Runtime with the given options.
func New(opts Options) *Runtime {
	ptyregistry.SetRunFilePath(opts.RunFilePath)
	sp := opts.Spawner
	if sp == nil {
		sp = defaultSpawnHost
	}
	return &Runtime{
		spawner:       sp,
		killHost:      clientKill,
		pidIsAlive:    pidAlive,
		processFinder: findProcess,
		destroyWait:   500 * time.Millisecond,
		destroyPoll:   25 * time.Millisecond,
		sessions:      make(map[string]*hostSession),
	}
}

// Create spawns a detached pty-host for the session, waits for READY, stores
// the addr+pid in-memory and in the B2 registry, and returns the handle.
// Returns an error if sessionID is invalid, already exists, or spawn fails.
func (r *Runtime) Create(ctx context.Context, cfg ports.RuntimeConfig) (ports.RuntimeHandle, error) {
	id := string(cfg.SessionID)
	if !validSessionID.MatchString(id) {
		return ports.RuntimeHandle{}, ports.NewRuntimeCreateRollbackSafeError(fmt.Errorf("conpty: invalid session id %q: must match ^[a-zA-Z0-9_-]+$", id))
	}
	if cfg.WorkspacePath == "" {
		return ports.RuntimeHandle{}, ports.NewRuntimeCreateRollbackSafeError(errors.New("conpty: workspace path required"))
	}
	if len(cfg.Argv) == 0 {
		return ports.RuntimeHandle{}, ports.NewRuntimeCreateRollbackSafeError(errors.New("conpty: argv required"))
	}

	r.mu.Lock()
	if existing, dup := r.sessions[id]; dup {
		r.mu.Unlock()
		handle := ports.RuntimeHandle{ID: id}
		if existing != nil {
			handle.RuntimeLaunchID = existing.launchID
		}
		return handle, ports.NewRuntimeCreatePreserveError(handle, fmt.Errorf("conpty: session %q already exists; destroy before re-creating", id))
	}
	// Reserve the slot before the async spawn so a concurrent Create for the
	// same id fails immediately (no gap between check and set).
	r.sessions[id] = nil
	r.mu.Unlock()

	addr, pid, err := r.spawner(ctx, id, cfg.WorkspacePath, cfg.Argv, cfg.Env)
	if err != nil {
		r.mu.Lock()
		delete(r.sessions, id)
		r.mu.Unlock()
		return ports.RuntimeHandle{}, ports.NewRuntimeCreateRollbackSafeError(fmt.Errorf("conpty: spawn pty-host for %q: %w", id, err))
	}

	launchID := cfg.RuntimeLaunchID
	if launchID == "" {
		launchID = cfg.Env[runtimeLaunchIDEnv]
	}
	sess := &hostSession{
		addr: addr, pid: pid, launchID: launchID,
		protocolVersion: conPTYHostProtocolVersion, protocolResolved: true,
	}

	r.mu.Lock()
	r.sessions[id] = sess
	r.mu.Unlock()

	// Register in B2 registry for daemon-restart recovery (best-effort).
	_ = ptyregistry.Register(ptyregistry.Entry{
		SessionID:    id,
		PtyHostPID:   pid,
		PipePath:     addr, // ponytail: reuse PipePath field for loopback addr
		LaunchID:     sess.launchID,
		RegisteredAt: time.Now().UTC().Format(time.RFC3339),
	})

	return ports.RuntimeHandle{ID: id, RuntimeLaunchID: launchID}, nil
}

// Destroy gracefully kills the pty-host, then force-kills it when necessary.
// The session remains registered until its PID is confirmed gone so callers
// never receive a false-success teardown while a provider may still be alive.
// Unknown/already-gone sessions remain idempotent.
func (r *Runtime) Destroy(ctx context.Context, handle ports.RuntimeHandle) error {
	sess := r.resolve(handle.ID)
	if sess == nil {
		return nil // unknown or already gone
	}

	// Ask host to shut down gracefully (triggers shutdown() in Serve).
	gracefulErr := r.killHost(sess.addr)
	exited, waitErr := r.waitForPIDExit(ctx, sess.pid)
	if waitErr != nil {
		return errors.Join(gracefulErr, waitErr)
	}

	var forceErr error
	if !exited {
		process, findErr := r.processFinder(sess.pid)
		if findErr != nil {
			forceErr = fmt.Errorf("find pty-host pid %d: %w", sess.pid, findErr)
		} else if err := process.Kill(); err != nil {
			forceErr = fmt.Errorf("force-kill pty-host pid %d: %w", sess.pid, err)
		}
		exited, waitErr = r.waitForPIDExit(ctx, sess.pid)
		if waitErr != nil {
			return errors.Join(gracefulErr, forceErr, waitErr)
		}
	}
	if !exited {
		return errors.Join(gracefulErr, forceErr, fmt.Errorf("conpty: pty-host pid %d is still alive after teardown", sess.pid))
	}

	r.mu.Lock()
	delete(r.sessions, handle.ID)
	r.mu.Unlock()

	if err := ptyregistry.Unregister(handle.ID); err != nil {
		return fmt.Errorf("conpty: unregister destroyed session %q: %w", handle.ID, err)
	}
	return nil
}

func (r *Runtime) waitForPIDExit(ctx context.Context, pid int) (bool, error) {
	if !r.pidIsAlive(pid) {
		return true, nil
	}
	wait := r.destroyWait
	if wait <= 0 {
		return false, nil
	}
	poll := r.destroyPoll
	if poll <= 0 {
		poll = 25 * time.Millisecond
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-timer.C:
			return !r.pidIsAlive(pid), nil
		case <-ticker.C:
			if !r.pidIsAlive(pid) {
				return true, nil
			}
		}
	}
}

// IsAlive distinguishes three outcomes so the reaper never spuriously reaps a
// live session on a transient probe failure:
//
//   - (true, nil):  the pty-host answered a status probe -> alive.
//   - (false, nil): DEFINITIVELY gone. Either the session resolves to nothing
//     (no in-memory entry and no registry entry), or the dial was refused
//     (nothing listening on the loopback addr).
//   - (false, err): a TRANSIENT probe failure (loopback timeout, connected-
//     then-failed I/O). The reaper records ProbeFailed and retries rather than
//     treating it as a death conclusion.
//
// tmux returns a non-nil error for transient failures for the same
// reason; conpty matches that contract here.
func (r *Runtime) IsAlive(ctx context.Context, handle ports.RuntimeHandle) (bool, error) {
	sess := r.resolve(handle.ID)
	if sess == nil {
		return false, nil // no in-memory entry, no registry entry -> definitively gone
	}
	return clientIsAlive(sess.addr)
}

// IsSupervisedProcessAlive uses the pty-host's child status. For a supervised
// launch that child is the AO supervisor, whose lifetime matches the managed
// agent process. When a generation ref is supplied, the launch id captured at
// Create (and persisted in the recovery registry) must match exactly.
func (r *Runtime) IsSupervisedProcessAlive(ctx context.Context, handle ports.RuntimeHandle, ref ports.SupervisedProcessRef) (bool, error) {
	sess := r.resolve(handle.ID)
	if sess == nil {
		return false, nil
	}
	if ref.SessionID != "" && string(ref.SessionID) != handle.ID {
		return false, nil
	}
	if ref.LaunchID != "" && (sess.launchID == "" || sess.launchID != ref.LaunchID) {
		return false, nil
	}
	status, hostAlive, err := clientStatus(sess.addr)
	if err != nil {
		return false, err
	}
	if !hostAlive {
		return false, nil
	}
	return status.Alive, nil
}

// IsExactSupervisedProcessAlive has the same implementation on ConPTY because
// the pty-host registry already fences its one child by the exact launch id.
func (r *Runtime) IsExactSupervisedProcessAlive(ctx context.Context, handle ports.RuntimeHandle, ref ports.SupervisedProcessRef) (bool, error) {
	if ref.SessionID == "" || ref.LaunchID == "" {
		return false, errors.New("conpty: exact supervisor session and launch are required")
	}
	return r.IsSupervisedProcessAlive(ctx, handle, ref)
}

// SendMessage chunks message and writes it to the pty-host followed by Enter.
func (r *Runtime) SendMessage(ctx context.Context, handle ports.RuntimeHandle, message string) error {
	sess := r.resolve(handle.ID)
	if sess == nil {
		return fmt.Errorf("conpty: session %q not found", handle.ID)
	}
	return clientSendMessage(sess.addr, message)
}

// Interrupt sends Ctrl-C to the PTY without tearing down the terminal host.
func (r *Runtime) Interrupt(ctx context.Context, handle ports.RuntimeHandle) error {
	sess := r.resolve(handle.ID)
	if sess == nil {
		return fmt.Errorf("conpty: session %q not found", handle.ID)
	}
	return clientSendInput(sess.addr, "\x03")
}

// SendInput writes raw terminal input without appending Enter. It is intended
// for TUI keybindings such as Escape rather than prompt text.
func (r *Runtime) SendInput(ctx context.Context, handle ports.RuntimeHandle, input string) error {
	sess := r.resolve(handle.ID)
	if sess == nil {
		return fmt.Errorf("conpty: session %q not found", handle.ID)
	}
	return clientSendInput(sess.addr, input)
}

// GetOutput returns the last lines lines from the pty-host ring buffer.
func (r *Runtime) GetOutput(ctx context.Context, handle ports.RuntimeHandle, lines int) (string, error) {
	if lines <= 0 {
		return "", fmt.Errorf("conpty: lines must be > 0")
	}
	sess := r.resolve(handle.ID)
	if sess == nil {
		return "", fmt.Errorf("conpty: session %q not found", handle.ID)
	}
	return clientGetOutput(ctx, sess.addr, lines)
}

// GetStyledOutput returns the current rendered ConPTY viewport with ANSI cell
// styles preserved. The pty-host owns the screen model so this remains valid
// across daemon restarts and never substitutes the raw scrollback ring.
func (r *Runtime) GetStyledOutput(ctx context.Context, handle ports.RuntimeHandle, lines int) (string, error) {
	if lines <= 0 {
		return "", fmt.Errorf("conpty: lines must be > 0")
	}
	sess := r.resolve(handle.ID)
	if sess == nil {
		return "", fmt.Errorf("conpty: session %q not found", handle.ID)
	}
	protocolVersion, err := r.resolveHostProtocol(ctx, sess)
	if err != nil {
		return "", err
	}
	if protocolVersion < conPTYStyledOutputProtocolVersion {
		return "", fmt.Errorf("conpty: pty-host protocol version %d: %w",
			protocolVersion, ports.ErrStyledTerminalOutputUnavailable)
	}
	return clientGetStyledOutput(ctx, sess.addr, lines)
}

// resolveHostProtocol negotiates capabilities when a daemon adopts a detached
// pty-host from the recovery registry. Hosts created by this Runtime already
// have a known version. Older hosts omit protocolVersion from MsgStatusRes, so
// they resolve to version zero without receiving an unsupported styled-output
// request or incurring that request's timeout on every drain poll.
func (r *Runtime) resolveHostProtocol(ctx context.Context, sess *hostSession) (int, error) {
	r.mu.Lock()
	if sess.protocolResolved {
		version := sess.protocolVersion
		r.mu.Unlock()
		return version, nil
	}
	r.mu.Unlock()

	status, hostAlive, err := clientStatusContext(ctx, sess.addr)
	if err != nil {
		return 0, fmt.Errorf("conpty: negotiate pty-host protocol: %w", err)
	}
	if !hostAlive {
		return 0, errors.New("conpty: negotiate pty-host protocol: host is not reachable")
	}

	r.mu.Lock()
	if !sess.protocolResolved {
		sess.protocolVersion = status.ProtocolVersion
		sess.protocolResolved = true
	}
	version := sess.protocolVersion
	r.mu.Unlock()
	return version, nil
}

// resolve looks up a session by id: first the in-memory map, then the B2
// registry (for daemon-restart recovery). Returns nil if not found either way.
func (r *Runtime) resolve(id string) *hostSession {
	r.mu.Lock()
	sess := r.sessions[id]
	r.mu.Unlock()
	if sess != nil {
		return sess
	}

	// Registry fallback: scan for the entry by session id.
	entries, err := ptyregistry.List()
	if err != nil {
		return nil
	}
	for _, e := range entries {
		if e.SessionID != id {
			continue
		}
		// Re-populate the map so subsequent calls skip the file scan.
		recovered := &hostSession{addr: e.PipePath, pid: e.PtyHostPID, launchID: e.LaunchID}
		r.mu.Lock()
		// Only store if another goroutine hasn't beaten us.
		if r.sessions[id] == nil {
			r.sessions[id] = recovered
		} else {
			recovered = r.sessions[id]
		}
		r.mu.Unlock()
		return recovered
	}
	return nil
}

// findProcess wraps os.FindProcess to make it swappable in tests.
// ponytail: direct call; no interface needed at this scale.
func findProcess(pid int) (processKiller, error) {
	p, err := osProcessFinder(pid)
	return p, err
}

// processKiller is the subset of *os.Process used by Destroy.
type processKiller interface {
	Kill() error
}

// osProcessFinder is the production implementation; tests may replace it.
// The real defaultOSProcessFinder is in pidalive_unix.go / pidalive_windows.go
// (same files that provide pidAlive).
var osProcessFinder = defaultOSProcessFinder
