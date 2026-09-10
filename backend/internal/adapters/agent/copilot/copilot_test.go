package copilot

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestManifestID(t *testing.T) {
	got := New().Manifest()
	if got.ID != "copilot" {
		t.Fatalf("Manifest().ID = %q, want %q", got.ID, "copilot")
	}
	if got.Name != "GitHub Copilot" {
		t.Fatalf("Manifest().Name = %q, want %q", got.Name, "GitHub Copilot")
	}
}

func TestGetLaunchCommandBuildsArgv(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "copilot"}

	cmd, err := plugin.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		Permissions: ports.PermissionModeBypassPermissions,
		Prompt:      "-fix this",
	})
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"copilot", "--allow-all", "--interactive", "-fix this"}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("unexpected command\nwant: %#v\n got: %#v", want, cmd)
	}
}

func TestGetLaunchCommandUsesSessionCustomAgentForSystemPrompt(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "copilot"}
	promptFile := filepath.Join(t.TempDir(), "system.md")

	cmd, err := plugin.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		Permissions:      ports.PermissionModeBypassPermissions,
		Prompt:           "-fix this",
		SessionID:        "mer-1",
		SystemPrompt:     "follow AO rules",
		SystemPromptFile: promptFile,
	})
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"copilot", "--allow-all", "--agent=ao-mer-1", "--interactive", "-fix this"}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("unexpected command\nwant: %#v\n got: %#v", want, cmd)
	}
}

func TestGetLaunchCommandOmitsPromptWhenEmpty(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "copilot"}

	cmd, err := plugin.GetLaunchCommand(context.Background(), ports.LaunchConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if contains(cmd, "-p") {
		t.Fatalf("command %#v unexpectedly contains -p", cmd)
	}
	if contains(cmd, "--interactive") {
		t.Fatalf("command %#v unexpectedly contains --interactive", cmd)
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
			notExpected: []string{"--allow-tool", "--allow-all-tools", "--allow-all"},
		},
		{
			name:       "accept-edits",
			permission: ports.PermissionModeAcceptEdits,
			want:       []string{"--allow-tool", "write"},
		},
		{
			name:       "auto",
			permission: ports.PermissionModeAuto,
			want:       []string{"--allow-all-tools"},
		},
		{
			name:       "bypass-permissions",
			permission: ports.PermissionModeBypassPermissions,
			want:       []string{"--allow-all"},
		},
		{
			name:        "empty falls back to default",
			permission:  "",
			notExpected: []string{"--allow-tool", "--allow-all-tools", "--allow-all"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plugin := &Plugin{resolvedBinary: "copilot"}
			cmd, err := plugin.GetLaunchCommand(context.Background(), ports.LaunchConfig{
				Permissions: tt.permission,
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(tt.want) > 0 && !containsSubsequence(cmd, tt.want) {
				t.Fatalf("command %#v does not contain %#v", cmd, tt.want)
			}
			for _, ne := range tt.notExpected {
				if contains(cmd, ne) {
					t.Fatalf("command %#v unexpectedly contains %q", cmd, ne)
				}
			}
		})
	}
}

func TestGetLaunchCommandRespectsCanceledContext(t *testing.T) {
	plugin := New()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := plugin.GetLaunchCommand(ctx, ports.LaunchConfig{Prompt: "hi"}); err == nil {
		t.Fatal("GetLaunchCommand with canceled context: err = nil, want non-nil")
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

func TestGetLaunchCommandDoesNotUseUnsupportedSystemPromptFlags(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "copilot"}
	promptFile := filepath.Join(t.TempDir(), "system.md")

	cmd, err := plugin.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		SystemPrompt:     "follow AO rules",
		SystemPromptFile: promptFile,
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, disallowed := range []string{"--system", "--system-prompt", "--append-system-prompt"} {
		if contains(cmd, disallowed) {
			t.Fatalf("command %#v unexpectedly contains unsupported Copilot system prompt flag %q", cmd, disallowed)
		}
	}
}

func TestGetLaunchCommandSelectsSessionCustomAgent(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "copilot"}
	promptFile := filepath.Join(t.TempDir(), "system.md")

	cmd, err := plugin.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		SessionID:        "mer-1",
		SystemPrompt:     "orchestrator must spawn workers",
		SystemPromptFile: promptFile,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !contains(cmd, "--agent=ao-mer-1") {
		t.Fatalf("command %#v does not select session custom agent", cmd)
	}
}

func TestGetConfigSpecReportsModelField(t *testing.T) {
	plugin := &Plugin{}

	spec, err := plugin.GetConfigSpec(context.Background())
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
		t.Fatalf("GetConfigSpec did not report a \"model\" field: %#v", spec.Fields)
	}
}

func TestGetConfigSpecRespectsCanceledContext(t *testing.T) {
	plugin := &Plugin{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := plugin.GetConfigSpec(ctx); err == nil {
		t.Fatal("GetConfigSpec with canceled context: err = nil, want non-nil")
	}
}

func TestGetLaunchCommandAppendsModelFlag(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "copilot"}

	cmd, err := plugin.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		Config: ports.AgentConfig{Model: "claude-sonnet-4.5"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"copilot", "--model", "claude-sonnet-4.5"}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("cmd = %#v, want %#v", cmd, want)
	}
}

func TestGetLaunchCommandOmitsBlankModel(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "copilot"}

	cmd, err := plugin.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		Config: ports.AgentConfig{Model: "   "},
	})
	if err != nil {
		t.Fatal(err)
	}
	if contains(cmd, "--model") {
		t.Fatalf("command %#v unexpectedly contains --model for blank config", cmd)
	}
}

// Restore path intentionally does not forward a configured model override —
// see appendModelFlag's doc comment (issue #2895; --model + --resume
// composition could not be verified — the test account's Copilot Free plan
// only grants "auto" regardless of --model). This test locks that decision
// in so a future "just mirror the launch path" edit doesn't silently wire
// it without re-verifying.
func TestGetRestoreCommandDoesNotForwardModelOverride(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "copilot"}

	cmd, ok, err := plugin.GetRestoreCommand(context.Background(), ports.RestoreConfig{
		Config: ports.AgentConfig{Model: "claude-sonnet-4.5"},
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
	want := []string{"copilot", "--resume", "uuid-123"}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("cmd = %#v, want %#v (model override must not be forwarded on restore)", cmd, want)
	}
}

func TestCopilotNativeBinaryForNpmLoader(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("npm loader native binary naming is covered on Unix-like platforms")
	}
	dir := t.TempDir()
	packageDir := filepath.Join(dir, "lib", "node_modules", "@github", "copilot")
	binDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(filepath.Join(packageDir, "node_modules", ".bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	loader := filepath.Join(packageDir, "npm-loader.js")
	if err := os.WriteFile(loader, []byte("#!/usr/bin/env node\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	native := filepath.Join(packageDir, "node_modules", ".bin", "copilot-"+runtime.GOOS+"-"+runtime.GOARCH)
	if err := os.WriteFile(native, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(binDir, "copilot")
	if err := os.Symlink(loader, link); err != nil {
		t.Fatal(err)
	}

	want, err := filepath.EvalSymlinks(native)
	if err != nil {
		t.Fatal(err)
	}
	got, err := filepath.EvalSymlinks(copilotNativeBinaryForLoader(link))
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("native binary = %q, want %q", got, want)
	}
}

func TestResolveCopilotBinaryFallbacks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix fallback candidate shape")
	}
	oldUnixPaths := copilotUnixPaths
	copilotUnixPaths = nil
	t.Cleanup(func() { copilotUnixPaths = oldUnixPaths })

	tests := []struct {
		name string
		seed func(t *testing.T, home string) string
	}{
		{
			name: "npm global",
			seed: func(t *testing.T, home string) string {
				return writeExecutable(t, filepath.Join(home, ".npm-global", "bin", "copilot"))
			},
		},
		{
			name: "nvm",
			seed: func(t *testing.T, home string) string {
				return writeExecutable(t, filepath.Join(home, ".nvm", "versions", "node", "v22.23.1", "bin", "copilot"))
			},
		},
		{
			name: "npm loader resolves to native",
			seed: func(t *testing.T, home string) string {
				packageDir := filepath.Join(home, ".npm-global", "lib", "node_modules", "@github", "copilot")
				binDir := filepath.Join(home, ".npm-global", "bin")
				if err := os.MkdirAll(filepath.Join(packageDir, "node_modules", ".bin"), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.MkdirAll(binDir, 0o755); err != nil {
					t.Fatal(err)
				}
				loader := filepath.Join(packageDir, "npm-loader.js")
				if err := os.WriteFile(loader, []byte("#!/usr/bin/env node\n"), 0o755); err != nil {
					t.Fatal(err)
				}
				native := writeExecutable(t, filepath.Join(packageDir, "node_modules", ".bin", "copilot-"+runtime.GOOS+"-"+runtime.GOARCH))
				link := filepath.Join(binDir, "copilot")
				if err := os.Symlink(loader, link); err != nil {
					t.Fatal(err)
				}
				return native
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("PATH", t.TempDir())
			t.Setenv("VOLTA_HOME", filepath.Join(home, ".volta"))
			t.Setenv("FNM_DIR", "")
			want := tt.seed(t, home)

			got, err := ResolveCopilotBinary(context.Background())
			if err != nil {
				t.Fatalf("ResolveCopilotBinary: %v", err)
			}
			got, err = filepath.EvalSymlinks(got)
			if err != nil {
				t.Fatal(err)
			}
			want, err = filepath.EvalSymlinks(want)
			if err != nil {
				t.Fatal(err)
			}
			if got != want {
				t.Fatalf("ResolveCopilotBinary = %q, want %q", got, want)
			}
		})
	}
}

func writeExecutable(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestAuthStatusAuthorizedFromEnv(t *testing.T) {
	clearCopilotAuthEnv(t)
	t.Setenv("GH_TOKEN", "github_pat_test")
	plugin := &Plugin{resolvedBinary: "copilot"}

	got, err := plugin.AuthStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != ports.AgentAuthStatusAuthorized {
		t.Fatalf("AuthStatus = %q, want %q", got, ports.AgentAuthStatusAuthorized)
	}
}

func TestCopilotClassicPATIsNotAnAuthorizationSignal(t *testing.T) {
	clearCopilotAuthEnv(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("GH_TOKEN", "ghp_classic_token")

	status, ok, err := copilotLocalAuthStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ok || status != ports.AgentAuthStatusUnknown {
		t.Fatalf("status = (%q, %v), want (%q, false)", status, ok, ports.AgentAuthStatusUnknown)
	}
}

func TestCopilotConfigAuthStatusAuthorizedWithPlainTextToken(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(configPath, []byte(`{"authToken":"token"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	status, ok, err := copilotConfigAuthStatus(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || status != ports.AgentAuthStatusAuthorized {
		t.Fatalf("status = (%q, %v), want (%q, true)", status, ok, ports.AgentAuthStatusAuthorized)
	}
}

func TestCopilotConfigAuthStatusUnknownWithEmptyConfig(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(configPath, []byte(" \n"), 0o600); err != nil {
		t.Fatal(err)
	}

	status, ok, err := copilotConfigAuthStatus(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if ok || status != ports.AgentAuthStatusUnknown {
		t.Fatalf("status = (%q, %v), want (%q, false)", status, ok, ports.AgentAuthStatusUnknown)
	}
}

func TestCopilotConfigAuthStatusDoesNotTreatAuthModeAsCredential(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(configPath, []byte(`{"authMode":"github"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	status, ok, err := copilotConfigAuthStatus(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if ok || status != ports.AgentAuthStatusUnknown {
		t.Fatalf("status = (%q, %v), want (%q, false)", status, ok, ports.AgentAuthStatusUnknown)
	}
}

func clearCopilotAuthEnv(t *testing.T) {
	t.Helper()
	for _, name := range copilotTokenEnvVars {
		t.Setenv(name, "")
	}
}

func TestGetRestoreCommandReadsAgentSessionID(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "copilot"}

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
	want := []string{"copilot", "--allow-all-tools", "--resume", "uuid-123"}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("restore cmd\nwant: %#v\n got: %#v", want, cmd)
	}
}

func TestGetRestoreCommandSelectsSessionCustomAgent(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "copilot"}
	promptFile := filepath.Join(t.TempDir(), "system.md")
	if err := os.WriteFile(promptFile, []byte("restore AO rules"), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd, ok, err := plugin.GetRestoreCommand(context.Background(), ports.RestoreConfig{
		SystemPromptFile: promptFile,
		Session: ports.SessionRef{
			ID:       "mer-1",
			Metadata: map[string]string{ports.MetadataKeyAgentSessionID: "uuid-123"},
		},
	})
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if !ok {
		t.Fatal("ok = false, want true")
	}
	want := []string{"copilot", "--agent=ao-mer-1", "--resume", "uuid-123"}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("restore cmd\nwant: %#v\n got: %#v", want, cmd)
	}
}

func TestGetRestoreCommandSelectsSessionCustomAgentFromPromptFile(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "copilot"}
	promptFile := filepath.Join(t.TempDir(), "system.md")
	if err := os.WriteFile(promptFile, []byte("orchestrator must spawn workers"), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd, ok, err := plugin.GetRestoreCommand(context.Background(), ports.RestoreConfig{
		SystemPromptFile: promptFile,
		Session: ports.SessionRef{
			ID:       "mer-1",
			Metadata: map[string]string{ports.MetadataKeyAgentSessionID: "uuid-123"},
		},
	})
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if !ok {
		t.Fatal("ok = false, want true")
	}
	if !contains(cmd, "--agent=ao-mer-1") {
		t.Fatalf("restore command %#v does not select session custom agent", cmd)
	}
}

func TestGetRestoreCommandFalseWithoutAgentSessionID(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "copilot"}

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
	plugin := &Plugin{resolvedBinary: "copilot"}

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
		t.Fatalf("Metadata = %#v, want nil for Copilot", info.Metadata)
	}
}

func TestSessionInfoFalseWhenNoHookMetadata(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "copilot"}

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

func TestGetAgentHooksInstallsCopilotHooks(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "copilot"}
	workspace := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workspace, ".git", "info"), 0o755); err != nil {
		t.Fatal(err)
	}

	hooksPath := copilotHooksPath(workspace)
	if err := os.MkdirAll(filepath.Dir(hooksPath), 0o755); err != nil {
		t.Fatal(err)
	}
	// Seed a user-owned agentStop hook plus an unrelated top-level field; both
	// must survive install.
	existing := `{"version":1,"disableAllHooks":false,"hooks":{"agentStop":[{"type":"command","bash":"custom stop hook","powershell":"custom stop hook"}]}}`
	if err := os.WriteFile(hooksPath, []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := ports.WorkspaceHookConfig{
		DataDir:       t.TempDir(),
		SessionID:     "sess-1",
		WorkspacePath: workspace,
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
	var file copilotHookFile
	if err := json.Unmarshal(data, &file); err != nil {
		t.Fatal(err)
	}
	if file.Version != copilotHooksVersion {
		t.Fatalf("version = %d, want %d", file.Version, copilotHooksVersion)
	}
	if file.DisableAllHooks == nil || *file.DisableAllHooks {
		t.Fatalf("disableAllHooks not preserved: %#v", file.DisableAllHooks)
	}
	for _, spec := range copilotManagedHooks {
		command := copilotHookCommandPrefix + spec.Command
		if count := countCopilotHookCommand(file.Hooks[spec.Event], command); count != 1 {
			t.Fatalf("%s command count = %d, want 1 in %#v", spec.Event, count, file.Hooks[spec.Event])
		}
	}
	if countCopilotHookCommand(file.Hooks["agentStop"], "custom stop hook") != 1 {
		t.Fatalf("existing agentStop hook was not preserved: %#v", file.Hooks["agentStop"])
	}
}

func TestInstallAgentProfileInstallsSessionCopilotAgent(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "copilot"}
	workspace := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workspace, ".git", "info"), 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := ports.WorkspaceHookConfig{
		DataDir:       t.TempDir(),
		SessionID:     "sess-1",
		SystemPrompt:  "orchestrator must spawn workers",
		WorkspacePath: workspace,
	}
	if err := plugin.GetAgentHooks(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(workspace, "AGENTS.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("root AGENTS.md exists or stat failed: %v", err)
	}
	exclude, err := os.ReadFile(filepath.Join(workspace, ".git", "info", "exclude"))
	if err != nil {
		t.Fatal(err)
	}
	agentPath := filepath.Join(workspace, ".github", "agents", "ao-sess-1.agent.md")
	agentData, err := os.ReadFile(agentPath)
	if err != nil {
		t.Fatal(err)
	}
	agentText := string(agentData)
	if !strings.HasPrefix(agentText, "---\n") {
		t.Fatalf("agent profile must start with YAML frontmatter for Copilot discovery:\n%s", agentText)
	}
	for _, want := range []string{
		copilotAgentSentinel,
		"name: ao-sess-1",
		"target: github-copilot",
		"orchestrator must spawn workers",
	} {
		if !strings.Contains(agentText, want) {
			t.Fatalf("agent profile missing %q:\n%s", want, agentText)
		}
	}
	if !strings.Contains(string(exclude), "/.github/agents/ao-sess-1.agent.md\n") {
		t.Fatalf("git exclude does not ignore custom agent:\n%s", exclude)
	}
}

func TestInstallAgentProfileDoesNotInstallLifecycleHooks(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "copilot"}
	workspace := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workspace, ".git", "info"), 0o755); err != nil {
		t.Fatal(err)
	}
	systemPromptFile := filepath.Join(t.TempDir(), "system.md")
	if err := os.WriteFile(systemPromptFile, []byte("review without modifying files\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := plugin.InstallAgentProfile(context.Background(), ports.WorkspaceHookConfig{
		DataDir:          t.TempDir(),
		SessionID:        "review-sess-1",
		SystemPromptFile: systemPromptFile,
		WorkspacePath:    workspace,
	})
	if err != nil {
		t.Fatalf("InstallAgentProfile: %v", err)
	}
	profilePath := filepath.Join(workspace, ".github", "agents", "ao-review-sess-1.agent.md")
	data, err := os.ReadFile(profilePath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "review without modifying files") {
		t.Fatalf("hidden system prompt missing from profile:\n%s", data)
	}
	if _, err := os.Stat(copilotHooksPath(workspace)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("profile-only install created lifecycle hooks: %v", err)
	}
	exclude, err := os.ReadFile(filepath.Join(workspace, ".git", "info", "exclude"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(exclude), "/.github/agents/ao-review-sess-1.agent.md\n") {
		t.Fatalf("profile is not git-excluded:\n%s", exclude)
	}
}

func TestInstallAgentProfilePreservesUserOwnedProfile(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "copilot"}
	workspace := t.TempDir()
	profilePath := filepath.Join(workspace, ".github", "agents", "ao-review-sess-1.agent.md")
	if err := os.MkdirAll(filepath.Dir(profilePath), 0o755); err != nil {
		t.Fatal(err)
	}
	const userProfile = "---\nname: user-owned\n---\nkeep me\n"
	if err := os.WriteFile(profilePath, []byte(userProfile), 0o600); err != nil {
		t.Fatal(err)
	}

	err := plugin.InstallAgentProfile(context.Background(), ports.WorkspaceHookConfig{
		SessionID:     "review-sess-1",
		SystemPrompt:  "AO replacement",
		WorkspacePath: workspace,
	})
	if err != nil {
		t.Fatalf("InstallAgentProfile: %v", err)
	}
	data, err := os.ReadFile(profilePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != userProfile {
		t.Fatalf("user-owned profile changed:\n%s", data)
	}
}

func TestGetAgentHooksIgnoresSessionCopilotAgentInLinkedWorktreeCommonExclude(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "copilot"}
	dir := t.TempDir()
	commonGitDir := filepath.Join(dir, "repo", ".git")
	worktreeGitDir := filepath.Join(commonGitDir, "worktrees", "sess-1")
	workspace := filepath.Join(dir, "worktree")
	if err := os.MkdirAll(worktreeGitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, ".git"), []byte("gitdir: "+worktreeGitDir+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktreeGitDir, "commondir"), []byte("../..\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := ports.WorkspaceHookConfig{
		DataDir:       t.TempDir(),
		SessionID:     "sess-1",
		SystemPrompt:  "orchestrator must spawn workers",
		WorkspacePath: workspace,
	}
	if err := plugin.GetAgentHooks(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}

	exclude, err := os.ReadFile(filepath.Join(commonGitDir, "info", "exclude"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(exclude), "/.github/agents/ao-sess-1.agent.md\n") {
		t.Fatalf("common git exclude does not ignore custom agent:\n%s", exclude)
	}
	if _, err := os.Stat(filepath.Join(worktreeGitDir, "info", "exclude")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("worktree-local exclude exists or stat failed: %v", err)
	}
}

func TestInstallAgentProfileUpdatesManagedSessionCopilotAgent(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "copilot"}
	workspace := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workspace, ".git", "info"), 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := ports.WorkspaceHookConfig{DataDir: t.TempDir(), SessionID: "sess-1", SystemPrompt: "old rules", WorkspacePath: workspace}
	if err := plugin.InstallAgentProfile(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	cfg.SystemPrompt = "new rules"
	if err := plugin.InstallAgentProfile(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(workspace, ".github", "agents", "ao-sess-1.agent.md"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if strings.Contains(text, "old rules") || !strings.Contains(text, "new rules") {
		t.Fatalf("AGENTS.md was not updated:\n%s", text)
	}
}

func TestGetAgentHooksDoesNotOverwriteProjectCopilotInstructions(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "copilot"}
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "AGENTS.md"), []byte("project-owned rules\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := ports.WorkspaceHookConfig{
		DataDir:       t.TempDir(),
		SessionID:     "sess-1",
		SystemPrompt:  "ao rules",
		WorkspacePath: workspace,
	}
	if err := plugin.GetAgentHooks(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(workspace, "AGENTS.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "project-owned rules\n" {
		t.Fatalf("project AGENTS.md was overwritten:\n%s", data)
	}
}

func TestUninstallHooksRemovesCopilotHooks(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "copilot"}
	workspace := t.TempDir()
	hooksPath := copilotHooksPath(workspace)

	ctx := context.Background()
	cfg := ports.WorkspaceHookConfig{DataDir: t.TempDir(), SessionID: "sess-1", WorkspacePath: workspace}

	// Pre-seed a user's own agentStop hook; it must survive uninstall.
	if err := os.MkdirAll(filepath.Dir(hooksPath), 0o755); err != nil {
		t.Fatal(err)
	}
	existing := `{"version":1,"hooks":{"agentStop":[{"type":"command","bash":"custom stop hook","powershell":"custom stop hook"}]}}`
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
	var file copilotHookFile
	if err := json.Unmarshal(data, &file); err != nil {
		t.Fatal(err)
	}
	for _, spec := range copilotManagedHooks {
		command := copilotHookCommandPrefix + spec.Command
		if got := countCopilotHookCommand(file.Hooks[spec.Event], command); got != 0 {
			t.Fatalf("%s command %q count = %d after uninstall, want 0", spec.Event, command, got)
		}
	}
	if countCopilotHookCommand(file.Hooks["agentStop"], "custom stop hook") != 1 {
		t.Fatalf("user agentStop hook not preserved: %#v", file.Hooks["agentStop"])
	}
}

func TestAreHooksInstalledMissingFile(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "copilot"}
	installed, err := plugin.AreHooksInstalled(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if installed {
		t.Fatal("AreHooksInstalled on empty workspace = true, want false")
	}
}

func TestHookMethodsRequireWorkspacePath(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "copilot"}
	ctx := context.Background()

	if err := plugin.GetAgentHooks(ctx, ports.WorkspaceHookConfig{}); err == nil {
		t.Fatal("GetAgentHooks with empty WorkspacePath: err = nil, want non-nil")
	}
	if err := plugin.UninstallHooks(ctx, ""); err == nil {
		t.Fatal("UninstallHooks with empty path: err = nil, want non-nil")
	}
	if _, err := plugin.AreHooksInstalled(ctx, ""); err == nil {
		t.Fatal("AreHooksInstalled with empty path: err = nil, want non-nil")
	}
}

// TestCopilotManagedHooksUseDocumentedEventNames pins the JSON keys AO writes
// into .github/hooks/ao.json to the camelCase names Copilot CLI documents
// (https://docs.github.com/en/copilot/how-tos/copilot-cli/customize-copilot/use-hooks).
// Drifting back to lowercase-dashed or any other casing silently disables the
// hooks, so this is a tripwire for that class of regression.
func TestCopilotManagedHooksUseDocumentedEventNames(t *testing.T) {
	wantEventByCommand := map[string]string{
		"session-start":      "sessionStart",
		"user-prompt-submit": "userPromptSubmitted",
		"permission-request": "preToolUse",
		"stop":               "agentStop",
	}
	if len(copilotManagedHooks) != len(wantEventByCommand) {
		t.Fatalf("copilotManagedHooks length = %d, want %d", len(copilotManagedHooks), len(wantEventByCommand))
	}
	for _, spec := range copilotManagedHooks {
		want, ok := wantEventByCommand[spec.Command]
		if !ok {
			t.Fatalf("unexpected AO sub-command %q in copilotManagedHooks", spec.Command)
		}
		if spec.Event != want {
			t.Fatalf("command %q event = %q, want %q (Copilot CLI documented camelCase)", spec.Command, spec.Event, want)
		}
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

func countCopilotHookCommand(entries []copilotHookEntry, command string) int {
	count := 0
	for _, entry := range entries {
		if entry.Bash == command || entry.Powershell == command {
			count++
		}
	}
	return count
}
