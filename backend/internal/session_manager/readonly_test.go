package sessionmanager

import (
	"context"
	"errors"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestReadOnlySpawnNeverFallsBackToTerminal(t *testing.T) {
	for _, tc := range []struct {
		name      string
		mode      domain.SessionMode
		preflight error
	}{
		{"explicit terminal", domain.SessionModeTUI, nil},
		{"default chat unavailable", "", ports.ErrChatDriverUnavailable},
		{"default unsupported", "", ports.ErrChatUnsupported},
		{"explicit unsupported", domain.SessionModeChat, ports.ErrChatPermissionModeUnsupported},
	} {
		t.Run(tc.name, func(t *testing.T) {
			launcher := &recordingLauncher{preflightErr: tc.preflight}
			mgr, store, runtime := newChatManager(launcher)
			mgr.defaults = fixedSessionModeDefaults(domain.SessionModeChat)
			_, _, _, err := mgr.Spawn(context.Background(), ports.SpawnConfig{ProjectID: chatTestProject, Kind: domain.KindWorker, Harness: domain.HarnessCodex, RequestedMode: tc.mode, AgentConfig: ports.AgentConfig{Permissions: ports.PermissionModeReadOnly}})
			if err == nil {
				t.Fatal("read-only spawn succeeded without a capable Chat driver")
			}
			if tc.preflight != nil && !errors.Is(err, tc.preflight) {
				t.Fatalf("error = %v, want %v", err, tc.preflight)
			}
			if runtime.created != 0 || len(launcher.started) != 0 {
				t.Fatal("rejected spawn launched a controller")
			}
			sessions, err := store.ListAllSessions(context.Background())
			if err != nil || len(sessions) != 0 {
				t.Fatalf("rejected spawn left sessions=%v, err=%v", sessions, err)
			}
		})
	}
}

func TestReadOnlySpawnOverrideIsDurableAndDoesNotChangeProject(t *testing.T) {
	launcher := &recordingLauncher{}
	mgr, store, _ := newChatManager(launcher)
	before := store.projects[string(chatTestProject)].Config.AgentConfig.Permissions
	rec, _, _, err := mgr.Spawn(context.Background(), ports.SpawnConfig{ProjectID: chatTestProject, Kind: domain.KindWorker, Harness: domain.HarnessCodex, RequestedMode: domain.SessionModeChat, AgentConfig: ports.AgentConfig{Permissions: ports.PermissionModeReadOnly}})
	if err != nil {
		t.Fatal(err)
	}
	if rec.Metadata.Permissions != ports.PermissionModeReadOnly || launcher.started[0].Permissions != ports.PermissionModeReadOnly {
		t.Fatalf("stored=%q launched=%q", rec.Metadata.Permissions, launcher.started[0].Permissions)
	}
	if after := store.projects[string(chatTestProject)].Config.AgentConfig.Permissions; before != after {
		t.Fatalf("spawn changed project: %q -> %q", before, after)
	}
}
