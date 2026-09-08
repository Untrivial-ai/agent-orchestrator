package shellterm

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/aoagents/agent-orchestrator/backend/internal/agentlaunch"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// ShellRuntime is the slice of the runtime adapter a shell terminal needs:
// spawn a PTY around an argv, exchange reviewed auth input, tear it down, and
// answer whether it is still alive.
type ShellRuntime interface {
	Create(ctx context.Context, cfg ports.RuntimeConfig) (ports.RuntimeHandle, error)
	Destroy(ctx context.Context, handle ports.RuntimeHandle) error
	GetOutput(ctx context.Context, handle ports.RuntimeHandle, lines int) (string, error)
	SendInput(ctx context.Context, handle ports.RuntimeHandle, input string) error
	SendMessage(ctx context.Context, handle ports.RuntimeHandle, message string) error
	IsAlive(ctx context.Context, handle ports.RuntimeHandle) (bool, error)
}

// ProjectRootLocator resolves a project id to the directory a shell should
// start in. The daemon wiring adapts the project service to it.
type ProjectRootLocator interface {
	ProjectRoot(ctx context.Context, id domain.ProjectID) (string, error)
}

// SessionWorkspaceLocator resolves a session id to the workspace it is
// currently running in, plus the project it belongs to. The project id lets
// the caller fall back to the project root when the session has no workspace
// of its own yet. The daemon wiring adapts the session service to it.
type SessionWorkspaceLocator interface {
	SessionWorkspace(ctx context.Context, id domain.SessionID) (workspacePath string, projectID domain.ProjectID, err error)
}

// Service opens, lists, and closes standalone shell terminals.
//
// appRunID is minted once per desktop-app launch and is the mechanism behind
// the feature's lifetime rule: shells must survive a DAEMON restart but die
// with the APP. Rows tagged with the current run are re-attachable; rows tagged
// with any other run are orphans from an app that exited without closing them
// (a crash or force-kill, where the clean shutdown path never ran) and are
// destroyed at boot by ReapShellTerminalsFromPreviousAppRuns.
type Service struct {
	runtime  ShellRuntime
	store    Store
	projects ProjectRootLocator
	sessions SessionWorkspaceLocator
	dataDir  string
	appRunID string
	log      *slog.Logger

	// now and newHandleID are injectable so tests can assert on exact ids and
	// timestamps without a clock or entropy dependency.
	now         func() time.Time
	newHandleID func() (string, error)
	executable  func() (string, error)

	// gatesMu guards gates itself (the map), not the individual gate mutexes it
	// holds.
	gatesMu sync.Mutex
	// gates holds one entry per session with an in-flight or completed
	// BeginSessionTeardown/OpenShellTerminal gate acquisition. OpenShellTerminal
	// validates a session id BEFORE calling sessionGateFor, so an invalid id
	// never allocates an entry here — only real sessions do, bounding growth to
	// the shape AO's single-user daemon actually runs.
	gates map[domain.SessionID]*sessionGate

	// onSessionGateWait, when set, is called the instant a session-scoped
	// OpenShellTerminal or CloseShellTerminal is about to attempt gate.mu.Lock()
	// — before the (possibly blocking) call, so it fires whether or not the
	// lock is free. Nil in production; tests use it to know a call has
	// genuinely reached the acquisition point before asserting it's blocked
	// there, rather than inferring "still blocked" from a goroutine that simply
	// hasn't run yet.
	onSessionGateWait func(domain.SessionID)
}

// sessionGate is the admission/teardown barrier for one session's scoped
// shell terminals. It is held for the ENTIRE span from BeginSessionTeardown
// through the release function it returns on success — deliberately crossing
// the call into Session Manager's own worktree teardown in between — so an
// OpenShellTerminal racing against that window waits until the window ends,
// rather than slipping a new shell in against a worktree that is mid-removal.
// By the time a waiter acquires the gate, the teardown it waited on has fully
// finished (destroyed or preserved), so there is nothing left to check here:
// resolveShellTerminalWorkingDir's own existence check is what decides whether
// that Open lands in the worktree or falls back.
//
// The gate is a capacity-1 channel rather than a sync.Mutex because
// sync.Mutex.Lock is uninterruptible: a teardown whose workspace.Destroy
// stalls (hung git subprocess, a Windows directory handle that never releases)
// would park every waiter forever, and an HTTP handler blocked in Lock cannot
// observe its client disconnecting or its context deadline. A channel send is
// selectable, so acquire can honour both.
type sessionGate struct {
	ch chan struct{}
}

func newSessionGate() *sessionGate {
	return &sessionGate{ch: make(chan struct{}, 1)}
}

// sessionGateWaitTimeout bounds how long a contended acquire waits before
// giving up. A teardown holding the gate is doing real filesystem work
// (worktree removal), so the budget is generous — but finite, so a wedged
// teardown surfaces as a retryable 409 instead of a request that never
// returns. A var so tests need not wait it out.
var sessionGateWaitTimeout = 30 * time.Second

// acquire takes the gate, returning a release function tied to THIS
// acquisition (calling it more than once is a no-op, and it can never release
// a different acquisition's hold). It gives up when ctx is done or the wait
// budget expires, so no caller parks indefinitely behind a stalled teardown.
func (g *sessionGate) acquire(ctx context.Context, id domain.SessionID) (release func(), err error) {
	var once sync.Once
	releaseFn := func() { once.Do(func() { <-g.ch }) }

	// Fast path: uncontended, no timer allocation.
	select {
	case g.ch <- struct{}{}:
		return releaseFn, nil
	default:
	}

	timer := time.NewTimer(sessionGateWaitTimeout)
	defer timer.Stop()
	select {
	case g.ch <- struct{}{}:
		return releaseFn, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("shell terminal gate for session %s: %w", id, ctx.Err())
	case <-timer.C:
		return nil, apierr.Conflict("SHELL_TERMINAL_SESSION_BUSY",
			"This session's shell terminals are being torn down; try again in a moment", nil)
	}
}

// NewService builds the shell terminal service. dataDir is the fallback working
// directory for a shell opened with no project context. A nil logger falls back
// to slog.Default.
func NewService(runtime ShellRuntime, store Store, projects ProjectRootLocator, sessions SessionWorkspaceLocator, dataDir, appRunID string, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{
		runtime:     runtime,
		store:       store,
		projects:    projects,
		sessions:    sessions,
		dataDir:     dataDir,
		appRunID:    appRunID,
		log:         log,
		now:         time.Now,
		newHandleID: newShellTerminalHandleID,
		executable:  os.Executable,
		gates:       map[domain.SessionID]*sessionGate{},
	}
}

func (s *Service) pinnedEnv() map[string]string {
	env := make(map[string]string)
	if err := agentlaunch.PinCLI(env, s.executable); err != nil {
		s.log.Warn("canonical AO CLI unavailable in shell terminal", "err", err)
	}
	path, err := agentlaunch.PinnedPATH(s.executable, os.Getenv, nil, s.dataDir)
	if err != nil {
		s.log.Warn("shell terminal PATH not pinned to the daemon binary; a bare `ao` may resolve to a different install", "err", err)
		return env
	}
	env["PATH"] = path
	return env
}

// sessionGateFor returns the gate for id, creating it on first use.
func (s *Service) sessionGateFor(id domain.SessionID) *sessionGate {
	s.gatesMu.Lock()
	defer s.gatesMu.Unlock()
	g, ok := s.gates[id]
	if !ok {
		g = newSessionGate()
		s.gates[id] = g
	}
	return g
}

// acquireSessionGate resolves and takes a session's gate in one step, firing
// the test hook at the moment the wait begins.
func (s *Service) acquireSessionGate(ctx context.Context, id domain.SessionID) (release func(), err error) {
	gate := s.sessionGateFor(id)
	if s.onSessionGateWait != nil {
		s.onSessionGateWait(id)
	}
	return gate.acquire(ctx, id)
}

// OpenShellTerminal spawns a shell PTY and records it against the current app
// run. The runtime is created BEFORE the row is written, and rolled back if the
// write fails, so a persisted row always names a PTY that actually exists —
// otherwise a restart would try to re-attach to a handle that was never spawned.
//
// A session-scoped open holds that session's gate for the whole call: without
// this, a shell could be inserted between BeginSessionTeardown's snapshot read
// and Session Manager's own worktree destroy a moment later, landing a fresh
// shell in a directory that is about to disappear. Holding the gate here means
// the open either runs entirely before a teardown starts, or blocks until the
// teardown (and the gate) releases — at which point resolveShellTerminalWorkingDir's
// existence check sees the worktree is gone and falls back to the project root.
func (s *Service) OpenShellTerminal(ctx context.Context, in OpenShellTerminalInput) (ShellTerminal, error) {
	if in.SessionID != "" {
		if s.sessions == nil {
			return ShellTerminal{}, apierr.Internal("SHELL_TERMINAL_NO_SESSION_LOOKUP", "Session lookup is unavailable")
		}
		// Validate the session exists BEFORE allocating a gate for it: a stream
		// of requests naming made-up session ids would otherwise grow the gate
		// map forever, since nothing else ever removes an entry. This lookup's
		// result is deliberately discarded — resolveShellTerminalWorkingDir
		// below re-resolves inside the gate, where a concurrent teardown can't
		// race it. An unknown session errors the same way it would have further
		// down, just before touching any gate state.
		if _, _, err := s.sessions.SessionWorkspace(ctx, in.SessionID); err != nil {
			return ShellTerminal{}, fmt.Errorf("open shell terminal: resolve session %s: %w", in.SessionID, err)
		}
		release, err := s.acquireSessionGate(ctx, in.SessionID)
		if err != nil {
			return ShellTerminal{}, err
		}
		defer release()
	}
	workingDir, projectID, err := s.resolveShellTerminalWorkingDir(ctx, in.ProjectID, in.SessionID)
	if err != nil {
		return ShellTerminal{}, err
	}
	openTerminals, err := s.store.SelectShellTerminalsByAppRunID(ctx, s.appRunID)
	if err != nil {
		return ShellTerminal{}, fmt.Errorf("open shell terminal: list existing terminals: %w", err)
	}
	argv, usedFallback := resolveUserLoginShell(in.Shell)
	if usedFallback {
		return ShellTerminal{}, apierr.Invalid("SHELL_TERMINAL_SHELL_UNAVAILABLE",
			fmt.Sprintf("The selected shell is unavailable: %s. Choose another shell in Settings.", in.Shell), nil)
	}
	if len(argv) == 0 {
		return ShellTerminal{}, apierr.Internal("SHELL_TERMINAL_NO_SHELL",
			"Could not determine a shell to launch. Set SHELL (macOS/Linux) or ComSpec (Windows).")
	}
	return s.openTerminal(ctx, openTerminalConfig{
		argv:       argv,
		env:        s.pinnedEnv(),
		projectID:  projectID,
		sessionID:  in.SessionID,
		workingDir: workingDir,
		title:      nextShellTerminalTitle(openTerminals),
	})
}

const authWorkspaceDirectoryName = "auth-workspace"

// OpenCommandTerminal opens a daemon-trusted command in a standalone terminal.
// Unlike OpenShellTerminal, it never receives public HTTP input. Interactive
// coding agents start in a dedicated workspace so they cannot mistake the
// daemon's database/config/runtime directory for a project.
func (s *Service) OpenCommandTerminal(ctx context.Context, in OpenCommandTerminalInput) (ShellTerminal, error) {
	if err := validateOpenCommandTerminalInput(in); err != nil {
		return ShellTerminal{}, err
	}
	handleID, err := s.newHandleID()
	if err != nil {
		return ShellTerminal{}, fmt.Errorf("open command terminal: handle id: %w", err)
	}

	workingDir := in.WorkingDir
	cleanupWorkingDirOnError := false
	if workingDir == "" {
		authWorkspaceRoot := filepath.Join(s.dataDir, authWorkspaceDirectoryName)
		if err := os.MkdirAll(authWorkspaceRoot, 0o700); err != nil {
			return ShellTerminal{}, fmt.Errorf("open command terminal: create auth workspace: %w", err)
		}
		workingDir = filepath.Join(authWorkspaceRoot, handleID)
		if err := os.Mkdir(workingDir, 0o700); err != nil {
			return ShellTerminal{}, fmt.Errorf("open command terminal: create private auth workspace: %w", err)
		}
		cleanupWorkingDirOnError = true
	}
	terminal, err := s.openTerminal(ctx, openTerminalConfig{
		handleID:                 handleID,
		argv:                     in.Argv,
		env:                      in.Env,
		workingDir:               workingDir,
		title:                    in.Title,
		exitOnCommandCompletion:  true,
		cleanupWorkingDirOnError: cleanupWorkingDirOnError,
	})
	if err != nil {
		return ShellTerminal{}, err
	}
	if len(in.InitialInputReadyStates) > 0 {
		go s.sendInitialInputWhenReady(context.WithoutCancel(ctx), ports.RuntimeHandle{ID: terminal.HandleID}, in.InitialInput, in.InitialInputReadyStates)
	}
	return terminal, nil
}

const (
	initialInputTimeout      = 10 * time.Second
	initialInputPollInterval = 50 * time.Millisecond
	initialInputOutputLines  = 100
)

func (s *Service) sendInitialInputWhenReady(ctx context.Context, handle ports.RuntimeHandle, input string, readyStates []InitialInputReadyState) {
	ctx, cancel := context.WithTimeout(ctx, initialInputTimeout)
	defer cancel()
	ticker := time.NewTicker(initialInputPollInterval)
	defer ticker.Stop()
	for {
		output, err := s.runtime.GetOutput(ctx, handle, initialInputOutputLines)
		if err == nil {
			var ready *InitialInputReadyState
			for i := range readyStates {
				if strings.Contains(output, readyStates[i].Text) {
					ready = &readyStates[i]
					break
				}
			}
			if ready == nil {
				goto wait
			}
			if ready.RawPrefix != "" {
				if err := s.runtime.SendInput(ctx, handle, ready.RawPrefix); err != nil {
					s.log.Warn("authentication terminal initial input prefix failed", "handleId", handle.ID, "error", err)
					return
				}
			}
			if err := s.runtime.SendMessage(ctx, handle, input); err != nil {
				s.log.Warn("authentication terminal initial input failed", "handleId", handle.ID, "error", err)
			}
			return
		}
	wait:
		select {
		case <-ctx.Done():
			s.log.Warn("authentication terminal did not become ready for initial input", "handleId", handle.ID)
			return
		case <-ticker.C:
		}
	}
}

type openTerminalConfig struct {
	handleID                 string
	argv                     []string
	env                      map[string]string
	projectID                domain.ProjectID
	sessionID                domain.SessionID
	workingDir               string
	title                    string
	exitOnCommandCompletion  bool
	cleanupWorkingDirOnError bool
}

// openTerminal creates and persists a terminal, rolling the runtime back on
// every failure after Create so an untracked PTY cannot leak.
func (s *Service) openTerminal(ctx context.Context, cfg openTerminalConfig) (ShellTerminal, error) {
	handleID := cfg.handleID
	if handleID == "" {
		var err error
		handleID, err = s.newHandleID()
		if err != nil {
			return ShellTerminal{}, fmt.Errorf("open shell terminal: handle id: %w", err)
		}
	}

	// SessionID is the runtime adapters' name for "what to call this PTY"; it
	// is not a session row and no sessions record is ever created. The
	// shellterm- prefix keeps the two namespaces disjoint.
	handle, err := s.runtime.Create(ctx, ports.RuntimeConfig{
		SessionID:               domain.SessionID(handleID),
		WorkspacePath:           cfg.workingDir,
		Argv:                    cfg.argv,
		Env:                     cfg.env,
		ExitOnCommandCompletion: cfg.exitOnCommandCompletion,
	})
	if err != nil {
		if cfg.cleanupWorkingDirOnError {
			s.cleanupAuthWorkspace(cfg.workingDir, handleID)
		}
		return ShellTerminal{}, fmt.Errorf("open shell terminal %s: runtime: %w", handleID, err)
	}

	rec := ShellTerminalRecord{
		HandleID: handle.ID,
		// The resolved project, not the requested one: a session-scoped open
		// that named no project still belongs to the session's project, and
		// persisting "" there would leave the row unattributable on the board.
		ProjectID:  cfg.projectID,
		SessionID:  cfg.sessionID,
		WorkingDir: cfg.workingDir,
		Title:      cfg.title,
		AppRunID:   s.appRunID,
		CreatedAt:  s.now().UTC(),
	}
	if err := s.store.InsertShellTerminal(ctx, rec); err != nil {
		stillAlive, destroyErr := s.destroyRuntimeConfirmed(context.WithoutCancel(ctx), handle)
		if stillAlive {
			s.log.Warn("shell terminal rollback failed; retaining workspace for live runtime",
				"handleId", handle.ID, "workingDir", cfg.workingDir, "error", destroyErr)
		} else if cfg.cleanupWorkingDirOnError {
			s.cleanupAuthWorkspace(cfg.workingDir, handle.ID)
		}
		return ShellTerminal{}, fmt.Errorf("open shell terminal %s: persist: %w", handle.ID, err)
	}
	s.log.Info("shell terminal opened", "handleId", handle.ID, "workingDir", cfg.workingDir)
	return shellTerminalFromRecord(rec), nil
}

// nextShellTerminalTitle assigns a name at creation time. It never depends on
// a tab's current UI position, so closing or reordering another tab cannot
// rename an existing terminal.
func nextShellTerminalTitle(terminals []ShellTerminalRecord) string {
	maxNumber := 0
	for _, terminal := range terminals {
		if terminal.Title == "Terminal" {
			if maxNumber < 1 {
				maxNumber = 1
			}
			continue
		}
		var number int
		if _, err := fmt.Sscanf(terminal.Title, "Terminal %d", &number); err == nil && number > maxNumber {
			maxNumber = number
		}
	}
	return fmt.Sprintf("Terminal %d", maxNumber+1)
}

// maxShellTerminalTitleLen bounds a user-supplied tab name. Tabs are truncated
// in the UI anyway; this only stops an unbounded string reaching the DB.
const maxShellTerminalTitleLen = 80

func validateOpenCommandTerminalInput(in OpenCommandTerminalInput) error {
	if len(in.Argv) == 0 {
		return apierr.Invalid("SHELL_TERMINAL_COMMAND_REQUIRED", "A shell terminal command is required", nil)
	}
	if strings.TrimSpace(in.Title) == "" {
		return apierr.Invalid("SHELL_TERMINAL_TITLE_REQUIRED", "A shell terminal title is required", nil)
	}
	if utf8.RuneCountInString(in.Title) > maxShellTerminalTitleLen {
		return apierr.Invalid("SHELL_TERMINAL_TITLE_TOO_LONG",
			fmt.Sprintf("A shell terminal title must be at most %d characters", maxShellTerminalTitleLen), nil)
	}
	if in.InitialInput != "" && len(in.InitialInputReadyStates) == 0 {
		return apierr.Invalid("SHELL_TERMINAL_INITIAL_INPUT_READY_TEXT_REQUIRED", "A reviewed readiness marker is required for automatic terminal input", nil)
	}
	for _, state := range in.InitialInputReadyStates {
		if strings.TrimSpace(state.Text) == "" {
			return apierr.Invalid("SHELL_TERMINAL_INITIAL_INPUT_READY_TEXT_REQUIRED", "A reviewed readiness marker is required for automatic terminal input", nil)
		}
	}
	return nil
}

// RenameShellTerminal sets a shell terminal's tab title. The title is trimmed
// and must be non-empty and within the length bound; an unknown handle is a 404.
func (s *Service) RenameShellTerminal(ctx context.Context, handleID, title string) (ShellTerminal, error) {
	if handleID == "" {
		return ShellTerminal{}, apierr.Invalid("SHELL_TERMINAL_ID_REQUIRED", "A shell terminal id is required", nil)
	}
	title = strings.TrimSpace(title)
	if title == "" {
		return ShellTerminal{}, apierr.Invalid("SHELL_TERMINAL_TITLE_REQUIRED", "A shell terminal title is required", nil)
	}
	if utf8.RuneCountInString(title) > maxShellTerminalTitleLen {
		return ShellTerminal{}, apierr.Invalid("SHELL_TERMINAL_TITLE_TOO_LONG",
			fmt.Sprintf("A shell terminal title must be at most %d characters", maxShellTerminalTitleLen), nil)
	}
	rec, found, err := s.store.UpdateShellTerminalTitle(ctx, handleID, title)
	if err != nil {
		return ShellTerminal{}, fmt.Errorf("rename shell terminal %s: %w", handleID, err)
	}
	if !found {
		return ShellTerminal{}, apierr.NotFound("SHELL_TERMINAL_NOT_FOUND", "No such shell terminal: "+handleID)
	}
	s.log.Info("shell terminal renamed", "handleId", handleID)
	return shellTerminalFromRecord(rec), nil
}

// CloseShellTerminal destroys a shell's PTY and forgets it — but only once
// death is confirmed (see destroyConfirmed): a shell that survives Destroy
// keeps its row, so it stays visible/re-attachable instead of vanishing from
// tracking while still running against a directory a later session teardown
// might remove.
//
// A session-scoped shell is closed under that session's gate — the same one
// BeginSessionTeardown takes — so this can't race a concurrent worktree
// teardown for the same session: whichever gets the gate first runs to
// completion before the other's SELECT (here, an unconditional destroy;
// there, SelectShellTerminalsBySessionID) can observe a half-finished state.
// Without this, a close whose row deletion raced BeginSessionTeardown's scan
// could vanish from that scan and leave a still-alive shell invisible to it
// while the worktree it's rooted in gets removed anyway.
func (s *Service) CloseShellTerminal(ctx context.Context, handleID string) error {
	if handleID == "" {
		return apierr.Invalid("SHELL_TERMINAL_ID_REQUIRED", "A shell terminal id is required", nil)
	}
	rec, found, err := s.store.SelectShellTerminalByHandleID(ctx, handleID)
	if err != nil {
		return fmt.Errorf("close shell terminal %s: %w", handleID, err)
	}
	if !found {
		return apierr.NotFound("SHELL_TERMINAL_NOT_FOUND", "No such shell terminal: "+handleID)
	}

	if rec.SessionID != "" {
		release, err := s.acquireSessionGate(ctx, rec.SessionID)
		if err != nil {
			return err
		}
		defer release()
	}

	stillAlive, destroyErr := s.destroyConfirmed(ctx, rec)
	if stillAlive {
		s.log.Warn("close shell terminal: runtime still alive after destroy", "handleId", handleID, "error", destroyErr)
		return apierr.Conflict("SHELL_TERMINAL_STILL_RUNNING",
			"The shell process is still running; try closing it again in a moment", nil)
	}
	return nil
}

// ListShellTerminalsForCurrentAppRun returns the shells the running app owns,
// dropping any whose PTY has died (the user typed `exit`, or the machine
// rebooted out from under a persisted row). Dead rows are deleted as they are
// found, so the list the UI renders only ever contains attachable panes.
//
// A liveness probe that ERRORS is not treated as proof of death — the same rule
// internal/terminal applies on attach — so a transient runtime hiccup cannot
// silently delete a working terminal.
func (s *Service) ListShellTerminalsForCurrentAppRun(ctx context.Context) ([]ShellTerminal, error) {
	recs, err := s.store.SelectShellTerminalsByAppRunID(ctx, s.appRunID)
	if err != nil {
		return nil, fmt.Errorf("list shell terminals: %w", err)
	}
	out := make([]ShellTerminal, 0, len(recs))
	for _, rec := range recs {
		alive, err := s.runtime.IsAlive(ctx, ports.RuntimeHandle{ID: rec.HandleID})
		if err != nil {
			s.log.Warn("shell terminal liveness probe failed; keeping row",
				"handleId", rec.HandleID, "error", err)
			out = append(out, shellTerminalFromRecord(rec))
			continue
		}
		if !alive {
			if deleted, delErr := s.store.DeleteShellTerminalByHandleID(ctx, rec.HandleID); delErr != nil {
				s.log.Warn("pruning dead shell terminal failed", "handleId", rec.HandleID, "error", delErr)
			} else if deleted {
				s.cleanupAuthWorkspace(rec.WorkingDir, rec.HandleID)
			}
			continue
		}
		out = append(out, shellTerminalFromRecord(rec))
	}
	return out, nil
}

// ReapShellTerminalsFromPreviousAppRuns destroys shells left behind by an
// earlier app run and returns how many rows it cleared. This is the half of the
// lifetime rule the clean shutdown path cannot cover: when the app crashes or
// is force-killed, nothing gets to close its terminals, so they are swept here
// on the next boot instead of leaking forever.
//
// Runtime teardown is per-handle and confirmed-dead (see destroyConfirmed): one
// un-destroyable, still-alive PTY does not stop the rest from being reaped, but
// its own row is deliberately kept rather than blindly wiped — a boot-time
// reconciliation pass reading this session's shells later must still be able
// to see it before removing the worktree it points at.
func (s *Service) ReapShellTerminalsFromPreviousAppRuns(ctx context.Context) (int64, error) {
	orphans, err := s.store.SelectShellTerminalsFromPreviousAppRuns(ctx, s.appRunID)
	if err != nil {
		return 0, fmt.Errorf("reap shell terminals: %w", err)
	}
	var cleared int64
	for _, rec := range orphans {
		stillAlive, destroyErr := s.destroyConfirmed(ctx, rec)
		if stillAlive {
			s.log.Warn("reaping orphaned shell terminal: runtime still alive after destroy",
				"handleId", rec.HandleID, "appRunId", rec.AppRunID, "error", destroyErr)
			continue
		}
		cleared++
	}
	if cleared > 0 {
		s.log.Info("reaped shell terminals from previous app runs", "count", cleared)
	}
	return cleared, nil
}

// destroyConfirmed is the one place a shell terminal's row is allowed to
// disappear: it destroys the runtime behind handleID and deletes the row only
// once death is confirmed, so CloseShellTerminal, ReapShellTerminalsFromPreviousAppRuns,
// and BeginSessionTeardown can't each independently forget a shell that
// actually survived.
//
//   - A clean Destroy is confirmed dead.
//   - A Destroy error is followed by an IsAlive check; an IsAlive error is
//     treated the same as "alive" (unknown state must never let a live shell's
//     row vanish) — only an explicit "not alive" counts as confirmed dead.
//
// Returns stillAlive=true when the row was deliberately kept because death
// could not be confirmed. Callers that need 404-for-unknown-handle semantics
// (CloseShellTerminal) look the row up themselves beforehand — by the time
// destroyConfirmed runs, the handle is already known to exist.
func (s *Service) destroyConfirmed(ctx context.Context, rec ShellTerminalRecord) (stillAlive bool, destroyErr error) {
	handleID := rec.HandleID
	stillAlive, destroyErr = s.destroyRuntimeConfirmed(ctx, ports.RuntimeHandle{ID: handleID})
	if stillAlive {
		return true, destroyErr
	}
	if deleted, err := s.store.DeleteShellTerminalByHandleID(ctx, handleID); err != nil {
		s.log.Warn("shell terminal: delete row after destroy failed", "handleId", handleID, "error", err)
	} else if deleted {
		s.cleanupAuthWorkspace(rec.WorkingDir, rec.HandleID)
	}
	return false, nil
}

func (s *Service) destroyRuntimeConfirmed(ctx context.Context, handle ports.RuntimeHandle) (stillAlive bool, destroyErr error) {
	destroyErr = s.runtime.Destroy(ctx, handle)
	if destroyErr == nil {
		return false, nil
	}
	alive, aliveErr := s.runtime.IsAlive(ctx, handle)
	if aliveErr != nil || alive {
		return true, destroyErr
	}
	return false, destroyErr
}

func (s *Service) cleanupAuthWorkspace(workingDir, handleID string) {
	root := filepath.Clean(filepath.Join(s.dataDir, authWorkspaceDirectoryName))
	rel, err := filepath.Rel(root, filepath.Clean(workingDir))
	if err != nil || !strings.HasPrefix(rel, "shellterm-") || filepath.IsAbs(rel) || filepath.Dir(rel) != "." {
		return
	}
	// Runtime handles are opaque and some adapters wrap the requested launch
	// id (for example ptyhost-v1:shellterm-* on macOS). Only delete the direct
	// child whose generated id is the complete handle or its final component.
	if handleID != rel && !strings.HasSuffix(handleID, ":"+rel) {
		return
	}
	if err := os.RemoveAll(workingDir); err != nil {
		s.log.Warn("shell terminal: remove private auth workspace failed", "workingDir", workingDir, "error", err)
	}
}

// BeginSessionTeardown drains every shell terminal scoped to a session and
// locks out new ones, ahead of Session Manager tearing down the session's
// worktree (Kill, Cleanup, RetireForReplacement, the reconcile/shutdown
// save-and-teardown path). It is the write side of the same gate
// OpenShellTerminal reads: taking the gate here is what makes a concurrent
// Open either finish first or wait until the gate releases, so nothing can
// insert a new shell terminal into a worktree that is about to disappear.
//
// A runtime that cannot be confirmed dead (see destroyConfirmed) is NOT
// deleted — its row survives so the terminal is still visible/re-attachable —
// and its error is folded into the returned aggregate. On error the gate is
// already released and the caller MUST NOT touch the worktree: some scoped
// shell may still be reading or writing under it.
//
// On success this returns a release function the caller MUST call exactly
// once (typically via defer) once its own worktree work finishes, whatever
// the outcome — release is tied to THIS acquisition specifically (a fresh
// closure over this exact lock), so there is no way for a call meant for a
// different Begin, or a duplicate/stray call, to release a lock it doesn't
// own. Never call EndSessionTeardown-style bookkeeping by session id alone:
// that was the earlier design, and it let an unmatched release for session X
// unlock a DIFFERENT, still in-flight teardown for the same X.
func (s *Service) BeginSessionTeardown(ctx context.Context, sessionID domain.SessionID) (release func(), err error) {
	release, err = s.acquireSessionGate(ctx, sessionID)
	if err != nil {
		return nil, err
	}

	recs, err := s.store.SelectShellTerminalsBySessionID(ctx, sessionID)
	if err != nil {
		release()
		return nil, fmt.Errorf("close shell terminals for session %s: %w", sessionID, err)
	}

	var stillAlive []error
	for _, rec := range recs {
		alive, destroyErr := s.destroyConfirmed(ctx, rec)
		if alive {
			s.log.Warn("close shell terminal for session: runtime still alive after destroy",
				"sessionID", sessionID, "handleId", rec.HandleID, "error", destroyErr)
			stillAlive = append(stillAlive, fmt.Errorf("%s: %w", rec.HandleID, destroyErr))
		}
	}

	if len(stillAlive) > 0 {
		release()
		return nil, fmt.Errorf("close shell terminals for session %s: %d still alive: %w",
			sessionID, len(stillAlive), errors.Join(stillAlive...))
	}
	return release, nil
}

// resolveShellTerminalWorkingDir picks where the shell starts. A session id
// takes precedence: the shell lands in that session's live workspace (its
// worktree), so it stays colocated with the agent even though that worktree
// differs from the project's registered root. A session with no workspace yet
// (or no session id at all) falls back to the project root, then the daemon's
// data dir.
// It also returns the project the shell ended up attributed to, which is not
// always the requested one: a session-scoped open may carry no project id, and
// the session's own project is what the persisted row should record.
func (s *Service) resolveShellTerminalWorkingDir(ctx context.Context, projectID domain.ProjectID, sessionID domain.SessionID) (workingDir string, resolvedProjectID domain.ProjectID, err error) {
	if sessionID != "" {
		if s.sessions == nil {
			return "", "", apierr.Internal("SHELL_TERMINAL_NO_SESSION_LOOKUP", "Session lookup is unavailable")
		}
		workspacePath, sessionProjectID, err := s.sessions.SessionWorkspace(ctx, sessionID)
		if err != nil {
			return "", "", fmt.Errorf("open shell terminal: resolve session %s: %w", sessionID, err)
		}
		if sessionProjectID != "" {
			projectID = sessionProjectID
		}
		if workspacePath != "" {
			return workspacePath, projectID, nil
		}
	}
	dir, err := s.resolveProjectRootOrDataDir(ctx, projectID)
	if err != nil {
		return "", "", err
	}
	return dir, projectID, nil
}

// resolveProjectRootOrDataDir picks the project root when a project is named,
// else the daemon's data dir.
func (s *Service) resolveProjectRootOrDataDir(ctx context.Context, projectID domain.ProjectID) (string, error) {
	if projectID == "" {
		if s.dataDir == "" {
			return "", apierr.Internal("SHELL_TERMINAL_NO_WORKING_DIR",
				"No project selected and the daemon has no data dir to fall back to")
		}
		return s.dataDir, nil
	}
	if s.projects == nil {
		return "", apierr.Internal("SHELL_TERMINAL_NO_PROJECT_LOOKUP",
			"Project lookup is unavailable")
	}
	root, err := s.projects.ProjectRoot(ctx, projectID)
	if err != nil {
		return "", fmt.Errorf("open shell terminal: resolve project %s: %w", projectID, err)
	}
	if root == "" {
		return "", apierr.NotFound("SHELL_TERMINAL_PROJECT_NOT_FOUND",
			"No such project: "+string(projectID))
	}
	return root, nil
}

// newShellTerminalHandleID mints a runtime handle id for a shell pane.
//
// The shellterm- prefix keeps shell handles trivially distinguishable from
// session handles in logs, the DB, and the mux. The character set is
// constrained by the runtime adapters, which are stricter than they look:
// conpty rejects anything outside ^[a-zA-Z0-9_-]+$ and tmux uses the id as a
// session name — so hex, not base64.
func newShellTerminalHandleID() (string, error) {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return "shellterm-" + hex.EncodeToString(buf), nil
}

func shellTerminalFromRecord(rec ShellTerminalRecord) ShellTerminal {
	return ShellTerminal{
		HandleID:   rec.HandleID,
		ProjectID:  rec.ProjectID,
		SessionID:  rec.SessionID,
		WorkingDir: rec.WorkingDir,
		Title:      rec.Title,
		CreatedAt:  rec.CreatedAt,
	}
}
