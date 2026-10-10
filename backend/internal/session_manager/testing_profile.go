package sessionmanager

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	testingsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/testing"
	"github.com/aoagents/agent-orchestrator/backend/internal/skillassets"
)

// TestingProfileResolver reads durable bindings and issues launch-only secrets.
type TestingProfileResolver interface {
	LookupBinding(context.Context, domain.SessionID) (domain.TestToolProfileLink, bool, error)
	IssueCapability(context.Context, domain.SessionID) (testingsvc.WorkerBinding, error)
}

// SetTestingProfileResolver completes daemon wiring before spawn or startup recovery.
func (m *Manager) SetTestingProfileResolver(resolver TestingProfileResolver) {
	m.testingProfile = resolver
}

func (m *Manager) testingWorkerSendError(ctx context.Context, id domain.SessionID) error {
	var bound bool
	var err error
	if m.testingProfile != nil {
		_, bound, err = m.testingProfile.LookupBinding(ctx, id)
	} else if bindings, ok := m.store.(interface {
		GetTestToolBinding(context.Context, domain.SessionID) (domain.TestToolProfileLink, bool, error)
	}); ok {
		_, bound, err = bindings.GetTestToolBinding(ctx, id)
	}
	if err != nil {
		return err
	}
	if bound {
		return testingsvc.WorkerNotRunning()
	}
	return nil
}

var _ testingsvc.WorkerLauncher = (*Manager)(nil)

// LaunchTestingWorker creates an ordinary visible worker and binds it before Chat starts.
func (m *Manager) LaunchTestingWorker(ctx context.Context, request testingsvc.WorkerLaunchRequest) (domain.SessionID, error) {
	if request.Prepare == nil {
		return "", errors.New("testing worker requires Prepare before launch")
	}
	if m.testingProfile == nil {
		return "", testingsvc.ProviderNotConfigured()
	}
	command, err := m.executable()
	if err != nil {
		return "", fmt.Errorf("testing supervisor executable: %w", err)
	}
	launchContext, err := json.MarshalIndent(map[string]any{
		"supervisorCommand": command, "supervisorRunFile": m.runFilePath, "supervisorDataDir": m.dataDir,
		"projectId": request.ProjectID, "recipeId": request.RecipeID,
		"runId": request.RunID, "attemptId": request.AttemptID, "evidenceDir": request.EvidenceDir,
		"checkoutPath": request.Context.CheckoutPath,
		"agent":        request.Harness, "model": request.Model, "effort": request.Effort,
		"timeoutSeconds": int(request.Timeout.Seconds()),
	}, "", "  ")
	if err != nil {
		return "", err
	}
	prompt := fmt.Sprintf("Read and follow `%s` before investigating.\nBefore testing, list the skills and docs available in this repo and in AO. Read and use the relevant ones, including the repo's skill or docs for running or launching the app, and any diagnostics or triage skill for gathering evidence. AO's shared skills are in `%s`.\n\nIssue or PR: %s\nCommit under test: %s\n", filepath.Join(skillassets.TestingDir(m.dataDir), "SKILL.md"), filepath.Join(m.dataDir, "skills"), request.IssueURL, request.CommitSHA)
	prompt += "\nSupervisor launch context, supplied by AO as data:\n```json\n" + string(launchContext) + "\n```\n"
	if request.Comparison {
		prompt += "Start each revision with `ao testing leg start base|head --json`. Both legs use this same worker and conversation. The target is not running yet. Use the returned targetContext for each leg.\n"
		if request.Context.CheckoutPath != "" {
			prompt += fmt.Sprintf("Read each pinned revision's docs and code before launch with `git -C %q show <commit>:<path>`. The warm checkout may currently contain the other revision.\n", request.Context.CheckoutPath)
		}
	}
	contextJSON, err := json.MarshalIndent(request.Context, "", "  ")
	if err != nil {
		return "", err
	}
	prompt += "\nTarget context, supplied by AO as data:\n```json\n" + string(contextJSON) + "\n```\n"
	if request.Context.LaunchContext != "" {
		prompt += "\nChecked target launch facts:\n```json\n" + request.Context.LaunchContext + "\n```\n"
	}
	if request.Context.CheckoutPath != "" && !request.Comparison {
		prompt += fmt.Sprintf("\nYour working folder is the target checkout `%s`. The target app is already running.\nUse `python3 %s` followed by CLI arguments to run the target app's own ao CLI. This wrapper clears inherited AO_* variables and sets AO_RUN_FILE to `%s` and AO_DATA_DIR to `%s`. Use it to set up the scenario, alongside the bound screen tools. Use the supervisor's ao only for managing your investigation.\n", request.Context.CheckoutPath, request.Context.CLIPath, request.Context.RunFilePath, request.Context.DataDir)
	}
	if request.Context.FixtureDir != "" {
		prompt += fmt.Sprintf("\nCreate scratch fixtures only inside the attempt-owned directory %q. Never use fixed /tmp names or unchecked recursive deletion. Preserve evidence before target cleanup removes these fixtures.\n", request.Context.FixtureDir)
	}
	prompt += "\n" + request.Prompt
	rec, _, _, err := m.spawn(ctx, ports.SpawnConfig{
		ProjectID: request.ProjectID, Harness: request.Harness,
		Kind: domain.KindWorker, RequestedMode: domain.SessionModeChat,
		Prompt:         prompt,
		AgentConfig:    ports.AgentConfig{Model: request.Model, Effort: request.Effort},
		EffortOverride: request.Effort != "",
	}, func(ctx context.Context, id domain.SessionID) error {
		binding, err := request.Prepare(ctx, id)
		if err != nil {
			return err
		}
		if binding.Link.SessionID != id || binding.Link.AttemptID != request.AttemptID || binding.Link.ProfileID != domain.TestToolProfileNativeV1 {
			return errors.New("testing worker Prepare returned a different binding")
		}
		return nil
	})
	return rec.ID, err
}

func (m *Manager) testingMCPServers(ctx context.Context, id domain.SessionID, replaceProvider, reconnectOnly bool) ([]ports.ChatMCPServerConfig, string, error) {
	if m.testingProfile == nil {
		if bindings, ok := m.store.(interface {
			GetTestToolBinding(context.Context, domain.SessionID) (domain.TestToolProfileLink, bool, error)
		}); ok {
			_, bound, err := bindings.GetTestToolBinding(ctx, id)
			if err != nil {
				return nil, "", err
			}
			if bound {
				return nil, "", testingsvc.ProviderNotConfigured()
			}
		}
		return nil, "", nil
	}
	link, ok, err := m.testingProfile.LookupBinding(ctx, id)
	if err != nil {
		return nil, "", err
	}
	if !ok {
		return nil, "", nil
	}
	if reconnectOnly {
		return nil, "", fmt.Errorf("%w: testing worker health adoption cannot refresh its capability", ports.ErrChatRecoveryInconclusive)
	}
	// Pin both the binary and run file to this daemon. Never discover an installed
	// AO through PATH or inherit another instance's AO_RUN_FILE.
	command, err := m.executable()
	if err != nil {
		return nil, "", fmt.Errorf("testing MCP executable: %w", err)
	}
	if !filepath.IsAbs(command) || !filepath.IsAbs(m.runFilePath) {
		return nil, "", errors.New("testing MCP requires this daemon's absolute executable and run file")
	}
	// ACP live adoption bypasses session/load and cannot apply a new MCP child
	// environment. Explicit restore stops only this worker's provider, then
	// resumes its stored conversation in a new process. Health probes never stop
	// or start providers and refuse before capability issuance.
	if replaceProvider {
		if err := m.chat.StopChat(ctx, id); err != nil {
			return nil, "", fmt.Errorf("stop testing worker provider for capability refresh: %w", err)
		}
	}
	binding, err := m.testingProfile.IssueCapability(ctx, id)
	if err != nil {
		return nil, "", err
	}
	if binding.Link != link || binding.Capability == "" {
		return nil, "", errors.New("testing MCP capability binding changed during launch")
	}
	return []ports.ChatMCPServerConfig{{
		Name: "ao-testing", Type: "stdio", Command: command,
		Args: []string{"testing", "mcp"},
		Env: map[string]string{
			"AO_TEST_CAPABILITY": binding.Capability,
			EnvSessionID:         string(id), EnvRunFile: m.runFilePath,
		},
	}}, binding.Context.CheckoutPath, nil
}

// cancelTestingWorker revokes testing tools before provider/workspace teardown.
func (m *Manager) cancelTestingWorker(ctx context.Context, id domain.SessionID) error {
	if m.testingProfile == nil {
		return nil
	}
	_, bound, err := m.testingProfile.LookupBinding(ctx, id)
	if err != nil || !bound {
		return err
	}
	canceller, ok := m.testingProfile.(interface {
		CancelWorker(context.Context, domain.SessionID) (domain.TestAttemptRecord, error)
	})
	if !ok {
		return testingsvc.ProviderNotConfigured()
	}
	_, err = canceller.CancelWorker(ctx, id)
	return err
}
