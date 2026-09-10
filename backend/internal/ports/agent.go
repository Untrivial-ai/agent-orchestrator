package ports

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// ErrAgentBinaryNotFound is returned by agent adapters when neither PATH nor
// any well-known install location holds the agent's binary. The session
// manager surfaces this BEFORE creating the runtime so a missing CLI doesn't
// silently launch into an empty tmux pane that the reaper later mistakes
// for a live session.
var ErrAgentBinaryNotFound = errors.New("agent: binary not found on PATH")

// AgentAuthStatus describes the result of a short local auth probe for an
// installed agent. It is advisory only: credentials, quota, selected model
// availability, or CLI state can still fail at session spawn/model-call time.
type AgentAuthStatus string

const (
	// AgentAuthStatusAuthorized means the local auth probe recently passed.
	// It does not guarantee that a later spawn or model call will succeed.
	AgentAuthStatusAuthorized AgentAuthStatus = "authorized"
	// AgentAuthStatusUnauthorized means the agent is installed but its local
	// auth probe reported missing or invalid authentication.
	AgentAuthStatusUnauthorized AgentAuthStatus = "unauthorized"
	// AgentAuthStatusUnknown means the daemon could not determine auth status.
	AgentAuthStatusUnknown AgentAuthStatus = "unknown"
)

// Agent is the contract every CLI coding agent adapter (claude-code, codex, …)
// must satisfy. It supplies the argv and process configuration the Session
// Manager needs to launch, restore, and read back a native agent session.
type Agent interface {
	// GetConfigSpec describes the agent-specific config keys AO can
	// expose to users in the AO config.
	GetConfigSpec(ctx context.Context) (ConfigSpec, error)

	// GetLaunchCommand builds the argv AO should run to start this agent.
	GetLaunchCommand(ctx context.Context, cfg LaunchConfig) (cmd []string, err error)

	// GetPromptDeliveryStrategy tells AO whether the prompt is included in
	// the launch command or must be sent after the agent process starts.
	GetPromptDeliveryStrategy(ctx context.Context, cfg LaunchConfig) (PromptDeliveryStrategy, error)

	// GetAgentHooks installs or merges AO hooks into the agent's
	// native workspace-local hook config. It must preserve user-defined hooks.
	GetAgentHooks(ctx context.Context, cfg WorkspaceHookConfig) error

	// GetRestoreCommand builds an argv that continues an existing native agent
	// session. ok=false means no existing native session can be continued.
	GetRestoreCommand(ctx context.Context, cfg RestoreConfig) (cmd []string, ok bool, err error)

	// SessionInfo reads agent-owned session metadata such as native session id,
	// display title, or summary. ok=false means no info is available.
	SessionInfo(ctx context.Context, session SessionRef) (info SessionInfo, ok bool, err error)
}

// AgentAuthChecker is the optional capability for adapters whose native CLI has
// a cheap local authentication status probe.
type AgentAuthChecker interface {
	AuthStatus(ctx context.Context) (AgentAuthStatus, error)
}

// AgentBinaryResolver is the optional capability adapters expose when their
// binary can be checked without constructing a real session launch command.
type AgentBinaryResolver interface {
	ResolveBinary(ctx context.Context) (path string, err error)
}

// AgentBinaryPresenceResolver is an optional startup-only refinement for an
// adapter whose normal binary resolution performs additional validation. It
// must only inspect local executable paths; it must not start the agent CLI.
// AO uses it for the first-render prerequisite gate, where existence is enough.
type AgentBinaryPresenceResolver interface {
	ResolveBinaryPresence(ctx context.Context) (path string, err error)
}

// AgentReadinessProvider is the daemon-owned coordination boundary used by
// launch and policy consumers. Implementations coalesce native checks and keep
// the resulting snapshots in memory.
type AgentReadinessProvider interface {
	EnsureAgentReadiness(ctx context.Context, agentID string, purpose domain.AgentReadinessPurpose) (domain.AgentReadinessSnapshot, error)
	InvalidateAgentInstallation(agentID string)
	InvalidateAgentAuthentication(agentID string)
	RecheckAgent(agentID string)
}

// AgentNativeSessionTerminator is an optional adapter capability used before
// AO destroys a terminal runtime or worktree whose agent may keep running in a
// detached native process. Implementations must affect only the supplied
// session and leave its transcript resumable.
type AgentNativeSessionTerminator interface {
	TerminateNativeSession(ctx context.Context, session SessionRef) error
}

// AgentInterfaceHandoff is an OPTIONAL capability for a TUI adapter whose
// native resume identity is also understood by its structured Chat driver.
// Merely supporting GetRestoreCommand is not enough: some harnesses expose a
// different identifier through their TUI and protocol surfaces.
type AgentInterfaceHandoff interface {
	NativeConversationID(
		ctx context.Context,
		session SessionRef,
		currentMode domain.SessionMode,
		providerConversationID string,
	) (id string, ok bool, err error)
}

// AgentInterfaceHandoffHistoryProbe is an OPTIONAL refinement for adapters
// that reserve a native conversation id before the provider has persisted any
// history. A missing history record means an interface transition may safely
// start the target fresh: there is no provider context to carry. Without this
// capability, Session Manager conservatively treats every declared id as an
// existing conversation and requires a native resume.
type AgentInterfaceHandoffHistoryProbe interface {
	NativeConversationExists(
		ctx context.Context,
		session SessionRef,
		nativeConversationID string,
		env map[string]string,
	) (bool, error)
}

// ModelSelectionMode tells clients how to render an agent's model control.
type ModelSelectionMode string

const (
	// ModelSelectionCatalog renders a model list reported by the selected agent.
	ModelSelectionCatalog ModelSelectionMode = "catalog"
	// ModelSelectionText renders a free-form model id input.
	ModelSelectionText ModelSelectionMode = "text"
	// ModelSelectionModeList renders an agent-owned mode list rather than model ids.
	ModelSelectionModeList ModelSelectionMode = "mode"
)

// CustomModelEntryMode tells clients how an agent handles models that are not
// present in its current catalog.
type CustomModelEntryMode string

const (
	// CustomModelEntryNone means the agent only accepts its reported choices.
	CustomModelEntryNone CustomModelEntryMode = "none"
	// CustomModelEntryDirect means AO may pass a user-entered model id directly.
	CustomModelEntryDirect CustomModelEntryMode = "direct"
	// CustomModelEntryConfigured means custom models must first be configured in
	// the agent and then discovered by AO as ordinary catalog entries.
	CustomModelEntryConfigured CustomModelEntryMode = "configured"
)

// AgentModelInfo is one model or mode that an adapter reports as selectable.
type AgentModelInfo struct {
	ID        string `json:"id"`
	Label     string `json:"label"`
	Provider  string `json:"provider,omitempty"`
	IsDefault bool   `json:"isDefault,omitempty"`
}

// AgentModelCatalog is AO's normalized model-picker response.
type AgentModelCatalog struct {
	AgentID          string               `json:"agentId"`
	SelectionMode    ModelSelectionMode   `json:"selectionMode" enum:"catalog,text,mode"`
	Models           []AgentModelInfo     `json:"models"`
	CustomModelEntry CustomModelEntryMode `json:"customModelEntry" enum:"none,direct,configured"`
	// AllowCustom is retained for compatibility and is true only for direct entry.
	AllowCustom bool   `json:"allowCustom"`
	Source      string `json:"source"`
	// BinaryVersion is the legacy wire name for AO's non-sensitive executable
	// and configuration metadata fingerprint.
	BinaryVersion string    `json:"binaryVersion,omitempty"`
	FetchedAt     time.Time `json:"fetchedAt"`
	ValidatedAt   time.Time `json:"validatedAt,omitempty"`
	// RefreshRecommended tells cache-first clients to revalidate in the
	// background while continuing to display the cached catalog.
	RefreshRecommended bool   `json:"refreshRecommended,omitempty"`
	Stale              bool   `json:"stale"`
	Warning            string `json:"warning,omitempty"`
}

// CachedAgentModelCatalog is the persistence record used by the model-catalog
// service. CatalogJSON contains a serialized AgentModelCatalog.
type CachedAgentModelCatalog struct {
	AgentID       string
	ProjectID     string
	BinaryVersion string // Legacy field name for the discovery-input metadata fingerprint.
	CatalogJSON   string
	Source        string
	FetchedAt     time.Time
}

// AgentModelCatalogCache persists normalized model catalogs across daemon
// restarts. Implementations must treat agent+project as the logical key.
type AgentModelCatalogCache interface {
	GetAgentModelCatalog(ctx context.Context, agentID, projectID string) (CachedAgentModelCatalog, bool, error)
	ListAgentModelCatalogsByAgent(ctx context.Context, agentID string) ([]CachedAgentModelCatalog, error)
	UpsertAgentModelCatalog(ctx context.Context, record CachedAgentModelCatalog) error
}

// AgentModelDiscoveryRequest describes one bounded, adapter-defined model
// discovery attempt. Args remain owned by the concrete discovery adapter.
type AgentModelDiscoveryRequest struct {
	AgentID    string
	Binary     string
	WorkingDir string
	Env        map[string]string
}

// AgentModelDiscoverer isolates CLI execution and discovery-input
// fingerprinting from the core agent service.
type AgentModelDiscoverer interface {
	Discover(ctx context.Context, request AgentModelDiscoveryRequest) (AgentModelCatalog, error)
	// CatalogFingerprint summarizes every input a discovery run would read: the
	// resolved executable plus any configuration the adapter consults. The
	// service compares it against the cached catalog's fingerprint, so it must
	// change whenever the catalog those inputs produce would change, and it must
	// stay cheap enough to compute before deciding to skip discovery.
	CatalogFingerprint(ctx context.Context, request AgentModelDiscoveryRequest) string
	Manual(agentID string) AgentModelCatalog
}

// AgentExitDetectionMode describes how AO learns that an agent CLI process
// ended while its terminal runtime remains alive.
type AgentExitDetectionMode string

const (
	// AgentExitDetectionSupervisor means AO must wrap the CLI in its generic
	// process supervisor because the adapter has no reliable exit hook.
	AgentExitDetectionSupervisor AgentExitDetectionMode = "supervisor"
)

// AgentExitDetector is an optional adapter capability. Adapters that omit it
// keep their existing launch behavior.
type AgentExitDetector interface {
	ExitDetectionMode() AgentExitDetectionMode
}

// AgentPromptReadinessProvider is an optional capability for interactive
// adapters that receive their first task after startup. It lets AO wait until a
// terminal UI is ready before injecting text through the runtime. When the
// adapter also implements TerminalActivityDetector, an authoritative idle
// detection takes precedence over the fallback text patterns.
type AgentPromptReadinessProvider interface {
	PromptReadinessHints(ctx context.Context, cfg LaunchConfig) (PromptReadinessHints, error)
}

// TerminalActivityDetector derives activity only from authoritative terminal UI markers.
type TerminalActivityDetector interface {
	DetectTerminalActivity(output string) (domain.ActivityState, bool)
}

// EmptyComposerDetector is an opt-in safety capability for unsolicited
// coordination sent to an already-running interactive agent. It must return
// true only when current terminal evidence positively proves that the active
// composer contains no human-authored draft. A stale activity=idle fact alone
// is insufficient because typing into a composer does not emit a lifecycle
// hook until the human submits it.
type EmptyComposerDetector interface {
	ComposerIsEmpty(output string) bool
}

// WaitingInputComposerReadiness is an opt-in capability for adapters where an
// empty composer authoritatively proves that a durable waiting_input state is
// safe for unsolicited delivery. EmptyComposerDetector alone is insufficient:
// other harnesses can render an empty composer beside a permission or
// structured-input boundary.
type WaitingInputComposerReadiness interface {
	EmptyComposerProvesWaitingInputReady() bool
}

// ContinuousTerminalActivityDetector is implemented by adapters whose TUI is
// the only authoritative source for some activity transitions. These adapters
// are sampled on every observer tick, including while idle or waiting for
// input, so terminal state can move in either direction.
type ContinuousTerminalActivityDetector interface {
	TerminalActivityDetector
	ContinuouslyDetectTerminalActivity() bool
}

// WaitingTerminalActivityDetector is implemented by non-continuous terminal
// detectors that can authoritatively recover from a durable waiting-input state.
type WaitingTerminalActivityDetector interface {
	TerminalActivityDetector
	ContinuouslyDetectTerminalActivityWhileWaiting() bool
}

// PromptReadinessHints describes when an after-start prompt should be sent.
// Empty patterns mean "send immediately" unless the adapter also implements
// TerminalActivityDetector, in which case AO waits for an authoritative idle
// detection. A non-positive timeout always preserves immediate delivery.
type PromptReadinessHints struct {
	InitialDelay time.Duration
	Patterns     []string
	PollInterval time.Duration
	Timeout      time.Duration
	Lines        int
}

// AgentResolver maps a session's harness onto the Agent adapter that drives it,
// so the Session Manager can spawn (and restore) a different agent per session
// without depending on the concrete adapter registry. ok=false means no adapter
// is registered for that harness.
type AgentResolver interface {
	Agent(harness domain.AgentHarness) (Agent, bool)
}

// SubmitActivitySignaler is an OPTIONAL capability an Agent adapter may
// implement to report whether its harness emits a prompt-submit signal (one
// that flips Activity.State to active). Without it the confirm loop could
// never observe active and would only burn its budget on spurious Enter
// nudges.
//
// The Session Manager uses this as one half of the bounded Enter
// re-submission gate for both ordinary messages and switched-agent
// continuations; it also requires BlockedActivitySignaler before it will
// nudge — see harnessNudgeSafe.
type SubmitActivitySignaler interface {
	EmitsSubmitActivity() bool
}

// BlockedActivitySignaler is an OPTIONAL capability an Agent adapter may
// implement to report whether its harness emits a decision-pause signal (a
// permission/approval prompt that flips Activity.State to blocked) AND can
// clear that state before the turn ends — which requires the pre/post-tool-
// use trio so lifecycle can correlate the approved tool's post with the dialog
// that blocked the session. The Enter-only nudge is only SAFE when this is
// true: a harness that submits but cannot report blocked leaves the confirm
// loop unable to tell an unsubmitted draft from a pending permission dialog,
// so an Enter meant to resubmit the draft could instead answer the dialog.
//
// Two adapters satisfy this today:
//
//   - claude-code installs the pre/post-tool-use trio that lets lifecycle
//     correlate the approved tool's post with the dialog and clear blocked
//     before the turn ends.
//   - kimchi installs the same trio and maps Notification(permission_prompt)
//     to ActivityBlocked. Unlike claude-code, kimchi has no separate
//     permission-request hook — the blocked signal arrives via a Notification
//     event whose payload carries tool_use_id, which the lifecycle correlator
//     matches against the inflight map populated by PreToolUse.
//
// codex maps permission-request to waiting_input and opts out (no tool trio →
// blocked could not be cleared). Adapters that later gain a correlatable
// blocked signal implement this interface to opt in.
type BlockedActivitySignaler interface {
	EmitsBlockedActivity() bool
}

// StartupInputReadinessSignaler is an OPTIONAL capability for a TUI adapter
// whose first lifecycle hook cannot arrive until native startup dialogs have
// cleared and the agent can safely accept pane input. AO gates user and
// automation writes on FirstSignalAt only for adapters that opt in here;
// hookless adapters must remain usable without manufacturing a signal.
type StartupInputReadinessSignaler interface {
	FirstSignalProvesInputReady() bool
}

// ActiveTurnSteerer is an OPTIONAL capability an Agent adapter implements when
// submitting input while its harness is mid-turn STEERS the running turn rather
// than being swallowed, queued, or applied to a dialog. AO uses it to decide
// whether an unsolicited coordination message may be written into an active
// session. Adapters that do not implement it are treated as unsafe to steer, so
// an unknown harness is only ever written to while idle.
type ActiveTurnSteerer interface {
	SteersActiveTurn() bool
}

// MetadataKeyAgentSessionID is the SessionRef.Metadata key that carries an
// agent's native session id. It matches the json tag on
// domain.SessionMetadata.AgentSessionID and the key the adapters read, so the
// Session Manager can bridge its typed metadata onto a SessionRef without
// either side hard-coding the other's vocabulary.
const MetadataKeyAgentSessionID = "agentSessionId"

// MetadataKeyTitle and MetadataKeySummary are the SessionRef.Metadata keys
// carrying a session's human title and one-line summary. They are the shared
// vocabulary every adapter reports under, so the dashboard renders agents
// uniformly.
const (
	MetadataKeyTitle   = "title"
	MetadataKeySummary = "summary"
)

// AgentConfig is the typed per-project agent config handed to adapters at
// launch. It aliases domain.AgentConfig so storage, services, and adapters
// share one definition without a translation layer.
type AgentConfig = domain.AgentConfig

// ConfigSpec describes the agent-specific config keys AO can expose to users.
type ConfigSpec struct {
	Fields []ConfigField
}

// ConfigField describes one user-facing agent config key.
type ConfigField struct {
	Key         string
	Type        ConfigFieldType
	Description string
	Required    bool
	Default     any
	Enum        []string
}

// ConfigFieldType is the primitive value kind AO expects for a field.
type ConfigFieldType string

// The primitive value kinds a ConfigField can declare.
const (
	ConfigFieldString     ConfigFieldType = "string"
	ConfigFieldBool       ConfigFieldType = "bool"
	ConfigFieldNumber     ConfigFieldType = "number"
	ConfigFieldStringList ConfigFieldType = "string_list"
	ConfigFieldEnum       ConfigFieldType = "enum"
)

// LaunchConfig carries inputs needed to build a new agent launch command.
type LaunchConfig struct {
	Config      AgentConfig
	DataDir     string
	IssueID     string
	Kind        domain.SessionKind
	Permissions PermissionMode
	Prompt      string
	SessionID   string
	// NativeSessionID optionally asks an adapter that supports caller-assigned
	// native identities to use this id for a fresh provider conversation. It is
	// deliberately separate from SessionID: one stable AO session may create
	// several provider-native conversations over its lifetime. Adapters whose
	// CLI assigns native ids ignore this field and report the id through hooks.
	NativeSessionID string
	// AllowedTools and DisallowedTools scope the agent to a tool allowlist when
	// it runs in a non-bypass permission mode (allow rules auto-approve, deny
	// rules auto-reject). They are the enforced read-only guarantee the reviewer
	// relies on: bypassPermissions ignores both lists, so a restricted launch
	// must leave Permissions off bypass. Empty means no restriction, so worker
	// sessions are unaffected.
	AllowedTools     []string
	DisallowedTools  []string
	SystemPrompt     string
	SystemPromptFile string
	WorkspacePath    string
}

// WorkspaceHookConfig carries inputs needed to install workspace-local agent hooks.
type WorkspaceHookConfig struct {
	Config           AgentConfig
	DataDir          string
	Env              map[string]string
	SessionID        string
	SystemPrompt     string
	SystemPromptFile string
	WorkspacePath    string
}

// RestoreConfig carries inputs needed to continue an existing native agent session.
type RestoreConfig struct {
	Config          AgentConfig
	DataDir         string
	Kind            domain.SessionKind
	Permissions     PermissionMode
	AllowedTools    []string
	DisallowedTools []string
	Session         SessionRef
	// Prompt is an optional new user turn to submit while resuming the native
	// conversation. Adapters whose CLI accepts a resume-time positional prompt
	// should append it to the restore command; after-start adapters leave it
	// empty and receive the turn through the interactive terminal instead.
	Prompt string
	// SystemPrompt carries the session's standing instructions (e.g. the
	// orchestrator role). Agent CLIs rebuild their system prompt from flags on
	// resume — it is not part of the transcript — so adapters whose CLI has a
	// system-prompt flag should re-apply this in their resume command.
	SystemPrompt     string
	SystemPromptFile string
}

// SessionRef identifies an AO session whose agent-owned metadata may be read.
type SessionRef struct {
	ID            string
	Metadata      map[string]string
	WorkspacePath string
	// DataDir is AO's isolated state root. Native lifecycle commands must use it
	// as their stable working/configuration root, never the session worktree.
	DataDir string
}

// SessionInfo contains agent-owned session metadata.
type SessionInfo struct {
	AgentSessionID string
	Metadata       map[string]string
	Title          string
	Summary        string
}

// PermissionMode controls how much review an agent requires before acting. It
// is a type alias for domain.PermissionMode so adapters keep using
// ports.PermissionMode while the typed AgentConfig (in domain) reuses the same
// type.
type PermissionMode = domain.PermissionMode

// The permission modes adapters map onto their agent's native approval flags.
// These re-export the domain constants so existing adapter code is unchanged.
const (
	PermissionModeDefault           = domain.PermissionModeDefault
	PermissionModeAcceptEdits       = domain.PermissionModeAcceptEdits
	PermissionModeAuto              = domain.PermissionModeAuto
	PermissionModeBypassPermissions = domain.PermissionModeBypassPermissions
)

// NormalizePermissionMode collapses an empty or unrecognized mode to
// PermissionModeDefault, leaving the four known modes unchanged. Adapters call
// it so a stored value they don't recognize defers to the agent's own config
// (usually by emitting no flag) rather than mapping onto a bogus one.
func NormalizePermissionMode(mode PermissionMode) PermissionMode {
	switch mode {
	case PermissionModeDefault,
		PermissionModeAcceptEdits,
		PermissionModeAuto,
		PermissionModeBypassPermissions:
		return mode
	default:
		return PermissionModeDefault
	}
}

// PromptDeliveryStrategy describes how AO should deliver the initial prompt.
type PromptDeliveryStrategy string

// How the orchestrator hands the initial prompt to a freshly launched agent.
const (
	PromptDeliveryInCommand   PromptDeliveryStrategy = "in_command"
	PromptDeliveryAfterStart  PromptDeliveryStrategy = "after_start"
	PromptDeliveryCustomAgent PromptDeliveryStrategy = "custom_agent"
)

// ErrLaunchNotReady is returned by a LaunchGate that refuses a spawn. AO must
// surface it instead of creating a child, so a session card never represents a
// child that was never able to become ready.
var ErrLaunchNotReady = errors.New("agent: launch not ready")

// LaunchGate is an optional, daemon-owned contract invoked after the workspace
// exists and the child argv is final, but before any child process is created.
//
// It exists because process creation is not successful startup. An agent can be
// spawned and then stop before its agent loop -- at a workspace-trust prompt, at
// a permission acknowledgement, or on a conversation that no longer exists --
// and AO would report that as ordinary work in progress. The gate is the one
// place where a caller still holds every trusted value (session, workspace, git
// common dir, resolved permissions, exact argv) and the child does not yet
// exist, so it can contribute a narrow child environment or refuse visibly.
//
// It deliberately mirrors the existing pre-launch agent-binary check: same
// position in Spawn, same fail-closed shape, same rollback. Nil disables it and
// leaves spawn behaviour byte-identical.
type LaunchGate interface {
	PreLaunch(ctx context.Context, req PreLaunchRequest) (PreLaunchDecision, error)
}

// PreLaunchRequest carries only daemon-owned values. Every field is resolved by
// AO itself; nothing here is supplied by the task or by the agent.
type PreLaunchRequest struct {
	SessionID     string
	Kind          domain.SessionKind
	Harness       domain.AgentHarness
	WorkspacePath string
	// GitCommonDir is the workspace's resolved git common directory, empty when
	// the workspace is not a git worktree. A gate that verifies AO-created
	// worktree provenance needs it, and must not infer it from WorkspacePath.
	GitCommonDir string
	Permissions  PermissionMode
	// Argv is the exact child command line, after adapter resolution and after
	// the binary check. A gate may read it to confirm that a resolved
	// permission mode actually reaches the child, but must not rewrite it.
	Argv []string
	// Env is the resolved child environment as it stands immediately before the
	// child is created. It is a copy: mutating it has no effect, and a gate
	// contributes through PreLaunchDecision.Env instead.
	//
	// A gate cannot do its job without this. An agent's own configuration root
	// is frequently an environment variable -- Claude reads CLAUDE_CONFIG_DIR --
	// and AO or its operator may already have set one. A gate that assumed the
	// default root would write its state to a file the child never reads, and
	// the child would still stop at the prompt that state was meant to answer,
	// with every observable surface reporting that the state exists. That exact
	// discrepancy was observed live: trust recorded true in the home root for
	// the precise worktree paths, and absent from the effective inherited root
	// the reviewer child actually used.
	Env map[string]string
	// LaunchID identifies this attempt, and is created before the gate is asked
	// so a decision can be bound to the launch it was made for. Without it a
	// gate cannot tell a current child from a replacement, which is what lets a
	// stale handshake make a new launch look ready.
	LaunchID string
	// ConversationID is the provider conversation this child will resume, empty
	// for a fresh one. A gate that must reason about resume validity needs to
	// know which conversation is being restored, not merely that one is.
	ConversationID string
	// Role distinguishes the session's own agent from a reviewer sidecar
	// launched beside it. Both create children, by separate paths, and a gate
	// that saw only one of them would leave the other able to strand silently.
	Role LaunchRole
}

// LaunchRole names which child a gate is being asked about.
type LaunchRole string

const (
	// LaunchRoleWorker is the session's own agent, created by Spawn.
	LaunchRoleWorker LaunchRole = "worker"
	// LaunchRoleReviewer is the reviewer sidecar, created by the review
	// launcher on its own path.
	LaunchRoleReviewer LaunchRole = "reviewer"
)

// PreLaunchDecision is a gate's answer. The zero value refuses, so a gate that
// returns nothing by mistake stops the launch rather than waving it through.
type PreLaunchDecision struct {
	// Env is merged into the child environment. Keys already set are not
	// replaced: a gate contributes, it does not take over the environment.
	Env map[string]string
	// EnvOverride replaces keys that are already set, which Env deliberately
	// cannot do. It is separate from Env so that taking ownership of a variable
	// is a visible act rather than a side effect of contributing one.
	//
	// It exists for one narrow case: an agent whose configuration root is chosen
	// by the environment. Claude reads CLAUDE_CONFIG_DIR, and an operator or an
	// outer process may already have set it. A gate that can only contribute is
	// then unable to put the child and whatever seeds the child's state in the
	// same root -- it writes trust to the root it owns, the child reads the root
	// it inherited, and the child stops at a prompt whose answer exists in a
	// file it never opens. That was observed live, and it is why contribute-only
	// is not enough.
	//
	// AO's own variables are never overridable: see LaunchGateProtectedEnv. A
	// gate may take a variable the agent owns; it may not take one the daemon
	// owns.
	EnvOverride map[string]string
	// Reason is required when Allow is false and is surfaced to the caller.
	Reason string
	// PromptKind optionally names the machine-readable blocker a gate
	// recognised, for example workspace_trust or bypass_acknowledgement.
	PromptKind string
	Allow      bool
}


// LaunchGateProtectedEnv reports whether a launch gate is forbidden to override
// this variable. AO's own names carry session identity and callback wiring into
// the child; rewriting them could redirect a session's activity reporting or its
// data store, which is a different power from choosing an agent's config root.
func LaunchGateProtectedEnv(key string) bool {
	return strings.HasPrefix(key, "AO_")
}
