package sessionmanager

import (
	"context"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestRunResearchUsesProjectWorkspaceAndIndependentSettings(t *testing.T) {
	launcher := &recordingLauncher{}
	m, st, _ := newChatManager(launcher)
	project := st.projects["mer"]
	project.Config.Env = map[string]string{"REPO_SETTING": "configured"}
	st.projects["mer"] = project
	st.sessions["mer-orch"] = domain.SessionRecord{
		ID: "mer-orch", ProjectID: "mer", Kind: domain.KindOrchestrator, Harness: domain.HarnessCodex,
		Metadata: domain.SessionMetadata{WorkspacePath: t.TempDir(), Model: "orchestrator-model"},
	}
	config := domain.ResearcherConfig{
		Enabled: true, Harness: domain.HarnessClaudeCode,
		AgentConfig: domain.AgentConfig{Model: "research-model", Mode: "plan", Permissions: domain.PermissionModeDefault},
	}
	result, err := m.RunResearch(context.Background(), "mer-orch", "run-1", " Find the entry point ", config,
		func(context.Context, ports.ChatEvent) (ports.ChatDecision, error) { return ports.ChatDecision{}, nil })
	if err != nil || result != "Generated title" {
		t.Fatalf("RunResearch = %q, %v", result, err)
	}
	cfg := launcher.background[0]
	if launcher.backgroundHarnesses[0] != config.Harness || cfg.SessionID != "research-run-1" ||
		cfg.WorkspacePath != st.sessions["mer-orch"].Metadata.WorkspacePath || cfg.Model != config.AgentConfig.Model ||
		cfg.Mode != "plan" || cfg.Permissions != domain.PermissionModeDefault || cfg.ReadOnly || cfg.OnApproval == nil {
		t.Fatalf("research config = %+v", cfg)
	}
	if cfg.Env["REPO_SETTING"] != "configured" || cfg.Env[EnvSessionID] != "" || launcher.backgroundPrompts[0] != "Find the entry point" {
		t.Fatalf("research did not receive project environment and prompt: %+v", cfg)
	}
	parent := st.sessions["mer-orch"]
	parent.Kind = domain.KindWorker
	st.sessions[parent.ID] = parent
	if _, err := m.RunResearch(context.Background(), parent.ID, "run-2", "question", config, nil); err == nil {
		t.Fatal("worker must not start research as an orchestrator")
	}
}
