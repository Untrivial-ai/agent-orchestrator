package session

import (
	"context"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func TestWorkspaceManifestIndexServesStaleSnapshotWhileRefreshing(t *testing.T) {
	repo := newWorkspaceRepo(t)
	writeWorkspaceFile(t, repo, "README.md", "hello\nfirst\n")
	st := newFakeStore()
	st.sessions["ao-1"] = domain.SessionRecord{ID: "ao-1", Metadata: domain.SessionMetadata{WorkspacePath: repo}}
	svc := NewWithDeps(Deps{Store: st})

	first, err := svc.GetWorkspaceManifest(context.Background(), "ao-1")
	if err != nil {
		t.Fatal(err)
	}
	var pending func()
	svc.runBackground = func(work func()) { pending = work }
	writeWorkspaceFile(t, repo, "README.md", "hello\nsecond\n")
	svc.InvalidateWorkspaceCache("ao-1")

	stale, err := svc.GetWorkspaceManifest(context.Background(), "ao-1")
	if err != nil {
		t.Fatal(err)
	}
	if !stale.Stale || !stale.Refreshing || stale.WorkspaceVersion != first.WorkspaceVersion {
		t.Fatalf("stale manifest = %+v, want refreshing snapshot at version %q", stale, first.WorkspaceVersion)
	}
	if pending == nil {
		t.Fatal("invalidation did not schedule a background refresh")
	}
	pending()

	refreshed, err := svc.GetWorkspaceManifest(context.Background(), "ao-1")
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.Stale || refreshed.Refreshing {
		t.Fatalf("refreshed manifest still stale: %+v", refreshed)
	}
	if refreshed.WorkspaceVersion == first.WorkspaceVersion {
		t.Fatalf("refreshed version = %q, want a new version", refreshed.WorkspaceVersion)
	}
}

func TestWorkspaceManifestIndexCoalescesConcurrentRefreshes(t *testing.T) {
	index := newWorkspaceManifestIndex()
	manifest := WorkspaceManifest{SessionID: "ao-1", WorkspaceVersion: "one"}
	index.publish("ao-1", manifest, 0)
	if !index.markStale("ao-1") {
		t.Fatal("first invalidation did not claim refresh")
	}
	if index.markStale("ao-1") {
		t.Fatal("second invalidation claimed a duplicate refresh")
	}
	got, ok := index.get("ao-1")
	if !ok || !got.Stale || !got.Refreshing {
		t.Fatalf("index entry = %+v, %v", got, ok)
	}
}

func TestWorkspaceManifestIndexEvictsLeastRecentlyUsedSnapshot(t *testing.T) {
	index := newWorkspaceManifestIndex()
	for n := 0; n < maxWorkspaceManifestEntries; n++ {
		id := domain.SessionID(string(rune(n + 1)))
		index.publish(id, WorkspaceManifest{SessionID: id}, 0)
	}
	oldest := domain.SessionID(string(rune(1)))
	recent := domain.SessionID(string(rune(2)))
	if _, ok := index.get(recent); !ok {
		t.Fatal("recent entry missing before eviction")
	}
	index.publish("overflow", WorkspaceManifest{SessionID: "overflow"}, 0)
	if _, ok := index.get(oldest); ok {
		t.Fatal("least recently used entry was not evicted")
	}
	if _, ok := index.get(recent); !ok {
		t.Fatal("recently accessed entry was evicted")
	}
}

func TestWorkspaceManifestIndexRejectsSnapshotInvalidatedDuringRefresh(t *testing.T) {
	index := newWorkspaceManifestIndex()
	generation := index.beginRefresh("ao-1")
	if index.markStale("ao-1") {
		t.Fatal("in-flight refresh should retain ownership")
	}
	if index.publish("ao-1", WorkspaceManifest{SessionID: "ao-1", WorkspaceVersion: "old"}, generation) {
		t.Fatal("snapshot invalidated during refresh was published as fresh")
	}
	got, ok := index.get("ao-1")
	if !ok || !got.Stale || got.Refreshing {
		t.Fatalf("published snapshot = %+v, %v; want usable stale snapshot", got, ok)
	}
}
