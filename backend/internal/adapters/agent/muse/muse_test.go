package muse

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/binaryutil"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestManifest(t *testing.T) {
	m := (&Plugin{}).Manifest()
	if m.ID != "muse" || m.Name != "Muse Code" {
		t.Fatalf("manifest = %#v, want muse/Muse Code", m)
	}
	for _, capability := range m.Capabilities {
		if capability == adapters.CapabilityAgent {
			return
		}
	}
	t.Fatal("manifest missing CapabilityAgent")
}

func TestMuseBinarySpecIncludesOfficialInstallerPath(t *testing.T) {
	want := []string{".local", "bin", "muse"}
	for _, path := range museBinarySpec.UnixHomePaths {
		if reflect.DeepEqual(path, want) {
			return
		}
	}
	t.Fatalf("UnixHomePaths = %#v, want %v", museBinarySpec.UnixHomePaths, want)
}

func TestGetConfigSpecAdvertisesModelField(t *testing.T) {
	spec, err := (&Plugin{}).GetConfigSpec(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(spec.Fields) != 1 || spec.Fields[0].Key != "model" || spec.Fields[0].Type != ports.ConfigFieldString {
		t.Fatalf("fields = %#v, want model/string", spec.Fields)
	}
}

func TestGetLaunchCommandStartsInteractiveSession(t *testing.T) {
	p := &Plugin{resolvedBinary: "muse"}
	cmd, err := p.GetLaunchCommand(context.Background(), ports.LaunchConfig{})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"muse", "--trust-workspace"}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("cmd = %#v, want %#v", cmd, want)
	}
}

func TestGetLaunchCommandDeliversPromptPositionally(t *testing.T) {
	tests := []struct {
		name   string
		prompt string
	}{
		{name: "ordinary", prompt: "fix the tests"},
		{name: "multiline", prompt: "fix the tests\nthen report"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &Plugin{resolvedBinary: "muse"}
			cmd, err := p.GetLaunchCommand(context.Background(), ports.LaunchConfig{Prompt: tt.prompt})
			if err != nil {
				t.Fatal(err)
			}
			want := []string{"muse", "--trust-workspace", tt.prompt}
			if !reflect.DeepEqual(cmd, want) {
				t.Fatalf("cmd = %#v, want %#v", cmd, want)
			}
			for _, arg := range cmd {
				if arg == "exec" {
					t.Fatalf("cmd = %#v unexpectedly uses Muse's headless mode", cmd)
				}
			}
		})
	}
}

func TestGetLaunchCommandAppendsModelBeforePrompt(t *testing.T) {
	p := &Plugin{resolvedBinary: "muse"}
	cmd, err := p.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		Config: ports.AgentConfig{Model: "muse-spark"},
		Prompt: "fix it",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"muse", "--trust-workspace", "--model", "muse-spark", "fix it"}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("cmd = %#v, want %#v", cmd, want)
	}
}

func TestGetLaunchCommandInjectsSystemPromptWithoutProjectFiles(t *testing.T) {
	p := &Plugin{resolvedBinary: "muse"}
	cmd, err := p.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		SystemPrompt: "follow AO rules\n",
		Prompt:       "fix it",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"env", museDeveloperPromptEnvVar + "=follow AO rules",
		"muse", "--trust-workspace", "fix it",
	}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("cmd = %#v, want %#v", cmd, want)
	}
}

func TestGetLaunchCommandInjectsManagedHooksPath(t *testing.T) {
	dataDir := t.TempDir()
	p := &Plugin{resolvedBinary: "muse"}
	cmd, err := p.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		DataDir:   dataDir,
		SessionID: "agent-orchestrator-96",
		Prompt:    "fix it",
	})
	if err != nil {
		t.Fatal(err)
	}
	hooksPath, err := museManagedHooksPath(dataDir, "agent-orchestrator-96")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"env", museManagedHooksEnvVar + "=" + hooksPath,
		"muse", "--trust-workspace", "fix it",
	}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("cmd = %#v, want %#v", cmd, want)
	}
}

func TestGetLaunchCommandCombinesSystemPromptAndManagedHooksEnvironment(t *testing.T) {
	dataDir := t.TempDir()
	p := &Plugin{resolvedBinary: "muse"}
	cmd, err := p.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		DataDir:      dataDir,
		SessionID:    "sess-1",
		SystemPrompt: "follow AO rules",
	})
	if err != nil {
		t.Fatal(err)
	}
	hooksPath, err := museManagedHooksPath(dataDir, "sess-1")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"env",
		museDeveloperPromptEnvVar + "=follow AO rules",
		museManagedHooksEnvVar + "=" + hooksPath,
		"muse", "--trust-workspace",
	}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("cmd = %#v, want %#v", cmd, want)
	}
}

func TestGetLaunchCommandReadsSystemPromptFile(t *testing.T) {
	promptFile := filepath.Join(t.TempDir(), "system.md")
	if err := os.WriteFile(promptFile, []byte("file rules\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	p := &Plugin{resolvedBinary: "muse"}
	cmd, err := p.GetLaunchCommand(context.Background(), ports.LaunchConfig{SystemPromptFile: promptFile})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"env", museDeveloperPromptEnvVar + "=file rules", "muse", "--trust-workspace"}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("cmd = %#v, want %#v", cmd, want)
	}
}

func TestGetLaunchCommandMissingSystemPromptFileErrors(t *testing.T) {
	p := &Plugin{resolvedBinary: "muse"}
	_, err := p.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		SystemPromptFile: filepath.Join(t.TempDir(), "missing.md"),
	})
	if err == nil {
		t.Fatal("GetLaunchCommand succeeded with a missing system prompt file")
	}
}

func TestGetLaunchCommandMapsOfficialPermissionFlags(t *testing.T) {
	tests := []struct {
		name string
		mode ports.PermissionMode
		want []string
	}{
		{"default", ports.PermissionModeDefault, []string{"muse", "--trust-workspace"}},
		{"accept edits", ports.PermissionModeAcceptEdits, []string{"muse", "--trust-workspace", "--approval-mode", "never"}},
		{"auto", ports.PermissionModeAuto, []string{"muse", "--trust-workspace", "--approval-mode", "never"}},
		{"bypass", ports.PermissionModeBypassPermissions, []string{"muse", "--trust-workspace", "--yolo"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &Plugin{resolvedBinary: "muse"}
			cmd, err := p.GetLaunchCommand(context.Background(), ports.LaunchConfig{
				Permissions: tt.mode,
			})
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(cmd, tt.want) {
				t.Fatalf("cmd = %#v, want %#v", cmd, tt.want)
			}
		})
	}
}

func TestGetPromptDeliveryStrategy(t *testing.T) {
	strategy, err := (&Plugin{}).GetPromptDeliveryStrategy(context.Background(), ports.LaunchConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if strategy != ports.PromptDeliveryInCommand {
		t.Fatalf("strategy = %q, want %q", strategy, ports.PromptDeliveryInCommand)
	}
}

func TestWorkspaceHooksLeaveTrackedAgentsMDUnchanged(t *testing.T) {
	t.Setenv(aoRunFileEnvVar, "")
	workspace := t.TempDir()
	runGit(t, workspace, "init")
	runGit(t, workspace, "config", "user.name", "AO Test")
	runGit(t, workspace, "config", "user.email", "ao@example.invalid")
	path := filepath.Join(workspace, "AGENTS.md")
	want := []byte("project-owned instructions\n")
	if err := os.WriteFile(path, want, 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, workspace, "add", "AGENTS.md")
	runGit(t, workspace, "commit", "-m", "add project instructions")

	p := &Plugin{}
	dataDir := t.TempDir()
	cfg := ports.WorkspaceHookConfig{
		DataDir: dataDir, SessionID: "sess-tracked",
		WorkspacePath: workspace, SystemPrompt: "AO-only instructions",
	}
	if err := p.GetAgentHooks(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	hooksPath, err := museManagedHooksPath(dataDir, cfg.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	assertMuseManagedHooks(t, hooksPath)
	if status := runGit(t, workspace, "status", "--porcelain"); status != "" {
		t.Fatalf("workspace became dirty before cleanup:\n%s", status)
	}
	if err := p.CleanupWorkspace(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(hooksPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("managed hooks stat after cleanup = %v, want not exist", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("AGENTS.md = %q, want unchanged %q", got, want)
	}
	if status := runGit(t, workspace, "status", "--porcelain"); status != "" {
		t.Fatalf("workspace became dirty:\n%s", status)
	}
}

func TestWorkspaceHooksDoNotCreateAgentsMD(t *testing.T) {
	t.Setenv(aoRunFileEnvVar, "")
	workspace := t.TempDir()
	p := &Plugin{}
	dataDir := t.TempDir()
	cfg := ports.WorkspaceHookConfig{
		DataDir: dataDir, SessionID: "sess-absent",
		WorkspacePath: workspace, SystemPrompt: "AO-only instructions",
	}
	if err := p.GetAgentHooks(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	hooksPath, err := museManagedHooksPath(dataDir, cfg.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	assertMuseManagedHooks(t, hooksPath)
	if _, err := os.Stat(filepath.Join(workspace, "AGENTS.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("AGENTS.md stat before cleanup = %v, want not exist", err)
	}
	if err := p.CleanupWorkspace(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	if err := p.CleanupWorkspace(context.Background(), cfg); err != nil {
		t.Fatalf("second cleanup: %v", err)
	}
	if _, err := os.Stat(filepath.Join(workspace, "AGENTS.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("AGENTS.md stat err = %v, want not exist", err)
	}
}

func TestManagedHookCommandsRestoreSanitizedAORoute(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "data dir")
	runFile := filepath.Join(t.TempDir(), "dev's running.json")
	t.Setenv(aoRunFileEnvVar, runFile)
	cfg := ports.WorkspaceHookConfig{DataDir: dataDir, SessionID: "sess-1"}
	if err := (&Plugin{}).GetAgentHooks(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	path, err := museManagedHooksPath(dataDir, cfg.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path) //nolint:gosec // test-owned path
	if err != nil {
		t.Fatal(err)
	}
	var file museHooksFile
	if err := json.Unmarshal(data, &file); err != nil {
		t.Fatal(err)
	}
	command := file.Hooks["UserPromptSubmit"][0].Hooks[0].Command
	want := "env AO_SESSION_ID='sess-1' AO_DATA_DIR=" + museShellQuote(dataDir) +
		" AO_RUN_FILE=" + museShellQuote(runFile) + " ao hooks muse user-prompt-submit"
	if command != want {
		t.Fatalf("command = %q, want %q", command, want)
	}
}

func TestManagedHooksRequireAOOwnedPathInputs(t *testing.T) {
	p := &Plugin{resolvedBinary: "muse"}
	for _, cfg := range []ports.WorkspaceHookConfig{
		{},
		{DataDir: t.TempDir()},
		{DataDir: t.TempDir(), SessionID: "../escape"},
	} {
		if err := p.GetAgentHooks(context.Background(), cfg); err == nil {
			t.Fatalf("GetAgentHooks(%+v) succeeded, want error", cfg)
		}
	}
}

func assertMuseManagedHooks(t *testing.T, path string) {
	t.Helper()
	data, err := os.ReadFile(path) //nolint:gosec // test-owned path
	if err != nil {
		t.Fatal(err)
	}
	var file museHooksFile
	if err := json.Unmarshal(data, &file); err != nil {
		t.Fatalf("decode managed hooks: %v", err)
	}
	want := map[string]string{
		"SessionStart":      "session-start",
		"UserPromptSubmit":  "user-prompt-submit",
		"PermissionRequest": "permission-request",
		"Stop":              "stop",
	}
	for nativeEvent, aoEvent := range want {
		groups := file.Hooks[nativeEvent]
		if len(groups) != 1 || len(groups[0].Hooks) != 1 {
			t.Fatalf("hooks[%q] = %#v, want one command", nativeEvent, groups)
		}
		hook := groups[0].Hooks[0]
		if hook.Type != "command" || !strings.HasSuffix(hook.Command, " "+museHookCommandPrefix+aoEvent) {
			t.Fatalf("hooks[%q] = %#v, want AO %q command", nativeEvent, hook, aoEvent)
		}
	}
}

func TestResolveMuseBinaryRejectsUnrelatedMuseCommand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture is Unix-only")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "muse")
	if err := os.WriteFile(path, []byte("#!/bin/sh\necho 'unrelated muse 1.0'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	_, err := resolveMuseBinary(context.Background(), binaryutil.BinarySpec{
		Label: "muse",
		Names: []string{"muse"},
	})
	if !errors.Is(err, ports.ErrAgentBinaryNotFound) {
		t.Fatalf("err = %v, want ErrAgentBinaryNotFound", err)
	}
}

func TestResolveMuseBinaryContinuesAfterPathCollision(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture is Unix-only")
	}
	pathDir := t.TempDir()
	shadow := filepath.Join(pathDir, "muse")
	if err := os.WriteFile(shadow, []byte("#!/bin/sh\necho 'unrelated muse 1.0'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	officialDir := t.TempDir()
	official := filepath.Join(officialDir, "muse")
	if err := os.WriteFile(official, []byte("#!/bin/sh\necho 'Muse Code 0.1.0 (0.1.0-R708.1)'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", pathDir)

	got, err := resolveMuseBinary(context.Background(), binaryutil.BinarySpec{
		Label:     "muse",
		Names:     []string{"muse"},
		UnixPaths: []string{official},
	})
	if err != nil || got != official {
		t.Fatalf("resolveMuseBinary = (%q, %v), want (%q, nil)", got, err, official)
	}
}

func TestResolveMuseBinaryAcceptsOfficialVersionSignature(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture is Unix-only")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "muse")
	if err := os.WriteFile(path, []byte("#!/bin/sh\necho 'Muse Code 0.1.0 (0.1.0-R708.1)'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	got, err := ResolveMuseBinary(context.Background())
	if err != nil || got != path {
		t.Fatalf("ResolveMuseBinary = (%q, %v), want (%q, nil)", got, err, path)
	}
}

func TestGetRestoreCommandUsesMuseNativeSessionID(t *testing.T) {
	p := &Plugin{resolvedBinary: "muse"}
	cmd, ok, err := p.GetRestoreCommand(context.Background(), ports.RestoreConfig{
		Session: ports.SessionRef{
			Metadata: map[string]string{ports.MetadataKeyAgentSessionID: "muse-native-1"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("ok = false, want true")
	}
	want := []string{"muse", "--trust-workspace", "resume", "muse-native-1"}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("cmd = %#v, want %#v", cmd, want)
	}
}

func TestGetRestoreCommandPreservesModel(t *testing.T) {
	p := &Plugin{resolvedBinary: "muse"}
	cmd, ok, err := p.GetRestoreCommand(context.Background(), ports.RestoreConfig{
		Config: ports.AgentConfig{Model: "muse-spark"},
		Session: ports.SessionRef{
			Metadata: map[string]string{ports.MetadataKeyAgentSessionID: "muse-native-1"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("ok = false, want true")
	}
	want := []string{"muse", "--trust-workspace", "--model", "muse-spark", "resume", "muse-native-1"}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("cmd = %#v, want %#v", cmd, want)
	}
}

// TestGetRestoreCommandAppendsResumeTimePrompt covers resuming a reviewer
// after it was killed and re-triggered: the new task must be embedded in the
// resume argv (mirroring GetLaunchCommand), or the resumed session would sit
// idle with no work to act on.
func TestGetRestoreCommandAppendsResumeTimePrompt(t *testing.T) {
	p := &Plugin{resolvedBinary: "muse"}
	cmd, ok, err := p.GetRestoreCommand(context.Background(), ports.RestoreConfig{
		Prompt: "review the new commit",
		Session: ports.SessionRef{
			Metadata: map[string]string{ports.MetadataKeyAgentSessionID: "muse-native-1"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("ok = false, want true")
	}
	want := []string{"muse", "--trust-workspace", "resume", "muse-native-1", "review the new commit"}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("cmd = %#v, want %#v", cmd, want)
	}
}

func TestGetRestoreCommandInjectsPromptAndManagedHooksEnvironment(t *testing.T) {
	dataDir := t.TempDir()
	p := &Plugin{resolvedBinary: "muse"}
	cmd, ok, err := p.GetRestoreCommand(context.Background(), ports.RestoreConfig{
		DataDir:      dataDir,
		SystemPrompt: "follow AO rules\n",
		Session: ports.SessionRef{
			ID:       "agent-orchestrator-96",
			Metadata: map[string]string{ports.MetadataKeyAgentSessionID: "muse-native-1"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("ok = false, want true")
	}
	hooksPath, err := museManagedHooksPath(dataDir, "agent-orchestrator-96")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"env",
		museDeveloperPromptEnvVar + "=follow AO rules",
		museManagedHooksEnvVar + "=" + hooksPath,
		"muse", "--trust-workspace", "resume", "muse-native-1",
	}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("cmd = %#v, want %#v", cmd, want)
	}
}

func TestGetRestoreCommandRequiresAgentSessionID(t *testing.T) {
	tests := []struct {
		name    string
		session ports.SessionRef
	}{
		{"missing metadata", ports.SessionRef{}},
		{"missing agent session metadata", ports.SessionRef{Metadata: map[string]string{}}},
		{"blank agent session metadata", ports.SessionRef{Metadata: map[string]string{ports.MetadataKeyAgentSessionID: "   "}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &Plugin{resolvedBinary: "muse"}
			cmd, ok, err := p.GetRestoreCommand(context.Background(), ports.RestoreConfig{Session: tt.session})
			if err != nil || ok || cmd != nil {
				t.Fatalf("restore = (%#v, %v, %v), want (nil, false, nil)", cmd, ok, err)
			}
		})
	}
}

func TestSessionInfoReturnsAgentSessionID(t *testing.T) {
	info, ok, err := (&Plugin{}).SessionInfo(context.Background(), ports.SessionRef{
		Metadata: map[string]string{
			ports.MetadataKeyAgentSessionID: "muse-native-1",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("ok = false, want true")
	}
	want := ports.SessionInfo{AgentSessionID: "muse-native-1"}
	if !reflect.DeepEqual(info, want) {
		t.Fatalf("info = %#v, want %#v", info, want)
	}
}

func TestContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (&Plugin{}).GetConfigSpec(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("GetConfigSpec err = %v, want context.Canceled", err)
	}
	if _, err := (&Plugin{}).GetPromptDeliveryStrategy(ctx, ports.LaunchConfig{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("GetPromptDeliveryStrategy err = %v, want context.Canceled", err)
	}
	if err := (&Plugin{}).GetAgentHooks(ctx, ports.WorkspaceHookConfig{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("GetAgentHooks err = %v, want context.Canceled", err)
	}
	if err := (&Plugin{}).CleanupWorkspace(ctx, ports.WorkspaceHookConfig{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("CleanupWorkspace err = %v, want context.Canceled", err)
	}
	if _, _, err := (&Plugin{}).GetRestoreCommand(ctx, ports.RestoreConfig{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("GetRestoreCommand err = %v, want context.Canceled", err)
	}
	if _, _, err := (&Plugin{}).SessionInfo(ctx, ports.SessionRef{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("SessionInfo err = %v, want context.Canceled", err)
	}
	if _, err := ResolveMuseBinary(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("ResolveMuseBinary err = %v, want context.Canceled", err)
	}
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}
