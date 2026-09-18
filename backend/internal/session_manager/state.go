package sessionmanager

// Saga state groups for Manager.
//
// The Manager struct historically carried ~30 saga-execution fields flat
// (operation gates, switch budgets, transition outbox). They are grouped here
// so each saga owns its state and New() delegates to small constructors.
// The groups are embedded in Manager, so existing m.<field> selectors keep
// compiling via promotion — this is a pure code-motion refactor with no
// behavior change.
//
// Ownership:
//   - sagaOperationState: input leases + exclusive-operation gates
//     (session_input.go; shared agentOpMu guards both maps).
//   - switchExecutionState: agent-switch worker budgets + background workers
//     (agent_switching.go, agent_switching_chat.go, codex_account_switch.go).
//   - transitionExecutionState: interface-transition runs + durable outbox
//     (interface_transition.go) and post-send confirmation (manager.go).

import (
	"context"
	"sync"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// sagaOperationState owns input admission and exclusive-operation fencing.
// agentOpMu guards every map below; callers must hold it for both the gate
// check and the lease/drain bookkeeping so a pane write is either fully
// admitted before an operation or rejected after it.
type sagaOperationState struct {
	agentOpMu       sync.Mutex
	agentOperations map[domain.SessionID]agentOperationKind
	// switchDecisionInput opens a narrow human-only terminal lane while the
	// source is blocked on permission during a mandatory switch.
	switchDecisionInput map[domain.SessionID]domain.AgentSwitchID
	// retainedSwitches marks switch gates intentionally kept closed after an
	// ambiguous external side effect (for example a target runtime that could
	// not be removed). A later reconciliation pass may reclaim exactly these
	// gates; an actively-running switch remains non-reentrant.
	retainedSwitches map[domain.SessionID]struct{}
	inputLeases      map[domain.SessionID]int
	inputDrained     map[domain.SessionID]chan struct{}
}

func newSagaOperationState() sagaOperationState {
	return sagaOperationState{
		agentOperations:     make(map[domain.SessionID]agentOperationKind),
		switchDecisionInput: make(map[domain.SessionID]domain.AgentSwitchID),
		retainedSwitches:    make(map[domain.SessionID]struct{}),
		inputLeases:         make(map[domain.SessionID]int),
		inputDrained:        make(map[domain.SessionID]chan struct{}),
	}
}

// switchExecutionState owns agent-switch worker budgets and background
// execution. Durations are tuned per saga phase; tests shrink them directly.
type switchExecutionState struct {
	// handoffWait bounds optional source-agent enrichment. Time spent waiting
	// for a human permission decision is paused and charged only against the
	// separate switchPermissionDecisionWait budget below.
	handoffWait time.Duration
	// switchPermissionDecisionWait is a separate human-response budget used only
	// while the source agent is blocked on a permission prompt. The semantic
	// handoff budget is paused while this budget is active.
	switchPermissionDecisionWait time.Duration
	// switchTargetStartWait bounds proof that the newly-created supervised
	// provider generation is actually alive before durable ownership transfers.
	switchTargetStartWait time.Duration
	// switchPostStopWait bounds aggregate target setup after source ownership is
	// conclusively stopped. Tests shorten it to exercise phase-budget isolation.
	switchPostStopWait time.Duration
	// switchDeliveryAckWait bounds the target generation's prompt-submit hook.
	// Timeout is an explicit failed/ambiguous delivery, never implicit success.
	switchDeliveryAckWait time.Duration
	// backgroundContext owns asynchronous agent-switch execution independently
	// of the admitting request. The daemon cancels it before waiting for workers.
	backgroundContext        context.Context
	agentSwitchWorkers       sync.WaitGroup
	agentSwitchWorkerMu      sync.Mutex
	agentSwitchWorkersClosed bool
}

func newSwitchExecutionState(backgroundContext context.Context) switchExecutionState {
	if backgroundContext == nil {
		backgroundContext = context.Background()
	}
	return switchExecutionState{
		handoffWait:                  90 * time.Second,
		switchPermissionDecisionWait: time.Minute,
		switchTargetStartWait:        3 * time.Second,
		switchPostStopWait:           switchPostStopWait,
		// Provider startup, including slow MCP initialization, can delay the
		// prompt-submit hook even though the continuation is correctly buffered.
		// Leave enough headroom to avoid a false delivery failure.
		switchDeliveryAckWait: 150 * time.Second,
		backgroundContext:     backgroundContext,
	}
}

// transitionExecutionState owns interface-transition runs, the durable
// transition-message outbox driver, and post-send confirmation bounds.
type transitionExecutionState struct {
	transitionMu sync.Mutex
	transitions  map[domain.SessionID]*interfaceTransitionRun
	// transitionDeliveryWake drives the durable transition-message outbox. A
	// daemon-lifetime worker is started by Reconcile; terminal transition paths
	// also make one immediate delivery attempt so tests and in-process callers do
	// not depend on the boot worker.
	transitionDeliveryMu        sync.Mutex
	transitionDeliveryRunning   bool
	transitionDeliveryWake      chan struct{}
	transitionDeliveryAttemptMu sync.Mutex
	// sendConfirm bounds the best-effort post-send confirmation that the session
	// actually became active (the agent accepted the prompt). New fills in the
	// sendConfirm* defaults; tests in this package shrink the timings directly.
	sendConfirm sendConfirmConfig
	// interfaceTransition bounds only contradictory stale-idle proof. Turns and
	// user-paced waits reported through the activity boundary remain unbounded.
	interfaceTransition interfaceTransitionConfig
}

func newTransitionExecutionState() transitionExecutionState {
	return transitionExecutionState{
		transitions:            make(map[domain.SessionID]*interfaceTransitionRun),
		transitionDeliveryWake: make(chan struct{}, 1),
		sendConfirm: sendConfirmConfig{
			pollInterval:    sendConfirmPollInterval,
			attemptDeadline: sendConfirmAttemptDeadline,
			maxAttempts:     sendConfirmMaxAttempts,
		},
		interfaceTransition: interfaceTransitionConfig{
			pollInterval:   interfaceTransitionPoll,
			idleSettle:     interfaceTransitionIdleSettle,
			staleIdleLimit: interfaceTransitionStaleIdleLimit,
		},
	}
}
