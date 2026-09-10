package domain

import "time"

// SessionInterfaceTransitionPolicy decides what AO does with work already in
// flight when moving a live session between its terminal and Chat controllers.
type SessionInterfaceTransitionPolicy string

const (
	// SessionInterfaceTransitionDrain stops accepting new source-controller work
	// and waits for the current turn (and already queued Chat turns) to finish.
	SessionInterfaceTransitionDrain SessionInterfaceTransitionPolicy = "drain"
	// SessionInterfaceTransitionInterrupt explicitly cancels the current turn and
	// queued Chat turns before the source controller is stopped.
	SessionInterfaceTransitionInterrupt SessionInterfaceTransitionPolicy = "interrupt"
)

// Valid reports whether the policy is one the transition coordinator knows.
func (p SessionInterfaceTransitionPolicy) Valid() bool {
	return p == SessionInterfaceTransitionDrain || p == SessionInterfaceTransitionInterrupt
}

// SessionInterfaceTransitionPhase is the durable checkpoint of one controller
// handoff. External process work cannot share a SQLite transaction, so these
// phases make the operation recoverable and visible to every client.
type SessionInterfaceTransitionPhase string

const (
	// SessionInterfaceTransitionRequested is the durable row before work starts.
	SessionInterfaceTransitionRequested SessionInterfaceTransitionPhase = "requested"
	// SessionInterfaceTransitionPreflighting validates source and target controllers.
	SessionInterfaceTransitionPreflighting SessionInterfaceTransitionPhase = "preflighting"
	// SessionInterfaceTransitionDraining waits for source-side in-flight work.
	SessionInterfaceTransitionDraining SessionInterfaceTransitionPhase = "draining"
	// SessionInterfaceTransitionSourceStopping records that the source controller is stopping.
	SessionInterfaceTransitionSourceStopping SessionInterfaceTransitionPhase = "source_stopping"
	// SessionInterfaceTransitionSourceStopped records that the source controller stopped.
	SessionInterfaceTransitionSourceStopped SessionInterfaceTransitionPhase = "source_stopped"
	// SessionInterfaceTransitionTargetStarting records that the target controller is starting.
	SessionInterfaceTransitionTargetStarting SessionInterfaceTransitionPhase = "target_starting"
	// SessionInterfaceTransitionActivating commits the target controller as active.
	SessionInterfaceTransitionActivating SessionInterfaceTransitionPhase = "activating"
	// SessionInterfaceTransitionCompleted is a successful terminal transition state.
	SessionInterfaceTransitionCompleted SessionInterfaceTransitionPhase = "completed"
	// SessionInterfaceTransitionFailed is a failed terminal transition state.
	SessionInterfaceTransitionFailed SessionInterfaceTransitionPhase = "failed"
	// SessionInterfaceTransitionCancelled is a user-cancelled terminal transition state.
	SessionInterfaceTransitionCancelled SessionInterfaceTransitionPhase = "cancelled"
	// SessionInterfaceTransitionRecovery marks a transition that either needs
	// reconciliation or was closed during daemon-start reconciliation. ErrorCode
	// distinguishes those outcomes; the terminal row remains durable diagnostics.
	SessionInterfaceTransitionRecovery SessionInterfaceTransitionPhase = "recovery_required"
)

// Terminal reports whether no transition worker may continue this row.
func (p SessionInterfaceTransitionPhase) Terminal() bool {
	switch p {
	case SessionInterfaceTransitionCompleted,
		SessionInterfaceTransitionFailed,
		SessionInterfaceTransitionCancelled,
		SessionInterfaceTransitionRecovery:
		return true
	default:
		return false
	}
}

// SessionInterfaceTransition is the durable controller-handoff record. The
// session row remains the authority for the currently committed mode; this row
// explains an in-progress gap where the old controller has stopped and the new
// one is not ready yet.
type SessionInterfaceTransition struct {
	ID                   string                           `json:"id"`
	SessionID            SessionID                        `json:"sessionId"`
	SourceMode           SessionMode                      `json:"sourceMode" enum:"chat,tui"`
	TargetMode           SessionMode                      `json:"targetMode" enum:"chat,tui"`
	Policy               SessionInterfaceTransitionPolicy `json:"policy" enum:"drain,interrupt"`
	Phase                SessionInterfaceTransitionPhase  `json:"phase" enum:"requested,preflighting,draining,source_stopping,source_stopped,target_starting,activating,completed,failed,cancelled,recovery_required"`
	NativeConversationID string                           `json:"nativeConversationId,omitempty"`
	ErrorCode            string                           `json:"errorCode,omitempty"`
	ErrorDetail          string                           `json:"errorDetail,omitempty"`
	CreatedAt            time.Time                        `json:"createdAt"`
	UpdatedAt            time.Time                        `json:"updatedAt"`
	CompletedAt          time.Time                        `json:"completedAt,omitempty"`
	NoticeAcknowledgedAt time.Time                        `json:"noticeAcknowledgedAt,omitempty"`
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
