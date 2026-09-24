package codewhale

import (
	"context"
	"reflect"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestLaunchCommand(t *testing.T) {
	p := &Plugin{resolvedBinary: "/opt/codewhale"}
	cmd, err := p.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		WorkspacePath: "/work/tree",
		Prompt:        "--fix the failing test",
		Config:        ports.AgentConfig{Model: "openai/gpt-5"},
		Permissions:   ports.PermissionModeAuto,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/opt/codewhale", "--workspace", "/work/tree", "--skip-onboarding", "--model", "openai/gpt-5", "--approval-policy", "auto", "--fresh", "--prompt", "--fix the failing test"}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("command = %#v, want %#v", cmd, want)
	}
}

func TestPermissionMappings(t *testing.T) {
	tests := []struct {
		mode ports.PermissionMode
		want []string
	}{
		{ports.PermissionModeDefault, []string{"--approval-policy", "on-request"}},
		{ports.PermissionModeAcceptEdits, []string{"--approval-policy", "on-request"}},
		{ports.PermissionModeAuto, []string{"--approval-policy", "auto"}},
		{ports.PermissionModeBypassPermissions, []string{"--yolo"}},
	}
	for _, tc := range tests {
		var got []string
		appendPermissionFlags(&got, tc.mode)
		if !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("mode %q = %#v, want %#v", tc.mode, got, tc.want)
		}
	}
}

func TestRestoreCommandUsesExactNativeSession(t *testing.T) {
	p := &Plugin{resolvedBinary: "/opt/codewhale"}
	cmd, ok, err := p.GetRestoreCommand(context.Background(), ports.RestoreConfig{
		Session: ports.SessionRef{
			WorkspacePath: "/work/tree",
			Metadata:      map[string]string{ports.MetadataKeyAgentSessionID: "sess_native-123"},
		},
		Permissions: ports.PermissionModeBypassPermissions,
	})
	if err != nil || !ok {
		t.Fatalf("restore ok=%v err=%v", ok, err)
	}
	want := []string{"/opt/codewhale", "--workspace", "/work/tree", "--skip-onboarding", "--yolo", "--resume", "sess_native-123"}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("command = %#v, want %#v", cmd, want)
	}
}

func TestRestoreCommandRejectsInvalidNativeSession(t *testing.T) {
	p := &Plugin{resolvedBinary: "/opt/codewhale"}
	_, ok, err := p.GetRestoreCommand(context.Background(), ports.RestoreConfig{
		Session: ports.SessionRef{
			WorkspacePath: "/work/tree",
			Metadata:      map[string]string{ports.MetadataKeyAgentSessionID: "latest"},
		},
	})
	if err == nil || ok {
		t.Fatalf("restore ok=%v err=%v, want invalid-id failure", ok, err)
	}
}

func TestManifestAndConfig(t *testing.T) {
	p := New()
	if got := p.Manifest(); got.ID != "codewhale" || got.Name != "Codewhale" || len(got.Capabilities) != 1 {
		t.Fatalf("manifest = %+v", got)
	}
	spec, err := p.GetConfigSpec(context.Background())
	if err != nil || len(spec.Fields) != 1 || spec.Fields[0].Key != "model" {
		t.Fatalf("config spec = %+v, err=%v", spec, err)
	}
}
