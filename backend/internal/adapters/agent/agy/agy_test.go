package agy

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
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
			Metadata:      map[string]string{ports.MetadataKeyAgentSessionID: "native-id-123"},
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
	if info.AgentSessionID != "native-id-123" || info.Title != "My Title" || info.Summary != "My Summary" {
		t.Fatalf("unexpected SessionInfo: %#v", info)
	}
}

func TestActivitySignalCapabilities(t *testing.T) {
	p := &Plugin{}
	if !p.EmitsSubmitActivity() {
		t.Fatal("EmitsSubmitActivity = false, want true")
	}
	if p.EmitsBlockedActivity() {
		t.Fatal("EmitsBlockedActivity = true, want false")
	}
}

func TestHooksLifecycle(t *testing.T) {
	tmpDir := t.TempDir()
	plugin := &Plugin{}
	cfg := ports.WorkspaceHookConfig{WorkspacePath: tmpDir}

	installed, err := plugin.AreHooksInstalled(context.Background(), tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	if installed {
		t.Fatal("expected hooks to not be installed initially")
	}

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

	hooksJSONPath := filepath.Join(tmpDir, ".agents", "hooks.json")
	data, err := os.ReadFile(hooksJSONPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(tmpDir, ".gemini", "hooks.json")); !os.IsNotExist(err) {
		t.Fatalf("legacy .gemini/hooks.json exists or stat failed: %v", err)
	}

	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		t.Fatal(err)
	}
	aoRaw, ok := top[agyManagedHookName]
	if !ok {
		t.Fatalf("missing named hook %q", agyManagedHookName)
	}
	var ao agyNamedHook
	if err := json.Unmarshal(aoRaw, &ao); err != nil {
		t.Fatal(err)
	}
	if len(ao.PreInvocation) != 1 || ao.PreInvocation[0].Command != "ao hooks agy pre-invocation" || ao.PreInvocation[0].Timeout != 30 {
		t.Fatalf("unexpected PreInvocation hooks: %#v", ao.PreInvocation)
	}
	if len(ao.PostToolUse) != 1 || ao.PostToolUse[0].Matcher == nil || *ao.PostToolUse[0].Matcher != "*" || len(ao.PostToolUse[0].Hooks) != 1 || ao.PostToolUse[0].Hooks[0].Command != "ao hooks agy post-tool-use" || ao.PostToolUse[0].Hooks[0].Timeout != 30 {
		t.Fatalf("unexpected PostToolUse hooks: %#v", ao.PostToolUse)
	}
	if len(ao.Stop) != 1 || ao.Stop[0].Command != "ao hooks agy stop" || ao.Stop[0].Timeout != 30 {
		t.Fatalf("unexpected Stop hooks: %#v", ao.Stop)
	}

	if err := plugin.GetAgentHooks(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	secondData, err := os.ReadFile(hooksJSONPath)
	if err != nil {
		t.Fatal(err)
	}
	var secondTop map[string]json.RawMessage
	if err := json.Unmarshal(secondData, &secondTop); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(top[agyManagedHookName], secondTop[agyManagedHookName]) {
		t.Fatal("reinstall changed the managed hook entry")
	}

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

func TestHooksPreserveUserNamedHooks(t *testing.T) {
	tmpDir := t.TempDir()
	hooksPath := filepath.Join(tmpDir, ".agents", "hooks.json")
	if err := os.MkdirAll(filepath.Dir(hooksPath), 0o750); err != nil {
		t.Fatal(err)
	}
	seed := []byte(`{
  "user-linter": {
    "enabled": false,
    "PostToolUse": [{"matcher":"run_command","hooks":[{"command":"./lint.sh"}]}]
  },
  "future-field": {"value": 7}
}`)
	if err := os.WriteFile(hooksPath, seed, 0o600); err != nil {
		t.Fatal(err)
	}
	var before map[string]json.RawMessage
	if err := json.Unmarshal(seed, &before); err != nil {
		t.Fatal(err)
	}

	plugin := &Plugin{}
	if err := plugin.GetAgentHooks(context.Background(), ports.WorkspaceHookConfig{WorkspacePath: tmpDir}); err != nil {
		t.Fatal(err)
	}
	afterInstall := readHookRawMap(t, hooksPath)
	if _, ok := afterInstall[agyManagedHookName]; !ok {
		t.Fatalf("missing named hook %q", agyManagedHookName)
	}
	assertRawJSONEqual(t, before["user-linter"], afterInstall["user-linter"])
	assertRawJSONEqual(t, before["future-field"], afterInstall["future-field"])

	if err := plugin.UninstallHooks(context.Background(), tmpDir); err != nil {
		t.Fatal(err)
	}
	afterUninstall := readHookRawMap(t, hooksPath)
	if _, ok := afterUninstall[agyManagedHookName]; ok {
		t.Fatalf("named hook %q remains after uninstall", agyManagedHookName)
	}
	assertRawJSONEqual(t, before["user-linter"], afterUninstall["user-linter"])
	assertRawJSONEqual(t, before["future-field"], afterUninstall["future-field"])
}

func TestHooksRejectMalformedJSON(t *testing.T) {
	tmpDir := t.TempDir()
	hooksPath := filepath.Join(tmpDir, ".agents", "hooks.json")
	if err := os.MkdirAll(filepath.Dir(hooksPath), 0o750); err != nil {
		t.Fatal(err)
	}
	want := []byte(`{not-json`)
	if err := os.WriteFile(hooksPath, want, 0o600); err != nil {
		t.Fatal(err)
	}

	err := (&Plugin{}).GetAgentHooks(context.Background(), ports.WorkspaceHookConfig{WorkspacePath: tmpDir})
	if err == nil || !strings.Contains(err.Error(), "parse") {
		t.Fatalf("GetAgentHooks error = %v, want parse error", err)
	}
	got, readErr := os.ReadFile(hooksPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("malformed file changed: got %q, want %q", got, want)
	}
}

func TestAreHooksInstalledRejectsDifferentEntryWithManagedName(t *testing.T) {
	tmpDir := t.TempDir()
	hooksPath := filepath.Join(tmpDir, ".agents", "hooks.json")
	if err := os.MkdirAll(filepath.Dir(hooksPath), 0o750); err != nil {
		t.Fatal(err)
	}
	data := []byte(`{"agent-orchestrator":{"Stop":[{"command":"user-command"}]}}`)
	if err := os.WriteFile(hooksPath, data, 0o600); err != nil {
		t.Fatal(err)
	}

	installed, err := (&Plugin{}).AreHooksInstalled(context.Background(), tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	if installed {
		t.Fatal("different named entry reported as AO-managed hooks")
	}
}

func readHookRawMap(t *testing.T, path string) map[string]json.RawMessage {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]json.RawMessage
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func assertRawJSONEqual(t *testing.T, want, got json.RawMessage) {
	t.Helper()
	var wantValue, gotValue any
	if err := json.Unmarshal(want, &wantValue); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(got, &gotValue); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotValue, wantValue) {
		t.Fatalf("JSON values differ\nwant: %s\n got: %s", want, got)
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
