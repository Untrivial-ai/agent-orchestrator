package claudecode

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// captureAgent is a stub ports.Agent that records the LaunchConfig the reviewer
// builds, so the test asserts the reviewer's tool policy without needing the
// real claude binary on PATH.
type captureAgent struct {
	got        ports.LaunchConfig
	gotRestore ports.RestoreConfig
	hooks      []ports.WorkspaceHookConfig
	prelaunch  []ports.LaunchConfig
}

func (a *captureAgent) GetConfigSpec(context.Context) (ports.ConfigSpec, error) {
	return ports.ConfigSpec{}, nil
}
func (a *captureAgent) GetLaunchCommand(_ context.Context, cfg ports.LaunchConfig) ([]string, error) {
	a.got = cfg
	return []string{"claude"}, nil
}
func (a *captureAgent) GetPromptDeliveryStrategy(context.Context, ports.LaunchConfig) (ports.PromptDeliveryStrategy, error) {
	return ports.PromptDeliveryInCommand, nil
}
func (a *captureAgent) GetAgentHooks(_ context.Context, cfg ports.WorkspaceHookConfig) error {
	a.hooks = append(a.hooks, cfg)
	return nil
}
func (a *captureAgent) PreLaunch(_ context.Context, cfg ports.LaunchConfig) error {
	a.prelaunch = append(a.prelaunch, cfg)
	return nil
}
func (a *captureAgent) GetRestoreCommand(_ context.Context, cfg ports.RestoreConfig) ([]string, bool, error) {
	a.gotRestore = cfg
	id := cfg.Session.Metadata[ports.MetadataKeyAgentSessionID]
	if id == "" {
		id = cfg.Session.ID
	}
	if id == "" {
		return nil, false, nil
	}
	return []string{"claude", "--resume", id}, true, nil
}
func (a *captureAgent) SessionInfo(context.Context, ports.SessionRef) (ports.SessionInfo, bool, error) {
	return ports.SessionInfo{}, false, nil
}

func TestReviewCommandLaunchesReadOnlyWithoutInteractivePrompts(t *testing.T) {
	agent := &captureAgent{}
	r := &Reviewer{agent: agent}

	spec, err := r.ReviewCommand(context.Background(), ports.ReviewInvocation{
		ReviewerID:    "review-w1",
		WorkspacePath: "/ws/w1",
		Prompt:        "review it",
		SystemPrompt:  "you are a reviewer",
	})
	if err != nil {
		t.Fatalf("ReviewCommand: %v", err)
	}

	// The allowlist is what enforces read-only, so it must launch in an
	// explicit non-bypass mode: bypassPermissions ignores allow/deny rules
	// entirely, and an empty mode would defer to a user's defaultMode.
	if agent.got.Permissions != ports.PermissionModeDenyUnapproved {
		t.Fatalf("reviewer must deny unapproved tools without prompting; got %q", agent.got.Permissions)
	}
	if agent.got.SessionID == "" {
		t.Fatal("reviewer must pin the persisted Claude session id")
	}
	if spec.AgentSessionID != agent.got.SessionID {
		t.Fatalf("persisted agent session id = %q, launched session id = %q", spec.AgentSessionID, agent.got.SessionID)
	}
	if !contains(agent.got.AllowedTools, "Read") || !contains(agent.got.AllowedTools, "Bash(ao review submit:*)") || !contains(agent.got.AllowedTools, "Bash(base64 --decode:*)") {
		t.Fatalf("allowlist missing read-only review tools: %#v", agent.got.AllowedTools)
	}
	if contains(agent.got.AllowedTools, "Bash(gh:*)") {
		t.Fatalf("allowlist grants blanket gh access: %#v", agent.got.AllowedTools)
	}
	if !contains(agent.got.AllowedTools, "Bash(gh api --method POST repos/:*)") {
		t.Fatalf("allowlist missing exact GitHub review-post path: %#v", agent.got.AllowedTools)
	}
	for _, denied := range []string{"Edit", "Write", "Bash(git push:*)", "Bash(git commit:*)", "Bash(gh pr merge:*)"} {
		if !contains(agent.got.DisallowedTools, denied) {
			t.Fatalf("disallow list missing %q: %#v", denied, agent.got.DisallowedTools)
		}
	}
}

func TestPreLaunchInstallsSelectedReviewerHooksAndTrustsWorkspace(t *testing.T) {
	agent := &captureAgent{}
	r := &Reviewer{agent: agent}

	if err := r.PreLaunch(context.Background(), ports.ReviewInvocation{
		ReviewerID:    "review-w1",
		WorkspacePath: "/ws/w1",
	}); err != nil {
		t.Fatalf("PreLaunch: %v", err)
	}
	if len(agent.hooks) != 1 || agent.hooks[0].WorkspacePath != "/ws/w1" {
		t.Fatalf("hooks = %#v, want selected reviewer hook install for /ws/w1", agent.hooks)
	}
	if len(agent.prelaunch) != 1 || agent.prelaunch[0].WorkspacePath != "/ws/w1" || agent.prelaunch[0].SessionID == "" {
		t.Fatalf("prelaunch = %#v, want trusted workspace with pinned session id", agent.prelaunch)
	}
}

func TestAllowlistCoversPromptRequiredPipedCommands(t *testing.T) {
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

	if !contains(agent.got.AllowedTools, "Bash(printf:*)") {
		t.Fatalf("allowlist missing printf for piped review commands: %#v", agent.got.AllowedTools)
	}

	for _, cmd := range []string{
		"printf '%s' 'eyJldmVudCI6IkNPTU1FTlQifQ==' | base64 --decode | gh api --method POST repos/o/r/pulls/1/reviews --input - --jq '.id'",
		"printf '%s' 'eyJyZXZpZXdzIjpbXX0=' | base64 --decode | ao review submit --session sess-1 --reviews -",
	} {
		if !compoundCommandCovered(agent.got.AllowedTools, cmd) {
			t.Fatalf("allowlist does not cover prompt-required command %q with tools %#v", cmd, agent.got.AllowedTools)
		}
	}

	disallowed := "printf x | rm -rf /"
	if compoundCommandCovered(agent.got.AllowedTools, disallowed) {
		t.Fatalf("allowlist unexpectedly covers disallowed command %q with tools %#v", disallowed, agent.got.AllowedTools)
	}
}

func TestReviewMessagesRequireAnalyzerSafeBase64Reporting(t *testing.T) {
	agent := &captureAgent{}
	r := &Reviewer{agent: agent}

	if _, err := r.ReviewCommand(context.Background(), ports.ReviewInvocation{
		ReviewerID: "review-w1",
		Prompt:     "Read `/ao/task.md`.",
	}); err != nil {
		t.Fatalf("ReviewCommand: %v", err)
	}
	for _, want := range []string{"base64-encode", "base64 --decode", "Never place raw review JSON"} {
		if !strings.Contains(agent.got.Prompt, want) {
			t.Fatalf("launch prompt missing %q: %q", want, agent.got.Prompt)
		}
	}

	message, err := r.ReviewMessage(context.Background(), ports.ReviewInvocation{Prompt: "Read `/ao/next.md`."})
	if err != nil {
		t.Fatalf("ReviewMessage: %v", err)
	}
	if !strings.Contains(message, "base64 --decode") {
		t.Fatalf("follow-up prompt lacks analyzer-safe reporting rule: %q", message)
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
	if !strings.HasPrefix(agent.got.Prompt, "Start the AO review task.") || !strings.Contains(agent.got.Prompt, "base64 --decode") || agent.got.SystemPrompt != "" || agent.got.SystemPromptFile != "/ao/prompts/reviewer/system.md" {
		t.Fatalf("launch config = %+v", agent.got)
	}
}

func TestReviewRestoreCommandUsesNativeSessionIDAndReadOnlyPolicy(t *testing.T) {
	agent := &captureAgent{}
	r := &Reviewer{agent: agent}

	got, ok, err := r.ReviewRestoreCommand(context.Background(), ports.ReviewInvocation{
		ReviewerID:       "review-w1",
		AgentSessionID:   "claude-native-1",
		WorkspacePath:    "/ws/w1",
		SystemPromptFile: "/ao/prompts/reviewer/system.md",
	})
	if err != nil {
		t.Fatalf("ReviewRestoreCommand: %v", err)
	}
	if !ok {
		t.Fatal("ReviewRestoreCommand ok = false, want true")
	}
	if strings.Join(got.Argv, " ") != "claude --resume claude-native-1" {
		t.Fatalf("argv = %#v", got.Argv)
	}
	if agent.gotRestore.Session.Metadata[ports.MetadataKeyAgentSessionID] != "claude-native-1" {
		t.Fatalf("restore metadata = %#v", agent.gotRestore.Session.Metadata)
	}
	if agent.gotRestore.Permissions != ports.PermissionModeDenyUnapproved {
		t.Fatalf("restore permissions = %q, want deny-unapproved", agent.gotRestore.Permissions)
	}
	if !contains(agent.gotRestore.AllowedTools, "Read") || !contains(agent.gotRestore.DisallowedTools, "Write") {
		t.Fatalf("restore tool policy allowed=%#v disallowed=%#v", agent.gotRestore.AllowedTools, agent.gotRestore.DisallowedTools)
	}
}

func TestReviewRestoreCommandAllowsAdapterFallbackWithoutNativeSessionID(t *testing.T) {
	agent := &captureAgent{}
	r := &Reviewer{agent: agent}

	got, ok, err := r.ReviewRestoreCommand(context.Background(), ports.ReviewInvocation{
		ReviewerID:       "review-w1",
		WorkspacePath:    "/ws/w1",
		SystemPromptFile: "/ao/prompts/reviewer/system.md",
	})
	if err != nil {
		t.Fatalf("ReviewRestoreCommand: %v", err)
	}
	if !ok {
		t.Fatal("ReviewRestoreCommand ok = false, want true")
	}
	if strings.Join(got.Argv, " ") != "claude --resume review-w1" {
		t.Fatalf("argv = %#v", got.Argv)
	}
	if agent.gotRestore.Session.ID != "review-w1" {
		t.Fatalf("restore session id = %q, want review-w1", agent.gotRestore.Session.ID)
	}
	if _, ok := agent.gotRestore.Session.Metadata[ports.MetadataKeyAgentSessionID]; ok {
		t.Fatalf("restore metadata should not invent native id: %#v", agent.gotRestore.Session.Metadata)
	}
}

func TestReviewCancelSendsDoubleEscapeInput(t *testing.T) {
	spec, err := (&Reviewer{}).ReviewCancel(context.Background())
	if err != nil {
		t.Fatalf("ReviewCancel: %v", err)
	}
	if spec.Mode != ports.ReviewCancelInput {
		t.Fatalf("cancel mode = %q, want %q", spec.Mode, ports.ReviewCancelInput)
	}
	if len(spec.Inputs) != 2 || spec.Inputs[0] != "\x1b" || spec.Inputs[1] != "\x1b" {
		t.Fatalf("inputs = %#v, want double escape", spec.Inputs)
	}
	if spec.InputDelay != 150*time.Millisecond {
		t.Fatalf("input delay = %s, want 150ms", spec.InputDelay)
	}
}

func compoundCommandCovered(allowedTools []string, cmd string) bool {
	for _, segment := range splitPipedCommand(cmd) {
		if !bashSegmentCovered(allowedTools, segment) {
			return false
		}
	}
	return true
}

func splitPipedCommand(cmd string) []string {
	rawSegments := strings.Split(cmd, "|")
	segments := make([]string, 0, len(rawSegments))
	for _, segment := range rawSegments {
		if trimmed := strings.TrimSpace(segment); trimmed != "" {
			segments = append(segments, trimmed)
		}
	}
	return segments
}

func bashSegmentCovered(allowedTools []string, segment string) bool {
	for _, tool := range allowedTools {
		cmd, ok := strings.CutPrefix(tool, "Bash(")
		if !ok {
			continue
		}
		cmd, ok = strings.CutSuffix(cmd, ":*)")
		if !ok {
			continue
		}
		if strings.HasPrefix(segment, cmd) {
			return true
		}
	}
	return false
}

func contains(values []string, needle string) bool {
	for _, v := range values {
		if v == needle {
			return true
		}
	}
	return false
}
