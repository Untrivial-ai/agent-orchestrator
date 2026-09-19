package integration

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func seedTerminatedOrchestratorWithMarker(t *testing.T, st *stack) (domain.SessionID, string) {
	t.Helper()
	ctx := context.Background()
	sess, _, _, err := st.sm.Spawn(ctx, ports.SpawnConfig{ProjectID: "mer", Kind: domain.KindOrchestrator})
	if err != nil {
		t.Fatal(err)
	}
	rec, ok, err := st.store.GetSession(ctx, sess.ID)
	if err != nil || !ok {
		t.Fatalf("get session: ok=%v err=%v", ok, err)
	}
	rec.IsTerminated = true
	rec.Activity = domain.Activity{State: domain.ActivityExited, LastActivityAt: time.Now()}
	if err := st.store.UpdateSession(ctx, rec); err != nil {
		t.Fatal(err)
	}
	if err := st.store.UpsertSessionWorktree(ctx, domain.SessionWorktreeRecord{
		SessionID:    rec.ID,
		RepoName:     domain.RootWorkspaceRepoName,
		Branch:       rec.Metadata.Branch,
		WorktreePath: rec.Metadata.WorkspacePath,
		State:        "removed",
	}); err != nil {
		t.Fatal(err)
	}
	return rec.ID, rec.Metadata.WorkspacePath
}

func TestOrchestratorSpawnReleasesTerminatedWorkspace(t *testing.T) {
	ctx := context.Background()
	st := newStack(t)
	st.ws.root = t.TempDir()
	oldID, oldPath := seedTerminatedOrchestratorWithMarker(t, st)

	replacement, _, _, err := st.sm.Spawn(ctx, ports.SpawnConfig{ProjectID: "mer", Kind: domain.KindOrchestrator})
	if err != nil {
		t.Fatalf("replacement spawn: %v", err)
	}
	if replacement.ID == oldID {
		t.Fatalf("replacement reused terminated orchestrator %s", oldID)
	}
	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Fatalf("old worktree survived replacement: %v", err)
	}
	rows, err := st.store.ListSessionWorktrees(ctx, oldID)
	if err != nil || len(rows) != 0 {
		t.Fatalf("old restore markers = %+v err=%v, want none", rows, err)
	}
}

func TestOrchestratorReleaseFailureRetainsRestoreMarker(t *testing.T) {
	ctx := context.Background()
	st := newStack(t)
	st.ws.root = t.TempDir()
	oldID, oldPath := seedTerminatedOrchestratorWithMarker(t, st)
	st.ws.stashErr = errors.New("access denied")

	if _, _, _, err := st.sm.Spawn(ctx, ports.SpawnConfig{ProjectID: "mer", Kind: domain.KindOrchestrator}); err == nil {
		t.Fatal("replacement spawn succeeded despite inconclusive workspace access")
	}
	if _, err := os.Stat(oldPath); err != nil {
		t.Fatalf("old worktree was removed after release failure: %v", err)
	}
	rows, err := st.store.ListSessionWorktrees(ctx, oldID)
	if err != nil || len(rows) != 1 {
		t.Fatalf("restore marker after release failure = %+v err=%v, want retained", rows, err)
	}
	if len(st.ws.forceDestroyed) != 0 {
		t.Fatalf("force destroy ran after preserve failure: %v", st.ws.forceDestroyed)
	}
}

func TestOrchestratorRuntimeReleaseFailureBlocksReplacement(t *testing.T) {
	ctx := context.Background()
	st := newStack(t)
	st.ws.root = t.TempDir()
	oldID, oldPath := seedTerminatedOrchestratorWithMarker(t, st)
	st.rt.destroyErr = errors.New("runtime unavailable")

	if _, _, _, err := st.sm.Spawn(ctx, ports.SpawnConfig{ProjectID: "mer", Kind: domain.KindOrchestrator}); err == nil {
		t.Fatal("replacement spawn succeeded despite runtime release failure")
	}
	if _, err := os.Stat(oldPath); err != nil {
		t.Fatalf("old worktree was removed after runtime failure: %v", err)
	}
	rows, err := st.store.ListSessionWorktrees(ctx, oldID)
	if err != nil || len(rows) != 1 {
		t.Fatalf("restore marker after runtime failure = %+v err=%v, want retained", rows, err)
	}
	if len(st.ws.forceDestroyed) != 0 {
		t.Fatalf("force destroy ran after runtime failure: %v", st.ws.forceDestroyed)
	}
}
