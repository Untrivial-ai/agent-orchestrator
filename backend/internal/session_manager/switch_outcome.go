package sessionmanager

// Shared agent-switch saga helpers (Phase 2 of the session_manager split).
//
// executeAgentSwitch (agent_switching.go) and executeChatAgentSwitch
// (agent_switching_chat.go) run the same phases — admit → stop-source →
// start-target → deliver → settle — with mode-specific store calls. This file
// holds the pieces already unified without behavior change:
//
//   - markTargetStartAmbiguous collapses the identical ambiguous-target marker
//     blocks (durable context + mark + fold into result).
//   - switchOutcomePolicy adapts executor locals to the shared settlement
//     policy in switchengine.
//
// Settlement branching (rollback-safe, workspace cleanup, retained marker vs
// terminal failure) is pure policy in switchengine.Outcome; both executors'
// deferred cleanup converges on it through the settlement() closures. The
// post-stop budget likewise resolves through
// switchengine.ResolvePostStopWait at both call sites. A full executor merge
// behind one target-starter remains future work: the TUI and Chat bodies
// still differ in store calls, fencing types, and delivery paths.

import (
	"context"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/internal/session_manager/switchengine"
)

// markTargetStartAmbiguous persists the target-start-unconfirmed marker and
// folds it into result. On marker failure the original result is returned
// alongside the error so callers can join it into their return, exactly as the
// inlined blocks did.
func (m *Manager) markTargetStartAmbiguous(ctx context.Context, store ports.AgentSwitchStore, result domain.AgentSwitch, recorder agentSwitchFlightRecorder) (domain.AgentSwitch, error) {
	markCtx, cancelMark := switchDurableContext(ctx)
	defer cancelMark()
	marked, markErr := m.markTargetStartUnconfirmedWithRecorder(markCtx, store, result, recorder)
	if markErr != nil {
		return result, markErr
	}
	return marked, nil
}

// switchOutcomePolicy adapts executor locals to the shared settlement policy.
// It keeps the defer-block conditions expressed once in switchengine.
func switchOutcomePolicy(failed, sourceStopped, ownerCommitted, targetAmbiguous, workspacePrepared, stateTerminal, requiresRecovery, skipTerminalization bool) switchengine.Outcome {
	return switchengine.Outcome{
		Failed:              failed,
		SourceStopped:       sourceStopped,
		OwnerCommitted:      ownerCommitted,
		TargetAmbiguous:     targetAmbiguous,
		WorkspacePrepared:   workspacePrepared,
		StateTerminal:       stateTerminal,
		RequiresRecovery:    requiresRecovery,
		SkipTerminalization: skipTerminalization,
	}
}
