// Package tmux implements ports.Runtime using tmux sessions on Darwin/Linux.
package tmux

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/runtime/ptyexec"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/internal/tmuxbin"
)

const (
	defaultTimeout    = 5 * time.Second
	defaultChunkBytes = 16 * 1024
	// defaultEnterDelay mirrors conpty's ptyInputEnterDelay: a pause after pasting
	// a non-empty message, before the trailing Enter, so a large multiline paste
	// does not absorb the Enter and leave the prompt unsubmitted (issue #2342).
	defaultEnterDelay = 300 * time.Millisecond
	// defaultReapGrace is how long Destroy waits between SIGTERM and SIGKILL when
	// reaping a pane's leftover background processes, giving them a chance to
	// exit cleanly (release ports) before being forced (issue #2523). It is a
	// ceiling, not a fixed wait: reapPollInterval decides how soon a pane that
	// is already empty lets Destroy return.
	defaultReapGrace = 5 * time.Second
	// reapPollInterval is how often the reap rechecks for survivors while the
	// grace runs. A plain shell exits within a tick or two, so Destroy returns
	// in roughly this long instead of always burning the full grace — which the
	// DELETE handler blocks on, and the user sees as a tab that will not close.
	reapPollInterval = 50 * time.Millisecond
	tmuxHandlePrefix = "tmux-v1:"
	runtimeLaunchEnv = "AO_RUNTIME_LAUNCH_ID"
	// tmuxObjectFenceMismatch is printed only by the false branch of a guarded
	// tmux command. A constant marker can cause only a conservative false
	// positive if pane output happens to contain it; it can never authorize an
	// action against the wrong server.
	tmuxObjectFenceMismatch = "__AO_TMUX_OBJECT_FENCE_MISMATCH__"
)

var (
	sessionIDPattern     = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)
	tmuxSessionIDPattern = regexp.MustCompile(`^\$\d+$`)
	tmuxPaneIDPattern    = regexp.MustCompile(`^%\d+$`)
)

var getenv = os.Getenv

// Options configures a tmux Runtime. Every field has a sensible default (see
// New), so the zero value is usable.
type Options struct {
	Binary           string        // default configured/bundled/system tmux resolution
	LegacyBinary     string        // default system tmux from PATH when SocketName is set; used only for pre-private-socket sessions
	SocketName       string        // default $AO_TMUX_SOCKET_NAME; empty uses tmux's machine-wide default socket
	LegacySocketPath string        // deterministic absolute -S socket path used by the historical private-socket release
	RunFilePath      string        // current absolute running.json path; enables immutable ownership proof for legacy handles
	Shell            string        // default $SHELL else /bin/sh
	Timeout          time.Duration // default 5s
	ChunkSize        int           // default 16*1024
	EnterDelay       time.Duration // pause after pasting a non-empty message before pressing Enter; default defaultEnterDelay. Conpty already does this (ptyInputEnterDelay); tmux lacked it, so a large multiline paste could absorb the trailing Enter and leave the prompt unsubmitted (issue #2342).
	ReapGrace        time.Duration // grace between SIGTERM and SIGKILL when reaping a pane's leftover background processes on Destroy; default defaultReapGrace.
}

type socketTargetKind string

const (
	socketTargetDefault socketTargetKind = "default"
	socketTargetNamed   socketTargetKind = "named"
	socketTargetPath    socketTargetKind = "path"
)

// socketTarget is the complete namespace provenance needed to address one
// tmux server. It deliberately excludes process-local state: a target encoded
// in a RuntimeHandle must work in a replacement daemon.
type socketTarget struct {
	kind            socketTargetKind
	value           string
	useLegacyBinary bool
}

type qualifiedHandlePayload struct {
	Session       string           `json:"session"`
	Target        socketTargetKind `json:"target"`
	Value         string           `json:"value,omitempty"`
	LegacyBinary  bool             `json:"legacy_binary,omitempty"`
	OwnerSession  string           `json:"owner_session,omitempty"`
	OwnerLaunch   string           `json:"owner_launch,omitempty"`
	TmuxServerPID int              `json:"tmux_server_pid,omitempty"`
	TmuxSessionID string           `json:"tmux_session_id,omitempty"`
	TmuxPaneID    string           `json:"tmux_pane_id,omitempty"`
}

type runtimeRoute struct {
	id            string
	target        socketTarget
	qualified     bool
	owner         ports.SupervisedProcessRef
	tmuxServerPID int
	tmuxSessionID string
	tmuxPaneID    string
}

func (route runtimeRoute) hasObjectFence() bool {
	return route.tmuxServerPID > 1 && route.tmuxSessionID != "" && route.tmuxPaneID != ""
}

func (route runtimeRoute) actionSessionTarget() string {
	if route.tmuxSessionID != "" {
		return route.tmuxSessionID
	}
	return route.id
}

func (route runtimeRoute) actionPaneTarget() string {
	if route.tmuxPaneID != "" {
		return route.tmuxPaneID
	}
	return route.id
}

// Runtime runs agent sessions inside tmux sessions, driving them via the tmux
// CLI. It implements ports.Runtime.
type Runtime struct {
	binary           string
	legacyBinary     string
	socketName       string
	legacySocketPath string
	runFilePath      string
	shell            string
	timeout          time.Duration
	chunkSize        int
	enterDelay       time.Duration
	reapGrace        time.Duration
	runner           runner
	reapSessions     func(ctx context.Context, pids []int, grace time.Duration)
	spawnAttach      func(ctx context.Context, argv, env []string, rows, cols uint16) (ports.Stream, error)
}

var _ ports.Runtime = (*Runtime)(nil)
var _ ports.FencedRuntimeProber = (*Runtime)(nil)
var _ ports.Attacher = (*Runtime)(nil)
var _ ports.RuntimeHandleResolver = (*Runtime)(nil)
var _ ports.ExactRuntimeHandleResolver = (*Runtime)(nil)
var _ ports.RuntimeIdentityInspector = (*Runtime)(nil)

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

func tmuxCreateFailure(err error) error {
	return runtimeEffectFailure{err: err, effect: ports.RuntimeEffectNone, cleanup: ports.RuntimeCleanupNotAttempted}
}

func tmuxPossibleCreateFailure(err error, handle ports.RuntimeHandle, cleanup ports.RuntimeCleanupOutcome) error {
	return runtimeEffectFailure{err: err, handle: handle, effect: ports.RuntimeEffectPossible, cleanup: cleanup}
}

type runner interface {
	Run(ctx context.Context, env []string, name string, args ...string) ([]byte, error)
}

// killSessionsByPID force-terminates every process in each pid's tmux pane
// session. tmux runs each pane in its own session (pane pid == session id), so
// signaling the session reaps the pane's background children — e.g. a dev
// server a worker started with `&` — that `kill-session`'s SIGHUP leaves
// running. It SIGTERMs, waits grace for a clean exit, then
// SIGKILLs survivors. Best-effort: `pkill` is absent on Windows, where tmux is
// never the runtime, so the calls simply no-op there.
func killSessionsByPID(ctx context.Context, pids []int, grace time.Duration) {
	reapPaneSessions(ctx, pids, grace, signalSessions, sessionsHaveProcesses)
}

// reapPaneSessions is killSessionsByPID's logic with the pkill/pgrep calls
// injected, so the SIGTERM → wait → SIGKILL sequence is testable without real
// processes.
func reapPaneSessions(
	ctx context.Context,
	pids []int,
	grace time.Duration,
	signal func(ctx context.Context, pids []int, sig string) bool,
	hasProcesses func(ctx context.Context, pids []int) bool,
) {
	if len(pids) == 0 {
		return
	}
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), grace+5*time.Second)
	defer cancel()

	// `-s` is a Linux procps extension; BSD/macOS pkill rejects it outright. When
	// the platform cannot signal by session id, no amount of waiting reaps
	// anything — the SIGTERM never landed and the SIGKILL would not either — so
	// return instead of blocking the caller for the whole grace. Destroy runs
	// inside the shell-terminal DELETE handler, and that dead wait was the
	// several-second delay users saw when closing a terminal on macOS.
	if !signal(cleanupCtx, pids, "-TERM") {
		return
	}
	if !hasProcesses(cleanupCtx, pids) {
		return
	}

	// Poll rather than sleep the whole grace. Callers block on this (Destroy runs
	// inside the shell-terminal DELETE handler), and the common case — an
	// interactive shell with nothing behind it — is empty almost immediately. A
	// process that really needs the time still gets the full grace before SIGKILL.
	deadline := time.NewTimer(grace)
	defer deadline.Stop()
	ticker := time.NewTicker(reapPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-cleanupCtx.Done():
			return
		case <-ticker.C:
			if !hasProcesses(cleanupCtx, pids) {
				return
			}
		case <-deadline.C:
			if !hasProcesses(cleanupCtx, pids) {
				return
			}
			signal(cleanupCtx, pids, "-KILL")
			return
		}
	}
}

// signalSessions sends a pkill signal flag (e.g. "-TERM") to every process in
// each pane session, matched by session id via `pkill -s`. It reports whether
// the platform supports signalling by session id at all: exit 2 is a usage
// error on both procps and BSD pkill, which is how macOS answers `-s`, and
// there the call reaches no process.
func signalSessions(ctx context.Context, pids []int, sig string) bool {
	supported := false
	for _, pid := range pids {
		err := exec.CommandContext(ctx, "pkill", sig, "-s", strconv.Itoa(pid)).Run()
		if !isUnsupportedMatcher(err) {
			supported = true
		}
	}
	return supported
}

// isUnsupportedMatcher reports whether a pgrep/pkill invocation failed because
// the platform rejects the matcher itself (exit 2, a usage error) rather than
// because nothing matched (exit 1) or the process is missing entirely.
func isUnsupportedMatcher(err error) bool {
	if err == nil {
		return false
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode() >= 2
	}
	// pkill/pgrep absent (Windows, minimal containers): equally unusable.
	return true
}

// sessionsHaveProcesses reports whether any process remains in the pane
// sessions. `pgrep` exit 1 means no matches; other failures are treated as
// survivors so Destroy stays conservative and still attempts SIGKILL.
func sessionsHaveProcesses(ctx context.Context, pids []int) bool {
	for _, pid := range pids {
		err := exec.CommandContext(ctx, "pgrep", "-s", strconv.Itoa(pid)).Run()
		if err == nil || ctx.Err() != nil {
			return true
		}
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
			return true
		}
	}
	return false
}

type execRunner struct{}

func (execRunner) Run(ctx context.Context, env []string, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(append([]string(nil), os.Environ()...), env...)
	// Run from a stable directory, not whatever the daemon process's cwd happens
	// to be. The first tmux CLI call auto-starts tmux's persistent server, which
	// inherits ITS launching process's cwd and keeps it for the server's entire
	// lifetime, regardless of what any later `new-session -c <dir>` asks for
	// (issue #2775). A packaged desktop build can start the daemon with its cwd
	// inside a Squirrel/ShipIt staging directory that the very next auto-update
	// deletes, permanently pinning the tmux server to a path that no longer
	// exists. os.TempDir() outlives app bundle swaps and update staging dirs, so
	// pinning here keeps the server cwd valid across the app's lifetime.
	cmd.Dir = stableRunDir()
	return cmd.CombinedOutput()
}

// stableRunDir returns the directory execRunner.Run pins the tmux CLI to.
//
// os.TempDir() is the preferred answer (see execRunner.Run), but it returns
// $TMPDIR verbatim without checking that it exists. A stale or bogus TMPDIR
// would then make exec fail with "chdir <dir>: no such file or directory" on
// EVERY tmux command, taking the whole runtime down for exactly the reason
// #2775 did: a cwd that no longer exists. So stat the candidates and degrade
// rather than hard-fail. The last resort is the empty string, which leaves
// cmd.Dir unset so the command inherits the daemon's own cwd: that is the
// pre-fix behavior and merely risks the poisoned-server race the pin avoids,
// which the retry in verifyPaneWorkingDirectory already tolerates.
func stableRunDir() string {
	candidates := []string{os.TempDir()}
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates, home)
	}
	for _, dir := range candidates {
		if dir == "" {
			continue
		}
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			return dir
		}
	}
	return ""
}

// New builds a tmux Runtime, filling unset Options with defaults: binary from
// AO's configured/bundled/system resolver; shell from $SHELL (else /bin/sh); and the
// default timeout and output chunk size.
func New(opts Options) *Runtime {
	binary := opts.Binary
	if binary == "" {
		resolution, err := tmuxbin.Resolve()
		if err == nil {
			binary = resolution.Path
		} else if configured := strings.TrimSpace(getenv("AO_TMUX_BINARY")); configured != "" {
			// Keep the configured path on failure so packaged builds fail closed
			// when they eventually execute it instead of selecting machine tmux.
			binary = configured
		} else {
			binary = "tmux"
		}
	}
	timeout := opts.Timeout
	if timeout == 0 {
		timeout = defaultTimeout
	}
	shellPath := opts.Shell
	if shellPath == "" {
		shellPath = getenv("SHELL")
	}
	if shellPath == "" {
		shellPath = "/bin/sh"
	}
	chunkSize := opts.ChunkSize
	if chunkSize <= 0 {
		chunkSize = defaultChunkBytes
	}
	enterDelay := opts.EnterDelay
	if enterDelay <= 0 {
		enterDelay = defaultEnterDelay
	}
	reapGrace := opts.ReapGrace
	if reapGrace <= 0 {
		reapGrace = defaultReapGrace
	}
	socketName := strings.TrimSpace(opts.SocketName)
	if socketName == "" {
		socketName = strings.TrimSpace(getenv("AO_TMUX_SOCKET_NAME"))
	}
	legacyBinary := opts.LegacyBinary
	if socketName == "" {
		legacyBinary = binary
	} else if legacyBinary == "" {
		// Sessions created before AO introduced its private socket were started by
		// the machine tmux from PATH. Use that matching client for the legacy
		// default socket: tmux's client/server protocol is not guaranteed across
		// versions, so AO's pinned bundled client may be unable to adopt them.
		if systemTmux, err := exec.LookPath("tmux"); err == nil {
			legacyBinary = systemTmux
		}
	}
	return &Runtime{
		binary:           binary,
		legacyBinary:     legacyBinary,
		socketName:       socketName,
		legacySocketPath: strings.TrimSpace(opts.LegacySocketPath),
		runFilePath:      strings.TrimSpace(opts.RunFilePath),
		shell:            shellPath,
		timeout:          timeout,
		chunkSize:        chunkSize,
		enterDelay:       enterDelay,
		reapGrace:        reapGrace,
		runner:           execRunner{},
		reapSessions:     killSessionsByPID,
		spawnAttach:      ptyexec.Spawn,
	}
}

// Create starts a new tmux session in the workspace, running the agent's
// launch command with a keep-alive shell, and returns a handle to it.
func (r *Runtime) Create(ctx context.Context, cfg ports.RuntimeConfig) (ports.RuntimeHandle, error) {
	id, err := tmuxSessionName(cfg.SessionID)
	if err != nil {
		return ports.RuntimeHandle{}, tmuxCreateFailure(err)
	}
	if cfg.WorkspacePath == "" {
		return ports.RuntimeHandle{}, tmuxCreateFailure(errors.New("tmux runtime: workspace path is required"))
	}
	if len(cfg.Argv) == 0 {
		return ports.RuntimeHandle{}, tmuxCreateFailure(errors.New("tmux runtime: launch command is required"))
	}
	if err := validateEnvKeys(cfg.Env); err != nil {
		return ports.RuntimeHandle{}, tmuxCreateFailure(err)
	}

	launchCmd := buildLaunchCommand(cfg)
	args := newSessionArgs(id, cfg.WorkspacePath, r.shell, launchCmd)
	createdOut, err := r.run(ctx, args...)
	if err != nil {
		return ports.RuntimeHandle{}, tmuxPossibleCreateFailure(
			fmt.Errorf("tmux runtime: create session %s: %w", id, err),
			ports.RuntimeHandle{ID: id},
			ports.RuntimeCleanupNotAttempted,
		)
	}
	target := r.primarySocketTarget()
	evidence, err := r.parsePaneIdentityEvidence(string(createdOut), cfg.SessionID, target)
	if err != nil {
		// new-session's -P output is the only race-free identity of the server
		// that accepted creation. Without it, even cleanup by name could hit a
		// replacement server, so fail closed and leave cleanup to reconciliation.
		return ports.RuntimeHandle{}, tmuxPossibleCreateFailure(
			fmt.Errorf("tmux runtime: capture created session %s: %w", id, err),
			ports.RuntimeHandle{ID: id},
			ports.RuntimeCleanupNotAttempted,
		)
	}
	route := runtimeRoute{
		id:            id,
		target:        target,
		qualified:     true,
		tmuxServerPID: evidence.tmuxServerPID,
		tmuxSessionID: evidence.tmuxSessionID,
		tmuxPaneID:    evidence.tmuxPaneID,
	}
	launchID := strings.TrimSpace(cfg.Env[runtimeLaunchEnv])
	if launchID != "" {
		if evidence.launchID != launchID || (r.runFilePath != "" && !evidence.identity.OwnershipProven) {
			cleanupHandle, encodeErr := qualifiedRuntimeHandleForRoute(route)
			if encodeErr == nil {
				return ports.RuntimeHandle{}, r.failedCreatedRuntime(cleanupHandle, fmt.Errorf(
					"%w: created tmux pane %s did not retain its launch provenance",
					ports.ErrRuntimeProbeInconclusive,
					id,
				))
			}
			return ports.RuntimeHandle{}, tmuxPossibleCreateFailure(
				errors.Join(
					fmt.Errorf("%w: created tmux pane %s did not retain its launch provenance", ports.ErrRuntimeProbeInconclusive, id),
					encodeErr,
				),
				ports.RuntimeHandle{ID: id},
				ports.RuntimeCleanupNotAttempted,
			)
		}
		route.owner = ports.SupervisedProcessRef{
			SessionID: cfg.SessionID,
			LaunchID:  launchID,
		}
	}
	handle, err := qualifiedRuntimeHandleForRoute(route)
	if err != nil {
		return ports.RuntimeHandle{}, tmuxPossibleCreateFailure(err, ports.RuntimeHandle{ID: id}, ports.RuntimeCleanupNotAttempted)
	}
	if err := r.verifyPaneWorkingDirectoryOnRoute(ctx, route, cfg.WorkspacePath); err != nil {
		return ports.RuntimeHandle{}, r.failedCreatedRuntime(handle, err)
	}

	// Hide the status bar in the embedded terminal: it clutters the view and
	// was not designed for the in-browser display context.
	if _, err := r.runActionOnRoute(ctx, route, setStatusOffArgs(route.actionSessionTarget())...); err != nil {
		return ports.RuntimeHandle{}, r.failedCreatedRuntime(handle, fmt.Errorf("tmux runtime: set status %s: %w", id, err))
	}

	// Enable mouse mode so the embedded terminal's SGR wheel reports scroll the
	// pane (see setMouseOnArgs). Without it, wheel scrolling silently no-ops.
	if _, err := r.runActionOnRoute(ctx, route, setMouseOnArgs(route.actionSessionTarget())...); err != nil {
		return ports.RuntimeHandle{}, r.failedCreatedRuntime(handle, fmt.Errorf("tmux runtime: set mouse %s: %w", id, err))
	}

	// Size the shared window to the largest attached client, not the most recent
	// one, so a small secondary viewer (e.g. the phone) can't strip down a larger
	// client's view (see setWindowSizeLargestArgs).
	if _, err := r.runActionOnRoute(ctx, route, setWindowSizeLargestArgs(route.actionSessionTarget())...); err != nil {
		return ports.RuntimeHandle{}, r.failedCreatedRuntime(handle, fmt.Errorf("tmux runtime: set window-size %s: %w", id, err))
	}

	alive, err := r.IsAlive(ctx, handle)
	if err != nil {
		return ports.RuntimeHandle{}, r.failedCreatedRuntime(handle, fmt.Errorf("tmux runtime: verify session %s: %w", id, err))
	}
	if !alive {
		return ports.RuntimeHandle{}, r.failedCreatedRuntime(handle, fmt.Errorf("tmux runtime: session %s exited before ready", id))
	}
	return handle, nil
}

func (r *Runtime) failedCreatedRuntime(handle ports.RuntimeHandle, cause error) error {
	if cleanupErr := r.Destroy(context.Background(), handle); cleanupErr != nil {
		return tmuxPossibleCreateFailure(errors.Join(cause, cleanupErr), handle, ports.RuntimeCleanupFailed)
	}
	return tmuxPossibleCreateFailure(cause, handle, ports.RuntimeCleanupSucceeded)
}

// Restart replaces the command in an existing pane while preserving the tmux
// session. This is used to resume an exited agent without discarding terminal
// history or forcing attached clients onto a new handle.
func (r *Runtime) Restart(ctx context.Context, handle ports.RuntimeHandle, cfg ports.RuntimeConfig) (ports.RuntimeHandle, error) {
	route, err := decodeRuntimeHandle(handle)
	if err != nil {
		return ports.RuntimeHandle{}, err
	}
	id := route.id
	expectedID, err := tmuxSessionName(cfg.SessionID)
	if err != nil {
		return ports.RuntimeHandle{}, err
	}
	if expectedID != id {
		return ports.RuntimeHandle{}, fmt.Errorf("tmux runtime: restart handle %s does not match session %s", id, cfg.SessionID)
	}
	if cfg.WorkspacePath == "" {
		return ports.RuntimeHandle{}, errors.New("tmux runtime: workspace path is required")
	}
	if len(cfg.Argv) == 0 {
		return ports.RuntimeHandle{}, errors.New("tmux runtime: launch command is required")
	}
	if err := validateEnvKeys(cfg.Env); err != nil {
		return ports.RuntimeHandle{}, err
	}
	newLaunchID := strings.TrimSpace(cfg.Env[runtimeLaunchEnv])
	if route.qualified && route.owner.SessionID != "" && newLaunchID == "" {
		return ports.RuntimeHandle{}, fmt.Errorf(
			"%w: owner-fenced tmux restart requires %s",
			ports.ErrRuntimeProbeInconclusive,
			runtimeLaunchEnv,
		)
	}
	route, err = r.routeForOperation(ctx, handle)
	if err != nil {
		return ports.RuntimeHandle{}, err
	}

	launchCmd := buildLaunchCommand(cfg)
	if !route.hasObjectFence() {
		if newLaunchID != "" {
			return ports.RuntimeHandle{}, fmt.Errorf(
				"%w: legacy tmux restart requires canonicalization before changing launch generation",
				ports.ErrRuntimeProbeInconclusive,
			)
		}
		if _, err := r.runOnSocket(ctx, route.target, respawnPaneArgs(route.actionPaneTarget(), cfg.WorkspacePath, r.shell, launchCmd)...); err != nil {
			return ports.RuntimeHandle{}, fmt.Errorf("tmux runtime: restart session %s: %w", id, err)
		}
		alive, err := r.IsAlive(ctx, handle)
		if err != nil {
			return ports.RuntimeHandle{}, fmt.Errorf("tmux runtime: verify restarted session %s: %w", id, err)
		}
		if !alive {
			return ports.RuntimeHandle{}, fmt.Errorf("tmux runtime: session %s exited during restart", id)
		}
		return handle, nil
	}
	restartedOut, err := r.runFencedCommands(
		ctx,
		route,
		respawnPaneArgs(route.actionPaneTarget(), cfg.WorkspacePath, r.shell, launchCmd),
		paneStartCommandsArgs(route.actionSessionTarget()),
	)
	if err != nil {
		return ports.RuntimeHandle{}, fmt.Errorf("tmux runtime: restart session %s: %w", id, err)
	}
	evidence, err := r.parsePaneIdentityEvidence(string(restartedOut), cfg.SessionID, route.target)
	if err != nil {
		return ports.RuntimeHandle{}, fmt.Errorf("tmux runtime: inspect restarted session %s: %w", id, err)
	}
	if evidence.tmuxServerPID != route.tmuxServerPID ||
		evidence.tmuxSessionID != route.tmuxSessionID || evidence.tmuxPaneID != route.tmuxPaneID {
		return ports.RuntimeHandle{}, fmt.Errorf(
			"%w: tmux object identity changed while restarting session %s",
			ports.ErrRuntimeProbeInconclusive,
			id,
		)
	}
	restartedRoute := route.withPaneIdentity(evidence)
	if newLaunchID != "" {
		if evidence.launchID != newLaunchID || (r.runFilePath != "" && !evidence.identity.OwnershipProven) {
			return ports.RuntimeHandle{}, fmt.Errorf(
				"%w: restarted tmux pane %s did not retain its launch provenance",
				ports.ErrRuntimeProbeInconclusive,
				id,
			)
		}
		restartedRoute.owner = ports.SupervisedProcessRef{
			SessionID: cfg.SessionID,
			LaunchID:  newLaunchID,
		}
	}
	restartedHandle, err := qualifiedRuntimeHandleForRoute(restartedRoute)
	if err != nil {
		return ports.RuntimeHandle{}, err
	}
	alive, err := r.IsAlive(ctx, restartedHandle)
	if err != nil {
		return ports.RuntimeHandle{}, fmt.Errorf("tmux runtime: verify restarted session %s: %w", id, err)
	}
	if !alive {
		return ports.RuntimeHandle{}, fmt.Errorf("tmux runtime: session %s exited during restart", id)
	}
	return restartedHandle, nil
}

// paneCwdVerifyAttempts and paneCwdVerifyRetryDelay bound how long Create
// waits for the pane's working directory to settle before giving up.
// buildLaunchCommand's `cd '<workspace>' || exit;` guard corrects a pane that
// started in the tmux server's own (possibly poisoned) cwd, but only once the
// pane's shell actually runs that cd. Measured live on 2026-07-25:
// #{pane_current_path} sampled immediately after `new-session` was stale, and
// the same probe sampled 50ms later was already correct. A single-shot check
// therefore lost that race every time and turned a spawn that was actually
// going to succeed into a hard failure (issue #2775): retrying gives the cd
// guard the moment it needs to run.
const (
	paneCwdVerifyAttempts   = 5
	paneCwdVerifyRetryDelay = 50 * time.Millisecond
)

func (r *Runtime) verifyPaneWorkingDirectory(ctx context.Context, id, want string) error {
	return r.verifyPaneWorkingDirectoryOnRoute(ctx, runtimeRoute{
		id:     id,
		target: r.primarySocketTarget(),
	}, want)
}

func (r *Runtime) verifyPaneWorkingDirectoryOnRoute(ctx context.Context, route runtimeRoute, want string) error {
	id := route.id
	var lastErr error
	for attempt := 0; attempt < paneCwdVerifyAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(paneCwdVerifyRetryDelay):
			}
		}
		out, err := r.runActionOnRoute(ctx, route, paneCurrentPathArgs(route.actionPaneTarget())...)
		if err != nil {
			// A later transient probe failure (e.g. a one-off tmux CLI hiccup)
			// must not overwrite an already-observed cwd mismatch: the mismatch
			// is the classifiable, actionable error toAPIError maps via
			// ports.ErrRuntimeWorkspaceCwdMismatch (Fix 4), and losing it here
			// would silently regress that mapping back to a bare, unclassifiable
			// 500 whenever the very last attempt happened to hit a probe error.
			if !errors.Is(lastErr, ports.ErrRuntimeWorkspaceCwdMismatch) {
				lastErr = fmt.Errorf("tmux runtime: verify working directory %s: %w", id, err)
			}
			continue
		}
		got := strings.TrimSpace(string(out))
		if sameDirectory(got, want) {
			return nil
		}
		lastErr = fmt.Errorf(
			"%w: session %s started in %q, want %q (the worktree may be missing, or the tmux server may be pinned to a stale directory)",
			ports.ErrRuntimeWorkspaceCwdMismatch, id, got, want,
		)
	}
	return lastErr
}

// Destroy kills the handle's tmux session and reaps the pane processes it
// leaves behind. `tmux kill-session` only SIGHUPs each pane's foreground
// process, so a worker's backgrounded children (e.g. a dev server started with
// `&`, later reparented to init) survive it and hold their ports indefinitely
// (issue #2523). To catch those, Destroy records each pane's session id before
// teardown and, after kill-session, signals the whole session (see
// killSessionsByPID). An already-gone session is treated as success (idempotent).
func (r *Runtime) Destroy(ctx context.Context, handle ports.RuntimeHandle) error {
	route, found, err := r.resolveRouteForOperation(ctx, handle)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	if route.hasObjectFence() {
		out, guardedErr := r.runFencedCommands(
			ctx,
			route,
			listPanePIDsArgs(route.actionSessionTarget()),
			killSessionArgs(route.actionSessionTarget()),
		)
		sessionIDs := paneSessionIDsFromOutput(string(out), route)
		// The listed pids came from the same guarded server command queue as the
		// kill. They remain safe to reap even if kill-session itself reports an
		// error after the identity check.
		r.reapSessions(ctx, sessionIDs, r.reapGrace)
		if guardedErr != nil {
			return fmt.Errorf("tmux runtime: destroy session %s: %w", route.id, guardedErr)
		}
		return nil
	}
	// Capture pane session ids while the session still exists; a missing
	// session lists no panes and reaps nothing. Best-effort: failures here must
	// not block the kill-session below.
	sessionIDs := r.paneSessionIDsOnRoute(ctx, route)

	out, err := r.runOnSocket(ctx, route.target, killSessionArgs(route.actionSessionTarget())...)
	// Reap regardless of the kill-session result: orphaned children outlive the
	// session, so they must be cleaned up even when the session was already
	// gone (a benign double-kill).
	r.reapSessions(ctx, sessionIDs, r.reapGrace)

	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && killSessionMissingOutput(string(out)) {
			return nil
		}
		return fmt.Errorf("tmux runtime: destroy session %s: %w", route.id, err)
	}
	return nil
}

// paneSessionIDsOnRoute returns a pane pid only when tmux reports the same
// session and pane object ids captured with owner provenance. tmux launches a
// pane in its own session (setsid), so its pid is also the process-session id
// killSessionsByPID uses to reap descendants. Any error, identity mismatch, or
// unparseable row yields no id; pids <= 1 are skipped so init is never signaled.
func (r *Runtime) paneSessionIDsOnRoute(ctx context.Context, route runtimeRoute) []int {
	out, err := r.runOnSocket(ctx, route.target, listPanePIDsArgs(route.actionSessionTarget())...)
	if err != nil {
		return nil
	}
	return paneSessionIDsFromOutput(string(out), route)
}

func paneSessionIDsFromOutput(output string, route runtimeRoute) []int {
	var ids []int
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Split(strings.TrimSpace(line), "\t")
		if len(fields) != 4 {
			continue
		}
		serverPID, serverErr := strconv.Atoi(fields[0])
		if serverErr != nil || serverPID <= 1 ||
			(route.tmuxServerPID != 0 && serverPID != route.tmuxServerPID) ||
			(route.tmuxSessionID != "" && fields[1] != route.tmuxSessionID) ||
			(route.tmuxPaneID != "" && fields[2] != route.tmuxPaneID) ||
			!tmuxSessionIDPattern.MatchString(fields[1]) ||
			!tmuxPaneIDPattern.MatchString(fields[2]) {
			continue
		}
		pid, convErr := strconv.Atoi(fields[3])
		if convErr != nil || pid <= 1 {
			continue
		}
		ids = append(ids, pid)
	}
	return ids
}

// IsAlive reports whether the handle's session still exists via `tmux
// has-session`. Exit 0 means alive. A non-zero exit with output naming this
// session as missing is a definitive false, nil. A conclusively absent server
// wraps ports.ErrRuntimeUnavailable so recovery may recreate it. A transient
// connection or protocol/client failure wraps ErrRuntimeProbeInconclusive so
// no caller can treat a possibly-live session as absent. Any other non-zero
// exit is a plain probe error, which is likewise never per-session death.
func (r *Runtime) IsAlive(ctx context.Context, handle ports.RuntimeHandle) (bool, error) {
	decoded, err := decodeRuntimeHandle(handle)
	if err != nil {
		return false, err
	}
	requiresResolution := decoded.qualified ||
		(!decoded.qualified && (len(r.legacySocketTargets()) > 1 || r.runFilePath != ""))
	if requiresResolution {
		_, found, resolveErr := r.resolveRouteForOperation(ctx, handle)
		return found, resolveErr
	}
	route, found, err := r.resolveRouteForOperation(ctx, handle)
	if err != nil {
		return false, err
	}
	if !found {
		return false, nil
	}
	out, err := r.runOnSocket(ctx, route.target, hasSessionArgs(route.id)...)
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			if sessionMissingOutput(string(out)) {
				return false, nil
			}
			if serverNotRunningOutput(string(out)) {
				return false, fmt.Errorf("tmux runtime: probe session %s: %w: %s",
					route.id, ports.ErrRuntimeUnavailable, strings.TrimSpace(string(out)))
			}
			if transientServerFailureOutput(string(out)) {
				return false, fmt.Errorf("tmux runtime: probe session %s: %w: %s",
					route.id, ports.ErrRuntimeProbeInconclusive, strings.TrimSpace(string(out)))
			}
		}
		return false, fmt.Errorf("tmux runtime: probe session %s: %w", route.id, err)
	}
	return true, nil
}

// ProbeFencedRuntime returns liveness evidence for the exact fenced runtime identity.
func (r *Runtime) ProbeFencedRuntime(ctx context.Context, ref ports.FencedRuntimeRef) ports.FencedProbeResult {
	if ref.Handle.ID == "" || ref.SessionID == "" || strings.TrimSpace(ref.Generation) == "" || ref.Handle.ID != string(ref.SessionID) {
		return ports.FencedProbeResult{Liveness: ports.FencedUnknown, Reason: ports.FencedReasonIdentityMissing}
	}
	alive, err := r.IsAlive(ctx, ref.Handle)
	if err != nil {
		return ports.FencedProbeResult{Liveness: ports.FencedUnknown, Reason: ports.FencedReasonProbeFailed}
	}
	if !alive {
		return ports.FencedProbeResult{Liveness: ports.FencedDead, Reason: ports.FencedReasonExactAbsent}
	}
	entries, panePID, err := r.supervisedProcessTree(ctx, ref.Handle)
	if err != nil {
		return ports.FencedProbeResult{Liveness: ports.FencedUnknown, Reason: ports.FencedReasonProbeFailed}
	}
	descendants := descendantPIDs(entries, panePID)
	exactSupervisorFound := false
	for _, entry := range entries {
		if entry.pid == panePID || !descendants[entry.pid] || !isAnySupervisorCommand(entry.command) {
			continue
		}
		if isSupervisorCommand(entry.command, string(ref.SessionID), ref.Generation) {
			exactSupervisorFound = true
			continue
		}
		return ports.FencedProbeResult{Liveness: ports.FencedUnknown, Reason: ports.FencedReasonGenerationMismatch}
	}
	if exactSupervisorFound {
		if containsExactSupervisedWorkload(entries, panePID, string(ref.SessionID), ref.Generation) {
			return ports.FencedProbeResult{Liveness: ports.FencedAlive, Reason: ports.FencedReasonExactMatch}
		}
		return ports.FencedProbeResult{Liveness: ports.FencedDead, Reason: ports.FencedReasonExactAbsent}
	}
	// A live pane without the exact AO supervisor may contain a workload that a
	// user manually relaunched from the preserved shell. That is not proof of
	// the requested generation, but it is also not proof that the pane is dead.
	return ports.FencedProbeResult{Liveness: ports.FencedUnknown, Reason: ports.FencedReasonIdentityMissing}
}

// IsSupervisedProcessAlive reports whether the managed workload for ref is
// still a descendant of this tmux pane. The initial launch is identified by
// its exact AO supervisor. After that supervisor exits and leaves the
// interactive shell behind, a child launched from that shell is treated as a
// manually resumed workload. Command failures remain inconclusive.
func (r *Runtime) IsSupervisedProcessAlive(ctx context.Context, handle ports.RuntimeHandle, ref ports.SupervisedProcessRef) (bool, error) {
	entries, panePID, err := r.supervisedProcessTree(ctx, handle)
	if err != nil {
		return false, err
	}
	return containsManagedWorkload(entries, panePID, string(ref.SessionID), ref.LaunchID), nil
}

// IsExactSupervisedProcessAlive reports only the AO supervisor matching ref
// while that supervisor still owns a live managed child. It deliberately
// excludes both the manual-child fallback used by the ordinary reaper probe
// and a supervisor that is merely waiting to durably report its child's exit:
// neither is proof that an agent can safely receive a continuation.
func (r *Runtime) IsExactSupervisedProcessAlive(ctx context.Context, handle ports.RuntimeHandle, ref ports.SupervisedProcessRef) (bool, error) {
	if ref.SessionID == "" || strings.TrimSpace(ref.LaunchID) == "" {
		return false, errors.New("tmux runtime: exact supervisor session and launch are required")
	}
	entries, panePID, err := r.supervisedProcessTree(ctx, handle)
	if err != nil {
		return false, err
	}
	return containsExactSupervisedWorkload(entries, panePID, string(ref.SessionID), ref.LaunchID), nil
}

func (r *Runtime) supervisedProcessTree(ctx context.Context, handle ports.RuntimeHandle) ([]processEntry, int, error) {
	route, err := r.routeForOperation(ctx, handle)
	if err != nil {
		return nil, 0, err
	}
	return r.supervisedProcessTreeOnTarget(ctx, route)
}

func (r *Runtime) isExactSupervisedProcessAliveOnTarget(
	ctx context.Context,
	id string,
	target socketTarget,
	tmuxServerPID int,
	tmuxSessionID string,
	tmuxPaneID string,
	ref ports.SupervisedProcessRef,
) (bool, error) {
	entries, panePID, err := r.supervisedProcessTreeOnTarget(ctx, runtimeRoute{
		id:            id,
		target:        target,
		tmuxServerPID: tmuxServerPID,
		tmuxSessionID: tmuxSessionID,
		tmuxPaneID:    tmuxPaneID,
	})
	if err != nil {
		return false, err
	}
	return containsExactSupervisedWorkload(entries, panePID, string(ref.SessionID), ref.LaunchID), nil
}

func (r *Runtime) supervisedProcessTreeOnTarget(
	ctx context.Context,
	route runtimeRoute,
) ([]processEntry, int, error) {
	id := route.id
	paneOut, err := r.runActionOnRoute(ctx, route, panePIDArgs(route.actionPaneTarget())...)
	if err != nil {
		return nil, 0, fmt.Errorf("tmux runtime: inspect pane pid %s: %w", id, err)
	}
	fields := strings.Split(strings.TrimSpace(string(paneOut)), "\t")
	if len(fields) != 4 {
		return nil, 0, fmt.Errorf("tmux runtime: pane identity changed for %s", id)
	}
	serverPID, serverErr := strconv.Atoi(fields[0])
	if serverErr != nil || serverPID <= 1 ||
		(route.tmuxServerPID != 0 && serverPID != route.tmuxServerPID) ||
		(route.tmuxSessionID != "" && fields[1] != route.tmuxSessionID) ||
		(route.tmuxPaneID != "" && fields[2] != route.tmuxPaneID) ||
		!tmuxSessionIDPattern.MatchString(fields[1]) ||
		!tmuxPaneIDPattern.MatchString(fields[2]) {
		return nil, 0, fmt.Errorf("tmux runtime: pane identity changed for %s", id)
	}
	panePID, err := strconv.Atoi(fields[3])
	if err != nil || panePID <= 0 {
		return nil, 0, fmt.Errorf("tmux runtime: invalid pane pid %q", strings.TrimSpace(string(paneOut)))
	}
	processOut, err := r.runCommand(ctx, "ps", "-ww", "-axo", "pid=,ppid=,args=")
	if err != nil {
		return nil, 0, fmt.Errorf("tmux runtime: inspect process tree %s: %w", id, err)
	}
	entries, err := parseProcessTable(string(processOut))
	if err != nil {
		return nil, 0, fmt.Errorf("tmux runtime: parse process tree %s: %w", id, err)
	}
	return entries, panePID, nil
}

// SendMessage sends literal text to the session (chunked via send-keys -l) then
// presses Enter to submit. An empty message presses Enter alone (the nudge
// contract on ports.AgentMessenger).
//
// ponytail: send-keys -l chunked is simpler than load-buffer/paste-buffer; the
// ceiling is very large messages may be slower, but chunk size defaults to 16 KB
// which is ample for agent prompts.
func (r *Runtime) SendMessage(ctx context.Context, handle ports.RuntimeHandle, message string) error {
	route, err := r.routeForOperation(ctx, handle)
	if err != nil {
		return err
	}
	id := route.id
	paneTargetID := route.actionPaneTarget()
	enterCtx := ctx
	if message != "" {
		messageChunks := chunks(message, r.chunkSize)
		sendCtx := ctx
		var finishCancel context.CancelFunc
		for i, chunk := range messageChunks {
			if _, err := r.runActionOnRoute(sendCtx, route, sendKeysLiteralArgs(paneTargetID, chunk)...); err != nil {
				if finishCancel != nil {
					finishCancel()
				}
				return fmt.Errorf("tmux runtime: send message %s: %w", id, err)
			}
			if i == 0 {
				completionBudget := sendCompletionBudget(len(messageChunks), r.timeout, r.enterDelay)
				enterCtx, finishCancel = context.WithTimeout(context.WithoutCancel(ctx), completionBudget)
				sendCtx = enterCtx
			}
		}
		if finishCancel != nil {
			defer finishCancel()
		}
		// Give the target TUI a moment to accept the pasted text before the
		// trailing Enter, mirroring conpty's ptyInputEnterDelay. Without it a
		// large multiline paste can absorb the Enter and leave the prompt
		// unsubmitted (issue #2342). Empty-message nudges skip this — there is
		// no paste ahead of a catch-up Enter.
		//
		// From here on the chunks are already in the pane, so the pause and
		// the Enter are detached from the caller's cancellation (bounded by
		// their own timeout instead): abandoning mid-pause would strand an
		// unsubmitted draft that a retried send would then double-paste.
		// Errors reported by tmux after it accepts a chunk still return to the
		// caller; they are not retried because AO cannot safely distinguish
		// whether tmux applied the failed command.
		if r.enterDelay > 0 {
			select {
			case <-enterCtx.Done():
				return enterCtx.Err()
			case <-time.After(r.enterDelay):
			}
		}
	}
	if _, err := r.runActionOnRoute(enterCtx, route, sendEnterArgs(paneTargetID)...); err != nil {
		return fmt.Errorf("tmux runtime: send enter %s: %w", id, err)
	}
	return nil
}

func sendCompletionBudget(chunkCount int, commandTimeout, enterDelay time.Duration) time.Duration {
	return time.Duration(chunkCount)*commandTimeout + enterDelay
}

// Interrupt sends Ctrl-C to the foreground process without destroying the tmux
// session, keeping the terminal available for inspection and reuse.
func (r *Runtime) Interrupt(ctx context.Context, handle ports.RuntimeHandle) error {
	route, err := r.routeForOperation(ctx, handle)
	if err != nil {
		return err
	}
	if _, err := r.runActionOnRoute(ctx, route, sendInterruptArgs(route.actionPaneTarget())...); err != nil {
		return fmt.Errorf("tmux runtime: interrupt session %s: %w", route.id, err)
	}
	return nil
}

// SendInput sends raw terminal input without appending Enter. It is intended
// for TUI keybindings such as Escape rather than prompt text.
func (r *Runtime) SendInput(ctx context.Context, handle ports.RuntimeHandle, input string) error {
	route, err := r.routeForOperation(ctx, handle)
	if err != nil {
		return err
	}
	args := sendKeysLiteralArgs(route.actionPaneTarget(), input)
	if _, err := r.runActionOnRoute(ctx, route, args...); err != nil {
		return fmt.Errorf("tmux runtime: send input %s: %w", route.id, err)
	}
	return nil
}

// GetOutput returns the last `lines` lines of the session pane's captured
// output.
func (r *Runtime) GetOutput(ctx context.Context, handle ports.RuntimeHandle, lines int) (string, error) {
	route, err := r.routeForOperation(ctx, handle)
	if err != nil {
		return "", err
	}
	if lines <= 0 {
		return "", errors.New("tmux runtime: lines must be positive")
	}
	out, err := r.runActionOnRoute(ctx, route, capturePaneArgs(route.actionPaneTarget(), lines)...)
	if err != nil {
		return "", fmt.Errorf("tmux runtime: capture output %s: %w", route.id, err)
	}
	return tailLines(trimTrailingBlankLines(string(out)), lines), nil
}

// GetStyledOutput is GetOutput with tmux's -e flag so SGR styling is retained.
func (r *Runtime) GetStyledOutput(ctx context.Context, handle ports.RuntimeHandle, lines int) (string, error) {
	route, err := r.routeForOperation(ctx, handle)
	if err != nil {
		return "", err
	}
	if lines <= 0 {
		return "", errors.New("tmux runtime: lines must be positive")
	}
	out, err := r.runActionOnRoute(ctx, route, capturePaneStyledArgs(route.actionPaneTarget(), lines)...)
	if err != nil {
		return "", fmt.Errorf("tmux runtime: capture styled output %s: %w", route.id, err)
	}
	return tailLines(trimTrailingBlankLines(string(out)), lines), nil
}

// Attach opens a fresh attach Stream by spawning `tmux attach-session` on a
// local PTY, sized rows x cols from birth when known. ctx cancellation closes
// the PTY.
func (r *Runtime) Attach(ctx context.Context, handle ports.RuntimeHandle, rows, cols uint16) (ports.Stream, error) {
	route, err := r.routeForOperation(ctx, handle)
	if err != nil {
		return nil, err
	}
	argv, err := r.attachCommandForRoute(ctx, route)
	if err != nil {
		return nil, fmt.Errorf("tmux runtime: attach session %s: %w", route.id, err)
	}
	return r.spawnAttach(ctx, argv, attachEnv(os.Environ()), rows, cols)
}

// attachCommand returns the argv to attach a terminal to the session.
// tmux needs no per-session env block.
//
// -u forces tmux's client-side CLIENT_UTF8 flag on. Without it, tmux infers
// UTF-8 capability from LC_ALL/LC_CTYPE/LANG in the attaching process's env
// (see tmux's main()); AO's daemon is typically started without an
// interactive shell's locale, so that inference silently fails. A non-UTF8
// client makes tmux's tty_check_codeset (tty.c) replace any character it
// can't map through the legacy ACS table with underscores matching the
// glyph's display width. Box-drawing glyphs are in that ACS table so they
// still looked fine; agent CLI status icons outside it (e.g. Claude Code's
// spinner "✻" U+273B, its "⎿" U+23BF continuation marker) were silently
// rewritten to "_", which is the underscore corruption reported in #2484.
// Confirmed byte-for-byte: attaching with a stripped, locale-less env
// reproduces "_ _ _" for those glyphs; adding -u fixes it, with no observable
// difference for the still-correct box-drawing case. AO already treats the
// PTY byte stream as UTF-8 end to end, so forcing the flag is always
// correct here regardless of the daemon's own environment.
func (r *Runtime) attachCommand(ctx context.Context, handle ports.RuntimeHandle) ([]string, error) {
	route, err := decodeRuntimeHandle(handle)
	if err != nil {
		return nil, err
	}
	if !route.qualified {
		route.target = r.primarySocketTarget()
	}
	return r.attachCommandForSocket(ctx, route.id, route.target)
}

func (r *Runtime) attachCommandForSocket(ctx context.Context, id string, target socketTarget) ([]string, error) {
	// The embedded xterm renderer supports 24-bit SGR colors. Tell this tmux
	// client explicitly so tmux forwards RGB instead of quantizing it to the
	// xterm-256color palette. -T is available in AO's minimum tmux version (3.2).
	binary, argv, err := r.commandForSocket(ctx, target, "-u", "-T", "RGB", "attach-session", "-t", id)
	if err != nil {
		return nil, err
	}
	return append([]string{binary}, argv...), nil
}

func (r *Runtime) attachCommandForRoute(ctx context.Context, route runtimeRoute) ([]string, error) {
	if !route.hasObjectFence() {
		return r.attachCommandForSocket(ctx, route.actionSessionTarget(), route.target)
	}
	guarded, err := fencedCommandArgs(route, []string{
		"attach-session", "-t", route.actionSessionTarget(),
	})
	if err != nil {
		return nil, err
	}
	binary, argv, err := r.commandForSocket(ctx, route.target, append([]string{"-u", "-T", "RGB"}, guarded...)...)
	if err != nil {
		return nil, err
	}
	return append([]string{binary}, argv...), nil
}

func attachEnv(base []string) []string {
	env := append([]string(nil), base...)
	hasTerm := false
	hasColorTerm := false
	for i, kv := range env {
		switch {
		case strings.HasPrefix(kv, "TERM="):
			env[i] = "TERM=xterm-256color"
			hasTerm = true
		case strings.HasPrefix(kv, "COLORTERM="):
			env[i] = "COLORTERM=truecolor"
			hasColorTerm = true
		}
	}
	if !hasTerm {
		env = append(env, "TERM=xterm-256color")
	}
	if !hasColorTerm {
		env = append(env, "COLORTERM=truecolor")
	}
	return env
}

// run wraps runner.Run with a per-call timeout context.
func (r *Runtime) run(ctx context.Context, args ...string) ([]byte, error) {
	return r.runOnSocket(ctx, r.primarySocketTarget(), args...)
}

func (r *Runtime) runOnSocket(ctx context.Context, target socketTarget, args ...string) ([]byte, error) {
	binary, argv, err := r.commandForSocket(ctx, target, args...)
	if err != nil {
		return nil, err
	}
	return r.runCommand(ctx, binary, argv...)
}

// runActionOnRoute runs action directly for an unresolved legacy route and
// uses a server-side object fence for every canonical route. Recovery is the
// only path allowed to turn a legacy locator into a canonical route; public
// operations reject legacy qualified handles before reaching this helper.
func (r *Runtime) runActionOnRoute(ctx context.Context, route runtimeRoute, action ...string) ([]byte, error) {
	if !route.hasObjectFence() {
		return r.runOnSocket(ctx, route.target, action...)
	}
	return r.runFencedCommands(ctx, route, action)
}

// runFencedCommands evaluates the complete server/session/pane identity and
// queues the action(s) in one tmux client command. if-shell executes the chosen
// branch inside the already-connected server, so a socket replacement after
// the condition cannot redirect the queued action to the replacement server.
func (r *Runtime) runFencedCommands(ctx context.Context, route runtimeRoute, actions ...[]string) ([]byte, error) {
	args, err := fencedCommandArgs(route, actions...)
	if err != nil {
		return nil, err
	}
	out, err := r.runOnSocket(ctx, route.target, args...)
	if ctx.Err() != nil {
		return out, ctx.Err()
	}
	if tmuxFenceMismatchOutput(string(out)) {
		return out, fmt.Errorf(
			"%w: tmux server or pane identity changed for session %s",
			ports.ErrRuntimeProbeInconclusive,
			route.id,
		)
	}
	if err != nil {
		return out, fmt.Errorf(
			"%w: guarded tmux command failed for session %s: %w",
			ports.ErrRuntimeProbeInconclusive,
			route.id,
			err,
		)
	}
	return out, nil
}

func fencedCommandArgs(route runtimeRoute, actions ...[]string) ([]string, error) {
	if !route.hasObjectFence() {
		return nil, fmt.Errorf(
			"%w: tmux route for session %s has no complete object fence",
			ports.ErrRuntimeProbeInconclusive,
			route.id,
		)
	}
	if len(actions) == 0 {
		return nil, errors.New("tmux runtime: guarded command requires an action")
	}
	commandStrings := make([]string, 0, len(actions))
	for _, action := range actions {
		command, err := tmuxCommandString(action)
		if err != nil {
			return nil, err
		}
		commandStrings = append(commandStrings, command)
	}
	falseCommand, err := tmuxCommandString([]string{"display-message", "-p", tmuxObjectFenceMismatch})
	if err != nil {
		return nil, err
	}
	condition := fmt.Sprintf(
		"#{&&:#{==:#{pid},%d},#{&&:#{==:#{session_id},%s},#{==:#{pane_id},%s}}}",
		route.tmuxServerPID,
		route.tmuxSessionID,
		route.tmuxPaneID,
	)
	return []string{
		"if-shell", "-F", "-t", route.tmuxPaneID,
		condition,
		strings.Join(commandStrings, " ; "),
		falseCommand,
	}, nil
}

// tmuxCommandString quotes one argv vector for tmux's command parser. The
// result is passed as a single if-shell branch argument, then parsed once by
// tmux. Always quoting every argument keeps user input literal (notably $, ;,
// quotes, newlines, and backslashes) while still allowing tmux format strings
// in AO-authored arguments to be evaluated by their target command.
func tmuxCommandString(args []string) (string, error) {
	if len(args) == 0 {
		return "", errors.New("tmux runtime: empty command")
	}
	quoted := make([]string, 0, len(args))
	for _, arg := range args {
		var b strings.Builder
		b.Grow(len(arg) + 2)
		b.WriteByte('"')
		for _, char := range arg {
			switch char {
			case 0:
				return "", errors.New("tmux runtime: command argument contains NUL")
			case '\\':
				b.WriteString(`\\`)
			case '"':
				b.WriteString(`\"`)
			case '$':
				b.WriteString(`\$`)
			case '\n':
				b.WriteString(`\n`)
			case '\r':
				b.WriteString(`\r`)
			case '\t':
				b.WriteString(`\t`)
			case '\x1b':
				b.WriteString(`\e`)
			default:
				b.WriteRune(char)
			}
		}
		b.WriteByte('"')
		quoted = append(quoted, b.String())
	}
	return strings.Join(quoted, " "), nil
}

func tmuxFenceMismatchOutput(output string) bool {
	for _, line := range strings.Split(output, "\n") {
		if strings.TrimSuffix(line, "\r") == tmuxObjectFenceMismatch {
			return true
		}
	}
	return false
}

// commandForSocket is the single mapping from namespace provenance to tmux
// argv. Attach and ordinary operations both use it so a new target kind cannot
// accidentally route those two paths differently.
func (r *Runtime) commandForSocket(ctx context.Context, target socketTarget, args ...string) (string, []string, error) {
	binary := r.binaryForSocket(target)
	if strings.TrimSpace(binary) == "" {
		return "", nil, fmt.Errorf("%w: tmux client is unavailable for %s", ports.ErrRuntimeProbeInconclusive, target)
	}
	prefix, err := target.argv(ctx)
	if err != nil {
		return "", nil, err
	}
	return binary, append(prefix, args...), nil
}

func (target socketTarget) argv(ctx context.Context) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	switch target.kind {
	case socketTargetDefault:
		// Always pin the machine default. Plain tmux honors inherited TMUX and
		// could otherwise redirect creation or recovery to a nested server.
		return []string{"-L", "default"}, nil
	case socketTargetNamed:
		if strings.TrimSpace(target.value) == "" {
			return nil, errors.New("tmux runtime: named socket target is empty")
		}
		return []string{"-L", target.value}, nil
	case socketTargetPath:
		if strings.TrimSpace(target.value) == "" {
			return nil, errors.New("tmux runtime: socket path target is empty")
		}
		address, err := socketAddress(ctx, target.value)
		if err != nil {
			return nil, err
		}
		return []string{"-S", address, "-f", os.DevNull}, nil
	default:
		return nil, fmt.Errorf("tmux runtime: unknown socket target %q", target.kind)
	}
}

func (target socketTarget) String() string {
	switch target.kind {
	case socketTargetDefault:
		return "default socket"
	case socketTargetNamed:
		return fmt.Sprintf("named socket %q", target.value)
	case socketTargetPath:
		return fmt.Sprintf("socket path %q", target.value)
	default:
		return fmt.Sprintf("unknown socket target %q", target.kind)
	}
}

func (r *Runtime) binaryForSocket(target socketTarget) string {
	if target.useLegacyBinary {
		return r.legacyBinary
	}
	return r.binary
}

func (r *Runtime) primarySocketTarget() socketTarget {
	if r.socketName != "" {
		return socketTarget{kind: socketTargetNamed, value: r.socketName}
	}
	return socketTarget{kind: socketTargetDefault}
}

func (r *Runtime) historicalPrivateSocketTarget() socketTarget {
	return socketTarget{kind: socketTargetPath, value: r.legacySocketPath}
}

func (r *Runtime) legacyDefaultSocketTarget() socketTarget {
	return socketTarget{kind: socketTargetDefault, useLegacyBinary: true}
}

func (r *Runtime) routeForOperation(ctx context.Context, handle ports.RuntimeHandle) (runtimeRoute, error) {
	route, found, err := r.resolveRouteForOperation(ctx, handle)
	if err != nil {
		return runtimeRoute{}, err
	}
	if !found {
		return runtimeRoute{}, fmt.Errorf("tmux runtime: session %s was not found", route.id)
	}
	return route, nil
}

func (r *Runtime) resolveRouteForOperation(
	ctx context.Context,
	handle ports.RuntimeHandle,
) (runtimeRoute, bool, error) {
	route, err := decodeRuntimeHandle(handle)
	if err != nil {
		return runtimeRoute{}, false, err
	}
	if route.qualified {
		// Pre-object-fence qualified handles remain decodable only so startup
		// recovery can discover the durable owner and persist a canonical route.
		// A public read or mutation must never re-resolve implicitly: doing so
		// would reopen a gap in which a replacement server can reuse $0/%0.
		if !route.hasObjectFence() {
			return runtimeRoute{}, false, fmt.Errorf(
				"%w: qualified tmux handle %q requires canonicalization",
				ports.ErrRuntimeProbeInconclusive,
				handle.ID,
			)
		}
		return r.resolveExactQualifiedRoute(ctx, route)
	}
	candidates := r.legacySocketTargets()
	if len(candidates) == 1 && r.runFilePath == "" {
		route.target = candidates[0]
		return route, true, nil
	}
	resolved, found, err := r.resolveRuntimeHandle(ctx, handle, ports.SupervisedProcessRef{}, true)
	if err != nil {
		return runtimeRoute{}, false, err
	}
	if !found {
		return route, false, nil
	}
	return resolved.route, true, nil
}

func (r *Runtime) legacySocketTargets() []socketTarget {
	targets := []socketTarget{r.primarySocketTarget()}
	if r.legacySocketPath != "" {
		targets = append(targets, r.historicalPrivateSocketTarget())
	}
	if r.socketName != "" {
		targets = append(targets, r.legacyDefaultSocketTarget())
	}
	return targets
}

func (r *Runtime) discoverTargets(
	ctx context.Context,
	id string,
	targets []socketTarget,
) ([]socketTarget, error) {
	var matches []socketTarget
	var failures []error
	for _, target := range targets {
		out, err := r.runOnSocket(ctx, target, hasSessionArgs(id)...)
		if err == nil {
			matches = append(matches, target)
			continue
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if sessionMissingOutput(string(out)) || serverNotRunningOutput(string(out)) || migrationSocketAbsentOutput(string(out)) {
			continue
		}
		failures = append(failures, fmt.Errorf("probe %s: %w", target, err))
	}
	if len(failures) > 0 {
		return nil, fmt.Errorf(
			"%w: could not inspect every tmux namespace for session %s: %w",
			ports.ErrRuntimeProbeInconclusive,
			id,
			errors.Join(failures...),
		)
	}
	return matches, nil
}

func appendUniqueTarget(targets []socketTarget, candidate socketTarget) []socketTarget {
	for _, target := range targets {
		if target == candidate {
			return targets
		}
	}
	return append(targets, candidate)
}

func (r *Runtime) recoveryTargets(route runtimeRoute) []socketTarget {
	targets := make([]socketTarget, 0, len(r.legacySocketTargets())+1)
	if route.qualified {
		targets = appendUniqueTarget(targets, route.target)
	}
	for _, target := range r.legacySocketTargets() {
		targets = appendUniqueTarget(targets, target)
	}
	return targets
}

type runtimeTargetCandidate struct {
	target        socketTarget
	identity      ports.RuntimeIdentity
	adoptable     bool
	tmuxServerPID int
	tmuxSessionID string
	tmuxPaneID    string
}

type runtimeResolution struct {
	handle ports.RuntimeHandle
	route  runtimeRoute
}

type paneIdentityEvidence struct {
	identity        ports.RuntimeIdentity
	launchID        string
	legacyNoRunFile bool
	tmuxServerPID   int
	tmuxSessionID   string
	tmuxPaneID      string
}

func (r *Runtime) inspectTargetIdentityEvidence(
	ctx context.Context,
	tmuxID string,
	expectedSessionID domain.SessionID,
	target socketTarget,
) (paneIdentityEvidence, error) {
	out, err := r.runOnSocket(ctx, target, paneStartCommandsArgs(tmuxID)...)
	if err != nil {
		return paneIdentityEvidence{}, fmt.Errorf(
			"%w: inspect pane provenance for session %s on %s: %w",
			ports.ErrRuntimeProbeInconclusive,
			tmuxID,
			target,
			err,
		)
	}
	return r.parsePaneIdentityEvidence(string(out), expectedSessionID, target)
}

func (r *Runtime) parsePaneIdentityEvidence(
	output string,
	expectedSessionID domain.SessionID,
	target socketTarget,
) (paneIdentityEvidence, error) {
	tmuxServerPID, tmuxSessionID, tmuxPaneID, command, err := parsePaneIdentityOutput(output)
	if err != nil {
		return paneIdentityEvidence{}, fmt.Errorf(
			"%w: inspect pane identity for session %s on %s: %w",
			ports.ErrRuntimeProbeInconclusive,
			expectedSessionID,
			target,
			err,
		)
	}
	rawSessionID := string(expectedSessionID)
	launchID, supervised := paneSupervisorIdentity(command, rawSessionID)
	if !supervised && r.isHistoricalPrivateSocketTarget(target) {
		// #4393 installed AO_SESSION_ID/AO_SUPERVISED_PROCESS in tmux's
		// session environment, not in pane_start_command. Its immutable pane
		// provenance is therefore the quoted supervisor argv itself. Accept
		// that weaker historical grammar only on this run file's deterministic
		// private socket; candidate selection below still requires the durable
		// launch fence to match exactly and can never adopt a newer launch.
		launchID, supervised = historicalPrivatePaneSupervisorIdentity(command, rawSessionID)
	}
	if !supervised {
		return paneIdentityEvidence{
			tmuxServerPID: tmuxServerPID,
			tmuxSessionID: tmuxSessionID,
			tmuxPaneID:    tmuxPaneID,
		}, nil
	}
	_, owned := paneRuntimeIdentity(command, rawSessionID, r.runFilePath)
	return paneIdentityEvidence{
		identity: ports.RuntimeIdentity{
			LaunchID:        launchID,
			OwnershipProven: owned,
		},
		launchID:        launchID,
		legacyNoRunFile: !strings.Contains(command, "export AO_RUN_FILE="),
		tmuxServerPID:   tmuxServerPID,
		tmuxSessionID:   tmuxSessionID,
		tmuxPaneID:      tmuxPaneID,
	}, nil
}

func parsePaneIdentityOutput(output string) (int, string, string, string, error) {
	line := strings.TrimSuffix(output, "\n")
	line = strings.TrimSuffix(line, "\r")
	if line == "" || strings.ContainsAny(line, "\r\n") {
		return 0, "", "", "", errors.New("expected exactly one pane identity")
	}
	fields := strings.SplitN(line, "\t", 4)
	serverPID, err := strconv.Atoi(firstField(fields))
	if len(fields) != 4 || err != nil || serverPID <= 1 ||
		!tmuxSessionIDPattern.MatchString(fields[1]) || !tmuxPaneIDPattern.MatchString(fields[2]) {
		return 0, "", "", "", errors.New("invalid tmux object identity")
	}
	command := strings.TrimSpace(fields[3])
	if command == "" {
		return 0, "", "", "", errors.New("pane start command is empty")
	}
	return serverPID, fields[1], fields[2], command, nil
}

func firstField(fields []string) string {
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

func (r *Runtime) isHistoricalPrivateSocketTarget(target socketTarget) bool {
	return target.kind == socketTargetPath &&
		r.legacySocketPath != "" &&
		filepath.Clean(target.value) == filepath.Clean(r.legacySocketPath)
}

func (r *Runtime) inspectTargetIdentity(
	ctx context.Context,
	tmuxID string,
	expectedSessionID domain.SessionID,
	target socketTarget,
) (ports.RuntimeIdentity, error) {
	evidence, err := r.inspectTargetIdentityEvidence(ctx, tmuxID, expectedSessionID, target)
	return evidence.identity, err
}

func paneRuntimeIdentity(command, id, runFilePath string) (string, bool) {
	launchID, supervised := paneSupervisorIdentity(command, id)
	if !supervised || !strings.Contains(command, "export AO_RUN_FILE="+shellQuote(runFilePath)+";") {
		return "", false
	}
	return launchID, true
}

func paneSupervisorIdentity(command, id string) (string, bool) {
	required := []string{
		"export AO_SESSION_ID=" + shellQuote(id) + ";",
		"export AO_SUPERVISED_PROCESS='1';",
	}
	for _, fragment := range required {
		if !strings.Contains(command, fragment) {
			return "", false
		}
	}
	return paneSupervisorArgvIdentity(command, id)
}

// historicalPrivatePaneSupervisorIdentity recognizes the supervised launch
// string emitted by the private-socket release (#4393). That release populated
// AO's environment through tmux set-environment, so pane_start_command contains
// no AO_SESSION_ID, AO_SUPERVISED_PROCESS, or AO_RUN_FILE exports. The framing
// below and the quoted supervisor argv are durable command-line facts; callers
// must additionally require an exact database session+launch match.
func historicalPrivatePaneSupervisorIdentity(command, id string) (string, bool) {
	required := []string{
		"cd ",
		" || exit;",
		"export COLORTERM='truecolor';",
		"; exec cat >/dev/null",
	}
	for _, fragment := range required {
		if !strings.Contains(command, fragment) {
			return "", false
		}
	}
	return paneSupervisorArgvIdentity(command, id)
}

func paneSupervisorArgvIdentity(command, id string) (string, bool) {
	pattern := regexp.MustCompile(
		`'agent-process'\s+'supervise'\s+'--session'\s+'` + regexp.QuoteMeta(id) + `'\s+'--launch'\s+'([^']+)'`,
	)
	match := pattern.FindStringSubmatch(command)
	if len(match) != 2 || strings.TrimSpace(match[1]) == "" {
		return "", false
	}
	return match[1], true
}

// InspectRuntimeIdentity rereads immutable pane provenance from the canonical
// target. It intentionally has no cache: a previous process's discovery is not
// evidence after daemon replacement.
func (r *Runtime) InspectRuntimeIdentity(ctx context.Context, handle ports.RuntimeHandle, expectedSessionID domain.SessionID) (ports.RuntimeIdentity, error) {
	route, err := decodeRuntimeHandle(handle)
	if err != nil {
		return ports.RuntimeIdentity{}, err
	}
	expectedTmuxID, err := tmuxSessionName(expectedSessionID)
	if err != nil {
		return ports.RuntimeIdentity{}, err
	}
	if route.id != expectedTmuxID && route.id != string(expectedSessionID) {
		return ports.RuntimeIdentity{}, fmt.Errorf(
			"tmux runtime: identity handle %s does not match session %s",
			route.id,
			expectedSessionID,
		)
	}
	if route.hasObjectFence() {
		out, err := r.runFencedCommands(ctx, route, paneStartCommandsArgs(route.actionSessionTarget()))
		if err != nil {
			return ports.RuntimeIdentity{}, err
		}
		evidence, err := r.parsePaneIdentityEvidence(string(out), expectedSessionID, route.target)
		if err != nil {
			return ports.RuntimeIdentity{}, err
		}
		return evidence.identity, nil
	}
	return r.inspectTargetIdentity(ctx, route.id, expectedSessionID, route.target)
}

// ResolveRuntimeHandle canonicalizes a legacy bare or pre-fence qualified
// locator into a durable owner-fenced route. A fully canonical handle remains
// pinned to its persisted server/session/pane object; only a newer launch on
// that unchanged object may be adopted to repair a crash between pane respawn
// and the database CAS. Destructive cleanup uses ResolveExactRuntimeHandle.
func (r *Runtime) ResolveRuntimeHandle(
	ctx context.Context,
	handle ports.RuntimeHandle,
	owner ports.SupervisedProcessRef,
) (ports.RuntimeHandle, bool, error) {
	resolved, found, err := r.resolveRuntimeHandle(ctx, handle, owner, true)
	return resolved.handle, found, err
}

// ResolveExactRuntimeHandle resolves only owner itself. A legacy locator may
// relocate an exact surviving workload to another known tmux namespace; a
// fully canonical handle stays pinned to its persisted object. Neither path
// adopts a newer same-session generation. Boot reaping depends on this
// distinction so a terminated stale row cannot kill its live replacement.
func (r *Runtime) ResolveExactRuntimeHandle(
	ctx context.Context,
	handle ports.RuntimeHandle,
	owner ports.SupervisedProcessRef,
) (ports.RuntimeHandle, bool, error) {
	if owner.SessionID == "" {
		return ports.RuntimeHandle{}, false, fmt.Errorf(
			"%w: exact tmux owner requires a session",
			ports.ErrRuntimeProbeInconclusive,
		)
	}
	owner.LaunchID = strings.TrimSpace(owner.LaunchID)
	if owner.LaunchID == "" {
		return r.resolveAbsentExactOwnerWithoutLaunch(ctx, handle, owner.SessionID)
	}
	resolved, found, err := r.resolveRuntimeHandle(ctx, handle, owner, false)
	return resolved.handle, found, err
}

// resolveAbsentExactOwnerWithoutLaunch handles terminated rows created before
// AO persisted runtime launch generations. Absence needs no ownership proof:
// exhaustively finding no same-named target is enough to finish boot cleanup.
// If any target exists, however, an empty historical generation cannot
// authorize inspection or destruction, so recovery stays failed closed.
func (r *Runtime) resolveAbsentExactOwnerWithoutLaunch(
	ctx context.Context,
	handle ports.RuntimeHandle,
	sessionID domain.SessionID,
) (ports.RuntimeHandle, bool, error) {
	route, err := decodeRuntimeHandle(handle)
	if err != nil {
		return ports.RuntimeHandle{}, false, err
	}
	tmuxID, err := tmuxSessionName(sessionID)
	if err != nil {
		return ports.RuntimeHandle{}, false, err
	}
	if route.id != tmuxID && route.id != string(sessionID) {
		return ports.RuntimeHandle{}, false, fmt.Errorf(
			"%w: tmux handle %s does not match owner session %s",
			ports.ErrRuntimeProbeInconclusive,
			route.id,
			sessionID,
		)
	}
	matches, err := r.discoverTargets(ctx, route.id, r.recoveryTargets(route))
	if err != nil {
		return ports.RuntimeHandle{}, false, err
	}
	if len(matches) == 0 {
		return ports.RuntimeHandle{}, false, nil
	}
	return ports.RuntimeHandle{}, false, fmt.Errorf(
		"%w: tmux session %s exists in %d namespace(s), but the durable launch generation is empty",
		ports.ErrRuntimeProbeInconclusive,
		route.id,
		len(matches),
	)
}

func (r *Runtime) resolveRuntimeHandle(
	ctx context.Context,
	handle ports.RuntimeHandle,
	owner ports.SupervisedProcessRef,
	allowProvenNewer bool,
) (runtimeResolution, bool, error) {
	route, err := decodeRuntimeHandle(handle)
	if err != nil {
		return runtimeResolution{}, false, err
	}
	owner.LaunchID = strings.TrimSpace(owner.LaunchID)
	if route.qualified && route.owner.SessionID != "" {
		if owner.SessionID == "" {
			owner = route.owner
		} else if owner != route.owner {
			return runtimeResolution{}, false, fmt.Errorf(
				"%w: qualified tmux handle owner does not match durable owner",
				ports.ErrRuntimeProbeInconclusive,
			)
		}
	}
	identitySessionID := domain.SessionID(route.id)
	hasExpectedOwner := owner.SessionID != ""
	if hasExpectedOwner {
		ownerTmuxID, nameErr := tmuxSessionName(owner.SessionID)
		if nameErr != nil {
			return runtimeResolution{}, false, nameErr
		}
		if ownerTmuxID != route.id && string(owner.SessionID) != route.id {
			return runtimeResolution{}, false, fmt.Errorf(
				"%w: tmux handle %s does not match owner session %s",
				ports.ErrRuntimeProbeInconclusive,
				route.id,
				owner.SessionID,
			)
		}
		identitySessionID = owner.SessionID
		if route.qualified && route.hasObjectFence() {
			return r.resolveFencedRuntimeHandle(ctx, route, identitySessionID, owner, allowProvenNewer)
		}
	} else if route.qualified {
		if r.runFilePath == "" {
			present, evidence, inspectErr := r.inspectTargetPresence(ctx, route, domain.SessionID(route.id))
			if inspectErr != nil || !present {
				return runtimeResolution{}, present, inspectErr
			}
			if route.hasObjectFence() && (evidence.tmuxServerPID != route.tmuxServerPID ||
				evidence.tmuxSessionID != route.tmuxSessionID || evidence.tmuxPaneID != route.tmuxPaneID) {
				return runtimeResolution{}, false, fmt.Errorf(
					"%w: tmux object identity changed for session %s",
					ports.ErrRuntimeProbeInconclusive,
					route.id,
				)
			}
			resolvedRoute := route.withPaneIdentity(evidence)
			resolved, encodeErr := qualifiedRuntimeHandleForRoute(resolvedRoute)
			return runtimeResolution{handle: resolved, route: resolvedRoute}, encodeErr == nil, encodeErr
		}
		return runtimeResolution{}, false, fmt.Errorf(
			"%w: qualified tmux handle %q has no immutable owner fence",
			ports.ErrRuntimeProbeInconclusive,
			handle.ID,
		)
	}

	matches, err := r.discoverTargets(ctx, route.id, r.recoveryTargets(route))
	if err != nil {
		return runtimeResolution{}, false, err
	}
	if len(matches) == 0 {
		return runtimeResolution{}, false, nil
	}

	candidates := make([]runtimeTargetCandidate, 0, len(matches))
	for _, target := range matches {
		evidence, inspectErr := r.inspectTargetIdentityEvidence(ctx, route.id, identitySessionID, target)
		if inspectErr != nil {
			return runtimeResolution{}, false, inspectErr
		}
		fullCandidate := evidence.identity.OwnershipProven &&
			(allowProvenNewer || !hasExpectedOwner || evidence.launchID == owner.LaunchID)
		weakExactCandidate := evidence.legacyNoRunFile && hasExpectedOwner &&
			owner.LaunchID != "" && evidence.launchID == owner.LaunchID
		// A pre-AO_RUN_FILE controller whose launch was overwritten in the DB is
		// not safe to adopt, but it is still conflict evidence. Keep it in the
		// candidate set so a later named/default duplicate cannot become the only
		// visible owner merely because it matches the stale durable generation.
		legacyController := evidence.legacyNoRunFile && evidence.launchID != ""
		if fullCandidate || weakExactCandidate || legacyController {
			candidates = append(candidates, runtimeTargetCandidate{
				target:        target,
				tmuxServerPID: evidence.tmuxServerPID,
				tmuxSessionID: evidence.tmuxSessionID,
				tmuxPaneID:    evidence.tmuxPaneID,
				adoptable:     fullCandidate || weakExactCandidate,
				identity: ports.RuntimeIdentity{
					LaunchID:        evidence.launchID,
					OwnershipProven: evidence.identity.OwnershipProven,
				},
			})
		}
	}
	if len(candidates) == 0 {
		if len(matches) > 1 {
			return runtimeResolution{}, false, ports.RuntimeHandleAmbiguityError{
				Handle: handle, Candidates: len(matches),
			}
		}
		return runtimeResolution{}, false, fmt.Errorf(
			"%w: tmux session %s exists on %s but is not the durable AO owner",
			ports.ErrRuntimeProbeInconclusive,
			route.id,
			matches[0],
		)
	}

	qualify := func(candidate runtimeTargetCandidate) (runtimeResolution, bool, error) {
		resolvedOwner := ports.SupervisedProcessRef{
			SessionID: identitySessionID,
			LaunchID:  candidate.identity.LaunchID,
		}
		resolvedRoute := runtimeRoute{
			id:            route.id,
			target:        candidate.target,
			qualified:     true,
			owner:         resolvedOwner,
			tmuxServerPID: candidate.tmuxServerPID,
			tmuxSessionID: candidate.tmuxSessionID,
			tmuxPaneID:    candidate.tmuxPaneID,
		}
		resolved, encodeErr := qualifiedRuntimeHandleForRoute(resolvedRoute)
		return runtimeResolution{
			handle: resolved,
			route:  resolvedRoute,
		}, encodeErr == nil, encodeErr
	}
	if len(candidates) == 1 && candidates[0].adoptable {
		return qualify(candidates[0])
	}

	// Prefer the only controller that still owns a live managed workload. This
	// repairs failed respawns while refusing to guess between live duplicates.
	var live []runtimeTargetCandidate
	for _, candidate := range candidates {
		alive, inspectErr := r.isExactSupervisedProcessAliveOnTarget(ctx, route.id, candidate.target, candidate.tmuxServerPID, candidate.tmuxSessionID, candidate.tmuxPaneID, ports.SupervisedProcessRef{
			SessionID: identitySessionID,
			LaunchID:  candidate.identity.LaunchID,
		})
		if inspectErr != nil {
			return runtimeResolution{}, false, fmt.Errorf(
				"%w: inspect live owned controller on %s for session %s: %w",
				ports.ErrRuntimeProbeInconclusive,
				candidate.target,
				route.id,
				inspectErr,
			)
		}
		if alive {
			live = append(live, candidate)
		}
	}
	matchingLaunch := func(in []runtimeTargetCandidate) []runtimeTargetCandidate {
		if !hasExpectedOwner || owner.LaunchID == "" {
			return nil
		}
		var matching []runtimeTargetCandidate
		for _, candidate := range in {
			if candidate.identity.LaunchID == owner.LaunchID {
				matching = append(matching, candidate)
			}
		}
		return matching
	}
	if len(live) == 1 {
		if live[0].adoptable {
			return qualify(live[0])
		}
		return runtimeResolution{}, false, fmt.Errorf(
			"%w: live tmux controller on %s is not the durable AO owner",
			ports.ErrRuntimeProbeInconclusive,
			live[0].target,
		)
	}
	if len(live) > 1 {
		return runtimeResolution{}, false, ports.RuntimeHandleAmbiguityError{
			Handle: handle, Candidates: len(live),
		}
	}
	adoptable := make([]runtimeTargetCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.adoptable {
			adoptable = append(adoptable, candidate)
		}
	}
	if matching := matchingLaunch(adoptable); len(matching) == 1 {
		return qualify(matching[0])
	}
	return runtimeResolution{}, false, ports.RuntimeHandleAmbiguityError{
		Handle: handle, Candidates: len(candidates),
	}
}

func (r *Runtime) resolveFencedRuntimeHandle(
	ctx context.Context,
	route runtimeRoute,
	identitySessionID domain.SessionID,
	owner ports.SupervisedProcessRef,
	allowProvenNewer bool,
) (runtimeResolution, bool, error) {
	present, evidence, err := r.inspectTargetPresence(ctx, route, identitySessionID)
	if err != nil || !present {
		return runtimeResolution{}, present, err
	}
	if evidence.tmuxServerPID != route.tmuxServerPID ||
		evidence.tmuxSessionID != route.tmuxSessionID || evidence.tmuxPaneID != route.tmuxPaneID {
		return runtimeResolution{}, false, fmt.Errorf(
			"%w: tmux object identity changed for session %s",
			ports.ErrRuntimeProbeInconclusive,
			route.id,
		)
	}
	fullCandidate := evidence.identity.OwnershipProven &&
		(allowProvenNewer || evidence.launchID == owner.LaunchID)
	weakExactCandidate := evidence.legacyNoRunFile && owner.LaunchID != "" &&
		evidence.launchID == owner.LaunchID
	if !fullCandidate && !weakExactCandidate {
		return runtimeResolution{}, false, fmt.Errorf(
			"%w: tmux session %s is not the durable AO owner",
			ports.ErrRuntimeProbeInconclusive,
			route.id,
		)
	}
	resolvedRoute := route
	resolvedRoute.owner = ports.SupervisedProcessRef{
		SessionID: identitySessionID,
		LaunchID:  evidence.launchID,
	}
	resolved, err := qualifiedRuntimeHandleForRoute(resolvedRoute)
	return runtimeResolution{handle: resolved, route: resolvedRoute}, err == nil, err
}

func (r *Runtime) resolveExactQualifiedRoute(
	ctx context.Context,
	route runtimeRoute,
) (runtimeRoute, bool, error) {
	if !route.hasObjectFence() {
		return runtimeRoute{}, false, fmt.Errorf(
			"%w: qualified tmux route %s has no complete object fence",
			ports.ErrRuntimeProbeInconclusive,
			route.id,
		)
	}
	expectedSessionID := route.owner.SessionID
	if expectedSessionID == "" {
		expectedSessionID = domain.SessionID(route.id)
	}
	present, evidence, err := r.inspectTargetPresence(ctx, route, expectedSessionID)
	if err != nil {
		return runtimeRoute{}, false, err
	}
	if !present {
		return route, false, nil
	}
	if evidence.tmuxServerPID != route.tmuxServerPID ||
		evidence.tmuxSessionID != route.tmuxSessionID ||
		evidence.tmuxPaneID != route.tmuxPaneID {
		return runtimeRoute{}, false, fmt.Errorf(
			"%w: tmux object identity changed for session %s",
			ports.ErrRuntimeProbeInconclusive,
			route.id,
		)
	}
	if route.owner.SessionID == "" {
		return route, true, nil
	}
	if evidence.launchID != route.owner.LaunchID ||
		(!evidence.identity.OwnershipProven && !evidence.legacyNoRunFile) {
		return runtimeRoute{}, false, fmt.Errorf(
			"%w: tmux session %s is not the persisted owner",
			ports.ErrRuntimeProbeInconclusive,
			route.id,
		)
	}
	return route, true, nil
}

func (route runtimeRoute) withPaneIdentity(evidence paneIdentityEvidence) runtimeRoute {
	route.tmuxServerPID = evidence.tmuxServerPID
	route.tmuxSessionID = evidence.tmuxSessionID
	route.tmuxPaneID = evidence.tmuxPaneID
	return route
}

func (r *Runtime) inspectTargetPresence(
	ctx context.Context,
	route runtimeRoute,
	expectedSessionID domain.SessionID,
) (bool, paneIdentityEvidence, error) {
	out, err := r.runOnSocket(ctx, route.target, hasSessionArgs(route.id)...)
	if err != nil {
		if sessionMissingOutput(string(out)) || serverNotRunningOutput(string(out)) || migrationSocketAbsentOutput(string(out)) {
			return false, paneIdentityEvidence{}, nil
		}
		return false, paneIdentityEvidence{}, fmt.Errorf(
			"%w: probe qualified tmux owner %s on %s: %w",
			ports.ErrRuntimeProbeInconclusive,
			route.id,
			route.target,
			err,
		)
	}
	evidence, err := r.inspectTargetIdentityEvidence(ctx, route.id, expectedSessionID, route.target)
	return true, evidence, err
}

func (r *Runtime) runCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmdCtx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	out, err := r.runner.Run(cmdCtx, nil, name, args...)
	if cmdCtx.Err() != nil {
		return out, cmdCtx.Err()
	}
	if err != nil {
		return out, commandError{err: err, output: strings.TrimSpace(string(out))}
	}
	return out, nil
}

type processEntry struct {
	pid     int
	ppid    int
	command string
}

func parseProcessTable(out string) ([]processEntry, error) {
	trimmed := strings.TrimSpace(out)
	if trimmed == "" {
		return nil, nil
	}
	lines := strings.Split(trimmed, "\n")
	entries := make([]processEntry, 0, len(lines))
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			return nil, fmt.Errorf("incomplete process row %q", line)
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil {
			return nil, fmt.Errorf("invalid pid in %q", line)
		}
		ppid, err := strconv.Atoi(fields[1])
		if err != nil {
			return nil, fmt.Errorf("invalid parent pid in %q", line)
		}
		entries = append(entries, processEntry{pid: pid, ppid: ppid, command: strings.Join(fields[2:], " ")})
	}
	return entries, nil
}

func descendantPIDs(entries []processEntry, rootPID int) map[int]bool {
	descendants := map[int]bool{rootPID: true}
	for changed := true; changed; {
		changed = false
		for _, entry := range entries {
			if descendants[entry.pid] || !descendants[entry.ppid] {
				continue
			}
			descendants[entry.pid] = true
			changed = true
		}
	}
	return descendants
}

func containsManagedWorkload(entries []processEntry, rootPID int, sessionID, launchID string) bool {
	descendants := descendantPIDs(entries, rootPID)
	hasChild := false
	hasSupervisor := false
	for _, entry := range entries {
		if entry.pid == rootPID || !descendants[entry.pid] {
			continue
		}
		hasChild = true
		if !isAnySupervisorCommand(entry.command) {
			continue
		}
		hasSupervisor = true
		if isSupervisorCommand(entry.command, sessionID, launchID) {
			return true
		}
	}

	// A supervisor in the pane tree must match the current generation. Once no
	// supervisor remains, the pane root is the preserved interactive shell and
	// any child is a workload the operator launched from that shell.
	return hasChild && !hasSupervisor
}

func containsExactSupervisedWorkload(entries []processEntry, rootPID int, sessionID, launchID string) bool {
	descendants := descendantPIDs(entries, rootPID)
	supervisorPID := 0
	for _, entry := range entries {
		if entry.pid != rootPID && descendants[entry.pid] && isSupervisorCommand(entry.command, sessionID, launchID) {
			supervisorPID = entry.pid
			break
		}
	}
	if supervisorPID == 0 {
		return false
	}
	workloadDescendants := descendantPIDs(entries, supervisorPID)
	for _, entry := range entries {
		if entry.pid != supervisorPID && workloadDescendants[entry.pid] {
			return true
		}
	}
	return false
}

func isAnySupervisorCommand(command string) bool {
	fields := strings.Fields(command)
	for i := 0; i+1 < len(fields); i++ {
		if fields[i] == "agent-process" && fields[i+1] == "supervise" {
			return true
		}
	}
	return false
}

func isSupervisorCommand(command, sessionID, launchID string) bool {
	fields := strings.Fields(command)
	for i := 0; i+6 < len(fields); i++ {
		if fields[i] == "agent-process" && fields[i+1] == "supervise" &&
			fields[i+2] == "--session" && fields[i+3] == sessionID &&
			fields[i+4] == "--launch" && fields[i+5] == launchID && fields[i+6] == "--" {
			return true
		}
	}
	return false
}

// -- session name helpers --

func tmuxSessionName(id domain.SessionID) (string, error) {
	raw := string(id)
	if raw == "" {
		return "", errors.New("tmux runtime: session id is required")
	}
	return SessionName(raw), nil
}

// SessionName returns the tmux session name the runtime registers for a given
// session id, applying the same sanitisation Create does. Callers that print an
// attach hint must use this rather than the raw id.
func SessionName(id string) string {
	if sessionIDPattern.MatchString(id) && len(id) <= 48 {
		return id
	}
	return sanitizedSessionName(id)
}

func sanitizedSessionName(raw string) string {
	var b strings.Builder
	lastDash := false
	for _, r := range raw {
		valid := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-'
		if valid {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	base := strings.Trim(b.String(), "-")
	if base == "" {
		base = "session"
	}
	if len(base) > 32 {
		base = strings.TrimRight(base[:32], "-")
	}
	sum := sha256.Sum256([]byte(raw))
	return base + "-" + hex.EncodeToString(sum[:4])
}

func qualifiedRuntimeHandle(id string, target socketTarget) (ports.RuntimeHandle, error) {
	return qualifiedRuntimeHandleForOwner(id, target, ports.SupervisedProcessRef{})
}

func qualifiedRuntimeHandleForOwner(
	id string,
	target socketTarget,
	owner ports.SupervisedProcessRef,
) (ports.RuntimeHandle, error) {
	return qualifiedRuntimeHandleForRoute(runtimeRoute{
		id:        id,
		target:    target,
		qualified: true,
		owner:     owner,
	})
}

func qualifiedRuntimeHandleForRoute(route runtimeRoute) (ports.RuntimeHandle, error) {
	id := route.id
	target := route.target
	owner := route.owner
	if err := validateHandleSessionID(id); err != nil {
		return ports.RuntimeHandle{}, err
	}
	owner.LaunchID = strings.TrimSpace(owner.LaunchID)
	if (owner.SessionID == "") != (owner.LaunchID == "") {
		return ports.RuntimeHandle{}, errors.New("tmux runtime: qualified handle owner requires both session and launch")
	}
	if owner.SessionID != "" {
		ownerTmuxID, err := tmuxSessionName(owner.SessionID)
		if err != nil {
			return ports.RuntimeHandle{}, err
		}
		if ownerTmuxID != id && string(owner.SessionID) != id {
			return ports.RuntimeHandle{}, fmt.Errorf(
				"tmux runtime: qualified handle owner session %s does not match tmux session %s",
				owner.SessionID,
				id,
			)
		}
	}
	payload := qualifiedHandlePayload{
		Session:       id,
		Target:        target.kind,
		LegacyBinary:  target.useLegacyBinary,
		OwnerSession:  string(owner.SessionID),
		OwnerLaunch:   owner.LaunchID,
		TmuxServerPID: route.tmuxServerPID,
		TmuxSessionID: route.tmuxSessionID,
		TmuxPaneID:    route.tmuxPaneID,
	}
	if (payload.TmuxServerPID == 0) != (payload.TmuxSessionID == "") ||
		(payload.TmuxServerPID == 0) != (payload.TmuxPaneID == "") {
		return ports.RuntimeHandle{}, errors.New("tmux runtime: qualified handle requires a complete object fence")
	}
	if payload.TmuxServerPID != 0 && (payload.TmuxServerPID <= 1 ||
		!tmuxSessionIDPattern.MatchString(payload.TmuxSessionID) ||
		!tmuxPaneIDPattern.MatchString(payload.TmuxPaneID)) {
		return ports.RuntimeHandle{}, errors.New("tmux runtime: invalid qualified handle object fence")
	}
	switch target.kind {
	case socketTargetDefault:
		// Default is intentionally explicit in the durable form; decoding pins
		// it with -L default even when the replacement Runtime has a named
		// primary or inherited TMUX points elsewhere.
	case socketTargetNamed, socketTargetPath:
		payload.Value = target.value
	default:
		return ports.RuntimeHandle{}, fmt.Errorf("tmux runtime: cannot encode unknown socket target %q", target.kind)
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return ports.RuntimeHandle{}, fmt.Errorf("tmux runtime: encode handle: %w", err)
	}
	return ports.RuntimeHandle{ID: tmuxHandlePrefix + base64.RawURLEncoding.EncodeToString(encoded)}, nil
}

func decodeRuntimeHandle(handle ports.RuntimeHandle) (runtimeRoute, error) {
	if handle.ID == "" {
		return runtimeRoute{}, errors.New("tmux runtime: session id is required")
	}
	if !strings.HasPrefix(handle.ID, tmuxHandlePrefix) {
		if err := validateHandleSessionID(handle.ID); err != nil {
			return runtimeRoute{}, err
		}
		return runtimeRoute{id: handle.ID}, nil
	}

	raw := strings.TrimPrefix(handle.ID, tmuxHandlePrefix)
	encoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return runtimeRoute{}, fmt.Errorf("tmux runtime: invalid qualified handle: %w", err)
	}
	var payload qualifiedHandlePayload
	if err := json.Unmarshal(encoded, &payload); err != nil {
		return runtimeRoute{}, fmt.Errorf("tmux runtime: invalid qualified handle payload: %w", err)
	}
	if err := validateHandleSessionID(payload.Session); err != nil {
		return runtimeRoute{}, err
	}
	payload.OwnerSession = strings.TrimSpace(payload.OwnerSession)
	payload.OwnerLaunch = strings.TrimSpace(payload.OwnerLaunch)
	if (payload.OwnerSession == "") != (payload.OwnerLaunch == "") {
		return runtimeRoute{}, errors.New("tmux runtime: invalid qualified handle owner")
	}
	owner := ports.SupervisedProcessRef{
		SessionID: domain.SessionID(payload.OwnerSession),
		LaunchID:  payload.OwnerLaunch,
	}
	if owner.SessionID != "" {
		ownerTmuxID, nameErr := tmuxSessionName(owner.SessionID)
		if nameErr != nil || (ownerTmuxID != payload.Session && string(owner.SessionID) != payload.Session) {
			return runtimeRoute{}, errors.New("tmux runtime: qualified handle owner does not match session route")
		}
	}
	hasServerPID := payload.TmuxServerPID != 0
	hasSessionID := payload.TmuxSessionID != ""
	hasPaneID := payload.TmuxPaneID != ""
	if hasServerPID != hasSessionID || hasServerPID != hasPaneID {
		return runtimeRoute{}, errors.New("tmux runtime: incomplete qualified handle object fence")
	}
	if hasServerPID && (payload.TmuxServerPID <= 1 ||
		!tmuxSessionIDPattern.MatchString(payload.TmuxSessionID) ||
		!tmuxPaneIDPattern.MatchString(payload.TmuxPaneID)) {
		return runtimeRoute{}, errors.New("tmux runtime: invalid qualified handle object fence")
	}
	target := socketTarget{
		kind:            payload.Target,
		value:           payload.Value,
		useLegacyBinary: payload.LegacyBinary,
	}
	switch target.kind {
	case socketTargetDefault:
		if payload.Value != "" {
			return runtimeRoute{}, errors.New("tmux runtime: default qualified handle has a socket value")
		}
	case socketTargetNamed:
		if strings.TrimSpace(target.value) == "" {
			return runtimeRoute{}, errors.New("tmux runtime: qualified named socket is empty")
		}
	case socketTargetPath:
		if strings.TrimSpace(target.value) == "" || !filepath.IsAbs(target.value) {
			return runtimeRoute{}, errors.New("tmux runtime: qualified socket path must be absolute")
		}
	default:
		return runtimeRoute{}, fmt.Errorf("tmux runtime: unknown qualified socket target %q", target.kind)
	}
	return runtimeRoute{
		id:            payload.Session,
		target:        target,
		qualified:     true,
		owner:         owner,
		tmuxServerPID: payload.TmuxServerPID,
		tmuxSessionID: payload.TmuxSessionID,
		tmuxPaneID:    payload.TmuxPaneID,
	}, nil
}

func validateHandleSessionID(id string) error {
	if id == "" {
		return errors.New("tmux runtime: session id is required")
	}
	if !sessionIDPattern.MatchString(id) {
		return fmt.Errorf("tmux runtime: invalid handle id %q", id)
	}
	return nil
}

// -- output detection helpers --

// sessionMissingOutput reports whether a non-zero `tmux has-session` exit is
// definitively "this session does not exist" — evidence about the probed
// session itself. Server-level failures deliberately do not match: "no server
// running" describes the whole server and "error connecting" is a transient
// socket failure; neither says anything about one session, so treating them as
// per-session death let a single server outage archive every session on the
// board (issue #3475).
func sessionMissingOutput(out string) bool {
	s := strings.ToLower(out)
	return strings.Contains(s, "can't find session") ||
		strings.Contains(s, "session not found")
}

// serverUnreachableOutput reports whether a non-zero tmux exit means the
// server itself could not be reached, which is inconclusive for any single
// session's liveness.
func serverUnreachableOutput(out string) bool {
	return serverNotRunningOutput(out) || transientServerFailureOutput(out)
}

func serverNotRunningOutput(out string) bool {
	s := strings.ToLower(out)
	return strings.Contains(s, "no server running")
}

// migrationSocketAbsentOutput identifies a named migration target whose Unix
// socket does not exist. This is definitive only for choosing whether to
// inspect the legacy default socket; it must not become per-session evidence
// of death, because the session may still be alive on that legacy server.
func migrationSocketAbsentOutput(out string) bool {
	s := strings.ToLower(out)
	return strings.Contains(s, "error connecting") &&
		strings.Contains(s, "no such file or directory")
}

func transientServerFailureOutput(out string) bool {
	s := strings.ToLower(out)
	return strings.Contains(s, "error connecting") ||
		strings.Contains(s, "protocol version mismatch") ||
		strings.Contains(s, "server exited unexpectedly")
}

// killSessionMissingOutput reports whether a non-zero `tmux kill-session`
// failed because the session was already gone. Teardown stays generous: a
// missing server also means there is nothing left to kill, so it shares the
// server-level patterns that liveness probing must not use.
func killSessionMissingOutput(out string) bool {
	return sessionMissingOutput(out) || serverUnreachableOutput(out)
}

// -- text helpers --

func chunks(s string, maxBytes int) []string {
	if s == "" {
		return []string{""}
	}
	if maxBytes <= 0 || len(s) <= maxBytes {
		return []string{s}
	}
	parts := []string{}
	for s != "" {
		if len(s) <= maxBytes {
			parts = append(parts, s)
			break
		}
		end := maxBytes
		for end > 0 && !utf8.ValidString(s[:end]) {
			end--
		}
		if end == 0 {
			_, size := utf8.DecodeRuneInString(s)
			end = size
		}
		parts = append(parts, s[:end])
		s = s[end:]
	}
	return parts
}

func tailLines(s string, n int) string {
	if n <= 0 || s == "" {
		return ""
	}
	lines := strings.SplitAfter(s, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) <= n {
		return s
	}
	return strings.Join(lines[len(lines)-n:], "")
}

func trimTrailingBlankLines(s string) string {
	if s == "" {
		return ""
	}
	lines := strings.SplitAfter(s, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	for len(lines) > 0 && strings.TrimRight(lines[len(lines)-1], "\r\n") == "" {
		lines = lines[:len(lines)-1]
	}
	return strings.Join(lines, "")
}

// -- env / quoting helpers --

func validateEnvKeys(env map[string]string) error {
	for key := range env {
		if !validEnvKey(key) {
			return fmt.Errorf("tmux runtime: invalid env key %q", key)
		}
	}
	return nil
}

func validEnvKey(key string) bool {
	if key == "" {
		return false
	}
	for i, r := range key {
		if r == '_' || (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') {
			continue
		}
		if i > 0 && r >= '0' && r <= '9' {
			continue
		}
		return false
	}
	return true
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

// buildLaunchCommand builds the shell command string passed to `sh -c`. It
// exports env vars and runs argv. Short-lived command terminals exit with the
// command; ordinary interactive runtimes keep the tmux session alive. Supervised
// launches park on a non-interpreting stdin sink after exit so bytes racing a
// process exit can never become shell commands; legacy/unsupervised launches
// retain the interactive-shell fallback used by manual recovery.
//
// PATH from cfg.Env is exported last, after all other keys, so an explicit
// override takes effect.
func buildLaunchCommand(cfg ports.RuntimeConfig) string {
	path := cfg.Env["PATH"]
	if path == "" {
		path = getenv("PATH")
	}

	var b strings.Builder
	b.WriteString("cd ")
	b.WriteString(shellQuote(cfg.WorkspacePath))
	b.WriteString(" || exit; ")
	if _, configured := cfg.Env["NO_COLOR"]; !configured {
		// The daemon may be launched from another agent or CI environment that
		// sets NO_COLOR for its own captured output. Do not leak that ambient
		// preference into an interactive terminal session. A project can still
		// opt out of color explicitly through its configured environment.
		b.WriteString("unset NO_COLOR; ")
	}
	for _, key := range sortedKeys(cfg.Env) {
		if key == "PATH" || key == "COLORTERM" {
			continue
		}
		b.WriteString("export ")
		b.WriteString(key)
		b.WriteString("=")
		b.WriteString(shellQuote(cfg.Env[key]))
		b.WriteString("; ")
	}
	// The AO web terminal and tmux attach client both support 24-bit SGR color.
	// Export this after caller env so agent color detection cannot accidentally
	// downgrade rich syntax/diff colors to ANSI-256.
	b.WriteString("export COLORTERM='truecolor'; ")
	if path != "" {
		b.WriteString("export PATH=")
		b.WriteString(shellQuote(path))
		b.WriteString("; ")
	}
	// Quote each argv word so spaces inside a word are preserved.
	parts := make([]string, len(cfg.Argv))
	for i, a := range cfg.Argv {
		parts[i] = shellQuote(a)
	}
	b.WriteString(strings.Join(parts, " "))
	if cfg.ExitOnCommandCompletion {
		// Let the tmux session disappear as soon as its one backend-owned command
		// completes. The terminal mux then emits `exited`, which drives exact
		// post-command work such as Codex account verification.
		b.WriteString(`; exit $?`)
	} else if cfg.Env["AO_SUPERVISED_PROCESS"] == "1" {
		// cat consumes and discards any input that arrived while the supervised
		// child was exiting. Runtime Restart/Destroy replaces or kills the pane.
		b.WriteString(`; exec cat >/dev/null`)
	} else {
		// Keep the tmux session alive after an unsupervised agent exits so the
		// operator can inspect it and use the historical manual-recovery shell.
		b.WriteString(`; exec "${SHELL:-/bin/sh}" -i`)
	}
	return b.String()
}

func sameDirectory(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	absA, errA := filepath.Abs(a)
	if errA == nil {
		a = absA
	}
	absB, errB := filepath.Abs(b)
	if errB == nil {
		b = absB
	}
	if realA, err := filepath.EvalSymlinks(a); err == nil {
		a = realA
	}
	if realB, err := filepath.EvalSymlinks(b); err == nil {
		b = realB
	}
	return filepath.Clean(a) == filepath.Clean(b)
}

// -- error type --

type commandError struct {
	err    error
	output string
}

func (e commandError) Error() string {
	if e.output == "" {
		return e.err.Error()
	}
	return e.err.Error() + ": " + e.output
}

func (e commandError) Unwrap() error { return e.err }
