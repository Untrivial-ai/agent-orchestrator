package ports

import (
	"context"
	"errors"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// The Chat controller contract.
//
// This is deliberately separate from the Agent/Runtime ports: those describe
// launching a provider's native CLI into a terminal and writing keystrokes at
// it. A Chat driver speaks a machine protocol and reports typed events. Stretching
// one contract over both would make every call site guess which one it has.
//
// Rules this port exists to keep:
//   - provider DTOs never escape the adapter; callers see domain vocabulary;
//   - terminal byte streams are never a source of Chat events;
//   - an empty user message is not "press Enter"; there is no keystroke concept;
//   - shell-terminal lifecycle is unrelated to conversation lifecycle.

// Errors a Chat driver returns. They map to stable API error codes, so a client
// can tell "this harness cannot do Chat" from "Codex is not logged in".
var (
	// ErrChatUnsupported means the harness has no Chat driver at all.
	ErrChatUnsupported = errors.New("chat mode unsupported for harness")
	// ErrChatDriverUnavailable means the driver exists but its binary or
	// bridge is missing.
	ErrChatDriverUnavailable = errors.New("chat driver unavailable")
	// ErrChatDriverIncompatible means the installed provider speaks a protocol
	// version AO does not support.
	ErrChatDriverIncompatible = errors.New("chat driver incompatible")
	// ErrChatAuthRequired means the provider is installed but not authenticated.
	ErrChatAuthRequired = errors.New("chat driver requires authentication")
	// ErrChatResumeFailed means a stored provider conversation could not be
	// resumed. Callers must surface a recovery choice, never silently start a
	// new provider conversation.
	ErrChatResumeFailed = errors.New("chat conversation resume failed")
	// ErrChatNoActiveTurn means an interrupt found nothing to cancel — either AO
	// has no turn in flight, or the provider no longer considers the named turn
	// active. A driver must translate its provider's refusal into this rather than
	// letting a protocol error escape: pressing stop a moment too late is an
	// ordinary thing for a person to do, not an internal failure.
	ErrChatNoActiveTurn = errors.New("no active turn to interrupt")
	// ErrChatRequestNotPending means the request a decision names is not waiting
	// for one: already answered, superseded, or from a controller that has been
	// replaced. Two clients looking at the same approval is normal, so one of them
	// arriving second is an ordinary outcome and not a failure.
	ErrChatRequestNotPending = errors.New("chat request is not pending")
	// ErrChatDecisionNotOffered means the decision is not one the provider offered
	// for that request. It must be refused and the request left pending: consent
	// AO invented is not consent, and consuming the request on a bad decision
	// would leave the real answer with nothing to answer.
	ErrChatDecisionNotOffered = errors.New("chat decision was not offered for this request")
	// ErrChatConfigOptionInvalid means a client named an unknown option, sent the
	// wrong value type, or selected a value the provider did not advertise.
	ErrChatConfigOptionInvalid = errors.New("chat config option value is invalid")
	// ErrChatHistoryUnsettled means the native conversation still contains a
	// running or queued turn. Callers may retry until the provider reaches a
	// settled boundary; they must not project a partial replay as complete.
	ErrChatHistoryUnsettled = errors.New("chat conversation history is not settled")
	// ErrChatHistoryUnavailable means the provider can resume its model context
	// but cannot replay that context as typed history. ACP session/resume has this
	// property; session/load is required when a caller needs a transcript replay.
	ErrChatHistoryUnavailable = errors.New("chat conversation history replay is unavailable")
)

// ChatCapability names something a driver may or may not be able to do. AO gates
// features on these rather than on the harness name.
type ChatCapability string

// The capabilities AO currently checks.
const (
	ChatCapabilityStreaming ChatCapability = "streaming"
	ChatCapabilityTools     ChatCapability = "tools"
	ChatCapabilityApprovals ChatCapability = "approvals"
	ChatCapabilityInterrupt ChatCapability = "interrupt"
	ChatCapabilitySteer     ChatCapability = "steer"
	ChatCapabilityResume    ChatCapability = "resume"
	ChatCapabilityHistory   ChatCapability = "history"
	ChatCapabilityUsage     ChatCapability = "usage"
	ChatCapabilityDiffs     ChatCapability = "diffs"
	ChatCapabilityPlans     ChatCapability = "plans"
	// ChatCapabilityModels means the provider can enumerate the models it offers
	// and accept one per turn.
	ChatCapabilityModels ChatCapability = "models"
	// ChatCapabilityConfigOptions means the provider advertises live, typed
	// session controls such as model, thought level, mode, fast mode, or an agent
	// persona. The catalog is provider-owned and may change after any selection.
	ChatCapabilityConfigOptions ChatCapability = "config_options"
	// ChatCapabilityCompaction means the provider can summarize earlier history to
	// reclaim context.
	ChatCapabilityCompaction ChatCapability = "compaction"
	// ChatCapabilityRollback means history can be discarded back to a turn.
	ChatCapabilityRollback ChatCapability = "rollback"
	// ChatCapabilityFork means a conversation can be branched.
	ChatCapabilityFork ChatCapability = "fork"
	// ChatCapabilityRename means the thread carries a title AO can set.
	ChatCapabilityRename ChatCapability = "rename"
	// ChatCapabilitySkills means named skills can be enumerated and invoked.
	ChatCapabilitySkills ChatCapability = "skills"
	// ChatCapabilityRateLimits means the account's quota position is readable.
	ChatCapabilityRateLimits  ChatCapability = "rate_limits"
	ChatCapabilityInteractive ChatCapability = "user_input"
	// ChatCapabilityMCPReload means the provider's tool servers can be restarted
	// without restarting the conversation. Named so a client can gate the control
	// rather than offering it and reading the refusal.
	ChatCapabilityMCPReload ChatCapability = "mcp_reload"
	// ChatCapabilityImages means the provider accepts native image content in a
	// prompt. AO may still stage a worktree copy for durable/local context, but it
	// must not reduce an image to a path when this capability is available.
	ChatCapabilityImages ChatCapability = "images"
	// ChatCapabilityEmbeddedContext means the provider accepts embedded resources
	// (for example the contents selected through an @ mention) in a prompt.
	ChatCapabilityEmbeddedContext ChatCapability = "embedded_context"
	// ChatCapabilityElicitation means the provider can stop a turn to request a
	// structured form or an explicitly-consented external URL interaction.
	ChatCapabilityElicitation ChatCapability = "elicitation"
	// ChatCapabilityNestedAgents means child-agent activity carries its parent tool
	// id, allowing clients to preserve the hierarchy instead of flattening it into
	// the top-level command stream.
	ChatCapabilityNestedAgents ChatCapability = "nested_agents"
	// ChatCapabilityTerminalOutput means command results include the provider's
	// terminal identity/output metadata. It is read-only transcript richness, not
	// permission for the provider to execute through AO's terminal runtime.
	ChatCapabilityTerminalOutput ChatCapability = "terminal_output"
)

// ChatCapabilities is the set a driver reports from Probe.
type ChatCapabilities map[ChatCapability]bool

// Has reports whether the capability is present and enabled.
func (c ChatCapabilities) Has(capability ChatCapability) bool { return c[capability] }

// chatProductionFloor is the minimum a driver must support before AO will let a
// session that can mutate a workspace run in Chat mode. Without approvals a
// mutating agent has no gate; without interrupt the user cannot stop it; without
// resume a daemon restart silently loses the conversation.
var chatProductionFloor = []ChatCapability{
	ChatCapabilityStreaming,
	ChatCapabilityApprovals,
	ChatCapabilityInterrupt,
	ChatCapabilityResume,
}

// MissingProductionCapabilities returns the floor capabilities a driver lacks.
// An empty result means the driver is Chat-ready.
func MissingProductionCapabilities(caps ChatCapabilities) []ChatCapability {
	var missing []ChatCapability
	for _, want := range chatProductionFloor {
		if !caps.Has(want) {
			missing = append(missing, want)
		}
	}
	return missing
}

// ChatStartConfig is what a driver needs to open a new provider conversation.
type ChatStartConfig struct {
	SessionID domain.SessionID
	// DataDir is AO's state root. Provider bindings may write process-scoped
	// configuration beneath it, but must never use the worktree or an OS-default
	// application-data directory for AO-owned state.
	DataDir string
	// WorkspacePath is the session worktree. Always absolute: app-server-style
	// drivers resolve a relative path against their own process directory.
	WorkspacePath string
	// Env is the environment for the driver process and, transitively, for the
	// shell commands the agent runs. AO passes a HookPATH-augmented copy so the
	// agent can invoke `ao` — that is how an orchestrator delegates.
	Env map[string]string
	// Model is optional; empty defers to the provider's configured default.
	Model string
	// Permissions is AO's existing per-session approval policy. Drivers map it
	// onto their provider's native approval and sandbox settings.
	Permissions PermissionMode
	// SystemPrompt carries AO's standing instructions for the session.
	SystemPrompt string
	// AdditionalDirectories are extra absolute workspace roots the provider may
	// access alongside WorkspacePath. Workspace projects use this for child repo
	// worktrees; it is not a replacement for AO's worktree ownership.
	AdditionalDirectories []string
	// MCPServers are client-supplied tool servers for this provider conversation.
	// User/provider configuration still loads normally; these are additive.
	MCPServers []ChatMCPServerConfig
}

// ChatResumeConfig reattaches to a provider conversation after a restart.
type ChatResumeConfig struct {
	SessionID              domain.SessionID
	ProviderConversationID string
	DataDir                string
	WorkspacePath          string
	Env                    map[string]string
	Permissions            PermissionMode
	// SystemPrompt is recomputed by the session manager on restore and reapplied
	// to the provider process. It is not persisted in the conversation transcript.
	SystemPrompt          string
	AdditionalDirectories []string
	MCPServers            []ChatMCPServerConfig
}

// ChatMCPServerConfig is the provider-neutral session-setup shape for a tool
// server supplied by AO. Exactly one transport is meaningful according to Type.
// Secrets stay in Env/Headers and are sent only to the local provider process;
// this value is never part of a conversation snapshot.
type ChatMCPServerConfig struct {
	Name    string
	Type    string
	Command string
	Args    []string
	Env     map[string]string
	URL     string
	Headers map[string]string
}

// ChatContent is structured prompt context. Text remains on ChatUserMessage so
// the durable transcript has an ordinary readable message; these blocks enrich
// what the provider receives without leaking protocol DTOs above the adapter.
type ChatContent struct {
	Type     string `json:"type"`
	Data     string `json:"data,omitempty"`
	MIMEType string `json:"mimeType,omitempty"`
	URI      string `json:"uri,omitempty"`
	Name     string `json:"name,omitempty"`
	Text     string `json:"text,omitempty"`
}

// ChatUserMessage is one inbound request to the agent.
type ChatUserMessage struct {
	Text string
	// Content carries native images and resources for providers that negotiated
	// them. Drivers must reject an unsupported block rather than silently discard
	// context the user believed they sent.
	Content []ChatContent
	// ClientMessageID makes delivery idempotent: a retry with the same key must
	// not produce a second provider turn.
	ClientMessageID string
	// Origin records who is speaking. Automation shares the queue with the user
	// and can never resolve an approval.
	Origin domain.MessageOrigin
	// Settings are the per-turn provider choices for this message. Zero means the
	// conversation's own defaults.
	Settings ChatTurnSettings
}

// ChatTurnSettings are the per-turn choices a provider accepts alongside the
// message text.
//
// Per turn, not per session: the provider takes these on every turn/start, so a
// user can change model or approval posture between messages without restarting
// the agent. Empty fields mean "use whatever the conversation was started with",
// which is what makes this additive — a caller that sets nothing behaves exactly
// as before.
type ChatTurnSettings struct {
	// Model is the provider's model id, from ChatModel.ID.
	Model string
	// Effort is how much reasoning to spend, from ChatModel.Efforts.
	Effort string
	// Approval is AO's permission mode for this turn. The driver maps it onto
	// whatever approval policy and sandbox its provider understands.
	Approval PermissionMode
}

// IsZero reports whether nothing was chosen, so a dispatch can omit the fields
// entirely rather than sending empty strings the provider would have to interpret.
func (s ChatTurnSettings) IsZero() bool {
	return s.Model == "" && s.Effort == "" && s.Approval == ""
}

// ChatModel is one model the provider offers for a conversation.
//
// The list comes from the provider, never from a table in AO. A hardcoded catalog
// is wrong within a week: models are added, renamed, hidden per account, and
// gated by entitlement the provider knows about and AO does not.
type ChatModel struct {
	ID          string
	DisplayName string
	Description string
	// Default marks the model the provider would pick on its own.
	Default bool
	// Efforts are the reasoning levels this model supports, in the provider's
	// order. Empty means the model does not take one.
	Efforts []string
	// DefaultEffort is the level the provider uses when none is chosen.
	DefaultEffort string
}

// ChatModelLister is implemented by drivers whose provider can enumerate models.
// Optional: a driver without it simply offers no choice, and the UI shows none.
type ChatModelLister interface {
	ListModels(ctx context.Context) ([]ChatModel, error)
}

// ChatConfigOptionType is the interaction an advertised provider setting needs.
// ACP currently standardizes selects and is incubating booleans; keeping both in
// AO's vocabulary prevents protocol DTOs from leaking out of the adapter.
type ChatConfigOptionType string

const (
	// ChatConfigOptionSelect offers one value from provider-owned choices.
	ChatConfigOptionSelect ChatConfigOptionType = "select"
	// ChatConfigOptionBoolean offers a native on/off control.
	ChatConfigOptionBoolean ChatConfigOptionType = "boolean"
)

// ChatConfigOptionValue is the current or requested value for one provider
// setting. Exactly one field is meaningful according to the option's Type.
type ChatConfigOptionValue struct {
	Select  string
	Boolean *bool
}

// ChatConfigOptionChoice is one value in a select. Group fields preserve an
// agent's organization without making grouped menus a protocol concern above
// the adapter.
type ChatConfigOptionChoice struct {
	Value       string
	Name        string
	Description string
	Group       string
	GroupName   string
}

// ChatConfigOption is one live provider-owned session control.
//
// Category is a presentation hint (model, thought_level, mode, or a provider
// extension), never a correctness discriminator. Unknown categories must still
// render: that is how a newly released agent feature reaches AO without an AO
// release adding a new hardcoded setting.
type ChatConfigOption struct {
	ID          string
	Name        string
	Description string
	Category    string
	Type        ChatConfigOptionType
	Current     ChatConfigOptionValue
	Choices     []ChatConfigOptionChoice
}

// ChatConfigOptionController is implemented by conversations whose provider
// exposes session configuration. Set returns the complete post-change catalog:
// changing a model can add or remove effort and fast-mode choices, so callers
// must replace rather than patch their cached list.
type ChatConfigOptionController interface {
	ListConfigOptions(ctx context.Context) ([]ChatConfigOption, error)
	SetConfigOption(ctx context.Context, id string, value ChatConfigOptionValue) ([]ChatConfigOption, error)
}

// ChatUsage is token accounting for a conversation.
//
// ContextWindow and ContextUsed are what make a "how full is this conversation"
// readout possible; without the window the used figure is a number with no scale.
type ChatUsage struct {
	InputTokens  int64
	OutputTokens int64
	CachedTokens int64
	TotalTokens  int64
	// ContextUsed and ContextWindow are the conversation's position in the model's
	// context, not a running total of the session's spend.
	ContextUsed   int64
	ContextWindow int64
	// Providers may report context occupancy and cumulative accounting in
	// separate notifications. These flags distinguish an omitted group from a
	// meaningful zero (for example, context immediately after compaction).
	ContextKnown bool
	TotalsKnown  bool
	// Cost is cumulative provider-reported spend in Currency. Nil means the
	// provider did not report money (common for subscription-backed sessions).
	Cost     *float64
	Currency string
}

// ChatRateLimits is the account's quota position.
//
// Modelled because it explains a class of failure the user cannot otherwise
// diagnose: a turn that fails for a reason unrelated to what they asked.
type ChatRateLimits struct {
	// PrimaryUsedPercent and SecondaryUsedPercent are the provider's own windows
	// (typically a short and a long one). Negative means not reported.
	PrimaryUsedPercent   float64
	SecondaryUsedPercent float64
	// PrimaryResetsInSeconds is how long until the tighter window refills.
	PrimaryResetsInSeconds   int64
	SecondaryResetsInSeconds int64
	// PlanLabel is the provider's name for the account tier, when it says.
	PlanLabel string
}

// ChatTurnDiff is the running diff of what a turn changed on disk.
type ChatTurnDiff struct {
	Files []ChatDiffFile
}

// ChatDiffFile is one changed path in a turn diff.
type ChatDiffFile struct {
	Path      string
	Additions int
	Deletions int
	// Status is the provider's change kind: added, modified, deleted, renamed.
	Status string
	// OldPath is set for a rename.
	OldPath string
	// Patch is the unified diff for this file when the provider sends one.
	Patch string
}

// ChatModelReroute is the provider answering with a model other than the one that
// was asked for.
type ChatModelReroute struct {
	FromModel string
	ToModel   string
	// Reason is the provider's own word for why, carried verbatim.
	Reason string
}

// ChatAccount is what the provider says about the account a conversation runs
// under.
type ChatAccount struct {
	AuthMode  string
	PlanLabel string
	// ReauthRequired means the provider asked for credentials the client is
	// expected to supply. AO does not hold provider credentials, so this is
	// reported to the user rather than answered.
	ReauthRequired bool
	// ReauthReason is the provider's stated reason, e.g. "unauthorized".
	ReauthReason string
}

// ChatThreadState is the provider's lifecycle view of the thread.
type ChatThreadState struct {
	// Status is AO's neutral spelling of the provider's state.
	Status domain.ThreadStatus
	// WaitingOn are the provider's active flags in AO's spelling.
	WaitingOn []string
	// Archived is tri-state: nil means this report says nothing about archiving,
	// which is different from saying it is not archived.
	Archived *bool
	// Closed is set only by a report that the thread was closed.
	Closed bool
}

// ChatMCPServer is one tool server's startup state.
type ChatMCPServer struct {
	Name          string
	Status        string
	Error         string
	FailureReason string
}

// ChatSkill is one capability the provider exposes to the agent, which a user can
// invoke by name.
type ChatSkill struct {
	Name        string
	DisplayName string
	Description string
	// InputHint is the provider's short argument placeholder, for example
	// "<issue-number>". It is presentation only; invocation remains ordinary
	// message text beginning with /Name.
	InputHint string
	// Source says where it came from (built-in, a plugin, the project), so a user
	// can tell an AO-provided skill from one the provider ships.
	Source string
}

// ChatCompactionResult reports what a compaction did.
type ChatCompactionResult struct {
	// TokensBefore and TokensAfter bracket the reclaim, so the UI can say what was
	// gained rather than only that something happened.
	TokensBefore int64
	TokensAfter  int64
}

// Optional driver interfaces. Each is feature-detected, so a driver that cannot do
// one of these simply does not offer it and AO hides the affordance rather than
// showing a control that fails.
type (
	// ChatCompactor summarizes earlier history to reclaim context. Without it a
	// long conversation eventually cannot accept another turn at all.
	ChatCompactor interface {
		Compact(ctx context.Context) (ChatCompactionResult, error)
	}
	// ChatRollbacker discards history back to a turn, provider-side. This is undo:
	// it changes what the agent remembers, which is why it is a provider call and
	// not a UI filter.
	ChatRollbacker interface {
		Rollback(ctx context.Context, providerTurnID string) error
	}
	// ChatForker branches a conversation, so an alternative approach can be tried
	// without destroying the original. A nil anchor copies the whole thread; a
	// non-nil provider turn id copies through that turn, inclusive.
	ChatForker interface {
		Fork(ctx context.Context, lastProviderTurnID *string) (providerConversationID string, err error)
	}
	// ChatRenamer sets or reads a human title the provider derived for the thread.
	ChatRenamer interface {
		SetTitle(ctx context.Context, title string) error
	}
	// ChatSkillLister enumerates the named skills a user can invoke.
	ChatSkillLister interface {
		ListSkills(ctx context.Context) ([]ChatSkill, error)
	}
	// ChatUsageReporter reads the account's quota position on demand, for the
	// cases where waiting for the next notification is too late to be useful.
	ChatUsageReporter interface {
		ReadRateLimits(ctx context.Context) (ChatRateLimits, error)
	}
	// ChatMCPReloader restarts the provider's tool servers. It exists because the
	// failure it addresses is not the agent's: a server that failed to start, or
	// whose config changed on disk, leaves the agent short of tools for the rest of
	// the conversation, and the only alternative is throwing the session away.
	ChatMCPReloader interface {
		ReloadMCPServers(ctx context.Context) ([]ChatMCPServer, error)
	}
)

// ChatTurnRef identifies a turn the provider accepted.
type ChatTurnRef struct {
	ProviderTurnID string
}

// ChatDeferredTurnStarter is implemented by protocols whose prompt call is the
// whole lifetime of a turn rather than a quick "accepted" request. SendTurn
// prepares such a turn and returns its id; the controller calls StartDeferredTurn
// only after that id is durably bound to AO's turn row. This prevents a fast
// provider notification from racing ahead of the correlation record needed to
// project it.
//
// Drivers whose SendTurn already receives a provider acknowledgement (Codex's
// app-server, for example) do not implement this interface.
type ChatDeferredTurnStarter interface {
	StartDeferredTurn(providerTurnID string) error
	// DiscardDeferredTurn releases a prepared turn when AO could not persist its
	// correlation id. No provider request has started at this point.
	DiscardDeferredTurn(providerTurnID string)
}

// ChatDecisionOption is one choice the provider says is valid for a pending
// request. Providers vary the offered set per request — some do not offer a
// plain decline — so clients render from this list rather than a fixed enum.
type ChatDecisionOption struct {
	// ID is the stable identifier, e.g. "accept" or "acceptForSession".
	ID string
	// Label is a human-readable form when the provider supplies one.
	Label string
	// Raw is the provider's own encoding, preserved so a structured decision
	// (one carrying a policy amendment, say) can be echoed back exactly.
	Raw []byte
}

// ChatDecision is the answer to a pending request.
type ChatDecision struct {
	ID string
	// Raw, when set, is sent verbatim instead of ID. Used for the object-shaped
	// decisions some providers offer.
	Raw []byte
}

// ChatInputAction is the provider-neutral disposition of a structured input
// request. Keeping it typed makes the HTTP boundary and every driver share one
// vocabulary instead of validating repeated string literals independently.
type ChatInputAction string

const (
	// ChatInputActionAccept accepts a provider structured input request.
	ChatInputActionAccept ChatInputAction = "accept"
	// ChatInputActionDecline declines a provider structured input request.
	ChatInputActionDecline ChatInputAction = "decline"
	// ChatInputActionCancel cancels a provider structured input request.
	ChatInputActionCancel ChatInputAction = "cancel"
)

// Valid reports whether the action can cross the Chat port boundary.
func (a ChatInputAction) Valid() bool {
	return a == ChatInputActionAccept || a == ChatInputActionDecline || a == ChatInputActionCancel
}

// ChatInputMode distinguishes the two ACP elicitation shapes AO supports.
type ChatInputMode string

const (
	// ChatInputModeForm represents an ACP form elicitation.
	ChatInputModeForm ChatInputMode = "form"
	// ChatInputModeURL represents an ACP URL elicitation.
	ChatInputModeURL ChatInputMode = "url"
)

// Valid reports whether the mode has a defined provider-neutral representation.
func (m ChatInputMode) Valid() bool {
	return m == ChatInputModeForm || m == ChatInputModeURL
}

// ChatInputResponse answers a structured user-input request. Content is only
// meaningful for accept and must match the provider-supplied restricted schema.
type ChatInputResponse struct {
	Action  ChatInputAction
	Content map[string]any
}

// ChatInputRequest is a provider-neutral structured question. Schema is a flat,
// restricted JSON Schema object for form mode. URL is only present for url mode
// and must never be opened without the user's explicit consent.
type ChatInputRequest struct {
	Mode          ChatInputMode
	Message       string
	Schema        map[string]any
	URL           string
	ElicitationID string
}

// ChatInputResponder is optional because not every machine protocol supports
// structured user input. AO gates the route/UI on the negotiated capability.
type ChatInputResponder interface {
	ResolveInput(ctx context.Context, requestID string, response ChatInputResponse) error
}

// ChatEventKind discriminates ChatEvent. These are provider-neutral: a driver
// translates its native notifications into this set and drops what has no
// meaning here rather than inventing semantics.
type ChatEventKind string

// The event kinds a driver emits.
const (
	ChatEventTurnStarted   ChatEventKind = "turn.started"
	ChatEventTurnCompleted ChatEventKind = "turn.completed"
	// ChatEventUserMessageCompleted is a settled user message recovered from the
	// provider's native history. Live sends are already durable before dispatch and
	// therefore never emit this event; history readers use it to reconstruct turns
	// AO did not originally render (for example, work completed in the TUI).
	ChatEventUserMessageCompleted ChatEventKind = "message.user.completed"
	ChatEventMessageDelta         ChatEventKind = "message.delta"
	ChatEventMessageCompleted     ChatEventKind = "message.completed"
	ChatEventActivityStarted      ChatEventKind = "activity.started"
	ChatEventActivityCompleted    ChatEventKind = "activity.completed"
	ChatEventApprovalRequested    ChatEventKind = "approval.requested"
	ChatEventApprovalResolved     ChatEventKind = "approval.resolved"
	ChatEventInputRequested       ChatEventKind = "input.requested"
	ChatEventInputResolved        ChatEventKind = "input.resolved"
	ChatEventControllerState      ChatEventKind = "controller.state"
	ChatEventError                ChatEventKind = "error"

	// Kinds below carry provider signal AO previously discarded. Each is modelled
	// rather than folded into an activity so a reader can tell "the agent produced
	// output" from "the conversation itself changed".

	// ChatEventCommandOutputDelta is streamed output from a running command. The
	// final aggregate can be incomplete, so a reader that wants the whole output
	// has to accumulate these.
	ChatEventCommandOutputDelta ChatEventKind = "command.output.delta"
	// ChatEventTurnDiff is the running diff of what the turn has changed on disk.
	ChatEventTurnDiff ChatEventKind = "turn.diff"
	// ChatEventCompacted reports that the provider summarized earlier history to
	// reclaim context. It is a fact about the conversation, not a turn.
	ChatEventCompacted ChatEventKind = "thread.compacted"
	// ChatEventUsage is a token accounting update for the conversation.
	ChatEventUsage ChatEventKind = "usage"
	// ChatEventRateLimits is the account's quota position, which is why a turn can
	// fail for reasons that have nothing to do with the request.
	ChatEventRateLimits ChatEventKind = "account.rateLimits"
	// ChatEventThreadRenamed reports a title the provider derived for the thread.
	ChatEventThreadRenamed ChatEventKind = "thread.renamed"
	// ChatEventPlanUpdated is the agent's current plan for the turn.
	ChatEventPlanUpdated ChatEventKind = "turn.plan"

	// ChatEventReasoningDelta is streamed reasoning text for a reasoning activity.
	// It is a delta rather than a message because reasoning is not prose the user is
	// being addressed with: it belongs to the activity it explains, and a reader
	// must be able to hide it without losing the answer.
	ChatEventReasoningDelta ChatEventKind = "reasoning.delta"
	// ChatEventCommandInput is a keystroke stream the agent sent INTO a running
	// command's terminal. Separate from command output on purpose: a PTY echoes what
	// is typed, so folding input into the output stream would print it twice, and a
	// transcript that cannot distinguish the two cannot say who did what.
	ChatEventCommandInput ChatEventKind = "command.input"
	// ChatEventActivityText is streamed provider prose for an activity that is not
	// reasoning: MCP tool-call progress, patch application output. Folded into the
	// same accumulation, which is why one kind covers both.
	ChatEventActivityText ChatEventKind = "activity.text"
	// ChatEventModelRerouted reports the provider answering with a model other than
	// the one asked for. Without it a client keeps naming the model the user chose
	// while a different one is answering.
	ChatEventModelRerouted ChatEventKind = "model.rerouted"
	// ChatEventAccountChanged reports the provider account's auth mode, plan, or its
	// need for credentials AO does not hold.
	ChatEventAccountChanged ChatEventKind = "account.changed"
	// ChatEventThreadState reports the provider's own lifecycle view of the thread:
	// working, idle, archived, closed.
	ChatEventThreadState ChatEventKind = "thread.state"
	// ChatEventMCPServers reports the startup state of the tool servers this
	// conversation can reach, which is what explains a tool call that failed for a
	// reason the agent had no part in.
	ChatEventMCPServers ChatEventKind = "mcp.servers"
)

// ChatControllerState is the health of the driver's connection to the provider.
type ChatControllerState string

// Controller states. Unknown is not death: a failed probe is not proof a session
// is gone, matching how AO already treats runtime probes.
const (
	ChatControllerConnecting ChatControllerState = "connecting"
	ChatControllerReady      ChatControllerState = "ready"
	ChatControllerBusy       ChatControllerState = "busy"
	ChatControllerRecovering ChatControllerState = "recovering"
	ChatControllerStopped    ChatControllerState = "stopped"
)

// ChatEvent is one normalized observation from the provider.
//
// Deltas are the high-frequency case, so they carry only what changed. A
// projector folds a delta into the message identified by ProviderItemID and
// bumps its revision; it never allocates a new timeline position per token.
type ChatEvent struct {
	Kind ChatEventKind
	// ProviderEventID is an identity for this exact native event, when the
	// provider supplies one. It is deliberately distinct from ProviderItemID:
	// start, delta and completion events commonly share one item id.
	ProviderEventID string

	// ProviderTurnID is set on every event that belongs to a turn.
	ProviderTurnID string
	// ProviderConversationID identifies the native thread/session the event came
	// from. It may differ from ChatConversation.ProviderConversationID for nested
	// agent work. Empty preserves compatibility with providers whose protocol has
	// no nested-conversation identity.
	ProviderConversationID string
	// ProviderItemID identifies the message or activity being reported.
	ProviderItemID string
	// ClientMessageID is the provider-carried idempotency key for a recovered user
	// message, when one exists. History adapters synthesize a stable value when the
	// native protocol has no client identity.
	ClientMessageID string

	// TurnState is set on turn.completed.
	TurnState domain.TurnState

	// Delta is the text appended by a message.delta, a reasoning.delta, a
	// command.output.delta, a command.input or an activity.text.
	Delta string
	// Text is the settled text on message.completed, and the settled prose on an
	// activity that streamed text — a reasoning item's summary, once the provider
	// reports it. Empty means the provider settled nothing, which must not be read
	// as "the streamed text was wrong": a projection that replaced an accumulation
	// with an empty settle would erase reasoning the user watched arrive.
	Text string

	// ActivityKind and ActivityStatus are set on activity events.
	ActivityKind   domain.ActivityKind
	ActivityStatus domain.ActivityStatus
	// Summary is the one-line label for an activity or approval.
	Summary string
	// Detail is the provider-neutral typed payload, JSON-encoded.
	Detail []byte

	// RequestID and Decisions are set on approval.requested.
	RequestID string
	Decisions []ChatDecisionOption
	// Input is set on input.requested. It remains separate from Decisions because
	// a form response is structured data, not one provider-owned button id.
	Input *ChatInputRequest

	// ControllerState is set on controller.state.
	ControllerState ChatControllerState

	// Usage is set on usage events. Nil elsewhere.
	Usage *ChatUsage
	// RateLimits is set on account.rateLimits events. Nil elsewhere.
	RateLimits *ChatRateLimits
	// Diff is set on turn.diff.
	Diff *ChatTurnDiff
	// Title is set on thread.renamed.
	Title string

	// Plan is set on turn.plan. Nil elsewhere.
	Plan *domain.ConversationPlan
	// Reroute is set on model.rerouted.
	Reroute *ChatModelReroute
	// Account is set on account.changed.
	Account *ChatAccount
	// ThreadState is set on thread.state.
	ThreadState *ChatThreadState
	// MCPServers is set on mcp.servers. A single-server update carries one entry:
	// the provider reports servers one at a time, so a consumer merges by name
	// rather than replacing its whole list.
	MCPServers []ChatMCPServer

	// Err carries a structured failure. Its presence does not imply the
	// conversation is over; check ControllerState for that.
	Err error
}

// ChatDriver opens conversations for one harness.
type ChatDriver interface {
	// Harness is the agent this driver serves.
	Harness() domain.AgentHarness
	// Probe checks the local install without creating anything: is the binary
	// present, is it authenticated, what can it do. It must be safe to call
	// before any durable session or worktree exists, so an unsupported request
	// can be rejected before AO commits resources.
	Probe(ctx context.Context) (ChatCapabilities, error)
	// Start opens a new provider conversation.
	Start(ctx context.Context, cfg ChatStartConfig) (ChatConversation, error)
	// Resume reattaches to an existing one. It returns ErrChatResumeFailed
	// rather than silently starting a new conversation.
	Resume(ctx context.Context, cfg ChatResumeConfig) (ChatConversation, error)
}

// ChatConversation is one live controller. Exactly one exists per Chat session,
// and it is the only writer to its provider conversation.
type ChatConversation interface {
	// ProviderConversationID is the opaque identifier AO persists so a later
	// process can resume this conversation.
	ProviderConversationID() string
	// Capabilities is what this conversation actually negotiated, which can be
	// narrower than what Probe reported.
	Capabilities() ChatCapabilities
	// SendTurn delivers a user or automation message. Calls are serialized by
	// the controller: only one dispatch mutates the provider at a time.
	SendTurn(ctx context.Context, msg ChatUserMessage) (ChatTurnRef, error)
	// Interrupt cancels an in-flight turn.
	Interrupt(ctx context.Context, providerTurnID string) error
	// ResolveRequest answers a pending approval or user-input request. It is a
	// typed action: it is never derived from sending a message.
	ResolveRequest(ctx context.Context, requestID string, decision ChatDecision) error
	// Events is the normalized event stream. It closes when the conversation
	// ends. A consumer that falls behind is disconnected rather than allowed to
	// block the provider reader.
	Events() <-chan ChatEvent
	// Close releases the controller. It does not delete provider-side history.
	Close() error
}

// ChatHistoryReader is optionally implemented by a conversation whose native
// protocol can read the durable thread it resumed. Events are returned oldest
// first, settled, and with stable ProviderEventID values so importing the same
// native history after every interface switch is idempotent.
//
// This is deliberately provider-neutral. Codex implements it with thread/read;
// ACP implements it with session/load replay. Neither leaks provider wire formats
// into the Chat service.
type ChatHistoryReader interface {
	ReadHistory(ctx context.Context) ([]ChatEvent, error)
}

// ChatDriverRegistry resolves the driver for a harness.
type ChatDriverRegistry interface {
	// Driver returns the driver for a harness, or ErrChatUnsupported.
	Driver(harness domain.AgentHarness) (ChatDriver, error)
	// SupportsChat reports whether a harness has a Chat driver registered at
	// all, without probing the local install.
	SupportsChat(harness domain.AgentHarness) bool
}
