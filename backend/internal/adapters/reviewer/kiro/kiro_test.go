package kiro

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestReviewCommandBuildsReadOnlyInteractiveTUI(t *testing.T) {
	promptRoot := t.TempDir()
	systemPath := filepath.Join(promptRoot, "system.md")
	if err := os.WriteFile(systemPath, []byte("AO reviewer role\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := &Reviewer{resolveBinary: func(context.Context) (string, error) { return "kiro-cli", nil }}

	spec, err := r.ReviewCommand(context.Background(), ports.ReviewInvocation{
		WorkerSessionID:  "worker-1",
		WorkspacePath:    "/worktrees/worker-1",
		Prompt:           "Read and follow /ao/task.md",
		SystemPromptFile: systemPath,
		TaskPromptRoot:   promptRoot,
	})
	if err != nil {
		t.Fatalf("ReviewCommand: %v", err)
	}
	if want := []string{"kiro-cli", "chat", "--agent", reviewerAgentName}; !slices.Equal(spec.Argv, want) {
		t.Fatalf("argv = %#v, want %#v", spec.Argv, want)
	}
	joined := strings.Join(spec.Argv, " ")
	for _, forbidden := range []string{"--no-interactive", "--trust-all-tools", "--trust-tools", "--json", "--print", "Read and follow"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("interactive argv contains %q: %#v", forbidden, spec.Argv)
		}
	}
	if spec.InitialMessage != "Read and follow /ao/task.md" {
		t.Fatalf("initial message = %q", spec.InitialMessage)
	}
	if spec.WorkingDirectory == "" || spec.WorkingDirectory == "/worktrees/worker-1" || !strings.HasPrefix(spec.WorkingDirectory, promptRoot) {
		t.Fatalf("working directory = %q, want AO-owned directory", spec.WorkingDirectory)
	}

	data, err := os.ReadFile(filepath.Join(spec.WorkingDirectory, ".kiro", "agents", reviewerAgentName+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		MCPServers     map[string]any `json:"mcpServers"`
		IncludeMCPJSON bool           `json:"includeMcpJson"`
		Resources      []any          `json:"resources"`
		Tools          []string       `json:"tools"`
		AllowedTools   []string       `json:"allowedTools"`
		ToolsSettings  struct {
			Shell struct {
				AllowedCommands []string `json:"allowedCommands"`
				DeniedCommands  []string `json:"deniedCommands"`
				DenyByDefault   bool     `json:"denyByDefault"`
			} `json:"shell"`
		} `json:"toolsSettings"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("agent config: %v", err)
	}
	if cfg.IncludeMCPJSON || len(cfg.MCPServers) != 0 || len(cfg.Resources) != 0 {
		t.Fatalf("project-controlled integrations enabled: %s", data)
	}
	if !slices.Equal(cfg.Tools, []string{"read", "glob", "grep", "shell"}) || slices.Contains(cfg.AllowedTools, "shell") {
		t.Fatalf("tool policy = tools %#v allowed %#v", cfg.Tools, cfg.AllowedTools)
	}
	if !cfg.ToolsSettings.Shell.DenyByDefault || len(cfg.ToolsSettings.Shell.AllowedCommands) != 5 || len(cfg.ToolsSettings.Shell.DeniedCommands) == 0 {
		t.Fatalf("shell policy = %+v", cfg.ToolsSettings.Shell)
	}
	for _, command := range cfg.ToolsSettings.Shell.AllowedCommands {
		if !strings.HasPrefix(command, "^") || !strings.HasSuffix(command, "$") {
			t.Fatalf("shell allowance is not a full-command match %q", command)
		}
		if strings.Contains(command, "git commit") || strings.Contains(command, "git push") || command == "^.*$" {
			t.Fatalf("unsafe shell allowance %q", command)
		}
	}
	systemPrompt, err := os.ReadFile(filepath.Join(spec.WorkingDirectory, "system.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, phrase := range []string{"supported git shell shapes are exactly", "whitespace-free", "intentionally unsupported"} {
		if !strings.Contains(string(systemPrompt), phrase) {
			t.Fatalf("Kiro shell policy does not document %q:\n%s", phrase, systemPrompt)
		}
	}
}

func TestReviewCommandPreflightDoesNotCreateConfig(t *testing.T) {
	r := &Reviewer{resolveBinary: func(context.Context) (string, error) { return "kiro-cli", nil }}
	spec, err := r.ReviewCommand(context.Background(), ports.ReviewInvocation{WorkspacePath: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if spec.WorkingDirectory != "" || spec.InitialMessage != "" {
		t.Fatalf("preflight spec = %+v", spec)
	}
}

func TestReviewMessageAndCancelUseInteractiveTUI(t *testing.T) {
	r := &Reviewer{}
	msg, err := r.ReviewMessage(context.Background(), ports.ReviewInvocation{Prompt: "next task file"})
	if err != nil || msg != "next task file" {
		t.Fatalf("ReviewMessage = %q, %v", msg, err)
	}
	cancel, err := r.ReviewCancel(context.Background())
	if err != nil || cancel.Mode != ports.ReviewCancelInput || cancel.Input != "\x1b" {
		t.Fatalf("ReviewCancel = %+v, %v", cancel, err)
	}
}
