package sessionmanager

import (
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestAffectedByPermissionChangeListsOnlyLiveWorkersWithStaleLaunchPermissions(t *testing.T) {
	m, store, _, _ := newManager()
	store.projects["mer"] = domain.ProjectRecord{
		ID: "mer",
		Config: domain.ProjectConfig{
			AgentConfig: domain.AgentConfig{Permissions: domain.PermissionModeBypassPermissions},
			Worker:      domain.RoleOverride{Harness: domain.HarnessClaudeCode},
		},
	}
	store.sessions["mer-stale"] = domain.SessionRecord{
		ID: "mer-stale", ProjectID: "mer", Kind: domain.KindWorker, DisplayName: "Stale worker",
		Metadata: domain.SessionMetadata{Permissions: domain.PermissionModeAuto, WorkspacePath: "/tmp/mer-stale", Branch: "ao/mer-stale"},
	}
	store.sessions["mer-current"] = domain.SessionRecord{
		ID: "mer-current", ProjectID: "mer", Kind: domain.KindWorker,
		Metadata: domain.SessionMetadata{Permissions: domain.PermissionModeBypassPermissions, WorkspacePath: "/tmp/mer-current", Branch: "ao/mer-current"},
	}
	store.sessions["mer-orchestrator"] = domain.SessionRecord{
		ID: "mer-orchestrator", ProjectID: "mer", Kind: domain.KindOrchestrator,
		Metadata: domain.SessionMetadata{Permissions: domain.PermissionModeAuto, WorkspacePath: "/tmp/mer-orchestrator", Branch: "ao/mer-orchestrator"},
	}

	affected, err := m.AffectedByPermissionChange(ctx, "mer")
	if err != nil {
		t.Fatalf("AffectedByPermissionChange: %v", err)
	}
	if len(affected) != 1 {
		t.Fatalf("affected = %#v, want one stale worker", affected)
	}
	if got := affected[0]; got.SessionID != "mer-stale" || got.FromMode != domain.PermissionModeAuto || got.ToMode != domain.PermissionModeBypassPermissions {
		t.Fatalf("affected[0] = %#v, want stale worker auto -> bypass-permissions", got)
	}
}

func TestRelaunchForPermissionChangeRestartsWorkerWithNewPermission(t *testing.T) {
	store := newFakeStore()
	store.projects["mer"] = domain.ProjectRecord{ID: "mer", Config: testRoleAgents()}
	agent := &recordingAgent{}
	manager := New(Deps{
		Runtime:   &fakeRuntime{},
		Agents:    singleAgent{agent: agent},
		Workspace: &fakeWorkspace{},
		Store:     store,
		Messenger: &fakeMessenger{},
		Lifecycle: &fakeLCM{store: store},
		LookPath:  func(string) (string, error) { return "/bin/true", nil },
	})

	record, _, _, err := manager.Spawn(ctx, ports.SpawnConfig{
		ProjectID: "mer",
		Kind:      domain.KindWorker,
		AgentConfig: ports.AgentConfig{
			Permissions: domain.PermissionModeAuto,
		},
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	stored := store.sessions[record.ID]
	stored.Metadata.AgentSessionID = "native-1"
	store.sessions[record.ID] = stored

	project := store.projects["mer"]
	project.Config.AgentConfig.Permissions = domain.PermissionModeBypassPermissions
	store.projects["mer"] = project

	outcomes, err := manager.RelaunchForPermissionChange(ctx, "mer")
	if err != nil {
		t.Fatalf("RelaunchForPermissionChange: %v", err)
	}
	if len(outcomes) != 1 || !outcomes[0].OK || outcomes[0].SessionID != record.ID {
		t.Fatalf("outcomes = %#v, want one successful relaunch for %s", outcomes, record.ID)
	}
	if agent.lastRestore.Permissions != domain.PermissionModeBypassPermissions {
		t.Fatalf("restore permissions = %q, want %q", agent.lastRestore.Permissions, domain.PermissionModeBypassPermissions)
	}
	if got := store.sessions[record.ID].Metadata.Permissions; got != domain.PermissionModeBypassPermissions {
		t.Fatalf("persisted permissions = %q, want %q", got, domain.PermissionModeBypassPermissions)
	}
}
