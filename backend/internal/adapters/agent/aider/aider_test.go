package aider

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestManifest(t *testing.T) {
	m := (&Plugin{}).Manifest()
	if m.ID != "aider" {
		t.Fatalf("ID = %q, want aider", m.ID)
	}
	if m.Name != "Aider" {
		t.Fatalf("Name = %q, want Aider", m.Name)
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

func TestGetConfigSpecReportsModel(t *testing.T) {
	spec, err := (&Plugin{}).GetConfigSpec(context.Background())
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(spec.Fields) != 1 || spec.Fields[0].Key != "model" {
		t.Fatalf("unexpected fields: %#v", spec.Fields)
	}
}

func TestGetPromptDeliveryStrategy(t *testing.T) {
	s, err := (&Plugin{}).GetPromptDeliveryStrategy(context.Background(), ports.LaunchConfig{})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if s != ports.PromptDeliveryAfterStart {
		t.Fatalf("strategy = %q, want %q", s, ports.PromptDeliveryAfterStart)
	}
}

func TestGetLaunchCommandOmitsPromptForInteractiveDelivery(t *testing.T) {
	p := &Plugin{resolvedBinary: "aider"}
	cmd, err := p.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		Prompt: "add a health check",
	})
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"aider", "--no-check-update", "--no-stream", "--no-pretty", "--notifications", "--notifications-command", "ao hooks aider notification"}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("unexpected command\nwant: %#v\n got: %#v", want, cmd)
	}
	for _, arg := range cmd {
		if arg == "-m" || arg == "add a health check" {
			t.Fatalf("cmd = %#v unexpectedly contains prompt argv", cmd)
		}
	}
}

func TestGetLaunchCommandForwardsModel(t *testing.T) {
	p := &Plugin{resolvedBinary: "aider"}
	cmd, err := p.GetLaunchCommand(context.Background(), ports.LaunchConfig{Config: ports.AgentConfig{Model: "  openai/gpt-5.4  "}})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"aider", "--model", "openai/gpt-5.4", "--no-check-update", "--no-stream", "--no-pretty", "--notifications", "--notifications-command", "ao hooks aider notification"}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("cmd = %#v, want %#v", cmd, want)
	}
}

func TestGetLaunchCommandOmitsPromptFlagWhenEmpty(t *testing.T) {
	p := &Plugin{resolvedBinary: "aider"}
	cmd, err := p.GetLaunchCommand(context.Background(), ports.LaunchConfig{})
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"aider", "--no-check-update", "--no-stream", "--no-pretty", "--notifications", "--notifications-command", "ao hooks aider notification"}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("cmd = %#v, want %#v", cmd, want)
	}
	for _, arg := range cmd {
		if arg == "-m" {
			t.Fatalf("cmd = %#v unexpectedly contains -m for empty prompt", cmd)
		}
	}
}

func TestGetLaunchCommandAlwaysAppendsStableOutputFlags(t *testing.T) {
	p := &Plugin{resolvedBinary: "aider"}
	cmd, err := p.GetLaunchCommand(context.Background(), ports.LaunchConfig{Prompt: "do the thing"})
	if err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{"--no-check-update", "--no-stream", "--no-pretty"} {
		found := false
		for _, arg := range cmd {
			if arg == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("cmd = %#v missing stable output flag %q", cmd, want)
		}
	}
}

func TestGetLaunchCommandReportsWhenAiderReturnsToInput(t *testing.T) {
	p := &Plugin{resolvedBinary: "aider"}
	cmd, err := p.GetLaunchCommand(context.Background(), ports.LaunchConfig{})
	if err != nil {
		t.Fatal(err)
	}

	want := []string{
		"aider",
		"--no-check-update", "--no-stream", "--no-pretty",
		"--notifications", "--notifications-command", "ao hooks aider notification",
	}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("cmd = %#v, want %#v", cmd, want)
	}
}

func TestGetLaunchCommandMapsPermissionModes(t *testing.T) {
	tests := []struct {
		name       string
		mode       ports.PermissionMode
		wantFlags  []string
		wantAbsent []string
	}{
		{
			name:       "default omits approval flags",
			mode:       ports.PermissionModeDefault,
			wantFlags:  nil,
			wantAbsent: []string{"--yes-always", "--no-auto-commits"},
		},
		{
			name:       "empty omits approval flags",
			mode:       "",
			wantFlags:  nil,
			wantAbsent: []string{"--yes-always", "--no-auto-commits"},
		},
		{
			name:       "accept edits applies but leaves uncommitted",
			mode:       ports.PermissionModeAcceptEdits,
			wantFlags:  []string{"--yes-always", "--no-auto-commits"},
			wantAbsent: nil,
		},
		{
			name:       "auto applies and auto-commits",
			mode:       ports.PermissionModeAuto,
			wantFlags:  []string{"--yes-always"},
			wantAbsent: []string{"--no-auto-commits"},
		},
		{
			name:       "bypass collapses onto auto",
			mode:       ports.PermissionModeBypassPermissions,
			wantFlags:  []string{"--yes-always"},
			wantAbsent: []string{"--no-auto-commits"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &Plugin{resolvedBinary: "aider"}
			cmd, err := p.GetLaunchCommand(context.Background(), ports.LaunchConfig{
				Prompt:      "do the thing",
				Permissions: tt.mode,
			})
			if err != nil {
				t.Fatal(err)
			}

			for _, want := range tt.wantFlags {
				found := false
				for _, arg := range cmd {
					if arg == want {
						found = true
						break
					}
				}
				if !found {
					t.Fatalf("cmd = %#v missing expected flag %q", cmd, want)
				}
			}
			for _, absent := range tt.wantAbsent {
				for _, arg := range cmd {
					if arg == absent {
						t.Fatalf("cmd = %#v unexpectedly contains %q", cmd, absent)
					}
				}
			}
		})
	}
}

func TestGetLaunchCommandSystemPromptFileUsesReadOnlyContext(t *testing.T) {
	p := &Plugin{resolvedBinary: "aider"}
	cmd, err := p.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		Prompt:           "do the thing",
		SystemPromptFile: "/tmp/system.md",
	})
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"aider", "--no-check-update", "--no-stream", "--no-pretty", "--notifications", "--notifications-command", "ao hooks aider notification", "--read", "/tmp/system.md"}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("cmd = %#v, want %#v", cmd, want)
	}
}

func TestGetLaunchCommandInlineSystemPromptIsDropped(t *testing.T) {
	p := &Plugin{resolvedBinary: "aider"}
	cmd, err := p.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		Prompt:       "do the thing",
		SystemPrompt: "inline ignored",
	})
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"aider", "--no-check-update", "--no-stream", "--no-pretty", "--notifications", "--notifications-command", "ao hooks aider notification"}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("cmd = %#v, want %#v", cmd, want)
	}
	for _, arg := range cmd {
		if arg == "--read" {
			t.Fatalf("cmd = %#v unexpectedly contains --read for inline system prompt", cmd)
		}
		if arg == "inline ignored" {
			t.Fatalf("cmd = %#v unexpectedly contains inline system prompt text", cmd)
		}
	}
}

func TestGetRestoreCommandAlwaysFalse(t *testing.T) {
	p := &Plugin{}
	cmd, ok, err := p.GetRestoreCommand(context.Background(), ports.RestoreConfig{
		Session: ports.SessionRef{
			Metadata: map[string]string{ports.MetadataKeyAgentSessionID: "abc123"},
		},
		Permissions: ports.PermissionModeBypassPermissions,
	})
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatalf("ok=true, want false (aider has no resume-by-id)")
	}
	if cmd != nil {
		t.Fatalf("cmd = %#v, want nil", cmd)
	}
}

func TestGetAgentHooksNoOp(t *testing.T) {
	if err := (&Plugin{}).GetAgentHooks(context.Background(), ports.WorkspaceHookConfig{WorkspacePath: t.TempDir()}); err != nil {
		t.Fatalf("GetAgentHooks err = %v, want nil", err)
	}
}

func TestSessionInfoNoOp(t *testing.T) {
	info, ok, err := (&Plugin{}).SessionInfo(context.Background(), ports.SessionRef{
		Metadata: map[string]string{ports.MetadataKeyAgentSessionID: "abc123"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatalf("ok=true with info %#v, want no-op false", info)
	}
	if !reflect.DeepEqual(info, ports.SessionInfo{}) {
		t.Fatalf("info = %#v, want zero", info)
	}
}

func TestContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := (&Plugin{}).GetConfigSpec(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("GetConfigSpec err = %v, want context.Canceled", err)
	}
	if _, err := (&Plugin{}).GetLaunchCommand(ctx, ports.LaunchConfig{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("GetLaunchCommand err = %v, want context.Canceled", err)
	}
	if _, err := (&Plugin{}).GetPromptDeliveryStrategy(ctx, ports.LaunchConfig{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("GetPromptDeliveryStrategy err = %v, want context.Canceled", err)
	}
	if err := (&Plugin{}).GetAgentHooks(ctx, ports.WorkspaceHookConfig{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("GetAgentHooks err = %v, want context.Canceled", err)
	}
	if _, _, err := (&Plugin{}).GetRestoreCommand(ctx, ports.RestoreConfig{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("GetRestoreCommand err = %v, want context.Canceled", err)
	}
	if _, _, err := (&Plugin{}).SessionInfo(ctx, ports.SessionRef{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("SessionInfo err = %v, want context.Canceled", err)
	}
}

func TestResolveAiderBinaryFallback(t *testing.T) {
	// When the binary is not on PATH or any well-known location, the resolver
	// MUST surface ports.ErrAgentBinaryNotFound rather than a silent string
	// fallback that lets a missing CLI launch into an empty tmux pane.
	bin, err := ResolveAiderBinary(context.Background())
	if err != nil {
		if !errors.Is(err, ports.ErrAgentBinaryNotFound) {
			t.Fatalf("err = %v, want ports.ErrAgentBinaryNotFound", err)
		}
		return
	}
	if bin == "" {
		t.Fatal("ResolveAiderBinary returned empty string with no error")
	}
}
