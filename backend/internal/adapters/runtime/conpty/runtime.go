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

const (
	runtimeLaunchIDEnv    = "AO_RUNTIME_LAUNCH_ID"
	unresolvedHostAddress = ptyregistry.UnresolvedPipePath
)

// Ensure Runtime satisfies the port at compile time (Attach in attach.go).
var _ ports.Runtime = (*Runtime)(nil)
var _ ports.FencedRuntimeProber = (*Runtime)(nil)
var _ ports.StyledTerminalOutputReader = (*Runtime)(nil)

type runtimeEffectFailure struct {
	err     error
	handle  ports.RuntimeHandle
	effect  ports.RuntimeEffectOutcome
	cleanup ports.RuntimeCleanupOutcome
}

func (e runtimeEffectFailure) Error() string                               { return e.err.Error() }
func (e runtimeEffectFailure) Unwrap() error                               { return e.err }
func (e runtimeEffectFailure) PossibleHandle() ports.RuntimeHandle         { return e.handle }
func (e runtimeEffectFailure) EffectOutcome() ports.RuntimeEffectOutcome   { return e.effect }
func (e runtimeEffectFailure) CleanupOutcome() ports.RuntimeCleanupOutcome { return e.cleanup }

func conptyCreateFailure(err error) error {
	return runtimeEffectFailure{err: err, effect: ports.RuntimeEffectNone, cleanup: ports.RuntimeCleanupNotAttempted}
}

func conptyPartialCreateFailure(err error, handle ports.RuntimeHandle, cleanup ports.RuntimeCleanupOutcome) error {
	return runtimeEffectFailure{err: err, handle: handle, effect: ports.RuntimeEffectPossible, cleanup: cleanup}
}

// validSessionID matches agent-orchestrator's assertValidSessionId.
var validSessionID = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// hostSession is the in-memory state for a live pty-host connection.
type hostSession struct {
	addr             string
	pid              int
	launchID         string
	currentOwner     bool
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

	// UnregisterHost overrides durable reservation cleanup. It exists for
	// manager-level fault-contract tests; nil uses the registry adapter.
	UnregisterHost func(context.Context, string) error
}

// Runtime is the conpty runtime adapter.
type Runtime struct {
	spawner        hostSpawner
	killHost       func(string) error
	pidIsAlive     func(int) bool
	processFinder  func(int) (processKiller, error)
	registerHost   func(context.Context, ptyregistry.Entry) error
	unregisterHost func(context.Context, string) error
	destroyWait    time.Duration
	destroyPoll    time.Duration

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
	unregisterHost := ptyregistry.Unregister
	if opts.UnregisterHost != nil {
		unregisterHost = opts.UnregisterHost
	}
	return &Runtime{
		spawner:        sp,
		killHost:       clientKill,
		pidIsAlive:     pidAlive,
		processFinder:  findProcess,
		registerHost:   ptyregistry.Register,
		unregisterHost: unregisterHost,
		destroyWait:    500 * time.Millisecond,
		destroyPoll:    25 * time.Millisecond,
		sessions:       make(map[string]*hostSession),
	}
}

// Create spawns a detached pty-host for the session, waits for READY, stores
// the addr+pid in-memory and in the B2 registry, and returns the handle.
// Returns an error if sessionID is invalid, already exists, or spawn fails.
func (r *Runtime) Create(ctx context.Context, cfg ports.RuntimeConfig) (ports.RuntimeHandle, error) {
	id := string(cfg.SessionID)
	if !validSessionID.MatchString(id) {
		return ports.RuntimeHandle{}, conptyCreateFailure(fmt.Errorf("conpty: invalid session id %q: must match ^[a-zA-Z0-9_-]+$", id))
	}
	if cfg.WorkspacePath == "" {
		return ports.RuntimeHandle{}, conptyCreateFailure(fmt.Errorf("conpty: workspace path required"))
	}
	if len(cfg.Argv) == 0 {
		return ports.RuntimeHandle{}, conptyCreateFailure(fmt.Errorf("conpty: argv required"))
	}

	r.mu.Lock()
	if _, dup := r.sessions[id]; dup {
		r.mu.Unlock()
		return ports.RuntimeHandle{}, conptyCreateFailure(fmt.Errorf("conpty: session %q already exists; destroy before re-creating", id))
	}
	r.mu.Unlock()
	existing, resolveErr := r.resolveWithEvidence(ctx, id)
	if resolveErr != nil {
		return ports.RuntimeHandle{}, conptyCreateFailure(fmt.Errorf("conpty: inspect existing ownership for %q: %w", id, resolveErr))
	}
	if existing != nil {
		return ports.RuntimeHandle{}, conptyCreateFailure(fmt.Errorf("conpty: session %q already exists; destroy before re-creating", id))
	}

	r.mu.Lock()
	if _, dup := r.sessions[id]; dup {
		r.mu.Unlock()
		return ports.RuntimeHandle{}, conptyCreateFailure(fmt.Errorf("conpty: session %q already exists; destroy before re-creating", id))
	}
	// Reserve both the in-memory slot and the restart registry before spawning.
	// If the daemon crashes after the child starts but before its PID/address
	// update is durable, a fresh daemon must still see unknown ownership rather
	// than exact absence.
	reservation := &hostSession{
		addr: unresolvedHostAddress, launchID: cfg.Env[runtimeLaunchIDEnv], currentOwner: true,
	}
	r.sessions[id] = reservation
	r.mu.Unlock()
	if err := r.registerHost(ctx, ptyregistry.Entry{
		SessionID: id, PtyHostPID: 0, PipePath: unresolvedHostAddress,
		LaunchID: reservation.launchID, RegisteredAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		r.mu.Lock()
		delete(r.sessions, id)
		r.mu.Unlock()
		return ports.RuntimeHandle{}, conptyCreateFailure(fmt.Errorf("conpty: reserve pty-host ownership for %q: %w", id, err))
	}

	addr, pid, err := r.spawner(ctx, id, cfg.WorkspacePath, cfg.Argv, cfg.Env)
	if err != nil {
		cause := fmt.Errorf("conpty: spawn pty-host for %q: %w", id, err)
		handle := ports.RuntimeHandle{ID: id}
		if addr == "" && pid == 0 {
			if unregisterErr := r.unregisterHost(ctx, id); unregisterErr != nil {
				cause = errors.Join(cause, fmt.Errorf("remove unused pty-host reservation for %q: %w", id, unregisterErr))
				// Keep the current-owner reservation in memory when durable
				// cleanup fails. A later Destroy can safely retry unregistering
				// it without spawning or killing any process.
				return ports.RuntimeHandle{}, conptyPartialCreateFailure(cause, handle, ports.RuntimeCleanupFailed)
			}
			r.mu.Lock()
			delete(r.sessions, id)
			r.mu.Unlock()
			return ports.RuntimeHandle{}, conptyCreateFailure(cause)
		}
		if addr == "" && pid > 0 {
			// The child started but its READY address was never observed and the
			// spawner could not prove cleanup. Retain its PID and launch fence in
			// both memory and the restart registry. The sentinel address prevents
			// ordinary client traffic while keeping the possible handle probeable.
			sess := &hostSession{addr: unresolvedHostAddress, pid: pid, launchID: cfg.Env[runtimeLaunchIDEnv], currentOwner: true}
			r.mu.Lock()
			r.sessions[id] = sess
			r.mu.Unlock()
			registryErr := r.registerHost(ctx, ptyregistry.Entry{
				SessionID: id, PtyHostPID: pid, PipePath: unresolvedHostAddress,
				LaunchID: sess.launchID, RegisteredAt: time.Now().UTC().Format(time.RFC3339),
			})
			if registryErr != nil {
				cause = errors.Join(cause, fmt.Errorf("retain unresolved pty-host ownership for %q: %w", id, registryErr))
			}
			return ports.RuntimeHandle{}, conptyPartialCreateFailure(cause, handle, ports.RuntimeCleanupFailed)
		}
		if addr == "" || pid <= 0 {
			// Some effect was reported but cannot be safely addressed and fenced.
			// Keep the prelaunch reservation rather than converting ambiguity to
			// absence; a later exact owner may replace or explicitly clear it.
			return ports.RuntimeHandle{}, conptyPartialCreateFailure(cause, handle, ports.RuntimeCleanupFailed)
		}
		r.mu.Lock()
		r.sessions[id] = &hostSession{addr: addr, pid: pid, launchID: cfg.Env[runtimeLaunchIDEnv], currentOwner: true}
		r.mu.Unlock()
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
		cleanupErr := r.Destroy(cleanupCtx, handle)
		cancel()
		if cleanupErr != nil {
			return ports.RuntimeHandle{}, conptyPartialCreateFailure(errors.Join(cause, cleanupErr), handle, ports.RuntimeCleanupFailed)
		}
		return ports.RuntimeHandle{}, conptyPartialCreateFailure(cause, handle, ports.RuntimeCleanupSucceeded)
	}

	sess := &hostSession{
		addr: addr, pid: pid, launchID: cfg.Env[runtimeLaunchIDEnv], currentOwner: true,
		protocolVersion: conPTYHostProtocolVersion, protocolResolved: true,
	}

	r.mu.Lock()
	r.sessions[id] = sess
	r.mu.Unlock()

	if err := r.registerHost(ctx, ptyregistry.Entry{
		SessionID:    id,
		PtyHostPID:   pid,
		PipePath:     addr, // ponytail: reuse PipePath field for loopback addr
		LaunchID:     sess.launchID,
		RegisteredAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		handle := ports.RuntimeHandle{ID: id}
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
		cleanupErr := r.Destroy(cleanupCtx, handle)
		cancel()
		cause := fmt.Errorf("conpty: register pty-host for %q: %w", id, err)
		if cleanupErr != nil {
			return ports.RuntimeHandle{}, conptyPartialCreateFailure(errors.Join(cause, cleanupErr), handle, ports.RuntimeCleanupFailed)
		}
		return ports.RuntimeHandle{}, conptyPartialCreateFailure(cause, handle, ports.RuntimeCleanupSucceeded)
	}

	return ports.RuntimeHandle{ID: id}, nil
}

// Destroy gracefully kills the pty-host, then force-kills it when necessary.
// The session remains registered until its PID is confirmed gone so callers
// never receive a false-success teardown while a provider may still be alive.
// Unknown/already-gone sessions remain idempotent.
func (r *Runtime) Destroy(ctx context.Context, handle ports.RuntimeHandle) error {
	sess, err := r.resolveWithEvidence(ctx, handle.ID)
	if err != nil {
		return errors.Join(ports.ErrRuntimeProbeInconclusive, fmt.Errorf("conpty: resolve runtime %q for destroy: %w", handle.ID, err))
	}
	if sess == nil {
		return nil // complete registry evidence proves the runtime is already gone
	}
	if sess.addr == unresolvedHostAddress && !sess.currentOwner {
		return fmt.Errorf("conpty: recovered unresolved ownership for %q cannot be safely killed by PID alone: %w", handle.ID, ports.ErrRuntimeProbeInconclusive)
	}

	// Ask a READY host to shut down gracefully (triggers shutdown() in Serve).
	// A startup-unresolved host has no safe address; retain the PID fence and
	// proceed directly to the force-kill path.
	var gracefulErr error
	if sess.addr != unresolvedHostAddress {
		gracefulErr = r.killHost(sess.addr)
	}
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

	if err := r.unregisterHost(ctx, handle.ID); err != nil {
		return fmt.Errorf("conpty: unregister destroyed session %q: %w", handle.ID, err)
	}

	r.mu.Lock()
	delete(r.sessions, handle.ID)
	r.mu.Unlock()
	return nil
}

func (r *Runtime) waitForPIDExit(ctx context.Context, pid int) (bool, error) {
	if pid <= 0 {
		return true, nil
	}
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
	sess, err := r.resolveWithEvidence(ctx, handle.ID)
	if err != nil {
		return false, err
	}
	if sess == nil {
		return false, nil // no in-memory entry, no registry entry -> definitively gone
	}
	if sess.addr == unresolvedHostAddress {
		if sess.pid <= 0 || r.pidIsAlive(sess.pid) {
			return false, fmt.Errorf("conpty: pty-host pid %d started without a READY address: %w", sess.pid, ports.ErrRuntimeProbeInconclusive)
		}
		return false, nil
	}
	return clientIsAlive(sess.addr)
}

// ProbeFencedRuntime returns liveness evidence for the exact fenced runtime identity.
func (r *Runtime) ProbeFencedRuntime(ctx context.Context, ref ports.FencedRuntimeRef) ports.FencedProbeResult {
	if ref.Handle.ID == "" || ref.SessionID == "" || ref.Generation == "" || ref.Handle.ID != string(ref.SessionID) {
		return ports.FencedProbeResult{Liveness: ports.FencedUnknown, Reason: ports.FencedReasonIdentityMissing}
	}
	sess, err := r.resolveWithEvidence(ctx, ref.Handle.ID)
	if err != nil {
		reason := ports.FencedReasonRegistryUnreadable
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			reason = ports.FencedReasonProbeFailed
		} else if errors.Is(err, ptyregistry.ErrRegistryMalformed) {
			reason = ports.FencedReasonRegistryMalformed
		}
		return ports.FencedProbeResult{Liveness: ports.FencedUnknown, Reason: reason}
	}
	if sess == nil {
		return ports.FencedProbeResult{Liveness: ports.FencedDead, Reason: ports.FencedReasonExactAbsent}
	}
	if sess.launchID == "" {
		return ports.FencedProbeResult{Liveness: ports.FencedUnknown, Reason: ports.FencedReasonIdentityMissing}
	}
	if sess.launchID != ref.Generation {
		return ports.FencedProbeResult{Liveness: ports.FencedUnknown, Reason: ports.FencedReasonGenerationMismatch}
	}
	if sess.addr == unresolvedHostAddress {
		if sess.pid <= 0 || r.pidIsAlive(sess.pid) {
			return ports.FencedProbeResult{Liveness: ports.FencedUnknown, Reason: ports.FencedReasonProbeFailed}
		}
		return ports.FencedProbeResult{Liveness: ports.FencedDead, Reason: ports.FencedReasonExactAbsent}
	}
	status, hostAlive, err := clientStatusContext(ctx, sess.addr)
	if err != nil || !hostAlive {
		return ports.FencedProbeResult{Liveness: ports.FencedUnknown, Reason: ports.FencedReasonProbeFailed}
	}
	if status.Alive {
		return ports.FencedProbeResult{Liveness: ports.FencedAlive, Reason: ports.FencedReasonExactMatch}
	}
	return ports.FencedProbeResult{Liveness: ports.FencedDead, Reason: ports.FencedReasonExactAbsent}
}

// IsSupervisedProcessAlive uses the pty-host's child status. For a supervised
// launch that child is the AO supervisor, whose lifetime matches the managed
// agent process. When a generation ref is supplied, the launch id captured at
// Create (and persisted in the recovery registry) must match exactly.
func (r *Runtime) IsSupervisedProcessAlive(ctx context.Context, handle ports.RuntimeHandle, ref ports.SupervisedProcessRef) (bool, error) {
	sess, err := r.resolveWithEvidence(ctx, handle.ID)
	if err != nil {
		return false, fmt.Errorf("conpty: resolve supervised runtime %q: %w", handle.ID, err)
	}
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
	sess, err := r.resolveWithEvidence(ctx, handle.ID)
	if err != nil {
		return fmt.Errorf("conpty: resolve runtime %q for message: %w", handle.ID, err)
	}
	if sess == nil {
		return fmt.Errorf("conpty: session %q not found", handle.ID)
	}
	return clientSendMessage(sess.addr, message)
}

// Interrupt sends Ctrl-C to the PTY without tearing down the terminal host.
func (r *Runtime) Interrupt(ctx context.Context, handle ports.RuntimeHandle) error {
	sess, err := r.resolveWithEvidence(ctx, handle.ID)
	if err != nil {
		return fmt.Errorf("conpty: resolve runtime %q for interrupt: %w", handle.ID, err)
	}
	if sess == nil {
		return fmt.Errorf("conpty: session %q not found", handle.ID)
	}
	return clientSendInput(sess.addr, "\x03")
}

// SendInput writes raw terminal input without appending Enter. It is intended
// for TUI keybindings such as Escape rather than prompt text.
func (r *Runtime) SendInput(ctx context.Context, handle ports.RuntimeHandle, input string) error {
	sess, err := r.resolveWithEvidence(ctx, handle.ID)
	if err != nil {
		return fmt.Errorf("conpty: resolve runtime %q for input: %w", handle.ID, err)
	}
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
	sess, err := r.resolveWithEvidence(ctx, handle.ID)
	if err != nil {
		return "", fmt.Errorf("conpty: resolve runtime %q for output: %w", handle.ID, err)
	}
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
	sess, err := r.resolveWithEvidence(ctx, handle.ID)
	if err != nil {
		return "", fmt.Errorf("conpty: resolve runtime %q for styled output: %w", handle.ID, err)
	}
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

// resolveWithEvidence looks up a session by id: first the in-memory map, then
// the B2 registry (for daemon-restart recovery). It returns nil only when a
// complete, uncancelled registry scan proves the session is absent.
func (r *Runtime) resolveWithEvidence(ctx context.Context, id string) (*hostSession, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.Lock()
	sess, exists := r.sessions[id]
	r.mu.Unlock()
	if sess != nil {
		return sess, nil
	}
	if exists {
		return nil, errors.New("conpty: runtime creation is still unresolved")
	}

	// Registry fallback: scan for the entry by session id.
	entries, complete, err := ptyregistry.Scan(ctx)
	if err != nil {
		return nil, err
	}
	if !complete {
		return nil, errors.New("conpty: pty registry scan incomplete")
	}
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
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
		return recovered, nil
	}
	return nil, nil
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
