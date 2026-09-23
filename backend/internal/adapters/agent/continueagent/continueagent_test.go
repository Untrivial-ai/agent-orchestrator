package continueagent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/claudecode"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/hooksjson"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestManifest(t *testing.T) {
	m := (&Plugin{}).Manifest()
	if m.ID != "continue" {
		t.Fatalf("ID = %q, want continue", m.ID)
	}
	if m.Name != "Continue" {
		t.Fatalf("Name = %q, want Continue", m.Name)
	}
	hasAgent := false
	for _, c := range m.Capabilities {
		if c == adapters.CapabilityAgent {
			hasAgent = true
		}
	}
	if !hasAgent {
		t.Fatal("missing CapabilityAgent")
	}
}

func TestImplementsAuthChecker(t *testing.T) {
	if _, ok := any(&Plugin{}).(ports.AgentAuthChecker); !ok {
		t.Fatal("Continue must implement AgentAuthChecker using local credential evidence")
	}
}

func TestGetConfigSpecReportsModel(t *testing.T) {
	spec, err := (&Plugin{}).GetConfigSpec(context.Background())
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(spec.Fields) != 1 || spec.Fields[0].Key != "model" {
		t.Fatalf("unexpected fields: %#v", spec.Fields)
	}
}

func TestGetPromptDeliveryStrategyNoPrompt(t *testing.T) {
	s, err := (&Plugin{}).GetPromptDeliveryStrategy(context.Background(), ports.LaunchConfig{})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if s != ports.PromptDeliveryAfterStart {
		t.Fatalf("strategy = %q, want after_start", s)
	}
}

func TestGetPromptDeliveryStrategyWithPrompt(t *testing.T) {
	s, err := (&Plugin{}).GetPromptDeliveryStrategy(context.Background(), ports.LaunchConfig{Prompt: "fix it"})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if s != ports.PromptDeliveryInCommand {
		t.Fatalf("strategy = %q, want in_command", s)
	}
}

func TestGetPromptDeliveryStrategyContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (&Plugin{}).GetPromptDeliveryStrategy(ctx, ports.LaunchConfig{}); err == nil {
		t.Fatal("expected error from canceled context, got nil")
	}
}

func TestGetLaunchCommandWorkerBypassIsInteractive(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "cn"}
	cmd, err := plugin.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		Kind:        domain.KindWorker,
		Prompt:      "do the thing",
		Permissions: ports.PermissionModeBypassPermissions,
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	want := []string{"cn", "--auto", "--", "do the thing"}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("cmd = %#v, want %#v", cmd, want)
	}
}

func TestGetLaunchCommandForwardsModel(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "cn"}
	cmd, err := plugin.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		Config: ports.AgentConfig{Model: "  anthropic/claude-sonnet  "},
		Prompt: "fix it",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"cn", "--model", "anthropic/claude-sonnet", "--", "fix it"}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("cmd = %#v, want %#v", cmd, want)
	}
}

func TestGetLaunchCommandWorkerAutoIsInteractive(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "cn"}
	cmd, err := plugin.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		Kind:        domain.KindWorker,
		Prompt:      "refactor auth",
		Permissions: ports.PermissionModeAuto,
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	want := []string{"cn", "--auto", "--", "refactor auth"}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("cmd = %#v, want %#v", cmd, want)
	}
}

func TestGetLaunchCommandWorkerDefaultPermsIsInteractive(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "cn"}
	cmd, err := plugin.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		Kind:   domain.KindWorker,
		Prompt: "fix it",
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	want := []string{"cn", "--", "fix it"}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("cmd = %#v, want %#v", cmd, want)
	}
	joined := strings.Join(cmd, " ")
	if strings.Contains(joined, "--print") || strings.Contains(joined, "--auto") || strings.Contains(joined, "--readonly") {
		t.Fatal("should launch interactively and emit no permission flag for default perms")
	}
}

func TestGetLaunchCommandAppendsInlineRule(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "cn"}
	cmd, err := plugin.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		Prompt:           "fix it",
		SystemPrompt:     "follow AO rules",
		SystemPromptFile: "/tmp/system.md",
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	want := []string{"cn", "--rule", "follow AO rules", "--", "fix it"}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("cmd = %#v, want %#v", cmd, want)
	}
}

func TestGetLaunchCommandAppendsRuleFile(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "cn"}
	cmd, err := plugin.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		Prompt:           "fix it",
		SystemPromptFile: "/tmp/system.md",
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	want := []string{"cn", "--rule", "/tmp/system.md", "--", "fix it"}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("cmd = %#v, want %#v", cmd, want)
	}
}

func TestGetLaunchCommandAcceptEditsNoFlag(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "cn"}
	cmd, err := plugin.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		Prompt:      "tidy up",
		Permissions: ports.PermissionModeAcceptEdits,
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	want := []string{"cn", "--", "tidy up"}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("cmd = %#v, want %#v (accept-edits should emit no flag)", cmd, want)
	}
}

func TestGetLaunchCommandNoPrompt(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "cn"}
	cmd, err := plugin.GetLaunchCommand(context.Background(), ports.LaunchConfig{})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	want := []string{"cn"}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("cmd = %#v, want %#v", cmd, want)
	}
}

func TestGetLaunchCommandNoPromptWithAuto(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "cn"}
	cmd, err := plugin.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		Permissions: ports.PermissionModeAuto,
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	want := []string{"cn", "--auto"}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("cmd = %#v, want %#v", cmd, want)
	}
}

func TestGetLaunchCommandContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// Force binary resolution (unset cache) so ctx.Err() is hit.
	_, err := (&Plugin{}).GetLaunchCommand(ctx, ports.LaunchConfig{Prompt: "x"})
	if err == nil {
		t.Fatal("expected error from canceled context, got nil")
	}
}

func TestGetRestoreCommand(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "cn"}
	cmd, ok, err := plugin.GetRestoreCommand(context.Background(), ports.RestoreConfig{
		Session: ports.SessionRef{
			Metadata: map[string]string{
				ports.MetadataKeyAgentSessionID: "sess-abc123",
			},
		},
		Permissions: ports.PermissionModeBypassPermissions,
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !ok {
		t.Fatal("ok=false, want true")
	}
	want := []string{"cn", "--auto", "--fork", "sess-abc123"}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("cmd = %#v, want %#v", cmd, want)
	}
}

func TestGetRestoreCommandDefaultPerms(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "cn"}
	cmd, ok, err := plugin.GetRestoreCommand(context.Background(), ports.RestoreConfig{
		Session: ports.SessionRef{
			Metadata: map[string]string{
				ports.MetadataKeyAgentSessionID: "sess-xyz",
			},
		},
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !ok {
		t.Fatal("ok=false, want true")
	}
	want := []string{"cn", "--fork", "sess-xyz"}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("cmd = %#v, want %#v", cmd, want)
	}
}

func TestGetRestoreCommandAppendsRule(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "cn"}
	cmd, ok, err := plugin.GetRestoreCommand(context.Background(), ports.RestoreConfig{
		SystemPrompt: "restore rules",
		Session: ports.SessionRef{
			Metadata: map[string]string{
				ports.MetadataKeyAgentSessionID: "sess-xyz",
			},
		},
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !ok {
		t.Fatal("ok=false, want true")
	}
	want := []string{"cn", "--rule", "restore rules", "--fork", "sess-xyz"}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("cmd = %#v, want %#v", cmd, want)
	}
}

func TestGetRestoreCommandNoID(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "cn"}
	_, ok, err := plugin.GetRestoreCommand(context.Background(), ports.RestoreConfig{
		Session: ports.SessionRef{Metadata: map[string]string{}},
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if ok {
		t.Fatal("ok=true with no agentSessionId, want false")
	}
}

func TestGetRestoreCommandWhitespaceID(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "cn"}
	_, ok, err := plugin.GetRestoreCommand(context.Background(), ports.RestoreConfig{
		Session: ports.SessionRef{Metadata: map[string]string{
			ports.MetadataKeyAgentSessionID: "   ",
		}},
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if ok {
		t.Fatal("ok=true with whitespace agentSessionId, want false")
	}
}

func TestSessionInfoReadsHookMetadata(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "cn"}
	info, ok, err := plugin.SessionInfo(context.Background(), ports.SessionRef{
		Metadata: map[string]string{
			ports.MetadataKeyAgentSessionID: "cn-ses-1",
			ports.MetadataKeyTitle:          "Fix login redirect",
			ports.MetadataKeySummary:        "Updated the auth callback and tests.",
		},
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !ok {
		t.Fatal("ok=false, want true")
	}
	if info.AgentSessionID != "cn-ses-1" {
		t.Fatalf("AgentSessionID = %q, want cn-ses-1", info.AgentSessionID)
	}
	if info.Title != "Fix login redirect" {
		t.Fatalf("Title = %q", info.Title)
	}
	if info.Summary != "Updated the auth callback and tests." {
		t.Fatalf("Summary = %q", info.Summary)
	}
}

func TestSessionInfoFalseWhenNoHookMetadata(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "cn"}
	info, ok, err := plugin.SessionInfo(context.Background(), ports.SessionRef{
		Metadata: map[string]string{},
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if ok {
		t.Fatalf("ok=true with empty metadata, want false")
	}
	if !reflect.DeepEqual(info, ports.SessionInfo{}) {
		t.Fatalf("info = %#v, want zero", info)
	}
}

func TestGetAgentHooksDelegates(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "cn"}
	ws := t.TempDir()
	if err := plugin.GetAgentHooks(context.Background(), ports.WorkspaceHookConfig{
		WorkspacePath: ws,
		SessionID:     "continue-test-1",
	}); err != nil {
		t.Fatalf("GetAgentHooks: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(ws, ".claude", "settings.local.json"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	for _, want := range []string{
		"ao hooks continue session-start",
		"ao hooks continue user-prompt-submit",
		"ao hooks continue notification",
		"ao hooks continue stop",
		"ao hooks continue session-end",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("settings missing %q: %s", want, body)
		}
	}
	if strings.Contains(body, "ao hooks claude-code") {
		t.Fatalf("Continue hooks must use the Continue token, got: %s", body)
	}
}

func TestGetAgentHooksIncludesSupportedSessionStartSources(t *testing.T) {
	ws := t.TempDir()
	if err := (&Plugin{resolvedBinary: "cn"}).GetAgentHooks(context.Background(), ports.WorkspaceHookConfig{WorkspacePath: ws}); err != nil {
		t.Fatalf("GetAgentHooks: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(ws, ".claude", "settings.local.json"))
	if err != nil {
		t.Fatal(err)
	}
	var settings struct {
		Hooks map[string][]hooksjson.MatcherGroup `json:"hooks"`
	}
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatal(err)
	}
	for _, group := range settings.Hooks["SessionStart"] {
		for _, hook := range group.Hooks {
			if hook.Command == "ao hooks continue session-start" {
				if group.Matcher == nil || *group.Matcher != "startup|resume|clear|compact" {
					t.Fatalf("SessionStart matcher = %v, want startup|resume|clear|compact", group.Matcher)
				}
				return
			}
		}
	}
	t.Fatal("Continue SessionStart hook not installed")
}

func TestGetAgentHooksMigratesLegacyClaudeHooks(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "cn"}
	ws := t.TempDir()
	settingsDir := filepath.Join(ws, ".claude")
	if err := os.MkdirAll(settingsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	legacy := `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"user stop hook"},{"type":"command","command":"ao hooks claude-code stop"}]}],"Notification":[{"hooks":[{"type":"command","command":"ao hooks claude-code notification"}]}]}}`
	if err := os.WriteFile(filepath.Join(settingsDir, "settings.local.json"), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := plugin.GetAgentHooks(context.Background(), ports.WorkspaceHookConfig{WorkspacePath: ws}); err != nil {
		t.Fatalf("GetAgentHooks: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(settingsDir, "settings.local.json"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	if strings.Contains(body, "ao hooks claude-code") {
		t.Fatalf("legacy Continue hooks were retained: %s", body)
	}
	if !strings.Contains(body, "user stop hook") {
		t.Fatalf("user hook was not preserved: %s", body)
	}
	if !strings.Contains(body, "ao hooks continue stop") || !strings.Contains(body, "ao hooks continue notification") {
		t.Fatalf("new Continue hooks missing: %s", body)
	}
}

func TestClaudeInstallReplacesContinueHooks(t *testing.T) {
	ws := t.TempDir()
	settingsDir := filepath.Join(ws, ".claude")
	if err := os.MkdirAll(settingsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	settingsPath := filepath.Join(settingsDir, "settings.local.json")
	if err := os.WriteFile(settingsPath, []byte(`{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"user stop hook"}]}]}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := ports.WorkspaceHookConfig{WorkspacePath: ws}
	if err := (&Plugin{resolvedBinary: "cn"}).GetAgentHooks(context.Background(), cfg); err != nil {
		t.Fatalf("install Continue hooks: %v", err)
	}
	if err := claudecode.New().GetAgentHooks(context.Background(), cfg); err != nil {
		t.Fatalf("install Claude Code hooks: %v", err)
	}

	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	if strings.Contains(body, "ao hooks continue ") {
		t.Fatalf("Continue hooks were retained after Claude install: %s", body)
	}
	if !strings.Contains(body, "ao hooks claude-code stop") {
		t.Fatalf("Claude hooks missing after switch: %s", body)
	}
	if !strings.Contains(body, "user stop hook") {
		t.Fatalf("user hook was not preserved: %s", body)
	}
}

func TestDeriveActivityStateUsesClaudeCompatiblePayload(t *testing.T) {
	got, ok := DeriveActivityState("notification", []byte(`{"notification_type":"agent_needs_input"}`))
	if !ok || got != domain.ActivityWaitingInput {
		t.Fatalf("DeriveActivityState(notification) = (%q, %v), want (%q, true)", got, ok, domain.ActivityWaitingInput)
	}
}

func TestResolveContinueBinaryContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ResolveContinueBinary(ctx); err == nil {
		t.Fatal("expected error from canceled context, got nil")
	}
}
