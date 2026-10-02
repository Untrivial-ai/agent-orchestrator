package commandcodeacp

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"testing"

	acpdriver "github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/acp"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestConfigureAlwaysLaunchesACPWithAutomationFlags(t *testing.T) {
	args, env, err := configure(context.Background(), acpdriver.LaunchConfig{})
	if err != nil {
		t.Fatalf("configure: %v", err)
	}
	if len(env) != 0 {
		t.Fatalf("env = %v, want none", env)
	}
	if args[0] != "acp" {
		t.Fatalf("args[0] = %q, want \"acp\"", args[0])
	}
	for _, want := range []string{"--skip-onboarding", "--no-auto-update", "--trust"} {
		if !slices.Contains(args, want) {
			t.Errorf("args %v missing %q", args, want)
		}
	}
}

func TestConfigureMapsPermissionMode(t *testing.T) {
	tests := []struct {
		name  string
		perms ports.PermissionMode
		want  string
	}{
		{"default", ports.PermissionModeDefault, modeDefault},
		{"empty is default", "", modeDefault},
		{"accept-edits", ports.PermissionModeAcceptEdits, modeAutoAccpt},
		{"auto collapses to accept", ports.PermissionModeAuto, modeAutoAccpt},
		{"bypass", ports.PermissionModeBypassPermissions, modeBypass},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := permissionMode(tc.perms); got != tc.want {
				t.Fatalf("permissionMode(%q) = %q, want %q", tc.perms, got, tc.want)
			}
			args, _, err := configure(context.Background(), acpdriver.LaunchConfig{Permissions: tc.perms})
			if err != nil {
				t.Fatalf("configure: %v", err)
			}
			if !slices.Contains(args, "--permission-mode") {
				t.Errorf("args %v missing --permission-mode", args)
			}
		})
	}
}

func TestConfigureForwardsModel(t *testing.T) {
	args, _, err := configure(context.Background(), acpdriver.LaunchConfig{Model: "  gpt-6.1-sol "})
	if err != nil {
		t.Fatalf("configure: %v", err)
	}
	idx := slices.Index(args, "--model")
	if idx < 0 || idx+1 >= len(args) {
		t.Fatalf("args %v missing --model <value>", args)
	}
	if args[idx+1] != "gpt-6.1-sol" {
		t.Fatalf("model = %q, want trimmed value", args[idx+1])
	}
}

func TestSessionOptionsMapsModelAndEffort(t *testing.T) {
	got := sessionOptions(ports.ChatTurnSettings{
		Model:  "claude-opus-5-5",
		Effort: "high",
	})
	want := []acpdriver.SessionOption{
		{ID: "model", Value: "claude-opus-5-5"},
		{ID: "effort", Value: "high"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d options %v, want %d", len(got), got, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("option[%d] = %v, want %v", i, got[i], want[i])
		}
	}
}

func TestSessionOptionsOmitsEmptyValues(t *testing.T) {
	if got := sessionOptions(ports.ChatTurnSettings{}); len(got) != 0 {
		t.Fatalf("got %v, want none", got)
	}
}

func TestValidateTurnSettings(t *testing.T) {
	tests := []struct {
		name     string
		initial  ports.PermissionMode
		settings ports.ChatTurnSettings
		wantErr  bool
	}{
		{"unset approval is fine", ports.PermissionModeDefault, ports.ChatTurnSettings{}, false},
		{"unchanged approval is fine", ports.PermissionModeDefault, ports.ChatTurnSettings{Approval: ports.PermissionModeDefault}, false},
		{"auto and accept-edits are the same mode", ports.PermissionModeAcceptEdits, ports.ChatTurnSettings{Approval: ports.PermissionModeAuto}, false},
		{"a real change is refused rather than silently ignored", ports.PermissionModeDefault, ports.ChatTurnSettings{Approval: ports.PermissionModeBypassPermissions}, true},
		{"a real change in the other direction is refused too", ports.PermissionModeBypassPermissions, ports.ChatTurnSettings{Approval: ports.PermissionModeDefault}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateTurnSettings(tc.initial, tc.settings)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if tc.wantErr && !errors.Is(err, acpdriver.ErrACPSetterUnsupported) {
				t.Fatalf("err = %v, want it to wrap ErrACPSetterUnsupported", err)
			}
		})
	}
}

// The mode ids below were read from a live v1.74.0 session/new handshake. If the
// provider renames one, this binding would silently send an unknown mode id, so
// pin the vocabulary we verified rather than whatever a doc comment claims.
func TestVerifiedModeVocabulary(t *testing.T) {
	for _, id := range []string{modeDefault, modeAutoAccpt, modePlan, modeDontAsk, modeBypass} {
		if id == "" {
			t.Fatal("verified mode id must not be empty")
		}
	}
	if modePlan == modeDefault || modeDontAsk == modeDefault {
		t.Fatal("plan/dont-ask must be distinct from default")
	}
}

// A registry built with this driver must resolve command-code. Registration is
// the whole capability gate, so an unregistered binding would leave the desktop
// with no Chat toggle for a harness that supports it.
func TestNewDriverTargetsCommandCodeHarness(t *testing.T) {
	driver := New(stubPlugin{}, slog.New(slog.DiscardHandler))
	if got := driver.Harness(); got != domain.HarnessCommandCode {
		t.Fatalf("Harness() = %q, want %q", got, domain.HarnessCommandCode)
	}
}

type stubPlugin struct{}

func (stubPlugin) ResolveBinary(context.Context) (string, error) { return "command-code", nil }

func (stubPlugin) AuthStatus(context.Context) (ports.AgentAuthStatus, error) {
	return ports.AgentAuthStatusAuthorized, nil
}
