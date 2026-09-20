package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// restartProviderForPermissions replaces the live provider process so launch-time
// approval flags (Cursor --auto-review / --force) match the durable next-turn
// setting. The AO session and conversation stay put; Cursor's session/load path
// recovers provider context when available.
func (s *Service) restartProviderForPermissions(
	ctx context.Context,
	id domain.SessionID,
	settings domain.ConversationSettings,
) error {
	gate := s.controllerGate(id)
	if err := gate.lock(ctx); err != nil {
		return err
	}
	defer gate.unlock()

	source, err := s.Controller(id)
	if err != nil {
		return err
	}
	cfg, driver, err := s.branchLaunchConfig(id, source)
	if err != nil {
		return err
	}
	cfg.Permissions = settings.ApprovalMode
	cfg.Model = settings.Model
	cfg.Effort = settings.ReasoningEffort

	if err := source.BeginIdleBranchHandoff(ctx); err != nil {
		return err
	}
	abortSource := true
	defer func() {
		if abortSource {
			source.AbortHandoff()
		}
	}()

	operationCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), nativeEditHandoffLimit)
	defer cancel()

	if err := s.recordPermissionRestartActivity(operationCtx, source, settings.ApprovalMode); err != nil {
		return err
	}

	activeBranch, err := s.store.ConversationBranch(
		operationCtx, source.conversation.ID, source.conversation.ActiveBranchID)
	if err != nil {
		return fmt.Errorf("load active conversation branch: %w", err)
	}
	providerConversationID := source.ProviderConversationID()
	if providerConversationID == "" {
		providerConversationID = activeBranch.ProviderConversationID
	}
	if providerConversationID == "" {
		return fmt.Errorf("%w: no provider conversation to resume after permission restart",
			ports.ErrChatPermissionRestartRequired)
	}

	if err := source.closeForBranchHandoff(operationCtx); err != nil {
		closeErr := fmt.Errorf("close provider before permission restart: %w", err)
		if source.State() == ports.ChatControllerStopped {
			if restoreErr := s.restoreClosedSourceController(
				operationCtx, id, source, activeBranch, cfg, driver); restoreErr != nil {
				closeErr = errors.Join(closeErr, restoreErr)
			} else {
				abortSource = false
			}
		}
		return closeErr
	}

	launchEnv, err := s.prepareBranchControllerEnv(operationCtx, cfg)
	if err != nil {
		if restoreErr := s.restoreClosedSourceController(
			operationCtx, id, source, activeBranch, cfg, driver); restoreErr != nil {
			err = errors.Join(err, restoreErr)
		} else {
			abortSource = false
		}
		return err
	}

	provider, err := driver.Resume(operationCtx, ports.ChatResumeConfig{
		SessionID: cfg.SessionID, ProviderConversationID: providerConversationID,
		DataDir: cfg.DataDir, WorkspacePath: cfg.WorkspacePath, Env: launchEnv,
		Model: cfg.Model, Effort: cfg.Effort,
		Permissions: cfg.Permissions, SystemPrompt: cfg.SystemPrompt,
		ProviderScopeID:       activeBranch.ProviderScopeID,
		ProviderIDsScoped:     activeBranch.ProviderIDsScoped,
		AdditionalDirectories: cfg.AdditionalDirectories, MCPServers: cfg.MCPServers,
	})
	if err != nil {
		resumeErr := fmt.Errorf("%w: resume after permission restart: %w",
			ports.ErrChatPermissionRestartRequired, err)
		if restoreErr := s.restoreClosedSourceController(
			operationCtx, id, source, activeBranch, cfg, driver); restoreErr != nil {
			resumeErr = errors.Join(resumeErr, restoreErr)
		} else {
			abortSource = false
		}
		return resumeErr
	}

	generation := s.newID()
	conversation := source.conversation
	conversation.Settings = settings
	replacement := newController(id, conversation, generation, source.harness, provider, s.store, s.activity, s.log, s.newID, s.now, s.onAccountChanged, s.onCodexCapacityChanged)
	if err := s.store.ActivateConversationBranch(operationCtx, id, conversation.ID, activeBranch.ID,
		replacement.ProviderConversationID(), generation, s.now()); err != nil {
		_ = cleanupUnpublishedConversation(provider, true)
		activateErr := err
		if restoreErr := s.restoreClosedSourceController(
			operationCtx, id, source, activeBranch, cfg, driver); restoreErr != nil {
			activateErr = errors.Join(activateErr, restoreErr)
		} else {
			abortSource = false
		}
		return activateErr
	}
	s.mu.Lock()
	if stored, ok := s.startConfigs[id]; ok {
		stored.Permissions = cfg.Permissions
		stored.Model = cfg.Model
		stored.Effort = cfg.Effort
		s.startConfigs[id] = stored
	}
	s.mu.Unlock()
	if err := s.installBranchController(operationCtx, id, source, replacement, activeBranch.ID); err != nil {
		return err
	}
	abortSource = false
	return nil
}

func (s *Service) recordPermissionRestartActivity(
	ctx context.Context,
	controller *Controller,
	mode domain.PermissionMode,
) error {
	detail, err := json.Marshal(map[string]string{
		"event":  "permissions.restart",
		"mode":   string(ports.NormalizePermissionMode(mode)),
		"reason": "process_launch_flags",
	})
	if err != nil {
		return fmt.Errorf("encode permission restart activity: %w", err)
	}
	now := s.now()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	return s.store.UpsertActivity(ctx, controller.conversation.ID, "",
		domain.ConversationActivity{
			ID:             s.newID(),
			Kind:           domain.ActivityKindSystem,
			Status:         domain.ActivityStatusCompleted,
			Summary:        "Restarting with new permissions",
			Detail:         detail,
			ProviderItemID: "ao-permission-restart-" + string(ports.NormalizePermissionMode(mode)),
		}, now)
}
