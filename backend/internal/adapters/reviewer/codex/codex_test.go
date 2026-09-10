package codex

import (
	"context"
	"slices"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type captureAgent struct {
	got        ports.LaunchConfig
	gotRestore ports.RestoreConfig
}

func (a *captureAgent) GetConfigSpec(context.Context) (ports.ConfigSpec, error) {
	return ports.ConfigSpec{}, nil
}
func (a *captureAgent) GetLaunchCommand(_ context.Context, cfg ports.LaunchConfig) ([]string, error) {
	a.got = cfg
	return []string{"agent", "--", cfg.Prompt}, nil
}
func (a *captureAgent) GetPromptDeliveryStrategy(context.Context, ports.LaunchConfig) (ports.PromptDeliveryStrategy, error) {
	return ports.PromptDeliveryInCommand, nil
}
func (a *captureAgent) GetAgentHooks(context.Context, ports.WorkspaceHookConfig) error { return nil }
func (a *captureAgent) GetRestoreCommand(_ context.Context, cfg ports.RestoreConfig) ([]string, bool, error) {
	a.gotRestore = cfg
	id := cfg.Session.Metadata[ports.MetadataKeyAgentSessionID]
	if id == "" {
		return nil, false, nil
	}
	return []string{"agent", "resume", id}, true, nil
}
func (a *captureAgent) SessionInfo(context.Context, ports.SessionRef) (ports.SessionInfo, bool, error) {
	return ports.SessionInfo{}, false, nil
}

func TestReviewCommandUsesReadOnlySandbox(t *testing.T) {
	t.Setenv("AO_PORT", "3103")
	t.Setenv("AO_DATA_DIR", "/wrong/data")
	t.Setenv("AO_RUN_FILE", "/wrong/running.json")
	agent := &captureAgent{}
	r := &Reviewer{agent: agent}

	got, err := r.ReviewCommand(context.Background(), ports.ReviewInvocation{
		ReviewerID:    "review-w1",
		WorkspacePath: "/ws/w1",
		DataDir:       "/tmp/ao data",
		RunFilePath:   "/tmp/ao data/running.json",
		Prompt:        "review it",
		SystemPrompt:  "review only",
	})
	if err != nil {
		t.Fatalf("ReviewCommand: %v", err)
	}

	want := []string{
		"agent",
		"--sandbox", "read-only",
		"-c", `shell_environment_policy.set.AO_PORT="3103"`,
		"-c", `shell_environment_policy.set.AO_DATA_DIR="/tmp/ao data"`,
		"-c", `shell_environment_policy.set.AO_RUN_FILE="/tmp/ao data/running.json"`,
		"--", "review it",
	}
	if !slices.Equal(got.Argv, want) {
		t.Fatalf("argv = %#v, want %#v", got.Argv, want)
	}
	if agent.got.Permissions != ports.PermissionModeAuto {
		t.Fatalf("permissions = %q, want auto", agent.got.Permissions)
	}
	if agent.got.SystemPrompt != "review only" {
		t.Fatalf("system prompt = %q", agent.got.SystemPrompt)
	}
}

func TestReviewMessageReturnsTaskPrompt(t *testing.T) {
	got, err := (&Reviewer{}).ReviewMessage(context.Background(), ports.ReviewInvocation{Prompt: "next review"})
	if err != nil {
		t.Fatalf("ReviewMessage: %v", err)
	}
	if got != "next review" {
		t.Fatalf("message = %q", got)
	}
}

func TestReviewCancelSendsSingleEscapeInput(t *testing.T) {
	spec, err := (&Reviewer{}).ReviewCancel(context.Background())
	if err != nil {
		t.Fatalf("ReviewCancel: %v", err)
	}
	if spec.Mode != ports.ReviewCancelInput {
		t.Fatalf("cancel mode = %q, want %q", spec.Mode, ports.ReviewCancelInput)
	}
	if spec.Input != "\x1b" || len(spec.Inputs) != 0 {
		t.Fatalf("input = %q inputs = %#v, want single escape input", spec.Input, spec.Inputs)
	}
}

func TestReviewCommandUsesHiddenSystemPromptFile(t *testing.T) {
	agent := &captureAgent{}
	r := &Reviewer{agent: agent}

	if _, err := r.ReviewCommand(context.Background(), ports.ReviewInvocation{
		Prompt:           "Start the AO review task.",
		SystemPromptFile: "/ao/prompts/reviewer/system.md",
	}); err != nil {
		t.Fatalf("ReviewCommand: %v", err)
	}
	if agent.got.Prompt != "Start the AO review task." || agent.got.SystemPrompt != "" || agent.got.SystemPromptFile != "/ao/prompts/reviewer/system.md" {
		t.Fatalf("launch config = %+v", agent.got)
	}
}

func TestReviewRestoreCommandUsesNativeSessionIDAndReadOnlySandbox(t *testing.T) {
	t.Setenv("AO_PORT", "3103")
	t.Setenv("AO_DATA_DIR", "/wrong/data")
	t.Setenv("AO_RUN_FILE", "/wrong/running.json")
	agent := &captureAgent{}
	r := &Reviewer{agent: agent}

	got, ok, err := r.ReviewRestoreCommand(context.Background(), ports.ReviewInvocation{
		ReviewerID:       "review-w1",
		AgentSessionID:   "codex-native-1",
		WorkspacePath:    "/ws/w1",
		DataDir:          "/tmp/ao data",
		RunFilePath:      "/tmp/ao data/running.json",
		SystemPromptFile: "/ao/prompts/reviewer/system.md",
	})
	if err != nil {
		t.Fatalf("ReviewRestoreCommand: %v", err)
	}
	if !ok {
		t.Fatal("ReviewRestoreCommand ok = false, want true")
	}
	want := []string{
		"agent", "resume",
		"--sandbox", "read-only",
		"-c", `shell_environment_policy.set.AO_PORT="3103"`,
		"-c", `shell_environment_policy.set.AO_DATA_DIR="/tmp/ao data"`,
		"-c", `shell_environment_policy.set.AO_RUN_FILE="/tmp/ao data/running.json"`,
		"codex-native-1",
	}
	if !slices.Equal(got.Argv, want) {
		t.Fatalf("argv = %#v, want %#v", got.Argv, want)
	}
	if agent.gotRestore.Session.ID != "review-w1" || agent.gotRestore.Session.WorkspacePath != "/ws/w1" {
		t.Fatalf("restore session = %+v", agent.gotRestore.Session)
	}
	if agent.gotRestore.Session.Metadata[ports.MetadataKeyAgentSessionID] != "codex-native-1" {
		t.Fatalf("restore metadata = %#v", agent.gotRestore.Session.Metadata)
	}
	if agent.gotRestore.Permissions != ports.PermissionModeAuto {
		t.Fatalf("restore permissions = %q, want auto", agent.gotRestore.Permissions)
	}
}
