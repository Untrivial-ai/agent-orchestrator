package domain

import (
	"time"

	"github.com/aoagents/agent-orchestrator/backend/pkg/interfacehandoff"
)

// SessionInterfaceTransitionPolicy decides what AO does with work already in
// flight when moving a live session between its terminal and Chat controllers.
type SessionInterfaceTransitionPolicy = interfacehandoff.Policy

const (
	SessionInterfaceTransitionDrain     = interfacehandoff.PolicyDrain
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
	SessionInterfaceTransitionRequested      = interfacehandoff.PhaseRequested
	SessionInterfaceTransitionPreflighting   = interfacehandoff.PhasePreflighting
	SessionInterfaceTransitionDraining       = interfacehandoff.PhaseDraining
	SessionInterfaceTransitionSourceStopping = interfacehandoff.PhaseSourceStopping
	SessionInterfaceTransitionSourceStopped  = interfacehandoff.PhaseSourceStopped
	SessionInterfaceTransitionTargetStarting = interfacehandoff.PhaseTargetStarting
	SessionInterfaceTransitionActivating     = interfacehandoff.PhaseActivating
	SessionInterfaceTransitionCompleted      = interfacehandoff.PhaseCompleted
	SessionInterfaceTransitionFailed         = interfacehandoff.PhaseFailed
	SessionInterfaceTransitionCancelled      = interfacehandoff.PhaseCancelled
	SessionInterfaceTransitionRecovery       = interfacehandoff.PhaseRecovery
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
