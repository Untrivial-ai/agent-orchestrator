package agy

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestManifest(t *testing.T) {
	plugin := New()
	manifest := plugin.Manifest()
	if manifest.ID != "agy" {
		t.Fatalf("manifest id = %q, want agy", manifest.ID)
	}
}

func TestGetLaunchCommand(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "agy"}

	cmd, err := plugin.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		Permissions:   ports.PermissionModeBypassPermissions,
		Prompt:        "fix this",
		WorkspacePath: "/tmp/ws",
		Config:        ports.AgentConfig{Model: "gemini-3-pro"},
	})
	if err != nil {
		t.Fatal(err)
	}

	want := []string{
		"agy",
		"--add-dir", "/tmp/ws",
		"--dangerously-skip-permissions",
		"--model", "gemini-3-pro",
		"--prompt-interactive", "fix this",
	}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("unexpected command\nwant: %#v\n got: %#v", want, cmd)
	}
}

func TestGetLaunchCommandNoModel(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "agy"}

	cmd, err := plugin.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		Permissions:   ports.PermissionModeBypassPermissions,
		Prompt:        "fix this",
		WorkspacePath: "/tmp/ws",
	})
	if err != nil {
		t.Fatal(err)
	}

	want := []string{
		"agy",
		"--add-dir", "/tmp/ws",
		"--dangerously-skip-permissions",
		"--prompt-interactive", "fix this",
	}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("unexpected command\nwant: %#v\n got: %#v", want, cmd)
	}
}

func TestGetPromptDeliveryStrategy(t *testing.T) {
	plugin := &Plugin{}
	got, err := plugin.GetPromptDeliveryStrategy(context.Background(), ports.LaunchConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if got != ports.PromptDeliveryInCommand {
		t.Fatalf("strategy = %q, want in_command", got)
	}
}

func TestGetRestoreCommand(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "agy"}

	cmd, ok, err := plugin.GetRestoreCommand(context.Background(), ports.RestoreConfig{
		Permissions: ports.PermissionModeBypassPermissions,
		Config:      ports.AgentConfig{Model: "gemini-3-flash"},
		Session: ports.SessionRef{
			Metadata: map[string]string{
				ports.MetadataKeyAgentSessionID: "native-id-123",
			},
			WorkspacePath: "/tmp/ws",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected ok=true")
	}

	want := []string{
		"agy",
		"--add-dir", "/tmp/ws",
		"--dangerously-skip-permissions",
		"--model", "gemini-3-flash",
		"--conversation", "native-id-123",
	}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("unexpected command\nwant: %#v\n got: %#v", want, cmd)
	}
}

func TestGetRestoreCommandNoSessionID(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "agy"}
	_, ok, err := plugin.GetRestoreCommand(context.Background(), ports.RestoreConfig{
		Session: ports.SessionRef{
			Metadata: map[string]string{},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("expected ok=false when agentSessionId is missing")
	}
}

func TestSessionInfo(t *testing.T) {
	plugin := &Plugin{}
	info, ok, err := plugin.SessionInfo(context.Background(), ports.SessionRef{
		Metadata: map[string]string{
			ports.MetadataKeyAgentSessionID: "native-id-123",
			"title":                         "My Title",
			"summary":                       "My Summary",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected ok=true")
	}
	if info.AgentSessionID != "native-id-123" ||
		info.Title != "My Title" ||
		info.Summary != "My Summary" {
		t.Fatalf("unexpected SessionInfo: %#v", info)
	}
}

func TestHooksLifecycle(t *testing.T) {
	tmpDir := t.TempDir()

	plugin := &Plugin{}
	cfg := ports.WorkspaceHookConfig{
		WorkspacePath: tmpDir,
	}

	// 1. Initially hooks should not be installed.
	installed, err := plugin.AreHooksInstalled(context.Background(), tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	if installed {
		t.Fatal("expected hooks to not be installed initially")
	}

	// 2. Install hooks.
	if err := plugin.GetAgentHooks(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}

	installed, err = plugin.AreHooksInstalled(context.Background(), tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	if !installed {
		t.Fatal("expected hooks to be installed after GetAgentHooks")
	}

	// Current Antigravity workspace hooks must be written to .agents/hooks.json.
	hooksJSONPath := filepath.Join(tmpDir, ".agents", "hooks.json")
	data, err := os.ReadFile(hooksJSONPath)
	if err != nil {
		t.Fatal(err)
	}

	var hookFile agyHookFile
	if err := json.Unmarshal(data, &hookFile); err != nil {
		t.Fatal(err)
	}

	raw, ok := hookFile[agyManagedHookName]
	if !ok {
		t.Fatalf("expected top-level hook %q", agyManagedHookName)
	}

	var definition agyHookDefinition
	if err := json.Unmarshal(raw, &definition); err != nil {
		t.Fatal(err)
	}

	if !hasAgyHookHandler(
		definition.PreInvocation,
		agyHookCommandPrefix+"before-agent",
	) {
		t.Fatal("expected PreInvocation before-agent hook")
	}

	if !hasAgyHookHandler(
		definition.PostInvocation,
		agyHookCommandPrefix+"after-agent",
	) {
		t.Fatal("expected PostInvocation after-agent hook")
	}

	if !hasAgyToolHook(
		definition.PostToolUse,
		agyHookCommandPrefix+"after-tool",
	) {
		t.Fatal("expected PostToolUse after-tool hook")
	}

	if !hasAgyHookHandler(
		definition.Stop,
		agyHookCommandPrefix+"after-agent",
	) {
		t.Fatal("expected Stop after-agent hook")
	}

	if len(definition.PostToolUse) != 1 {
		t.Fatalf(
			"expected one PostToolUse matcher group, got %d",
			len(definition.PostToolUse),
		)
	}

	if definition.PostToolUse[0].Matcher != "*" {
		t.Fatalf(
			"PostToolUse matcher = %q, want *",
			definition.PostToolUse[0].Matcher,
		)
	}

	// Legacy path must no longer be generated.
	legacyHooksPath := filepath.Join(tmpDir, ".gemini", "hooks.json")
	if _, err := os.Stat(legacyHooksPath); !os.IsNotExist(err) {
		t.Fatalf(
			"legacy hook file should not be generated at %s",
			legacyHooksPath,
		)
	}

	// 3. Reinstall should be idempotent.
	before, err := os.ReadFile(hooksJSONPath)
	if err != nil {
		t.Fatal(err)
	}

	if err := plugin.GetAgentHooks(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}

	after, err := os.ReadFile(hooksJSONPath)
	if err != nil {
		t.Fatal(err)
	}

	if string(before) != string(after) {
		t.Fatal("expected repeated GetAgentHooks to be idempotent")
	}

	// 4. Uninstall hooks.
	if err := plugin.UninstallHooks(context.Background(), tmpDir); err != nil {
		t.Fatal(err)
	}

	installed, err = plugin.AreHooksInstalled(context.Background(), tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	if installed {
		t.Fatal("expected hooks to be uninstalled after UninstallHooks")
	}
}

func TestGetAgentHooksPreservesUserHooks(t *testing.T) {
	tmpDir := t.TempDir()

	hooksDir := filepath.Join(tmpDir, ".agents")
	if err := os.MkdirAll(hooksDir, 0o750); err != nil {
		t.Fatal(err)
	}

	hooksPath := filepath.Join(hooksDir, "hooks.json")

	existing := `{
  "user-linter": {
    "PostToolUse": [
      {
        "matcher": "run_command",
        "hooks": [
          {
            "type": "command",
            "command": "./scripts/lint.sh",
            "timeout": 5
          }
        ]
      }
    ]
  }
}
`

	if err := os.WriteFile(hooksPath, []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}

	plugin := &Plugin{}
	cfg := ports.WorkspaceHookConfig{
		WorkspacePath: tmpDir,
	}

	if err := plugin.GetAgentHooks(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(hooksPath)
	if err != nil {
		t.Fatal(err)
	}

	var hookFile agyHookFile
	if err := json.Unmarshal(data, &hookFile); err != nil {
		t.Fatal(err)
	}

	if _, ok := hookFile["user-linter"]; !ok {
		t.Fatal("expected existing user hook to be preserved")
	}

	if _, ok := hookFile[agyManagedHookName]; !ok {
		t.Fatalf("expected managed hook %q to be installed", agyManagedHookName)
	}

	if err := plugin.UninstallHooks(context.Background(), tmpDir); err != nil {
		t.Fatal(err)
	}

	data, err = os.ReadFile(hooksPath)
	if err != nil {
		t.Fatal(err)
	}

	hookFile = nil
	if err := json.Unmarshal(data, &hookFile); err != nil {
		t.Fatal(err)
	}

	if _, ok := hookFile["user-linter"]; !ok {
		t.Fatal("expected user hook to survive AO uninstall")
	}

	if _, ok := hookFile[agyManagedHookName]; ok {
		t.Fatal("expected AO managed hook to be removed")
	}
}

func TestAreHooksInstalledRequiresCompleteDefinition(t *testing.T) {
	tmpDir := t.TempDir()

	hooksDir := filepath.Join(tmpDir, ".agents")
	if err := os.MkdirAll(hooksDir, 0o750); err != nil {
		t.Fatal(err)
	}

	hooksPath := filepath.Join(hooksDir, "hooks.json")

	incomplete := agyHookFile{}

	raw, err := json.Marshal(agyHookDefinition{
		PreInvocation: []agyHookHandler{
			{
				Type:    "command",
				Command: agyHookCommandPrefix + "before-agent",
				Timeout: agyHookTimeout,
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	incomplete[agyManagedHookName] = raw

	data, err := json.MarshalIndent(incomplete, "", "  ")
	if err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(hooksPath, append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}

	plugin := &Plugin{}

	installed, err := plugin.AreHooksInstalled(context.Background(), tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	if installed {
		t.Fatal("expected incomplete managed hooks to report not installed")
	}
}

func TestAuthStatus(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "agy"}

	status, err := plugin.AuthStatus(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status != ports.AgentAuthStatusAuthorized {
		t.Errorf("AuthStatus() = %v, want AgentAuthStatusAuthorized", status)
	}
}

func TestGetConfigSpecReportsModelField(t *testing.T) {
	plugin := &Plugin{}

	spec, err := plugin.GetConfigSpec(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	want := []ports.ConfigField{
		{
			Key:         "model",
			Type:        ports.ConfigFieldString,
			Description: "Model override passed to `agy --model` (e.g. gemini-3-pro).",
		},
	}

	if !reflect.DeepEqual(spec.Fields, want) {
		t.Fatalf("config fields\nwant: %#v\n got: %#v", want, spec.Fields)
	}
}
