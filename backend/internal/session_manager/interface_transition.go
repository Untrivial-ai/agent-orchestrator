package sessionmanager

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const (
	interfaceTransitionPoll               = 150 * time.Millisecond
	interfaceTransitionIdleSettle         = 750 * time.Millisecond
	interfaceTransitionSurfaceIdleSamples = 3
	interfaceTransitionStaleIdleLimit     = 5 * time.Second
	interfaceTransitionOutputLines        = 40
	interfaceInterruptSettle              = 2 * time.Second
	interfaceTransitionStopLimit          = 15 * time.Second
	interfaceTransitionStepLimit          = 45 * time.Second
	interfaceDeliveryRetry                = 2 * time.Second
	interfaceDeliveryIdlePoll             = 30 * time.Second
)

var (
	errDrainQuiescenceUnverified = errors.New("AO could not verify that the terminal was idle after the latest input. The source interface was left untouched; retry after the terminal settles")
	errDrainDraftPresent         = errors.New("AO found unsent text in the terminal composer. The source interface was left untouched; submit or clear the draft and retry, or choose Discard draft and switch")
	errDrainDecisionPending      = errors.New("AO found a provider decision waiting in Terminal. The source interface was left untouched; answer it in Terminal and retry, or choose Cancel request and switch")
)

// interfaceTransitionStore is optional so the existing narrow Manager Store
// port and its many focused fakes do not grow methods unrelated to their tests.
// Production's SQLite store implements the full capability.
type interfaceTransitionStore interface {
	CreateSessionInterfaceTransition(context.Context, domain.SessionInterfaceTransition) (domain.SessionInterfaceTransition, bool, error)
	GetSessionInterfaceTransition(context.Context, string) (domain.SessionInterfaceTransition, bool, error)
	GetActiveSessionInterfaceTransition(context.Context, domain.SessionID) (domain.SessionInterfaceTransition, bool, error)
	GetLatestSessionInterfaceTransition(context.Context, domain.SessionID) (domain.SessionInterfaceTransition, bool, error)
	ListActiveSessionInterfaceTransitions(context.Context) ([]domain.SessionInterfaceTransition, error)
	ListDeliverableSessionInterfaceTransitions(context.Context) ([]domain.SessionInterfaceTransition, error)
	AdvanceSessionInterfaceTransition(context.Context, string, domain.SessionInterfaceTransitionPhase, domain.SessionInterfaceTransitionPhase, string, string, string, time.Time) (bool, error)
	AcknowledgeSessionInterfaceTransitionNotice(context.Context, domain.SessionID, string, time.Time) (domain.SessionInterfaceTransition, bool, error)
	EnqueueSessionInterfaceTransitionMessage(context.Context, string, string, string, time.Time) error
	ListPendingSessionInterfaceTransitionMessages(context.Context, string) ([]domain.SessionInterfaceTransitionMessage, error)
	MarkSessionInterfaceTransitionMessageDelivered(context.Context, int64, time.Time) error
}

type chatHandoffLauncher interface {
	ArmChatHandoff(context.Context, domain.SessionID, domain.SessionInterfaceTransitionPolicy) error
	PrepareChatHandoff(context.Context, domain.SessionID, domain.SessionInterfaceTransitionPolicy) error
	AbortChatHandoff(domain.SessionID)
}

type runtimeInterrupter interface {
	Interrupt(context.Context, ports.RuntimeHandle) error
}

type interfaceTransitionRun struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// InterfaceTransitionStatus is the controller-facing view used by desktop and
// mobile to decide whether to draw the switch and to render any durable attempt.
type InterfaceTransitionStatus struct {
	Supported  bool
	TargetMode domain.SessionMode
	ReasonCode string
	Reason     string
	Transition *domain.SessionInterfaceTransition
}

// InterfaceTransitionStatus reports static adapter support plus the latest
// durable attempt. Read-only: target binary/auth checks happen on POST so a
// status render never launches a provider process.
func (m *Manager) InterfaceTransitionStatus(
	ctx context.Context,
	id domain.SessionID,
) (InterfaceTransitionStatus, error) {
	rec, ok, err := m.store.GetSession(ctx, id)
	if err != nil {
		return InterfaceTransitionStatus{}, fmt.Errorf("interface transition status %s: %w", id, err)
	}
	if !ok {
		return InterfaceTransitionStatus{}, ErrNotFound
	}
	target := oppositeSessionMode(rec.Mode)
	status := InterfaceTransitionStatus{TargetMode: target}
	if rec.IsTerminated {
		status.ReasonCode = "SESSION_TERMINATED"
		status.Reason = "Terminated sessions must be restored before switching interfaces."
	} else if target == domain.SessionModeChat && (m.chat == nil || !m.chat.SupportsChat(rec.Harness)) {
		status.ReasonCode = "CHAT_UNSUPPORTED"
		status.Reason = fmt.Sprintf("%s does not support Chat UI.", rec.Harness)
	} else if _, _, err := m.nativeConversationID(ctx, rec); err != nil {
		if errors.Is(err, ErrInterfaceHandoffUnsupported) {
			status.ReasonCode = "INTERFACE_HANDOFF_UNSUPPORTED"
		} else if errors.Is(err, ErrNativeConversationMissing) {
			status.ReasonCode = "NATIVE_SESSION_MISSING"
		} else if errors.Is(err, ErrNativeConversationUnverified) {
			status.ReasonCode = "NATIVE_SESSION_UNVERIFIED"
		} else {
			return InterfaceTransitionStatus{}, err
		}
		status.Reason = err.Error()
	} else {
		status.Supported = true
	}
	if store, ok := m.store.(interfaceTransitionStore); ok {
		latest, found, err := store.GetLatestSessionInterfaceTransition(ctx, id)
		if err != nil {
			return InterfaceTransitionStatus{}, err
		}
		if found {
			status.Transition = &latest
		}
	}
	return status, nil
}

// StartInterfaceTransition durably claims the session and starts the handoff in
// the background. The caller gets the transition immediately; long-running
// drain mode therefore does not depend on an HTTP request timeout.
func (m *Manager) StartInterfaceTransition(
	ctx context.Context,
	id domain.SessionID,
	target domain.SessionMode,
	policy domain.SessionInterfaceTransitionPolicy,
) (domain.SessionInterfaceTransition, error) {
	if !target.Valid() {
		return domain.SessionInterfaceTransition{}, fmt.Errorf("target mode %q is invalid", target)
	}
	if !policy.Valid() {
		return domain.SessionInterfaceTransition{}, fmt.Errorf("transition policy %q is invalid", policy)
	}
	store, ok := m.store.(interfaceTransitionStore)
	if !ok {
		return domain.SessionInterfaceTransition{}, ErrInterfaceHandoffUnsupported
	}
	rec, found, err := m.store.GetSession(ctx, id)
	if err != nil {
		return domain.SessionInterfaceTransition{}, err
	}
	if !found {
		return domain.SessionInterfaceTransition{}, ErrNotFound
	}
	if rec.IsTerminated {
		return domain.SessionInterfaceTransition{}, ErrTerminated
	}
	source := domain.NormalizeSessionMode(rec.Mode)
	if target == source {
		return domain.SessionInterfaceTransition{}, fmt.Errorf("%w: session %s is already in %s mode",
			ErrInterfaceAlreadySelected, id, source)
	}
	nativeID, handoff, err := m.nativeConversationID(ctx, rec)
	if err != nil {
		return domain.SessionInterfaceTransition{}, err
	}
	nativeID, err = m.persistedNativeConversationID(ctx, rec, nativeID, handoff)
	if err != nil {
		return domain.SessionInterfaceTransition{}, err
	}
	now := m.clock()
	transition, created, err := store.CreateSessionInterfaceTransition(ctx, domain.SessionInterfaceTransition{
		ID: m.newLaunchID(), SessionID: id, SourceMode: source, TargetMode: target,
		Policy: policy, Phase: domain.SessionInterfaceTransitionRequested,
		NativeConversationID: nativeID, CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		return domain.SessionInterfaceTransition{}, err
	}
	if !created {
		return transition, ErrInterfaceTransitionInProgress
	}
	if source == domain.SessionModeChat {
		handoff, ok := m.chat.(chatHandoffLauncher)
		if !ok {
			err := ErrInterfaceHandoffUnsupported
			_ = m.finishInterfaceTransition(transition.ID, domain.SessionInterfaceTransitionFailed,
				"SOURCE_FENCE_FAILED", err.Error())
			return domain.SessionInterfaceTransition{}, err
		}
		// This is the acceptance boundary visible to the caller. The transition
		// must not return while a Chat completion can still promote queued work;
		// target preflight and provider cancellation happen asynchronously later.
		if err := handoff.ArmChatHandoff(ctx, id, policy); err != nil {
			finishErr := m.finishInterfaceTransition(transition.ID,
				domain.SessionInterfaceTransitionFailed, "SOURCE_FENCE_FAILED", err.Error())
			if finishErr != nil {
				return domain.SessionInterfaceTransition{}, errors.Join(err,
					fmt.Errorf("record source fence failure: %w", finishErr))
			}
			return domain.SessionInterfaceTransition{}, err
		}
	}

	runCtx, cancel := context.WithCancel(context.Background())
	run := &interfaceTransitionRun{cancel: cancel, done: make(chan struct{})}
	m.transitionMu.Lock()
	m.transitions[id] = run
	m.transitionMu.Unlock()
	go m.runInterfaceTransition(runCtx, transition, run)
	return transition, nil
}

// CancelInterfaceTransition cancels only while the source controller is still
// intact. Once source_stopping begins, finishing or rolling back is safer than
// reopening a controller whose process state is ambiguous.
func (m *Manager) CancelInterfaceTransition(ctx context.Context, id domain.SessionID) error {
	store, ok := m.store.(interfaceTransitionStore)
	if !ok {
		return ErrInterfaceHandoffUnsupported
	}
	transition, found, err := store.GetActiveSessionInterfaceTransition(ctx, id)
	if err != nil {
		return err
	}
	if !found {
		return ErrInterfaceTransitionNotFound
	}
	switch transition.Phase {
	case domain.SessionInterfaceTransitionRequested,
		domain.SessionInterfaceTransitionPreflighting,
		domain.SessionInterfaceTransitionDraining:
	default:
		return ErrInterfaceTransitionNotCancellable
	}
	// Cancellation must win durably before it signals the in-memory worker. If
	// the worker crossed into source_stopping after our read, this CAS loses and
	// we refuse the cancellation instead of acknowledging it while proceeding to
	// tear down the source controller.
	moved, err := store.AdvanceSessionInterfaceTransition(
		ctx,
		transition.ID,
		transition.Phase,
		domain.SessionInterfaceTransitionCancelled,
		transition.NativeConversationID,
		"TRANSITION_CANCELLED",
		"The interface switch was cancelled.",
		m.clock(),
	)
	if err != nil {
		return err
	}
	if !moved {
		return ErrInterfaceTransitionNotCancellable
	}
	m.transitionMu.Lock()
	run := m.transitions[id]
	m.transitionMu.Unlock()
	if run != nil {
		run.cancel()
	}
	m.wakeTransitionMessageDispatcher()
	return nil
}

// AcknowledgeInterfaceTransitionNotice records that a failed/recovered handoff
// notice has been seen without deleting the transition's audit history. Both
// ids are checked so a stale client cannot accidentally dismiss a newer row or
// a transition owned by another session.
func (m *Manager) AcknowledgeInterfaceTransitionNotice(
	ctx context.Context,
	id domain.SessionID,
	transitionID string,
) (domain.SessionInterfaceTransition, error) {
	store, ok := m.store.(interfaceTransitionStore)
	if !ok {
		return domain.SessionInterfaceTransition{}, ErrInterfaceHandoffUnsupported
	}
	transition, found, err := store.GetSessionInterfaceTransition(ctx, transitionID)
	if err != nil {
		return domain.SessionInterfaceTransition{}, err
	}
	if !found || transition.SessionID != id {
		return domain.SessionInterfaceTransition{}, ErrInterfaceTransitionNotFound
	}
	if transition.Phase != domain.SessionInterfaceTransitionFailed &&
		transition.Phase != domain.SessionInterfaceTransitionRecovery {
		return domain.SessionInterfaceTransition{}, ErrInterfaceTransitionNoticeNotAcknowledgeable
	}
	acknowledged, changed, err := store.AcknowledgeSessionInterfaceTransitionNotice(
		ctx, id, transitionID, m.clock(),
	)
	if err != nil {
		return domain.SessionInterfaceTransition{}, err
	}
	if !changed {
		return domain.SessionInterfaceTransition{}, ErrInterfaceTransitionNoticeNotAcknowledgeable
	}
	return acknowledged, nil
}

func (m *Manager) runInterfaceTransition(
	ctx context.Context,
	transition domain.SessionInterfaceTransition,
	run *interfaceTransitionRun,
) {
	defer m.settleTransitionMessages(transition)
	defer close(run.done)
	defer func() {
		m.transitionMu.Lock()
		if current := m.transitions[transition.SessionID]; current == run {
			delete(m.transitions, transition.SessionID)
		}
		m.transitionMu.Unlock()
	}()

	// Chat was armed synchronously before StartInterfaceTransition returned. TUI
	// installs its raw-input gate below because that gate is tied to this worker's
	// lifetime. Any failure before Chat stops must therefore reopen Chat intake.
	sourcePrepared := transition.SourceMode == domain.SessionModeChat
	sourceStopped := false
	modeChanged := false
	var sourceRuntimeHandle ports.RuntimeHandle
	fail := func(code string, cause error) {
		if sourcePrepared && !sourceStopped {
			m.abortSourceHandoff(transition)
		}
		if errors.Is(cause, context.Canceled) && !sourceStopped {
			_ = m.finishInterfaceTransition(transition.ID, domain.SessionInterfaceTransitionCancelled,
				"TRANSITION_CANCELLED", "The interface switch was cancelled.")
			return
		}
		if sourceStopped {
			m.rollbackInterfaceTransition(transition, sourceRuntimeHandle, modeChanged, code, cause)
			return
		}
		_ = m.finishInterfaceTransition(transition.ID, domain.SessionInterfaceTransitionFailed, code, cause.Error())
	}

	if err := m.moveInterfaceTransition(transition.ID, domain.SessionInterfaceTransitionPreflighting, "", ""); err != nil {
		fail("TRANSITION_STATE_FAILED", err)
		return
	}
	rec, ok, err := m.store.GetSession(ctx, transition.SessionID)
	if err != nil || !ok {
		if err == nil {
			err = ErrNotFound
		}
		fail("SESSION_NOT_FOUND", err)
		return
	}
	if rec.IsTerminated || domain.NormalizeSessionMode(rec.Mode) != transition.SourceMode {
		fail("SESSION_CHANGED", fmt.Errorf("session changed before the interface switch could start"))
		return
	}
	sourceRuntimeHandle = runtimeHandle(rec.Metadata)
	// Claim the raw terminal input path before target preflight or the first idle
	// observation. Without this gate a mux client can submit work after the TUI is
	// observed idle but before Destroy, and that accepted work is then killed by
	// the handoff. Chat intake has its equivalent gate in BeginHandoff below.
	lastTerminalInputAt, releaseTerminalInput := m.beginTerminalInputDrain(rec)
	if releaseTerminalInput != nil {
		defer releaseTerminalInput()
	}
	if err := m.preflightInterfaceTarget(ctx, rec, transition); err != nil {
		fail(interfaceTransitionErrorCode(err), err)
		return
	}
	if err := m.moveInterfaceTransition(transition.ID, domain.SessionInterfaceTransitionDraining, "", ""); err != nil {
		fail("TRANSITION_STATE_FAILED", err)
		return
	}
	if err := m.prepareSourceHandoff(ctx, rec, transition.Policy, lastTerminalInputAt); err != nil {
		code := "SOURCE_QUIESCE_FAILED"
		switch {
		case errors.Is(err, errDrainQuiescenceUnverified):
			code = "DRAIN_QUIESCENCE_UNVERIFIED"
		case errors.Is(err, errDrainDraftPresent):
			code = "DRAIN_DRAFT_PRESENT"
		case errors.Is(err, errDrainDecisionPending):
			code = "DRAIN_DECISION_PENDING"
		}
		fail(code, err)
		return
	}
	sourcePrepared = true
	// A promptless TUI may not create a provider conversation until its first
	// submitted turn. When the transition was admitted from positive initial-
	// composer proof, resolve again after input is frozen and drain is complete.
	// This closes the race where a turn starts between the admission snapshot and
	// the terminal input gate: the new native id is transferred, or the switch
	// fails before stopping the source if identity still cannot be proven.
	if transition.SourceMode == domain.SessionModeTUI && transition.NativeConversationID == "" {
		current, found, refreshErr := m.store.GetSession(ctx, rec.ID)
		if refreshErr != nil || !found {
			if refreshErr == nil {
				refreshErr = ErrNotFound
			}
			fail("SESSION_NOT_FOUND", refreshErr)
			return
		}
		if current.Metadata.RuntimeLaunchID != rec.Metadata.RuntimeLaunchID ||
			domain.NormalizeSessionMode(current.Mode) != transition.SourceMode {
			fail("SESSION_CHANGED", fmt.Errorf("session changed while the interface switch was draining"))
			return
		}
		resolvedID, handoff, refreshErr := m.nativeConversationID(ctx, current)
		if refreshErr == nil {
			resolvedID, refreshErr = m.persistedNativeConversationID(ctx, current, resolvedID, handoff)
		}
		if refreshErr != nil {
			fail(interfaceTransitionErrorCode(refreshErr), refreshErr)
			return
		}
		transition.NativeConversationID = resolvedID
	}
	if err := m.moveInterfaceTransitionWithNativeID(
		transition.ID,
		domain.SessionInterfaceTransitionSourceStopping,
		transition.NativeConversationID,
		"",
		"",
	); err != nil {
		fail("TRANSITION_STATE_FAILED", err)
		return
	}
	// Cancellation is deliberately no longer inherited after source_stopping was
	// committed. At this boundary the old process may already have received its
	// stop signal; abandoning the operation could leave its liveness ambiguous.
	// Retry the idempotent stop under fresh bounded contexts. If it still cannot
	// be proven stopped, do not launch either controller: recovery is safer than
	// two writers racing on one native conversation.
	if err := m.stopSourceControllerConclusive(rec); err != nil {
		detail := "AO could not prove that the old controller stopped. Restart AO to reconcile this session before sending more work: " + err.Error()
		_ = m.finishInterfaceTransition(transition.ID, domain.SessionInterfaceTransitionRecovery,
			"SOURCE_STOP_UNCERTAIN", detail)
		return
	}
	sourceStopped = true
	if err := m.moveInterfaceTransition(transition.ID, domain.SessionInterfaceTransitionSourceStopped, "", ""); err != nil {
		fail("TRANSITION_STATE_FAILED", err)
		return
	}
	if m.lcm == nil {
		fail("LIFECYCLE_UNAVAILABLE", fmt.Errorf("lifecycle manager is unavailable"))
		return
	}
	changed, err := m.lcm.CommitControllerEpoch(ctx, rec.ID, transition.SourceMode,
		transition.TargetMode, transition.NativeConversationID, transition.NativeConversationID == "")
	if err != nil || !changed {
		if err == nil {
			err = fmt.Errorf("session mode compare-and-swap failed")
		}
		fail("SESSION_CHANGED", err)
		return
	}
	modeChanged = true
	if err := m.moveInterfaceTransition(transition.ID, domain.SessionInterfaceTransitionTargetStarting, "", ""); err != nil {
		fail("TRANSITION_STATE_FAILED", err)
		return
	}
	if err := m.startTransitionTarget(ctx, rec.ID, transition.NativeConversationID == "", true); err != nil {
		code := "TARGET_RESUME_FAILED"
		switch {
		case errors.Is(err, ports.ErrChatHistoryUnavailable):
			code = "TARGET_HISTORY_UNAVAILABLE"
		case errors.Is(err, ports.ErrChatHistoryUnsettled):
			code = "TARGET_HISTORY_UNSETTLED"
		}
		fail(code, err)
		return
	}
	if err := m.moveInterfaceTransition(transition.ID, domain.SessionInterfaceTransitionActivating, "", ""); err != nil {
		// The target is live. Do not tear it down merely because diagnostics failed
		// to advance; mark recovery so a user is never left with no controller.
		_ = m.finishInterfaceTransition(transition.ID, domain.SessionInterfaceTransitionRecovery,
			"TRANSITION_STATE_FAILED", err.Error())
		return
	}
	if err := m.finishInterfaceTransition(transition.ID, domain.SessionInterfaceTransitionCompleted, "", ""); err != nil {
		m.logger.Error("interface transition completed but durable completion failed",
			"sessionID", rec.ID, "transitionID", transition.ID, "error", err)
		return
	}
}

// nativeConversationID resolves the adapter's native conversation id for the
// session's current interface. An empty id is only safe to pass through when
// the adapter can distinguish a fresh TUI from a real conversation (it
// implements AgentInterfaceHandoffHistoryProbe) AND the session has no
// hook-derived conversation history. If any conversation metadata is set but
// the native id is empty, the hooks fired but failed to capture the id — a
// bug state where fresh-starting would silently discard the conversation, so
// hard-block with ErrNativeConversationMissing instead.
func (m *Manager) nativeConversationID(
	ctx context.Context,
	rec domain.SessionRecord,
) (string, ports.AgentInterfaceHandoff, error) {
	agent, ok := m.agents.Agent(rec.Harness)
	if !ok {
		return "", nil, fmt.Errorf("%w: no adapter for %s", ErrInterfaceHandoffUnsupported, rec.Harness)
	}
	handoff, ok := agent.(ports.AgentInterfaceHandoff)
	if !ok {
		return "", nil, fmt.Errorf("%w: %s has not declared compatible TUI and Chat identities",
			ErrInterfaceHandoffUnsupported, rec.Harness)
	}
	mode := domain.NormalizeSessionMode(rec.Mode)
	ref := ports.SessionRef{
		ID: string(rec.ID), WorkspacePath: rec.Metadata.WorkspacePath,
		Metadata: map[string]string{ports.MetadataKeyAgentSessionID: rec.Metadata.AgentSessionID},
	}
	id, ok, err := handoff.NativeConversationID(ctx, ref, mode, rec.Metadata.ProviderConversationID)
	if err != nil {
		return "", handoff, err
	}
	id = strings.TrimSpace(id)
	if !ok || id == "" {
		if m.nativeConversationNotStarted(ctx, rec, agent) {
			return "", handoff, nil
		}
		return "", handoff, fmt.Errorf("%w for %s", ErrNativeConversationMissing, rec.Harness)
	}
	// AgentSessionID is also the resume hint used to launch a TUI, so it can
	// legitimately outlive the runtime generation that originally reported it.
	// That makes it insufficient as handoff proof by itself: a failed resume may
	// leave a fresh visible TUI beside the old durable id. Only a native-id hook
	// accepted under this exact launch generation proves the two still match.
	if mode == domain.SessionModeTUI {
		launchID := strings.TrimSpace(rec.Metadata.RuntimeLaunchID)
		identityLaunchID := strings.TrimSpace(rec.Metadata.AgentSessionIDLaunchID)
		if launchID == "" || identityLaunchID != launchID {
			return "", handoff, fmt.Errorf("%w for %s", ErrNativeConversationUnverified, rec.Harness)
		}
	}
	return id, handoff, nil
}

// nativeConversationNotStarted is the only evidence that may turn a missing
// provider history into an intentional fresh handoff. A missing file or a
// reserved native id is not enough: both are also observable when persistence
// is lagging or broken after real work. The current rendered provider surface
// must positively identify its untouched initial composer, and AO must have no
// hook-derived conversation facts that contradict it.
func (m *Manager) nativeConversationNotStarted(
	ctx context.Context,
	rec domain.SessionRecord,
	agent ports.Agent,
) bool {
	if domain.NormalizeSessionMode(rec.Mode) != domain.SessionModeTUI ||
		rec.Metadata.LatestUserPrompt != "" ||
		rec.Metadata.LatestAssistantUpdate != "" ||
		rec.Metadata.NativeTranscriptPath != "" {
		return false
	}
	if _, ok := agent.(ports.AgentInterfaceHandoffHistoryProbe); !ok {
		return false
	}
	return m.terminalProvesNativeConversationNotStarted(ctx, rec, agent)
}

func (m *Manager) terminalProvesNativeConversationNotStarted(
	ctx context.Context,
	rec domain.SessionRecord,
	agent ports.Agent,
) bool {
	inspector, ok := agent.(ports.TerminalSurfaceInspector)
	if !ok {
		return false
	}
	styled, ok := m.runtime.(ports.StyledTerminalOutputReader)
	if !ok || strings.TrimSpace(rec.Metadata.RuntimeHandleID) == "" {
		return false
	}
	probeCtx, cancel := context.WithTimeout(ctx, interfaceInterruptSettle)
	defer cancel()
	output, err := styled.GetStyledOutput(
		probeCtx,
		ports.RuntimeHandle{ID: rec.Metadata.RuntimeHandleID},
		interfaceTransitionOutputLines,
	)
	if err != nil {
		return false
	}
	return inspector.InspectTerminalSurface(output).NativeConversationNotStarted
}

// persistedNativeConversationID verifies that a reserved native id has durable
// provider history behind it. A failed lookup is not proof of freshness: only
// positive untouched-composer evidence may produce the explicit fresh sentinel.
func (m *Manager) persistedNativeConversationID(
	ctx context.Context,
	rec domain.SessionRecord,
	id string,
	handoff ports.AgentInterfaceHandoff,
) (string, error) {
	if strings.TrimSpace(id) == "" {
		return "", nil
	}
	probe, ok := handoff.(ports.AgentInterfaceHandoffHistoryProbe)
	if !ok {
		return id, nil
	}
	project, err := m.loadProject(ctx, rec.ProjectID)
	if err != nil {
		return "", err
	}
	env := m.runtimeEnv(rec.ID, rec.ProjectID, rec.IssueID, project.Config.Env)
	exists, err := probe.NativeConversationExists(ctx, ports.SessionRef{
		ID:            string(rec.ID),
		WorkspacePath: rec.Metadata.WorkspacePath,
		Metadata: map[string]string{
			ports.MetadataKeyAgentSessionID: rec.Metadata.AgentSessionID,
		},
	}, id, env)
	if err != nil {
		return "", fmt.Errorf("inspect native conversation %s: %w", id, err)
	}
	if !exists {
		agent, registered := m.agents.Agent(rec.Harness)
		if registered && m.nativeConversationNotStarted(ctx, rec, agent) {
			return "", nil
		}
		return "", fmt.Errorf("%w for %s", ErrNativeConversationMissing, rec.Harness)
	}
	return id, nil
}

func (m *Manager) preflightInterfaceTarget(
	ctx context.Context,
	rec domain.SessionRecord,
	transition domain.SessionInterfaceTransition,
) error {
	if transition.TargetMode == domain.SessionModeChat {
		if m.chat == nil {
			return ports.ErrChatUnsupported
		}
		return m.chat.PreflightChat(ctx, rec.Harness)
	}
	agent, ok := m.agents.Agent(rec.Harness)
	if !ok {
		return ErrUnknownHarness
	}
	project, err := m.loadProject(ctx, rec.ProjectID)
	if err != nil {
		return err
	}
	systemPrompt, err := m.buildSystemPrompt(ctx, rec.Kind, rec.ProjectID)
	if err != nil {
		return err
	}
	config := effectiveAgentConfig(rec.Kind, project.Config)
	var cmd []string
	if transition.NativeConversationID == "" {
		cmd, _, _, err = freshLaunchArgv(ctx, agent, rec.ID, rec.Metadata.WorkspacePath,
			rec.Metadata, systemPrompt, "", config, rec.Kind, m.dataDir, true)
	} else {
		var resumable bool
		cmd, resumable, err = agent.GetRestoreCommand(ctx, ports.RestoreConfig{
			Session: ports.SessionRef{
				ID: string(rec.ID), WorkspacePath: rec.Metadata.WorkspacePath,
				Metadata: map[string]string{ports.MetadataKeyAgentSessionID: transition.NativeConversationID},
			},
			Kind: rec.Kind, DataDir: m.dataDir, SystemPrompt: systemPrompt,
			Config: config, Permissions: config.Permissions,
		})
		if err == nil && !resumable {
			return ErrNativeConversationMissing
		}
	}
	if err != nil {
		return err
	}
	return m.validateAgentBinary(cmd)
}

func (m *Manager) prepareSourceHandoff(
	ctx context.Context,
	rec domain.SessionRecord,
	policy domain.SessionInterfaceTransitionPolicy,
	lastTerminalInputAt time.Time,
) error {
	if domain.NormalizeSessionMode(rec.Mode) == domain.SessionModeChat {
		handoff, ok := m.chat.(chatHandoffLauncher)
		if !ok {
			return ErrInterfaceHandoffUnsupported
		}
		return handoff.PrepareChatHandoff(ctx, rec.ID, policy)
	}
	handle := runtimeHandle(rec.Metadata)
	if handle.ID == "" {
		if rec.Activity.State == domain.ActivityExited {
			return nil
		}
		return ErrIncompleteHandle
	}
	if policy == domain.SessionInterfaceTransitionInterrupt {
		if rec.Activity.State == domain.ActivityExited {
			return nil
		}
		interrupter, ok := m.runtime.(runtimeInterrupter)
		if !ok {
			return fmt.Errorf("runtime cannot interrupt a live terminal controller")
		}
		// Explicit Stop now is authoritative even when the durable activity row
		// says idle: approval screens can be user-blocked while that row is idle.
		// Sending the interrupt is what makes “Cancel request and switch” true.
		if err := interrupter.Interrupt(ctx, handle); err != nil {
			return err
		}
		// Let the provider observe Ctrl-C and begin flushing its native history
		// before an already-idle activity row can end the loop immediately.
		initialSettle := time.NewTimer(m.interfaceTransition.pollInterval)
		select {
		case <-ctx.Done():
			initialSettle.Stop()
			return ctx.Err()
		case <-initialSettle.C:
		}
		// Give the provider a short, bounded window to flush its native transcript
		// after Ctrl-C. The subsequent Destroy is still authoritative, so a stale
		// activity hook cannot turn "stop now" into an unbounded drain.
		deadline := time.NewTimer(interfaceInterruptSettle)
		defer deadline.Stop()
		ticker := time.NewTicker(interfaceTransitionPoll)
		defer ticker.Stop()
		for {
			current, found, readErr := m.store.GetSession(ctx, rec.ID)
			if readErr != nil {
				return readErr
			}
			if !found {
				return ErrNotFound
			}
			if current.Activity.State == domain.ActivityExited || tuiIdleAfterInput(current, lastTerminalInputAt) {
				return nil
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-deadline.C:
				return nil
			case <-ticker.C:
			}
		}
	}

	var detector ports.TerminalActivityDetector
	var surfaceInspector ports.TerminalSurfaceInspector
	var emptyComposerDetector ports.EmptyComposerDetector
	if agent, ok := m.agents.Agent(rec.Harness); ok {
		detector, _ = agent.(ports.TerminalActivityDetector)
		surfaceInspector, _ = agent.(ports.TerminalSurfaceInspector)
		emptyComposerDetector, _ = agent.(ports.EmptyComposerDetector)
	}
	styledOutput, _ := m.runtime.(ports.StyledTerminalOutputReader)
	// Surface proof is a joint capability: the adapter must understand its TUI
	// and the runtime must provide a rendered viewport with the styling that
	// distinguishes provider chrome from human-authored text. Runtimes without
	// that capability retain the causally-fresh idle / legacy fallback below.
	surfaceProofAvailable := surfaceInspector != nil && styledOutput != nil
	ticker := time.NewTicker(m.interfaceTransition.pollInterval)
	defer ticker.Stop()
	idleSince := time.Time{}
	idleSamples := 0
	unverifiedIdleSince := time.Time{}
	for {
		current, ok, err := m.store.GetSession(ctx, rec.ID)
		if err != nil {
			return err
		}
		if !ok {
			return ErrNotFound
		}
		if current.Activity.State == domain.ActivityExited {
			return nil
		}
		switch current.Activity.State {
		case domain.ActivityWaitingInput, domain.ActivityBlocked:
			// This typed provider state outranks terminal-screen heuristics. The
			// input gate is already closed, so continuing to wait would make the
			// decision impossible to answer and deadlock the drain on runtimes
			// without styled current-screen capture.
			return errDrainDecisionPending
		}
		now := time.Now()
		durableIdleProven := tuiIdleAfterInput(current, lastTerminalInputAt)
		idleProven := durableIdleProven && !surfaceProofAvailable
		unverifiedIdle := current.Activity.State == domain.ActivityIdle && !idleProven
		probeCtx := ctx
		var cancelProbe context.CancelFunc
		if unverifiedIdle {
			if unverifiedIdleSince.IsZero() {
				unverifiedIdleSince = now
			}
			probeCtx, cancelProbe = context.WithDeadline(ctx, unverifiedIdleSince.Add(m.interfaceTransition.staleIdleLimit))
		}
		// A screen can still show the idle composer briefly after Enter was
		// accepted. Give the last input a full settle window before treating
		// adapter markers as evidence, then require repeated samples below.
		inputQuiet := lastTerminalInputAt.IsZero() ||
			!now.Before(lastTerminalInputAt.Add(m.interfaceTransition.idleSettle))
		surfaceKnownBusy := false
		if surfaceProofAvailable && inputQuiet && styledOutput != nil {
			output, outputErr := styledOutput.GetStyledOutput(probeCtx, handle, interfaceTransitionOutputLines)
			now = time.Now()
			if errors.Is(outputErr, ports.ErrStyledTerminalOutputUnavailable) {
				// Detached ConPTY hosts survive daemon/app upgrades. A host from a
				// protocol version without current-screen support is a per-handle
				// capability miss, so retain the same durable/legacy fallback as a
				// runtime that never implemented styled output at all.
				surfaceProofAvailable = false
				idleProven = durableIdleProven
				unverifiedIdle = current.Activity.State == domain.ActivityIdle && !idleProven
			} else if outputErr == nil {
				observation := surfaceInspector.InspectTerminalSurface(output)
				switch {
				case observation.Work == ports.TerminalSurfaceWorkWaitingInput,
					observation.Work == ports.TerminalSurfaceWorkBlocked:
					// Provider-owned approval and input requests are not composer
					// drafts. The terminal input gate is closed while draining, so
					// waiting here would also make the request impossible to answer.
					// Abort the drain and return control to the source interface.
					if cancelProbe != nil {
						cancelProbe()
					}
					return errDrainDecisionPending
				case observation.Composer == ports.TerminalComposerDraft &&
					current.Activity.State == domain.ActivityIdle:
					// A positively identified draft is sufficient to preserve the
					// source. Work markers are provider chrome heuristics and may
					// also occur in transcript or draft text, so they cannot hide
					// unsent input when the durable provider state is idle.
					if cancelProbe != nil {
						cancelProbe()
					}
					return errDrainDraftPresent
				case current.Activity.State == domain.ActivityIdle &&
					observation.Work == ports.TerminalSurfaceWorkIdle &&
					observation.Composer == ports.TerminalComposerEmpty:
					idleProven = true
				case observation.Work == ports.TerminalSurfaceWorkActive:
					surfaceKnownBusy = true
				}
			}
		}
		if !surfaceProofAvailable && unverifiedIdle && detector != nil && inputQuiet {
			// Compatibility path for terminal activity adapters that have not yet
			// adopted the richer surface observation capability.
			if output, outputErr := m.runtime.GetOutput(probeCtx, handle, interfaceTransitionOutputLines); outputErr == nil {
				now = time.Now()
				state, authoritative := detector.DetectTerminalActivity(output)
				idleProven = authoritative && state == domain.ActivityIdle
				if idleProven && emptyComposerDetector != nil {
					// The legacy activity contract cannot carry draft state. At
					// this destructive boundary, an adapter that can separately
					// prove composer emptiness must do so before idle is accepted.
					idleProven = emptyComposerDetector.ComposerIsEmpty(output)
				}
			}
		}
		if current.Activity.State != domain.ActivityIdle || surfaceKnownBusy {
			// Active work may wait without a deadline. A recognized current-screen
			// state resets prior ambiguity; decisions returned above instead because
			// the closed input gate would prevent the user from answering them.
			unverifiedIdleSince = time.Time{}
			if surfaceKnownBusy && cancelProbe != nil {
				cancelProbe()
				cancelProbe = nil
				probeCtx = ctx
			}
		}
		if idleProven {
			idleSamples++
			if idleSince.IsZero() {
				idleSince = now
			} else if now.Sub(idleSince) >= m.interfaceTransition.idleSettle &&
				(!surfaceProofAvailable && durableIdleProven || idleSamples >= interfaceTransitionSurfaceIdleSamples) {
				if cancelProbe != nil {
					cancelProbe()
				}
				return nil
			}
		} else {
			idleSince = time.Time{}
			idleSamples = 0
		}
		alive, probeErr := m.runtime.IsAlive(probeCtx, handle)
		if cancelProbe != nil {
			cancelProbe()
		}
		if probeErr == nil && !alive {
			return nil
		}
		now = time.Now()
		// Bound the whole contradictory idle regime, not just consecutive
		// unknown captures. Partial terminal redraws may alternate between safe
		// and ambiguous forever; neither outcome is enough to stop the source.
		if current.Activity.State == domain.ActivityIdle && !idleProven && !surfaceKnownBusy &&
			!unverifiedIdleSince.IsZero() && now.Sub(unverifiedIdleSince) >= m.interfaceTransition.staleIdleLimit {
			return errDrainQuiescenceUnverified
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// tuiIdleAfterInput proves that the idle fact was written after every terminal
// frame accepted before the drain gate closed. Merely observing an old idle row
// is unsafe: the PTY may still be processing input that arrived afterward.
func tuiIdleAfterInput(rec domain.SessionRecord, lastInputAt time.Time) bool {
	if rec.Activity.State != domain.ActivityIdle {
		return false
	}
	return lastInputAt.IsZero() || !rec.Activity.LastActivityAt.Before(lastInputAt)
}

func (m *Manager) abortSourceHandoff(transition domain.SessionInterfaceTransition) {
	if transition.SourceMode != domain.SessionModeChat {
		return
	}
	if handoff, ok := m.chat.(chatHandoffLauncher); ok {
		handoff.AbortChatHandoff(transition.SessionID)
	}
}

func (m *Manager) stopSourceController(ctx context.Context, rec domain.SessionRecord) error {
	if domain.NormalizeSessionMode(rec.Mode) == domain.SessionModeChat {
		if m.chat == nil {
			return nil
		}
		return m.chat.StopChat(ctx, rec.ID)
	}
	handle := runtimeHandle(rec.Metadata)
	if handle.ID == "" {
		return nil
	}
	return m.runtime.Destroy(ctx, handle)
}

// stopSourceControllerConclusive makes one retry because both shipped runtime
// teardown and Chat stop are idempotent. A TUI liveness probe can turn a failed
// tmux command into a conclusive success when the process did in fact exit. Chat
// Service retains a controller whose event stream has not stopped, so its retry
// waits for the same process rather than losing the only handle to it.
func (m *Manager) stopSourceControllerConclusive(rec domain.SessionRecord) error {
	var failures []error
	for attempt := 0; attempt < 2; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), interfaceTransitionStopLimit)
		err := m.stopSourceController(ctx, rec)
		cancel()
		if err == nil {
			return nil
		}
		failures = append(failures, err)
		if domain.NormalizeSessionMode(rec.Mode) != domain.SessionModeChat {
			probeCtx, probeCancel := context.WithTimeout(context.Background(), 5*time.Second)
			alive, probeErr := m.runtime.IsAlive(probeCtx, runtimeHandle(rec.Metadata))
			probeCancel()
			if probeErr == nil && !alive {
				return nil
			}
			if probeErr != nil {
				failures = append(failures, fmt.Errorf("verify terminal controller stopped: %w", probeErr))
			}
		}
	}
	return fmt.Errorf("could not prove the source controller stopped after retry: %w", errors.Join(failures...))
}

func (m *Manager) startTransitionTarget(ctx context.Context, id domain.SessionID, fresh, requireNativeHistory bool) error {
	ctx, cancel := context.WithTimeout(ctx, interfaceTransitionStepLimit)
	defer cancel()
	rec, ok, err := m.store.GetSession(ctx, id)
	if err != nil {
		return err
	}
	if !ok {
		return ErrNotFound
	}
	project, err := m.loadProject(ctx, rec.ProjectID)
	if err != nil {
		return err
	}
	ws := workspaceInfo(rec)
	// A genuinely fresh target has nothing to replay. Every resumed TUI -> Chat
	// handoff must import native history before activation; rollback and ordinary
	// daemon restore deliberately use the normal context-resume policy.
	_, err = m.relaunchSessionWithPolicy(
		ctx, "switch interface", rec, project, ws, nil,
		fresh, requireNativeHistory && !fresh,
	)
	return err
}

func (m *Manager) rollbackInterfaceTransition(
	transition domain.SessionInterfaceTransition,
	sourceRuntimeHandle ports.RuntimeHandle,
	modeChanged bool,
	code string,
	cause error,
) {
	ctx, cancel := context.WithTimeout(context.Background(), interfaceTransitionStepLimit)
	defer cancel()
	if modeChanged {
		// A target may have partially started. Stop whatever its committed mode can
		// identify before restoring the old writer.
		if current, ok, _ := m.store.GetSession(ctx, transition.SessionID); ok {
			_ = m.stopSourceController(ctx, current)
		}
		if m.lcm == nil {
			_ = m.finishInterfaceTransition(transition.ID, domain.SessionInterfaceTransitionRecovery,
				"RECOVERY_REQUIRED", cause.Error()+"; rollback mode: lifecycle manager is unavailable")
			return
		}
		changed, err := m.lcm.CommitControllerEpoch(ctx, transition.SessionID,
			transition.TargetMode, transition.SourceMode, transition.NativeConversationID,
			transition.NativeConversationID == "")
		if err != nil || !changed {
			detail := cause.Error()
			if err != nil {
				detail += "; rollback mode: " + err.Error()
			}
			_ = m.finishInterfaceTransition(transition.ID, domain.SessionInterfaceTransitionRecovery,
				"RECOVERY_REQUIRED", detail)
			return
		}
	}
	// A conclusive liveness probe can prove the source process exited even when
	// its first teardown timed out. Clear any stale runtime registration using
	// the handle captured before the controller epoch erased it, or rollback can
	// fail to recreate the TUI with "session already exists" on ConPTY.
	if transition.SourceMode != domain.SessionModeChat && sourceRuntimeHandle.ID != "" {
		if err := m.runtime.Destroy(ctx, sourceRuntimeHandle); err != nil {
			_ = m.finishInterfaceTransition(transition.ID, domain.SessionInterfaceTransitionRecovery,
				"RECOVERY_REQUIRED", cause.Error()+"; source runtime cleanup: "+err.Error())
			return
		}
	}
	if err := m.startTransitionTarget(ctx, transition.SessionID, transition.NativeConversationID == "", false); err != nil {
		_ = m.finishInterfaceTransition(transition.ID, domain.SessionInterfaceTransitionRecovery,
			"RECOVERY_REQUIRED", cause.Error()+"; source restore: "+err.Error())
		return
	}
	_ = m.finishInterfaceTransition(transition.ID, domain.SessionInterfaceTransitionFailed, code, cause.Error())
}

func (m *Manager) moveInterfaceTransition(
	id string,
	next domain.SessionInterfaceTransitionPhase,
	errorCode, errorDetail string,
) error {
	return m.moveInterfaceTransitionWithNativeID(id, next, "", errorCode, errorDetail)
}

func (m *Manager) moveInterfaceTransitionWithNativeID(
	id string,
	next domain.SessionInterfaceTransitionPhase,
	nativeConversationID string,
	errorCode, errorDetail string,
) error {
	store, ok := m.store.(interfaceTransitionStore)
	if !ok {
		return ports.ErrChatUnsupported
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for attempt := 0; attempt < 4; attempt++ {
		current, ok, err := store.GetSessionInterfaceTransition(ctx, id)
		if err != nil {
			return err
		}
		if !ok {
			return ErrNotFound
		}
		if current.Phase.Terminal() {
			return fmt.Errorf("transition %s is already %s", id, current.Phase)
		}
		if nativeConversationID == "" {
			nativeConversationID = current.NativeConversationID
		}
		moved, err := store.AdvanceSessionInterfaceTransition(ctx, id, current.Phase, next,
			nativeConversationID, errorCode, errorDetail, m.clock())
		if err != nil {
			return err
		}
		if moved {
			return nil
		}
	}
	return fmt.Errorf("transition %s changed concurrently", id)
}

func (m *Manager) finishInterfaceTransition(
	id string,
	phase domain.SessionInterfaceTransitionPhase,
	errorCode, errorDetail string,
) error {
	return m.moveInterfaceTransition(id, phase, errorCode, errorDetail)
}

func (m *Manager) deliverTransitionMessages(
	ctx context.Context,
	transition domain.SessionInterfaceTransition,
) error {
	m.transitionDeliveryAttemptMu.Lock()
	defer m.transitionDeliveryAttemptMu.Unlock()
	store, ok := m.store.(interfaceTransitionStore)
	if !ok {
		return ports.ErrChatUnsupported
	}
	ctx, cancel := context.WithTimeout(ctx, interfaceTransitionStepLimit)
	defer cancel()
	messages, err := store.ListPendingSessionInterfaceTransitionMessages(ctx, transition.ID)
	if err != nil {
		return fmt.Errorf("read transition %s messages: %w", transition.ID, err)
	}
	if len(messages) == 0 {
		return nil
	}
	rec, err := m.waitForTransitionDeliveryReady(ctx, transition.SessionID, time.Time{})
	if err != nil {
		return fmt.Errorf("wait to deliver transition %s messages: %w", transition.ID, err)
	}
	lastTerminalInputAt, releaseTerminalInput := m.beginTerminalInputDrain(rec)
	if releaseTerminalInput != nil {
		defer releaseTerminalInput()
		// Closing the raw-input gate and rechecking readiness makes queued AO
		// delivery the next instruction accepted by the TUI controller.
		if _, err := m.waitForTransitionDeliveryReady(ctx, transition.SessionID, lastTerminalInputAt); err != nil {
			return fmt.Errorf("recheck transition %s delivery readiness: %w", transition.ID, err)
		}
	}
	for _, message := range messages {
		if err := m.send(ctx, transition.SessionID, message.Message, message.ClientMessageID); err != nil {
			return fmt.Errorf("deliver transition %s message %d: %w", transition.ID, message.ID, err)
		}
		if err := store.MarkSessionInterfaceTransitionMessageDelivered(ctx, message.ID, m.clock()); err != nil {
			return fmt.Errorf("acknowledge transition %s message %d: %w", transition.ID, message.ID, err)
		}
	}
	return nil
}

func (m *Manager) waitForTransitionDeliveryReady(
	ctx context.Context,
	id domain.SessionID,
	lastTerminalInputAt time.Time,
) (domain.SessionRecord, error) {
	ticker := time.NewTicker(interfaceTransitionPoll)
	defer ticker.Stop()
	idleSince := time.Time{}
	for {
		rec, ok, err := m.store.GetSession(ctx, id)
		if err != nil {
			return domain.SessionRecord{}, err
		}
		if !ok {
			return domain.SessionRecord{}, ErrNotFound
		}
		if rec.IsTerminated || rec.Activity.State == domain.ActivityExited {
			return domain.SessionRecord{}, ErrTerminated
		}
		ready := rec.Activity.State == domain.ActivityIdle
		if domain.NormalizeSessionMode(rec.Mode) == domain.SessionModeTUI {
			// MarkSpawned intentionally clears FirstSignalAt. Waiting for a signal
			// prevents replaying a queued prompt into the TUI before its native
			// initialization/resume sequence has reached an input-ready state.
			ready = ready && !rec.FirstSignalAt.IsZero() && tuiIdleAfterInput(rec, lastTerminalInputAt)
		}
		if ready {
			if idleSince.IsZero() {
				idleSince = time.Now()
			} else if time.Since(idleSince) >= interfaceTransitionIdleSettle {
				return rec, nil
			}
		} else {
			idleSince = time.Time{}
		}
		select {
		case <-ctx.Done():
			return domain.SessionRecord{}, ctx.Err()
		case <-ticker.C:
		}
	}
}

func (m *Manager) queueDuringInterfaceTransition(
	ctx context.Context,
	id domain.SessionID,
	message, clientMessageID string,
) (bool, error) {
	store, ok := m.store.(interfaceTransitionStore)
	if !ok {
		return false, nil
	}
	transition, active, err := store.GetActiveSessionInterfaceTransition(ctx, id)
	if err != nil || !active {
		return false, err
	}
	if strings.TrimSpace(clientMessageID) == "" {
		clientMessageID = "interface-transition:" + m.newLaunchID()
	}
	if err := store.EnqueueSessionInterfaceTransitionMessage(
		ctx, transition.ID, clientMessageID, message, m.clock(),
	); err != nil {
		return true, err
	}
	return true, nil
}

// settleTransitionMessages runs after every transition outcome, not only the
// happy path. The immediate attempt makes rollback/cancellation responsive;
// the daemon-lifetime worker retains responsibility after any transient error.
func (m *Manager) settleTransitionMessages(transition domain.SessionInterfaceTransition) {
	store, ok := m.store.(interfaceTransitionStore)
	if !ok {
		m.logger.Error("interface transition: store does not support queued messages", "transitionID", transition.ID)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), interfaceTransitionStepLimit)
	current, ok, err := store.GetSessionInterfaceTransition(ctx, transition.ID)
	cancel()
	if err != nil {
		m.logger.Error("interface transition: read terminal state for queued messages",
			"transitionID", transition.ID, "error", err)
		m.wakeTransitionMessageDispatcher()
		return
	}
	if !ok || !current.Phase.Terminal() {
		m.wakeTransitionMessageDispatcher()
		return
	}
	if err := m.deliverTransitionMessages(context.Background(), current); err != nil {
		m.logger.Error("interface transition: queued-message delivery deferred",
			"transitionID", transition.ID, "error", err)
	}
	m.wakeTransitionMessageDispatcher()
}

func (m *Manager) deliverAllTransitionMessages(ctx context.Context) error {
	store, ok := m.store.(interfaceTransitionStore)
	if !ok {
		return nil
	}
	transitions, err := store.ListDeliverableSessionInterfaceTransitions(ctx)
	if err != nil {
		return err
	}
	var failures []error
	for _, transition := range transitions {
		if err := m.deliverTransitionMessages(ctx, transition); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

func (m *Manager) wakeTransitionMessageDispatcher() {
	select {
	case m.transitionDeliveryWake <- struct{}{}:
	default:
	}
}

// startTransitionMessageDispatcher starts one daemon-lifetime durable outbox
// worker. Reconcile supplies the daemon context, so shutdown bounds the worker;
// a periodic scan repairs crashes that happened after a transition became
// terminal but before its in-memory wake-up.
func (m *Manager) startTransitionMessageDispatcher(ctx context.Context) {
	if _, ok := m.store.(interfaceTransitionStore); !ok {
		return
	}
	m.transitionDeliveryMu.Lock()
	if m.transitionDeliveryRunning {
		m.transitionDeliveryMu.Unlock()
		return
	}
	m.transitionDeliveryRunning = true
	m.transitionDeliveryMu.Unlock()

	go func() {
		defer func() {
			m.transitionDeliveryMu.Lock()
			m.transitionDeliveryRunning = false
			m.transitionDeliveryMu.Unlock()
		}()
		delay := time.Duration(0)
		for {
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				return
			case <-m.transitionDeliveryWake:
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
			case <-timer.C:
			}
			if err := m.deliverAllTransitionMessages(ctx); err != nil {
				m.logger.Error("interface transition: retry queued-message delivery", "error", err)
				delay = interfaceDeliveryRetry
			} else {
				delay = interfaceDeliveryIdlePoll
			}
		}
	}()
}

func (m *Manager) hasActiveInterfaceTransition(ctx context.Context, id domain.SessionID) (bool, error) {
	store, ok := m.store.(interfaceTransitionStore)
	if !ok {
		return false, nil
	}
	_, active, err := store.GetActiveSessionInterfaceTransition(ctx, id)
	return active, err
}

// recoverInterruptedInterfaceTransitions closes every durable handoff left
// active by a daemon exit. A TUI -> Chat transition whose mode commit landed is
// rolled back first: ordinary Chat restore is context-only and cannot satisfy
// the handoff's mandatory replay barrier. Reconcile can then restore the source
// TUI, and a later retry performs native replay again idempotently.
func (m *Manager) recoverInterruptedInterfaceTransitions(
	ctx context.Context,
) ([]domain.SessionInterfaceTransition, error) {
	store, ok := m.store.(interfaceTransitionStore)
	if !ok {
		return nil, nil
	}
	active, err := store.ListActiveSessionInterfaceTransitions(ctx)
	if err != nil {
		return nil, err
	}
	for i := range active {
		transition := &active[i]
		detail := "The daemon restarted during the interface switch; AO recovered the session from its last committed mode."
		if transition.SourceMode == domain.SessionModeTUI && transition.TargetMode == domain.SessionModeChat {
			rec, found, readErr := m.store.GetSession(ctx, transition.SessionID)
			if readErr != nil {
				return nil, readErr
			}
			if found && !rec.IsTerminated && domain.NormalizeSessionMode(rec.Mode) == transition.TargetMode {
				if m.lcm == nil {
					return nil, fmt.Errorf("recover transition %s: lifecycle manager is unavailable", transition.ID)
				}
				changed, rollbackErr := m.lcm.CommitControllerEpoch(
					ctx,
					transition.SessionID,
					transition.TargetMode,
					transition.SourceMode,
					transition.NativeConversationID,
					transition.NativeConversationID == "",
				)
				if rollbackErr != nil {
					return nil, fmt.Errorf("recover transition %s source mode: %w", transition.ID, rollbackErr)
				}
				if !changed {
					return nil, fmt.Errorf("recover transition %s source mode: session changed", transition.ID)
				}
				detail = "The daemon restarted during the interface switch; AO restored Terminal so native history can be replayed safely on retry."
			}
		}
		moved, err := store.AdvanceSessionInterfaceTransition(
			ctx,
			transition.ID,
			transition.Phase,
			domain.SessionInterfaceTransitionRecovery,
			transition.NativeConversationID,
			"DAEMON_RESTARTED",
			detail,
			m.clock(),
		)
		if err != nil {
			return nil, err
		}
		if !moved {
			return nil, fmt.Errorf("transition %s changed while recovering", transition.ID)
		}
		transition.Phase = domain.SessionInterfaceTransitionRecovery
		transition.ErrorCode = "DAEMON_RESTARTED"
		transition.ErrorDetail = detail
		transition.UpdatedAt = m.clock()
		transition.CompletedAt = transition.UpdatedAt
	}
	return active, nil
}

func oppositeSessionMode(mode domain.SessionMode) domain.SessionMode {
	if domain.NormalizeSessionMode(mode) == domain.SessionModeChat {
		return domain.SessionModeTUI
	}
	return domain.SessionModeChat
}

func interfaceTransitionErrorCode(err error) string {
	switch {
	case errors.Is(err, ports.ErrChatUnsupported), errors.Is(err, ErrInterfaceHandoffUnsupported):
		return "INTERFACE_HANDOFF_UNSUPPORTED"
	case errors.Is(err, ports.ErrChatDriverUnavailable), errors.Is(err, ports.ErrAgentBinaryNotFound):
		return "TARGET_UNAVAILABLE"
	case errors.Is(err, ports.ErrChatDriverIncompatible):
		return "TARGET_INCOMPATIBLE"
	case errors.Is(err, ports.ErrChatAuthRequired):
		return "TARGET_AUTH_REQUIRED"
	case errors.Is(err, ErrNativeConversationMissing):
		return "NATIVE_SESSION_MISSING"
	case errors.Is(err, ErrNativeConversationUnverified):
		return "NATIVE_SESSION_UNVERIFIED"
	default:
		return "TARGET_PREFLIGHT_FAILED"
	}
}
