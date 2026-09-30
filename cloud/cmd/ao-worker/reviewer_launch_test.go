package main

import (
	"testing"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/aoagents/agent-orchestrator/cloud/internal/worker"
)

func TestReviewerLaunchUsesIndependentDurableSettings(t *testing.T) {
	base := worker.LaunchContext{SessionID: "worker", Kind: "worker", Harness: "codex", Model: "worker-model", AgentSessionID: "worker-thread", ParentSessionID: "parent", Prompt: "worker task", SystemPrompt: "worker instructions", Mode: "trusted"}
	launch, err := reviewerLaunch(base, worker.TerminalCommand{
		ReviewRunID: "00000000-0000-0000-0000-000000000001",
		Reviewer:    &domain.ProjectReviewer{Harness: "claude-code", AgentConfig: domain.ProjectAgentConfig{Model: "reviewer-model", Effort: "high", Permissions: "auto"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if launch.Harness != "claude-code" || launch.Model != "reviewer-model" || launch.AgentConfig.Effort != "high" || launch.AgentConfig.Permissions != "auto" || launch.Kind != "reviewer" || launch.SessionID == base.SessionID || launch.AgentSessionID != "" || launch.ParentSessionID != "" || launch.Prompt != "" || launch.SystemPrompt != "" {
		t.Fatalf("reviewer launch = %+v", launch)
	}
	if base.Harness != "codex" || base.Model != "worker-model" || base.AgentSessionID != "worker-thread" {
		t.Fatal("reviewer launch changed the worker context")
	}
}
