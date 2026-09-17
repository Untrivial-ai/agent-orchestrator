package ports

import (
	"context"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// AgentSwitchStore is the persistence contract for retained provider-native
// conversations and durable agent-switch sagas. UpdateAgentNativeSession,
// UpdateAgentSwitch, and FailAgentSwitchIfUnacknowledged are compare-and-swap
// operations: callers must supply the generation/state facts they observed,
// and false means a stale writer lost the fence without mutating durable state.
type AgentSwitchStore interface {
	CreateAgentNativeSession(ctx context.Context, rec domain.AgentNativeSession) (stored domain.AgentNativeSession, created bool, err error)
	GetAgentNativeSession(ctx context.Context, id domain.AgentNativeSessionID) (domain.AgentNativeSession, bool, error)
	ListAgentNativeSessions(ctx context.Context, sessionID domain.SessionID) ([]domain.AgentNativeSession, error)
	UpdateAgentNativeSession(ctx context.Context, rec domain.AgentNativeSession, expectedGenerationID domain.AgentGenerationID) (bool, error)

	CreateAgentSwitch(ctx context.Context, rec domain.AgentSwitch) (stored domain.AgentSwitch, created bool, err error)
	GetAgentSwitch(ctx context.Context, id domain.AgentSwitchID) (domain.AgentSwitch, bool, error)
	GetAgentSwitchByIdempotencyKey(ctx context.Context, sessionID domain.SessionID, idempotencyKey string) (domain.AgentSwitch, bool, error)
	GetActiveAgentSwitch(ctx context.Context, sessionID domain.SessionID) (domain.AgentSwitch, bool, error)
	ListAgentSwitches(ctx context.Context, sessionID domain.SessionID) ([]domain.AgentSwitch, error)
	UpdateAgentSwitch(ctx context.Context, rec domain.AgentSwitch, expectedState domain.AgentSwitchState, expectedSourceGenerationID, expectedTargetGenerationID domain.AgentGenerationID) (bool, error)
	FailAgentSwitchIfUnacknowledged(ctx context.Context, rec domain.AgentSwitch) (bool, error)
	RecordAgentHandoff(ctx context.Context, id domain.AgentSwitchID, sourceGenerationID domain.AgentGenerationID, status domain.AgentHandoffStatus, handoffPath, handoffHash string, updatedAt time.Time) (bool, error)
	FinalizeAgentSwitchHandoff(ctx context.Context, id domain.AgentSwitchID, sessionID domain.SessionID, sourceGenerationID, targetGenerationID domain.AgentGenerationID, handoffPath, handoffHash string, semanticIncluded bool, sourceTranscriptStatus domain.AgentSwitchSourceTranscriptStatus, updatedAt time.Time) (bool, error)
	ConfirmAgentSwitchSourceStopped(ctx context.Context, confirmation domain.AgentSwitchSourceStopConfirmation) (bool, error)
	AcknowledgeAgentSwitchTarget(ctx context.Context, id domain.AgentSwitchID, sessionID domain.SessionID, targetGenerationID domain.AgentGenerationID, acknowledgedAt time.Time) (bool, error)
	ActivateAgentSwitchTarget(ctx context.Context, activation domain.AgentSwitchTargetActivation) (bool, error)
	ActivateChatAgentSwitchTarget(ctx context.Context, activation domain.AgentSwitchChatTargetActivation) (bool, error)
}

// AgentSwitchMutation combines a fenced saga update with optional failure
// enrollment authority.
type AgentSwitchMutation struct {
	Record                     domain.AgentSwitch
	ExpectedState              domain.AgentSwitchState
	ExpectedSourceGenerationID domain.AgentGenerationID
	ExpectedTargetGenerationID domain.AgentGenerationID
	Fault                      *domain.AgentSwitchFault
	Authorization              domain.AgentSwitchReportingAuthorization
}

// AgentSwitchMutationResult reports whether the saga changed and how failure
// enrollment concluded.
type AgentSwitchMutationResult struct {
	CoreChanged bool
	Enrollment  domain.AgentSwitchEnrollmentStatus
}

// AgentSwitchOperationalFault is fenced by the durable switch fingerprint
// reread in the enqueue transaction. DaemonRunID is required only for the
// maintenance-failure dedupe formula.
type AgentSwitchOperationalFault struct {
	SwitchID             domain.AgentSwitchID
	ExpectedState        domain.AgentSwitchState
	ExpectedErrorCode    domain.AgentSwitchErrorCode
	ExpectedFailurePoint domain.AgentSwitchFailurePoint
	ExpectedUpdatedAt    time.Time
	DaemonRunID          string
	Fault                domain.AgentSwitchFault
	Authorization        domain.AgentSwitchReportingAuthorization
}

// AgentSwitchDaemonFault describes a daemon-scoped failure that is not tied to
// a durable switch row.
type AgentSwitchDaemonFault struct {
	DaemonRunID   string
	Fault         domain.AgentSwitchFault
	Authorization domain.AgentSwitchReportingAuthorization
}

// AgentSwitchFaultStore atomically persists fenced saga mutations and enrolls
// their associated failure reports.
type AgentSwitchFaultStore interface {
	ApplyAgentSwitchMutation(context.Context, AgentSwitchMutation) (AgentSwitchMutationResult, error)
	FailAgentSwitchIfUnacknowledgedWithFault(context.Context, AgentSwitchMutation) (AgentSwitchMutationResult, error)
	EnqueueAgentSwitchOperationalFault(context.Context, AgentSwitchOperationalFault) (AgentSwitchMutationResult, error)
	EnqueueAgentSwitchDaemonFault(context.Context, AgentSwitchDaemonFault) (AgentSwitchMutationResult, error)
}

// AgentSwitchFailurePolicy is the durable reporting authority mirror and its
// update time.
type AgentSwitchFailurePolicy struct {
	Authorization domain.AgentSwitchReportingAuthorization
	UpdatedAt     time.Time
}

// AgentSwitchFailurePolicyAcknowledgement is proof produced by one completed
// daemon policy-control call. PurgeConfirmed is generation-bound and is true
// only after a disabled policy mirror and the outbox purge commit atomically.
type AgentSwitchFailurePolicyAcknowledgement struct {
	Authorization  domain.AgentSwitchReportingAuthorization
	GateDrained    bool
	PurgeConfirmed bool
}

// AgentSwitchFailureEventMetadataStore configures the immutable metadata used
// when building persisted failure events.
type AgentSwitchFailureEventMetadataStore interface {
	ConfigureAgentSwitchFailureEventMetadata(context.Context, domain.AgentSwitchEventMetadata) error
}

// AgentSwitchFailureRecoveryEnrollment authorizes a one-time scan that enrolls
// retained recovery markers.
type AgentSwitchFailureRecoveryEnrollment struct {
	Authorization domain.AgentSwitchReportingAuthorization
	EnrolledAt    time.Time
}

// AgentSwitchFailureClaimRequest contains the authority and lease fence for one
// outbox claim.
type AgentSwitchFailureClaimRequest struct {
	Authorization  domain.AgentSwitchReportingAuthorization
	DeliveryEpoch  int64
	LeaseToken     string
	Now            time.Time
	LeaseExpiresAt time.Time
}

// AgentSwitchFailureClaim is a leased immutable event ready for one bounded
// delivery attempt.
type AgentSwitchFailureClaim struct {
	ID                     string
	Event                  domain.AgentSwitchFailureEvent
	LeaseToken             string
	ConsentGeneration      string
	DeliveryEpoch          int64
	DestinationFingerprint string
	ExpiresAt              time.Time
	AttemptCount           int64
}

// AgentSwitchFailureAttempt identifies and fences the start of an external
// delivery call.
type AgentSwitchFailureAttempt struct {
	ID                     string
	LeaseToken             string
	ConsentGeneration      string
	DeliveryEpoch          int64
	DestinationFingerprint string
	Now                    time.Time
}

// AgentSwitchFailureSettlement applies a provider-neutral result to the exact
// leased delivery attempt.
type AgentSwitchFailureSettlement struct {
	ID                     string
	LeaseToken             string
	ConsentGeneration      string
	DeliveryEpoch          int64
	DestinationFingerprint string
	SettledAt              time.Time
	NextAvailableAt        time.Time
	Result                 DeliveryResult
}

// AgentSwitchFailureReceiptResolution marks receipts resolved against an exact
// durable switch fingerprint.
type AgentSwitchFailureReceiptResolution struct {
	SwitchID                domain.AgentSwitchID
	DurableStateFingerprint string
	ResolvedAt              time.Time
}

// AgentSwitchFailureBacklog summarizes outbox state without exposing payloads.
type AgentSwitchFailureBacklog struct {
	Pending   int64
	Leased    int64
	Delivered int64
	Discarded int64
	OldestDue time.Time
}

// AgentSwitchFailureOutboxStore manages policy-gated enrollment, leasing,
// settlement, expiry, and receipt resolution.
type AgentSwitchFailureOutboxStore interface {
	ForceDisableAgentSwitchFailurePolicy(context.Context, time.Time) error
	// ApplyAgentSwitchFailurePolicy atomically mirrors policy and, when it is
	// disabled, deletes every outbox payload without deleting receipts.
	ApplyAgentSwitchFailurePolicy(context.Context, AgentSwitchFailurePolicy) error
	PurgeAgentSwitchFailurePayloads(context.Context) (int64, error)
	EnrollCurrentAgentSwitchRecoveryMarkers(context.Context, AgentSwitchFailureRecoveryEnrollment) (int64, error)
	ClaimAgentSwitchFailure(context.Context, AgentSwitchFailureClaimRequest) (AgentSwitchFailureClaim, bool, error)
	BeginAgentSwitchFailureAttempt(context.Context, AgentSwitchFailureAttempt) (bool, error)
	SettleAgentSwitchFailureDelivery(context.Context, AgentSwitchFailureSettlement) (bool, error)
	ExpireAgentSwitchFailurePayloads(context.Context, time.Time) (int64, error)
	ResolveAgentSwitchFailureReceipts(context.Context, AgentSwitchFailureReceiptResolution) (int64, error)
	AgentSwitchFailureBacklog(context.Context, time.Time) (AgentSwitchFailureBacklog, error)
}
