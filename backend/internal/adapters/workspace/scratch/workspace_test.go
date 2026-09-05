package scratch_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/workspace/scratch"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestWorkspaceCreatesBranchlessPerSessionDirectories(t *testing.T) {
	root := t.TempDir()
	physicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("resolve temp root: %v", err)
	}
	ws, err := scratch.New(scratch.Options{ManagedRoot: root})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	worker, err := ws.Create(context.Background(), ports.WorkspaceConfig{
		ProjectID: "scratch",
		SessionID: "scratch-1",
		Kind:      domain.KindWorker,
	})
	if err != nil {
		t.Fatalf("Create worker: %v", err)
	}
	if want := filepath.Join(physicalRoot, "scratch", "workers", "scratch-1"); worker.Path != want {
		t.Fatalf("worker path = %q, want %q", worker.Path, want)
	}
	if worker.Branch != "" {
		t.Fatalf("worker branch = %q, want empty", worker.Branch)
	}
	if info, err := os.Stat(worker.Path); err != nil || !info.IsDir() {
		t.Fatalf("worker dir stat = %#v, %v; want directory", info, err)
	}

	orchestrator, err := ws.Create(context.Background(), ports.WorkspaceConfig{
		ProjectID: "scratch",
		SessionID: "scratch-2",
		Kind:      domain.KindOrchestrator,
	})
	if err != nil {
		t.Fatalf("Create orchestrator: %v", err)
	}
	if want := filepath.Join(physicalRoot, "scratch", "orchestrators", "scratch-2"); orchestrator.Path != want {
		t.Fatalf("orchestrator path = %q, want %q", orchestrator.Path, want)
	}
	if orchestrator.Branch != "" {
		t.Fatalf("orchestrator branch = %q, want empty", orchestrator.Branch)
	}
}

func TestWorkspaceDestroyPreservesNonEmptyDirectory(t *testing.T) {
	ws, err := scratch.New(scratch.Options{ManagedRoot: t.TempDir()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	info, err := ws.Create(context.Background(), ports.WorkspaceConfig{
		ProjectID: "scratch",
		SessionID: "scratch-1",
		Kind:      domain.KindWorker,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := os.WriteFile(filepath.Join(info.Path, "notes.txt"), []byte("keep\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	err = ws.Destroy(context.Background(), info)
	if !errors.Is(err, ports.ErrWorkspaceDirty) {
		t.Fatalf("Destroy err = %v, want ErrWorkspaceDirty", err)
	}
	if _, err := os.Stat(filepath.Join(info.Path, "notes.txt")); err != nil {
		t.Fatalf("non-empty scratch dir was not preserved: %v", err)
	}

	if err := os.Remove(filepath.Join(info.Path, "notes.txt")); err != nil {
		t.Fatal(err)
	}
	if err := ws.Destroy(context.Background(), info); err != nil {
		t.Fatalf("Destroy empty: %v", err)
	}
	if _, err := os.Stat(info.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("empty scratch dir still exists or stat failed differently: %v", err)
	}
}

func TestWorkspaceRejectsUnsafeSessionIDs(t *testing.T) {
	ws, err := scratch.New(scratch.Options{ManagedRoot: t.TempDir()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = ws.Create(context.Background(), ports.WorkspaceConfig{
		ProjectID: "scratch",
		SessionID: "../outside",
		Kind:      domain.KindWorker,
	})
	if err == nil {
		t.Fatal("Create unsafe session id succeeded, want error")
	}
}

// TestWorkspaceDestroyReclaimReportsAlreadyAbsentDirectory covers the scratch
// half of the cleanup accounting. os.Remove is deliberately tolerated on a
// missing path, so a scratch directory that was already gone tears down without
// error while releasing nothing. Counting that as reclaimed reports space that
// was never freed.
func TestWorkspaceDestroyReclaimReportsAlreadyAbsentDirectory(t *testing.T) {
	ws, err := scratch.New(scratch.Options{ManagedRoot: t.TempDir()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	info, err := ws.Create(context.Background(), ports.WorkspaceConfig{
		ProjectID: "scratch",
		SessionID: "scratch-gone",
		Kind:      domain.KindWorker,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	reclaim, err := ws.DestroyReclaim(context.Background(), info)
	if err != nil {
		t.Fatalf("DestroyReclaim present: %v", err)
	}
	if reclaim != ports.WorkspaceReclaimRemoved {
		t.Fatalf("reclaim = %q, want %q", reclaim, ports.WorkspaceReclaimRemoved)
	}

	// Second pass: the directory is gone, so there is nothing left to release.
	reclaim, err = ws.DestroyReclaim(context.Background(), info)
	if err != nil {
		t.Fatalf("DestroyReclaim absent: %v", err)
	}
	if reclaim != ports.WorkspaceReclaimAlreadyAbsent {
		t.Fatalf("reclaim = %q, want %q", reclaim, ports.WorkspaceReclaimAlreadyAbsent)
	}
}
