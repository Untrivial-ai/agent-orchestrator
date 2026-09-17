package junie

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type fakeBuilder struct {
	files RuntimeFiles
	err   error
}

func (f fakeBuilder) Prepare(context.Context, RuntimeFileRequest) (RuntimeFiles, error) {
	return f.files, f.err
}

func TestLaunchAndRestoreCommands(t *testing.T) {
	p := New()
	p.resolvedBinary = "junie"
	p.builder = fakeBuilder{files: RuntimeFiles{ConfigPath: "config", GuidelinesPath: "guidelines"}}
	got, err := p.GetLaunchCommand(context.Background(), ports.LaunchConfig{DataDir: "data", SessionID: "ao", Prompt: "-task", Config: domain.AgentConfig{Model: " m ", Effort: "high"}, Permissions: ports.PermissionModeBypassPermissions})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"junie", "--skip-update-check", "--config-location", "config", "--guidelines-filename", "guidelines", "--model", "m", "--effort", "high", "--brave", "--prompt", "-task"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("launch=%q want %q", got, want)
	}
	got, ok, err := p.GetRestoreCommand(context.Background(), ports.RestoreConfig{DataDir: "data", Session: ports.SessionRef{ID: "ao", Metadata: map[string]string{ports.MetadataKeyAgentSessionID: "native-1"}}, Prompt: "next"})
	if err != nil || !ok {
		t.Fatalf("restore ok=%v err=%v", ok, err)
	}
	want = []string{"junie", "--skip-update-check", "--config-location", "config", "--guidelines-filename", "guidelines", "--resume", "--session-id", "native-1", "--prompt", "next"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("restore=%q want %q", got, want)
	}
}

func TestLaunchRejectsUnsupportedEffortAndBuilderFailure(t *testing.T) {
	p := New()
	p.resolvedBinary = "junie"
	p.builder = fakeBuilder{files: RuntimeFiles{ConfigPath: "c", GuidelinesPath: "g"}}
	_, err := p.GetLaunchCommand(context.Background(), ports.LaunchConfig{Config: domain.AgentConfig{Effort: "ultra"}})
	if !errors.Is(err, ports.ErrUnsupportedEffort) {
		t.Fatalf("err=%v", err)
	}
	p.builder = fakeBuilder{err: errors.New("boom")}
	if _, err = p.GetLaunchCommand(context.Background(), ports.LaunchConfig{}); err == nil {
		t.Fatal("expected builder error")
	}
}

func TestActivityMapping(t *testing.T) {
	cases := map[string]domain.ActivityState{"user-prompt-submit": domain.ActivityActive, "pre-tool-use": domain.ActivityActive, "permission-request": domain.ActivityBlocked, "stop": domain.ActivityIdle, "stop-failure": domain.ActivityWaitingInput, "session-end": domain.ActivityExited}
	for event, want := range cases {
		got, ok := DeriveActivityState(event, nil)
		if !ok || got != want {
			t.Fatalf("%s=(%q,%v)", event, got, ok)
		}
	}
	if _, ok := DeriveActivityState("session-start", nil); ok {
		t.Fatal("session-start must be metadata-only")
	}
}
