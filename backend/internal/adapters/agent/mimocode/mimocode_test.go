package mimocode

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestManifestIdentifiesMiMoCodeAgent(t *testing.T) {
	m := New().Manifest()
	if m.ID != "mimo-code" || m.Name != "MiMo Code" {
		t.Fatalf("manifest = %#v, want MiMo Code identity", m)
	}
	if !reflect.DeepEqual(m.Capabilities, []adapters.Capability{adapters.CapabilityAgent}) {
		t.Fatalf("capabilities = %#v, want agent", m.Capabilities)
	}
}

func TestResolveMiMoCodeBinaryFindsOfficialCommand(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mimo")
	if runtime.GOOS == "windows" {
		path += ".cmd"
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("HOME", t.TempDir())

	got, err := ResolveMiMoCodeBinary(context.Background())
	if err != nil {
		t.Fatalf("ResolveMiMoCodeBinary: %v", err)
	}
	if got != path {
		t.Fatalf("ResolveMiMoCodeBinary = %q, want %q", got, path)
	}
}

func TestGetLaunchCommandMapsPromptModelAndPermissions(t *testing.T) {
	tests := []struct {
		name string
		mode ports.PermissionMode
		want []string
	}{
		{name: "default", mode: ports.PermissionModeDefault, want: []string{"mimo", "--trust", "--model", "mimo/mimo-v2-pro", "--agent", "ao-sess-1", "--prompt", "-fix this"}},
		{name: "accept edits", mode: ports.PermissionModeAcceptEdits, want: []string{"mimo", "--trust", "--model", "mimo/mimo-v2-pro", "--agent", "ao-sess-1", "--prompt", "-fix this"}},
		{name: "auto", mode: ports.PermissionModeAuto, want: []string{"mimo", "--trust", "--dangerously-skip-permissions", "--model", "mimo/mimo-v2-pro", "--agent", "ao-sess-1", "--prompt", "-fix this"}},
		{name: "bypass", mode: ports.PermissionModeBypassPermissions, want: []string{"mimo", "--trust", "--dangerously-skip-permissions", "--model", "mimo/mimo-v2-pro", "--agent", "ao-sess-1", "--prompt", "-fix this"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cmd, err := (&Plugin{resolvedBinary: "mimo"}).GetLaunchCommand(context.Background(), ports.LaunchConfig{
				SessionID: "sess/1", Prompt: "-fix this", SystemPrompt: "follow AO", Permissions: tc.mode,
				Config: ports.AgentConfig{Model: " mimo/mimo-v2-pro "},
			})
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(cmd, tc.want) {
				t.Fatalf("command\nwant: %#v\n got: %#v", tc.want, cmd)
			}
		})
	}
}

func TestGetRestoreCommandUsesExactNativeSessionAndReappliesSettings(t *testing.T) {
	cmd, ok, err := (&Plugin{resolvedBinary: "mimo"}).GetRestoreCommand(context.Background(), ports.RestoreConfig{
		Permissions: ports.PermissionModeAuto,
		Prompt:      "continue now",
		Config:      ports.AgentConfig{Model: "anthropic/claude-sonnet-4-6"},
		Session: ports.SessionRef{
			ID: "sess/1", Metadata: map[string]string{ports.MetadataKeyAgentSessionID: "ses_abc123"},
		},
		SystemPrompt: "follow AO",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("ok = false, want true")
	}
	want := []string{"mimo", "--trust", "--dangerously-skip-permissions", "--model", "anthropic/claude-sonnet-4-6", "--agent", "ao-sess-1", "--session", "ses_abc123", "--prompt", "continue now"}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("command\nwant: %#v\n got: %#v", want, cmd)
	}
}

func TestGetRestoreCommandRejectsMissingOrForeignNativeSessionID(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "mimo"}
	for _, id := range []string{"", "abc123", "ses_"} {
		t.Run(id, func(t *testing.T) {
			cmd, ok, err := plugin.GetRestoreCommand(context.Background(), ports.RestoreConfig{
				Session: ports.SessionRef{Metadata: map[string]string{ports.MetadataKeyAgentSessionID: id}},
			})
			if err != nil || ok || cmd != nil {
				t.Fatalf("GetRestoreCommand(%q) = (%#v, %v, %v), want (nil, false, nil)", id, cmd, ok, err)
			}
		})
	}
}

func TestPromptDeliveryAndModelConfig(t *testing.T) {
	p := New()
	strategy, err := p.GetPromptDeliveryStrategy(context.Background(), ports.LaunchConfig{})
	if err != nil || strategy != ports.PromptDeliveryInCommand {
		t.Fatalf("prompt strategy = (%q, %v), want in-command", strategy, err)
	}
	spec, err := p.GetConfigSpec(context.Background())
	if err != nil || len(spec.Fields) != 1 || spec.Fields[0].Key != "model" {
		t.Fatalf("config spec = (%#v, %v), want model field", spec, err)
	}
}
