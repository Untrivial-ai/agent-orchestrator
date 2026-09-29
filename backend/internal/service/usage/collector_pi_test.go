package usage

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func TestDefaultSourceRootsIncludesPiSessions(t *testing.T) {
	home := t.TempDir()
	dataDir := filepath.Join(home, "ao-data")
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", "")

	got, err := DefaultSourceRoots(context.Background(), dataDir)
	mustNoError(t, err)
	if got.PiSessions != filepath.Join(dataDir, "pi", "sessions") {
		t.Fatalf("Pi sessions = %q, want %q", got.PiSessions, filepath.Join(dataDir, "pi", "sessions"))
	}
}

// TestCollectorDiscoversPiSessionByHeaderID catches filename-only discovery:
// Pi's native session identity lives in the first JSONL record.
func TestCollectorDiscoversPiSessionByHeaderID(t *testing.T) {
	const nativeID = "pi-session-1"
	store := collectorTestStore(t)
	session := collectorTestSession(t, store, domain.HarnessPi, nativeID, false)
	root := t.TempDir()
	path := filepath.Join(root, "project", "session.jsonl")
	writeUsageFixture(t, path, `{"type":"session","id":"`+nativeID+`","cwd":"/repo","version":3}`+"\n")
	collector := NewCollector(store, SourceRoots{PiSessions: root}, nil)

	mustNoError(t, collector.RecordHook(context.Background(), session.ID, HookSignal{
		Harness: domain.HarnessPi, Event: "session-start", NativeSessionID: nativeID,
	}))
	bindings, err := store.ListUsageBindingsForSession(context.Background(), session.ID)
	if err != nil || len(bindings) != 1 {
		t.Fatalf("bindings=%+v err=%v", bindings, err)
	}
	sources, err := store.ListUsageSourcesForBinding(context.Background(), bindings[0].ID)
	if err != nil || len(sources) != 1 || sources[0].Kind != domain.UsageSourcePiSession ||
		sources[0].ArtifactPath != canonicalUsagePath(t, path) {
		t.Fatalf("sources=%+v err=%v", sources, err)
	}
}

func TestCollectorReconcilesPiTranscriptCreatedAfterHook(t *testing.T) {
	const nativeID = "pi-late-session"
	store := collectorTestStore(t)
	session := collectorTestSession(t, store, domain.HarnessPi, nativeID, false)
	root := t.TempDir()
	collector := NewCollector(store, SourceRoots{PiSessions: root}, nil)

	mustNoError(t, collector.RecordHook(context.Background(), session.ID, HookSignal{
		Harness: domain.HarnessPi, Event: "session-start", NativeSessionID: nativeID,
	}))
	bindings, err := store.ListUsageBindingsForSession(context.Background(), session.ID)
	if err != nil || len(bindings) != 1 || bindings[0].LastErrorCode != domain.UsageErrorSourceDiscoveryPending {
		t.Fatalf("binding before transcript = %+v, err=%v", bindings, err)
	}
	path := filepath.Join(root, "project", "session.jsonl")
	writeUsageFixture(t, path, `{"type":"session","id":"`+nativeID+`","cwd":"/repo","version":3}`+"\n")

	mustNoError(t, collector.ReconcileSources(context.Background(), 8))
	sources, err := store.ListUsageSourcesForBinding(context.Background(), bindings[0].ID)
	if err != nil || len(sources) != 1 || sources[0].Kind != domain.UsageSourcePiSession ||
		sources[0].ArtifactPath != canonicalUsagePath(t, path) {
		t.Fatalf("sources after late transcript = %+v, err=%v", sources, err)
	}
}

func TestCollectorReconcilePiPathMatchesDurableNativeID(t *testing.T) {
	const nativeID = "pi-session-from-header"
	store := collectorTestStore(t)
	workspace := t.TempDir()
	session := collectorTestSession(t, store, domain.HarnessPi, nativeID, false)
	session.Metadata.WorkspacePath = workspace
	mustNoError(t, store.UpdateSession(context.Background(), session))
	root := t.TempDir()
	path := filepath.Join(root, "project", "session.jsonl")
	writeUsageFixture(t, path, `{"type":"session","id":"`+nativeID+`","cwd":"`+workspace+`","version":3}`+"\n")

	collector := NewCollector(store, SourceRoots{PiSessions: root}, nil)
	mustNoError(t, collector.ReconcilePath(context.Background(), path))
	bindings, err := store.ListUsageBindingsForSession(context.Background(), session.ID)
	if err != nil || len(bindings) != 1 || bindings[0].NativeRootID != nativeID {
		t.Fatalf("bindings=%+v err=%v", bindings, err)
	}
}

// TestCollectorPiBackfillRejectsUnrelatedSameWorkspaceSession catches binding
// a later manual Pi invocation merely because it shares the live AO workspace.
func TestCollectorPiBackfillRejectsUnrelatedSameWorkspaceSession(t *testing.T) {
	store := collectorTestStore(t)
	workspace := t.TempDir()
	session := collectorTestSession(t, store, domain.HarnessPi, "pi-current", false)
	session.Metadata.WorkspacePath = workspace
	mustNoError(t, store.UpdateSession(context.Background(), session))

	root := t.TempDir()
	currentPath := filepath.Join(root, "project", "current.jsonl")
	manualPath := filepath.Join(root, "project", "manual.jsonl")
	writeUsageFixture(t, currentPath, `{"type":"session","id":"pi-current","cwd":"`+workspace+`","version":3}`+"\n")
	writeUsageFixture(t, manualPath, `{"type":"session","id":"pi-manual","cwd":"`+workspace+`","version":3}`+"\n")

	collector := NewCollector(store, SourceRoots{PiSessions: root}, nil)
	mustNoError(t, collector.ReconcilePath(context.Background(), manualPath))
	bindings, err := store.ListUsageBindingsForSession(context.Background(), session.ID)
	if err != nil || len(bindings) != 0 {
		t.Fatalf("manual same-workspace path created bindings=%+v err=%v", bindings, err)
	}
	mustNoError(t, collector.BackfillActive(context.Background()))
	bindings, err = store.ListUsageBindingsForSession(context.Background(), session.ID)
	if err != nil || len(bindings) != 1 || bindings[0].NativeRootID != "pi-current" {
		t.Fatalf("bindings=%+v err=%v, want only current Pi session", bindings, err)
	}
}

func TestReadPiSessionMetaHonorsCanceledContext(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	writeUsageFixture(t, path, `{"type":"session","id":"pi-context","timestamp":"2026-08-24T10:00:00Z","cwd":"/repo","version":3}`+"\n")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, ok := readPiSessionMeta(ctx, path); ok {
		t.Fatal("canceled Pi metadata read succeeded")
	}
}
