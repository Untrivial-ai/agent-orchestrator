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
	FinalizeAgentSwitchHandoff(ctx context.Context, id domain.AgentSwitchID, sessionID domain.SessionID, sourceGenerationID, targetGenerationID domain.AgentGenerationID, handoffPath, handoffHash string, semanticIncluded bool, updatedAt time.Time) (bool, error)
	ConfirmAgentSwitchSourceStopped(ctx context.Context, confirmation domain.AgentSwitchSourceStopConfirmation) (bool, error)
	AcknowledgeAgentSwitchTarget(ctx context.Context, id domain.AgentSwitchID, sessionID domain.SessionID, targetGenerationID domain.AgentGenerationID, acknowledgedAt time.Time) (bool, error)
	ActivateAgentSwitchTarget(ctx context.Context, activation domain.AgentSwitchTargetActivation) (bool, error)
}
