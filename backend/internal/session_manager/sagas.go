package sessionmanager

// Saga ownership map and narrow capability interfaces (Phase 3 of the
// session_manager split).
//
// The Manager facade still implements every saga directly, but each saga now
// has a named owner: the files, state group, and capability interface listed
// below. New code must go in the owner's files and depend only on the narrow
// interface, never the whole Manager. service/session's commander will narrow
// to these interfaces one saga at a time.
//
// Ownership:
//   - Spawn saga: manager.go Spawn + chat_spawn.go + prompt.go +
//     chat_attachments.go (StageAttachments). State: core Manager fields.
//     Interface: Spawner.
//   - Terminate saga: Kill, RetireForReplacement, RollbackSpawn, Cleanup,
//     Restore/Resume/Exit paths in manager.go. Interface: Terminator.
//   - Switch saga: agent_switching.go + agent_switching_chat.go +
//     agent_switch_faults.go + handoff_artifact.go +
//     source_semantic_handoff.go. State: switchExecutionState (state.go);
//     shared policy: switchengine.Outcome. Interface: SwitchEngine.
//   - Transition saga: interface_transition.go. State:
//     transitionExecutionState (state.go). Interface: TransitionCoordinator.
//   - Codex-accounts saga: codex_account_switch.go + codex_operation_gate.go.
//     Interface: CodexAccounts.
//   - Messaging saga: session_input.go + message_delivery.go + Send paths.
//     State: sagaOperationState (state.go). Interface: MessengerFacade.
//
// The switchengine subpackage holds the pure switch-settlement policy both
// executors share; it must stay dependency-free (domain + stdlib only) so the
// executors and their tests can consume it without importing the saga.

import (
	"context"
	"encoding/json"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// Spawner owns session creation: Spawn, Restore/Resume paths, RollbackSpawn,
// and attachment staging.
type Spawner interface {
	Spawn(ctx context.Context, cfg ports.SpawnConfig) (domain.SessionRecord, int, int, error)
	RestoreWithMode(ctx context.Context, id domain.SessionID) (RestoreResult, error)
	ResumeAgentWithMode(ctx context.Context, id domain.SessionID) (RestoreResult, error)
	RollbackSpawn(ctx context.Context, id domain.SessionID) (deleted, killed bool, err error)
	StageAttachments(ctx context.Context, id domain.SessionID, attachments []ports.SpawnAttachment) ([]string, error)
}

// Terminator owns session teardown: Kill, RetireForReplacement, Cleanup.
type Terminator interface {
	Kill(ctx context.Context, id domain.SessionID) (bool, error)
	RetireForReplacement(ctx context.Context, id domain.SessionID) error
	Cleanup(ctx context.Context, project domain.ProjectID) (CleanupResult, error)
	ExitAgent(ctx context.Context, id domain.SessionID) (domain.SessionRecord, error)
}

// SwitchEngine owns provider replacement: SwitchAgent and its recovery and
// handoff surfaces.
type SwitchEngine interface {
	SwitchAgent(ctx context.Context, id domain.SessionID, cfg SwitchAgentConfig) (domain.AgentSwitch, error)
	RecoverAgentSwitch(ctx context.Context, id domain.SessionID, switchID domain.AgentSwitchID) (domain.AgentSwitch, error)
	ListAgentSwitches(ctx context.Context, id domain.SessionID) ([]domain.AgentSwitch, error)
	SubmitAgentHandoff(ctx context.Context, id domain.SessionID, switchID domain.AgentSwitchID, sourceGenerationID domain.AgentGenerationID, raw json.RawMessage) (domain.AgentSwitch, error)
	WaitAgentSwitchWorkers(ctx context.Context) error
}

// TransitionCoordinator owns TUI↔Chat handoffs and their durable outbox.
type TransitionCoordinator interface {
	InterfaceTransitionStatus(ctx context.Context, id domain.SessionID) (InterfaceTransitionStatus, error)
	StartInterfaceTransition(ctx context.Context, id domain.SessionID, target domain.SessionMode, policy domain.SessionInterfaceTransitionPolicy) (domain.SessionInterfaceTransition, error)
	CancelInterfaceTransition(ctx context.Context, id domain.SessionID) error
	AcknowledgeInterfaceTransitionNotice(ctx context.Context, id domain.SessionID, transitionID string) (domain.SessionInterfaceTransition, error)
}

// CodexAccounts owns the device-global Codex account switch saga.
type CodexAccounts interface {
	StartCodexAccountSwitch(ctx context.Context, cfg ports.CodexAccountSwitchConfig) (domain.CodexAccountSwitch, error)
	RecoverCodexAccountSwitch(ctx context.Context, id string) (domain.CodexAccountSwitch, error)
	GetActiveCodexAccountSwitch(ctx context.Context) (domain.CodexAccountSwitch, bool, error)
	CodexAccountSwitchInProgress() bool
}

// MessengerFacade owns message delivery and input-lease gating.
type MessengerFacade interface {
	Send(ctx context.Context, id domain.SessionID, message string, attachment *ports.SpawnAttachment) error
	WaitForMessageDeliveryReady(ctx context.Context, id domain.SessionID) error
	AcquireSessionInput(id domain.SessionID) (release func(), ok bool)
	SessionMutationInProgress(id domain.SessionID) bool
}

// Compile guards: the Manager facade must satisfy every saga interface so
// callers can narrow to capabilities without touching the saga bodies.
var (
	_ Spawner               = (*Manager)(nil)
	_ Terminator            = (*Manager)(nil)
	_ SwitchEngine          = (*Manager)(nil)
	_ TransitionCoordinator = (*Manager)(nil)
	_ CodexAccounts         = (*Manager)(nil)
	_ MessengerFacade       = (*Manager)(nil)
)
