// Package sessionmanager drives internal session command operations over runtime,
// agent, workspace, storage, messenger, and lifecycle dependencies.
package sessionmanager

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	aoprocess "github.com/aoagents/agent-orchestrator/backend/internal/process"
	"github.com/aoagents/agent-orchestrator/backend/internal/sessionguard"
	"github.com/aoagents/agent-orchestrator/backend/internal/skillassets"
)

// Sentinel errors returned by the Session Manager; callers match them with
// errors.Is.
var (
	ErrNotFound         = errors.New("session: not found")
	ErrNotRestorable    = errors.New("session: not restorable (not terminal)")
	ErrTerminated       = errors.New("session: terminated")
	ErrAgentExited      = errors.New("session: agent exited")
	ErrAgentNotExited   = errors.New("session: agent has not exited")
	ErrIncompleteHandle = errors.New("session: incomplete teardown handle")
	// ErrProjectNotResolvable means the spawn's project has no usable repo
	// (unregistered, archived, or missing a path). The API maps it to a 400.
	ErrProjectNotResolvable = errors.New("session: project repo not resolvable")
	// ErrUnknownHarness means the requested agent harness has no registered
	// adapter. The API maps it to a 400 so a typo'd `--harness` is a validation
	// error, not an opaque 500.
	ErrUnknownHarness = errors.New("session: unknown agent harness")
	// ErrMissingHarness means neither the spawn request nor the project's role
	// config selected an agent. Worker/orchestrator spawns must be explicit.
	ErrMissingHarness = errors.New("session: agent harness required")
	// ErrScratchBranchUnsupported means a caller tried to force git branch
	// semantics onto a scratch project.
	ErrScratchBranchUnsupported = errors.New("session: scratch projects do not support branches")
	// ErrNotResumable means a terminated session cannot be relaunched: its adapter
	// cannot natively resume it AND it has no prompt to fresh-launch from, and it is
	// not an orchestrator (orchestrators are promptless by design and relaunch fresh
	// with the system prompt only). Workers without a task and without a native
	// session id have nothing meaningful to restore.
	ErrNotResumable = errors.New("session: nothing to resume from")
	// ErrSwitchInProgress means an agent switch is already running for this
	// session. The API maps it to a 409 so a double-submit does not race two
	// teardown/relaunch cycles over one worktree.
	ErrSwitchInProgress = errors.New("session: switch already in progress")
	// ErrInterfaceHandoffUnsupported means the harness has not proven that its
	// TUI resume identity and Chat protocol identity name the same conversation.
	ErrInterfaceHandoffUnsupported = errors.New("session: interface handoff unsupported")
	// ErrNativeConversationMissing means a supported harness has not yet exposed
	// the native id required to resume it through the other controller.
	ErrNativeConversationMissing = errors.New("session: native conversation id unavailable")
	// ErrInterfaceAlreadySelected makes a stale/double switch request an explicit
	// conflict instead of leaking a generic 500 after the first switch commits.
	ErrInterfaceAlreadySelected = errors.New("session: requested interface is already selected")
	// ErrInterfaceTransitionNotFound distinguishes a missing handoff from a
	// missing session when DELETE is retried after the transition settled.
	ErrInterfaceTransitionNotFound = errors.New("session: no active interface transition")
	// ErrInterfaceTransitionNotCancellable protects the no-overlap invariant once
	// the source controller is already stopping or stopped.
	ErrInterfaceTransitionNotCancellable = errors.New("session: interface transition can no longer be cancelled")
	// ErrResumeInProgress prevents concurrent resume requests from replacing the
	// same runtime twice.
	ErrResumeInProgress = errors.New("session: agent resume already in progress")
	// ErrAwaitingDecision means the session is paused on a pending
	// permission/approval dialog. Send refuses to paste into it: the runtime
	// appends Enter after every paste, and an Enter into a decision dialog
	// would answer it on the user's behalf. The API maps it to a 409; the
	// caller retries once the user has answered in the terminal.
	ErrAwaitingDecision = errors.New("session: awaiting a user decision")
)

// Env vars a spawned process reads to learn who it is. A worker that starts
// its own Docker containers (a database, a queue, any ad-hoc service) should
// label them `--label ao.session=$AO_SESSION_ID` so AO's container reaper
// (dockerreap) removes them on session kill/terminal state — see #2652. Add
// `--label ao.spare=true` to a deliberately shared container that must
// survive past this session.
const (
	EnvSessionID = "AO_SESSION_ID"
	EnvProjectID = "AO_PROJECT_ID"
	EnvIssueID   = "AO_ISSUE_ID"
	// EnvRuntimeLaunchID identifies the current supervised agent generation.
	EnvRuntimeLaunchID = "AO_RUNTIME_LAUNCH_ID"
	// EnvDataDir tells a spawned agent's AO hook commands where the store lives.
	EnvDataDir = "AO_DATA_DIR"
	// EnvBrowserCapability proves ownership of the session's browser target.
	EnvBrowserCapability = "AO_BROWSER_CAPABILITY"
	// EnvBrowserRuntimeToken must never be inherited by a worker. It authenticates
	// the privileged Electron runtime, not session-scoped browser callers.
	EnvBrowserRuntimeToken = "AO_BROWSER_RUNTIME_TOKEN" //nolint:gosec // Environment variable name, not a credential.
	// EnvBrowserRuntimeTokenStdin is the daemon-only token handoff marker and
	// must be cleared before a worker process is spawned.
	EnvBrowserRuntimeTokenStdin = "AO_BROWSER_RUNTIME_TOKEN_STDIN" //nolint:gosec // Environment variable name, not a credential.
)

// hookBinaryName is the executable name the workspace hook commands invoke:
// every agent adapter installs a bare `ao hooks <agent> <event>`. The session
// PATH pin (hookPATH) only works when the daemon's own executable carries this
// name, since prepending its directory must change what `ao` resolves to.
const hookBinaryName = "ao"

type lifecycleRecorder interface {
	PrepareLaunch(id domain.SessionID, launchID string) error
	CancelLaunch(id domain.SessionID, launchID string)
	MarkSpawned(ctx context.Context, id domain.SessionID, metadata domain.SessionMetadata) error
	CommitControllerEpoch(ctx context.Context, id domain.SessionID, source, target domain.SessionMode, nativeConversationID string, startFresh bool) (bool, error)
	MarkTerminated(ctx context.Context, id domain.SessionID) error
}

// ShellTerminalCloser gates a session's scoped shell terminals around every
// path that releases its worktree (Kill, Cleanup, RetireForReplacement, the
// reconcile/shutdown save-and-teardown path), so none of them removes a
// worktree out from under a shell whose cwd still points into it — on Windows
// an open handle on that directory can even make the removal itself fail.
//
// BeginSessionTeardown drains the session's open shells and, on success,
// blocks any new OpenShellTerminal for that session until the returned
// release function is called — the caller MUST call it exactly once
// (typically via defer) once its own worktree work finishes, whatever the
// outcome. That release is tied to this specific acquisition (a fresh
// closure), not looked up by session id, so it can never be confused with — or
// release — a different, unrelated Begin for the same session. An error from
// BeginSessionTeardown means some scoped runtime could not be confirmed dead;
// the caller MUST NOT touch the worktree in that case, and release is nil (the
// gate already released itself on that error path).
//
// Late-bound via SetShellTerminalCloser: shellterm.Service is built after
// Session Manager during boot (see daemon.startShellTerminals), mirroring why
// lifecycle.Manager takes its completion terminator the same way.
type ShellTerminalCloser interface {
	BeginSessionTeardown(ctx context.Context, id domain.SessionID) (release func(), err error)
}

// TerminalInputGate closes the raw terminal input path while an interface
// transition drains and stops a TUI controller. It is separate from Messenger:
// xterm keystrokes travel over the terminal mux and never pass through Send.
type TerminalInputGate interface {
	// BeginInputDrain atomically blocks later writes and returns the time of the
	// newest write that was accepted before the block. Session Manager uses that
	// barrier to avoid trusting an idle hook which predates already-buffered PTY
	// input.
	BeginInputDrain(terminalID string) (lastInputAt time.Time, release func())
}

// ReviewerTerminator tears down a worker's reviewer pane when the worker leaves
// its live lifecycle. It is late-bound like ShellTerminalCloser because review
// services are assembled after the session manager in daemon wiring.
type ReviewerTerminator interface {
	TerminateReviewer(ctx context.Context, workerID domain.SessionID, body string) error
	TeardownReviewerTerminal(ctx context.Context, workerID domain.SessionID) error
	RestoreReviewer(ctx context.Context, workerID domain.SessionID) error
}

type runtimeController interface {
	Create(ctx context.Context, cfg ports.RuntimeConfig) (ports.RuntimeHandle, error)
	Destroy(ctx context.Context, handle ports.RuntimeHandle) error
	GetOutput(ctx context.Context, handle ports.RuntimeHandle, lines int) (string, error)
	// IsAlive reports whether the handle's runtime session still exists. Used by
	// Reconcile on boot to adopt crash-surviving sessions and reap leaked ones.
	IsAlive(ctx context.Context, handle ports.RuntimeHandle) (bool, error)
}

// RestoreMode reports whether a restore continued an agent-native transcript or
// relaunched from AO's saved task prompt.
type RestoreMode string

const (
	// RestoreModeNative means AO relaunched through the agent's native transcript resume command.
	RestoreModeNative RestoreMode = "native"
	// RestoreModeSavedPrompt means AO relaunched a new conversation from the saved task prompt.
	RestoreModeSavedPrompt RestoreMode = "saved_prompt"
	// RestoreModeFresh means AO relaunched without a saved task prompt.
	RestoreModeFresh RestoreMode = "fresh"
)

// RestoreResult is the command result for a restored session.
type RestoreResult struct {
	Session domain.SessionRecord
	Mode    RestoreMode
}

// Store is the persistence surface needed by the internal session Manager.
type Store interface {
	// GetProject loads a project row so spawn can resolve its per-project agent
	// config into the launch command. ok=false means the project is unknown.
	GetProject(ctx context.Context, id string) (domain.ProjectRecord, bool, error)
	ListWorkspaceRepos(ctx context.Context, projectID string) ([]domain.WorkspaceRepoRecord, error)
	CreateSession(ctx context.Context, rec domain.SessionRecord) (domain.SessionRecord, error)
	UpdateSession(ctx context.Context, rec domain.SessionRecord) error
	GetSession(ctx context.Context, id domain.SessionID) (domain.SessionRecord, bool, error)
	ListSessions(ctx context.Context, project domain.ProjectID) ([]domain.SessionRecord, error)
	ListAllSessions(ctx context.Context) ([]domain.SessionRecord, error)
	// DeleteSession removes a session row only if it is still in seed state
	// (no workspace, runtime handle, agent session id, or prompt; not
	// terminated). Returns deleted=true when removal happened; deleted=false
	// when the row had already progressed past seed state — preserving the
	// no-resurrection guarantee for live sessions.
	DeleteSession(ctx context.Context, id domain.SessionID) (bool, error)
	// UpsertSessionWorktree records or updates the worktree row for a session.
	// SaveAndTeardownAll writes the preserved_ref here (even when empty) as the
	// "shutdown-saved" marker before ForceDestroying the worktree.
	UpsertSessionWorktree(ctx context.Context, row domain.SessionWorktreeRecord) error
	// ListSessionWorktrees returns every worktree row for a session. RestoreAll
	// uses this to identify sessions saved by the last SaveAndTeardownAll: the
	// presence of any row is the marker; preserved_ref may be empty for clean
	// worktrees.
	ListSessionWorktrees(ctx context.Context, id domain.SessionID) ([]domain.SessionWorktreeRecord, error)
	// DeleteSessionWorktrees consumes stale shutdown-restore markers. Explicit
	// Kill and successful RestoreAll must remove these rows to prevent
	// resurrecting sessions the user intentionally terminated.
	DeleteSessionWorktrees(ctx context.Context, id domain.SessionID) error
}

// Manager coordinates internal session spawn, restore, kill, and cleanup over
// the outbound ports. User-facing read-model assembly lives in the service package.
type Manager struct {
	runtime   runtimeController
	agents    ports.AgentResolver
	workspace ports.Workspace
	store     Store
	// messenger is a sessionguard.Guard wrapping the raw messenger, so every
	// pane write is guarded (re-read state, refuse a blocked session) without
	// each call site re-deriving the check. Send/confirmActive use Deliver for
	// its Outcome; Spawn/Restore use the interface-level Send for
	// initial-prompt delivery, where a blocked session is impossible.
	messenger *sessionguard.Guard
	// chat launches the structured controller for a chat-mode session. Nil means
	// this build cannot run chat sessions, and a chat spawn is refused rather
	// than silently downgraded to a terminal.
	// defaults resolves the daemon-owned default session interface for a spawn
	// that names no mode. Nil falls back to the compatibility default, so a build
	// without it behaves exactly as before.
	defaults            SessionModeDefaults
	chat                ChatLauncher
	lcm                 lifecycleRecorder
	preview             PreviewLifecycle
	browser             BrowserLifecycle
	browserCapabilities BrowserCapabilityIssuer
	dataDir             string
	clock               func() time.Time
	// lookPath is exec.LookPath in production; tests substitute a stub so
	// they don't need real binaries on PATH. Returns ports.ErrAgentBinaryNotFound
	// when the binary is missing so the sentinel propagates through toAPIError.
	lookPath func(string) (string, error)
	// executable resolves the daemon's own binary (os.Executable in
	// production); its directory is prepended to spawned sessions' PATH so the
	// workspace hook commands resolve back to this daemon. Tests inject a stub.
	executable   func() (string, error)
	newLaunchID  func() string
	resumeMu     sync.Mutex
	resuming     map[domain.SessionID]struct{}
	transitionMu sync.Mutex
	transitions  map[domain.SessionID]*interfaceTransitionRun
	// transitionDeliveryWake drives the durable transition-message outbox. A
	// daemon-lifetime worker is started by Reconcile; terminal transition paths
	// also make one immediate delivery attempt so tests and in-process callers do
	// not depend on the boot worker.
	transitionDeliveryMu        sync.Mutex
	transitionDeliveryRunning   bool
	transitionDeliveryWake      chan struct{}
	transitionDeliveryAttemptMu sync.Mutex
	// sendConfirm bounds the best-effort post-send confirmation that the session
	// actually became active (the agent accepted the prompt). New fills in the
	// sendConfirm* defaults; tests in this package shrink the timings directly.
	sendConfirm sendConfirmConfig
	// interfaceTransition bounds only contradictory stale-idle proof. Turns and
	// user-paced waits reported through the activity boundary remain unbounded.
	interfaceTransition interfaceTransitionConfig
	logger              *slog.Logger

	// shellTerminalsMu guards shellTerminals: it is late-bound (see
	// ShellTerminalCloser) after Manager already exists, so a setter mutates it
	// under lock rather than through the constructor.
	shellTerminalsMu sync.Mutex
	shellTerminals   ShellTerminalCloser

	terminalInputGateMu sync.Mutex
	terminalInputGate   TerminalInputGate

	reviewersMu sync.Mutex
	reviewers   ReviewerTerminator
}

// SetShellTerminalCloser wires every worktree-releasing path to gate the
// session's scoped shell terminals shut first. Safe to leave unset: a nil
// closer makes beginShellTerminalTeardown a no-op (release=nil, err=nil),
// which is what every test in this package that does not care about shell
// terminals relies on.
func (m *Manager) SetShellTerminalCloser(closer ShellTerminalCloser) {
	m.shellTerminalsMu.Lock()
	defer m.shellTerminalsMu.Unlock()
	m.shellTerminals = closer
}

// SetTerminalInputGate late-binds the daemon's terminal mux after Session
// Manager is constructed. Nil preserves the no-op behavior used by narrow tests.
func (m *Manager) SetTerminalInputGate(gate TerminalInputGate) {
	m.terminalInputGateMu.Lock()
	defer m.terminalInputGateMu.Unlock()
	m.terminalInputGate = gate
}

func (m *Manager) beginTerminalInputDrain(rec domain.SessionRecord) (lastInputAt time.Time, release func()) {
	if domain.NormalizeSessionMode(rec.Mode) != domain.SessionModeTUI {
		return time.Time{}, nil
	}
	handle := runtimeHandle(rec.Metadata)
	if handle.ID == "" {
		return time.Time{}, nil
	}
	m.terminalInputGateMu.Lock()
	gate := m.terminalInputGate
	m.terminalInputGateMu.Unlock()
	if gate == nil {
		return time.Time{}, nil
	}
	return gate.BeginInputDrain(handle.ID)
}

// beginShellTerminalTeardown starts the shell-terminal gate for id ahead of
// releasing its worktree. release==nil, err==nil means no closer is wired
// (nothing to gate; proceed exactly as before this mechanism existed).
// err!=nil means some scoped shell terminal could not be confirmed closed —
// the caller MUST NOT touch the worktree, and release is nil (the gate
// already released itself). On success release is non-nil and tied to this
// specific acquisition; the caller MUST call it exactly once, typically via
// defer, once its own worktree work finishes.
func (m *Manager) beginShellTerminalTeardown(ctx context.Context, id domain.SessionID) (release func(), err error) {
	m.shellTerminalsMu.Lock()
	closer := m.shellTerminals
	m.shellTerminalsMu.Unlock()
	if closer == nil {
		return nil, nil
	}
	return closer.BeginSessionTeardown(ctx, id)
}

// SetReviewerTerminator wires worker lifecycle paths to the worker's reviewer
// pane. Safe to leave unset: a nil terminator is a no-op.
func (m *Manager) SetReviewerTerminator(terminator ReviewerTerminator) {
	m.reviewersMu.Lock()
	defer m.reviewersMu.Unlock()
	m.reviewers = terminator
}

func (m *Manager) terminateReviewer(ctx context.Context, id domain.SessionID, body string) error {
	m.reviewersMu.Lock()
	terminator := m.reviewers
	m.reviewersMu.Unlock()
	if terminator == nil {
		return nil
	}
	return terminator.TerminateReviewer(ctx, id, body)
}

func (m *Manager) teardownReviewerTerminal(ctx context.Context, id domain.SessionID) error {
	m.reviewersMu.Lock()
	terminator := m.reviewers
	m.reviewersMu.Unlock()
	if terminator == nil {
		return nil
	}
	return terminator.TeardownReviewerTerminal(ctx, id)
}

func (m *Manager) restoreReviewer(ctx context.Context, id domain.SessionID) error {
	m.reviewersMu.Lock()
	reviewer := m.reviewers
	m.reviewersMu.Unlock()
	if reviewer == nil {
		return nil
	}
	return reviewer.RestoreReviewer(ctx, id)
}

// PreviewLifecycle is the narrow teardown hook consumed by Session Manager.
// Keeping it here follows the consumer-owned interface boundary.
type PreviewLifecycle interface {
	StopSession(ctx context.Context, id domain.SessionID) error
}

// BrowserLifecycle is the narrow Electron-target teardown hook consumed by
// Session Manager. It must work even when no renderer panel mounted.
type BrowserLifecycle interface {
	DestroySession(ctx context.Context, id domain.SessionID) error
}

// BrowserCapabilityIssuer mints the split capability injected into a worker
// and persisted as a one-way verifier on its session row.
type BrowserCapabilityIssuer interface {
	Issue(id domain.SessionID) (token, verifier string, err error)
}

// sendConfirmConfig bounds the best-effort activity-confirmation loop run after
// Send. AO has no delivery ack: ao send returns 200 the moment tmux send-keys
// exits 0, and for a large multiline paste the single Enter may not submit the
// prompt — so UserPromptSubmit never fires and the orchestrator cannot tell the
// worker started. confirmActive observes the durable Activity.State (written by
// the user-prompt-submit hook) and re-sends Enter until the session is active or
// the budget is exhausted. It never fails the send.
type sendConfirmConfig struct {
	// pollInterval is the gap between activity reads.
	pollInterval time.Duration
	// attemptDeadline is how long to wait for active after each Enter.
	attemptDeadline time.Duration
	// maxAttempts bounds how many times Enter is (re)sent, counting the initial
	// Enter from Send itself.
	maxAttempts int
}

// interfaceTransitionConfig keeps reported human-paced work unbounded while
// making the contradictory stale-idle proof window short and testable. Only an
// idle row older than accepted PTY input consumes staleIdleLimit.
type interfaceTransitionConfig struct {
	pollInterval   time.Duration
	idleSettle     time.Duration
	staleIdleLimit time.Duration
}

// Production sendConfirm bounds: 3 Enters total (1 from Send + 2 re-sends),
// each given 2s to flip the session active, polled every 300ms.
const (
	sendConfirmPollInterval    = 300 * time.Millisecond
	sendConfirmAttemptDeadline = 2 * time.Second
	sendConfirmMaxAttempts     = 3
)

// Deps are the collaborators a Session Manager needs; New wires them together.
type Deps struct {
	Runtime   runtimeController
	Agents    ports.AgentResolver
	Workspace ports.Workspace
	Store     Store
	Messenger ports.AgentMessenger
	// Defaults supplies the daemon-owned default session interface for spawns that
	// name no mode. Nil means always use the compatibility default.
	Defaults SessionModeDefaults
	// Chat launches the structured controller for a chat-mode session. Nil means
	// chat mode is unavailable, and a chat spawn is refused rather than silently
	// downgraded to a terminal.
	Chat                ChatLauncher
	Lifecycle           lifecycleRecorder
	Preview             PreviewLifecycle
	Browser             BrowserLifecycle
	BrowserCapabilities BrowserCapabilityIssuer
	// DataDir is exported to spawned agents as AO_DATA_DIR so their hook
	// commands can open the same store.
	DataDir string
	Clock   func() time.Time
	// LookPath overrides exec.LookPath for the pre-launch agent-binary check.
	// Production wiring leaves this nil and the manager defaults to
	// exec.LookPath; tests inject a stub so they need not seed real binaries.
	LookPath func(string) (string, error)
	// Executable overrides os.Executable for the session PATH pin (see
	// hookPATH). Production wiring leaves this nil; tests inject a stub so they
	// control what the test binary appears to be.
	Executable func() (string, error)
	// NewLaunchID overrides supervised-process generation for deterministic tests.
	NewLaunchID func() string
	// Logger receives spawn-time diagnostics (e.g. when the session PATH
	// cannot be pinned to the daemon binary). Nil defaults to slog.Default().
	Logger *slog.Logger
}

// New builds a Session Manager from its dependencies, defaulting the clock to
// time.Now when Deps.Clock is nil.
func New(d Deps) *Manager {
	m := &Manager{
		runtime:                d.Runtime,
		agents:                 d.Agents,
		workspace:              d.Workspace,
		store:                  d.Store,
		defaults:               d.Defaults,
		chat:                   d.Chat,
		lcm:                    d.Lifecycle,
		preview:                d.Preview,
		browser:                d.Browser,
		browserCapabilities:    d.BrowserCapabilities,
		dataDir:                d.DataDir,
		clock:                  d.Clock,
		lookPath:               d.LookPath,
		executable:             d.Executable,
		newLaunchID:            d.NewLaunchID,
		resuming:               make(map[domain.SessionID]struct{}),
		transitions:            make(map[domain.SessionID]*interfaceTransitionRun),
		transitionDeliveryWake: make(chan struct{}, 1),
		sendConfirm: sendConfirmConfig{
			pollInterval:    sendConfirmPollInterval,
			attemptDeadline: sendConfirmAttemptDeadline,
			maxAttempts:     sendConfirmMaxAttempts,
		},
		interfaceTransition: interfaceTransitionConfig{
			pollInterval:   interfaceTransitionPoll,
			idleSettle:     interfaceTransitionIdleSettle,
			staleIdleLimit: interfaceTransitionStaleIdleLimit,
		},
		logger: d.Logger,
	}
	if m.clock == nil {
		// UTC so spawn-stamped CreatedAt/UpdatedAt match every other session
		// write (rename, activity) — all of which use time.Now().UTC(). A local
		// default produced mixed-timezone timestamps in `ao session get`.
		m.clock = func() time.Time { return time.Now().UTC() }
	}
	if m.lookPath == nil {
		m.lookPath = exec.LookPath
	}
	if m.executable == nil {
		m.executable = os.Executable
	}
	if m.newLaunchID == nil {
		m.newLaunchID = uuid.NewString
	}
	if m.logger == nil {
		m.logger = slog.Default()
	}
	// messenger is the raw d.Messenger wrapped in a Guard (needs m.logger, so it
	// is built after the logger default).
	m.messenger = sessionguard.New(d.Store, d.Messenger, m.logger)
	return m
}

// Spawn creates the session row (which assigns the "{project}-{n}" id), then the
// workspace and runtime, then reports completion to the LCM. If workspace
// materialization fails the still-seed row is deleted outright; a later failure
// parks the row as terminated and rolls back what was built.
func (m *Manager) Spawn(ctx context.Context, cfg ports.SpawnConfig) (domain.SessionRecord, int, int, error) {
	project, err := m.loadProject(ctx, cfg.ProjectID)
	if err != nil {
		return domain.SessionRecord{}, 0, 0, fmt.Errorf("spawn: %w", err)
	}
	projectKind := project.Kind.WithDefault()
	if projectKind == domain.ProjectKindScratch && strings.TrimSpace(cfg.Branch) != "" {
		return domain.SessionRecord{}, 0, 0, fmt.Errorf("spawn: %w", ErrScratchBranchUnsupported)
	}
	// A per-project role override picks the harness when the spawn names none,
	// so a project can default workers to one agent and orchestrators to another.
	cfg.Harness = effectiveHarness(cfg.Harness, cfg.Kind, project.Config)
	if cfg.Harness == "" {
		return domain.SessionRecord{}, 0, 0, fmt.Errorf("spawn: %w: configure project %s.agent or pass --harness", ErrMissingHarness, roleConfigName(cfg.Kind))
	}

	// Reject an unknown harness before any durable state is created. Doing this
	// after CreateSession would leave a terminated orphan row and waste a
	// worktree on a spawn that can never launch.
	if _, ok := m.agents.Agent(cfg.Harness); !ok {
		return domain.SessionRecord{}, 0, 0, fmt.Errorf("spawn: %w: %q", ErrUnknownHarness, cfg.Harness)
	}

	// Resolve the controller mode here, before anything durable is created, for
	// the same reason an unknown harness is rejected above: a chat request AO
	// cannot honor should cost nothing, not leave a terminated row and a worktree
	// behind. It never falls back to TUI — that would put the user in a terminal
	// they deliberately did not ask for.
	mode := m.resolveSessionMode(ctx, cfg.RequestedMode)
	if mode == domain.SessionModeChat {
		if m.chat == nil {
			return domain.SessionRecord{}, 0, 0, fmt.Errorf("spawn: %w: chat mode is not available in this build", ports.ErrChatUnsupported)
		}
		if err := m.chat.PreflightChat(ctx, cfg.Harness); err != nil {
			return domain.SessionRecord{}, 0, 0, fmt.Errorf("spawn: %w", err)
		}
	}
	cfg.RequestedMode = mode

	// A chat session runs no agent inside a terminal runtime, so the terminal
	// prerequisites are not its concern.
	if mode == domain.SessionModeTUI {
		if err := m.validateRuntimePrerequisites(); err != nil {
			return domain.SessionRecord{}, 0, 0, fmt.Errorf("spawn: %w", err)
		}
	}

	prompt, systemPrompt, err := m.buildSpawnTexts(ctx, cfg)
	if err != nil {
		return domain.SessionRecord{}, 0, 0, fmt.Errorf("spawn: prompt: %w", err)
	}
	promptBytes := len(prompt)
	systemPromptBytes := len(systemPrompt)
	m.logger.Debug("spawn: assembled prompts", "promptBytes", promptBytes, "systemPromptBytes", systemPromptBytes, "harness", cfg.Harness)

	rec, err := m.store.CreateSession(ctx, seedRecord(cfg, m.clock()))
	if err != nil {
		return domain.SessionRecord{}, promptBytes, systemPromptBytes, fmt.Errorf("spawn: create: %w", err)
	}
	id := rec.ID
	systemPromptFile, err := m.prepareSystemPromptFile(id, cfg.Harness, systemPrompt)
	if err != nil {
		m.rollbackSpawnSeedRow(ctx, id)
		return domain.SessionRecord{}, promptBytes, systemPromptBytes, fmt.Errorf("spawn %s: system prompt file: %w", id, err)
	}

	branch := cfg.Branch
	if branch == "" {
		branch = DefaultSpawnBranch(id, cfg.Kind, sessionPrefix(project), projectKind, m.dataDir)
	}
	ws, workspaceProject, err := m.createSessionWorkspace(ctx, project, cfg, id, branch)
	if err != nil {
		// Nothing observable exists yet — no worktree, no runtime — so the seed
		// row is deleted outright instead of accumulating as a terminated orphan
		// in session lists (e.g. when gitworktree refuses the branch).
		m.rollbackSpawnSeedRow(ctx, id)
		return domain.SessionRecord{}, promptBytes, systemPromptBytes, fmt.Errorf("spawn %s: workspace: %w", id, err)
	}

	// Per-project workspace provisioning: symlink shared files, then run any
	// post-create commands (e.g. `pnpm install`) before the agent launches.
	if err := m.provisionWorkspace(ctx, project, ws.Path); err != nil {
		m.rollbackSeedSpawnWorkspace(ctx, rec, ws, workspaceProject, false)
		return domain.SessionRecord{}, promptBytes, systemPromptBytes, fmt.Errorf("spawn %s: provision: %w", id, err)
	}

	// CLI agents receive the prompt as text and cannot consume inline binary
	// data, so any pasted/dropped images are written into the worktree and
	// referenced by path in the prompt. Done after provisioning (so the worktree
	// exists) and before the launch command is built (so the references reach
	// the agent).
	if len(cfg.Attachments) > 0 {
		refs, err := writeSpawnAttachments(ws.Path, cfg.Attachments)
		if err != nil {
			m.rollbackSeedSpawnWorkspace(ctx, rec, ws, workspaceProject, false)
			return domain.SessionRecord{}, promptBytes, systemPromptBytes, fmt.Errorf("spawn %s: attachments: %w", id, err)
		}
		// Keep the attachments dir out of git status. Best-effort: the images are
		// already written and usable, so an exclude failure must not fail the spawn.
		if err := m.workspace.AddExclude(ctx, ws, "/"+attachmentsDir+"/"); err != nil {
			m.logger.Warn("spawn: exclude attachments dir", "sessionID", id, "error", err)
		}
		prompt = appendAttachmentReferences(prompt, refs)
	}

	// Everything above is shared: project, harness, prompts, seed row, worktree,
	// provisioning, attachments. From here the two modes launch different
	// controllers, and exactly one of them runs.
	if mode == domain.SessionModeChat {
		rec, err = m.launchChatController(ctx, chatSpawn{
			cfg:              cfg,
			project:          project,
			projectKind:      projectKind,
			record:           rec,
			workspace:        ws,
			workspaceProject: workspaceProject,
			prompt:           prompt,
			systemPrompt:     systemPrompt,
		})
		if err != nil {
			return domain.SessionRecord{}, 0, 0, err
		}
		return rec, promptBytes, systemPromptBytes, nil
	}

	agent, ok := m.agents.Agent(cfg.Harness)
	if !ok {
		m.rollbackSeedSpawnWorkspace(ctx, rec, ws, workspaceProject, false)
		return domain.SessionRecord{}, promptBytes, systemPromptBytes, fmt.Errorf("spawn %s: no agent adapter for harness %q", id, cfg.Harness)
	}
	agentConfig := applySpawnAgentConfig(effectiveAgentConfig(cfg.Kind, project.Config), cfg.AgentConfig)
	env, browserCapabilityVerifier, err := m.launchRuntimeEnv(id, cfg.ProjectID, cfg.IssueID, project.Config.Env)
	if err != nil {
		m.rollbackSeedSpawnWorkspace(ctx, rec, ws, workspaceProject, true)
		return domain.SessionRecord{}, 0, 0, fmt.Errorf("spawn %s: browser capability: %w", id, err)
	}
	rec, err = m.persistBrowserCapabilityVerifier(ctx, rec, browserCapabilityVerifier)
	if err != nil {
		m.rollbackSeedSpawnWorkspace(ctx, rec, ws, workspaceProject, true)
		return domain.SessionRecord{}, 0, 0, fmt.Errorf("spawn %s: persist browser capability: %w", id, err)
	}
	m.augmentAgentRuntimeEnv(agent, env)
	if err := m.prepareWorkspace(ctx, agent, id, ws.Path, systemPrompt, systemPromptFile, agentConfig, env); err != nil {
		m.rollbackSeedSpawnWorkspace(ctx, rec, ws, workspaceProject, false)
		return domain.SessionRecord{}, promptBytes, systemPromptBytes, fmt.Errorf("spawn %s: %w", id, err)
	}
	launchCfg := ports.LaunchConfig{
		DataDir:          m.dataDir,
		SessionID:        string(id),
		WorkspacePath:    ws.Path,
		Kind:             cfg.Kind,
		Prompt:           prompt,
		SystemPrompt:     systemPrompt,
		SystemPromptFile: systemPromptFile,
		IssueID:          string(cfg.IssueID),
		Config:           agentConfig,
		Permissions:      agentConfig.Permissions,
	}
	delivery, err := agent.GetPromptDeliveryStrategy(ctx, launchCfg)
	if err != nil {
		m.rollbackSeedSpawnWorkspace(ctx, rec, ws, workspaceProject, true)
		return domain.SessionRecord{}, promptBytes, systemPromptBytes, fmt.Errorf("spawn %s: prompt delivery: %w", id, err)
	}
	if delivery == ports.PromptDeliveryAfterStart {
		launchCfg.Prompt = ""
	}
	argv, err := agent.GetLaunchCommand(ctx, launchCfg)
	if err != nil {
		m.rollbackSeedSpawnWorkspace(ctx, rec, ws, workspaceProject, true)
		return domain.SessionRecord{}, promptBytes, systemPromptBytes, fmt.Errorf("spawn %s: launch command: %w", id, err)
	}
	// Pre-flight: confirm argv[0] actually exists on PATH (or as an absolute
	// path the adapter returned) BEFORE handing the launch to the runtime.
	// tmux happily creates a session+pane around a missing command, so an
	// unresolved binary would leak through as a "live" session that never ran.
	if err := m.validateAgentBinary(argv); err != nil {
		m.rollbackSeedSpawnWorkspace(ctx, rec, ws, workspaceProject, true)
		return domain.SessionRecord{}, promptBytes, systemPromptBytes, fmt.Errorf("spawn %s: %w", id, err)
	}
	m.augmentRuntimePATHForLaunchBinary(ctx, env, argv)
	argv, launchID, err := m.superviseAgentProcess(agent, id, env, argv)
	if err != nil {
		m.rollbackSeedSpawnWorkspace(ctx, rec, ws, workspaceProject, true)
		return domain.SessionRecord{}, promptBytes, systemPromptBytes, fmt.Errorf("spawn %s: supervisor: %w", id, err)
	}
	if err := m.lcm.PrepareLaunch(id, launchID); err != nil {
		m.rollbackSeedSpawnWorkspace(ctx, rec, ws, workspaceProject, true)
		return domain.SessionRecord{}, promptBytes, systemPromptBytes, fmt.Errorf("spawn %s: prepare launch: %w", id, err)
	}
	defer m.lcm.CancelLaunch(id, launchID)
	handle, err := m.runtime.Create(ctx, ports.RuntimeConfig{
		SessionID:     id,
		WorkspacePath: ws.Path,
		Argv:          argv,
		Env:           env,
	})
	if err != nil {
		m.rollbackSeedSpawnWorkspace(ctx, rec, ws, workspaceProject, true)
		return domain.SessionRecord{}, promptBytes, systemPromptBytes, fmt.Errorf("spawn %s: runtime: %w", id, err)
	}

	metadata := domain.SessionMetadata{
		Branch:                    ws.Branch,
		WorkspacePath:             ws.Path,
		WorkspaceRepoPath:         ws.RepoPath,
		RuntimeHandleID:           handle.ID,
		RuntimeLaunchID:           launchID,
		Prompt:                    prompt,
		BrowserCapabilityVerifier: browserCapabilityVerifier,
	}
	if projectKind == domain.ProjectKindSingleRepo {
		metadata.DiffBaseSHA, metadata.DiffBaseRef = resolveSpawnDiffBase(ctx, ws.Path, project.Config.WithDefaults().DefaultBranch)
	}
	if err := m.lcm.MarkSpawned(ctx, id, metadata); err != nil {
		runtimeDestroyed := m.runtime.Destroy(ctx, handle) == nil
		m.rollbackPreparedSpawnWorkspace(ctx, rec, ws, workspaceProject, runtimeDestroyed)
		m.markSpawnFailedTerminated(ctx, id)
		return domain.SessionRecord{}, promptBytes, systemPromptBytes, fmt.Errorf("spawn %s: completed: %w", id, err)
	}
	if delivery == ports.PromptDeliveryAfterStart && prompt != "" {
		if err := m.deliverAfterStartPrompt(ctx, agent, launchCfg, handle, id, prompt); err != nil {
			runtimeDestroyed := m.runtime.Destroy(ctx, handle) == nil
			workspaceDestroyed := m.rollbackPreparedSpawnWorkspace(ctx, rec, ws, workspaceProject, runtimeDestroyed)
			if runtimeDestroyed && workspaceDestroyed {
				m.markSpawnFailedTerminatedWithoutWorkspace(ctx, id)
			} else {
				m.markSpawnFailedTerminated(ctx, id)
			}
			return domain.SessionRecord{}, promptBytes, systemPromptBytes, fmt.Errorf("spawn %s: deliver prompt: %w", id, err)
		}
	}
	rec, err = m.getRecord(ctx, id)
	if err != nil {
		return domain.SessionRecord{}, promptBytes, systemPromptBytes, err
	}
	return rec, promptBytes, systemPromptBytes, nil
}

// loadProject loads the project record so spawn can resolve its per-project
// config (harness/agent overrides, env, branch, rules, provisioning). A missing
// project yields a zero record rather than an error: the project may be
// unregistered yet still have live sessions, and an empty config simply means
// every field falls back to its default.
func (m *Manager) loadProject(ctx context.Context, projectID domain.ProjectID) (domain.ProjectRecord, error) {
	row, ok, err := m.store.GetProject(ctx, string(projectID))
	if err != nil {
		return domain.ProjectRecord{}, fmt.Errorf("load project: %w", err)
	}
	if !ok {
		return domain.ProjectRecord{}, nil
	}
	return row, nil
}

func (m *Manager) createSessionWorkspace(ctx context.Context, project domain.ProjectRecord, cfg ports.SpawnConfig, id domain.SessionID, branch string) (ports.WorkspaceInfo, *ports.WorkspaceProjectInfo, error) {
	projectKind := project.Kind.WithDefault()
	if projectKind != domain.ProjectKindWorkspace {
		baseBranch := project.Config.WithDefaults().DefaultBranch
		if projectKind == domain.ProjectKindScratch {
			baseBranch = ""
		}
		ws, err := m.workspace.Create(ctx, ports.WorkspaceConfig{
			ProjectID:     cfg.ProjectID,
			SessionID:     id,
			Kind:          cfg.Kind,
			SessionPrefix: sessionPrefix(project),
			Branch:        branch,
			BaseBranch:    baseBranch,
		})
		return ws, nil, err
	}
	workspaceProject, ok := m.workspace.(ports.WorkspaceProject)
	if !ok {
		return ports.WorkspaceInfo{}, nil, errors.New("workspace project materialization is not supported by workspace adapter")
	}
	repos, err := m.store.ListWorkspaceRepos(ctx, project.ID)
	if err != nil {
		return ports.WorkspaceInfo{}, nil, err
	}
	childRepos := make([]ports.WorkspaceProjectRepoConfig, 0, len(repos))
	for _, repo := range repos {
		childRepos = append(childRepos, ports.WorkspaceProjectRepoConfig{
			Name:         repo.Name,
			RelativePath: repo.RelativePath,
			RepoPath:     filepath.Join(project.Path, filepath.FromSlash(repo.RelativePath)),
		})
	}
	info, err := workspaceProject.CreateWorkspaceProject(ctx, ports.WorkspaceProjectConfig{
		ProjectID:     cfg.ProjectID,
		SessionID:     id,
		Kind:          cfg.Kind,
		SessionPrefix: sessionPrefix(project),
		Branch:        branch,
		RootRepoPath:  project.Path,
		BaseBranch:    project.Config.WithDefaults().DefaultBranch,
		Repos:         childRepos,
	})
	if err != nil {
		return ports.WorkspaceInfo{}, nil, err
	}
	for _, wt := range info.Worktrees {
		if err := m.store.UpsertSessionWorktree(ctx, domain.SessionWorktreeRecord{
			SessionID:    id,
			RepoName:     wt.RepoName,
			Branch:       wt.Branch,
			BaseSHA:      wt.BaseSHA,
			WorktreePath: wt.Path,
			State:        "active",
		}); err != nil {
			_ = workspaceProject.DestroyWorkspaceProject(ctx, info)
			return ports.WorkspaceInfo{}, nil, fmt.Errorf("record workspace worktree %q: %w", wt.RepoName, err)
		}
	}
	return info.Root, &info, nil
}

func resolveSpawnDiffBase(ctx context.Context, root, defaultBranch string) (string, string) {
	for _, ref := range spawnDiffBaseRefCandidates(defaultBranch) {
		if sha, ok := spawnGitSingleLine(ctx, root, "merge-base", "HEAD", ref); ok {
			return sha, ref
		}
	}
	if sha, ok := spawnGitSingleLine(ctx, root, "rev-parse", "HEAD"); ok {
		return sha, "HEAD"
	}
	return "", ""
}

func spawnDiffBaseRefCandidates(defaultBranch string) []string {
	defaultBranch = strings.TrimSpace(defaultBranch)
	if defaultBranch == "" {
		return nil
	}
	seen := map[string]struct{}{}
	var refs []string
	add := func(ref string) {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			return
		}
		if _, ok := seen[ref]; ok {
			return
		}
		seen[ref] = struct{}{}
		refs = append(refs, ref)
	}
	if !strings.HasPrefix(defaultBranch, "origin/") && !strings.HasPrefix(defaultBranch, "refs/") {
		add("origin/" + defaultBranch)
		add("refs/remotes/origin/" + defaultBranch)
	}
	add(defaultBranch)
	return refs
}

func spawnGitSingleLine(ctx context.Context, root string, args ...string) (string, bool) {
	cmd := aoprocess.CommandContext(ctx, "git", append([]string{"-C", root}, args...)...)
	out, err := cmd.Output()
	if err != nil {
		return "", false
	}
	value := strings.TrimSpace(string(out))
	return value, value != ""
}

func (m *Manager) destroySpawnWorkspace(ctx context.Context, ws ports.WorkspaceInfo, workspaceProject *ports.WorkspaceProjectInfo) bool {
	if workspaceProject != nil {
		if adapter, ok := m.workspace.(ports.WorkspaceProject); ok {
			err := adapter.DestroyWorkspaceProject(ctx, *workspaceProject)
			_ = m.store.DeleteSessionWorktrees(ctx, ws.SessionID)
			return err == nil
		}
	}
	err := m.workspace.Destroy(ctx, ws)
	_ = m.store.DeleteSessionWorktrees(ctx, ws.SessionID)
	return err == nil
}

func (m *Manager) rollbackPreparedSpawnWorkspace(ctx context.Context, rec domain.SessionRecord, ws ports.WorkspaceInfo, workspaceProject *ports.WorkspaceProjectInfo, runtimeDestroyed bool) bool {
	if m.destroySpawnWorkspace(ctx, ws, workspaceProject) {
		m.cleanupAgentWorkspace(ctx, rec, ws.Path)
		return true
	}
	m.preserveFailedSpawnWorkspace(ctx, rec.ID, ws, runtimeDestroyed)
	return false
}

func (m *Manager) rollbackSeedSpawnWorkspace(ctx context.Context, rec domain.SessionRecord, ws ports.WorkspaceInfo, workspaceProject *ports.WorkspaceProjectInfo, prepared bool) {
	if m.destroySpawnWorkspace(ctx, ws, workspaceProject) {
		if prepared {
			m.cleanupAgentWorkspace(ctx, rec, ws.Path)
		}
		m.rollbackSpawnSeedRow(ctx, rec.ID)
		return
	}
	m.preserveFailedSpawnWorkspace(ctx, rec.ID, ws, true)
	m.markSpawnFailedTerminated(ctx, rec.ID)
}

func (m *Manager) preserveFailedSpawnWorkspace(ctx context.Context, id domain.SessionID, ws ports.WorkspaceInfo, runtimeDestroyed bool) {
	rec, ok, err := m.store.GetSession(ctx, id)
	if err != nil {
		m.logger.Warn("spawn rollback: failed to load session for preserved workspace", "sessionID", id, "workspacePath", ws.Path, "error", err)
		return
	}
	if !ok {
		m.logger.Warn("spawn rollback: session missing for preserved workspace", "sessionID", id, "workspacePath", ws.Path)
		return
	}
	rec.Metadata.Branch = ws.Branch
	rec.Metadata.WorkspacePath = ws.Path
	rec.Metadata.WorkspaceRepoPath = ws.RepoPath
	if runtimeDestroyed {
		rec.Metadata.RuntimeHandleID = ""
		rec.Metadata.RuntimeLaunchID = ""
	}
	if err := m.store.UpdateSession(ctx, rec); err != nil {
		m.logger.Warn("spawn rollback: failed to record preserved workspace", "sessionID", id, "workspacePath", ws.Path, "error", err)
	}
}

// effectiveHarness resolves the harness for a spawn: an explicit harness wins;
// otherwise the project's role override for the session kind applies. Empty is
// invalid for new worker/orchestrator launches and is rejected by Spawn.
func effectiveHarness(explicit domain.AgentHarness, kind domain.SessionKind, cfg domain.ProjectConfig) domain.AgentHarness {
	if explicit != "" {
		return explicit
	}
	if role := roleOverride(kind, cfg).Harness; role != "" {
		return role
	}
	return ""
}

func roleConfigName(kind domain.SessionKind) string {
	if kind == domain.KindOrchestrator {
		return "orchestrator"
	}
	return "worker"
}

// effectiveAgentConfig merges the role override's agent config over the
// project's base agent config; set override fields win.
func effectiveAgentConfig(kind domain.SessionKind, cfg domain.ProjectConfig) ports.AgentConfig {
	merged := cfg.AgentConfig
	override := roleOverride(kind, cfg).AgentConfig
	if override.Model != "" {
		merged.Model = override.Model
	}
	if override.Mode != "" {
		merged.Mode = override.Mode
	}
	if override.Permissions != "" {
		merged.Permissions = override.Permissions
	}
	return merged
}

func applySpawnAgentConfig(base, override ports.AgentConfig) ports.AgentConfig {
	if override.Model != "" {
		base.Model = override.Model
	}
	if override.Mode != "" {
		base.Mode = override.Mode
	}
	if override.Permissions != "" {
		base.Permissions = override.Permissions
	}
	return base
}

func roleOverride(kind domain.SessionKind, cfg domain.ProjectConfig) domain.RoleOverride {
	if kind == domain.KindOrchestrator {
		return cfg.Orchestrator
	}
	return cfg.Worker
}

// sessionPrefix returns the display prefix for a project: the explicit
// SessionPrefix when set, otherwise the first 12 characters of the project ID.
func sessionPrefix(project domain.ProjectRecord) string {
	if p := strings.TrimSpace(project.Config.SessionPrefix); p != "" {
		return p
	}
	if len(project.ID) <= 12 {
		return project.ID
	}
	return project.ID[:12]
}

// markSpawnFailedTerminated best-effort parks an orphaned spawn as terminated.
// A phantom half-spawned row is worse than a terminal one; we only delete the
// row when nothing observable has landed yet (seed state) via rollbackSpawn or
// rollbackSpawnSeedRow.
func (m *Manager) markSpawnFailedTerminated(ctx context.Context, id domain.SessionID) {
	_ = m.lcm.MarkTerminated(ctx, id)
	m.cleanupSystemPromptDir(id)
}

// markSpawnFailedTerminatedWithoutWorkspace parks a spawn failure after the
// runtime row had become observable, but clears launch handles for resources
// that were destroyed during rollback. This keeps later restore/cleanup paths
// from treating a removed worktree as reusable state.
func (m *Manager) markSpawnFailedTerminatedWithoutWorkspace(ctx context.Context, id domain.SessionID) {
	m.markSpawnFailedTerminated(ctx, id)
	rec, ok, err := m.store.GetSession(ctx, id)
	if err != nil || !ok {
		return
	}
	rec.Metadata.Branch = ""
	rec.Metadata.WorkspacePath = ""
	rec.Metadata.RuntimeHandleID = ""
	rec.Metadata.AgentSessionID = ""
	_ = m.store.UpdateSession(ctx, rec)
}

// rollbackSpawnSeedRow best-effort removes the row of a spawn that failed
// before anything observable (worktree, runtime) was built, so failed spawns
// don't accumulate terminated rows in session lists. DeleteSession only removes
// rows still in seed state; if the row has progressed or the delete itself
// fails, fall back to parking it terminated so a phantom row never looks live.
func (m *Manager) rollbackSpawnSeedRow(ctx context.Context, id domain.SessionID) {
	if deleted, err := m.store.DeleteSession(ctx, id); err == nil && deleted {
		m.cleanupSystemPromptDir(id)
		return
	}
	m.markSpawnFailedTerminated(ctx, id)
}

// rollbackSpawn deletes a session row when it is still in seed state — used
// when an out-of-band step that happens AFTER `Spawn` returns (e.g. PR claim
// over HTTP) has failed and the caller wants the partially-spawned session
// gone without leaving a terminated orphan visible under `--include-terminated`.
//
// If the row has progressed past seed state (workspace exists, runtime created,
// etc.), DeleteSession is a no-op and rollbackSpawn falls back to a Kill so the
// runtime/workspace are torn down. Returns (deleted, killed):
//   - deleted=true: the row was a seed row and has been removed
//   - killed=true:  the row had spawn output and was torn down + terminated
//   - both false:   the row was already terminated or absent — benign no-op
func (m *Manager) rollbackSpawn(ctx context.Context, id domain.SessionID) (deleted, killed bool, err error) {
	deleted, err = m.store.DeleteSession(ctx, id)
	if err != nil {
		return false, false, fmt.Errorf("rollback %s: %w", id, err)
	}
	if deleted {
		m.cleanupSystemPromptDir(id)
		return true, false, nil
	}
	killed, err = m.Kill(ctx, id)
	if err != nil {
		return false, false, err
	}
	return false, killed, nil
}

// RollbackSpawn is the public surface of rollbackSpawn for service-layer callers.
func (m *Manager) RollbackSpawn(ctx context.Context, id domain.SessionID) (deleted, killed bool, err error) {
	return m.rollbackSpawn(ctx, id)
}

// Kill tears down the runtime and workspace, then records terminal intent with
// the LCM. A workspace teardown refused by the worktree-remove safety
// (uncommitted work) is never forced: Kill succeeds with freed=false,
// signalling the workspace was preserved for later inspection/cleanup while
// the session itself is still marked terminated.
//
// A session whose runtime handle or workspace path is missing (e.g. spawn
// failed partway, handle lost after a crash) is still terminated after the
// available destroy steps are skipped so it can be cleaned up from the
// dashboard.
func (m *Manager) Kill(ctx context.Context, id domain.SessionID) (bool, error) {
	if active, err := m.hasActiveInterfaceTransition(ctx, id); err != nil {
		return false, fmt.Errorf("kill %s: interface transition: %w", id, err)
	} else if active {
		return false, fmt.Errorf("kill %s: %w", id, ErrSwitchInProgress)
	}
	rec, ok, err := m.store.GetSession(ctx, id)
	if err != nil {
		return false, fmt.Errorf("kill %s: %w", id, err)
	}
	if !ok {
		return false, nil // already gone: benign race
	}
	m.stopPreviewBestEffort(ctx, id)
	m.destroyBrowserBestEffort(ctx, id)
	handle := runtimeHandle(rec.Metadata)
	ws := workspaceInfo(rec)

	var workspaceProjectRows []ports.WorkspaceRepoInfo
	workspaceProject := false
	if rows, ok, rowErr := m.workspaceProjectRows(ctx, rec); rowErr != nil {
		return false, fmt.Errorf("kill %s: workspace rows: %w", id, rowErr)
	} else if ok {
		workspaceProjectRows = rows
		workspaceProject = true
	}

	// Exactly one controller exists, so exactly one gets torn down. A chat
	// session has no runtime handle; its controller owns an app-server child
	// process, and closing it also settles any turn left in flight so a later
	// read does not show work that is no longer running.
	if domain.NormalizeSessionMode(rec.Mode) == domain.SessionModeChat {
		m.stopChatBestEffort(ctx, id)
	} else if handle.ID != "" {
		if err := m.runtime.Destroy(ctx, handle); err != nil {
			return false, fmt.Errorf("kill %s: runtime: %w", id, err)
		}
	}
	if err := m.terminateReviewer(ctx, id, "cancelled by worker session termination"); err != nil {
		return false, fmt.Errorf("kill %s: reviewer: %w", id, err)
	}
	// Gate shut any shell terminal scoped to this session BEFORE the worktree
	// goes away: an open shell whose cwd is that directory can otherwise
	// survive the removal (and on Windows can even block it — an open handle
	// on a directory refuses deletion), or a concurrent Open could land a new
	// one in the same race window. A runtime that cannot be confirmed dead
	// stops Kill here — same shape as a dirty-workspace refusal — rather than
	// letting the worktree disappear out from under it.
	if ws.Path != "" {
		release, err := m.beginShellTerminalTeardown(ctx, id)
		if err != nil {
			// Same shape as the dirty-workspace refusal below: the worktree is
			// left alone, but the restore marker still must not survive a user
			// kill, or the next boot's RestoreAll could resurrect a session the
			// user explicitly terminated (#2319).
			if err := m.store.DeleteSessionWorktrees(ctx, id); err != nil {
				m.logger.Warn("kill: delete restore marker failed", "sessionID", id, "error", err)
			}
			if err := m.lcm.MarkTerminated(ctx, id); err != nil {
				return false, fmt.Errorf("kill %s: %w", id, err)
			}
			m.cleanupSystemPromptDir(id)
			return false, nil
		}
		if release != nil {
			defer release()
		}
	}
	freed := false
	if workspaceProject {
		cleaned, err := m.destroyWorkspaceProjectRows(ctx, workspaceProjectRows)
		if err != nil {
			if errors.Is(err, ports.ErrWorkspaceDirty) {
				if err := m.lcm.MarkTerminated(ctx, id); err != nil {
					return false, fmt.Errorf("kill %s: %w", id, err)
				}
				m.cleanupSystemPromptDir(id)
				return false, nil
			}
			return false, fmt.Errorf("kill %s: workspace: %w", id, err)
		}
		freed = cleaned
		if cleaned {
			m.cleanupAgentWorkspace(ctx, rec, ws.Path)
		}
	} else if ws.Path != "" {
		if err := m.workspace.Destroy(ctx, ws); err != nil {
			if errors.Is(err, ports.ErrWorkspaceDirty) {
				if err := m.store.DeleteSessionWorktrees(ctx, id); err != nil {
					m.logger.Warn("kill: delete restore marker failed", "sessionID", id, "error", err)
				}
				if err := m.lcm.MarkTerminated(ctx, id); err != nil {
					return false, fmt.Errorf("kill %s: %w", id, err)
				}
				m.cleanupSystemPromptDir(id)
				return false, nil
			}
			return false, fmt.Errorf("kill %s: workspace: %w", id, err)
		}
		freed = true
		m.cleanupAgentWorkspace(ctx, rec, ws.Path)
	}
	// Clear the restore marker so the next boot's RestoreAll cannot resurrect a
	// killed session (#2319). For workspace projects this must happen after
	// teardown reads the rows; dirty-preserved rows return above and are left as
	// non-restorable inventory.
	if err := m.store.DeleteSessionWorktrees(ctx, id); err != nil {
		m.logger.Warn("kill: delete restore marker failed", "sessionID", id, "error", err)
	}
	if err := m.lcm.MarkTerminated(ctx, id); err != nil {
		return false, fmt.Errorf("kill %s: %w", id, err)
	}
	m.cleanupSystemPromptDir(id)
	return freed, nil
}

// RetireForReplacement terminates a live orchestrator and releases its branch
// for a replacement session. Unlike Kill, this captures uncommitted work before
// force-removing the worktree, so a dirty canonical orchestrator worktree does
// not block the replacement from claiming the canonical branch.
//
// This deliberately does not write a session_worktrees row: those rows are
// boot-restore markers, and a replaced orchestrator must stay terminated.
func (m *Manager) RetireForReplacement(ctx context.Context, id domain.SessionID) error {
	rec, ok, err := m.store.GetSession(ctx, id)
	if err != nil {
		return fmt.Errorf("retire replacement %s: %w", id, err)
	}
	if !ok || rec.IsTerminated {
		return nil
	}
	m.stopPreviewBestEffort(ctx, id)
	m.destroyBrowserBestEffort(ctx, id)
	if rec.Metadata.WorkspacePath == "" || rec.Metadata.Branch == "" {
		if err := m.store.DeleteSessionWorktrees(ctx, rec.ID); err != nil {
			return fmt.Errorf("retire replacement %s: clear restore markers: %w", id, err)
		}
		handle := runtimeHandle(rec.Metadata)
		if handle.ID != "" {
			if err := m.runtime.Destroy(ctx, handle); err != nil {
				return fmt.Errorf("retire replacement %s: runtime: %w", id, err)
			}
		}
		if err := m.lcm.MarkTerminated(ctx, id); err != nil {
			return fmt.Errorf("retire replacement %s: mark terminated: %w", id, err)
		}
		return nil
	}
	// Gate shut this session's scoped shell terminals before either branch
	// below force-removes its worktree (or worktrees, for a workspace
	// project). Unlike Kill there is no dirty-refusal path here — retirement
	// always force-destroys — so a shell that cannot be confirmed closed fails
	// the whole retirement instead of silently force-removing ground out from
	// under it.
	release, closeErr := m.beginShellTerminalTeardown(ctx, id)
	if closeErr != nil {
		return fmt.Errorf("retire replacement %s: %w", id, closeErr)
	}
	if release != nil {
		defer release()
	}

	if rows, ok, rowErr := m.workspaceProjectRows(ctx, rec); rowErr != nil {
		return fmt.Errorf("retire replacement %s: workspace rows: %w", id, rowErr)
	} else if ok {
		return m.retireWorkspaceProjectForReplacement(ctx, rec, rows)
	}

	ws := workspaceInfo(rec)
	staleWorkspace := false
	if _, err := m.workspace.StashUncommitted(ctx, ws); err != nil {
		if !errors.Is(err, ports.ErrWorkspaceStale) {
			return fmt.Errorf("retire replacement %s: stash: %w", id, err)
		}
		staleWorkspace = true
		m.logger.Warn("retire replacement: stale workspace; skipping preserve", "sessionID", id, "path", ws.Path, "error", err)
	}
	handle := runtimeHandle(rec.Metadata)
	if handle.ID != "" {
		if err := m.runtime.Destroy(ctx, handle); err != nil {
			return fmt.Errorf("retire replacement %s: runtime: %w", id, err)
		}
	}
	if err := m.workspace.ForceDestroy(ctx, ws); err != nil {
		if staleWorkspace {
			m.logger.Warn("retire replacement: stale workspace cleanup failed", "sessionID", id, "path", ws.Path, "error", err)
		}
		return fmt.Errorf("retire replacement %s: force destroy: %w", id, err)
	}
	m.cleanupAgentWorkspace(ctx, rec, ws.Path)
	if err := m.store.DeleteSessionWorktrees(ctx, rec.ID); err != nil {
		return fmt.Errorf("retire replacement %s: clear restore markers: %w", id, err)
	}
	if err := m.lcm.MarkTerminated(ctx, rec.ID); err != nil {
		return fmt.Errorf("retire replacement %s: mark terminated: %w", id, err)
	}
	return nil
}

func (m *Manager) stopPreviewBestEffort(ctx context.Context, id domain.SessionID) {
	if m.preview == nil {
		return
	}
	if err := m.preview.StopSession(ctx, id); err != nil {
		m.logger.Warn("session preview cleanup failed", "sessionID", id, "error", err)
	}
}

func (m *Manager) destroyBrowserBestEffort(ctx context.Context, id domain.SessionID) {
	if m.browser == nil {
		return
	}
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := m.browser.DestroySession(cleanupCtx, id); err != nil {
		m.logger.Warn("session browser cleanup failed", "sessionID", id, "error", err)
	}
}

func (m *Manager) retireWorkspaceProjectForReplacement(ctx context.Context, rec domain.SessionRecord, rows []ports.WorkspaceRepoInfo) error {
	staleRepos := make(map[string]bool)
	for _, row := range rows {
		if _, err := m.workspace.StashUncommitted(ctx, workspaceInfoFromRepoInfo(row)); err != nil {
			if !errors.Is(err, ports.ErrWorkspaceStale) {
				return fmt.Errorf("retire replacement %s repo %s: stash: %w", rec.ID, row.RepoName, err)
			}
			staleRepos[row.RepoName] = true
			m.logger.Warn("retire replacement: stale workspace repo; skipping preserve", "sessionID", rec.ID, "repo", row.RepoName, "path", row.Path, "error", err)
		}
	}
	handle := runtimeHandle(rec.Metadata)
	if handle.ID != "" {
		if err := m.runtime.Destroy(ctx, handle); err != nil {
			return fmt.Errorf("retire replacement %s: runtime: %w", rec.ID, err)
		}
	}
	for i := len(rows) - 1; i >= 0; i-- {
		if err := m.workspace.ForceDestroy(ctx, workspaceInfoFromRepoInfo(rows[i])); err != nil {
			if staleRepos[rows[i].RepoName] {
				m.logger.Warn("retire replacement: stale workspace repo cleanup failed", "sessionID", rec.ID, "repo", rows[i].RepoName, "path", rows[i].Path, "error", err)
			}
			return fmt.Errorf("retire replacement %s repo %s: force destroy: %w", rec.ID, rows[i].RepoName, err)
		}
	}
	m.cleanupAgentWorkspace(ctx, rec, rec.Metadata.WorkspacePath)
	if err := m.store.DeleteSessionWorktrees(ctx, rec.ID); err != nil {
		return fmt.Errorf("retire replacement %s: clear restore markers: %w", rec.ID, err)
	}
	if err := m.lcm.MarkTerminated(ctx, rec.ID); err != nil {
		return fmt.Errorf("retire replacement %s: mark terminated: %w", rec.ID, err)
	}
	return nil
}

// RestoreWithMode relaunches a torn-down session and reports whether AO used
// native resume, a saved-prompt fallback, or a fresh launch. The fallible I/O
// runs before any durable session write, so a failure never resurrects the row
// or destroys the worktree (it may hold the agent's prior work).
func (m *Manager) RestoreWithMode(ctx context.Context, id domain.SessionID) (RestoreResult, error) {
	if active, err := m.hasActiveInterfaceTransition(ctx, id); err != nil {
		return RestoreResult{}, fmt.Errorf("restore %s: interface transition: %w", id, err)
	} else if active {
		return RestoreResult{}, fmt.Errorf("restore %s: %w", id, ErrSwitchInProgress)
	}
	rec, ok, err := m.store.GetSession(ctx, id)
	if err != nil {
		return RestoreResult{}, fmt.Errorf("restore %s: %w", id, err)
	}
	if !ok {
		return RestoreResult{}, fmt.Errorf("restore %s: %w", id, ErrNotFound)
	}
	if !rec.IsTerminated {
		return RestoreResult{}, fmt.Errorf("restore %s: %w", id, ErrNotRestorable)
	}
	meta := rec.Metadata
	project, err := m.loadProject(ctx, rec.ProjectID)
	if err != nil {
		return RestoreResult{}, fmt.Errorf("restore %s: %w", id, err)
	}
	// Mirror Kill's incomplete-handle guard: a session whose spawn failed before
	// the workspace landed has neither WorkspacePath nor Branch, and there is
	// nothing meaningful to restore from. Surface this as a typed 409 instead of
	// letting workspace.Restore fail with an opaque wrapped error.
	if meta.WorkspacePath == "" || (meta.Branch == "" && project.Kind.WithDefault() != domain.ProjectKindScratch) {
		return RestoreResult{}, fmt.Errorf("restore %s: %w", id, ErrIncompleteHandle)
	}
	// Resumability is decided inside restoreArgv, not here. A promptless session
	// can still be fully resumable when the harness pins a deterministic session id
	// (Claude Code). restoreArgv returns ErrNotResumable only for a promptless,
	// unresumable non-orchestrator (a worker with no task and no native id to resume).
	// Orchestrators always relaunch fresh with the system prompt only.

	ws, err := m.restoreSessionWorkspace(ctx, project, rec)
	if err != nil {
		return RestoreResult{}, fmt.Errorf("restore %s: workspace: %w", id, err)
	}
	return m.relaunchRestoredSession(ctx, rec, project, ws)
}

func (m *Manager) relaunchRestoredSession(ctx context.Context, rec domain.SessionRecord, project domain.ProjectRecord, ws ports.WorkspaceInfo) (RestoreResult, error) {
	result, err := m.relaunchSession(ctx, "restore", rec, project, ws, nil)
	if err != nil {
		return RestoreResult{}, err
	}
	if rec.Kind == domain.KindWorker {
		if err := m.restoreReviewer(ctx, rec.ID); err != nil {
			m.logger.Warn("restore: reviewer terminal restore failed; worker remains restored", "sessionID", rec.ID, "error", err)
		}
	}
	return result, nil
}

// ResumeAgentWithMode replaces an exited agent inside its still-live session.
// Unlike RestoreWithMode, it preserves the existing worktree and terminal
// identity and never changes the durable terminated flag as an intermediate
// step.
func (m *Manager) ResumeAgentWithMode(ctx context.Context, id domain.SessionID) (RestoreResult, error) {
	if active, err := m.hasActiveInterfaceTransition(ctx, id); err != nil {
		return RestoreResult{}, fmt.Errorf("resume agent %s: interface transition: %w", id, err)
	} else if active {
		return RestoreResult{}, fmt.Errorf("resume agent %s: %w", id, ErrSwitchInProgress)
	}
	if !m.beginAgentResume(id) {
		return RestoreResult{}, fmt.Errorf("resume agent %s: %w", id, ErrResumeInProgress)
	}
	defer m.endAgentResume(id)

	rec, ok, err := m.store.GetSession(ctx, id)
	if err != nil {
		return RestoreResult{}, fmt.Errorf("resume agent %s: %w", id, err)
	}
	if !ok {
		return RestoreResult{}, fmt.Errorf("resume agent %s: %w", id, ErrNotFound)
	}
	if rec.IsTerminated {
		return RestoreResult{}, fmt.Errorf("resume agent %s: %w", id, ErrTerminated)
	}
	mode := domain.NormalizeSessionMode(rec.Mode)
	if mode == domain.SessionModeChat && m.chat != nil && m.chat.HasLiveChatController(id) {
		return RestoreResult{}, fmt.Errorf("resume agent %s: %w", id, ErrAgentNotExited)
	}
	if rec.Activity.State != domain.ActivityExited {
		// Builds before the controller-stop lifecycle fix can leave a Chat row
		// idle, active, or blocked even though no controller survived. The live
		// registry is authoritative for whether a duplicate Chat controller could
		// be created, so recover only when it confirms there is none. TUI keeps its
		// existing durable-exited precondition.
		if mode != domain.SessionModeChat || m.chat == nil {
			return RestoreResult{}, fmt.Errorf("resume agent %s: %w", id, ErrAgentNotExited)
		}
	}
	project, err := m.loadProject(ctx, rec.ProjectID)
	if err != nil {
		return RestoreResult{}, fmt.Errorf("resume agent %s: %w", id, err)
	}
	meta := rec.Metadata
	if meta.WorkspacePath == "" ||
		(meta.Branch == "" && project.Kind.WithDefault() != domain.ProjectKindScratch) ||
		(mode != domain.SessionModeChat && meta.RuntimeHandleID == "") {
		return RestoreResult{}, fmt.Errorf("resume agent %s: %w", id, ErrIncompleteHandle)
	}
	ws := ports.WorkspaceInfo{
		Path:      meta.WorkspacePath,
		Branch:    meta.Branch,
		SessionID: rec.ID,
		ProjectID: rec.ProjectID,
	}
	if mode == domain.SessionModeChat {
		return m.relaunchSession(ctx, "resume agent", rec, project, ws, nil)
	}
	handle := ports.RuntimeHandle{ID: meta.RuntimeHandleID}
	return m.relaunchSession(ctx, "resume agent", rec, project, ws, &handle)
}

func (m *Manager) beginAgentResume(id domain.SessionID) bool {
	m.resumeMu.Lock()
	defer m.resumeMu.Unlock()
	if _, exists := m.resuming[id]; exists {
		return false
	}
	m.resuming[id] = struct{}{}
	return true
}

func (m *Manager) endAgentResume(id domain.SessionID) {
	m.resumeMu.Lock()
	delete(m.resuming, id)
	m.resumeMu.Unlock()
}

func (m *Manager) relaunchSession(ctx context.Context, operation string, rec domain.SessionRecord, project domain.ProjectRecord, ws ports.WorkspaceInfo, restartHandle *ports.RuntimeHandle) (RestoreResult, error) {
	return m.relaunchSessionWithPolicy(ctx, operation, rec, project, ws, restartHandle, false)
}

// relaunchSessionFresh is reserved for an interface handoff whose adapter
// proved that the reserved provider id has no persisted conversation behind it.
// It bypasses native resume on both sides, so an empty Claude session cannot
// fail target startup (or rollback) with "No conversation found".
func (m *Manager) relaunchSessionFresh(ctx context.Context, operation string, rec domain.SessionRecord, project domain.ProjectRecord, ws ports.WorkspaceInfo, restartHandle *ports.RuntimeHandle) (RestoreResult, error) {
	return m.relaunchSessionWithPolicy(ctx, operation, rec, project, ws, restartHandle, true)
}

func (m *Manager) relaunchSessionWithPolicy(ctx context.Context, operation string, rec domain.SessionRecord, project domain.ProjectRecord, ws ports.WorkspaceInfo, restartHandle *ports.RuntimeHandle, forceFresh bool) (RestoreResult, error) {
	// Relaunch dispatches from the currently committed persisted mode, never from
	// a caller hint. The interface-transition coordinator changes that fact only
	// after stopping the old controller, then reuses this ordinary restore path.
	if domain.NormalizeSessionMode(rec.Mode) == domain.SessionModeChat {
		if forceFresh {
			rec.Metadata.ProviderConversationID = ""
		} else if strings.TrimSpace(rec.Metadata.ProviderConversationID) == "" {
			return RestoreResult{}, fmt.Errorf("%s %s: %w", operation, rec.ID, ErrIncompleteHandle)
		}
		return m.resumeChatController(ctx, operation, rec, project, ws)
	}

	agent, ok := m.agents.Agent(rec.Harness)
	if !ok {
		return RestoreResult{}, fmt.Errorf("%s %s: no agent adapter for harness %q", operation, rec.ID, rec.Harness)
	}
	// The system prompt is derived, not persisted: recompute it so a restored
	// session keeps its standing instructions across the relaunch.
	systemPrompt, err := m.buildSystemPrompt(ctx, rec.Kind, rec.ProjectID)
	if err != nil {
		return RestoreResult{}, fmt.Errorf("%s %s: system prompt: %w", operation, rec.ID, err)
	}
	systemPromptFile, err := m.prepareSystemPromptFile(rec.ID, rec.Harness, systemPrompt)
	if err != nil {
		m.cleanupSystemPromptDir(rec.ID)
		return RestoreResult{}, fmt.Errorf("%s %s: system prompt file: %w", operation, rec.ID, err)
	}

	// Restore re-applies the project's resolved agent config so a configured
	// model/permissions carry across a restore, matching fresh spawn.
	agentConfig := effectiveAgentConfig(rec.Kind, project.Config)
	env, browserCapabilityVerifier, err := m.launchRuntimeEnv(rec.ID, rec.ProjectID, rec.IssueID, project.Config.Env)
	if err != nil {
		return RestoreResult{}, fmt.Errorf("%s %s: browser capability: %w", operation, rec.ID, err)
	}
	rec, err = m.persistBrowserCapabilityVerifier(ctx, rec, browserCapabilityVerifier)
	if err != nil {
		return RestoreResult{}, fmt.Errorf("%s %s: persist browser capability: %w", operation, rec.ID, err)
	}
	m.augmentAgentRuntimeEnv(agent, env)
	if err := m.prepareWorkspace(ctx, agent, rec.ID, ws.Path, systemPrompt, systemPromptFile, agentConfig, env); err != nil {
		return RestoreResult{}, fmt.Errorf("%s %s: %w", operation, rec.ID, err)
	}
	var argv []string
	var delivery ports.PromptDeliveryStrategy
	var mode RestoreMode
	if forceFresh {
		argv, delivery, mode, err = freshLaunchArgv(ctx, agent, rec.ID, ws.Path, rec.Metadata,
			systemPrompt, systemPromptFile, agentConfig, rec.Kind, m.dataDir, true)
	} else {
		argv, delivery, mode, err = restoreArgv(ctx, agent, rec.ID, ws.Path, rec.Metadata,
			systemPrompt, systemPromptFile, agentConfig, rec.Kind, rec.Harness, m.dataDir)
	}
	if err != nil {
		m.cleanupSystemPromptDir(rec.ID)
		return RestoreResult{}, fmt.Errorf("%s %s: %w", operation, rec.ID, err)
	}
	if err := m.validateAgentBinary(argv); err != nil {
		m.cleanupSystemPromptDir(rec.ID)
		return RestoreResult{}, fmt.Errorf("%s %s: %w", operation, rec.ID, err)
	}
	m.augmentRuntimePATHForLaunchBinary(ctx, env, argv)
	argv, launchID, err := m.superviseAgentProcess(agent, rec.ID, env, argv)
	if err != nil {
		m.cleanupSystemPromptDir(rec.ID)
		return RestoreResult{}, fmt.Errorf("%s %s: supervisor: %w", operation, rec.ID, err)
	}
	if err := m.lcm.PrepareLaunch(rec.ID, launchID); err != nil {
		m.cleanupSystemPromptDir(rec.ID)
		return RestoreResult{}, fmt.Errorf("%s %s: prepare launch: %w", operation, rec.ID, err)
	}
	defer m.lcm.CancelLaunch(rec.ID, launchID)
	runtimeCfg := ports.RuntimeConfig{
		SessionID:     rec.ID,
		WorkspacePath: ws.Path,
		Argv:          argv,
		Env:           env,
	}
	var handle ports.RuntimeHandle
	if restartHandle == nil {
		handle, err = m.runtime.Create(ctx, runtimeCfg)
	} else {
		handle, err = m.restartRuntime(ctx, *restartHandle, runtimeCfg)
	}
	if err != nil {
		m.cleanupSystemPromptDir(rec.ID)
		return RestoreResult{}, fmt.Errorf("%s %s: runtime: %w", operation, rec.ID, err)
	}
	metadata := domain.SessionMetadata{
		Branch:                    ws.Branch,
		WorkspacePath:             ws.Path,
		WorkspaceRepoPath:         ws.RepoPath,
		RuntimeHandleID:           handle.ID,
		RuntimeLaunchID:           launchID,
		AgentSessionID:            rec.Metadata.AgentSessionID,
		Prompt:                    rec.Metadata.Prompt,
		BrowserCapabilityVerifier: browserCapabilityVerifier,
	}
	if err := m.lcm.MarkSpawned(ctx, rec.ID, metadata); err != nil {
		_ = m.runtime.Destroy(ctx, handle)
		m.cleanupSystemPromptDir(rec.ID)
		return RestoreResult{}, fmt.Errorf("%s %s: completed: %w", operation, rec.ID, err)
	}
	if delivery == ports.PromptDeliveryAfterStart && rec.Metadata.Prompt != "" {
		launchCfg := ports.LaunchConfig{
			DataDir:          m.dataDir,
			SessionID:        string(rec.ID),
			WorkspacePath:    ws.Path,
			Kind:             rec.Kind,
			Prompt:           rec.Metadata.Prompt,
			SystemPrompt:     systemPrompt,
			SystemPromptFile: systemPromptFile,
			Config:           agentConfig,
			Permissions:      agentConfig.Permissions,
		}
		if err := m.deliverAfterStartPrompt(ctx, agent, launchCfg, handle, rec.ID, rec.Metadata.Prompt); err != nil {
			_ = m.runtime.Destroy(ctx, handle)
			_ = m.lcm.MarkTerminated(ctx, rec.ID)
			m.cleanupSystemPromptDir(rec.ID)
			return RestoreResult{}, fmt.Errorf("%s %s: deliver prompt: %w", operation, rec.ID, err)
		}
	}
	updated, err := m.getRecord(ctx, rec.ID)
	if err != nil {
		return RestoreResult{}, err
	}
	return RestoreResult{Session: updated, Mode: mode}, nil
}

func (m *Manager) restartRuntime(ctx context.Context, handle ports.RuntimeHandle, cfg ports.RuntimeConfig) (ports.RuntimeHandle, error) {
	alive, err := m.runtime.IsAlive(ctx, handle)
	if err != nil {
		if !errors.Is(err, ports.ErrRuntimeUnavailable) {
			return ports.RuntimeHandle{}, fmt.Errorf("probe existing runtime: %w", err)
		}
		// The runtime infrastructure itself is gone (e.g. the tmux server was
		// killed). Restore/restart is exactly the recovery path for that
		// outage, so proceed as "no existing runtime" and create a fresh one.
		alive = false
	}
	if alive {
		if restarter, ok := m.runtime.(ports.RuntimeRestarter); ok {
			return restarter.Restart(ctx, handle, cfg)
		}
		if err := m.runtime.Destroy(ctx, handle); err != nil {
			return ports.RuntimeHandle{}, fmt.Errorf("destroy existing runtime: %w", err)
		}
	}
	return m.runtime.Create(ctx, cfg)
}

func (m *Manager) getRecord(ctx context.Context, id domain.SessionID) (domain.SessionRecord, error) {
	rec, ok, err := m.store.GetSession(ctx, id)
	if err != nil {
		return domain.SessionRecord{}, fmt.Errorf("get %s: %w", id, err)
	}
	if !ok {
		return domain.SessionRecord{}, fmt.Errorf("get %s: %w", id, ErrNotFound)
	}
	return rec, nil
}

// SaveAndTeardownAll captures uncommitted work and tears down every live
// session that has a workspace path. It is the shutdown path for the daemon:
// each session's uncommitted work is stashed into a preserve ref, the ref is
// written to session_worktrees (the "shutdown-saved" marker) BEFORE the
// worktree is force-removed. The DB write is committed before the worktree is
// destroyed so a crash between the two leaves the ref in place and the row
// present; RestoreAll will replay both.
//
// Failures on individual sessions are logged and do not abort the loop.
// ForceDestroy is never called if capture or the DB write did not succeed.
func (m *Manager) SaveAndTeardownAll(ctx context.Context) error {
	recs, err := m.store.ListAllSessions(ctx)
	if err != nil {
		return fmt.Errorf("save-teardown-all: list sessions: %w", err)
	}
	for _, rec := range recs {
		if rec.IsTerminated {
			continue
		}
		if rec.Metadata.WorkspacePath == "" || rec.Metadata.Branch == "" {
			continue
		}
		if err := m.saveAndTeardownOne(ctx, rec, true); err != nil {
			m.logger.Error("save-teardown-all: session failed, skipping", "sessionID", rec.ID, "error", err)
		}
	}
	return nil
}

// saveAndTeardownOne runs the capture-then-destroy sequence for a single
// session. The DB write (UpsertSessionWorktree) is committed before
// ForceDestroy; if either capture or the DB write fails, ForceDestroy is
// not called.
func (m *Manager) saveAndTeardownOne(ctx context.Context, rec domain.SessionRecord, destroyRuntime bool) error {
	// Gate shut this session's scoped shell terminals before either branch
	// below force-removes its worktree. Both SaveAndTeardownAll and
	// reconcileLive only reach here for a session with a real workspace, so
	// there is always a worktree to protect.
	release, closeErr := m.beginShellTerminalTeardown(ctx, rec.ID)
	if closeErr != nil {
		return fmt.Errorf("save %s: %w", rec.ID, closeErr)
	}
	if release != nil {
		defer release()
	}

	if rows, ok, err := m.workspaceProjectRows(ctx, rec); err != nil {
		return fmt.Errorf("save %s: workspace rows: %w", rec.ID, err)
	} else if ok {
		return m.saveAndTeardownWorkspaceProject(ctx, rec, rows, destroyRuntime)
	}

	// 1. Capture uncommitted work (ref may be "" for clean worktrees).
	ws := workspaceInfo(rec)
	ref, err := m.workspace.StashUncommitted(ctx, ws)
	if err != nil {
		return fmt.Errorf("save %s: stash: %w", rec.ID, err)
	}

	// 2. Write the shutdown-saved marker to the DB. The row's presence (even
	// with an empty preserved_ref) is what RestoreAll uses to identify sessions
	// saved by this run. This MUST be committed before ForceDestroy.
	row := domain.SessionWorktreeRecord{
		SessionID:    rec.ID,
		RepoName:     domain.RootWorkspaceRepoName,
		Branch:       rec.Metadata.Branch,
		WorktreePath: rec.Metadata.WorkspacePath,
		PreservedRef: ref,
		State:        "removed",
	}
	if err := m.store.UpsertSessionWorktree(ctx, row); err != nil {
		return fmt.Errorf("save %s: upsert worktree row: %w", rec.ID, err)
	}

	// 3. Remove reviewer panes before the worktree disappears, while preserving
	// review rows and native reviewer ids for restore. This is shutdown recovery,
	// not user intent to cancel review history.
	if err := m.teardownReviewerTerminal(ctx, rec.ID); err != nil {
		return fmt.Errorf("save %s: teardown reviewer: %w", rec.ID, err)
	}

	// 4. Mark terminal via the LCM (same path Kill uses).
	if err := m.lcm.MarkTerminated(ctx, rec.ID); err != nil {
		return fmt.Errorf("save %s: mark terminated: %w", rec.ID, err)
	}

	// 5. Runtime teardown (best-effort; same pattern as Kill).
	handle := runtimeHandle(rec.Metadata)
	if destroyRuntime && handle.ID != "" {
		if err := m.runtime.Destroy(ctx, handle); err != nil {
			m.logger.Warn("save-teardown-all: runtime destroy failed", "sessionID", rec.ID, "error", err)
		}
	}

	// 6. Force-remove the worktree (safe: work is captured in step 1 and the
	// DB write in step 2 is already committed).
	if err := m.workspace.ForceDestroy(ctx, ws); err != nil {
		m.logger.Warn("save-teardown-all: force destroy failed", "sessionID", rec.ID, "error", err)
	} else {
		m.cleanupAgentWorkspace(ctx, rec, ws.Path)
	}
	return nil
}

// reconcileLive handles a single non-terminated session on boot. If its runtime
// session is still alive (tmux is the persistence layer, so it survives a daemon
// crash) we adopt it: a no-op, the agent keeps running. If the runtime is gone,
// the agent died with the daemon, so we save-and-tear-down to the SAME end state
// a graceful shutdown produces: capture uncommitted work into a preserve ref,
// record the session_worktrees restore marker, mark terminated, and remove the
// worktree. RestoreAll (which Reconcile runs immediately after) then relaunches
// it on this same boot, resuming history. Crash recovery thus matches graceful
// restart instead of silently abandoning the session.
//
// If the work capture fails we mark terminated WITHOUT a marker and leave the
// worktree intact: better to skip the relaunch than to tear down un-preserved
// work or relaunch onto an inconsistent worktree.
func (m *Manager) reconcileLive(ctx context.Context, rec domain.SessionRecord) error {
	project, err := m.loadProject(ctx, rec.ProjectID)
	if err != nil {
		return err
	}
	projectKind := project.Kind.WithDefault()
	if rec.Metadata.WorkspacePath == "" || (rec.Metadata.Branch == "" && projectKind != domain.ProjectKindScratch) {
		return nil
	}
	// A chat controller is an in-process child of the daemon, so unlike tmux it can
	// never have survived the crash: there is nothing to adopt and nothing to
	// probe. It falls through to the same save-and-teardown a dead runtime gets,
	// which is what records the restore marker RestoreAll needs — skipping that
	// would leave the session marked live with no controller behind it, and it
	// would never be resumed.
	isChat := domain.NormalizeSessionMode(rec.Mode) == domain.SessionModeChat

	if !isChat {
		handle := runtimeHandle(rec.Metadata)
		if handle.ID != "" {
			alive, err := m.runtime.IsAlive(ctx, handle)
			switch {
			case err == nil:
			case errors.Is(err, ports.ErrRuntimeUnavailable):
				// Normal after a machine reboot: the runtime is conclusively gone,
				// so preserve work and create the restore marker below.
				alive = false
			default:
				// A failed probe is not proof of death: leave the session as-is.
				return fmt.Errorf("reconcile %s: probe: %w", rec.ID, err)
			}
			if alive {
				return nil // adopt: the session survived the crash.
			}
		}
	}
	if projectKind == domain.ProjectKindScratch {
		return m.lcm.MarkTerminated(ctx, rec.ID)
	}
	if err := m.saveAndTeardownOne(ctx, rec, false); err != nil {
		m.logger.Warn("reconcile: save-and-teardown failed; terminating without restore marker", "sessionID", rec.ID, "error", err)
		if mErr := m.lcm.MarkTerminated(ctx, rec.ID); mErr != nil {
			return fmt.Errorf("reconcile %s: mark terminated: %w", rec.ID, mErr)
		}
	}
	return nil
}

// reconcileReap kills the leaked tmux session of a session the DB already marks
// terminated. This covers the teardown that marked the row terminated but failed
// to kill the runtime (e.g. ForceDestroy/Destroy errored after MarkTerminated).
// Destroy is idempotent, so an already-gone session is a no-op.
func (m *Manager) reconcileReap(ctx context.Context, rec domain.SessionRecord) error {
	handle := runtimeHandle(rec.Metadata)
	if handle.ID == "" {
		return nil
	}
	alive, err := m.runtime.IsAlive(ctx, handle)
	if err != nil {
		if errors.Is(err, ports.ErrRuntimeUnavailable) {
			return nil // no server means no leaked session to reap
		}
		return fmt.Errorf("reconcile reap %s: probe: %w", rec.ID, err)
	}
	if !alive {
		return nil
	}
	if err := m.runtime.Destroy(ctx, handle); err != nil {
		return fmt.Errorf("reconcile reap %s: destroy: %w", rec.ID, err)
	}
	return nil
}

// Reconcile is the boot-time consistency pass. It replaces the bare RestoreAll
// call so that however the previous daemon died (clean shutdown, SIGKILL, or
// crash), live reality matches the DB:
//
//  1. Live pass: for each non-terminated session, adopt it if its runtime
//     survived, else capture work and mark terminated (reconcileLive).
//  2. Reap pass: for each terminated session whose runtime leaked, kill it
//     (reconcileReap). Runs before restore so a restored session does not
//     collide with a leaked tmux of the same name.
//  3. Restore pass: relaunch shutdown-saved sessions (existing RestoreAll).
//
// Best-effort throughout: a per-session failure is logged and never aborts the
// pass or blocks boot.
func (m *Manager) Reconcile(ctx context.Context) error {
	m.startTransitionMessageDispatcher(ctx)
	_, err := m.recoverInterruptedInterfaceTransitions(ctx)
	if err != nil {
		return fmt.Errorf("reconcile: interface transitions: %w", err)
	}
	recs, err := m.store.ListAllSessions(ctx)
	if err != nil {
		return fmt.Errorf("reconcile: list sessions: %w", err)
	}
	for _, rec := range recs {
		if rec.IsTerminated {
			continue
		}
		if err := m.reconcileLive(ctx, rec); err != nil {
			m.logger.Error("reconcile: live pass failed, skipping", "sessionID", rec.ID, "error", err)
		}
	}
	for _, rec := range recs {
		if !rec.IsTerminated {
			continue
		}
		if err := m.reconcileReap(ctx, rec); err != nil {
			m.logger.Error("reconcile: reap pass failed, skipping", "sessionID", rec.ID, "error", err)
		}
	}
	if err := m.RestoreAll(ctx); err != nil {
		return err
	}
	if err := m.deliverAllTransitionMessages(ctx); err != nil {
		m.logger.Error("reconcile: transition-message delivery deferred for retry", "error", err)
	}
	m.wakeTransitionMessageDispatcher()
	return nil
}

// RestoreAll relaunches every terminated session that was saved by the last
// SaveAndTeardownAll. The "shutdown-saved" marker is the presence of a
// session_worktrees row for the session; sessions the user killed before
// shutdown have no such row and are left terminated.
//
// For each saved session:
//  1. Ensure the worktree exists via workspace.Restore.
//  2. If a preserve ref is recorded, replay it via ApplyPreserved; on conflict
//     log and continue (still relaunch the agent, never delete the ref).
//  3. Relaunch via the existing Restore method.
//
// Failures on individual sessions are logged and do not abort the loop.
func (m *Manager) RestoreAll(ctx context.Context) error {
	recs, err := m.store.ListAllSessions(ctx)
	if err != nil {
		return fmt.Errorf("restore-all: list sessions: %w", err)
	}
	for _, rec := range recs {
		if !rec.IsTerminated {
			continue
		}
		// Check the shutdown-saved marker: is there a session_worktrees row?
		rows, err := m.store.ListSessionWorktrees(ctx, rec.ID)
		if err != nil {
			m.logger.Error("restore-all: list worktrees failed", "sessionID", rec.ID, "error", err)
			continue
		}
		if len(rows) == 0 {
			// No marker: this session was killed by the user before shutdown.
			continue
		}
		rows = restorableWorktreeRows(rows)
		if len(rows) == 0 {
			continue
		}

		// Step 1: ensure the worktree exists. workspace.Restore re-creates it
		// if it was removed by SaveAndTeardownAll.
		project, err := m.loadProject(ctx, rec.ProjectID)
		if err != nil {
			m.logger.Error("restore-all: load project failed", "sessionID", rec.ID, "error", err)
			continue
		}
		var ws ports.WorkspaceInfo
		restoredWorkspaceProject := project.Kind.WithDefault() == domain.ProjectKindWorkspace
		var projectRows []ports.WorkspaceRepoInfo
		if restoredWorkspaceProject {
			var rowErr error
			projectRows, rowErr = m.workspaceProjectRestoreRowsFromMarkers(ctx, project, rec, rows)
			if rowErr != nil {
				m.logger.Error("restore-all: workspace rows failed", "sessionID", rec.ID, "error", rowErr)
				continue
			}
			root, restoreErr := m.restoreWorkspaceProjectRows(ctx, projectRows)
			if restoreErr != nil {
				m.logger.Error("restore-all: workspace project restore failed", "sessionID", rec.ID, "error", restoreErr)
				continue
			}
			ws = workspaceInfoFromRepoInfo(root)
		} else {
			var restoreErr error
			ws, restoreErr = m.workspace.Restore(ctx, ports.WorkspaceConfig{
				ProjectID:     rec.ProjectID,
				SessionID:     rec.ID,
				Kind:          rec.Kind,
				SessionPrefix: sessionPrefix(project),
				Branch:        rec.Metadata.Branch,
				Path:          rec.Metadata.WorkspacePath,
			})
			if restoreErr != nil {
				m.logger.Error("restore-all: workspace restore failed", "sessionID", rec.ID, "error", restoreErr)
				continue
			}
		}
		if ws.Path == "" {
			m.logger.Error("restore-all: workspace restore failed", "sessionID", rec.ID, "error", "empty restored root path")
			continue
		}

		// Step 2: replay preserve ref when one was recorded.
		if restoredWorkspaceProject {
			m.applyWorkspaceProjectPreserved(ctx, projectRows)
		} else {
			var preserveRef string
			for _, r := range rows {
				if r.PreservedRef != "" {
					preserveRef = r.PreservedRef
					break
				}
			}
			if preserveRef != "" {
				if applyErr := m.workspace.ApplyPreserved(ctx, ws, preserveRef); applyErr != nil {
					if errors.Is(applyErr, ports.ErrPreservedConflict) {
						m.logger.Warn("restore-all: apply preserved produced conflicts; agent relaunched with conflict markers in place",
							"sessionID", rec.ID, "ref", preserveRef, "error", applyErr)
					} else {
						m.logger.Error("restore-all: apply preserved failed", "sessionID", rec.ID, "error", applyErr)
					}
					// Continue: always relaunch even on conflict (never delete the ref here).
				}
			}
		}

		// Step 3: relaunch the agent in the restored workspace.
		if _, err := m.relaunchRestoredSession(ctx, rec, project, ws); err != nil {
			switch {
			case errors.Is(err, ErrNotResumable):
				// A promptless, unresumable worker is intentionally left terminated:
				// expected, not an operational failure, so log it quietly.
				m.logger.Warn("restore-all: session left terminated (nothing to resume)", "sessionID", rec.ID)
			case errors.Is(err, ErrNotFound):
				// The row was reaped between listing and relaunch (a stale id during
				// reconciliation): skip it and keep restoring the rest.
				m.logger.Warn("restore-all: session vanished before relaunch, skipping", "sessionID", rec.ID)
			default:
				m.logger.Error("restore-all: relaunch failed", "sessionID", rec.ID, "error", err)
			}
			continue
		}

		// One-shot: drop the consumed marker so it never outlives one restart
		// (#2319). A still-live session re-acquires it at the next quit.
		if restoredWorkspaceProject {
			for _, row := range projectRows {
				if err := m.upsertWorkspaceProjectRowState(ctx, row, "active"); err != nil {
					m.logger.Warn("restore-all: marking workspace repo active failed", "sessionID", rec.ID, "repo", row.RepoName, "error", err)
				}
			}
		} else {
			if err := m.markSessionWorktreesActive(ctx, rows); err != nil {
				m.logger.Warn("restore-all: marking worktrees active failed", "sessionID", rec.ID, "error", err)
			}
			if err := m.store.DeleteSessionWorktrees(ctx, rec.ID); err != nil {
				m.logger.Warn("restore-all: delete restore marker failed", "sessionID", rec.ID, "error", err)
			}
		}
	}
	return nil
}

func restorableWorktreeRows(rows []domain.SessionWorktreeRecord) []domain.SessionWorktreeRecord {
	out := make([]domain.SessionWorktreeRecord, 0, len(rows))
	for _, row := range rows {
		if row.State == "removed" || legacyRestorableWorktreeRow(row) {
			out = append(out, row)
		}
	}
	return out
}

func legacyRestorableWorktreeRow(row domain.SessionWorktreeRecord) bool {
	return row.State == "" && (row.PreservedRef != "" || row.RepoName == domain.RootWorkspaceRepoName)
}

func (m *Manager) markSessionWorktreesActive(ctx context.Context, rows []domain.SessionWorktreeRecord) error {
	for _, row := range rows {
		row.State = "active"
		row.PreservedRef = ""
		if err := m.store.UpsertSessionWorktree(ctx, row); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) restoreSessionWorkspace(ctx context.Context, project domain.ProjectRecord, rec domain.SessionRecord) (ports.WorkspaceInfo, error) {
	if project.Kind.WithDefault() != domain.ProjectKindWorkspace {
		return m.workspace.Restore(ctx, ports.WorkspaceConfig{
			ProjectID:     rec.ProjectID,
			SessionID:     rec.ID,
			Kind:          rec.Kind,
			SessionPrefix: sessionPrefix(project),
			Branch:        rec.Metadata.Branch,
			Path:          rec.Metadata.WorkspacePath,
		})
	}
	rows, err := m.workspaceProjectRestoreRows(ctx, project, rec)
	if err != nil {
		return ports.WorkspaceInfo{}, err
	}
	root, err := m.restoreWorkspaceProjectRows(ctx, rows)
	if err != nil {
		return ports.WorkspaceInfo{}, err
	}
	for _, row := range rows {
		if err := m.upsertWorkspaceProjectRowState(ctx, row, "active"); err != nil {
			return ports.WorkspaceInfo{}, fmt.Errorf("mark repo %s active: %w", row.RepoName, err)
		}
	}
	return workspaceInfoFromRepoInfo(root), nil
}

func (m *Manager) workspaceProjectRestoreRows(ctx context.Context, project domain.ProjectRecord, rec domain.SessionRecord) ([]ports.WorkspaceRepoInfo, error) {
	rows, err := m.store.ListSessionWorktrees(ctx, rec.ID)
	if err != nil {
		return nil, err
	}
	return m.workspaceProjectRestoreRowsFromMarkers(ctx, project, rec, rows)
}

func (m *Manager) workspaceProjectRestoreRowsFromMarkers(ctx context.Context, project domain.ProjectRecord, rec domain.SessionRecord, rows []domain.SessionWorktreeRecord) ([]ports.WorkspaceRepoInfo, error) {
	if len(rows) > 1 {
		return m.sessionWorktreeRowsToRepoInfos(ctx, project, rec, rows)
	}
	childRepos, err := m.store.ListWorkspaceRepos(ctx, project.ID)
	if err != nil {
		return nil, err
	}
	rootPath := rec.Metadata.WorkspacePath
	rootBranch := rec.Metadata.Branch
	var rootBaseSHA string
	if len(rows) == 1 && (rows[0].RepoName == "" || rows[0].RepoName == domain.RootWorkspaceRepoName) {
		rootPath = firstNonEmptyString(rows[0].WorktreePath, rootPath)
		rootBranch = firstNonEmptyString(rows[0].Branch, rootBranch)
		rootBaseSHA = rows[0].BaseSHA
	}
	out := []ports.WorkspaceRepoInfo{{
		RepoName:  domain.RootWorkspaceRepoName,
		RepoPath:  project.Path,
		Path:      rootPath,
		Branch:    rootBranch,
		BaseSHA:   rootBaseSHA,
		SessionID: rec.ID,
		ProjectID: rec.ProjectID,
	}}
	for _, repo := range childRepos {
		out = append(out, ports.WorkspaceRepoInfo{
			RepoName:     repo.Name,
			RepoPath:     filepath.Join(project.Path, filepath.FromSlash(repo.RelativePath)),
			Path:         filepath.Join(rootPath, filepath.FromSlash(repo.RelativePath)),
			Branch:       rootBranch,
			SessionID:    rec.ID,
			ProjectID:    rec.ProjectID,
			RelativePath: repo.RelativePath,
		})
	}
	return out, nil
}

func (m *Manager) workspaceProjectRows(ctx context.Context, rec domain.SessionRecord) ([]ports.WorkspaceRepoInfo, bool, error) {
	rows, err := m.store.ListSessionWorktrees(ctx, rec.ID)
	if err != nil {
		return nil, false, err
	}
	if len(rows) <= 1 {
		return nil, false, nil
	}
	project, err := m.loadProject(ctx, rec.ProjectID)
	if err != nil {
		return nil, false, err
	}
	if project.Kind.WithDefault() != domain.ProjectKindWorkspace {
		return nil, false, nil
	}
	infos, err := m.sessionWorktreeRowsToRepoInfos(ctx, project, rec, rows)
	if err != nil {
		return nil, false, err
	}
	return infos, true, nil
}

func (m *Manager) sessionWorktreeRowsToRepoInfos(ctx context.Context, project domain.ProjectRecord, rec domain.SessionRecord, rows []domain.SessionWorktreeRecord) ([]ports.WorkspaceRepoInfo, error) {
	childRepos, err := m.store.ListWorkspaceRepos(ctx, project.ID)
	if err != nil {
		return nil, err
	}
	repoPaths := map[string]string{domain.RootWorkspaceRepoName: project.Path}
	relPaths := map[string]string{}
	for _, repo := range childRepos {
		repoPaths[repo.Name] = filepath.Join(project.Path, filepath.FromSlash(repo.RelativePath))
		relPaths[repo.Name] = repo.RelativePath
	}
	out := make([]ports.WorkspaceRepoInfo, 0, len(rows))
	for _, row := range rows {
		repoPath := repoPaths[row.RepoName]
		if repoPath == "" {
			return nil, fmt.Errorf("session worktree row %q no longer matches workspace registry", row.RepoName)
		}
		out = append(out, ports.WorkspaceRepoInfo{
			RepoName:     row.RepoName,
			RepoPath:     repoPath,
			Path:         row.WorktreePath,
			Branch:       firstNonEmptyString(row.Branch, rec.Metadata.Branch),
			BaseSHA:      row.BaseSHA,
			SessionID:    rec.ID,
			ProjectID:    rec.ProjectID,
			RelativePath: relPaths[row.RepoName],
		})
	}
	return out, nil
}

func (m *Manager) saveAndTeardownWorkspaceProject(ctx context.Context, rec domain.SessionRecord, rows []ports.WorkspaceRepoInfo, destroyRuntime bool) error {
	for _, row := range rows {
		ref, err := m.workspace.StashUncommitted(ctx, workspaceInfoFromRepoInfo(row))
		if err != nil {
			return fmt.Errorf("save %s repo %s: stash: %w", rec.ID, row.RepoName, err)
		}
		if err := m.store.UpsertSessionWorktree(ctx, domain.SessionWorktreeRecord{
			SessionID:    rec.ID,
			RepoName:     row.RepoName,
			Branch:       row.Branch,
			BaseSHA:      row.BaseSHA,
			WorktreePath: row.Path,
			PreservedRef: ref,
			State:        "removed",
		}); err != nil {
			return fmt.Errorf("save %s repo %s: upsert worktree row: %w", rec.ID, row.RepoName, err)
		}
	}
	if err := m.teardownReviewerTerminal(ctx, rec.ID); err != nil {
		return fmt.Errorf("save %s: teardown reviewer: %w", rec.ID, err)
	}
	if err := m.lcm.MarkTerminated(ctx, rec.ID); err != nil {
		return fmt.Errorf("save %s: mark terminated: %w", rec.ID, err)
	}
	handle := runtimeHandle(rec.Metadata)
	if destroyRuntime && handle.ID != "" {
		if err := m.runtime.Destroy(ctx, handle); err != nil {
			m.logger.Warn("save-teardown-all: runtime destroy failed", "sessionID", rec.ID, "error", err)
		}
	}
	rootDestroyed := false
	for i := len(rows) - 1; i >= 0; i-- {
		info := workspaceInfoFromRepoInfo(rows[i])
		if err := m.workspace.ForceDestroy(ctx, info); err != nil {
			m.logger.Warn("save-teardown-all: force destroy failed", "sessionID", rec.ID, "repo", rows[i].RepoName, "error", err)
		} else if info.Path == rec.Metadata.WorkspacePath {
			rootDestroyed = true
		}
	}
	if rootDestroyed {
		m.cleanupAgentWorkspace(ctx, rec, rec.Metadata.WorkspacePath)
	}
	return nil
}

func (m *Manager) destroyWorkspaceProjectRows(ctx context.Context, rows []ports.WorkspaceRepoInfo) (bool, error) {
	cleaned := false
	var firstErr error
	for i := len(rows) - 1; i >= 0; i-- {
		if rows[i].Path == "" {
			continue
		}
		info := workspaceInfoFromRepoInfo(rows[i])
		if err := m.workspace.Destroy(ctx, info); err != nil {
			if errors.Is(err, ports.ErrWorkspaceDirty) {
				return cleaned, err
			}
			if stateErr := m.upsertWorkspaceProjectRowState(ctx, rows[i], "retry_remove"); stateErr != nil && firstErr == nil {
				firstErr = stateErr
			}
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if err := m.upsertWorkspaceProjectRowState(ctx, rows[i], "unavailable"); err != nil && firstErr == nil {
			firstErr = err
		}
		cleaned = true
	}
	return cleaned, firstErr
}

func (m *Manager) upsertWorkspaceProjectRowState(ctx context.Context, row ports.WorkspaceRepoInfo, state string) error {
	return m.store.UpsertSessionWorktree(ctx, domain.SessionWorktreeRecord{
		SessionID:    row.SessionID,
		RepoName:     row.RepoName,
		Branch:       row.Branch,
		BaseSHA:      row.BaseSHA,
		WorktreePath: row.Path,
		State:        state,
	})
}

func (m *Manager) restoreWorkspaceProjectRows(ctx context.Context, rows []ports.WorkspaceRepoInfo) (ports.WorkspaceRepoInfo, error) {
	var root ports.WorkspaceRepoInfo
	for _, row := range rows {
		restored, err := m.workspace.Restore(ctx, ports.WorkspaceConfig{
			ProjectID: row.ProjectID,
			SessionID: row.SessionID,
			Branch:    row.Branch,
			RepoPath:  row.RepoPath,
			Path:      row.Path,
		})
		if err != nil {
			return ports.WorkspaceRepoInfo{}, fmt.Errorf("repo %s: %w", row.RepoName, err)
		}
		row.Path = restored.Path
		row.Branch = restored.Branch
		if row.RepoName == domain.RootWorkspaceRepoName {
			root = row
		}
	}
	if root.Path == "" {
		return ports.WorkspaceRepoInfo{}, errors.New("workspace project root worktree row missing")
	}
	return root, nil
}

func (m *Manager) applyWorkspaceProjectPreserved(ctx context.Context, rows []ports.WorkspaceRepoInfo) {
	for _, row := range rows {
		var preserveRef string
		sessionRows, err := m.store.ListSessionWorktrees(ctx, row.SessionID)
		if err != nil {
			m.logger.Error("restore-all: list worktrees failed", "sessionID", row.SessionID, "error", err)
			continue
		}
		for _, sessionRow := range sessionRows {
			if sessionRow.RepoName == row.RepoName {
				preserveRef = sessionRow.PreservedRef
				break
			}
		}
		if preserveRef == "" {
			continue
		}
		if applyErr := m.workspace.ApplyPreserved(ctx, workspaceInfoFromRepoInfo(row), preserveRef); applyErr != nil {
			if errors.Is(applyErr, ports.ErrPreservedConflict) {
				m.logger.Warn("restore-all: apply preserved produced conflicts; agent relaunched with conflict markers in place",
					"sessionID", row.SessionID, "repo", row.RepoName, "ref", preserveRef, "error", applyErr)
			} else {
				m.logger.Error("restore-all: apply preserved failed", "sessionID", row.SessionID, "repo", row.RepoName, "error", applyErr)
			}
		}
	}
}

// Send delivers a message to a running session's agent through the guarded
// pane-write primitive, then best-effort confirms the agent actually accepted
// it. The guard refuses delivery into a session that is gone, terminated, has
// an exited agent, or is paused on a permission decision;
// those refusals surface as typed sentinels so the API reports why instead of
// silently dropping the message. AO has no delivery ack: the messenger returns
// nil the moment the runtime paste + Enter commands exit 0, and for a large
// multiline prompt a single Enter may not submit (claude-code leaves it as an
// unsubmitted draft). confirmActive observes the durable Activity.State
// (flipped to active by the user-prompt-submit hook) and re-sends Enter until
// the session is active or the budget is exhausted. Confirmation never fails
// the send: it only decides whether to nudge again.
func (m *Manager) Send(ctx context.Context, id domain.SessionID, message string) error {
	return m.send(ctx, id, message, "")
}

// send carries an optional idempotency key used by durable transition-message
// retries. Ordinary callers leave it empty; the outbox preserves the key across
// restart, rollback, and even a second overlapping handoff.
func (m *Manager) send(ctx context.Context, id domain.SessionID, message, clientMessageID string) error {
	// A controller transition deliberately has a short interval with no writer.
	// Queue internal/lifecycle sends durably instead of racing either controller
	// or dropping coordination work; the transition worker drains this outbox
	// only after the target controller is active.
	if queued, err := m.queueDuringInterfaceTransition(ctx, id, message, clientMessageID); err != nil {
		return fmt.Errorf("send %s: interface transition: %w", id, err)
	} else if queued {
		return nil
	}
	// Chat mode has no pane to type into, so it does not go through the messenger
	// at all. Without this branch the send reached the runtime guard and was
	// refused as "missing runtime handles" — true of the handles, wrong about the
	// session, and it left `ao send` and orchestrator-to-worker relay unable to
	// reach a chat worker.
	if handled, err := m.sendChat(ctx, id, message, clientMessageID); handled {
		return err
	}

	message, err := m.prepareOutboundMessage(ctx, id, message)
	if err != nil {
		return err
	}
	outcome, err := m.messenger.Deliver(ctx, id, message)
	if err != nil {
		return fmt.Errorf("send %s: %w", id, err)
	}
	switch outcome {
	case sessionguard.SuppressedNotFound:
		return fmt.Errorf("send %s: %w", id, ErrNotFound)
	case sessionguard.SuppressedTerminated:
		return fmt.Errorf("send %s: %w", id, ErrTerminated)
	case sessionguard.SuppressedExited:
		return fmt.Errorf("send %s: %w", id, ErrAgentExited)
	case sessionguard.SuppressedAwaitingUser:
		return fmt.Errorf("send %s: %w", id, ErrAwaitingDecision)
	}
	// confirmActive only helps — and is only SAFE — when the harness reports
	// both a prompt-submit signal (so the loop can observe active) and a
	// blocked signal it can clear mid-turn (so it can tell an unsubmitted
	// draft from a pending permission dialog and never Enter into the latter).
	// Only claude-code and its hook-delegators (grok/continueagent/devin)
	// satisfy both; every other harness opts out via EmitsBlockedActivity —
	// see ports.ActivitySignaler.
	rec, ok, err := m.store.GetSession(ctx, id)
	if err != nil {
		// Confirmation is best-effort and never fails the send (the message
		// was already delivered above); log so a store error is not swallowed
		// silently.
		m.logger.Warn("send: confirm skipped, session lookup failed", "sessionID", id, "error", err)
		return nil
	}
	if !ok {
		return nil
	}
	if m.harnessNudgeSafe(rec.Harness) {
		m.confirmActive(ctx, m.messenger, id)
	}
	return nil
}

func (m *Manager) prepareOutboundMessage(ctx context.Context, id domain.SessionID, message string) (string, error) {
	rec, ok, err := m.store.GetSession(ctx, id)
	if err != nil {
		return "", fmt.Errorf("send %s: session: %w", id, err)
	}
	if !ok {
		return message, nil
	}
	if rec.Harness != domain.HarnessCopilot || rec.Kind != domain.KindOrchestrator {
		return message, nil
	}
	return copilotOrchestratorMessage(rec.ProjectID, message), nil
}

func copilotOrchestratorMessage(projectID domain.ProjectID, message string) string {
	project := strings.TrimSpace(string(projectID))
	if project == "" {
		project = "<project>"
	}
	return fmt.Sprintf(`AO ORCHESTRATOR DIRECTIVE

You are acting as the AO orchestrator for project %s. Do not implement code changes, edit files, run implementation tests, or complete the user's task yourself.

Your next action for any implementation, fix, UI change, test, PR, or code-review task must be to spawn or redirect a worker session. Use:

ao spawn --project %s --name "<label, max 20 chars>" --prompt "<clear worker task>"

If a suitable worker already exists, use ao send to redirect that worker instead. After spawning or redirecting, report the worker session id and stop. Do not do the worker's task in this orchestrator session.

USER MESSAGE:
%s`, project, project, message)
}

// harnessNudgeSafe reports whether the session's harness is safe to nudge with
// an Enter-only re-send (see ports.ActivitySignaler): it must emit BOTH a
// prompt-submit signal (else the loop wastes its budget never observing active)
// and a blocked signal (else an Enter meant to resubmit a draft could answer a
// permission dialog the harness cannot report).
func (m *Manager) harnessNudgeSafe(harness domain.AgentHarness) bool {
	if m.agents == nil {
		return false
	}
	agent, ok := m.agents.Agent(harness)
	if !ok {
		return false
	}
	s, ok := agent.(ports.ActivitySignaler)
	return ok && s.EmitsSubmitActivity() && s.EmitsBlockedActivity()
}

// waitOutcome is one poll round's verdict on whether confirmActive should
// nudge again.
type waitOutcome int

const (
	// waitTimedOut: the deadline elapsed without the session going active —
	// the previous Enter likely did not land, another may help.
	waitTimedOut waitOutcome = iota
	// waitActive: the session went active — the prompt was accepted, done.
	waitActive
	// waitBlocked: the session is paused on a user decision (a pending
	// permission/approval dialog) — an automated Enter could answer the dialog
	// on the user's behalf, so confirmation must stop and never nudge.
	waitBlocked
)

// confirmActive re-sends Enter until the session reports ActivityActive or the
// attempt budget is exhausted. The initial Send already submitted one Enter;
// each additional attempt sends Enter again (an empty message is an Enter-only
// nudge, see ports.AgentMessenger) after waiting for Activity.State to flip. It
// is best-effort: on context cancellation, store failure, or budget exhaustion
// it returns silently (the message was already delivered; the agent may yet
// pick it up). Harnesses without a user-prompt-submit hook never flip to
// active, so the loop simply times out — Send remains successful for them.
//
// Decision safety: a session observed in ActivityBlocked stops confirmation
// immediately with no nudge — an Enter into a pending permission dialog would
// answer it for the user. Sticky ActivityWaitingInput does NOT stop the loop:
// an idle-prompt session with an unsubmitted pasted draft is exactly the case
// the nudge exists for.
func (m *Manager) confirmActive(ctx context.Context, guard *sessionguard.Guard, id domain.SessionID) {
	for attempt := 1; ; attempt++ {
		outcome, err := m.waitForActive(ctx, id)
		if err != nil || outcome == waitActive {
			return
		}
		if outcome == waitBlocked {
			m.logger.Info("send: session awaiting a decision; skipping Enter nudge", "sessionID", id, "attempt", attempt)
			return
		}
		if attempt >= m.sendConfirm.maxAttempts {
			m.logger.Warn("send: activity confirmation budget exhausted", "sessionID", id, "attempts", attempt)
			return
		}
		// Timed out with budget remaining: the previous Enter did not land.
		// Nudge again with an Enter-only send. Deliver re-reads state
		// immediately before pasting — a permission dialog can appear in the
		// gap between waitForActive's final poll and this send, and an Enter
		// into it would answer the decision. This closes the TOCTOU the
		// per-poll check inside waitForActive cannot cover; a store failure
		// inside the guard fails closed (no Enter on an unknown state).
		nudge, nudgeErr := guard.Deliver(ctx, id, "")
		if nudgeErr != nil {
			m.logger.Warn("send: confirm re-send failed", "sessionID", id, "attempt", attempt, "error", nudgeErr)
			return
		}
		if nudge != sessionguard.Sent {
			// Not necessarily blocked: the session may also have terminated or
			// vanished since the poll — the outcome says which.
			m.logger.Info("send: session unavailable before nudge; skipping Enter nudge", "sessionID", id, "attempt", attempt, "outcome", nudge.String())
			return
		}
	}
}

// waitForActive polls Activity.State for up to attemptDeadline and reports
// whether another nudge could help (see waitOutcome). Blocked is checked every
// poll so a permission dialog appearing mid-wait aborts immediately instead of
// burning the deadline. A non-nil error means polling cannot continue (ctx
// cancelled, store failure, session gone).
func (m *Manager) waitForActive(ctx context.Context, id domain.SessionID) (waitOutcome, error) {
	deadlineAt := m.clock().Add(m.sendConfirm.attemptDeadline)
	ticker := time.NewTicker(m.sendConfirm.pollInterval)
	defer ticker.Stop()
	for {
		rec, ok, err := m.store.GetSession(ctx, id)
		if err != nil {
			return waitTimedOut, err
		}
		if !ok {
			return waitTimedOut, fmt.Errorf("session %s not found", id)
		}
		switch rec.Activity.State {
		case domain.ActivityActive:
			return waitActive, nil
		case domain.ActivityBlocked:
			return waitBlocked, nil
		}
		if !m.clock().Before(deadlineAt) {
			return waitTimedOut, nil
		}
		// The tick select respects ctx cancellation so a request timeout
		// unblocks promptly.
		select {
		case <-ctx.Done():
			return waitTimedOut, ctx.Err()
		case <-ticker.C:
		}
	}
}

// CleanupSkip reports one terminal session whose workspace was preserved
// rather than reclaimed, and why.
type CleanupSkip struct {
	SessionID domain.SessionID
	Reason    string
}

// CleanupResult reports what Cleanup reclaimed and what it preserved.
type CleanupResult struct {
	Cleaned []domain.SessionID
	Skipped []CleanupSkip
}

// Cleanup reclaims the workspaces of terminal sessions in a project. A workspace
// whose teardown is refused (uncommitted work) is never forced; it is reported
// in Skipped with the reason so the refusal is visible instead of silent.
func (m *Manager) Cleanup(ctx context.Context, project domain.ProjectID) (CleanupResult, error) {
	recs, err := m.cleanupRecords(ctx, project)
	if err != nil {
		return CleanupResult{}, fmt.Errorf("cleanup %s: %w", project, err)
	}
	result := CleanupResult{Cleaned: make([]domain.SessionID, 0, len(recs)), Skipped: []CleanupSkip{}}
	for _, rec := range recs {
		if !rec.IsTerminated {
			continue
		}
		ws := workspaceInfo(rec)
		if ws.Path == "" {
			m.cleanupSystemPromptDir(rec.ID)
			continue
		}
		if h := runtimeHandle(rec.Metadata); h.ID != "" {
			_ = m.runtime.Destroy(ctx, h) // best effort; usually already gone
		}
		if reason := m.cleanupOne(ctx, rec, ws); reason != "" {
			result.Skipped = append(result.Skipped, CleanupSkip{SessionID: rec.ID, Reason: reason})
			continue
		}
		m.cleanupSystemPromptDir(rec.ID)
		result.Cleaned = append(result.Cleaned, rec.ID)
	}
	return result, nil
}

// cleanupOne reclaims one terminated session's workspace, gating shut any
// shell terminal scoped to it first (same ordering as Kill). Split out of
// Cleanup's loop so the release function's defer is scoped to one session's
// call, not deferred across every iteration until Cleanup itself returns.
// Returns "" when the workspace was reclaimed; a non-empty reason means it was
// left alone this run (Cleanup records it in Skipped and can retry on a later
// call) — most commonly because a scoped shell terminal could not be
// confirmed closed, so reclaiming would pull the ground out from under it.
func (m *Manager) cleanupOne(ctx context.Context, rec domain.SessionRecord, ws ports.WorkspaceInfo) (skipReason string) {
	release, closeErr := m.beginShellTerminalTeardown(ctx, rec.ID)
	if closeErr != nil {
		m.logger.Warn("cleanup: shell terminal still open", "sessionID", rec.ID, "error", closeErr)
		return "shell terminal still open"
	}
	if release != nil {
		defer release()
	}

	if rows, ok, rowErr := m.workspaceProjectRows(ctx, rec); rowErr != nil {
		m.logger.Warn("cleanup: workspace rows failed", "sessionID", rec.ID, "error", rowErr)
		return "workspace teardown failed"
	} else if ok {
		if _, err := m.destroyWorkspaceProjectRows(ctx, rows); err != nil {
			if !errors.Is(err, ports.ErrWorkspaceDirty) {
				m.logger.Warn("cleanup: workspace teardown failed", "sessionID", rec.ID, "path", ws.Path, "error", err)
			}
			return cleanupSkipReason(err)
		}
		m.cleanupAgentWorkspace(ctx, rec, ws.Path)
		return ""
	}
	if err := m.workspace.Destroy(ctx, ws); err != nil {
		if !errors.Is(err, ports.ErrWorkspaceDirty) {
			// The public reason stays a fixed string (the raw error carries
			// internal filesystem paths); the full cause lands here.
			m.logger.Warn("cleanup: workspace teardown failed", "sessionID", rec.ID, "path", ws.Path, "error", err)
		}
		return cleanupSkipReason(err)
	}
	m.cleanupAgentWorkspace(ctx, rec, ws.Path)
	return ""
}

// cleanupSkipReason renders a workspace teardown refusal as a short
// user-facing reason for the cleanup report. Deliberately not the raw error:
// it flows to the API response and CLI output, and teardown errors embed
// internal filesystem paths.
func cleanupSkipReason(err error) string {
	if errors.Is(err, ports.ErrWorkspaceDirty) {
		return "workspace has uncommitted changes"
	}
	if errors.Is(err, ErrProjectNotResolvable) {
		return "project is archived or unregistered — remove worktree manually"
	}
	return "workspace teardown failed"
}

func (m *Manager) cleanupRecords(ctx context.Context, project domain.ProjectID) ([]domain.SessionRecord, error) {
	if project == "" {
		return m.store.ListAllSessions(ctx)
	}
	return m.store.ListSessions(ctx, project)
}

// ---- helpers ----

func seedRecord(cfg ports.SpawnConfig, now time.Time) domain.SessionRecord {
	return domain.SessionRecord{
		ProjectID:   cfg.ProjectID,
		IssueID:     cfg.IssueID,
		Kind:        cfg.Kind,
		CreatedAt:   now,
		UpdatedAt:   now,
		Harness:     cfg.Harness,
		DisplayName: cfg.DisplayName,
		Activity:    domain.Activity{State: domain.ActivityIdle, LastActivityAt: now},
		// Resolved before this point and persisted here. There is no UPDATE
		// statement that can change it afterwards.
		Mode: domain.NormalizeSessionMode(cfg.RequestedMode),
	}
}

func defaultSessionBranch(id domain.SessionID, kind domain.SessionKind, prefix, branchNamespace string) string {
	if kind == domain.KindOrchestrator {
		return aoBranch(branchNamespace, prefix+"-orchestrator")
	}
	// A fresh, unique branch per worker session: gitworktree can't add a worktree
	// on a branch already checked out elsewhere (e.g. main). Put the root work
	// branch under a session namespace so sibling PR branches such as
	// ao/<session>/<topic> remain valid Git refs.
	return aoBranch(branchNamespace, string(id), "root")
}

// DefaultSpawnBranch returns AO's generated work branch for a spawn. Explicit
// user-provided branches bypass this helper.
func DefaultSpawnBranch(id domain.SessionID, kind domain.SessionKind, prefix string, projectKind domain.ProjectKind, dataDir string) string {
	if projectKind == domain.ProjectKindScratch {
		return ""
	}
	branchNamespace := generatedBranchNamespace(dataDir)
	if projectKind == domain.ProjectKindWorkspace {
		return aoBranch(branchNamespace, string(id))
	}
	return defaultSessionBranch(id, kind, prefix, branchNamespace)
}

// DefaultOrchestratorBranch returns the generated canonical orchestrator branch
// for a project in the current data-dir namespace.
func DefaultOrchestratorBranch(prefix, dataDir string) string {
	return defaultSessionBranch("", domain.KindOrchestrator, prefix, generatedBranchNamespace(dataDir))
}

func aoBranch(namespace string, parts ...string) string {
	all := []string{"ao"}
	if namespace != "" {
		all = append(all, namespace)
	}
	all = append(all, parts...)
	return strings.Join(all, "/")
}

func generatedBranchNamespace(dataDir string) string {
	if isDefaultDevDataDir(dataDir) {
		return "dev"
	}
	return ""
}

func isDefaultDevDataDir(dataDir string) bool {
	if strings.TrimSpace(dataDir) == "" {
		return false
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	want, err := filepath.Abs(filepath.Join(home, ".ao", "dev", "data"))
	if err != nil {
		return false
	}
	got, err := filepath.Abs(dataDir)
	if err != nil {
		return false
	}
	return filepath.Clean(got) == filepath.Clean(want)
}

func buildPrompt(cfg ports.SpawnConfig) string {
	return buildTaskPrompt(taskPromptConfig{
		Role:         promptRoleForKind(cfg.Kind),
		Prompt:       cfg.Prompt,
		IssueID:      string(cfg.IssueID),
		IssueContext: cfg.IssueContext,
	})
}

func promptRoleForKind(kind domain.SessionKind) sessionPromptRole {
	switch kind {
	case domain.KindOrchestrator:
		return sessionPromptRoleOrchestrator
	case domain.KindWorker:
		return sessionPromptRoleWorker
	default:
		return ""
	}
}

func promptProjectContext(projectID domain.ProjectID, project domain.ProjectRecord) promptProject {
	cfg := project.Config.WithDefaults()
	if project.Kind.WithDefault() == domain.ProjectKindScratch {
		cfg.DefaultBranch = ""
	}
	id := project.ID
	if strings.TrimSpace(id) == "" {
		id = string(projectID)
	}
	return promptProject{
		ID:            id,
		Name:          project.DisplayName,
		Repo:          project.RepoOriginURL,
		DefaultBranch: cfg.DefaultBranch,
		Path:          project.Path,
	}
}

// attachmentsDir is the worktree-relative directory where spawn file
// attachments are written.
const attachmentsDir = ".ao/attachments"

// writeSpawnAttachments writes each attachment into the worktree under
// attachmentsDir as attachment-1<ext>, attachment-2<ext>, ... and returns the
// worktree-relative paths in order. The files are excluded from git via the
// worktree's info/exclude so they do not dirty the working tree.
func writeSpawnAttachments(workspacePath string, attachments []ports.SpawnAttachment) ([]string, error) {
	dir := filepath.Join(workspacePath, filepath.FromSlash(attachmentsDir))
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("create attachments dir: %w", err)
	}
	refs := make([]string, 0, len(attachments))
	for i, a := range attachments {
		ext := a.Ext
		if ext == "" {
			ext = ".bin"
		}
		name := fmt.Sprintf("attachment-%d%s", i+1, ext)
		if err := os.WriteFile(filepath.Join(dir, name), a.Data, 0o600); err != nil {
			return nil, fmt.Errorf("write attachment %d: %w", i+1, err)
		}
		// Worktree-relative reference, always forward-slashed for the prompt.
		refs = append(refs, attachmentsDir+"/"+name)
	}
	return refs, nil
}

// appendAttachmentReferences appends a block listing the attached file paths so
// the agent knows to read them. Placed after the human's brief.
func appendAttachmentReferences(prompt string, refs []string) string {
	if len(refs) == 0 {
		return prompt
	}
	var b strings.Builder
	b.WriteString(prompt)
	if strings.TrimSpace(prompt) != "" {
		b.WriteString("\n\n")
	}
	b.WriteString("Attached files (read these files in the workspace for context):")
	for _, ref := range refs {
		b.WriteString("\n- ")
		b.WriteString(ref)
	}
	return b.String()
}

// buildSpawnTexts returns the user-facing prompt and the system prompt to
// deliver separately to the agent. Orchestrator role instructions and worker
// coordination hints are placed in the system prompt so they are treated as
// standing instructions rather than part of the human's task request. A
// promptless spawn delivers no user prompt at all: the agent simply lands at an
// empty input box rather than receiving an auto-generated kickoff turn.
func (m *Manager) buildSpawnTexts(ctx context.Context, cfg ports.SpawnConfig) (prompt, systemPrompt string, err error) {
	prompt = buildPrompt(cfg)
	systemPrompt, err = m.buildSystemPrompt(ctx, cfg.Kind, cfg.ProjectID)
	if err != nil {
		return "", "", err
	}
	return prompt, systemPrompt, nil
}

// buildSystemPrompt derives the standing instructions for a session of the
// given kind from current store state. Restore recomputes them through here
// rather than persisting them, so a restored worker points at the orchestrator
// that is active now, not the one from its original spawn.
func (m *Manager) buildSystemPrompt(ctx context.Context, kind domain.SessionKind, projectID domain.ProjectID) (string, error) {
	project, err := m.loadProject(ctx, projectID)
	if err != nil {
		return "", err
	}
	cfg := systemPromptConfig{
		Role:    promptRoleForKind(kind),
		Project: promptProjectContext(projectID, project),
	}

	switch kind {
	case domain.KindOrchestrator:
		cfg.OrchestratorRules = project.Config.OrchestratorRules
	case domain.KindWorker:
		orchestratorID, ok, err := m.activeOrchestratorSessionID(ctx, projectID)
		if err != nil {
			return "", err
		}
		if ok {
			cfg.OrchestratorSessionID = string(orchestratorID)
		}
		rules, err := buildProjectRules(projectRulesConfig{
			ProjectPath:    project.Path,
			AgentRules:     project.Config.AgentRules,
			AgentRulesFile: project.Config.AgentRulesFile,
		})
		if err != nil {
			return "", err
		}
		cfg.ProjectRules = rules
	default:
		return "", nil
	}

	workspacePrompt, err := m.workspaceProjectPrompt(ctx, kind, projectID)
	if err != nil {
		return "", err
	}
	if workspacePrompt != "" {
		cfg.AdditionalSections = append(cfg.AdditionalSections, workspacePrompt)
	}
	if pointer := strings.TrimSpace(m.aoSkillPointer()); pointer != "" {
		cfg.AdditionalSections = append(cfg.AdditionalSections, pointer)
	}
	return buildSystemPromptText(cfg), nil
}

// aoSkillPointer is appended to every agent system prompt. It points the agent
// at the using-ao skill the daemon installs under the data dir, rather than
// inlining the whole CLI catalog. The path is absolute so it resolves from any
// project's worktree, not just the AO repo (the only place a repo-relative
// skills/ path would exist). The skill file carries exact flags and examples,
// so the standing prompt stays a short pointer rather than a command dump.
func (m *Manager) aoSkillPointer() string {
	dir := skillassets.Dir(m.dataDir)
	skillFile := filepath.ToSlash(filepath.Join(dir, "SKILL.md"))
	commandsGlob := filepath.ToSlash(filepath.Join(dir, "commands", "*.md"))
	browserFile := filepath.ToSlash(filepath.Join(dir, "commands", "browser.md"))
	previewFile := filepath.ToSlash(filepath.Join(dir, "commands", "preview.md"))
	return "\n\n" + "## Using the ao CLI\n\n" +
		"When using `ao`, read `" + skillFile + "` and only the relevant file under `" + commandsGlob + "`; do not load unrelated command guides.\n\n" +
		"## AO desktop Browser panel\n\n" +
		"For frontend work, read `" + previewFile + "` before previewing or starting an app: open static HTML or Markdown directly; Never create or modify `package.json` or install dependencies solely to display static files. Do not create `.ao/launch.json` unless the user asks. Automatically open the primary requested browser-displayable artifact immediately after creating or materially updating it, but do not replace an active application preview with a supporting asset. " +
		"For page inspection or interaction, read `" + browserFile + "` and use `ao browser` from this AO session. Browser network capture is optional and off by default; follow that guide and never enable it for routine browser actions. " +
		"Do not use Codex/host in-app browser connectors, `agent.browsers.get(\"iab\")`, or a browser MCP for the AO Browser panel: those are separate browser runtimes and cannot see or control AO's session-owned page. " +
		"`ao browser` operates the same live page the user sees in that panel."
}

func (m *Manager) workspaceProjectPrompt(ctx context.Context, kind domain.SessionKind, projectID domain.ProjectID) (string, error) {
	project, err := m.loadProject(ctx, projectID)
	if err != nil {
		return "", err
	}
	if project.Kind.WithDefault() != domain.ProjectKindWorkspace {
		return "", nil
	}
	repos, err := m.store.ListWorkspaceRepos(ctx, string(projectID))
	if err != nil {
		return "", fmt.Errorf("list workspace repos for prompt: %w", err)
	}
	switch kind {
	case domain.KindOrchestrator:
		return workspaceOrchestratorPrompt(repos), nil
	case domain.KindWorker:
		return workspaceWorkerPrompt(repos), nil
	default:
		return "", nil
	}
}

func (m *Manager) activeOrchestratorSessionID(ctx context.Context, project domain.ProjectID) (domain.SessionID, bool, error) {
	recs, err := m.store.ListSessions(ctx, project)
	if err != nil {
		return "", false, fmt.Errorf("list sessions for %s: %w", project, err)
	}
	for _, rec := range recs {
		if rec.Kind == domain.KindOrchestrator && !rec.IsTerminated {
			return rec.ID, true, nil
		}
	}
	return "", false, nil
}

func (m *Manager) writeSystemPromptFile(id domain.SessionID, systemPrompt string) (string, error) {
	if systemPrompt == "" || strings.TrimSpace(m.dataDir) == "" {
		return "", nil
	}
	path := filepath.Join(m.systemPromptDir(id), "system.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(strings.TrimRight(systemPrompt, "\n")+"\n"), 0o600); err != nil {
		return "", err
	}
	return path, nil
}

func (m *Manager) prepareSystemPromptFile(id domain.SessionID, harness domain.AgentHarness, systemPrompt string) (string, error) {
	path, err := m.writeSystemPromptFile(id, systemPrompt)
	if err == nil || path != "" {
		return path, err
	}
	if systemPromptFileRequired(harness) {
		return "", err
	}
	m.logger.Warn("system prompt file unavailable; falling back to inline system prompt", "session", id, "harness", harness, "err", err)
	return "", nil
}

func systemPromptFileRequired(harness domain.AgentHarness) bool {
	switch harness {
	case domain.HarnessAider,
		domain.HarnessAgy,
		domain.HarnessAuggie,
		domain.HarnessKiro,
		domain.HarnessOpenCode,
		domain.HarnessCopilot,
		domain.HarnessVibe,
		domain.HarnessCodex:
		return true
	default:
		return false
	}
}

func (m *Manager) systemPromptDir(id domain.SessionID) string {
	if strings.TrimSpace(m.dataDir) == "" {
		return ""
	}
	return filepath.Join(m.dataDir, "prompts", string(id))
}

func (m *Manager) cleanupSystemPromptDir(id domain.SessionID) {
	dir := m.systemPromptDir(id)
	if dir == "" {
		return
	}
	if err := os.RemoveAll(dir); err != nil {
		m.logger.Warn("system prompt cleanup failed", "session", id, "path", dir, "err", err)
	}
}

func workspaceOrchestratorPrompt(repos []domain.WorkspaceRepoRecord) string {
	return fmt.Sprintf(`## Workspace project

This project is a multi-repository workspace. Sessions start at the workspace root. The root repository is %s at path `+"`.`"+`; child repositories are nested below it.

Repositories:
%s

When spawning workers, name the repository path or paths they should work in. Work can span multiple repositories, so track deliverables, pull requests, and checks by repository.`, domain.RootWorkspaceRepoName, workspaceRepoList(repos))
}

func workspaceWorkerPrompt(repos []domain.WorkspaceRepoRecord) string {
	return fmt.Sprintf(`## Workspace project

This session is a multi-repository workspace. You start at the workspace root. The root repository is %s at path `+"`.`"+`; child repositories are nested below it.

Repositories:
%s

Before editing, identify which repository owns the task and keep changes scoped to the requested repository or repositories. If you touch root files, call that out explicitly because root changes are separate from child-repository changes.`, domain.RootWorkspaceRepoName, workspaceRepoList(repos))
}

func workspaceRepoList(repos []domain.WorkspaceRepoRecord) string {
	lines := make([]string, 0, 1+len(repos))
	lines = append(lines, fmt.Sprintf("- %s: .", domain.RootWorkspaceRepoName))
	for _, repo := range repos {
		lines = append(lines, fmt.Sprintf("- %s: %s", repo.Name, repo.RelativePath))
	}
	return strings.Join(lines, "\n")
}

// spawnEnv builds the runtime environment: the per-project env vars first, then
// the AO-internal vars last so they always win (a project cannot override
// AO_SESSION_ID and friends).
func spawnEnv(id domain.SessionID, project domain.ProjectID, issue domain.IssueID, dataDir string, projectEnv map[string]string) map[string]string {
	env := make(map[string]string, len(projectEnv)+4)
	for k, v := range projectEnv {
		env[k] = v
	}
	env[EnvSessionID] = string(id)
	env[EnvProjectID] = string(project)
	env[EnvIssueID] = string(issue)
	env[EnvDataDir] = dataDir
	return env
}

// runtimeEnv is spawnEnv plus the hook PATH pin: the session's PATH puts the
// running daemon's own directory first, so the bare `ao` in workspace hook
// commands resolves to the daemon that installed them rather than whatever
// `ao` is first on the inherited PATH (e.g. a legacy CLI without the hooks
// command, which fails every callback and silently kills activity tracking).
// When the pin cannot be applied the inherited PATH is kept and a warning is
// logged so the degradation isn't silent.
func (m *Manager) runtimeEnv(id domain.SessionID, project domain.ProjectID, issue domain.IssueID, projectEnv map[string]string) map[string]string {
	env := spawnEnv(id, project, issue, m.dataDir, projectEnv)
	env[EnvBrowserCapability] = ""
	env[EnvBrowserRuntimeToken] = ""
	env[EnvBrowserRuntimeTokenStdin] = ""
	path, err := HookPATH(m.executable, os.Getenv, projectEnv)
	if err != nil {
		m.logger.Warn("session PATH not pinned to the daemon binary; `ao hooks` callbacks may resolve to a different ao and activity tracking will stall",
			"session", id, "error", err)
		return env
	}
	env["PATH"] = path
	return env
}

func (m *Manager) launchRuntimeEnv(id domain.SessionID, project domain.ProjectID, issue domain.IssueID, projectEnv map[string]string) (map[string]string, string, error) {
	env := m.runtimeEnv(id, project, issue, projectEnv)
	if m.browserCapabilities == nil {
		return env, "", nil
	}
	token, verifier, err := m.browserCapabilities.Issue(id)
	if err != nil {
		return nil, "", err
	}
	if strings.TrimSpace(token) == "" || strings.TrimSpace(verifier) == "" {
		return nil, "", errors.New("browser capability issuer returned an empty credential")
	}
	env[EnvBrowserCapability] = token
	return env, verifier, nil
}

// persistBrowserCapabilityVerifier runs before the worker runtime starts. This
// closes the launch race where an eager worker could present its freshly
// injected token before the daemon had stored the verifier needed to validate
// it. The bearer token remains only in the runtime environment.
func (m *Manager) persistBrowserCapabilityVerifier(ctx context.Context, rec domain.SessionRecord, verifier string) (domain.SessionRecord, error) {
	if verifier == "" {
		return rec, nil
	}
	rec.Metadata.BrowserCapabilityVerifier = verifier
	rec.UpdatedAt = m.clock()
	if err := m.store.UpdateSession(ctx, rec); err != nil {
		return rec, err
	}
	return rec, nil
}

// HookPATH builds the PATH value pinned into a spawned session: the daemon
// executable's directory prepended to the base PATH (the project's PATH
// override when set, else the daemon's inherited PATH — matching what the
// runtime would have exported anyway). An error means the pin cannot be
// applied: the executable is unresolvable, or is not named "ao", in which case
// prepending its directory would not change what `ao` resolves to. Exported so
// the reviewer launcher can pin its pane's PATH the same way.
func HookPATH(executable func() (string, error), getenv func(string) string, projectEnv map[string]string) (string, error) {
	exe, err := executable()
	if err != nil {
		return "", fmt.Errorf("resolve daemon executable: %w", err)
	}
	name := filepath.Base(exe)
	if runtime.GOOS == "windows" {
		name = strings.TrimSuffix(strings.ToLower(name), ".exe")
	}
	if name != hookBinaryName {
		return "", fmt.Errorf("daemon executable %s is not named %q", exe, hookBinaryName)
	}
	base := projectEnv["PATH"]
	if base == "" {
		base = getenv("PATH")
	}
	dir := filepath.Dir(exe)
	if base == "" {
		return dir, nil
	}
	return dir + string(os.PathListSeparator) + base, nil
}

// provisionWorkspace applies the project's per-workspace setup after the
// worktree exists: symlink shared files from the project repo, then run any
// post-create commands. Either failing aborts the spawn so a half-provisioned
// workspace never launches an agent.
func (m *Manager) provisionWorkspace(ctx context.Context, project domain.ProjectRecord, workspacePath string) error {
	if err := applySymlinks(project.Path, workspacePath, project.Config.Symlinks); err != nil {
		return err
	}
	return runPostCreate(ctx, workspacePath, project.Config.PostCreate)
}

// applySymlinks links each repo-relative path into the workspace. A source that
// does not exist is skipped (symlinks are a convenience for optional files like
// .env); a real link failure aborts. Paths must be repo-relative with no
// parent traversal (no leading "/", no ".." segment) — a bad path is refused
// up front so a project config cannot escape the project or workspace tree.
func applySymlinks(projectPath, workspacePath string, symlinks []string) error {
	for _, rel := range symlinks {
		rel = strings.TrimSpace(rel)
		if rel == "" {
			continue
		}
		clean, err := safeRelPath(rel)
		if err != nil {
			return fmt.Errorf("symlink %q: %w", rel, err)
		}
		source := filepath.Join(projectPath, clean)
		if _, err := os.Stat(source); err != nil {
			continue
		}
		target := filepath.Join(workspacePath, clean)
		if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
			return fmt.Errorf("symlink %q: %w", rel, err)
		}
		if _, err := os.Lstat(target); err == nil {
			continue
		}
		if err := os.Symlink(source, target); err != nil {
			return fmt.Errorf("symlink %q: %w", rel, err)
		}
	}
	return nil
}

// safeRelPath confines rel to a repo-relative path: no absolute paths and no
// ".." segments (before or after Clean). The cleaned form is returned so
// callers join it against project/workspace roots safely.
func safeRelPath(rel string) (string, error) {
	if filepath.IsAbs(rel) || strings.HasPrefix(rel, "/") || strings.HasPrefix(rel, `\`) {
		return "", fmt.Errorf("path must be repo-relative")
	}
	clean := filepath.Clean(rel)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || clean == "." || clean == "" {
		return "", fmt.Errorf("path must be repo-relative")
	}
	for _, seg := range strings.Split(filepath.ToSlash(clean), "/") {
		if seg == ".." {
			return "", fmt.Errorf("path must be repo-relative")
		}
	}
	return clean, nil
}

// runPostCreate runs each post-create command in the workspace via the platform
// shell, so OS-agnostic commands like "pnpm install" work. A non-zero exit
// aborts the spawn with the command output.
func runPostCreate(ctx context.Context, workspacePath string, commands []string) error {
	for _, command := range commands {
		command = strings.TrimSpace(command)
		if command == "" {
			continue
		}
		var cmd *exec.Cmd
		if runtime.GOOS == "windows" {
			cmd = aoprocess.CommandContext(ctx, "cmd", "/c", command)
		} else {
			cmd = aoprocess.CommandContext(ctx, "sh", "-c", command)
		}
		cmd.Dir = workspacePath
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("postCreate %q: %w: %s", command, err, strings.TrimSpace(string(out)))
		}
	}
	return nil
}

// preLauncher is an optional Agent capability: a step the manager runs before
// launch. Claude Code implements it to record workspace trust in ~/.claude.json
// so its interactive "do you trust this folder?" dialog can't block the headless
// pane. Adapters that don't need it simply omit the method.
type preLauncher interface {
	PreLaunch(ctx context.Context, cfg ports.LaunchConfig) error
}

// workspaceCleaner is an optional Agent capability for durable agent-side state
// that should be released only after AO has actually removed the workspace.
type workspaceCleaner interface {
	CleanupWorkspace(ctx context.Context, cfg ports.WorkspaceHookConfig) error
}

type runtimeEnvAugmenter interface {
	AugmentRuntimeEnv(env map[string]string, dataDir string)
}

func (m *Manager) augmentAgentRuntimeEnv(agent ports.Agent, env map[string]string) {
	if augmenter, ok := agent.(runtimeEnvAugmenter); ok {
		augmenter.AugmentRuntimeEnv(env, m.dataDir)
	}
}

// prepareWorkspace runs the per-session pre-launch steps before the runtime
// starts the agent: installing the workspace-local activity hooks (so early
// startup hooks can update the already-created session row), then any optional
// PreLaunch step. Shared by Spawn and Restore.
func (m *Manager) prepareWorkspace(ctx context.Context, agent ports.Agent, id domain.SessionID, workspacePath, systemPrompt, systemPromptFile string, agentConfig ports.AgentConfig, env map[string]string) error {
	if err := agent.GetAgentHooks(ctx, ports.WorkspaceHookConfig{
		SessionID:        string(id),
		WorkspacePath:    workspacePath,
		DataDir:          m.dataDir,
		Env:              env,
		SystemPrompt:     systemPrompt,
		SystemPromptFile: systemPromptFile,
		Config:           agentConfig,
	}); err != nil {
		m.cleanupPreparedAgentWorkspace(ctx, agent, id, workspacePath, env)
		return fmt.Errorf("install hooks: %w", err)
	}
	if pl, ok := agent.(preLauncher); ok {
		if err := pl.PreLaunch(ctx, ports.LaunchConfig{DataDir: m.dataDir, SessionID: string(id), WorkspacePath: workspacePath}); err != nil {
			m.cleanupPreparedAgentWorkspace(ctx, agent, id, workspacePath, env)
			return fmt.Errorf("pre-launch: %w", err)
		}
	}
	return nil
}

func (m *Manager) cleanupPreparedAgentWorkspace(ctx context.Context, agent ports.Agent, id domain.SessionID, workspacePath string, env map[string]string) {
	cleaner, ok := agent.(workspaceCleaner)
	if !ok {
		return
	}
	if err := cleaner.CleanupWorkspace(ctx, ports.WorkspaceHookConfig{
		SessionID:     string(id),
		WorkspacePath: workspacePath,
		DataDir:       m.dataDir,
		Env:           env,
	}); err != nil {
		m.logger.Warn("session prepare rollback: failed to clean agent workspace state",
			"session", id, "workspacePath", workspacePath, "error", err)
	}
}

func (m *Manager) cleanupAgentWorkspace(ctx context.Context, rec domain.SessionRecord, workspacePath string) {
	if strings.TrimSpace(workspacePath) == "" {
		return
	}
	agent, ok := m.agents.Agent(rec.Harness)
	if !ok {
		return
	}
	cleaner, ok := agent.(workspaceCleaner)
	if !ok {
		return
	}
	env := spawnEnv(rec.ID, rec.ProjectID, rec.IssueID, m.dataDir, nil)
	if project, err := m.loadProject(ctx, rec.ProjectID); err == nil {
		env = m.runtimeEnv(rec.ID, rec.ProjectID, rec.IssueID, project.Config.Env)
	} else {
		m.logger.Warn("workspace cleanup: project env unavailable; agent cleanup using AO env only",
			"sessionID", rec.ID, "projectID", rec.ProjectID, "error", err)
	}
	if err := cleaner.CleanupWorkspace(ctx, ports.WorkspaceHookConfig{
		DataDir:       m.dataDir,
		Env:           env,
		SessionID:     string(rec.ID),
		WorkspacePath: workspacePath,
	}); err != nil {
		m.logger.Warn("workspace cleanup: agent cleanup failed", "sessionID", rec.ID, "workspacePath", workspacePath, "error", err)
	}
}

func (m *Manager) deliverAfterStartPrompt(ctx context.Context, agent ports.Agent, cfg ports.LaunchConfig, handle ports.RuntimeHandle, id domain.SessionID, prompt string) error {
	if err := m.waitForPromptReadiness(ctx, agent, cfg, handle); err != nil {
		return err
	}
	// Call Deliver directly (not the Guard.Send wrapper, which folds a suppressed
	// outcome into nil): a freshly-spawned session can terminate or hit a
	// permission dialog between readiness and prompt injection, and folding that
	// into success would report a spawn/restore that never delivered its prompt.
	outcome, err := m.messenger.Deliver(ctx, id, prompt)
	if err != nil {
		return fmt.Errorf("send %s: %w", id, err)
	}
	switch outcome {
	case sessionguard.SuppressedNotFound:
		return fmt.Errorf("send %s: %w", id, ErrNotFound)
	case sessionguard.SuppressedTerminated:
		return fmt.Errorf("send %s: %w", id, ErrTerminated)
	case sessionguard.SuppressedExited:
		return fmt.Errorf("send %s: %w", id, ErrAgentExited)
	case sessionguard.SuppressedAwaitingUser:
		return fmt.Errorf("send %s: %w", id, ErrAwaitingDecision)
	case sessionguard.SuppressedUnknown:
		return fmt.Errorf("send %s: pre-write session read failed", id)
	default:
		return nil
	}
}

func (m *Manager) waitForPromptReadiness(ctx context.Context, agent ports.Agent, cfg ports.LaunchConfig, handle ports.RuntimeHandle) error {
	provider, ok := agent.(ports.AgentPromptReadinessProvider)
	if !ok {
		return nil
	}
	hints, err := provider.PromptReadinessHints(ctx, cfg)
	if err != nil {
		return fmt.Errorf("prompt readiness: %w", err)
	}
	if hints.InitialDelay > 0 {
		if err := sleepContext(ctx, hints.InitialDelay); err != nil {
			return err
		}
	}
	if len(hints.Patterns) == 0 || hints.Timeout <= 0 {
		return nil
	}
	poll := hints.PollInterval
	if poll <= 0 {
		poll = 200 * time.Millisecond
	}
	lines := hints.Lines
	if lines <= 0 {
		lines = 80
	}

	deadline := time.NewTimer(hints.Timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(poll)
	defer ticker.Stop()

	for {
		output, err := m.runtime.GetOutput(ctx, handle, lines)
		if err == nil && promptOutputContains(output, hints.Patterns) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			// Prompt readiness is best-effort: a missing terminal marker must not
			// block spawn forever or be treated as confirmed readiness. Fall back
			// to delivering the prompt and make the degraded path observable.
			m.logger.Warn("prompt readiness timed out; falling back to after-start prompt delivery",
				"sessionID", cfg.SessionID,
				"kind", string(cfg.Kind),
				"timeout", hints.Timeout.String(),
				"pollInterval", poll.String(),
				"lines", lines,
			)
			return nil
		case <-ticker.C:
		}
	}
}

func promptOutputContains(output string, patterns []string) bool {
	for _, pattern := range patterns {
		if pattern != "" && strings.Contains(output, pattern) {
			return true
		}
	}
	return false
}

func sleepContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// restoreArgv builds the argv to relaunch a torn-down session: the agent's
// native resume command when it can continue the session, else a fresh launch
// for harnesses where replaying the saved prompt is acceptable. The agent
// signals via ok=false (e.g. no native session id captured yet). Returns
// ErrNotResumable when transcript-preserving restore is required but unavailable,
// or when a promptless, unresumable worker has nothing to restore from.
func restoreArgv(ctx context.Context, agent ports.Agent, id domain.SessionID, workspacePath string, meta domain.SessionMetadata, systemPrompt, systemPromptFile string, agentConfig ports.AgentConfig, kind domain.SessionKind, _ domain.AgentHarness, dataDir string) ([]string, ports.PromptDeliveryStrategy, RestoreMode, error) {
	ref := ports.SessionRef{
		ID:            string(id),
		WorkspacePath: workspacePath,
		Metadata:      map[string]string{ports.MetadataKeyAgentSessionID: meta.AgentSessionID},
	}
	cmd, ok, err := agent.GetRestoreCommand(ctx, ports.RestoreConfig{Session: ref, Kind: kind, DataDir: dataDir, SystemPrompt: systemPrompt, SystemPromptFile: systemPromptFile, Config: agentConfig, Permissions: agentConfig.Permissions})
	if err != nil {
		return nil, "", "", fmt.Errorf("restore command: %w", err)
	}
	if ok {
		return cmd, ports.PromptDeliveryInCommand, RestoreModeNative, nil
	}
	return freshLaunchArgv(ctx, agent, id, workspacePath, meta, systemPrompt,
		systemPromptFile, agentConfig, kind, dataDir, false)
}

// freshLaunchArgv builds the non-resume half of restoreArgv. Interface
// transitions also use it when an adapter proves its reserved id has no
// persisted history, both for preflight and for the actual target launch.
func freshLaunchArgv(ctx context.Context, agent ports.Agent, id domain.SessionID, workspacePath string, meta domain.SessionMetadata, systemPrompt, systemPromptFile string, agentConfig ports.AgentConfig, kind domain.SessionKind, dataDir string, allowPromptless bool) ([]string, ports.PromptDeliveryStrategy, RestoreMode, error) {
	// A saved prompt is replayed fresh. An orchestrator is promptless by design
	// and relaunches with the system prompt only. A promptless WORKER has no task
	// and no session id to restore from: do not blank-relaunch it.
	if meta.Prompt == "" && kind != domain.KindOrchestrator && !allowPromptless {
		return nil, "", "", ErrNotResumable
	}
	// Fall through to a fresh launch. Command-delivered agents receive
	// meta.Prompt in argv; after-start agents receive it via the messenger once
	// the runtime is live.
	launchCfg := ports.LaunchConfig{
		DataDir:          dataDir,
		SessionID:        string(id),
		WorkspacePath:    workspacePath,
		Kind:             kind,
		Prompt:           meta.Prompt,
		SystemPrompt:     systemPrompt,
		SystemPromptFile: systemPromptFile,
		Config:           agentConfig,
		Permissions:      agentConfig.Permissions,
	}
	delivery, err := agent.GetPromptDeliveryStrategy(ctx, launchCfg)
	if err != nil {
		return nil, "", "", fmt.Errorf("prompt delivery: %w", err)
	}
	if delivery == ports.PromptDeliveryAfterStart {
		launchCfg.Prompt = ""
	}
	argv, err := agent.GetLaunchCommand(ctx, launchCfg)
	if err != nil {
		return nil, "", "", fmt.Errorf("launch command: %w", err)
	}
	mode := RestoreModeFresh
	if meta.Prompt != "" {
		mode = RestoreModeSavedPrompt
	}
	return argv, delivery, mode, nil
}

// validateAgentBinary checks that argv[0] resolves via the manager's
// lookPath (exec.LookPath in prod) before any runtime work happens. Adapters
// that can't resolve their binary now return ports.ErrAgentBinaryNotFound from
// GetLaunchCommand directly; this guard is a defense-in-depth for adapters
// that return an argv[0] like "claude" without verifying. Some adapters prefix
// their command with `env KEY=value`; in that case validate the first real
// executable after the environment assignments.
func (m *Manager) validateAgentBinary(argv []string) error {
	if len(argv) == 0 {
		return fmt.Errorf("agent: empty launch argv: %w", ports.ErrAgentBinaryNotFound)
	}
	bin, ok := launchBinary(argv)
	if !ok {
		return fmt.Errorf("agent: launch argv missing binary: %w", ports.ErrAgentBinaryNotFound)
	}
	if _, err := m.lookPath(bin); err != nil {
		return fmt.Errorf("agent binary %q: %w", bin, ports.ErrAgentBinaryNotFound)
	}
	return nil
}

func launchBinary(argv []string) (string, bool) {
	if len(argv) == 0 {
		return "", false
	}
	if filepath.Base(argv[0]) != "env" {
		return argv[0], true
	}
	for _, arg := range argv[1:] {
		if strings.Contains(arg, "=") {
			continue
		}
		return arg, true
	}
	return "", false
}

func (m *Manager) augmentRuntimePATHForLaunchBinary(ctx context.Context, env map[string]string, argv []string) {
	AugmentRuntimePATHForLaunchBinary(ctx, env, argv, m.lookPath)
}

// AugmentRuntimePATHForLaunchBinary prepends the resolved launch binary
// directory to the runtime PATH. For Node-backed CLI shims, it also prepends a
// concrete Node runtime directory so shebangs like `#!/usr/bin/env node` work
// in GUI-launched terminals whose PATH may not include shell manager setup.
func AugmentRuntimePATHForLaunchBinary(ctx context.Context, env map[string]string, argv []string, lookPath func(string) (string, error)) {
	bin, ok := launchBinary(argv)
	if !ok || !filepath.IsAbs(bin) {
		return
	}
	launchDir := filepath.Dir(bin)
	if launchDir == "." || launchDir == string(filepath.Separator) {
		return
	}
	dirs := []string{launchDir}
	if isNodeLaunchBinary(bin) {
		if nodeDir := nodeRuntimeDir(ctx, lookPath); nodeDir != "" && nodeDir != launchDir {
			dirs = append(dirs, nodeDir)
		}
	}
	var parts []string
	if path := env["PATH"]; path != "" {
		parts = strings.Split(path, string(os.PathListSeparator))
	}
	for i := len(dirs) - 1; i >= 0; i-- {
		if !containsPathDir(parts, dirs[i]) {
			parts = append([]string{dirs[i]}, parts...)
		}
	}
	env["PATH"] = strings.Join(parts, string(os.PathListSeparator))
}

func isNodeLaunchBinary(path string) bool {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	const maxShebangBytes = 4096
	buf := make([]byte, maxShebangBytes)
	n, _ := f.Read(buf)
	line := string(buf[:n])
	if newline := strings.IndexByte(line, '\n'); newline >= 0 {
		line = line[:newline]
	}
	if !strings.HasPrefix(line, "#!") {
		return false
	}
	for _, field := range strings.Fields(strings.TrimPrefix(line, "#!")) {
		if filepath.Base(field) == "node" {
			return true
		}
	}
	return false
}

func containsPathDir(parts []string, dir string) bool {
	for _, part := range parts {
		if part == dir {
			return true
		}
	}
	return false
}

func nodeRuntimeDir(ctx context.Context, lookPath func(string) (string, error)) string {
	if err := ctx.Err(); err != nil || runtime.GOOS == "windows" {
		return ""
	}
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	if node, err := lookPath("node"); err == nil && node != "" {
		return filepath.Dir(node)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	fnmDir := os.Getenv("FNM_DIR")
	if fnmDir == "" {
		if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
			fnmDir = filepath.Join(xdg, "fnm")
		} else if runtime.GOOS == "darwin" {
			fnmDir = filepath.Join(home, "Library", "Application Support", "fnm")
		} else {
			fnmDir = filepath.Join(home, ".local", "share", "fnm")
		}
	}
	voltaHome := os.Getenv("VOLTA_HOME")
	if voltaHome == "" {
		voltaHome = filepath.Join(home, ".volta")
	}
	nvm := versionedNodeMatches(filepath.Join(home, ".nvm", "versions", "node", "*", "bin", "node"))
	if data, err := os.ReadFile(filepath.Join(home, ".nvm", "alias", "default")); err == nil {
		fields := strings.Fields(string(data))
		if len(fields) > 0 {
			nvm = preferNodeVersion(nvm, fields[0])
		}
	}
	fnmMatches := versionedNodeMatches(filepath.Join(fnmDir, "node-versions", "*", "installation", "bin", "node"))
	candidates := make([]string, 0, len(nvm)+len(fnmMatches)+3)
	candidates = append(candidates, nvm...)
	candidates = append(candidates, fnmMatches...)
	// Prefer explicitly selected/versioned runtimes over manager and package-
	// manager shims. A dormant ~/.volta installation must not override the NVM
	// default or newest fnm runtime merely because the GUI omitted shell setup.
	candidates = append(candidates, filepath.Join(voltaHome, "bin", "node"), "/opt/homebrew/bin/node", "/usr/local/bin/node")
	for _, candidate := range candidates {
		if err := ctx.Err(); err != nil {
			return ""
		}
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
			return filepath.Dir(candidate)
		}
	}
	return ""
}

func versionedNodeMatches(pattern string) []string {
	matches, _ := filepath.Glob(pattern)
	sort.SliceStable(matches, func(i, j int) bool {
		return compareNodeVersion(nodeVersionFromPath(matches[i]), nodeVersionFromPath(matches[j])) > 0
	})
	return matches
}

func nodeVersionFromPath(path string) string {
	dir := filepath.Dir(path)
	if filepath.Base(dir) == "bin" {
		dir = filepath.Dir(dir)
	}
	if filepath.Base(dir) == "installation" {
		dir = filepath.Dir(dir)
	}
	return filepath.Base(dir)
}

func preferNodeVersion(paths []string, version string) []string {
	version = normalizeNodeVersion(version)
	for i, path := range paths {
		if normalizeNodeVersion(nodeVersionFromPath(path)) != version {
			continue
		}
		out := make([]string, 0, len(paths))
		out = append(out, path)
		out = append(out, paths[:i]...)
		out = append(out, paths[i+1:]...)
		return out
	}
	return paths
}

func compareNodeVersion(a, b string) int {
	av, aok := parseNodeVersion(a)
	bv, bok := parseNodeVersion(b)
	for i := range av {
		if av[i] != bv[i] {
			if av[i] > bv[i] {
				return 1
			}
			return -1
		}
	}
	if aok != bok {
		if aok {
			return 1
		}
		return -1
	}
	return strings.Compare(a, b)
}

func parseNodeVersion(version string) ([3]int, bool) {
	var parsed [3]int
	fields := strings.Split(normalizeNodeVersion(version), ".")
	if len(fields) == 0 || fields[0] == "" {
		return parsed, false
	}
	for i := 0; i < len(fields) && i < len(parsed); i++ {
		n, err := strconv.Atoi(fields[i])
		if err != nil {
			return [3]int{}, false
		}
		parsed[i] = n
	}
	return parsed, true
}

func normalizeNodeVersion(version string) string {
	return strings.TrimPrefix(strings.TrimSpace(version), "v")
}

func (m *Manager) validateRuntimePrerequisites() error {
	if runtime.GOOS == "windows" {
		return nil
	}
	if path, err := m.lookPath("tmux"); err != nil || path == "" {
		return fmt.Errorf("%w: tmux required on macOS/Linux but not in PATH", ports.ErrRuntimePrerequisite)
	}
	return nil
}

func (m *Manager) superviseAgentProcess(agent ports.Agent, id domain.SessionID, env map[string]string, argv []string) ([]string, string, error) {
	detector, ok := agent.(ports.AgentExitDetector)
	if !ok || detector.ExitDetectionMode() != ports.AgentExitDetectionSupervisor {
		delete(env, EnvRuntimeLaunchID)
		return argv, "", nil
	}
	executable, err := m.executable()
	if err != nil {
		return nil, "", fmt.Errorf("resolve AO executable: %w", err)
	}
	launchID := m.newLaunchID()
	if strings.TrimSpace(launchID) == "" {
		return nil, "", errors.New("generated empty launch id")
	}
	env[EnvRuntimeLaunchID] = launchID
	wrapped := make([]string, 0, 8+len(argv))
	wrapped = append(wrapped, executable, "agent-process", "supervise", "--session", string(id), "--launch", launchID, "--")
	wrapped = append(wrapped, argv...)
	return wrapped, launchID, nil
}

func runtimeHandle(meta domain.SessionMetadata) ports.RuntimeHandle {
	return ports.RuntimeHandle{ID: meta.RuntimeHandleID}
}

func workspaceInfo(rec domain.SessionRecord) ports.WorkspaceInfo {
	return ports.WorkspaceInfo{
		Path:      rec.Metadata.WorkspacePath,
		Branch:    rec.Metadata.Branch,
		SessionID: rec.ID,
		ProjectID: rec.ProjectID,
		RepoPath:  rec.Metadata.WorkspaceRepoPath,
	}
}

func workspaceInfoFromRepoInfo(info ports.WorkspaceRepoInfo) ports.WorkspaceInfo {
	return ports.WorkspaceInfo{
		Path:      info.Path,
		Branch:    info.Branch,
		SessionID: info.SessionID,
		ProjectID: info.ProjectID,
		RepoPath:  info.RepoPath,
	}
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
