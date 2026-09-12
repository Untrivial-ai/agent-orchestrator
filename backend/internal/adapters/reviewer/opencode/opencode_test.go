package opencode

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

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
	return []string{"agent", "--prompt", cfg.Prompt}, nil
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
	return []string{"agent", "--session", id}, true, nil
}
func (a *captureAgent) SessionInfo(context.Context, ports.SessionRef) (ports.SessionInfo, bool, error) {
	return ports.SessionInfo{}, false, nil
}

func TestReviewCommandUsesReadOnlyPermissionPolicy(t *testing.T) {
	agent := &captureAgent{}
	r := &Reviewer{agent: agent}

	got, err := r.ReviewCommand(context.Background(), ports.ReviewInvocation{
		ReviewerID:    "review-w1",
		WorkspacePath: "/ws/w1",
		Prompt:        "review it",
		SystemPrompt:  "review only",
	})
	if err != nil {
		t.Fatalf("ReviewCommand: %v", err)
	}

	if agent.got.Prompt != "review only\n\nreview it" {
		t.Fatalf("prompt = %q", agent.got.Prompt)
	}
	if agent.got.Permissions != ports.PermissionModeAuto {
		t.Fatalf("permissions = %q, want auto", agent.got.Permissions)
	}
	config := map[string]any{}
	if err := json.Unmarshal([]byte(got.Env["OPENCODE_CONFIG_CONTENT"]), &config); err != nil {
		t.Fatalf("inline config is invalid JSON: %v", err)
	}
	permission := config["permission"].(map[string]any)
	if permission["*"] != "deny" || permission["read"] != "allow" {
		t.Fatalf("permission policy = %#v", permission)
	}
	bash := permission["bash"].(map[string]any)
	if bash["*"] != "deny" || bash["gh api *"] != "allow" || bash["ao review submit *"] != "allow" {
		t.Fatalf("bash policy = %#v", bash)
	}
}

func TestReviewCommandKeepsSystemPromptFileOutOfVisiblePrompt(t *testing.T) {
	agent := &captureAgent{}
	r := &Reviewer{agent: agent}
	taskPromptRoot := filepath.Join("ao", "prompts", "reviewer")
	taskPromptFile := filepath.Join(taskPromptRoot, "requests", "batch-1", "run-1", "task.md")

	got, err := r.ReviewCommand(context.Background(), ports.ReviewInvocation{
		Prompt:           "Start the AO review task.",
		SystemPromptFile: "/ao/prompts/reviewer/system.md",
		TaskPromptFile:   taskPromptFile,
		TaskPromptRoot:   taskPromptRoot,
	})
	if err != nil {
		t.Fatalf("ReviewCommand: %v", err)
	}
	if agent.got.Prompt != "Start the AO review task." || agent.got.SystemPrompt != "" || agent.got.SystemPromptFile != "/ao/prompts/reviewer/system.md" {
		t.Fatalf("launch config = %+v", agent.got)
	}
	var config struct {
		Permission struct {
			ExternalDirectory map[string]string `json:"external_directory"`
		} `json:"permission"`
	}
	if err := json.Unmarshal([]byte(got.Env["OPENCODE_CONFIG_CONTENT"]), &config); err != nil {
		t.Fatalf("reviewer config: %v", err)
	}
	wantPattern := filepath.ToSlash(taskPromptRoot) + "/**"
	if len(config.Permission.ExternalDirectory) != 1 || config.Permission.ExternalDirectory[wantPattern] != "allow" {
		t.Fatalf("external directory policy = %#v, want only %q allowed", config.Permission.ExternalDirectory, wantPattern)
	}
}

func TestReviewRestoreCommandUsesNativeSessionIDAndReadOnlyPolicy(t *testing.T) {
	agent := &captureAgent{}
	r := &Reviewer{agent: agent}

	got, ok, err := r.ReviewRestoreCommand(context.Background(), ports.ReviewInvocation{
		ReviewerID:       "review-w1",
		AgentSessionID:   "opencode-native-1",
		WorkspacePath:    "/ws/w1",
		TaskPromptRoot:   filepath.Join("ao", "prompts", "reviewer"),
		SystemPromptFile: "/ao/prompts/reviewer/system.md",
	})
	if err != nil {
		t.Fatalf("ReviewRestoreCommand: %v", err)
	}
	if !ok {
		t.Fatal("ReviewRestoreCommand ok = false, want true")
	}
	if !got.NativeResumed {
		t.Fatal("ReviewRestoreCommand did not report native resume")
	}
	if strings.Join(got.Argv, " ") != "agent --session opencode-native-1" {
		t.Fatalf("argv = %#v", got.Argv)
	}
	if agent.gotRestore.Session.Metadata[ports.MetadataKeyAgentSessionID] != "opencode-native-1" {
		t.Fatalf("restore metadata = %#v", agent.gotRestore.Session.Metadata)
	}
	if agent.gotRestore.Permissions != ports.PermissionModeAuto {
		t.Fatalf("restore permissions = %q, want auto", agent.gotRestore.Permissions)
	}
	var config map[string]any
	if err := json.Unmarshal([]byte(got.Env["OPENCODE_CONFIG_CONTENT"]), &config); err != nil {
		t.Fatalf("reviewer config: %v", err)
	}
	permission := config["permission"].(map[string]any)
	if permission["*"] != "deny" || permission["read"] != "allow" {
		t.Fatalf("permission policy = %#v", permission)
	}
}

func TestBuildReviewerConfigLeavesOtherExternalPathsDenied(t *testing.T) {
	configText, err := buildReviewerConfig("")
	if err != nil {
		t.Fatalf("buildReviewerConfig: %v", err)
	}
	var config struct {
		Permission map[string]json.RawMessage `json:"permission"`
	}
	if err := json.Unmarshal([]byte(configText), &config); err != nil {
		t.Fatalf("reviewer config: %v", err)
	}
	if _, ok := config.Permission["external_directory"]; ok {
		t.Fatalf("unexpected external-directory exception: %s", config.Permission["external_directory"])
	}
	var catchAll string
	if err := json.Unmarshal(config.Permission["*"], &catchAll); err != nil || catchAll != "deny" {
		t.Fatalf("catch-all permission = %q, err = %v", catchAll, err)
	}
}

func TestReviewCommandBuildsBothOpenCodeConfigSources(t *testing.T) {
	binDir := t.TempDir()
	binaryName := "opencode"
	binaryBody := "#!/bin/sh\n"
	if runtime.GOOS == "windows" {
		binaryName = "opencode.cmd"
		binaryBody = "@echo off\r\n"
	}
	if err := os.WriteFile(filepath.Join(binDir, binaryName), []byte(binaryBody), 0o755); err != nil {
		t.Fatalf("write fake opencode: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	promptDir := t.TempDir()
	systemPath := filepath.Join(promptDir, "system.md")
	taskPath := filepath.Join(promptDir, "requests", "batch-1", "run-1", "task.md")
	if err := os.MkdirAll(filepath.Dir(taskPath), 0o700); err != nil {
		t.Fatalf("create task prompt dir: %v", err)
	}
	if err := os.WriteFile(systemPath, []byte("review system prompt\n"), 0o600); err != nil {
		t.Fatalf("write system prompt: %v", err)
	}
	if err := os.WriteFile(taskPath, []byte("review task\n"), 0o600); err != nil {
		t.Fatalf("write task prompt: %v", err)
	}

	spec, err := New().ReviewCommand(context.Background(), ports.ReviewInvocation{
		ReviewerID:       "review-w1",
		WorkspacePath:    t.TempDir(),
		Prompt:           "Read the AO review task.",
		SystemPromptFile: systemPath,
		TaskPromptFile:   taskPath,
		TaskPromptRoot:   promptDir,
	})
	if err != nil {
		t.Fatalf("ReviewCommand: %v", err)
	}
	configPath := filepath.Join(promptDir, "opencode.json")
	joinedArgv := strings.Join(spec.Argv, "\n")
	for _, want := range []string{
		"OPENCODE_CONFIG=" + configPath,
		"--agent\nao-review-w1",
		"--prompt\nRead the AO review task.",
	} {
		if !strings.Contains(joinedArgv, want) {
			t.Fatalf("argv missing %q: %#v", want, spec.Argv)
		}
	}
	generated, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read generated opencode config: %v", err)
	}
	if !strings.Contains(string(generated), `"prompt": "{file:./system.md}"`) {
		t.Fatalf("generated prompt config = %s", generated)
	}
	if !strings.Contains(spec.Env["OPENCODE_CONFIG_CONTENT"], `"external_directory"`) ||
		!strings.Contains(spec.Env["OPENCODE_CONFIG_CONTENT"], `"permission"`) {
		t.Fatalf("inline reviewer config = %s", spec.Env["OPENCODE_CONFIG_CONTENT"])
	}
}

func TestBashAllowlistCoversPromptRequiredCommands(t *testing.T) {
	bash := reviewerConfigBashPolicy(t)

	tests := []struct {
		name    string
		command string
		allowed bool
	}{
		{
			name:    "github review creation",
			command: `printf '%s' '{ "event": "COMMENT", "body": "x" }' | gh api --method POST repos/o/r/pulls/1/reviews --input - --jq '.id'`,
			allowed: true,
		},
		{
			name:    "local review submit",
			command: `printf '%s' '{ "reviews": [] }' | ao review submit --session sess-1 --reviews -`,
			allowed: true,
		},
		{
			name:    "arbitrary shell command",
			command: `rm -rf /`,
			allowed: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := bashAllowsCommand(t, bash, tt.command); got != tt.allowed {
				t.Fatalf("bashAllowsCommand(%q) = %v, want %v", tt.command, got, tt.allowed)
			}
		})
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

func reviewerConfigBashPolicy(t *testing.T) map[string]string {
	t.Helper()
	configText, err := buildReviewerConfig("")
	if err != nil {
		t.Fatalf("buildReviewerConfig: %v", err)
	}

	var config struct {
		Permission struct {
			Bash map[string]string `json:"bash"`
		} `json:"permission"`
	}
	if err := json.Unmarshal([]byte(configText), &config); err != nil {
		t.Fatalf("reviewer config is invalid JSON: %v", err)
	}
	if len(config.Permission.Bash) == 0 {
		t.Fatal("reviewerConfig permission.bash is empty")
	}
	return config.Permission.Bash
}

func bashAllowsCommand(t *testing.T, bash map[string]string, command string) bool {
	t.Helper()

	for pattern, action := range bash {
		if action == "deny" {
			continue
		}
		if simplePicomatchGlobMatches(t, pattern, command) {
			return true
		}
	}
	return false
}

func simplePicomatchGlobMatches(t *testing.T, pattern, command string) bool {
	t.Helper()

	parts := strings.Split(pattern, "*")
	for i, part := range parts {
		parts[i] = regexp.QuoteMeta(part)
	}
	re, err := regexp.Compile("^" + strings.Join(parts, ".*") + "$")
	if err != nil {
		t.Fatalf("compile pattern %q: %v", pattern, err)
	}
	return re.MatchString(command)
}
