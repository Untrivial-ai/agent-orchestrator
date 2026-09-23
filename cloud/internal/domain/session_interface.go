package domain

import (
	"time"

	"github.com/aoagents/agent-orchestrator/backend/pkg/interfacehandoff"
)

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
type SessionInterfaceTransitionPolicy = interfacehandoff.Policy

const (
	SessionInterfaceTransitionDrain     = interfacehandoff.PolicyDrain
	SessionInterfaceTransitionInterrupt = interfacehandoff.PolicyInterrupt
)

// SessionInterfaceTransitionPhase is the durable checkpoint of one controller
// handoff. Its shared state table lives in backend/pkg/interfacehandoff so the
// Cloud and local adapters use the same durable edges and terminal semantics.
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
