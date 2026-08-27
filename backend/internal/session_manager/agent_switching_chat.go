package sessionmanager

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// executeChatAgentSwitch replaces a structured controller without manufacturing
// a terminal runtime. The durable switch record and session row retain the same
// ownership boundaries as the TUI saga; their generation fence is the Chat
// controller generation instead of a runtime launch id.
func (m *Manager) executeChatAgentSwitch(
	ctx context.Context,
	admitted *admittedAgentSwitch,
) (result domain.AgentSwitch, retErr error) {
	workerCtx := ctx
	result = admitted.record
	store := admitted.store
	cfg := admitted.config
	rec := admitted.session
	id := rec.ID
	project := admitted.project
	targetAgent := admitted.targetAgent

	sourceStopConclusive := false
	sourceStopped := false
	targetWorkspacePrepared := false
	targetOwnerCommitted := false
	targetOwnershipAmbiguous := false
	skipTerminalization := false
	targetEnv := m.runtimeEnv(rec.ID, rec.ProjectID, rec.IssueID, project.Config.Env)
	m.augmentAgentRuntimeEnv(targetAgent, targetEnv)

	defer func() {
		if panicValue := recover(); panicValue != nil {
			retErr = fmt.Errorf("switch Chat agent %s panicked: %v", id, panicValue)
		}
		if retErr != nil && !sourceStopped && !sourceStopConclusive {
			m.abortChatAgentSwitchHandoff(rec)
		}
		if retErr != nil && sourceStopConclusive && !sourceStopped {
			// The controller is already gone, but the durable confirmation did not
			// finish. Keep the stopping_source row and mutation gate for restart
			// reconciliation; treating this as pre-stop would reopen a dead source.
			skipTerminalization = true
		}
		rollbackSafe := retErr != nil && sourceStopped && !targetOwnerCommitted && !targetOwnershipAmbiguous
		if retErr != nil && targetWorkspacePrepared && !targetOwnerCommitted && !targetOwnershipAmbiguous {
			cleanupCtx, cancel := switchDurableContext(workerCtx)
			cleanupErr := m.cleanupPreparedAgentWorkspaceStrict(
				cleanupCtx, targetAgent, id, rec.Metadata.WorkspacePath, targetEnv)
			cancel()
			if cleanupErr != nil {
				rollbackSafe = false
				skipTerminalization = true
				retErr = errors.Join(retErr, fmt.Errorf("clean Chat target workspace before source rollback: %w", cleanupErr))
			} else {
				targetWorkspacePrepared = false
			}
		}
		if rollbackSafe {
			rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(workerCtx), switchPostStopWait)
			rollbackErr := m.rollbackStoppedChatAgentSwitchSource(rollbackCtx, rec, project)
			cancel()
			if rollbackErr != nil {
				skipTerminalization = true
				retErr = errors.Join(retErr, fmt.Errorf("restore Chat source after failed switch: %w", rollbackErr))
				m.logger.Error("Chat agent switch: automatic source rollback failed",
					"sessionID", id, "switchID", result.ID, "error", rollbackErr)
			} else {
				m.logger.Info("Chat agent switch: source restored after target failure",
					"sessionID", id, "switchID", result.ID, "harness", result.FromHarness)
			}
		}
		if retErr != nil && !result.State.Terminal() && skipTerminalization && !result.RequiresRecovery() {
			markerCtx, cancel := switchDurableContext(workerCtx)
			marked, markerErr := m.markRetainedSourceRecovery(markerCtx, store, result, targetOwnershipAmbiguous)
			cancel()
			if markerErr != nil {
				retErr = errors.Join(retErr, fmt.Errorf("persist retained Chat switch recovery marker: %w", markerErr))
			} else {
				result = marked
			}
		}
		if retErr != nil && !result.State.Terminal() && !skipTerminalization {
			settleCtx, cancel := switchDurableContext(workerCtx)
			settled, failErr := m.failAgentSwitch(
				settleCtx, store, result, switchErrorCode(retErr, result.State))
			cancel()
			if failErr == nil {
				result = settled
				if result.State == domain.AgentSwitchCompleted {
					retErr = nil
				}
			} else {
				m.logger.Error("Chat agent switch: failed to persist terminal failure",
					"sessionID", id, "switchID", result.ID, "state", result.State, "error", failErr)
			}
		}
		if targetWorkspacePrepared && !targetOwnerCommitted && !targetOwnershipAmbiguous {
			cleanupCtx, cancel := switchDurableContext(workerCtx)
			m.cleanupPreparedAgentWorkspace(
				cleanupCtx, targetAgent, id, rec.Metadata.WorkspacePath, targetEnv)
			cancel()
		}
		if result.State.Terminal() && strings.TrimSpace(m.dataDir) != "" {
			cleanupCtx, cancel := switchDurableContext(workerCtx)
			cleanupErr := m.cleanupAgentHandoffArtifacts(cleanupCtx, result)
			cancel()
			if cleanupErr != nil {
				m.logger.Warn("Chat agent switch: handoff artifact cleanup failed",
					"sessionID", id, "switchID", result.ID, "state", result.State, "error", cleanupErr)
			}
		}
		if result.State.Terminal() {
			m.endAgentSwitch(id)
			return
		}
		m.retainAgentSwitch(id)
	}()

	if m.chat == nil {
		return result, fmt.Errorf("switch Chat agent %s: %w", id, ports.ErrChatUnsupported)
	}
	if err := m.chat.PreflightChat(
		ctx,
		cfg.TargetHarness,
		effectiveAgentConfig(rec.Kind, project.Config).Permissions,
	); err != nil {
		return result, fmt.Errorf("switch Chat agent %s: target preflight: %w", id, err)
	}
	handoff, ok := m.chat.(chatHandoffLauncher)
	if !ok {
		return result, fmt.Errorf("switch Chat agent %s: %w", id, ErrInterfaceHandoffUnsupported)
	}

	systemPrompt, err := m.buildSystemPrompt(ctx, rec.Kind, rec.ProjectID)
	if err != nil {
		return result, fmt.Errorf("switch Chat agent %s: system prompt: %w", id, err)
	}
	systemPrompt = appendAgentContinuationProtocol(systemPrompt)
	agentConfig := effectiveAgentConfig(rec.Kind, project.Config)
	if roleOverride(rec.Kind, project.Config).Harness != cfg.TargetHarness {
		agentConfig.Model = ""
		agentConfig.Mode = ""
	}
	if model := strings.TrimSpace(cfg.Model); model != "" {
		agentConfig.Model = model
	}
	targetConfigDir, err := nativeConfigDir(ctx, targetAgent, targetEnv)
	if err != nil {
		return result, fmt.Errorf("switch Chat agent %s: target config: %w", id, err)
	}
	resumeCandidate, resumable, err := m.findTargetResumeCandidate(
		ctx, store, rec, cfg.TargetHarness, targetAgent, targetConfigDir)
	if err != nil {
		return result, fmt.Errorf("switch Chat agent %s: target resume candidate: %w", id, err)
	}
	targetStartMode := domain.AgentSwitchTargetStartFresh
	providerConversationID := ""
	if resumable {
		targetStartMode = domain.AgentSwitchTargetStartResumed
		providerConversationID = resumeCandidate.NativeSessionID
	}
	additionalDirectories, err := m.restoredWorkspaceProjectDirectories(
		ctx, rec, project, rec.Metadata.WorkspacePath)
	if err != nil {
		return result, fmt.Errorf("switch Chat agent %s: workspace roots: %w", id, err)
	}
	if _, _, err := m.prepareAgentHandoffPaths(ctx, id, string(result.ID)); err != nil {
		return result, fmt.Errorf("switch Chat agent %s: prepare handoff directory: %w", id, err)
	}

	result, err = m.settleOptionalAgentHandoff(
		ctx, store, result, domain.AgentHandoffUnavailable)
	if err != nil {
		return result, fmt.Errorf("switch Chat agent %s: close optional source handoff: %w", id, err)
	}
	if err := handoff.PrepareChatHandoff(
		ctx, id, domain.SessionInterfaceTransitionInterrupt); err != nil {
		return result, fmt.Errorf("switch Chat agent %s: quiesce source: %w", id, err)
	}

	targetGeneration := domain.AgentGenerationID(strings.TrimSpace(m.newLaunchID()))
	if targetGeneration == "" {
		return result, fmt.Errorf("switch Chat agent %s: target controller generation is empty", id)
	}
	if err := m.advanceAgentSwitch(ctx, store, &result, domain.AgentSwitchStoppingSource, func(next *domain.AgentSwitch) {
		next.TargetStartMode = targetStartMode
		next.TargetGenerationID = targetGeneration
	}); err != nil {
		return result, fmt.Errorf("switch Chat agent %s: stop source: %w", id, err)
	}

	if err := m.stopSourceControllerConclusive(rec); err != nil {
		skipTerminalization = true
		return result, fmt.Errorf("switch Chat agent %s: stop source controller: %w", id, err)
	}
	sourceStopConclusive = true
	stoppedAt := m.clock()
	boundaryCtx, cancelBoundary := switchDurableContext(workerCtx)
	confirmed, err := m.lcm.ConfirmAgentSwitchSourceStopped(
		boundaryCtx,
		domain.AgentSwitchSourceStopConfirmation{
			SwitchID:                           result.ID,
			SessionID:                          id,
			SourceMode:                         domain.SessionModeChat,
			SourceHarness:                      rec.Harness,
			SourceGenerationID:                 result.SourceGenerationID,
			ExpectedSourceControllerGeneration: rec.Metadata.ControllerGeneration,
			TargetGenerationID:                 targetGeneration,
			StoppedAt:                          stoppedAt,
		},
	)
	if err != nil {
		cancelBoundary()
		skipTerminalization = true
		return result, fmt.Errorf("switch Chat agent %s: persist source stop: %w", id, err)
	}
	if !confirmed {
		cancelBoundary()
		skipTerminalization = true
		return result, fmt.Errorf("switch Chat agent %s: persist source stop: durable ownership changed concurrently", id)
	}
	sourceStopped = true
	result, err = requireAgentSwitch(boundaryCtx, store, result.ID)
	cancelBoundary()
	if err != nil {
		return result, fmt.Errorf("switch Chat agent %s: reload source stop: %w", id, err)
	}
	stoppedSession, ok, err := m.store.GetSession(workerCtx, id)
	if err != nil {
		return result, fmt.Errorf("switch Chat agent %s: reload stopped session: %w", id, err)
	}
	if !ok {
		return result, fmt.Errorf("switch Chat agent %s: reload stopped session: %w", id, ErrNotFound)
	}

	postStopWait := m.switchPostStopWait
	if postStopWait <= 0 {
		postStopWait = switchPostStopWait
	}
	postStopCtx, cancelPostStop := context.WithTimeout(workerCtx, postStopWait)
	defer cancelPostStop()
	ctx = postStopCtx

	observedTranscript, sourceTranscriptStatus := m.captureSourceTranscriptFact(
		ctx, admitted.sourceAgent, admitted.sourceNative, true)
	finalContext := deterministicSwitchContext{
		OriginalTask:          stoppedSession.Metadata.Prompt,
		LatestUserPrompt:      stoppedSession.Metadata.LatestUserPrompt,
		LatestAssistantUpdate: stoppedSession.Metadata.LatestAssistantUpdate,
		Workspaces:            m.captureWorkspaceFacts(ctx, stoppedSession),
		PullRequests:          m.capturePRFacts(ctx, id),
		CapturedAt:            m.clock(),
	}
	if observedTranscript != nil {
		finalContext.SourceTranscriptPath = observedTranscript.Path
	}
	continuation := buildTargetContinuationMessageWithLimit(
		result, finalContext, observedTranscript, handoffContinuationMaxBytes)
	writtenFinal, err := m.writeFinalizedHandoffFile(ctx, result, continuation)
	if err != nil {
		return result, fmt.Errorf("switch Chat agent %s: retain finalized handoff: %w", id, err)
	}
	result, err = m.finalizeAgentSwitchHandoff(
		ctx, store, result, writtenFinal, false, sourceTranscriptStatus)
	if err != nil {
		return result, fmt.Errorf("switch Chat agent %s: record finalized handoff: %w", id, err)
	}
	finalSystemPrompt := appendAgentSwitchContinuation(systemPrompt, continuation)
	systemPromptFile, err := m.prepareSystemPromptFile(rec.ID, cfg.TargetHarness, finalSystemPrompt)
	if err != nil {
		return result, fmt.Errorf("switch Chat agent %s: target system prompt file: %w", id, err)
	}
	if err := m.prepareWorkspace(
		ctx, targetAgent, rec.ID, rec.Metadata.WorkspacePath,
		finalSystemPrompt, systemPromptFile, agentConfig, targetEnv,
	); err != nil {
		return result, fmt.Errorf("switch Chat agent %s: prepare target workspace: %w", id, err)
	}
	targetWorkspacePrepared = true
	if err := m.advanceAgentSwitch(ctx, store, &result, domain.AgentSwitchStartingTarget, nil); err != nil {
		return result, fmt.Errorf("switch Chat agent %s: record target start: %w", id, err)
	}

	_, err = m.chat.StartChat(ctx, ChatStart{
		SessionID:               id,
		ProjectID:               rec.ProjectID,
		Kind:                    rec.Kind,
		Harness:                 cfg.TargetHarness,
		DataDir:                 m.dataDir,
		WorkspacePath:           rec.Metadata.WorkspacePath,
		Env:                     targetEnv,
		Model:                   agentConfig.Model,
		Permissions:             agentConfig.Permissions,
		SystemPrompt:            finalSystemPrompt,
		AdditionalDirectories:   additionalDirectories,
		ProviderConversationID:  providerConversationID,
		ProviderScopeID:         chatSwitchProviderBoundaryID(result.ID),
		ControllerGeneration:    string(targetGeneration),
		SkipNativeHistoryImport: resumable,
		ControllerReady: func(started ChatStarted) (ChatControllerCommit, error) {
			emptyCommit := ChatControllerCommit{}
			if strings.TrimSpace(started.ProviderConversationID) == "" ||
				started.ControllerGeneration != string(targetGeneration) {
				return emptyCommit, errors.New("target Chat controller returned incomplete or mismatched identity")
			}
			var stored domain.AgentNativeSession
			if resumable {
				if started.ProviderConversationID != resumeCandidate.NativeSessionID {
					return emptyCommit, errors.New("resumed target Chat controller changed provider conversation identity")
				}
				expectedGeneration := resumeCandidate.LastGenerationID
				stored = resumeCandidate
				stored.LastGenerationID = targetGeneration
				stored.LastUsedAt = m.clock()
				changed, updateErr := store.UpdateAgentNativeSession(ctx, stored, expectedGeneration)
				if updateErr != nil {
					return emptyCommit, fmt.Errorf("advance resumed target Chat native session: %w", updateErr)
				}
				if !changed {
					return emptyCommit, errors.New("resumed target Chat native session changed concurrently")
				}
			} else {
				now := m.clock()
				native := domain.AgentNativeSession{
					ID:          domain.AgentNativeSessionID("native-" + uuid.NewString()),
					AOSessionID: id, Harness: cfg.TargetHarness, ConfigDir: targetConfigDir,
					NativeSessionID: started.ProviderConversationID, LastGenerationID: targetGeneration,
					CreatedAt: now, LastUsedAt: now,
				}
				var createErr error
				stored, _, createErr = store.CreateAgentNativeSession(ctx, native)
				if createErr != nil {
					return emptyCommit, fmt.Errorf("persist target Chat native session: %w", createErr)
				}
			}
			if err := m.advanceAgentSwitch(ctx, store, &result, domain.AgentSwitchStartingTarget, func(next *domain.AgentSwitch) {
				next.TargetNativeSessionRef = nativeSessionIDPtr(stored.ID)
			}); err != nil {
				return emptyCommit, fmt.Errorf("record target Chat native session: %w", err)
			}
			activatedAt := m.clock()
			activation := domain.AgentSwitchChatTargetActivation{
				SwitchID: result.ID, SessionID: id,
				SourceHarness: rec.Harness, SourceGenerationID: result.SourceGenerationID,
				TargetHarness: cfg.TargetHarness, TargetNativeSessionRef: stored.ID,
				TargetGenerationID:     targetGeneration,
				ProviderConversationID: started.ProviderConversationID,
				ControllerGeneration:   started.ControllerGeneration,
				ActivatedAt:            activatedAt,
			}
			activated, activationErr := m.lcm.ActivateChatAgentSwitchTarget(
				ctx,
				activation,
			)
			if activationErr != nil || !activated {
				activationCtx, cancelActivation := switchDurableContext(ctx)
				current, committed, sourceStillOwns, resolutionErr := m.resolveChatTargetActivationOutcome(
					activationCtx, store, rec, activation)
				cancelActivation()
				if committed {
					result = current
					targetOwnerCommitted = true
					return ChatControllerCommit{Conversation: committedChatSwitchConversation(
						started.Conversation, result.ID, activatedAt)}, nil
				}
				if !sourceStillOwns || resolutionErr != nil {
					targetOwnershipAmbiguous = true
					skipTerminalization = true
					cause := activationErr
					if cause == nil {
						cause = errors.New("activation CAS returned false")
					}
					return emptyCommit, fmt.Errorf("target Chat controller activation outcome is ambiguous: %w", errors.Join(cause, resolutionErr))
				}
				if activationErr != nil {
					return emptyCommit, activationErr
				}
				return emptyCommit, errors.New("target Chat controller ownership changed concurrently")
			}
			targetOwnerCommitted = true
			result.State = domain.AgentSwitchTargetReady
			result.UpdatedAt = activatedAt
			return ChatControllerCommit{Conversation: committedChatSwitchConversation(
				started.Conversation, result.ID, activatedAt)}, nil
		},
	})
	if err != nil {
		if targetOwnerCommitted {
			skipTerminalization = true
		}
		return result, fmt.Errorf("switch Chat agent %s: start target controller: %w", id, err)
	}
	if !targetOwnerCommitted {
		skipTerminalization = true
		return result, fmt.Errorf("switch Chat agent %s: target controller started without durable ownership", id)
	}
	result, err = requireAgentSwitch(ctx, store, result.ID)
	if err != nil {
		return result, fmt.Errorf("switch Chat agent %s: reload target activation: %w", id, err)
	}
	if err := m.advanceAgentSwitch(ctx, store, &result, domain.AgentSwitchDelivering, nil); err != nil {
		return result, fmt.Errorf("switch Chat agent %s: begin continuation delivery: %w", id, err)
	}
	if _, err := m.chat.RelayChatTurnWithID(
		ctx, id, aoTargetActivationPrompt, chatSwitchActivationMessageID(result.ID)); err != nil {
		return result, fmt.Errorf("switch Chat agent %s: deliver continuation: %w", id, err)
	}
	acknowledgedAt := m.clock()
	acknowledged, err := store.AcknowledgeAgentSwitchTarget(
		ctx, result.ID, id, targetGeneration, acknowledgedAt)
	if err != nil {
		return result, fmt.Errorf("switch Chat agent %s: acknowledge continuation: %w", id, err)
	}
	if !acknowledged {
		return result, fmt.Errorf("switch Chat agent %s: acknowledge continuation: durable state changed concurrently", id)
	}
	result, err = requireAgentSwitch(ctx, store, result.ID)
	if err != nil {
		return result, fmt.Errorf("switch Chat agent %s: reload continuation acknowledgement: %w", id, err)
	}
	completionCtx, cancelCompletion := switchDurableContext(ctx)
	result, err = m.completeAcknowledgedAgentSwitch(completionCtx, store, result)
	cancelCompletion()
	if err != nil {
		return result, fmt.Errorf("switch Chat agent %s: complete: %w", id, err)
	}
	return result, nil
}

func committedChatSwitchConversation(
	conversation domain.ConversationRecord,
	switchID domain.AgentSwitchID,
	activatedAt time.Time,
) domain.ConversationRecord {
	if conversation.ID == "" {
		return domain.ConversationRecord{}
	}
	conversation.ActiveBranchID = chatSwitchProviderBoundaryID(switchID)
	conversation.Settings.Model = ""
	conversation.Settings.ReasoningEffort = ""
	conversation.UpdatedAt = activatedAt
	return conversation
}

func chatSwitchProviderBoundaryID(switchID domain.AgentSwitchID) string {
	return string(switchID) + ":provider"
}

func chatSwitchActivationMessageID(switchID domain.AgentSwitchID) string {
	return "agent-switch:" + string(switchID) + ":activation"
}

func (m *Manager) abortChatAgentSwitchHandoff(rec domain.SessionRecord) {
	if domain.NormalizeSessionMode(rec.Mode) != domain.SessionModeChat {
		return
	}
	if handoff, ok := m.chat.(chatHandoffLauncher); ok {
		handoff.AbortChatHandoff(rec.ID)
	}
}

func (m *Manager) rollbackStoppedChatAgentSwitchSource(
	ctx context.Context,
	rec domain.SessionRecord,
	project domain.ProjectRecord,
) error {
	current, found, err := m.store.GetSession(ctx, rec.ID)
	if err != nil {
		return err
	}
	if !found {
		return ErrNotFound
	}
	if current.IsTerminated || current.Harness != rec.Harness ||
		domain.NormalizeSessionMode(current.Mode) != domain.SessionModeChat {
		return errors.New("chat source no longer owns the session")
	}
	sourceAgent, ok := m.agents.Agent(rec.Harness)
	if !ok {
		return ErrUnknownHarness
	}
	systemPrompt, err := m.buildSystemPrompt(ctx, rec.Kind, rec.ProjectID)
	if err != nil {
		return err
	}
	systemPromptFile, err := m.prepareSystemPromptFile(rec.ID, rec.Harness, systemPrompt)
	if err != nil {
		return err
	}
	agentConfig := effectiveAgentConfig(rec.Kind, project.Config)
	env := m.runtimeEnv(rec.ID, rec.ProjectID, rec.IssueID, project.Config.Env)
	m.augmentAgentRuntimeEnv(sourceAgent, env)
	if err := m.prepareWorkspace(
		ctx, sourceAgent, rec.ID, rec.Metadata.WorkspacePath,
		systemPrompt, systemPromptFile, agentConfig, env,
	); err != nil {
		return err
	}
	_, err = m.resumeChatController(
		ctx, "restore failed agent switch", current, project,
		workspaceInfo(current), false, "",
	)
	return err
}

// reconcileChatAgentSwitch settles a switch interrupted with its structured
// controller in memory. No Chat controller survives a daemon restart, so the
// durable controller generation replaces runtime liveness as the ownership
// proof: a source-owned row is resumed, while an activated target is restored
// with its finalized continuation before the switch gate is released.
func (m *Manager) reconcileChatAgentSwitch(
	ctx context.Context,
	store ports.AgentSwitchStore,
	rec domain.SessionRecord,
	sw domain.AgentSwitch,
) (bool, error) {
	fail := func(code domain.AgentSwitchErrorCode) (bool, error) {
		_, err := m.failAgentSwitch(ctx, store, sw, code)
		return err == nil, err
	}
	switch sw.State {
	case domain.AgentSwitchPreparingHandoff:
		return fail(domain.AgentSwitchErrorDaemonRestartPreStop)
	case domain.AgentSwitchStoppingSource:
		if rec.IsTerminated {
			return fail(domain.AgentSwitchErrorSourceSessionTerminated)
		}
		confirmed, err := m.lcm.ConfirmAgentSwitchSourceStopped(ctx, domain.AgentSwitchSourceStopConfirmation{
			SwitchID:                           sw.ID,
			SessionID:                          rec.ID,
			SourceMode:                         domain.SessionModeChat,
			SourceHarness:                      sw.FromHarness,
			SourceGenerationID:                 sw.SourceGenerationID,
			ExpectedSourceControllerGeneration: string(sw.SourceGenerationID),
			TargetGenerationID:                 sw.TargetGenerationID,
			StoppedAt:                          m.clock(),
		})
		if err != nil {
			return false, err
		}
		if !confirmed {
			return false, fmt.Errorf("reconcile Chat agent switch %s: source-stop ownership changed concurrently", sw.ID)
		}
		current, err := requireAgentSwitch(ctx, store, sw.ID)
		if err != nil {
			return false, err
		}
		return m.failRecoveredSwitchWithSourceRollback(ctx, store, rec, current)
	case domain.AgentSwitchSourceStopped:
		if err := m.cleanupRecoveredTargetWorkspace(ctx, rec, sw); err != nil {
			return false, err
		}
		return m.failRecoveredSwitchWithSourceRollback(ctx, store, rec, sw)
	case domain.AgentSwitchStartingTarget:
		if rec.Harness != sw.FromHarness {
			return false, fmt.Errorf("reconcile Chat agent switch %s: starting target has unexpected durable owner %q", sw.ID, rec.Harness)
		}
		if err := m.cleanupRecoveredTargetWorkspace(ctx, rec, sw); err != nil {
			return false, err
		}
		return m.failRecoveredSwitchWithSourceRollback(ctx, store, rec, sw)
	case domain.AgentSwitchTargetReady, domain.AgentSwitchDelivering:
		recoverable, err := m.chatTargetNativeIdentityRecoverable(ctx, store, rec, sw)
		if err != nil {
			return false, err
		}
		if !recoverable {
			return false, fmt.Errorf("reconcile Chat agent switch %s: activated target identity is not durable", sw.ID)
		}
		return m.recoverActivatedChatAgentSwitch(ctx, store, rec, sw)
	default:
		if sw.State.Terminal() {
			return true, nil
		}
		return false, fmt.Errorf("reconcile Chat agent switch %s: unknown state %q", sw.ID, sw.State)
	}
}

// recoverActivatedChatAgentSwitch reopens the exact provider conversation with
// its verified finalized continuation before the switch gate can be released.
// target_ready is safe to deliver because no turn was attempted yet. A
// delivering row is ambiguous, so it is never replayed; restoring its hidden
// context is enough to leave the durable target usable before terminalization.
func (m *Manager) recoverActivatedChatAgentSwitch(
	ctx context.Context,
	store ports.AgentSwitchStore,
	rec domain.SessionRecord,
	sw domain.AgentSwitch,
) (bool, error) {
	if m.chat == nil {
		return false, fmt.Errorf("reconcile Chat agent switch %s: %w", sw.ID, ports.ErrChatUnsupported)
	}
	if _, valid := m.readVerifiedFinalizedHandoff(ctx, sw); !valid {
		return false, fmt.Errorf("reconcile Chat agent switch %s: finalized handoff failed verification", sw.ID)
	}
	if !m.chat.HasLiveChatController(rec.ID) {
		project, err := m.loadProject(ctx, rec.ProjectID)
		if err != nil {
			return false, fmt.Errorf("reconcile Chat agent switch %s: target project: %w", sw.ID, err)
		}
		if _, err := m.resumeChatController(
			ctx, "recover Chat agent switch", rec, project, workspaceInfo(rec), false,
			string(sw.TargetGenerationID),
		); err != nil {
			return false, err
		}
	}

	current, err := requireAgentSwitch(ctx, store, sw.ID)
	if err != nil {
		return false, err
	}
	if current.State == domain.AgentSwitchDelivering {
		if current.TargetAcknowledgedAt != nil {
			if _, err := m.completeAcknowledgedAgentSwitch(ctx, store, current); err != nil {
				return false, err
			}
			return true, nil
		}
		_, err := m.failAgentSwitch(ctx, store, current, domain.AgentSwitchErrorDeliveryUnconfirmed)
		return err == nil, err
	}
	if current.State != domain.AgentSwitchTargetReady {
		return false, fmt.Errorf("reconcile Chat agent switch %s: target state changed to %q", sw.ID, current.State)
	}
	if err := m.advanceAgentSwitch(ctx, store, &current, domain.AgentSwitchDelivering, nil); err != nil {
		return false, err
	}
	if _, err := m.chat.RelayChatTurnWithID(
		ctx, rec.ID, aoTargetActivationPrompt, chatSwitchActivationMessageID(current.ID)); err != nil {
		_, failErr := m.failAgentSwitch(ctx, store, current, domain.AgentSwitchErrorDeliveryUnconfirmed)
		return failErr == nil, failErr
	}
	acknowledged, err := store.AcknowledgeAgentSwitchTarget(
		ctx, current.ID, rec.ID, current.TargetGenerationID, m.clock())
	if err != nil {
		return false, err
	}
	if !acknowledged {
		return false, fmt.Errorf("reconcile Chat agent switch %s: continuation acknowledgement changed concurrently", sw.ID)
	}
	current, err = requireAgentSwitch(ctx, store, current.ID)
	if err != nil {
		return false, err
	}
	if _, err := m.completeAcknowledgedAgentSwitch(ctx, store, current); err != nil {
		return false, err
	}
	return true, nil
}

func (m *Manager) chatTargetNativeIdentityRecoverable(
	ctx context.Context,
	store ports.AgentSwitchStore,
	rec domain.SessionRecord,
	sw domain.AgentSwitch,
) (bool, error) {
	if rec.Harness != sw.TargetHarness ||
		strings.TrimSpace(rec.Metadata.ControllerGeneration) != string(sw.TargetGenerationID) {
		return false, nil
	}
	recoverable, err := m.targetNativeIdentityRecoverable(ctx, store, rec, sw)
	if err != nil || !recoverable || sw.TargetNativeSessionRef == nil {
		return recoverable, err
	}
	native, found, err := store.GetAgentNativeSession(ctx, *sw.TargetNativeSessionRef)
	if err != nil {
		return false, err
	}
	return found && strings.TrimSpace(rec.Metadata.ProviderConversationID) != "" &&
		rec.Metadata.ProviderConversationID == native.NativeSessionID, nil
}

func (m *Manager) resolveChatTargetActivationOutcome(
	ctx context.Context,
	store ports.AgentSwitchStore,
	source domain.SessionRecord,
	activation domain.AgentSwitchChatTargetActivation,
) (domain.AgentSwitch, bool, bool, error) {
	current, found, err := store.GetAgentSwitch(ctx, activation.SwitchID)
	if err != nil {
		return domain.AgentSwitch{}, false, false, err
	}
	if !found {
		return domain.AgentSwitch{}, false, false, errors.New("chat agent switch disappeared while resolving target activation")
	}
	session, found, err := m.store.GetSession(ctx, activation.SessionID)
	if err != nil {
		return current, false, false, err
	}
	if !found {
		return current, false, false, ErrNotFound
	}
	targetRefMatches := current.TargetNativeSessionRef != nil &&
		*current.TargetNativeSessionRef == activation.TargetNativeSessionRef
	committed := current.State == domain.AgentSwitchTargetReady &&
		current.SourceGenerationID == activation.SourceGenerationID &&
		current.TargetGenerationID == activation.TargetGenerationID && targetRefMatches &&
		domain.NormalizeSessionMode(session.Mode) == domain.SessionModeChat &&
		session.Harness == activation.TargetHarness &&
		session.Metadata.ProviderConversationID == activation.ProviderConversationID &&
		session.Metadata.ControllerGeneration == activation.ControllerGeneration
	if committed {
		return current, true, false, nil
	}
	sourceStillOwns := current.State == domain.AgentSwitchStartingTarget &&
		current.SourceGenerationID == activation.SourceGenerationID &&
		current.TargetGenerationID == activation.TargetGenerationID && targetRefMatches &&
		domain.NormalizeSessionMode(session.Mode) == domain.SessionModeChat &&
		session.Harness == activation.SourceHarness &&
		session.Metadata.ProviderConversationID == source.Metadata.ProviderConversationID &&
		session.Metadata.ControllerGeneration == activation.ControllerGeneration
	return current, false, sourceStillOwns, nil
}
