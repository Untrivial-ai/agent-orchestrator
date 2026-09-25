package sessionmanager

import (
	"context"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// importManager builds a chat-capable manager while keeping a handle on the
// workspace fake, so a test can control which branches Create accepts.
func importManager(t *testing.T) (*Manager, *fakeStore, *fakeWorkspace) {
	t.Helper()
	st := newFakeStore()
	st.projects["mer"] = domain.ProjectRecord{ID: "mer", Config: testRoleAgents()}
	ws := &fakeWorkspace{}
	m := New(Deps{
		Runtime:   &fakeRuntime{},
		Agents:    fakeAgents{},
		Workspace: ws,
		Store:     st,
		Messenger: &fakeMessenger{},
		Chat:      &recordingLauncher{},
		Lifecycle: &fakeLCM{store: st},
		DataDir:   "/ao-test-data",
		LookPath:  func(string) (string, error) { return "/bin/true", nil },
	})
	return m, st, ws
}

func importSpawnConfig(branch string) ports.SpawnConfig {
	return ports.SpawnConfig{
		ProjectID: "mer",
		Kind:      domain.KindWorker,
		Harness:   domain.HarnessCodex,
		Branch:    branch,
		ResumeNativeSession: &ports.ResumeNativeSession{
			Provider:        domain.HarnessCodex,
			NativeSessionID: "native-1",
			ConfigDir:       "/home/user/.codex",
		},
	}
}

// The fallback is scoped to imports. An ordinary spawn that names a branch it
// cannot have must still fail loudly rather than silently landing somewhere
// else, which would hide the conflict from the user who chose that branch.
func TestOrdinarySpawnStillFailsOnBranchConflict(t *testing.T) {
	m, _, ws := importManager(t)
	ws.createErrForBranch = "feat/payments"

	_, _, _, err := m.Spawn(context.Background(), ports.SpawnConfig{
		ProjectID: "mer",
		Kind:      domain.KindWorker,
		Harness:   domain.HarnessCodex,
		Branch:    "feat/payments",
	})
	if err == nil {
		t.Fatal("an ordinary spawn must not silently fall back to another branch")
	}
	if len(ws.createBranches) != 1 {
		t.Errorf("an ordinary spawn should not retry, got %v", ws.createBranches)
	}
}

// The branch a conversation ran on and the branch its session owns are two
// different facts. The session takes its own generated branch; recording the
// original is what keeps the conversation's pull request findable.
func TestImportSpawnRecordsSourceBranchSeparately(t *testing.T) {
	m, _, _ := importManager(t)

	cfg := importSpawnConfig("")
	cfg.ResumeNativeSession.SourceBranch = "feat/payments"

	rec, _, _, err := m.Spawn(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if rec.Metadata.SourceBranch != "feat/payments" {
		t.Errorf("the conversation's branch must be recorded, got %q", rec.Metadata.SourceBranch)
	}
	if rec.Metadata.Branch == "feat/payments" {
		t.Error("the session must take its own branch, not the conversation's")
	}
}

// An ordinary spawn has no source branch to record.
func TestOrdinarySpawnRecordsNoSourceBranch(t *testing.T) {
	m, _, _ := importManager(t)

	rec, _, _, err := m.Spawn(context.Background(), ports.SpawnConfig{
		ProjectID: "mer",
		Kind:      domain.KindWorker,
		Harness:   domain.HarnessCodex,
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if rec.Metadata.SourceBranch != "" {
		t.Errorf("only an import has a source branch, got %q", rec.Metadata.SourceBranch)
	}
}

func TestImportSpawnDoesNotFetchDefaultBranch(t *testing.T) {
	m, st, ws := importManager(t)
	project := st.projects["mer"]
	project.Path = "/repo/mer"
	st.projects["mer"] = project
	if _, _, _, err := m.Spawn(context.Background(), importSpawnConfig("feat/payments")); err != nil {
		t.Fatal(err)
	}
	if len(ws.fetches) != 0 {
		t.Fatalf("import waited for unrelated fetches: %v", ws.fetches)
	}
	if ws.lastCfg.BaseRef == "" {
		t.Fatal("import must pass the locally resolved base to workspace creation")
	}
	if len(ws.resolves) != 0 || len(ws.localResolves) != 1 {
		t.Fatalf("import resolution calls: live=%d local=%d", len(ws.resolves), len(ws.localResolves))
	}
	cfg := importSpawnConfig("")
	cfg.ResumeNativeSession = nil
	if _, _, _, err := m.Spawn(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	if len(ws.resolves) != 1 || len(ws.fetches) != 1 || len(ws.localResolves) != 1 {
		t.Fatalf("ordinary spawn after import must resolve live and fetch: live=%d local=%d fetch=%d", len(ws.resolves), len(ws.localResolves), len(ws.fetches))
	}
}

func TestImportFallbackSeparatesDataDirectories(t *testing.T) {
	m, st, _ := importManager(t)
	cfg := importSpawnConfig("")
	m.dataDir = "/ao/dev-one/data"
	first := m.importSpawnBranch(cfg, st.projects["mer"], "mer-1")
	m.dataDir = "/ao/dev-two/data"
	second := m.importSpawnBranch(cfg, st.projects["mer"], "mer-1")
	if first == second {
		t.Fatalf("independent import databases reused branch %q", first)
	}
	if second != m.importSpawnBranch(cfg, st.projects["mer"], "mer-1") {
		t.Fatal("fallback must be stable across retry")
	}
}
