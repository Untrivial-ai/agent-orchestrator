package domain

import "time"

// SessionInterface is the conversation controller currently committed for a
// session. Cloud sessions run a single controller at a time, and the interface
// can change only through the durable interface-transition coordinator.
//
//   - SessionInterfaceTUI: the provider's native interactive TUI inside the
//     sandbox agent PTY. This is the historical behavior and the default.
//   - SessionInterfaceChat: a durable, structured, event-projected headless
//     controller; the provider is invoked per-turn with headless flags.
type SessionInterface string

const (
	SessionInterfaceTUI  SessionInterface = "tui"
	SessionInterfaceChat SessionInterface = "chat"
)

func (i SessionInterface) Valid() bool {
	switch i {
	case SessionInterfaceTUI, SessionInterfaceChat:
		return true
	default:
		return false
	}
}

func (i SessionInterface) Normalized() SessionInterface {
	if i.Valid() {
		return i
	}
	return SessionInterfaceTUI
}

func (i SessionInterface) Opposite() SessionInterface {
	if i.Normalized() == SessionInterfaceChat {
		return SessionInterfaceTUI
	}
	return SessionInterfaceChat
}

// SessionInterfaceTransitionPolicy decides what AO does with work already in
// flight when moving a live session between its terminal and Chat controllers.
type SessionInterfaceTransitionPolicy string

const (
	SessionInterfaceTransitionDrain     SessionInterfaceTransitionPolicy = "drain"
	SessionInterfaceTransitionInterrupt SessionInterfaceTransitionPolicy = "interrupt"
)

func (p SessionInterfaceTransitionPolicy) Valid() bool {
	return p == SessionInterfaceTransitionDrain || p == SessionInterfaceTransitionInterrupt
}

// SessionInterfaceTransitionPhase is the durable checkpoint of one controller
// handoff. External process work cannot share a Postgres transaction, so these
// phases make the operation recoverable and visible to every client against a
// multi-replica, stateless control plane.
type SessionInterfaceTransitionPhase string

const (
	SessionInterfaceTransitionRequested      SessionInterfaceTransitionPhase = "requested"
	SessionInterfaceTransitionPreflighting   SessionInterfaceTransitionPhase = "preflighting"
	SessionInterfaceTransitionDraining       SessionInterfaceTransitionPhase = "draining"
	SessionInterfaceTransitionSourceStopping SessionInterfaceTransitionPhase = "source_stopping"
	SessionInterfaceTransitionSourceStopped  SessionInterfaceTransitionPhase = "source_stopped"
	SessionInterfaceTransitionTargetStarting SessionInterfaceTransitionPhase = "target_starting"
	SessionInterfaceTransitionActivating     SessionInterfaceTransitionPhase = "activating"
	SessionInterfaceTransitionCompleted      SessionInterfaceTransitionPhase = "completed"
	SessionInterfaceTransitionFailed         SessionInterfaceTransitionPhase = "failed"
	SessionInterfaceTransitionCancelled      SessionInterfaceTransitionPhase = "cancelled"
	SessionInterfaceTransitionRecovery       SessionInterfaceTransitionPhase = "recovery_required"
)

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

func (p SessionInterfaceTransitionPhase) Active() bool { return !p.Terminal() }

// SessionInterfaceTransition is the durable controller-handoff record. The
// session row remains the authority for the currently committed interface; this
// row explains an in-progress gap where the old controller has stopped and the
// new one is not ready yet.
type SessionInterfaceTransition struct {
	ID                   string
	OrgID                string
	SessionID            string
	SourceInterface      SessionInterface
	TargetInterface      SessionInterface
	Policy               SessionInterfaceTransitionPolicy
	Phase                SessionInterfaceTransitionPhase
	NativeConversationID string
	ErrorCode            string
	ErrorDetail          string
	NoticeAcknowledgedAt *time.Time
	CreatedAt            time.Time
	UpdatedAt            time.Time
	CompletedAt          *time.Time
}

// SessionInterfaceTransitionMessage is an automation/lifecycle message held
// while neither controller is allowed to accept work.
type SessionInterfaceTransitionMessage struct {
	ID              int64
	TransitionID    string
	ClientMessageID string
	Message         string
	CreatedAt       time.Time
	DeliveredAt     *time.Time
}
