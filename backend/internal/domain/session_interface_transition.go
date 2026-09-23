package domain

import (
	"time"

	"github.com/aoagents/agent-orchestrator/backend/pkg/interfacehandoff"
)

// SessionInterfaceTransitionPolicy decides what AO does with work already in
// flight when moving a live session between its terminal and Chat controllers.
type SessionInterfaceTransitionPolicy = interfacehandoff.Policy

const (
	// SessionInterfaceTransitionDrain waits for in-flight source work to finish.
	SessionInterfaceTransitionDrain = interfacehandoff.PolicyDrain
	// SessionInterfaceTransitionInterrupt stops in-flight source work immediately.
	SessionInterfaceTransitionInterrupt = interfacehandoff.PolicyInterrupt
)

// SessionInterfaceTransitionHistoryPolicy scopes an explicit recovery choice.
// Provider history may replace only legacy/untrusted hook text; it never waives
// a trusted current-turn checkpoint, AO's projected high-water mark, or native
// conversation identity.
type SessionInterfaceTransitionHistoryPolicy string

// Session interface-transition history policies.
const (
	SessionInterfaceTransitionHistoryStrict   SessionInterfaceTransitionHistoryPolicy = "strict"
	SessionInterfaceTransitionHistoryProvider SessionInterfaceTransitionHistoryPolicy = "provider_history"
)

// Valid reports whether the history policy is supported by the transition
// coordinator.
func (p SessionInterfaceTransitionHistoryPolicy) Valid() bool {
	return p == SessionInterfaceTransitionHistoryStrict || p == SessionInterfaceTransitionHistoryProvider
}

// SessionInterfaceTransitionPhase is the durable checkpoint of one controller
// handoff. Its shared state table lives in pkg/interfacehandoff so local and
// Cloud adapters accept the same durable edges and terminal semantics.
type SessionInterfaceTransitionPhase = interfacehandoff.Phase

const (
	// SessionInterfaceTransitionRequested is the checkpoint before work starts.
	SessionInterfaceTransitionRequested = interfacehandoff.PhaseRequested
	// SessionInterfaceTransitionPreflighting validates the controllers.
	SessionInterfaceTransitionPreflighting = interfacehandoff.PhasePreflighting
	// SessionInterfaceTransitionDraining waits for source work to quiesce.
	SessionInterfaceTransitionDraining = interfacehandoff.PhaseDraining
	// SessionInterfaceTransitionSourceStopping records source teardown.
	SessionInterfaceTransitionSourceStopping = interfacehandoff.PhaseSourceStopping
	// SessionInterfaceTransitionSourceStopped records completed source teardown.
	SessionInterfaceTransitionSourceStopped = interfacehandoff.PhaseSourceStopped
	// SessionInterfaceTransitionTargetStarting records target startup.
	SessionInterfaceTransitionTargetStarting = interfacehandoff.PhaseTargetStarting
	// SessionInterfaceTransitionActivating records target activation.
	SessionInterfaceTransitionActivating = interfacehandoff.PhaseActivating
	// SessionInterfaceTransitionCompleted records successful activation.
	SessionInterfaceTransitionCompleted = interfacehandoff.PhaseCompleted
	// SessionInterfaceTransitionFailed records a safely restored failure.
	SessionInterfaceTransitionFailed = interfacehandoff.PhaseFailed
	// SessionInterfaceTransitionCancelled records an early cancellation.
	SessionInterfaceTransitionCancelled = interfacehandoff.PhaseCancelled
	// SessionInterfaceTransitionRecovery records required adapter recovery.
	SessionInterfaceTransitionRecovery = interfacehandoff.PhaseRecovery
)

// SessionInterfaceTransition is the durable controller-handoff record. The
// session row remains the authority for the currently committed mode; this row
// explains an in-progress gap where the old controller has stopped and the new
// one is not ready yet.
type SessionInterfaceTransition struct {
	ID                   string                                  `json:"id"`
	SessionID            SessionID                               `json:"sessionId"`
	SourceMode           SessionMode                             `json:"sourceMode" enum:"chat,tui"`
	TargetMode           SessionMode                             `json:"targetMode" enum:"chat,tui"`
	Policy               SessionInterfaceTransitionPolicy        `json:"policy" enum:"drain,interrupt"`
	HistoryPolicy        SessionInterfaceTransitionHistoryPolicy `json:"historyPolicy" enum:"strict,provider_history"`
	Phase                SessionInterfaceTransitionPhase         `json:"phase" enum:"requested,preflighting,draining,source_stopping,source_stopped,target_starting,activating,completed,failed,cancelled,recovery_required"`
	NativeConversationID string                                  `json:"nativeConversationId,omitempty"`
	ErrorCode            string                                  `json:"errorCode,omitempty"`
	ErrorDetail          string                                  `json:"errorDetail,omitempty"`
	CreatedAt            time.Time                               `json:"createdAt"`
	UpdatedAt            time.Time                               `json:"updatedAt"`
	CompletedAt          time.Time                               `json:"completedAt,omitempty"`
	NoticeAcknowledgedAt time.Time                               `json:"noticeAcknowledgedAt,omitempty"`
}

// Active reports whether this row still owns the session's handoff gate.
func (t SessionInterfaceTransition) Active() bool { return !t.Phase.Terminal() }

// SessionInterfaceTransitionMessage is an automation/lifecycle message held
// while neither controller is allowed to accept work.
type SessionInterfaceTransitionMessage struct {
	ID              int64     `json:"id"`
	TransitionID    string    `json:"transitionId"`
	ClientMessageID string    `json:"clientMessageId"`
	Message         string    `json:"message"`
	CreatedAt       time.Time `json:"createdAt"`
	DeliveredAt     time.Time `json:"deliveredAt,omitempty"`
}
