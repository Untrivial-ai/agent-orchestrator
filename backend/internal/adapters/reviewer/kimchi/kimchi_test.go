package kimchi

import (
	"context"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// captureAgent is a stub ports.Agent that records the configs the reviewer
// builds without needing the real kimchi binary on PATH.
type captureAgent struct {
	got        ports.LaunchConfig
	gotRestore ports.RestoreConfig
	hooks      []ports.WorkspaceHookConfig
}

func (a *captureAgent) GetConfigSpec(context.Context) (ports.ConfigSpec, error) {
	return ports.ConfigSpec{}, nil
}
func (a *captureAgent) GetLaunchCommand(_ context.Context, cfg ports.LaunchConfig) ([]string, error) {
	a.got = cfg
	return []string{"kimchi"}, nil
}
func (a *captureAgent) GetPromptDeliveryStrategy(context.Context, ports.LaunchConfig) (ports.PromptDeliveryStrategy, error) {
	return ports.PromptDeliveryInCommand, nil
}
func (a *captureAgent) GetAgentHooks(_ context.Context, cfg ports.WorkspaceHookConfig) error {
	a.hooks = append(a.hooks, cfg)
	return nil
}
func (a *captureAgent) GetRestoreCommand(_ context.Context, cfg ports.RestoreConfig) ([]string, bool, error) {
	a.gotRestore = cfg
	id := cfg.Session.Metadata[ports.MetadataKeyAgentSessionID]
	if id == "" {
		return nil, false, nil
	}
	return []string{"kimchi", "--session", id}, true, nil
}
func (a *captureAgent) SessionInfo(context.Context, ports.SessionRef) (ports.SessionInfo, bool, error) {
	return ports.SessionInfo{}, false, nil
}

func TestReviewCommandAppliesBestEffortPolicyOffBypass(t *testing.T) {
	agent := &captureAgent{}
	r := &Reviewer{agent: agent}

	if _, err := r.ReviewCommand(context.Background(), ports.ReviewInvocation{
		ReviewerID:    "review-w1",
		WorkspacePath: "/ws/w1",
		Prompt:        "review it",
		Config:        ports.AgentConfig{Model: "sonnet-5"},
		SystemPrompt:  "you are a reviewer",
	}); err != nil {
		t.Fatalf("ReviewCommand: %v", err)
	}

	// The policy is only useful in an explicit non-bypass mode: --yolo ignores
	// allow/deny rules, and an empty mode would defer to Kimchi's default.
	if agent.got.Permissions != ports.PermissionModeAuto {
		t.Fatalf("reviewer must launch in auto permission mode; got %q", agent.got.Permissions)
	}
	if agent.got.Config.Model != "sonnet-5" {
		t.Fatalf("launch config model = %q, want sonnet-5", agent.got.Config.Model)
	}
	if !contains(agent.got.AllowedTools, "read") || !contains(agent.got.AllowedTools, "bash(ao review submit:*)") {
		t.Fatalf("allowlist missing read-only review tools: %#v", agent.got.AllowedTools)
	}
	for _, denied := range []string{
		"edit",
		"write",
		"bash(git push:*)",
		"bash(git commit:*)",
		"bash(git show:*)",
		"bash(gh pr merge:*)",
		"bash(gh api --method DELETE:*)",
		"bash(gh api --method PUT:*)",
		"bash(gh api --method PATCH:*)",
		"bash(gh gist:*)",
	} {
		if !contains(agent.got.DisallowedTools, denied) {
			t.Fatalf("disallow list missing %q: %#v", denied, agent.got.DisallowedTools)
		}
	}
}

func TestPreLaunchInstallsKimchiHooksInReviewWorkspace(t *testing.T) {
	agent := &captureAgent{}
	r := &Reviewer{agent: agent}

	if err := r.PreLaunch(context.Background(), ports.ReviewInvocation{WorkspacePath: "/ws/w1"}); err != nil {
		t.Fatalf("PreLaunch: %v", err)
	}
	if len(agent.hooks) != 1 || agent.hooks[0].WorkspacePath != "/ws/w1" {
		t.Fatalf("hooks = %#v, want Kimchi hook install for /ws/w1", agent.hooks)
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

func TestAllowlistIncludesProtocolToolsAndDeniesDangerousGh(t *testing.T) {
	agent := &captureAgent{}
	r := &Reviewer{agent: agent}

	if _, err := r.ReviewCommand(context.Background(), ports.ReviewInvocation{
		ReviewerID:    "review-w1",
		WorkspacePath: "/ws/w1",
		Prompt:        "review it",
		SystemPrompt:  "you are a reviewer",
	}); err != nil {
		t.Fatalf("ReviewCommand: %v", err)
	}

	// printf and gh must be in the allow list — the review protocol
	// (prompt.go step 1) requires a piped printf | gh api command.
	for _, tool := range []string{"bash(printf:*)", "bash(gh:*)"} {
		if !contains(agent.got.AllowedTools, tool) {
			t.Fatalf("allowlist missing protocol tool %q: %#v", tool, agent.got.AllowedTools)
		}
	}

	// The reviewer can still submit verdicts via ao review submit.
	if !contains(agent.got.AllowedTools, "bash(ao review submit:*)") {
		t.Fatalf("allowlist missing ao review submit: %#v", agent.got.AllowedTools)
	}

	// git show must NOT be in the allow list — it can read arbitrary tracked
	// content like .env.production.
	if contains(agent.got.AllowedTools, "bash(git show:*)") {
		t.Fatalf("allowlist unexpectedly contains bash(git show:*): %#v", agent.got.AllowedTools)
	}

	// git show must be in the deny list as defense in depth.
	if !contains(agent.got.DisallowedTools, "bash(git show:*)") {
		t.Fatalf("disallow list missing bash(git show:*): %#v", agent.got.DisallowedTools)
	}

	// Dangerous gh verbs must be denied as defense in depth.
	for _, denied := range []string{
		"bash(gh pr merge:*)",
		"bash(gh api --method DELETE:*)",
		"bash(gh api --method PUT:*)",
		"bash(gh api --method PATCH:*)",
		"bash(gh gist:*)",
	} {
		if !contains(agent.got.DisallowedTools, denied) {
			t.Fatalf("disallow list missing %q: %#v", denied, agent.got.DisallowedTools)
		}
	}

	// The blanket bash(gh:*) deny must NOT be present — it blocks the
	// protocol's gh api --method POST call.
	if contains(agent.got.DisallowedTools, "bash(gh:*)") {
		t.Fatalf("disallow list must not contain blanket bash(gh:*): %#v", agent.got.DisallowedTools)
	}
}

func TestReviewRestoreCommandUsesNativeSessionAndReappliesPolicy(t *testing.T) {
	agent := &captureAgent{}
	r := &Reviewer{agent: agent}

	spec, ok, err := r.ReviewRestoreCommand(context.Background(), ports.ReviewInvocation{
		ReviewerID:       "review-w1",
		AgentSessionID:   "kimchi-native-1",
		WorkspacePath:    "/ws/w1",
		SystemPromptFile: "/ao/prompts/reviewer/system.md",
	})
	if err != nil {
		t.Fatalf("ReviewRestoreCommand: %v", err)
	}
	if !ok {
		t.Fatal("ReviewRestoreCommand ok = false, want true")
	}
	if strings.Join(spec.Argv, " ") != "kimchi --session kimchi-native-1" {
		t.Fatalf("argv = %#v", spec.Argv)
	}
	if spec.AgentSessionID != "kimchi-native-1" {
		t.Fatalf("AgentSessionID = %q, want kimchi-native-1", spec.AgentSessionID)
	}
	if agent.gotRestore.Session.Metadata[ports.MetadataKeyAgentSessionID] != "kimchi-native-1" {
		t.Fatalf("restore metadata = %#v", agent.gotRestore.Session.Metadata)
	}
	if agent.gotRestore.Permissions != ports.PermissionModeAuto {
		t.Fatalf("restore permissions = %q, want auto", agent.gotRestore.Permissions)
	}
	if !contains(agent.gotRestore.AllowedTools, "read") || !contains(agent.gotRestore.DisallowedTools, "write") {
		t.Fatalf("restore tool policy allowed=%#v disallowed=%#v", agent.gotRestore.AllowedTools, agent.gotRestore.DisallowedTools)
	}
}

func TestReviewRestoreCommandWithoutNativeSessionFallsBack(t *testing.T) {
	agent := &captureAgent{}
	r := &Reviewer{agent: agent}

	_, ok, err := r.ReviewRestoreCommand(context.Background(), ports.ReviewInvocation{ReviewerID: "review-w1"})
	if err != nil {
		t.Fatalf("ReviewRestoreCommand: %v", err)
	}
	if ok {
		t.Fatal("ReviewRestoreCommand ok = true without a native Kimchi session id")
	}
}

func TestHostTrustWarningNamesBoundary(t *testing.T) {
	for _, phrase := range []string{"host-trusted", "not OS isolation", "shell commands", "GitHub access"} {
		if !strings.Contains(HostTrustWarning, phrase) {
			t.Fatalf("HostTrustWarning %q is missing %q", HostTrustWarning, phrase)
		}
	}
}

func contains(values []string, needle string) bool {
	for _, v := range values {
		if v == needle {
			return true
		}
	}
	return false
}
