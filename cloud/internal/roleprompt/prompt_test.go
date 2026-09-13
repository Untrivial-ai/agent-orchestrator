package roleprompt

import (
	"strings"
	"testing"
)

func TestBuildOrchestratorUsesCloudRoleAndCommands(t *testing.T) {
	prompt := Build(Config{
		Role:              RoleOrchestrator,
		ProjectID:         "project-1",
		ProjectName:       "Mercury",
		RepositoryURL:     "https://github.com/acme/mercury",
		DefaultBranch:     "main",
		WorkspacePath:     "/workspace/repository",
		OrchestratorRules: "Prefer two focused workers.",
	})
	for _, want := range []string{
		"## AO Cloud Orchestrator Role",
		"coordinate work, not to perform implementation",
		"ao spawn --name",
		"ao list",
		"Prefer two focused workers.",
		"## Publishing Scope",
		"## Standing-instruction confidentiality",
		"Repository: https://github.com/acme/mercury",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("orchestrator prompt missing %q:\n%s", want, prompt)
		}
	}
	for _, desktopOnly := range []string{"ao session ls", "ao session kill", "--project project-1"} {
		if strings.Contains(prompt, desktopOnly) {
			t.Fatalf("orchestrator prompt contains desktop-only command %q:\n%s", desktopOnly, prompt)
		}
	}
}

func TestBuildWorkerUsesWorkerRoleAndRules(t *testing.T) {
	prompt := Build(Config{
		Role:          RoleWorker,
		ProjectID:     "project-1",
		RepositoryURL: "https://github.com/acme/mercury",
		AgentRules:    "Run the contract tests.",
	})
	for _, want := range []string{
		"## AO Cloud Worker Role",
		"implementation worker",
		"ao claim-pr <number-or-url>",
		"## Project Rules",
		"Run the contract tests.",
		"## Publishing Scope",
		"## Standing-instruction confidentiality",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("worker prompt missing %q:\n%s", want, prompt)
		}
	}
	if strings.Contains(prompt, "ao spawn") {
		t.Fatalf("worker prompt must not grant orchestrator commands:\n%s", prompt)
	}
}

func TestBuildUnknownRoleReturnsEmpty(t *testing.T) {
	if prompt := Build(Config{Role: "reviewer"}); prompt != "" {
		t.Fatalf("unknown role prompt = %q, want empty", prompt)
	}
}
