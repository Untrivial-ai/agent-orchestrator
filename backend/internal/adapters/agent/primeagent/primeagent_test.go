package primeagent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestManifest(t *testing.T) {
	m := New().Manifest()
	if m.ID != "prime-agent" || m.Name != "Prime Agent" {
		t.Fatalf("manifest identity = (%q, %q), want (%q, %q)", m.ID, m.Name, "prime-agent", "Prime Agent")
	}
	if !reflect.DeepEqual(m.Capabilities, []adapters.Capability{adapters.CapabilityAgent}) {
		t.Fatalf("capabilities = %#v, want agent capability", m.Capabilities)
	}
}

func TestGetConfigSpecReportsModel(t *testing.T) {
	spec, err := New().GetConfigSpec(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []ports.ConfigField{{
		Key:         "model",
		Type:        ports.ConfigFieldString,
		Description: "Model override passed to `prime-agent --model`.",
	}}
	if !reflect.DeepEqual(spec.Fields, want) {
		t.Fatalf("fields = %#v, want %#v", spec.Fields, want)
	}
}

func TestGetLaunchCommandOrdersFlagsAndProtectsPrompt(t *testing.T) {
	dataDir := t.TempDir()
	p := &Plugin{resolvedBinary: "/bin/prime-agent"}
	cmd, err := p.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		Config:       ports.AgentConfig{Model: "  prime/model  "},
		DataDir:      dataDir,
		Permissions:  ports.PermissionModeBypassPermissions,
		Prompt:       "--delete-nothing",
		SystemPrompt: "follow repository rules",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"/bin/prime-agent",
		"--session-dir", filepath.Join(dataDir, "agent-runtime", "prime-agent", "sessions"),
		"--extension", filepath.Join(dataDir, "agent-runtime", "prime-agent", "ao-activity.ts"),
		"--append-system-prompt", "follow repository rules",
		"--model", "prime/model",
		"--", "--delete-nothing",
	}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("command\n got: %#v\nwant: %#v", cmd, want)
	}
}

func TestGetLaunchCommandOmitsBlankOptionalArguments(t *testing.T) {
	dataDir := t.TempDir()
	p := &Plugin{resolvedBinary: "prime-agent"}
	cmd, err := p.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		Config:       ports.AgentConfig{Model: " \t "},
		DataDir:      dataDir,
		SystemPrompt: " \n ",
		Prompt:       "",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"prime-agent",
		"--session-dir", filepath.Join(dataDir, "agent-runtime", "prime-agent", "sessions"),
		"--extension", filepath.Join(dataDir, "agent-runtime", "prime-agent", "ao-activity.ts"),
	}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("command = %#v, want %#v", cmd, want)
	}
}

func TestGetLaunchCommandReadsSystemPromptFileAndPrefersInline(t *testing.T) {
	file := filepath.Join(t.TempDir(), "system.md")
	if err := os.WriteFile(file, []byte("instructions from file"), 0o600); err != nil {
		t.Fatal(err)
	}
	p := &Plugin{resolvedBinary: "prime-agent"}

	fromFile, err := p.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		DataDir:          t.TempDir(),
		SystemPromptFile: file,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := fromFile[len(fromFile)-2:]; !reflect.DeepEqual(got, []string{"--append-system-prompt", "instructions from file"}) {
		t.Fatalf("file system prompt argv = %#v", fromFile)
	}

	inline, err := p.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		DataDir:          t.TempDir(),
		SystemPrompt:     "inline wins",
		SystemPromptFile: filepath.Join(t.TempDir(), "missing.md"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := inline[len(inline)-2:]; !reflect.DeepEqual(got, []string{"--append-system-prompt", "inline wins"}) {
		t.Fatalf("inline system prompt argv = %#v", inline)
	}
}

func TestGetLaunchCommandOmitsWhitespaceSystemPromptFile(t *testing.T) {
	file := filepath.Join(t.TempDir(), "system.md")
	if err := os.WriteFile(file, []byte(" \n\t "), 0o600); err != nil {
		t.Fatal(err)
	}
	p := &Plugin{resolvedBinary: "prime-agent"}
	cmd, err := p.GetLaunchCommand(context.Background(), ports.LaunchConfig{DataDir: t.TempDir(), SystemPromptFile: file})
	if err != nil {
		t.Fatal(err)
	}
	if len(cmd) != 5 {
		t.Fatalf("command = %#v, want only required arguments", cmd)
	}
}

func TestGetLaunchCommandReportsSystemPromptFileError(t *testing.T) {
	p := &Plugin{resolvedBinary: "prime-agent"}
	missing := filepath.Join(t.TempDir(), "missing.md")
	_, err := p.GetLaunchCommand(context.Background(), ports.LaunchConfig{DataDir: t.TempDir(), SystemPromptFile: missing})
	if err == nil {
		t.Fatal("expected unreadable system prompt error")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("error = %v, want wrapped os.ErrNotExist", err)
	}
}

func TestGetLaunchCommandIgnoresEveryPermissionMode(t *testing.T) {
	modes := []ports.PermissionMode{
		ports.PermissionModeDefault,
		ports.PermissionModeAcceptEdits,
		ports.PermissionModeAuto,
		ports.PermissionModeBypassPermissions,
		"future-mode",
	}
	for _, mode := range modes {
		t.Run(string(mode), func(t *testing.T) {
			p := &Plugin{resolvedBinary: "prime-agent"}
			cmd, err := p.GetLaunchCommand(context.Background(), ports.LaunchConfig{DataDir: t.TempDir(), Permissions: mode})
			if err != nil {
				t.Fatal(err)
			}
			if len(cmd) != 5 {
				t.Fatalf("command = %#v, want no permission argument", cmd)
			}
		})
	}
}

func TestCapabilities(t *testing.T) {
	p := New()
	if !p.SteersActiveTurn() {
		t.Fatal("SteersActiveTurn = false, want true")
	}
	if !p.EmitsSubmitActivity() {
		t.Fatal("EmitsSubmitActivity = false, want true")
	}
	if p.EmitsBlockedActivity() {
		t.Fatal("EmitsBlockedActivity = true, want false")
	}
}

func TestGetRestoreCommandUsesNativeSessionAndRoleConfig(t *testing.T) {
	dataDir := t.TempDir()
	p := &Plugin{resolvedBinary: "/bin/prime-agent"}
	cmd, ok, err := p.GetRestoreCommand(context.Background(), ports.RestoreConfig{
		DataDir:      dataDir,
		SystemPrompt: "coordinate workers only",
		Config:       ports.AgentConfig{Model: " anthropic/claude-opus-4-8 "},
		Session: ports.SessionRef{
			WorkspacePath: "/work/session",
			Metadata: map[string]string{
				ports.MetadataKeyAgentSessionID: " prime-session-123 ",
			},
		},
	})
	if err != nil || !ok {
		t.Fatalf("GetRestoreCommand ok=%v err=%v", ok, err)
	}
	want := []string{
		"/bin/prime-agent",
		"--session-dir", filepath.Join(dataDir, "agent-runtime", "prime-agent", "sessions"),
		"--extension", filepath.Join(dataDir, "agent-runtime", "prime-agent", "ao-activity.ts"),
		"--append-system-prompt", "coordinate workers only",
		"--model", "anthropic/claude-opus-4-8",
		"--resume", "prime-session-123",
	}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("command\n got: %#v\nwant: %#v", cmd, want)
	}
}

func TestGetRestoreCommandReadsSystemPromptFile(t *testing.T) {
	file := filepath.Join(t.TempDir(), "system.md")
	if err := os.WriteFile(file, []byte("restored role instructions"), 0o600); err != nil {
		t.Fatal(err)
	}
	p := &Plugin{resolvedBinary: "prime-agent"}
	cmd, ok, err := p.GetRestoreCommand(context.Background(), ports.RestoreConfig{
		DataDir:          t.TempDir(),
		SystemPromptFile: file,
		Session: ports.SessionRef{Metadata: map[string]string{
			ports.MetadataKeyAgentSessionID: "native-7",
		}},
	})
	if err != nil || !ok {
		t.Fatalf("GetRestoreCommand ok=%v err=%v", ok, err)
	}
	if got := cmd[len(cmd)-4:]; !reflect.DeepEqual(got, []string{
		"--append-system-prompt", "restored role instructions", "--resume", "native-7",
	}) {
		t.Fatalf("restore prompt argv = %#v", cmd)
	}
}

func TestGetRestoreCommandUnavailableWithoutNativeSessionID(t *testing.T) {
	tests := []ports.SessionRef{
		{},
		{Metadata: map[string]string{}},
		{Metadata: map[string]string{ports.MetadataKeyAgentSessionID: " \t "}},
	}
	for i, session := range tests {
		cmd, ok, err := New().GetRestoreCommand(context.Background(), ports.RestoreConfig{Session: session})
		if err != nil || ok || cmd != nil {
			t.Fatalf("case %d: GetRestoreCommand = (%#v, %v, %v), want (nil, false, nil)", i, cmd, ok, err)
		}
	}
}

func TestContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p := New()
	if _, err := p.GetConfigSpec(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("GetConfigSpec error = %v", err)
	}
	if _, err := p.GetLaunchCommand(ctx, ports.LaunchConfig{DataDir: t.TempDir()}); !errors.Is(err, context.Canceled) {
		t.Fatalf("GetLaunchCommand error = %v", err)
	}
	if _, _, err := p.GetRestoreCommand(ctx, ports.RestoreConfig{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("GetRestoreCommand error = %v", err)
	}
	if _, err := ResolvePrimeAgentBinary(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("ResolvePrimeAgentBinary error = %v", err)
	}
}

func TestResolvePrimeAgentBinaryFromPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("executable fixture uses Unix permissions")
	}
	binDir := t.TempDir()
	path := filepath.Join(binDir, "prime-agent")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir)
	got, err := ResolvePrimeAgentBinary(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != path {
		t.Fatalf("resolved path = %q, want %q", got, path)
	}
}

func TestResolvePrimeAgentBinaryFromNPMGlobalHome(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix npm-global fallback")
	}
	home := t.TempDir()
	path := filepath.Join(home, ".npm-global", "bin", "prime-agent")
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("PATH", t.TempDir())

	original := primeAgentBinarySpec
	primeAgentBinarySpec.UnixPaths = nil
	primeAgentBinarySpec.NodeManaged = false
	t.Cleanup(func() { primeAgentBinarySpec = original })

	got, err := ResolvePrimeAgentBinary(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != path {
		t.Fatalf("resolved path = %q, want npm-global fallback %q", got, path)
	}
}

func TestResolvePrimeAgentBinaryNotFound(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	original := primeAgentBinarySpec
	primeAgentBinarySpec.Names = []string{"definitely-missing-prime-agent"}
	primeAgentBinarySpec.WinNames = []string{"definitely-missing-prime-agent"}
	primeAgentBinarySpec.UnixPaths = nil
	primeAgentBinarySpec.UnixHomePaths = nil
	primeAgentBinarySpec.WinPaths = nil
	primeAgentBinarySpec.NodeManaged = false
	t.Cleanup(func() { primeAgentBinarySpec = original })

	_, err := ResolvePrimeAgentBinary(context.Background())
	if !errors.Is(err, ports.ErrAgentBinaryNotFound) {
		t.Fatalf("ResolvePrimeAgentBinary error = %v, want ports.ErrAgentBinaryNotFound", err)
	}
}
