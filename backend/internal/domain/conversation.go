package domain

import (
	"errors"
	"time"
)

// The durable conversation model for Chat-mode sessions.
//
// Two deliberate shapes here, both learned from how existing chat products
// settle:
//
//   - Messages and activities are separate records, not one polymorphic "item"
//     row. Assistant text is mutated in place as deltas arrive; a command
//     execution or approval is written once with a typed payload. Those are
//     different write patterns and fighting them in one table costs more than
//     the extra type.
//
//   - Sequence is conversation-scoped, assigned on insert, and immutable.
//     Timestamps are display metadata: provider events arrive out of order and
//     several can share a millisecond, so ordering must not depend on them.
//
// The provider conversation remains authoritative for model context. These rows
// are authoritative for what AO renders and for delivery state — AO never
// maintains a second independently writable model transcript.

// ConversationScope says whether a conversation belongs to a project (the
// orchestrator narrative, which outlives any single orchestrator session) or to
// one session (a worker).
type ConversationScope string

// Conversation scopes.
const (
	ConversationScopeSession ConversationScope = "session"
	ConversationScopeProject ConversationScope = "project"
)

// ConversationContextResetProviderItemID returns the durable identity of the
// hidden activity that separates a fresh project orchestrator context from the
// project conversation history that came before it.
func ConversationContextResetProviderItemID(session SessionID) string {
	return "ao-context-reset:" + string(session)
}

// TurnState is the lifecycle of one request and the agent work that follows it.
type TurnState string

// Turn states. Interrupted is distinct from failed: the provider reports it as
// its own terminal status when a turn is cancelled, and AO must not relabel it.
// Recovered means history proved the turn is no longer live but did not carry a
// portable provider outcome; it is terminal without claiming success or failure.
const (
	TurnStateQueued      TurnState = "queued"
	TurnStateRunning     TurnState = "running"
	TurnStateCompleted   TurnState = "completed"
	TurnStateRecovered   TurnState = "recovered"
	TurnStateInterrupted TurnState = "interrupted"
	TurnStateFailed      TurnState = "failed"
	// TurnStateCancelled marks a queued turn the user withdrew from the dock
	// before dispatch. Stop and handoff still settle the queue as interrupted.
	TurnStateCancelled TurnState = "cancelled"
)

// Terminal reports whether no further work is expected on the turn.
func (s TurnState) Terminal() bool {
	switch s {
	case TurnStateCompleted, TurnStateRecovered, TurnStateInterrupted, TurnStateFailed, TurnStateCancelled:
		return true
	default:
		return false
	}
}

// MessageRole distinguishes what the reader sees as their own text from the
// agent's.
type MessageRole string

// Message roles.
const (
	MessageRoleUser      MessageRole = "user"
	MessageRoleAssistant MessageRole = "assistant"
)

// MessageOrigin records who produced a message. It is durable and structural:
// worker and automation identity is never inferred from a text prefix.
type MessageOrigin string

// Message origins.
const (
	MessageOriginHuman      MessageOrigin = "human"
	MessageOriginAutomation MessageOrigin = "automation"
	MessageOriginDaemon     MessageOrigin = "daemon"
	MessageOriginProvider   MessageOrigin = "provider"
)

// ActivityKind is the type of a non-message timeline entry. Provider-specific
// detail lives in the activity's typed payload, never in a new kind per
// provider event subtype.
type ActivityKind string

// Activity kinds. Reasoning is retained as its own kind so it can be excluded
// from display without parsing message text.
const (
	ActivityKindCommand    ActivityKind = "command"
	ActivityKindFileChange ActivityKind = "file_change"
	ActivityKindPlan       ActivityKind = "plan"
	ActivityKindReasoning  ActivityKind = "reasoning"
	ActivityKindApproval   ActivityKind = "approval"
	ActivityKindUsage      ActivityKind = "usage"
	ActivityKindError      ActivityKind = "error"
	ActivityKindSystem     ActivityKind = "system"
	// ActivityKindMCPTool is a tool call into an MCP server. Its own kind rather
	// than a command: an MCP call has a server, a tool name, structured arguments
	// and a structured result, and none of that is a shell invocation. Reporting it
	// as a command made every tool call render as if the agent had run something in
	// the worktree.
	ActivityKindMCPTool ActivityKind = "mcp_tool"
	// ActivityKindAutoReview is an automatic approval review: the provider decided,
	// on the user's behalf, whether an action was safe enough to run without asking.
	// Distinct from ActivityKindApproval, which is a question waiting on a person.
	// A decision made FOR the user is exactly the thing they need to be able to see
	// after the fact.
	ActivityKindAutoReview ActivityKind = "auto_review"
	// ActivityKindUserInput is a structured question the provider is waiting for
	// the person to answer. It is not an approval: accepting a tool and supplying
	// form data have different response contracts and different UI.
	ActivityKindUserInput ActivityKind = "user_input"
)

// ActivityStatus is the lifecycle of one activity. A provider may omit an item's
// completion when its enclosing turn stops, so the turn settlement path also
// settles any activity that is still running.
type ActivityStatus string

// Activity statuses.
const (
	ActivityStatusRunning   ActivityStatus = "running"
	ActivityStatusCompleted ActivityStatus = "completed"
	// ActivityStatusRecovered means replay proved the activity is historical,
	// but the provider supplied no portable success/failure outcome.
	ActivityStatusRecovered ActivityStatus = "recovered"
	ActivityStatusFailed    ActivityStatus = "failed"
	ActivityStatusCancelled ActivityStatus = "cancelled"
	ActivityStatusPending   ActivityStatus = "pending"
	ActivityStatusResolved  ActivityStatus = "resolved"
)

// ConversationRecord is the durable head of one narrative.
type ConversationRecord struct {
	ID    string            `json:"id"`
	Scope ConversationScope `json:"scope"`
	// ProjectID is set for both scopes. SessionID is the session currently
	// controlling the conversation; for project-scoped orchestrators it changes
	// on clean replacement while the conversation identity remains stable.
	ProjectID ProjectID `json:"projectId"`
	SessionID SessionID `json:"sessionId,omitempty"`
	// ActiveBranchID identifies the one provider-thread lineage the session may
	// write. Sibling branches remain durable and are selected by moving this head;
	// display status is still derived independently at read time.
	ActiveBranchID string `json:"activeBranchId,omitempty"`
	// LatestSequence is the highest sequence handed out in this conversation.
	// New messages and activities take LatestSequence+1 under the same
	// transaction that bumps it, so ordering has a single writer.
	LatestSequence int64 `json:"latestSequence"`
	// Settings are the provider choices for the conversation's NEXT turn. Empty
	// fields mean the provider's own default, which is why a conversation nobody
	// configures behaves exactly as it did before these existed.
	Settings ConversationSettings `json:"settings"`
	// Usage is how full this conversation is. Nil until the provider reports.
	Usage *ConversationUsage `json:"usage,omitempty"`
	// RateLimits is where the account stands. Nil until the provider reports.
	RateLimits *ConversationRateLimits `json:"rateLimits,omitempty"`
	// CompactedAt is when the provider last summarized earlier history to reclaim
	// context. Nil means never. Held as conversation state as well as a timeline
	// entry so a client can tell whether compaction has run without walking an
	// unbounded timeline on every render.
	CompactedAt *time.Time `json:"compactedAt,omitempty"`
	// ProviderTitle is what the provider currently calls this thread. Kept even
	// when the user has overridden the AO label, because it is the name the
	// conversation carries in the provider's own history.
	ProviderTitle string `json:"providerTitle,omitempty"`
	// ModelReroute records the provider swapping the model mid-conversation. Nil
	// means it never has. Held as conversation state because the alternative is a
	// UI that keeps naming the model the user picked while a different one is
	// answering, which is the one thing a model selector must never do.
	ModelReroute *ConversationModelReroute `json:"modelReroute,omitempty"`
	// Account is the provider account this conversation runs under. Nil until the
	// provider says anything about it.
	Account *ConversationAccount `json:"account,omitempty"`
	// ThreadState is the provider's own view of the thread's lifecycle: whether it
	// is working, archived, or closed. Nil until the provider reports.
	ThreadState *ConversationThreadState `json:"threadState,omitempty"`
	// MCPServers is the startup state of the tool servers this conversation can
	// reach. Empty means none are configured or none has reported yet. It is here
	// because it answers a question the timeline cannot: a tool call that failed
	// because its server never started is not the agent's mistake.
	MCPServers []ConversationMCPServer `json:"mcpServers,omitempty"`
	// AppliedTitle is the last provider title AO wrote into the session's display
	// name. It is what makes "replace a label AO chose" distinguishable from
	// "overwrite a label a person chose". Empty means AO has never named it.
	AppliedTitle string    `json:"-"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

// ConversationBranchStrategy records how a provider branch was materialized.
// Native branches preserve provider-owned history; approximate branches seed a
// fresh provider session with bounded AO-owned textual context.
type ConversationBranchStrategy string

const (
	// ConversationBranchStrategyNative preserves the provider's exact history.
	ConversationBranchStrategyNative ConversationBranchStrategy = "native"
	// ConversationBranchStrategyApproximateContext starts a fresh provider
	// session from bounded AO-owned context.
	ConversationBranchStrategyApproximateContext ConversationBranchStrategy = "approximate_context"
)

// NormalizeConversationBranchStrategy keeps pre-strategy rows compatible. Only
// the empty legacy value means native; unknown nonempty values are preserved so
// a newer materialization strategy is never misreported as exact history.
func NormalizeConversationBranchStrategy(strategy ConversationBranchStrategy) ConversationBranchStrategy {
	if strategy == "" {
		return ConversationBranchStrategyNative
	}
	return strategy
}

// ConversationBranch is one durable provider-thread lineage node.
type ConversationBranch struct {
	ID                     string    `json:"id"`
	ConversationID         string    `json:"conversationId"`
	SessionID              SessionID `json:"sessionId"`
	ProviderConversationID string    `json:"-"`
	ParentBranchID         string    `json:"parentBranchId,omitempty"`
	ForkAfterTurnID        string    `json:"forkAfterTurnId,omitempty"`
	ReplacedTurnID         string    `json:"replacedTurnId,omitempty"`
	ReplacementTurnID      string    `json:"replacementTurnId,omitempty"`
	ForkAfterSequence      int64     `json:"-"`
	// Strategy distinguishes native anchored forks from approximate user-context
	// branches.
	Strategy             ConversationBranchStrategy `json:"strategy,omitempty"`
	ReplayCutoffSequence int64                      `json:"-"`
	ReplayTruncated      bool                       `json:"replayTruncated,omitempty"`
	// ProviderBindingID identifies the durable adapter/configuration epoch that
	// can reopen this branch. It is deliberately separate from ProviderScopeID:
	// approximate siblings own different opaque-id namespaces while remaining
	// reopenable by the same provider binding.
	ProviderBindingID string    `json:"-"`
	ProviderScopeID   string    `json:"-"`
	Active            bool      `json:"active"`
	CreatedAt         time.Time `json:"createdAt"`
}

// ConversationBranchPoint describes the sibling continuations available at one
// human prompt. Position is one-based; previous and next deliberately do not
// wrap so a client can disable the corresponding edge control.
type ConversationBranchPoint struct {
	TurnID           string `json:"turnId"`
	Position         int    `json:"position"`
	Total            int    `json:"total"`
	PreviousBranchID string `json:"previousBranchId,omitempty"`
	NextBranchID     string `json:"nextBranchId,omitempty"`
}

// ConversationEditAnchor is the immutable source material needed to fork
// immediately before one human prompt. The sequence cutoff applies uniformly to
// every branch-owned timeline table.
type ConversationEditAnchor struct {
	ConversationID              string
	SourceBranchID              string
	ReplacedTurnID              string
	PreviousProviderTurnID      string
	ForkAfterSequence           int64
	ReplayFloorSequence         int64
	HasPriorContext             bool
	OriginalDeliveryContentJSON string
	RetryActiveBranch           bool
}

// ConversationUsage is the conversation's token position.
//
// Latest-wins state on the conversation, not timeline entries. The provider
// reports this after every tool call, so a row per report is what buried the
// conversation the first time round; there is only ever one current answer to
// "how full is this".
type ConversationUsage struct {
	// ContextUsed and ContextWindow are what make the readout mean anything: a
	// used figure without the window is a number with no scale, which is exactly
	// what the header used to show.
	ContextUsed   int64 `json:"contextUsed"`
	ContextWindow int64 `json:"contextWindow"`
	// The cumulative spend for the conversation, which grows without bound and is
	// deliberately kept apart from the context figures above.
	InputTokens  int64 `json:"inputTokens"`
	OutputTokens int64 `json:"outputTokens"`
	CachedTokens int64 `json:"cachedTokens"`
	TotalTokens  int64 `json:"totalTokens"`
	// Cost is cumulative provider-reported spend. Nil is distinct from zero: most
	// subscription sessions report token position but no monetary charge.
	Cost     *float64 `json:"cost,omitempty"`
	Currency string   `json:"currency,omitempty"`
}

// ContextFraction is how full the context is, in 0..1, or -1 when the provider
// did not state a window.
//
// A caller must handle the negative case rather than dividing: without a window
// there is no honest fullness to report, and defaulting to 0 would draw an empty
// meter for a conversation that might be nearly full.
func (u ConversationUsage) ContextFraction() float64 {
	if u.ContextWindow <= 0 || u.ContextUsed < 0 {
		return -1
	}
	return float64(u.ContextUsed) / float64(u.ContextWindow)
}

// ConversationRateLimits is the account's quota position.
//
// Stored because it explains a class of failure the user cannot otherwise
// diagnose: a turn that fails for a reason unrelated to what they asked.
type ConversationRateLimits struct {
	// Percentages in 0..100. Negative means the provider did not report that
	// window, which is not the same as reporting it empty.
	PrimaryUsedPercent       float64 `json:"primaryUsedPercent"`
	SecondaryUsedPercent     float64 `json:"secondaryUsedPercent"`
	PrimaryResetsInSeconds   int64   `json:"primaryResetsInSeconds,omitempty"`
	SecondaryResetsInSeconds int64   `json:"secondaryResetsInSeconds,omitempty"`
	// PlanLabel is the provider's name for the account tier, when it says.
	PlanLabel string `json:"planLabel,omitempty"`
}

// WorstUsedPercent is the tighter of the two windows, or -1 when neither was
// reported. The tighter one is what will actually stop the next turn.
func (l ConversationRateLimits) WorstUsedPercent() float64 {
	worst := l.PrimaryUsedPercent
	if l.SecondaryUsedPercent > worst {
		worst = l.SecondaryUsedPercent
	}
	if worst < 0 {
		return -1
	}
	return worst
}

// ConversationModelReroute records the provider answering with a model other than
// the one that was asked for.
//
// Durable because it is a correction to a claim AO has already made. The composer
// says which model it is sending to; if the provider silently substitutes another,
// every later reading of that turn attributes the answer to the wrong model unless
// the substitution is written down.
type ConversationModelReroute struct {
	FromModel string `json:"fromModel"`
	ToModel   string `json:"toModel"`
	// Reason is the provider's own word for why. Carried verbatim rather than
	// translated: AO cannot improve on the provider's account of its own policy.
	Reason string `json:"reason,omitempty"`
	// ProviderTurnID is the turn the reroute happened on, so a client can point at
	// the exchange rather than only at the conversation.
	ProviderTurnID string    `json:"providerTurnId,omitempty"`
	At             time.Time `json:"at"`
}

// ConversationAccount is the provider account a conversation runs under.
//
// ReauthRequiredAt is the load-bearing field. A long-lived chat session outlives
// its credentials, and a provider that cannot refresh them stops answering for a
// reason that has nothing to do with the request. Recording the moment the
// provider asked for fresh credentials is what lets a client say "sign in again"
// instead of showing an unexplained failed turn.
type ConversationAccount struct {
	// AuthMode is the provider's name for how it authenticates (chatgpt, apikey...).
	AuthMode string `json:"authMode,omitempty"`
	// PlanLabel is the account tier the provider reports.
	PlanLabel string `json:"planLabel,omitempty"`
	// ReauthRequiredAt is when the provider last asked for credentials AO does not
	// hold. Nil means it never has.
	ReauthRequiredAt *time.Time `json:"reauthRequiredAt,omitempty"`
	// ReauthReason is the provider's stated reason, e.g. "unauthorized".
	ReauthReason string `json:"reauthReason,omitempty"`
}

// ThreadStatus is the provider's own lifecycle state for a thread. It is NOT AO's
// session status, which stays derived from durable facts at read time; this is one
// more such fact.
type ThreadStatus string

// Thread statuses. These mirror the provider's vocabulary rather than renaming it:
// a value AO does not recognize is stored as-is rather than being flattened into
// something AO does understand.
const (
	ThreadStatusActive      ThreadStatus = "active"
	ThreadStatusIdle        ThreadStatus = "idle"
	ThreadStatusNotLoaded   ThreadStatus = "not_loaded"
	ThreadStatusSystemError ThreadStatus = "system_error"
	ThreadStatusClosed      ThreadStatus = "closed"
)

// ConversationThreadState is what the provider says about the thread itself, as
// opposed to about a turn.
type ConversationThreadState struct {
	Status ThreadStatus `json:"status,omitempty"`
	// WaitingOn are the provider's active flags, e.g. "waiting_on_approval". A
	// thread can be active AND blocked on a person, and those are different states.
	WaitingOn []string `json:"waitingOn,omitempty"`
	// ArchivedAt is set while the provider considers the thread archived. Archiving
	// is reversible, so this returns to nil on unarchive rather than being a
	// one-way marker.
	ArchivedAt *time.Time `json:"archivedAt,omitempty"`
	// ClosedAt is when the provider dropped the thread. Recorded rather than acted
	// on: AO has never observed this notification, so tearing a controller down on
	// the strength of it would be guessing.
	ClosedAt  *time.Time `json:"closedAt,omitempty"`
	UpdatedAt time.Time  `json:"updatedAt"`
}

// ConversationMCPServer is one tool server's startup state.
type ConversationMCPServer struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	// Error is the provider's failure text, when it failed.
	Error string `json:"error,omitempty"`
	// FailureReason is the provider's classification, e.g.
	// "reauthenticationRequired", which is actionable in a way a message is not.
	FailureReason string `json:"failureReason,omitempty"`
}

// PlanStepStatus is where one plan step stands.
type PlanStepStatus string

// Plan step statuses. AO's own snake_case spelling, not the provider's camelCase:
// these are persisted and read by clients, and the rest of the durable vocabulary
// here is snake_case.
const (
	PlanStepPending    PlanStepStatus = "pending"
	PlanStepInProgress PlanStepStatus = "in_progress"
	PlanStepCompleted  PlanStepStatus = "completed"
)

// ConversationPlan is the agent's current plan for a turn.
//
// Structured steps rather than a blob of text. The provider sends a list of steps
// each carrying its own status and re-sends the whole list on every change, so the
// structure is what it actually said; flattening it to prose would throw away the
// one thing a reader wants, which is where the agent is up to.
type ConversationPlan struct {
	// Explanation is the agent's note about the plan as a whole, when it gives one.
	Explanation string                 `json:"explanation,omitempty"`
	Steps       []ConversationPlanStep `json:"steps"`
}

// ConversationPlanStep is one step of a plan.
type ConversationPlanStep struct {
	Text   string         `json:"text"`
	Status PlanStepStatus `json:"status"`
}

// ConversationSettings are the per-turn provider choices AO remembers.
//
// Durable rather than client-side because they must apply to turns AO dispatches
// on the user's behalf: a queued message draining after a restart, a relay from
// `ao send`. A preference held in the renderer would quietly stop applying the
// moment the user was not the one pressing send.
type ConversationSettings struct {
	// Model is the provider's model id. Empty means the provider's default.
	Model string `json:"model,omitempty"`
	// ReasoningEffort is how much thinking to spend, from the model's own list.
	ReasoningEffort string `json:"reasoningEffort,omitempty"`
	// ApprovalMode is AO's permission vocabulary, applied per turn.
	ApprovalMode PermissionMode `json:"approvalMode,omitempty"`
}

// ConversationTurn is one user or automation request plus the agent work it
// caused.
type ConversationTurn struct {
	ID             string `json:"id"`
	ConversationID string `json:"conversationId"`
	// BranchID is durable provider-lineage metadata used to keep opaque turn ids
	// inside the provider ownership epoch that created them.
	BranchID string `json:"-"`
	// HandledBySessionID is the AO session whose controller ran the turn. For a
	// project-scoped conversation this changes when the orchestrator is
	// replaced; the conversation identity does not.
	HandledBySessionID SessionID `json:"handledBySessionId"`
	// ProviderTurnID correlates back to the provider's own turn. Opaque.
	ProviderTurnID string `json:"providerTurnId,omitempty"`
	// RetryOfTurnID is the failed source whose durable prompt created this turn.
	// It remains present after rollback so the source cannot offer a dead action.
	RetryOfTurnID string `json:"retryOfTurnId,omitempty"`
	// HasRetryAttempt is a snapshot-only fact derived from every durable retry
	// relation, including attempts outside the active provider branch.
	HasRetryAttempt bool      `json:"hasRetryAttempt,omitempty"`
	State           TurnState `json:"state"`
	// ErrorMessage is set for failed turns. Interrupted turns are not errors.
	ErrorMessage string     `json:"errorMessage,omitempty"`
	RequestedAt  time.Time  `json:"requestedAt"`
	StartedAt    *time.Time `json:"startedAt,omitempty"`
	CompletedAt  *time.Time `json:"completedAt,omitempty"`
	// RolledBackAt is set when a rollback discarded this turn provider-side. The
	// row survives because AO does not destroy durable facts, but the agent no
	// longer remembers the exchange, so its messages and activities are left out
	// of the timeline. The turn itself stays readable so a client can say how much
	// an undo took back rather than letting the history silently shrink.
	RolledBackAt *time.Time `json:"rolledBackAt,omitempty"`
	// Diff is what the turn has changed on disk so far. Nil when the provider has
	// reported nothing, which is not the same as "changed nothing": a provider
	// without diff support never reports at all.
	Diff *ConversationTurnDiff `json:"diff,omitempty"`
	// Plan is the agent's current plan for this turn. Per-turn state rather than a
	// stack of timeline entries, for the same reason Diff is: the provider re-sends
	// the whole plan on every change, so only the latest is an answer and the
	// earlier ones are the same plan with fewer steps ticked off.
	Plan *ConversationPlan `json:"plan,omitempty"`
}

// ConversationTurnDiff is the turn's changed-file summary.
//
// Per-turn state rather than a timeline entry: the provider re-sends the whole
// diff on each update, so this is overwritten, and a reader sees the current
// answer instead of a stack of repeats.
type ConversationTurnDiff struct {
	Files []ConversationDiffFile `json:"files"`
	// Truncated reports that the file list was cut at AO's cap. A cut list shown
	// as a whole one would understate the size of the change.
	Truncated bool `json:"truncated,omitempty"`
}

// ChatDiffTruncatedSummary marks a turn-diff event whose file list the driver had
// to cut at its cap.
//
// It travels in the event's Summary because ports.ChatTurnDiff has no field for
// it, and the fact cannot simply be dropped: a cut list rendered as a complete one
// would understate the size of the change. Declared here so the adapter that sets
// it and the projection that reads it share one spelling rather than two string
// literals that can drift apart silently.
const ChatDiffTruncatedSummary = "diff file list truncated"

// ConversationDiffFile is one changed path in a turn diff.
type ConversationDiffFile struct {
	Path      string `json:"path"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
	// Status is the change kind: added, modified, deleted, renamed.
	Status string `json:"status"`
	// OldPath is set only for a rename.
	OldPath string `json:"oldPath,omitempty"`
	// RolledBackAt is set when a rollback discarded this turn provider-side. The
	// row survives because AO does not destroy durable facts, but the agent no
	// longer remembers the exchange, so its messages and activities are left out
	// of the timeline. The turn itself stays readable so a client can say how much
	// an undo took back rather than letting the history silently shrink.
	RolledBackAt *time.Time `json:"rolledBackAt,omitempty"`
}

// QueuedTurn is a turn that was recorded but never dispatched, paired with the
// text that still has to be sent.
//
// A message the user typed while the agent was busy is durable before it is
// delivered, so the queue survives a daemon restart as data rather than living
// only in a controller's memory.
type QueuedTurn struct {
	TurnID string
	Text   string
	// ClientMessageID is carried through to the provider so a dispatch retried
	// after a crash cannot produce a second provider turn.
	ClientMessageID string
	Origin          MessageOrigin
	// DeliveryContentJSON carries provider-neutral native prompt blocks through
	// the durable queue. It is not rendered and never contains provider DTOs.
	DeliveryContentJSON string
}

// RetryPrompt is the durable human-authored content of a failed turn that may
// be dispatched again. Unlike QueuedTurn, its source was already sent and
// settled; ActiveLineage says whether it still belongs to the visible branch.
type RetryPrompt struct {
	Text                string
	Origin              MessageOrigin
	DeliveryContentJSON string
	ActiveLineage       bool
}

// ConversationMessage is one readable block of text.
type ConversationMessage struct {
	ID             string `json:"id"`
	ConversationID string `json:"conversationId"`
	TurnID         string `json:"turnId,omitempty"`
	Sequence       int64  `json:"sequence"`
	// Revision increases each time streaming rewrites this message's text, so a
	// client can detect a gap and resync instead of rendering stale text.
	Revision int64         `json:"revision"`
	Role     MessageRole   `json:"role"`
	Origin   MessageOrigin `json:"origin"`
	Text     string        `json:"text"`
	// Streaming is true while more deltas are expected.
	Streaming bool `json:"streaming"`
	// ProviderItemID deduplicates provider observations of the same message.
	ProviderItemID string `json:"providerItemId,omitempty"`
	// ClientMessageID is the caller-supplied idempotency key for user messages.
	// A retry carrying the same key must not create a second provider turn.
	ClientMessageID     string    `json:"clientMessageId,omitempty"`
	DeliveryContentJSON string    `json:"-"`
	CreatedAt           time.Time `json:"createdAt"`
	UpdatedAt           time.Time `json:"updatedAt"`
}

// ConversationActivity is one non-message timeline entry: a command, a diff, a
// plan, an approval request, a usage report, an error.
type ConversationActivity struct {
	ID             string         `json:"id"`
	ConversationID string         `json:"conversationId"`
	TurnID         string         `json:"turnId,omitempty"`
	Sequence       int64          `json:"sequence"`
	Revision       int64          `json:"revision"`
	Kind           ActivityKind   `json:"kind"`
	Status         ActivityStatus `json:"status"`
	// Summary is the one-line label a client renders when collapsed.
	Summary string `json:"summary"`
	// Detail is the typed provider-neutral payload for this kind, as JSON. It
	// never carries provider DTOs verbatim.
	Detail []byte `json:"detail,omitempty"`
	// RequestID is the provider's request identifier for approval activities.
	// Resolving an approval matches on this, so a stale card cannot resolve a
	// newer request.
	RequestID      string    `json:"requestId,omitempty"`
	ProviderItemID string    `json:"providerItemId,omitempty"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
	// CommandOutput is streamed command output accumulated from output deltas.
	// Held outside Detail because it is appended to many times per command, while
	// Detail is written whole.
	//
	// Its presence does not make the output complete: the provider was measured
	// dropping the first chunk from the delta stream and the aggregate alike. What
	// it buys is output while the command is still running, and output at all for a
	// command that never completes.
	CommandOutput string `json:"commandOutput,omitempty"`
	// CommandOutputTruncated reports that accumulation stopped at AO's cap.
	CommandOutputTruncated bool `json:"commandOutputTruncated,omitempty"`
	// StreamedText is prose the PROVIDER streamed for this activity, accumulated
	// from deltas. What it means depends on Kind, because each kind has exactly one
	// such stream: reasoning summary text for a reasoning activity, the keystrokes
	// the agent sent to a PTY for a command, patch output for a file change,
	// progress messages for an MCP tool call.
	//
	// Deliberately not merged into CommandOutput. That column is a program's own
	// bytes -- capped, ANSI-laden, and admitted to be incomplete -- and a reader
	// must be able to tell "the agent thought this" or "the agent typed this" from
	// "the program printed this". They also settle differently: command output is
	// only ever appended to, while streamed reasoning is REPLACED by the settled
	// summary when the item completes.
	StreamedText string `json:"streamedText,omitempty"`
	// StreamedTextTruncated reports that accumulation stopped at AO's cap.
	StreamedTextTruncated bool `json:"streamedTextTruncated,omitempty"`
}

// ErrNoConversation reports that a session has no conversation row yet. It is not
// a failure: a Chat session has no conversation until its controller first
// starts, and a reader should show an empty conversation rather than an error.
var ErrNoConversation = errors.New("session has no conversation")

// ErrNoQueuedTurn reports that nothing is waiting to be sent. Draining an empty
// queue is the normal case, not an error.
var ErrNoQueuedTurn = errors.New("no queued turn")

// ErrNoConversationTurn reports a turn id that is not in the conversation it was
// named against. It lives here rather than in the storage layer so a controller and
// an HTTP handler can both recognize it without importing SQLite.
var ErrNoConversationTurn = errors.New("conversation turn not found")

// ErrNoConversationBranch reports a branch id outside the named conversation.
var ErrNoConversationBranch = errors.New("conversation branch not found")
