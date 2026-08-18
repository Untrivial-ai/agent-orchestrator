package kiro

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/activitystate"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/authprobe"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// TestKiroPreToolUseReportsActiveNotWaiting is a regression guard for the
// sticky-waiting_input misclassification: Kiro's preToolUse hook fires before
// every tool invocation (and never at all under auto/bypass approval modes), so
// it must report the agent as actively working, not blocked on the user.
func TestKiroPreToolUseReportsActiveNotWaiting(t *testing.T) {
	sub := ""
	for _, spec := range kiroManagedHooks {
		if spec.Event == "preToolUse" {
			sub = strings.TrimPrefix(spec.Command, kiroHookCommandPrefix)
		}
	}
	if sub == "" {
		t.Fatal("kiro installs no preToolUse hook")
	}
	state, ok := activitystate.StandardDeriveActivityState(sub, nil)
	if !ok {
		t.Fatalf("preToolUse sub-command %q derives no activity", sub)
	}
	if state == domain.ActivityWaitingInput {
		t.Fatalf("preToolUse maps to %q; a tool invocation must not pin the session to the sticky waiting_input state", state)
	}
	if state != domain.ActivityActive {
		t.Fatalf("preToolUse derives %q, want %q (the agent is working)", state, domain.ActivityActive)
	}
}

func TestManifestIDIsKiro(t *testing.T) {
	m := (&Plugin{}).Manifest()
	if m.ID != "kiro" {
		t.Fatalf("manifest ID = %q, want %q", m.ID, "kiro")
	}
	if m.Name != "Kiro" {
		t.Fatalf("manifest Name = %q, want %q", m.Name, "Kiro")
	}
	if len(m.Capabilities) != 1 || m.Capabilities[0] != adapters.CapabilityAgent {
		t.Fatalf("manifest Capabilities = %#v, want [CapabilityAgent]", m.Capabilities)
	}
}

func TestGetLaunchCommandBuildsInteractiveArgv(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "kiro-cli"}

	cmd, err := plugin.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		Permissions: ports.PermissionModeBypassPermissions,
		Kind:        domain.KindWorker,
		Prompt:      "-fix this",
	})
	if err != nil {
		t.Fatal(err)
	}

	want := []string{
		"kiro-cli", "chat",
		"--agent", "ao",
		"--trust-all-tools",
	}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("unexpected command\nwant: %#v\n got: %#v", want, cmd)
	}
}

func TestGetLaunchCommandOrchestratorUsesInteractiveAgent(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "kiro-cli"}

	cmd, err := plugin.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		Kind:         domain.KindOrchestrator,
		SystemPrompt: "You are the human-facing coordinator.",
	})
	if err != nil {
		t.Fatal(err)
	}

	want := []string{
		"kiro-cli", "chat",
		"--agent", "ao",
	}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("unexpected command\nwant: %#v\n got: %#v", want, cmd)
	}
}

func TestGetLaunchCommandPromptlessWorkerStaysInteractive(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "kiro-cli"}

	cmd, err := plugin.GetLaunchCommand(context.Background(), ports.LaunchConfig{})
	if err != nil {
		t.Fatal(err)
	}

	want := []string{
		"kiro-cli", "chat",
		"--agent", "ao",
	}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("unexpected command\nwant: %#v\n got: %#v", want, cmd)
	}
}

func TestGetLaunchCommandPromptedWorkerKeepsPromptOutOfArgv(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "kiro-cli"}

	cmd, err := plugin.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		Kind:         domain.KindWorker,
		Prompt:       "fix the failing test",
		SystemPrompt: "standing role instructions",
	})
	if err != nil {
		t.Fatal(err)
	}

	want := []string{
		"kiro-cli", "chat",
		"--agent", "ao",
	}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("unexpected command\nwant: %#v\n got: %#v", want, cmd)
	}
}

func TestGetLaunchCommandPromptedOrchestratorCarriesPrompt(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "kiro-cli"}

	cmd, err := plugin.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		Kind:   domain.KindOrchestrator,
		Prompt: "do the explicit task",
	})
	if err != nil {
		t.Fatal(err)
	}

	want := []string{
		"kiro-cli", "chat",
		"--agent", "ao",
		"--", "do the explicit task",
	}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("unexpected command\nwant: %#v\n got: %#v", want, cmd)
	}
}

func TestGetLaunchCommandSelectsPreparedCustomAgentForSystemPrompt(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "kiro-cli"}
	workspace := t.TempDir()

	cmd, err := plugin.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		Permissions:      ports.PermissionModeBypassPermissions,
		Prompt:           "-fix this",
		SystemPrompt:     "follow AO rules",
		SystemPromptFile: filepath.Join(t.TempDir(), "system.md"),
		WorkspacePath:    workspace,
	})
	if err != nil {
		t.Fatal(err)
	}

	want := []string{
		"kiro-cli", "chat",
		"--agent", "ao",
		"--trust-all-tools",
	}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("unexpected command\nwant: %#v\n got: %#v", want, cmd)
	}
	if _, err := os.Stat(kiroAgentPath(workspace)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("launch wrote agent config, err = %v", err)
	}
}

func TestGetLaunchCommandDoesNotRewritePreparedAgentConfig(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "kiro-cli"}
	workspace := t.TempDir()
	promptFile := kiroPromptFile(t, "standing AO instructions")

	if err := plugin.GetAgentHooks(context.Background(), ports.WorkspaceHookConfig{
		Config:           ports.AgentConfig{Model: "project-model"},
		DataDir:          t.TempDir(),
		SessionID:        "sess-1",
		SystemPrompt:     "standing AO instructions",
		SystemPromptFile: promptFile,
		WorkspacePath:    workspace,
	}); err != nil {
		t.Fatal(err)
	}

	cmd, err := plugin.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		SystemPromptFile: promptFile,
		WorkspacePath:    workspace,
	})
	if err != nil {
		t.Fatal(err)
	}

	want := []string{
		"kiro-cli", "chat",
		"--agent", "ao",
	}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("unexpected command\nwant: %#v\n got: %#v", want, cmd)
	}
	var topLevel map[string]json.RawMessage
	data, err := os.ReadFile(kiroAgentPath(workspace))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &topLevel); err != nil {
		t.Fatal(err)
	}
	if got := string(topLevel["prompt"]); got != `"`+kiroPromptURI(promptFile)+`"` {
		t.Fatalf("agent prompt = %q, want file URI", got)
	}
	var model string
	if err := json.Unmarshal(topLevel["model"], &model); err != nil {
		t.Fatalf("decode model from %s: %v", data, err)
	}
	if model != "project-model" {
		t.Fatalf("model = %q, want preserved model", model)
	}
}

func TestGetLaunchCommandMapsApprovalModes(t *testing.T) {
	tests := []struct {
		name        string
		permission  ports.PermissionMode
		want        []string
		notExpected []string
	}{
		{
			name:        "default",
			permission:  ports.PermissionModeDefault,
			notExpected: []string{"--trust-all-tools", "--trust-tools=fs_read,fs_write"},
		},
		{
			name:       "accept-edits",
			permission: ports.PermissionModeAcceptEdits,
			want:       []string{"--trust-tools=fs_read,fs_write"},
		},
		{
			name:       "auto",
			permission: ports.PermissionModeAuto,
			want:       []string{"--trust-all-tools"},
		},
		{
			name:       "bypass-permissions",
			permission: ports.PermissionModeBypassPermissions,
			want:       []string{"--trust-all-tools"},
		},
		{
			name:        "empty",
			permission:  "",
			notExpected: []string{"--trust-all-tools", "--trust-tools=fs_read,fs_write"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plugin := &Plugin{resolvedBinary: "kiro-cli"}
			cmd, err := plugin.GetLaunchCommand(context.Background(), ports.LaunchConfig{
				Permissions: tt.permission,
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(tt.want) > 0 && !containsSubsequence(cmd, tt.want) {
				t.Fatalf("command %#v does not contain %#v", cmd, tt.want)
			}
			for _, missing := range tt.notExpected {
				if contains(cmd, missing) {
					t.Fatalf("command %#v contains %q", cmd, missing)
				}
			}
		})
	}
}

func TestGetLaunchCommandCtxCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	plugin := &Plugin{}
	if _, err := plugin.GetLaunchCommand(ctx, ports.LaunchConfig{}); err == nil {
		t.Fatal("expected error from cancelled context, got nil")
	}
}

func TestGetPromptDeliveryStrategyIsInCommand(t *testing.T) {
	plugin := &Plugin{}

	got, err := plugin.GetPromptDeliveryStrategy(context.Background(), ports.LaunchConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if got != ports.PromptDeliveryInCommand {
		t.Fatalf("unexpected strategy: %q", got)
	}
}

func TestGetPromptDeliveryStrategyPromptedWorkerIsAfterStart(t *testing.T) {
	plugin := &Plugin{}

	got, err := plugin.GetPromptDeliveryStrategy(context.Background(), ports.LaunchConfig{
		Kind:   domain.KindWorker,
		Prompt: "do this task",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != ports.PromptDeliveryAfterStart {
		t.Fatalf("unexpected strategy: %q", got)
	}
}

func TestGetPromptDeliveryStrategyOrchestratorUsesCustomAgent(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "kiro-cli"}

	got, err := plugin.GetPromptDeliveryStrategy(context.Background(), ports.LaunchConfig{Kind: domain.KindOrchestrator})
	if err != nil {
		t.Fatal(err)
	}
	if got != ports.PromptDeliveryCustomAgent {
		t.Fatalf("unexpected strategy: %q", got)
	}
}

func TestGetPromptDeliveryStrategyPromptedOrchestratorUsesCommand(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "kiro-cli"}

	got, err := plugin.GetPromptDeliveryStrategy(context.Background(), ports.LaunchConfig{
		Kind:   domain.KindOrchestrator,
		Prompt: "do this explicit task",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != ports.PromptDeliveryInCommand {
		t.Fatalf("unexpected strategy: %q", got)
	}
}

func TestPromptReadinessHints(t *testing.T) {
	hints, err := (&Plugin{}).PromptReadinessHints(context.Background(), ports.LaunchConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if hints.Timeout <= 0 || len(hints.Patterns) == 0 {
		t.Fatalf("hints = %#v, want bounded readiness patterns", hints)
	}
}

func TestAuthStatusUsesKiroWhoami(t *testing.T) {
	restore := stubKiroAuthRunner(t, func(_ context.Context, name string, arg ...string) ([]byte, error) {
		if name != "kiro-cli" {
			t.Fatalf("binary = %q, want kiro-cli", name)
		}
		if !reflect.DeepEqual(arg, []string{"whoami"}) {
			t.Fatalf("args = %#v, want [whoami]", arg)
		}
		return []byte("Logged in with Google\nEmail: nicachale456@gmail.com\n"), nil
	})
	defer restore()

	plugin := &Plugin{resolvedBinary: "kiro-cli"}
	status, err := plugin.AuthStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status != ports.AgentAuthStatusAuthorized {
		t.Fatalf("status = %q, want %q", status, ports.AgentAuthStatusAuthorized)
	}
}

func TestGetConfigSpecReportsModelField(t *testing.T) {
	p := New()

	spec, err := p.GetConfigSpec(context.Background())
	if err != nil {
		t.Fatalf("GetConfigSpec: %v", err)
	}

	var found bool
	for _, f := range spec.Fields {
		if f.Key != "model" {
			continue
		}
		found = true
		if f.Type != ports.ConfigFieldString {
			t.Errorf("model field Type = %v, want %v", f.Type, ports.ConfigFieldString)
		}
		if f.Description == "" {
			t.Error("model field Description is empty")
		}
	}
	if !found {
		t.Fatal("GetConfigSpec did not report a \"model\" field")
	}
}

func TestGetConfigSpecHonorsContextCancellation(t *testing.T) {
	p := New()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := p.GetConfigSpec(ctx)
	if err == nil {
		t.Fatal("GetConfigSpec with canceled context: want error, got nil")
	}
}
func TestAuthStatusUnauthorizedFromKiroWhoami(t *testing.T) {
	restore := stubKiroAuthRunner(t, func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		return []byte("Not logged in\n"), nil
	})
	defer restore()

	plugin := &Plugin{resolvedBinary: "kiro-cli"}
	status, err := plugin.AuthStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status != ports.AgentAuthStatusUnauthorized {
		t.Fatalf("status = %q, want %q", status, ports.AgentAuthStatusUnauthorized)
	}
}

func TestGetAgentHooksInstallsKiroHooks(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "kiro-cli"}
	workspace := t.TempDir()
	promptFile := kiroPromptFile(t, "standing AO instructions")
	hooksDir := filepath.Join(workspace, kiroHooksDirName, kiroAgentsDirName)
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	hooksPath := filepath.Join(hooksDir, kiroAgentFileName)
	existing := `{"name":"stale","hooks":{"stop":[{"command":"custom stop hook"}]}}`
	if err := os.WriteFile(hooksPath, []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := ports.WorkspaceHookConfig{
		DataDir:          t.TempDir(),
		SessionID:        "sess-1",
		SystemPrompt:     "standing AO instructions",
		SystemPromptFile: promptFile,
		WorkspacePath:    workspace,
	}
	if err := plugin.GetAgentHooks(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	// A second install must not duplicate AO hook commands.
	if err := plugin.GetAgentHooks(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(hooksPath)
	if err != nil {
		t.Fatal(err)
	}
	var topLevel map[string]json.RawMessage
	if err := json.Unmarshal(data, &topLevel); err != nil {
		t.Fatal(err)
	}
	var name string
	if err := json.Unmarshal(topLevel["name"], &name); err != nil {
		t.Fatalf("decode name from %s: %v", data, err)
	}
	if name != kiroAgentName {
		t.Fatalf("name = %q, want %q", name, kiroAgentName)
	}
	var prompt string
	if err := json.Unmarshal(topLevel["prompt"], &prompt); err != nil {
		t.Fatalf("decode prompt from %s: %v", data, err)
	}
	if prompt != kiroPromptURI(promptFile) {
		t.Fatalf("prompt = %q, want system prompt file URI", prompt)
	}
	if strings.Contains(string(data), "standing AO instructions") {
		t.Fatalf("agent file leaked prompt body:\n%s", data)
	}

	var config kiroHookFile
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	if config.Hooks == nil {
		t.Fatalf("hooks config missing hooks object: %#v", config)
	}
	for _, spec := range kiroManagedHooks {
		entries := config.Hooks[spec.Event]
		if count := countKiroHookCommand(entries, spec.Command); count != 1 {
			t.Fatalf("%s command count = %d, want 1 in %#v", spec.Event, count, entries)
		}
	}
	stopEntries := config.Hooks["stop"]
	if countKiroHookCommand(stopEntries, "custom stop hook") != 1 {
		t.Fatalf("existing stop hook was not preserved: %#v", stopEntries)
	}
}

func TestGetAgentHooksCreatesNamedKiroAgentFile(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "kiro-cli"}
	workspace := t.TempDir()
	hooksPath := kiroAgentPath(workspace)
	promptFile := kiroPromptFile(t, "exact orchestrator system prompt")

	cfg := ports.WorkspaceHookConfig{
		DataDir:          t.TempDir(),
		SessionID:        "sess-1",
		SystemPrompt:     "exact orchestrator system prompt",
		SystemPromptFile: promptFile,
		WorkspacePath:    workspace,
	}
	if err := plugin.GetAgentHooks(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(hooksPath)
	if err != nil {
		t.Fatal(err)
	}
	var topLevel map[string]json.RawMessage
	if err := json.Unmarshal(data, &topLevel); err != nil {
		t.Fatal(err)
	}
	var name string
	if err := json.Unmarshal(topLevel["name"], &name); err != nil {
		t.Fatalf("decode name from %s: %v", data, err)
	}
	if name != kiroAgentName {
		t.Fatalf("name = %q, want %q", name, kiroAgentName)
	}
	var config kiroHookFile
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	if config.Prompt == nil || *config.Prompt != kiroPromptURI(promptFile) {
		t.Fatalf("prompt = %#v, want system prompt file URI", config.Prompt)
	}
}

func TestGetAgentHooksRequiresSystemPromptFile(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "kiro-cli"}
	err := plugin.GetAgentHooks(context.Background(), ports.WorkspaceHookConfig{
		SessionID:     "sess-1",
		SystemPrompt:  "standing AO instructions",
		WorkspacePath: t.TempDir(),
	})
	if err == nil {
		t.Fatal("expected error for system prompt without prompt file")
	}
	if !strings.Contains(err.Error(), "system prompt file required") {
		t.Fatalf("err = %v, want system prompt file required", err)
	}
}

func TestGetAgentHooksWritesConfiguredModel(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "kiro-cli"}
	workspace := t.TempDir()
	hooksPath := kiroAgentPath(workspace)
	promptFile := kiroPromptFile(t, "standing AO instructions")

	cfg := ports.WorkspaceHookConfig{
		Config:           ports.AgentConfig{Model: "claude-sonnet-4-5"},
		DataDir:          t.TempDir(),
		SessionID:        "sess-1",
		SystemPrompt:     "standing AO instructions",
		SystemPromptFile: promptFile,
		WorkspacePath:    workspace,
	}
	if err := plugin.GetAgentHooks(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(hooksPath)
	if err != nil {
		t.Fatal(err)
	}
	var topLevel map[string]json.RawMessage
	if err := json.Unmarshal(data, &topLevel); err != nil {
		t.Fatal(err)
	}
	var model string
	if err := json.Unmarshal(topLevel["model"], &model); err != nil {
		t.Fatalf("decode model from %s: %v", data, err)
	}
	if model != "claude-sonnet-4-5" {
		t.Fatalf("model = %q, want configured model", model)
	}
}

func TestGetAgentHooksOverwritesStaleConfiguredModel(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "kiro-cli"}
	workspace := t.TempDir()
	hooksPath := kiroAgentPath(workspace)
	promptFile := kiroPromptFile(t, "standing AO instructions")
	if err := os.MkdirAll(filepath.Dir(hooksPath), 0o755); err != nil {
		t.Fatal(err)
	}
	existing := `{"name":"ao","model":"stale-model","tools":["custom"]}`
	if err := os.WriteFile(hooksPath, []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := ports.WorkspaceHookConfig{
		Config:           ports.AgentConfig{Model: "project-model"},
		DataDir:          t.TempDir(),
		SessionID:        "sess-1",
		SystemPrompt:     "standing AO instructions",
		SystemPromptFile: promptFile,
		WorkspacePath:    workspace,
	}
	if err := plugin.GetAgentHooks(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(hooksPath)
	if err != nil {
		t.Fatal(err)
	}
	var topLevel map[string]json.RawMessage
	if err := json.Unmarshal(data, &topLevel); err != nil {
		t.Fatal(err)
	}
	var model string
	if err := json.Unmarshal(topLevel["model"], &model); err != nil {
		t.Fatalf("decode model from %s: %v", data, err)
	}
	if model != "project-model" {
		t.Fatalf("model = %q, want project override", model)
	}
	var tools []string
	if err := json.Unmarshal(topLevel["tools"], &tools); err != nil {
		t.Fatalf("decode tools from %s: %v", data, err)
	}
	if !reflect.DeepEqual(tools, []string{"custom"}) {
		t.Fatalf("tools = %#v, want preserved custom tools", tools)
	}
}

func TestGetAgentHooksClearsStaleModelWhenConfigRemoved(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "kiro-cli"}
	workspace := t.TempDir()
	hooksPath := kiroAgentPath(workspace)
	promptFile := kiroPromptFile(t, "standing AO instructions")
	if err := os.MkdirAll(filepath.Dir(hooksPath), 0o755); err != nil {
		t.Fatal(err)
	}
	existing := `{"name":"ao","model":"stale-model","tools":["custom"]}`
	if err := os.WriteFile(hooksPath, []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := ports.WorkspaceHookConfig{
		DataDir:          t.TempDir(),
		SessionID:        "sess-1",
		SystemPrompt:     "standing AO instructions",
		SystemPromptFile: promptFile,
		WorkspacePath:    workspace,
	}
	if err := plugin.GetAgentHooks(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(hooksPath)
	if err != nil {
		t.Fatal(err)
	}
	var topLevel map[string]json.RawMessage
	if err := json.Unmarshal(data, &topLevel); err != nil {
		t.Fatal(err)
	}
	if _, ok := topLevel["model"]; ok {
		t.Fatalf("model still present in %s, want cleared when config has no model", data)
	}
	var tools []string
	if err := json.Unmarshal(topLevel["tools"], &tools); err != nil {
		t.Fatalf("decode tools from %s: %v", data, err)
	}
	if !reflect.DeepEqual(tools, []string{"custom"}) {
		t.Fatalf("tools = %#v, want preserved custom tools", tools)
	}
}

func TestUninstallHooksRemovesKiroHooks(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "kiro-cli"}
	workspace := t.TempDir()
	hooksPath := kiroAgentPath(workspace)

	ctx := context.Background()
	cfg := ports.WorkspaceHookConfig{DataDir: t.TempDir(), SessionID: "sess-1", WorkspacePath: workspace}

	// Pre-seed a user's own stop hook; it must survive uninstall.
	if err := os.MkdirAll(filepath.Dir(hooksPath), 0o755); err != nil {
		t.Fatal(err)
	}
	existing := `{"hooks":{"stop":[{"command":"custom stop hook"}]}}`
	if err := os.WriteFile(hooksPath, []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := plugin.GetAgentHooks(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if installed, err := plugin.AreHooksInstalled(ctx, workspace); err != nil || !installed {
		t.Fatalf("AreHooksInstalled after install = (%v, %v), want (true, nil)", installed, err)
	}

	if err := plugin.UninstallHooks(ctx, workspace); err != nil {
		t.Fatal(err)
	}
	if installed, err := plugin.AreHooksInstalled(ctx, workspace); err != nil || installed {
		t.Fatalf("AreHooksInstalled after uninstall = (%v, %v), want (false, nil)", installed, err)
	}

	data, err := os.ReadFile(hooksPath)
	if err != nil {
		t.Fatal(err)
	}
	var config kiroHookFile
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	for _, spec := range kiroManagedHooks {
		if got := countKiroHookCommand(config.Hooks[spec.Event], spec.Command); got != 0 {
			t.Fatalf("%s command %q count = %d after uninstall, want 0", spec.Event, spec.Command, got)
		}
	}
	if countKiroHookCommand(config.Hooks["stop"], "custom stop hook") != 1 {
		t.Fatalf("user stop hook not preserved: %#v", config.Hooks["stop"])
	}
}

func TestAreHooksInstalledMissingFile(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "kiro-cli"}
	workspace := t.TempDir()
	installed, err := plugin.AreHooksInstalled(context.Background(), workspace)
	if err != nil {
		t.Fatal(err)
	}
	if installed {
		t.Fatal("AreHooksInstalled = true for missing file, want false")
	}
}

func TestGetRestoreCommandReadsAgentSessionID(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "kiro-cli"}

	cmd, ok, err := plugin.GetRestoreCommand(context.Background(), ports.RestoreConfig{
		Permissions: ports.PermissionModeAuto,
		Session: ports.SessionRef{
			Metadata: map[string]string{ports.MetadataKeyAgentSessionID: "uuid-123"},
		},
	})
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if !ok {
		t.Fatal("ok = false, want true")
	}
	want := []string{
		"kiro-cli", "chat",
		"--agent", "ao",
		"--resume-id", "uuid-123",
		"--trust-all-tools",
	}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("restore cmd\nwant: %#v\n got: %#v", want, cmd)
	}
}

func TestGetRestoreCommandReappliesSystemPromptAgent(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "kiro-cli"}
	workspace := t.TempDir()
	promptFile := kiroPromptFile(t, "restore AO rules")
	if err := plugin.GetAgentHooks(context.Background(), ports.WorkspaceHookConfig{
		Config:           ports.AgentConfig{Model: "project-model"},
		DataDir:          t.TempDir(),
		SessionID:        "sess-1",
		SystemPrompt:     "restore AO rules",
		SystemPromptFile: promptFile,
		WorkspacePath:    workspace,
	}); err != nil {
		t.Fatal(err)
	}

	cmd, ok, err := plugin.GetRestoreCommand(context.Background(), ports.RestoreConfig{
		SystemPromptFile: promptFile,
		Session: ports.SessionRef{
			WorkspacePath: workspace,
			Metadata:      map[string]string{ports.MetadataKeyAgentSessionID: "uuid-123"},
		},
	})
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if !ok {
		t.Fatal("ok = false, want true")
	}
	want := []string{
		"kiro-cli", "chat",
		"--agent", "ao",
		"--resume-id", "uuid-123",
	}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("restore cmd\nwant: %#v\n got: %#v", want, cmd)
	}
	var config map[string]json.RawMessage
	data, err := os.ReadFile(kiroAgentPath(workspace))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	if string(config["prompt"]) != `"`+kiroPromptURI(promptFile)+`"` {
		t.Fatalf("agent config = %#v, want restore prompt", config)
	}
	var model string
	if err := json.Unmarshal(config["model"], &model); err != nil {
		t.Fatalf("decode model from %s: %v", data, err)
	}
	if model != "project-model" {
		t.Fatalf("model = %q, want preserved model", model)
	}
}

func TestGetRestoreCommandOrchestratorUsesInteractiveAgent(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "kiro-cli"}

	cmd, ok, err := plugin.GetRestoreCommand(context.Background(), ports.RestoreConfig{
		Kind:        domain.KindOrchestrator,
		Permissions: ports.PermissionModeDefault,
		Session: ports.SessionRef{
			Metadata: map[string]string{ports.MetadataKeyAgentSessionID: "uuid-123"},
		},
	})
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if !ok {
		t.Fatal("ok = false, want true")
	}
	want := []string{
		"kiro-cli", "chat",
		"--agent", "ao",
		"--resume-id", "uuid-123",
	}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("restore cmd\nwant: %#v\n got: %#v", want, cmd)
	}
}

func TestGetRestoreCommandFalseWithoutAgentSessionID(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "kiro-cli"}

	cases := []struct {
		name string
		ref  ports.SessionRef
	}{
		{"empty session ref", ports.SessionRef{}},
		{"empty metadata", ports.SessionRef{Metadata: map[string]string{}}},
		{"blank agent session metadata", ports.SessionRef{Metadata: map[string]string{ports.MetadataKeyAgentSessionID: "   "}}},
		{"workspace path only", ports.SessionRef{WorkspacePath: "/some/path"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd, ok, err := plugin.GetRestoreCommand(context.Background(), ports.RestoreConfig{
				Permissions: ports.PermissionModeAuto,
				Session:     tc.ref,
			})
			if err != nil {
				t.Fatalf("err = %v, want nil", err)
			}
			if ok {
				t.Fatalf("ok = true, want false")
			}
			if cmd != nil {
				t.Fatalf("cmd = %#v, want nil", cmd)
			}
		})
	}
}

func TestSessionInfoReadsHookMetadata(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "kiro-cli"}

	info, ok, err := plugin.SessionInfo(context.Background(), ports.SessionRef{
		WorkspacePath: "/some/path",
		Metadata: map[string]string{
			ports.MetadataKeyAgentSessionID: "uuid-123",
			ports.MetadataKeyTitle:          "Fix login redirect",
			ports.MetadataKeySummary:        "Updated the auth callback and tests.",
			"ignored":                       "not returned",
		},
	})
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if !ok {
		t.Fatalf("ok = false, want true")
	}
	if info.AgentSessionID != "uuid-123" {
		t.Fatalf("AgentSessionID = %q, want native id", info.AgentSessionID)
	}
	if info.Title != "Fix login redirect" {
		t.Fatalf("Title = %q, want hook title", info.Title)
	}
	if info.Summary != "Updated the auth callback and tests." {
		t.Fatalf("Summary = %q, want hook summary", info.Summary)
	}
	if info.Metadata != nil {
		t.Fatalf("Metadata = %#v, want nil for Kiro", info.Metadata)
	}
}

func TestSessionInfoFalseWhenNoHookMetadata(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "kiro-cli"}

	info, ok, err := plugin.SessionInfo(context.Background(), ports.SessionRef{
		WorkspacePath: "/some/path",
		Metadata:      map[string]string{},
	})
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if ok {
		t.Fatalf("ok = true, want false")
	}
	if !reflect.DeepEqual(info, ports.SessionInfo{}) {
		t.Fatalf("info = %#v, want zero value", info)
	}
}

func TestResolveKiroBinaryFallback(t *testing.T) {
	// When the binary is not on PATH or any well-known location, the resolver
	// MUST surface ports.ErrAgentBinaryNotFound rather than a silent string
	// fallback that lets a missing CLI launch into an empty tmux pane.
	bin, err := ResolveKiroBinary(context.Background())
	if err != nil {
		if !errors.Is(err, ports.ErrAgentBinaryNotFound) {
			t.Fatalf("err = %v, want ports.ErrAgentBinaryNotFound", err)
		}
		return
	}
	if bin == "" {
		t.Fatal("ResolveKiroBinary returned empty path with no error")
	}
}

func TestResolveKiroBinaryCtxCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ResolveKiroBinary(ctx); err == nil {
		t.Fatal("expected error from cancelled context, got nil")
	}
}

func contains(values []string, needle string) bool {
	for _, value := range values {
		if value == needle {
			return true
		}
	}
	return false
}

func containsSubsequence(values []string, needle []string) bool {
	if len(needle) == 0 {
		return true
	}

	for start := range values {
		if start+len(needle) > len(values) {
			return false
		}
		ok := true
		for offset, want := range needle {
			if values[start+offset] != want {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}

	return false
}

func stubKiroAuthRunner(t *testing.T, runner func(context.Context, string, ...string) ([]byte, error)) func() {
	t.Helper()
	previous := authprobe.CmdRunner
	authprobe.CmdRunner = runner
	return func() { authprobe.CmdRunner = previous }
}

func countKiroHookCommand(entries []kiroHookEntry, command string) int {
	count := 0
	for _, entry := range entries {
		if entry.Command == command {
			count++
		}
	}
	return count
}

func kiroPromptFile(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "system.md")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func kiroPromptURI(path string) string {
	return "file://" + filepath.ToSlash(path)
}
