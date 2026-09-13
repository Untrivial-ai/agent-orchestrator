package goose

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	osexec "os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/binaryutil"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/hooksjson"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	aoprocess "github.com/aoagents/agent-orchestrator/backend/internal/process"
)

func TestManifestIDIsGoose(t *testing.T) {
	m := New().Manifest()
	if m.ID != "goose" {
		t.Fatalf("Manifest().ID = %q, want %q", m.ID, "goose")
	}
	if m.Name != "Goose" {
		t.Fatalf("Manifest().Name = %q, want %q", m.Name, "Goose")
	}
	if len(m.Capabilities) != 1 || m.Capabilities[0] != "agent" {
		t.Fatalf("Manifest().Capabilities = %#v, want [agent]", m.Capabilities)
	}
}

func TestGetLaunchCommandBuildsArgv(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "goose"}

	cmd, err := plugin.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		Permissions:  ports.PermissionModeBypassPermissions,
		Prompt:       "-fix this",
		SystemPrompt: "be terse",
	})
	if err != nil {
		t.Fatal(err)
	}

	want := []string{
		"env", "GOOSE_MODE=auto",
		"goose", "run",
		"--system", "be terse",
		"-t", "", "--interactive",
	}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("unexpected command\nwant: %#v\n got: %#v", want, cmd)
	}
	if contains(cmd, "-fix this") {
		t.Fatalf("command %#v unexpectedly contains prompt text", cmd)
	}
}

func TestGetLaunchCommandPrefersInlineSystemPrompt(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "prompt.md")
	if err := os.WriteFile(file, []byte("  from file  \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	plugin := &Plugin{resolvedBinary: "goose"}

	cmd, err := plugin.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		SystemPromptFile: file,
		SystemPrompt:     "inline wins",
		Prompt:           "do work",
	})
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"goose", "run", "--system", "inline wins", "-t", "", "--interactive"}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("unexpected command\nwant: %#v\n got: %#v", want, cmd)
	}
}

func TestGetLaunchCommandAlwaysLaunchesInteractive(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "goose"}

	cmd, err := plugin.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		SystemPrompt: "coordinate this project",
	})
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"goose", "run", "--system", "coordinate this project", "-t", "", "--interactive"}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("unexpected command\nwant: %#v\n got: %#v", want, cmd)
	}
}

func TestGetLaunchCommandMapsApprovalModes(t *testing.T) {
	tests := []struct {
		name        string
		permission  ports.PermissionMode
		want        []string
		notExpected string
	}{
		{
			name:        "default",
			permission:  ports.PermissionModeDefault,
			notExpected: "env",
		},
		{
			name:       "accept-edits",
			permission: ports.PermissionModeAcceptEdits,
			want:       []string{"env", "GOOSE_MODE=smart_approve"},
		},
		{
			name:       "auto",
			permission: ports.PermissionModeAuto,
			want:       []string{"env", "GOOSE_MODE=auto"},
		},
		{
			name:       "bypass-permissions",
			permission: ports.PermissionModeBypassPermissions,
			want:       []string{"env", "GOOSE_MODE=auto"},
		},
		{
			name:        "empty",
			permission:  "",
			notExpected: "env",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plugin := &Plugin{resolvedBinary: "goose"}
			cmd, err := plugin.GetLaunchCommand(context.Background(), ports.LaunchConfig{
				Permissions: tt.permission,
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(tt.want) > 0 && !containsSubsequence(cmd, tt.want) {
				t.Fatalf("command %#v does not contain %#v", cmd, tt.want)
			}
			if tt.notExpected != "" && contains(cmd, tt.notExpected) {
				t.Fatalf("command %#v contains %q", cmd, tt.notExpected)
			}
		})
	}
}

func TestGetPromptDeliveryStrategyIsAfterStart(t *testing.T) {
	plugin := &Plugin{}

	got, err := plugin.GetPromptDeliveryStrategy(context.Background(), ports.LaunchConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if got != ports.PromptDeliveryAfterStart {
		t.Fatalf("unexpected strategy: %q", got)
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
			Description: "Model override passed to `goose run --model`.",
		},
	}
	if !reflect.DeepEqual(spec.Fields, want) {
		t.Fatalf("config fields\nwant: %#v\n got: %#v", want, spec.Fields)
	}
}

func TestGetLaunchCommandAppendsConfiguredModel(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "goose"}

	cmd, err := plugin.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		Config: ports.AgentConfig{Model: "  claude-4-sonnet  "},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !containsSubsequence(cmd, []string{"--model", "claude-4-sonnet"}) {
		t.Fatalf("command %#v missing trimmed --model flag", cmd)
	}
	if containsSubsequence(cmd, []string{"--model", "  claude-4-sonnet  "}) {
		t.Fatalf("command %#v used untrimmed model", cmd)
	}
}

func TestGetLaunchCommandOmitsBlankConfiguredModel(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "goose"}

	cmd, err := plugin.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		Config: ports.AgentConfig{Model: " \t "},
	})
	if err != nil {
		t.Fatal(err)
	}
	if contains(cmd, "--model") {
		t.Fatalf("command %#v contains --model for blank model", cmd)
	}
}

func TestGetRestoreCommandAppendsConfiguredModel(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "goose"}

	cmd, ok, err := plugin.GetRestoreCommand(context.Background(), ports.RestoreConfig{
		Config: ports.AgentConfig{Model: "  claude-4-sonnet  "},
		Session: ports.SessionRef{
			Metadata: map[string]string{ports.MetadataKeyAgentSessionID: "20260720_1"},
		},
	})
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if !ok {
		t.Fatal("ok = false, want true")
	}
	if !containsSubsequence(cmd, []string{"--model", "claude-4-sonnet"}) {
		t.Fatalf("restore command %#v missing trimmed --model flag", cmd)
	}
}

func TestAuthStatusAuthorizedFromEnv(t *testing.T) {
	clearGooseAuthEnv(t)
	t.Setenv("OPENROUTER_API_KEY", "test-key")
	plugin := &Plugin{resolvedBinary: "goose"}

	got, err := plugin.AuthStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != ports.AgentAuthStatusAuthorized {
		t.Fatalf("AuthStatus = %q, want %q", got, ports.AgentAuthStatusAuthorized)
	}
}

func TestAuthStatusAuthorizedFromGooseConfig(t *testing.T) {
	clearGooseAuthEnv(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	configPath := filepath.Join(home, ".config", "goose", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte("GOOSE_PROVIDER__API_KEY: test-key\nproviders:\n  openrouter:\n    model: anthropic/claude-sonnet-4\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	plugin := &Plugin{resolvedBinary: "goose"}

	got, err := plugin.AuthStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != ports.AgentAuthStatusAuthorized {
		t.Fatalf("AuthStatus = %q, want %q", got, ports.AgentAuthStatusAuthorized)
	}
}

func TestAuthStatusUnknownFromEmptyGooseConfig(t *testing.T) {
	clearGooseAuthEnv(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	configPath := filepath.Join(home, ".config", "goose", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte(" \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	plugin := &Plugin{resolvedBinary: "goose"}

	got, err := plugin.AuthStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != ports.AgentAuthStatusUnknown {
		t.Fatalf("AuthStatus = %q, want %q", got, ports.AgentAuthStatusUnknown)
	}
}

func clearGooseAuthEnv(t *testing.T) {
	t.Helper()
	for _, name := range gooseAPIKeyEnvVars {
		t.Setenv(name, "")
	}
}

func TestContextCancellationIsHonored(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "goose"}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := plugin.GetConfigSpec(ctx); err == nil {
		t.Fatal("GetConfigSpec: expected error from cancelled context")
	}
	if _, err := plugin.GetPromptDeliveryStrategy(ctx, ports.LaunchConfig{}); err == nil {
		t.Fatal("GetPromptDeliveryStrategy: expected error from cancelled context")
	}
	if _, _, err := plugin.GetRestoreCommand(ctx, ports.RestoreConfig{}); err == nil {
		t.Fatal("GetRestoreCommand: expected error from cancelled context")
	}
	if _, _, err := plugin.SessionInfo(ctx, ports.SessionRef{}); err == nil {
		t.Fatal("SessionInfo: expected error from cancelled context")
	}
	if err := plugin.GetAgentHooks(ctx, ports.WorkspaceHookConfig{WorkspacePath: "/tmp"}); err == nil {
		t.Fatal("GetAgentHooks: expected error from cancelled context")
	}
}

func TestGetAgentHooksInstallsGooseHooks(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "goose"}
	workspace := t.TempDir()
	hooksPath := gooseHooksPath(workspace)
	if err := os.MkdirAll(filepath.Dir(hooksPath), 0o755); err != nil {
		t.Fatal(err)
	}
	existing := `{"hooks":{"Stop":[{"matcher":null,"hooks":[{"type":"command","command":"custom stop hook","timeout":3}]}]}}`
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
	var config gooseHookFile
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	if config.Hooks == nil {
		t.Fatalf("hooks config missing hooks object: %#v", config)
	}
	for _, spec := range gooseManagedHooks {
		entries := config.Hooks[spec.Event]
		if count := countGooseHookCommand(entries, spec.Command); count != 1 {
			t.Fatalf("%s command count = %d, want 1 in %#v", spec.Event, count, entries)
		}
	}
	stopEntries := config.Hooks["Stop"]
	if countGooseHookCommand(stopEntries, "custom stop hook") != 1 {
		t.Fatalf("existing Stop hook was not preserved: %#v", stopEntries)
	}
}

func TestUninstallHooksRemovesGooseHooks(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "goose"}
	workspace := t.TempDir()
	hooksPath := gooseHooksPath(workspace)

	ctx := context.Background()
	cfg := ports.WorkspaceHookConfig{DataDir: t.TempDir(), SessionID: "sess-1", WorkspacePath: workspace}

	// Pre-seed a user's own Stop hook; it must survive uninstall.
	if err := os.MkdirAll(filepath.Dir(hooksPath), 0o755); err != nil {
		t.Fatal(err)
	}
	existing := `{"hooks":{"Stop":[{"matcher":null,"hooks":[{"type":"command","command":"custom stop hook","timeout":3}]}]}}`
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
	var config gooseHookFile
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	for _, spec := range gooseManagedHooks {
		if got := countGooseHookCommand(config.Hooks[spec.Event], spec.Command); got != 0 {
			t.Fatalf("%s command %q count = %d after uninstall, want 0", spec.Event, spec.Command, got)
		}
	}
	if countGooseHookCommand(config.Hooks["Stop"], "custom stop hook") != 1 {
		t.Fatalf("user Stop hook not preserved: %#v", config.Hooks["Stop"])
	}
}

func TestGetAgentHooksRequiresWorkspacePath(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "goose"}
	if err := plugin.GetAgentHooks(context.Background(), ports.WorkspaceHookConfig{}); err == nil {
		t.Fatal("expected error when WorkspacePath is empty")
	}
}

func TestGetRestoreCommandReadsAgentSessionID(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "goose"}

	cmd, ok, err := plugin.GetRestoreCommand(context.Background(), ports.RestoreConfig{
		Permissions:      ports.PermissionModeAuto,
		SystemPrompt:     "restore inline wins",
		SystemPromptFile: filepath.Join(t.TempDir(), "missing.md"),
		Session: ports.SessionRef{
			Metadata: map[string]string{ports.MetadataKeyAgentSessionID: "20260720_1"},
		},
	})
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if !ok {
		t.Fatal("ok = false, want true")
	}
	want := []string{
		"env", "GOOSE_MODE=auto",
		"goose", "run", "--system", "restore inline wins", "--resume", "--session-id", "20260720_1",
	}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("restore cmd\nwant: %#v\n got: %#v", want, cmd)
	}
	if contains(cmd, "-t") || contains(cmd, "restore original task") {
		t.Fatalf("restore command %#v unexpectedly replays the original task", cmd)
	}
}

func TestGetRestoreCommandFalseWithoutAgentSessionID(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "goose"}

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
	plugin := &Plugin{resolvedBinary: "goose"}

	info, ok, err := plugin.SessionInfo(context.Background(), ports.SessionRef{
		WorkspacePath: "/some/path",
		Metadata: map[string]string{
			ports.MetadataKeyAgentSessionID: "thread-123",
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
	if info.AgentSessionID != "thread-123" {
		t.Fatalf("AgentSessionID = %q, want native id", info.AgentSessionID)
	}
	if info.Title != "Fix login redirect" {
		t.Fatalf("Title = %q, want hook title", info.Title)
	}
	if info.Summary != "Updated the auth callback and tests." {
		t.Fatalf("Summary = %q, want hook summary", info.Summary)
	}
	if info.Metadata != nil {
		t.Fatalf("Metadata = %#v, want nil for Goose", info.Metadata)
	}
}

func TestSessionInfoFalseWhenNoHookMetadata(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "goose"}

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

func TestOfficialGooseBinaryRequiresPositiveBlockHelpSignature(t *testing.T) {
	tests := []struct {
		name string
		out  string
		err  error
		want bool
	}{
		{
			name: "Block Goose",
			out:  "Usage: goose [OPTIONS] <COMMAND>\n\nCommands:\n  session  Manage sessions\n  recipe   Manage recipes\n",
			want: true,
		},
		{
			name: "Pressly Goose",
			out:  "Usage: goose [command]\n\nCommands:\n  up       Migrate up\n  down     Migrate down\n  status   Migration status\n  create   Create a migration\n",
		},
		{
			name: "malformed help",
			out:  "goose is not the expected CLI\n",
		},
		{
			name: "non-zero output",
			out:  "Commands:\n  session\n  recipe\n",
			err:  errors.New("exit status 1"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			previous := gooseIdentityCommand
			gooseIdentityCommand = func(context.Context, string, ...string) ([]byte, error) {
				return []byte(tt.out), tt.err
			}
			t.Cleanup(func() { gooseIdentityCommand = previous })

			if got := isOfficialGooseBinary(context.Background(), testGooseBinaryPath()); got != tt.want {
				t.Fatalf("isOfficialGooseBinary() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestOfficialGooseBinaryProbeIsBounded(t *testing.T) {
	previousTimeout := gooseIdentityProbeTimeout
	gooseIdentityProbeTimeout = 20 * time.Millisecond
	t.Cleanup(func() { gooseIdentityProbeTimeout = previousTimeout })
	previousCommand := gooseIdentityCommand
	gooseIdentityCommand = func(ctx context.Context, _ string, _ ...string) ([]byte, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	t.Cleanup(func() { gooseIdentityCommand = previousCommand })

	started := time.Now()
	if got := isOfficialGooseBinary(context.Background(), testGooseBinaryPath()); got {
		t.Fatal("timed-out identity probe was accepted")
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("identity probe took %s, want bounded probe", elapsed)
	}
}

func TestGooseIdentityCommandBoundsDescendantHoldingOutputPipes(t *testing.T) {
	const (
		modeEnv   = "AO_TEST_GOOSE_WAIT_DELAY_MODE"
		readyEnv  = "AO_TEST_GOOSE_WAIT_DELAY_READY"
		signalEnv = "AO_TEST_GOOSE_WAIT_DELAY_SIGNAL"
		doneEnv   = "AO_TEST_GOOSE_WAIT_DELAY_DONE"
	)
	dir := t.TempDir()
	ready := filepath.Join(dir, "ready")
	signal := filepath.Join(dir, "stop")
	done := filepath.Join(dir, "done")
	t.Setenv(modeEnv, "parent")
	t.Setenv(readyEnv, ready)
	t.Setenv(signalEnv, signal)
	t.Setenv(doneEnv, done)
	t.Cleanup(func() {
		_ = os.WriteFile(signal, nil, 0o600)
		waitForGooseTestFile(done, time.Second)
	})

	started := time.Now()
	result := make(chan struct {
		out []byte
		err error
	}, 1)
	go func() {
		out, err := gooseIdentityCommand(context.Background(), os.Args[0], "-test.run=^TestGooseIdentityPipeHolder$", "--")
		result <- struct {
			out []byte
			err error
		}{out: out, err: err}
	}()
	if !waitForGooseTestFile(ready, time.Second) {
		t.Fatal("pipe-holder subprocess did not start")
	}

	select {
	case got := <-result:
		if !errors.Is(got.err, osexec.ErrWaitDelay) {
			t.Fatalf("identity command error = %v, want exec.ErrWaitDelay", got.err)
		}
		if !strings.Contains(string(got.out), "session recipe") {
			t.Fatalf("identity command output = %q, want helper marker", got.out)
		}
		if elapsed := time.Since(started); elapsed > 2*time.Second {
			t.Fatalf("identity command returned after %s, want bounded pipe cleanup", elapsed)
		}
	case <-time.After(3 * time.Second):
		_ = os.WriteFile(signal, nil, 0o600)
		got := <-result
		t.Fatalf("identity command exceeded bound: err=%v output=%q", got.err, got.out)
	}
}

// TestGooseIdentityPipeHolder is a subprocess fixture for the production
// identity command. The parent exits while its child retains both output
// descriptors; the test above then proves exec.Cmd.WaitDelay closes the
// command's pipes instead of waiting for that descendant indefinitely.
func TestGooseIdentityPipeHolder(t *testing.T) {
	const (
		modeEnv   = "AO_TEST_GOOSE_WAIT_DELAY_MODE"
		readyEnv  = "AO_TEST_GOOSE_WAIT_DELAY_READY"
		signalEnv = "AO_TEST_GOOSE_WAIT_DELAY_SIGNAL"
		doneEnv   = "AO_TEST_GOOSE_WAIT_DELAY_DONE"
	)
	switch os.Getenv(modeEnv) {
	case "child":
		for {
			if _, err := os.Stat(os.Getenv(signalEnv)); err == nil {
				_ = os.WriteFile(os.Getenv(doneEnv), nil, 0o600)
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	case "parent":
		child := aoprocess.Command(os.Args[0], "-test.run=^TestGooseIdentityPipeHolder$", "--")
		child.Env = replaceGooseTestEnv(os.Environ(), modeEnv, "child")
		child.Stdout = os.Stdout
		child.Stderr = os.Stderr
		if err := child.Start(); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(os.Getenv(readyEnv), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		_, _ = fmt.Fprintln(os.Stdout, "session recipe")
		_, _ = fmt.Fprintln(os.Stderr, "session recipe")
	default:
		t.Skip("subprocess fixture")
	}
}

func replaceGooseTestEnv(env []string, key, value string) []string {
	prefix := key + "="
	for i, item := range env {
		if strings.HasPrefix(item, prefix) {
			env[i] = prefix + value
			return env
		}
	}
	return append(env, prefix+value)
}

func waitForGooseTestFile(path string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}

func TestOfficialGooseBinaryProbeHonorsCallerCancellation(t *testing.T) {
	previousCommand := gooseIdentityCommand
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{})
	gooseIdentityCommand = func(ctx context.Context, _ string, _ ...string) ([]byte, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	t.Cleanup(func() { gooseIdentityCommand = previousCommand })
	result := make(chan bool, 1)
	run := func() { result <- isOfficialGooseBinary(ctx, testGooseBinaryPath()) }
	go run()
	select {
	case <-started:
		cancel()
	case <-time.After(500 * time.Millisecond):
		t.Fatal("identity probe did not start")
	}
	select {
	case got := <-result:
		if got {
			t.Fatal("cancelled identity probe was accepted")
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("identity probe did not stop after cancellation")
	}
}

func TestResolveGooseBinaryUsesValidFallbackAfterPathCollision(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("native Windows candidate policy is tested without launching shims")
	}
	home := t.TempDir()
	pathDir := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", pathDir)
	t.Setenv("VOLTA_HOME", "")
	t.Setenv("FNM_DIR", "")

	foreign := filepath.Join(pathDir, "goose")
	valid := filepath.Join(home, ".local", "share", "mise", "shims", "goose")
	writeGooseIdentityFixture(t, foreign, `#!/bin/sh
printf '%s\n' 'Usage: goose [command]' '  up  Migrate up' '  down  Migrate down'
`)
	writeGooseIdentityFixture(t, valid, `#!/bin/sh
printf '%s\n' 'Usage: goose [OPTIONS] <COMMAND>' 'Commands:' '  session  Manage sessions' '  recipe  Manage recipes'
`)

	got, err := binaryutil.ResolveBinary(context.Background(), binaryutil.BinarySpec{
		Label:            "goose",
		Names:            []string{"goose"},
		ValidateIdentity: isOfficialGooseBinary,
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got != valid {
		t.Fatalf("resolved binary = %q, want valid fallback %q", got, valid)
	}
}

func TestResolveGooseBinaryHangingPathCollisionStillReachesFallback(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("native Windows candidate policy is tested without launching shims")
	}
	home := t.TempDir()
	pathDir := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", pathDir)
	t.Setenv("VOLTA_HOME", "")
	t.Setenv("FNM_DIR", "")
	foreign := filepath.Join(pathDir, "goose")
	valid := filepath.Join(home, ".local", "share", "mise", "shims", "goose")
	writeGooseIdentityFixture(t, foreign, "#!/bin/sh\n")
	writeGooseIdentityFixture(t, valid, "#!/bin/sh\n")

	previousTimeout := gooseIdentityProbeTimeout
	gooseIdentityProbeTimeout = 20 * time.Millisecond
	t.Cleanup(func() { gooseIdentityProbeTimeout = previousTimeout })
	previousCommand := gooseIdentityCommand
	gooseIdentityCommand = func(ctx context.Context, binary string, _ ...string) ([]byte, error) {
		switch binary {
		case foreign:
			<-ctx.Done()
			return nil, ctx.Err()
		case valid:
			return []byte("Commands:\n  session\n  recipe\n"), nil
		default:
			return nil, nil
		}
	}
	t.Cleanup(func() { gooseIdentityCommand = previousCommand })

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	got, err := ResolveGooseBinary(ctx)
	if err != nil {
		t.Fatalf("ResolveGooseBinary: %v", err)
	}
	if got != valid {
		t.Fatalf("resolved binary = %q, want valid fallback %q", got, valid)
	}
}

func TestResolveGooseBinaryPresenceDoesNotRunIdentityProbe(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("PATH fixture uses a Unix executable name")
	}
	pathDir := t.TempDir()
	t.Setenv("PATH", pathDir)
	t.Setenv("HOME", t.TempDir())
	writeGooseIdentityFixture(t, filepath.Join(pathDir, "goose"), "#!/bin/sh\n")

	previousCommand := gooseIdentityCommand
	called := false
	gooseIdentityCommand = func(context.Context, string, ...string) ([]byte, error) {
		called = true
		return nil, nil
	}
	t.Cleanup(func() { gooseIdentityCommand = previousCommand })

	_, err := (&Plugin{}).ResolveBinaryPresence(context.Background())
	if !errors.Is(err, ports.ErrAgentBinaryIdentityUnknown) {
		t.Fatalf("ResolveBinaryPresence() error = %v, want identity-unknown", err)
	}
	if called {
		t.Fatal("ResolveBinaryPresence() ran an identity process")
	}
}

func TestResolveGooseBinaryPresenceDoesNotTrustReplacedCachedIdentity(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("PATH fixture uses a Unix executable name")
	}
	pathDir := t.TempDir()
	t.Setenv("PATH", pathDir)
	t.Setenv("HOME", t.TempDir())
	binary := filepath.Join(pathDir, "goose")
	writeGooseIdentityFixture(t, binary, `#!/bin/sh
printf '%s\n' 'Usage: goose [OPTIONS] <COMMAND>' 'Commands:' '  session  Manage sessions' '  recipe  Manage recipes'
`)

	previousCommand := gooseIdentityCommand
	probeCalls := 0
	gooseIdentityCommand = func(context.Context, string, ...string) ([]byte, error) {
		probeCalls++
		return []byte("Commands:\n  session\n  recipe\n"), nil
	}
	t.Cleanup(func() { gooseIdentityCommand = previousCommand })

	plugin := &Plugin{}
	if got, err := plugin.ResolveBinary(context.Background()); err != nil || got != binary {
		t.Fatalf("ResolveBinary() = (%q, %v), want cached Block binary %q", got, err, binary)
	}
	if err := os.Remove(binary); err != nil {
		t.Fatal(err)
	}
	writeGooseIdentityFixture(t, binary, `#!/bin/sh
printf '%s\n' 'Usage: goose [command]' '  up  Migrate up' '  down  Migrate down'
`)

	got, err := plugin.ResolveBinaryPresence(context.Background())
	if !errors.Is(err, ports.ErrAgentBinaryIdentityUnknown) || got != "" {
		t.Fatalf("ResolveBinaryPresence() = (%q, %v), want empty identity-unknown result", got, err)
	}
	if probeCalls != 1 {
		t.Fatalf("presence check ran %d identity probes, want only the initial fresh probe", probeCalls)
	}
}

func TestWindowsGooseIdentityOnlyAcceptsNativeExecutables(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{path: `C:\Users\tester\goose.exe`, want: true},
		{path: `C:\Users\tester\goose.EXE`, want: true},
		{path: `C:\Users\tester\goose.cmd`},
		{path: `C:\Users\tester\goose.bat`},
		{path: `C:\Users\tester\goose`},
	}
	for _, tt := range tests {
		if got := isNativelyLaunchableWindowsGoose(tt.path); got != tt.want {
			t.Errorf("isNativelyLaunchableWindowsGoose(%q) = %v, want %v", tt.path, got, tt.want)
		}
	}
	if runtime.GOOS == "windows" {
		previousCommand := gooseIdentityCommand
		called := false
		gooseIdentityCommand = func(context.Context, string, ...string) ([]byte, error) {
			called = true
			return nil, nil
		}
		t.Cleanup(func() { gooseIdentityCommand = previousCommand })
		if isOfficialGooseBinary(context.Background(), `C:\Users\tester\goose.cmd`) || called {
			t.Fatal("Windows shim reached the native Goose identity probe")
		}
	}
	if !reflect.DeepEqual(gooseBinarySpec.WinNames, []string{"goose.exe"}) {
		t.Fatalf("Windows PATH names = %#v, want only native goose.exe", gooseBinarySpec.WinNames)
	}
	for _, candidate := range gooseBinarySpec.WinPaths {
		if filepath.Ext(filepath.Join(candidate.Parts...)) != ".exe" {
			t.Fatalf("Windows fallback candidate %#v is not native", candidate)
		}
	}
}

func writeGooseIdentityFixture(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o755); err != nil {
		t.Fatal(err)
	}
}

func testGooseBinaryPath() string {
	if runtime.GOOS == "windows" {
		return `C:\goose.exe`
	}
	return "/usr/local/bin/goose"
}

func TestResolveGooseBinaryFallback(t *testing.T) {
	// When the binary is not on PATH or any well-known location, the resolver
	// MUST surface ports.ErrAgentBinaryNotFound rather than a silent string
	// fallback that lets a missing CLI launch into an empty tmux pane.
	bin, err := ResolveGooseBinary(context.Background())
	if err != nil {
		if !errors.Is(err, ports.ErrAgentBinaryNotFound) {
			t.Fatalf("err = %v, want ports.ErrAgentBinaryNotFound", err)
		}
		return
	}
	if bin == "" {
		t.Fatal("ResolveGooseBinary returned empty path with no error")
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

// gooseHookFile is the on-disk shape of the hooks file, used to decode and
// assert on what GetAgentHooks wrote.
type gooseHookFile struct {
	Hooks map[string][]hooksjson.MatcherGroup `json:"hooks"`
}

func countGooseHookCommand(entries []hooksjson.MatcherGroup, command string) int {
	count := 0
	for _, entry := range entries {
		for _, hook := range entry.Hooks {
			if hook.Command == command {
				count++
			}
		}
	}
	return count
}
