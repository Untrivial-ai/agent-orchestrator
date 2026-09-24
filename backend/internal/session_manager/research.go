package sessionmanager

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const researchSystemPrompt = `You are an Agent Orchestrator researcher. Investigate the supplied question in the current repository. Find relevant files, cite precise paths and line numbers, explain call flows and existing helpers, and distinguish evidence from uncertainty. Return a concise Markdown report. Do not edit files, create commits, send messages, or delegate to another agent.`

type researchHostStopper interface {
	StopBackgroundTask(context.Context, domain.AgentHarness, string, domain.SessionID) error
}

// SupportsResearch reports whether the selected harness supports background chat.
func (m *Manager) SupportsResearch(harness domain.AgentHarness) bool {
	return m.chat != nil && m.chat.SupportsChat(harness)
}

// StopResearchOrphan shuts down a provider host left by an interrupted run.
func (m *Manager) StopResearchOrphan(ctx context.Context, rec domain.ResearchRun) error {
	stopper, ok := m.chat.(researchHostStopper)
	if !ok {
		return ports.ErrChatUnsupported
	}
	return stopper.StopBackgroundTask(ctx, rec.Harness, m.dataDir, domain.SessionID("research-"+rec.ID))
}

// RunResearch executes one configured investigation in the requesting
// orchestrator's workspace. It does not create an AO worker session.
func (m *Manager) RunResearch(ctx context.Context, parentID domain.SessionID, runID, prompt string, config domain.ResearcherConfig, onApproval func(context.Context, ports.ChatEvent) (ports.ChatDecision, error)) (string, error) {
	runner, ok := m.chat.(chatBackgroundTaskRunner)
	if !ok || m.chat == nil || !m.chat.SupportsChat(config.Harness) {
		return "", ports.ErrChatUnsupported
	}
	rec, err := m.getRecord(ctx, parentID)
	if err != nil {
		return "", err
	}
	if rec.IsTerminated || rec.Kind != domain.KindOrchestrator || rec.ProjectID == "" {
		return "", errors.New("research requires an active project orchestrator")
	}
	project, err := m.loadProject(ctx, rec.ProjectID)
	if err != nil {
		return "", err
	}
	if project.Kind.WithDefault() != domain.ProjectKindSingleRepo {
		return "", errors.New("research currently supports single-repository projects")
	}
	workspace := rec.Metadata.WorkspacePath
	if !filepath.IsAbs(workspace) {
		return "", fmt.Errorf("research workspace path is not absolute: %q", workspace)
	}
	if config.Harness != domain.HarnessCodex && config.AgentConfig.Effort != "" {
		return "", fmt.Errorf("research effort is unsupported by %s", config.Harness)
	}
	if err := m.validateResearchModel(ctx, rec.ProjectID, config); err != nil {
		return "", err
	}
	releaseHarness, err := m.beginHarnessUse(config.Harness)
	if err != nil {
		return "", err
	}
	defer releaseHarness()
	releaseCodex, err := m.acquireCodexControllerAdmission(ctx, config.Harness)
	if err != nil {
		return "", err
	}
	defer releaseCodex()
	env := m.runtimeEnv(rec.ID, rec.ProjectID, rec.IssueID, project.Config.Env)
	if m.agents != nil {
		if agent, found := m.agents.Agent(config.Harness); found {
			m.augmentAgentRuntimeEnv(agent, env)
		}
	}
	deleteProtectedEnv(env, EnvSessionID, envKeysCaseInsensitive)
	permissions := config.AgentConfig.Permissions
	if permissions == "" {
		permissions = domain.PermissionModeAuto
	}
	pinRuntimePermissionEnv(env, permissions)
	return runner.RunBackgroundTask(ctx, config.Harness, ports.ChatStartConfig{
		SessionID:     domain.SessionID("research-" + runID),
		DataDir:       m.dataDir,
		WorkspacePath: workspace,
		Env:           env,
		Model:         config.AgentConfig.Model,
		Effort:        config.AgentConfig.Effort,
		Mode:          config.AgentConfig.Mode,
		Permissions:   permissions,
		SystemPrompt:  researchSystemPrompt,
		OnApproval:    onApproval,
	}, strings.TrimSpace(prompt))
}

func (m *Manager) validateResearchModel(ctx context.Context, projectID domain.ProjectID, config domain.ResearcherConfig) error {
	if config.Harness != domain.HarnessCodex || m.modelCatalog == nil {
		if config.AgentConfig.Effort != "" {
			return ports.ErrModelCapabilitiesUnavailable
		}
		return nil
	}
	catalog, err := m.modelCatalog.Models(ctx, string(config.Harness), string(projectID), true)
	if err != nil {
		if config.AgentConfig.Model != "" || config.AgentConfig.Effort != "" {
			return fmt.Errorf("%w: %w", ports.ErrModelCapabilitiesUnavailable, err)
		}
		return nil
	}
	modelID := config.AgentConfig.Model
	if modelID == "" {
		for _, item := range catalog.Models {
			if item.IsDefault {
				modelID = item.ID
				break
			}
		}
	}
	for _, item := range catalog.Models {
		if item.ID == modelID {
			if config.AgentConfig.Effort != "" && (catalog.Stale || !containsString(item.Efforts, config.AgentConfig.Effort)) {
				return fmt.Errorf("%w %q for model %q", ports.ErrUnsupportedEffort, config.AgentConfig.Effort, modelID)
			}
			return nil
		}
	}
	if config.AgentConfig.Model != "" || config.AgentConfig.Effort != "" {
		return fmt.Errorf("%w for model %q", ports.ErrModelCapabilitiesUnavailable, modelID)
	}
	return nil
}
