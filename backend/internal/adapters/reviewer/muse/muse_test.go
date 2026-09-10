package muse

import (
	"context"
	"slices"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
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
	return []string{"muse", "--trust-workspace", "--approval-mode", "never", cfg.Prompt}, nil
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
	return []string{"muse", "--trust-workspace", "--approval-mode", "never", "resume", id}, true, nil
}
func (a *captureAgent) SessionInfo(context.Context, ports.SessionRef) (ports.SessionInfo, bool, error) {
	return ports.SessionInfo{}, false, nil
}

func TestHarness(t *testing.T) {
	if got := (&Reviewer{}).Harness(); got != domain.ReviewerMuse {
		t.Fatalf("Harness = %q, want muse", got)
	}
}

func TestReviewCommandUsesMuseNoWriteSandbox(t *testing.T) {
	agent := &captureAgent{}
	r := &Reviewer{agent: agent}

	got, err := r.ReviewCommand(context.Background(), ports.ReviewInvocation{
		ReviewerID:       "review-w1",
		WorkspacePath:    "/ws/w1",
		Prompt:           "Read the AO review task.",
		Config:           ports.AgentConfig{Mode: "plan"},
		SystemPromptFile: "/ao/prompts/reviewer/system.md",
		DataDir:          "/ao/data",
	})
	if err != nil {
		t.Fatalf("ReviewCommand: %v", err)
	}
	want := []string{"muse", "--trust-workspace", "--approval-mode", "never", "--disable-write", "Read the AO review task."}
	if !slices.Equal(got.Argv, want) {
		t.Fatalf("argv = %#v, want %#v", got.Argv, want)
	}
	if agent.got.SessionID != "review-w1" || agent.got.WorkspacePath != "/ws/w1" {
		t.Fatalf("launch config = %+v", agent.got)
	}
	if agent.got.Permissions != ports.PermissionModeAuto {
		t.Fatalf("permissions = %q, want auto", agent.got.Permissions)
	}
	if agent.got.Config.Mode != "plan" {
		t.Fatalf("launch config mode = %q, want plan", agent.got.Config.Mode)
	}
	if agent.got.SystemPromptFile != "/ao/prompts/reviewer/system.md" {
		t.Fatalf("system prompt file = %q", agent.got.SystemPromptFile)
	}
	if agent.got.DataDir != "/ao/data" {
		t.Fatalf("data dir = %q, want /ao/data", agent.got.DataDir)
	}
}

func TestReviewRestoreCommandUsesNativeSessionIDAndNoWriteSandbox(t *testing.T) {
	agent := &captureAgent{}
	r := &Reviewer{agent: agent}

	got, ok, err := r.ReviewRestoreCommand(context.Background(), ports.ReviewInvocation{
		ReviewerID:       "review-w1",
		AgentSessionID:   "muse-native-1",
		WorkspacePath:    "/ws/w1",
		SystemPromptFile: "/ao/prompts/reviewer/system.md",
		DataDir:          "/ao/data",
	})
	if err != nil {
		t.Fatalf("ReviewRestoreCommand: %v", err)
	}
	if !ok {
		t.Fatal("ReviewRestoreCommand ok = false, want true")
	}
	want := []string{"muse", "--trust-workspace", "--approval-mode", "never", "--disable-write", "resume", "muse-native-1"}
	if !slices.Equal(got.Argv, want) {
		t.Fatalf("argv = %#v, want %#v", got.Argv, want)
	}
	if agent.gotRestore.Session.ID != "review-w1" || agent.gotRestore.Session.WorkspacePath != "/ws/w1" {
		t.Fatalf("restore session = %+v", agent.gotRestore.Session)
	}
	if agent.gotRestore.Session.Metadata[ports.MetadataKeyAgentSessionID] != "muse-native-1" {
		t.Fatalf("restore metadata = %#v", agent.gotRestore.Session.Metadata)
	}
	if agent.gotRestore.Permissions != ports.PermissionModeAuto {
		t.Fatalf("restore permissions = %q, want auto", agent.gotRestore.Permissions)
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
