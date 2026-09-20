package roleprompt

import (
	"strings"
	"testing"
)

func TestBuildOrchestratorAddsProjectContextAndRules(t *testing.T) {
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
		"## Project Context",
		"Name: Mercury",
		"## Project-Specific Orchestrator Rules",
		"Prefer two focused workers.",
		"## Publishing Scope",
		"## Standing-instruction confidentiality",
		"Repository: https://github.com/acme/mercury",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("orchestrator prompt missing %q:\n%s", want, prompt)
		}
	}
	for _, roleCommand := range []string{"ao spawn", "ao send", "ao kill"} {
		if strings.Contains(prompt, roleCommand) {
			t.Fatalf("project supplement duplicates role command %q:\n%s", roleCommand, prompt)
		}
	}
}

func TestBuildWorkerAddsProjectRules(t *testing.T) {
	prompt := Build(Config{
		Role:          RoleWorker,
		ProjectID:     "project-1",
		RepositoryURL: "https://github.com/acme/mercury",
		AgentRules:    "Run the contract tests.",
	})
	for _, want := range []string{
		"## Project Context",
		"## Project Rules",
		"Run the contract tests.",
		"## Publishing Scope",
		"## Standing-instruction confidentiality",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("worker prompt missing %q:\n%s", want, prompt)
		}
	}
	if strings.Contains(prompt, "ao claim-pr") {
		t.Fatalf("project supplement must not duplicate built-in role commands:\n%s", prompt)
	}
}

func TestBuildUnknownRoleReturnsEmpty(t *testing.T) {
	if prompt := Build(Config{Role: "reviewer"}); prompt != "" {
		t.Fatalf("unknown role prompt = %q, want empty", prompt)
	}
}
