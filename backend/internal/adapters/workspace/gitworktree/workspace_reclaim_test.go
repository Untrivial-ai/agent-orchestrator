package gitworktree

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// TestDestroyReclaimReportsAlreadyAbsentWorktree covers the accounting half of
// teardown. A worktree directory that is already gone leaves `git worktree
// remove` nothing to act on; the prune still clears the stale registration and
// the final os.RemoveAll is a documented no-op on a missing path, so Destroy
// returns nil having reclaimed nothing. Callers that count reclaimed
// workspaces from that nil report space they never freed, so DestroyReclaim
// has to name the two cases apart.
func TestDestroyReclaimReportsAlreadyAbsentWorktree(t *testing.T) {
	git := requireGit(t)
	tmp := t.TempDir()
	repo := setupOriginClone(t, git, tmp)
	root := filepath.Join(tmp, "managed")
	ws, err := New(Options{Binary: git, ManagedRoot: root, RepoResolver: StaticRepoResolver{"proj": repo}})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	ctx := context.Background()
	cfg := ports.WorkspaceConfig{ProjectID: "proj", SessionID: "sess-absent", Branch: "feature/absent"}

	info, err := ws.Create(ctx, cfg)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// The directory disappears behind AO's back while git still has it
	// registered: an external `rm -rf`, a wiped scratch disk, a restored backup.
	if err := os.RemoveAll(info.Path); err != nil {
		t.Fatalf("remove worktree dir: %v", err)
	}

	reclaim, err := ws.DestroyReclaim(ctx, info)
	if err != nil {
		t.Fatalf("destroy reclaim: %v", err)
	}
	if reclaim != ports.WorkspaceReclaimAlreadyAbsent {
		t.Fatalf("reclaim = %q, want %q", reclaim, ports.WorkspaceReclaimAlreadyAbsent)
	}

	// The stale registration is still reconciled, so the post-state matches a
	// real removal; only the accounting differs.
	records, err := ws.listRecords(ctx, repo)
	if err != nil {
		t.Fatalf("list records: %v", err)
	}
	if _, ok := findWorktree(records, info.Path); ok {
		t.Fatalf("worktree %q still registered after DestroyReclaim", info.Path)
	}
}

// TestDestroyReclaimReportsRemovedWorktree pins the other side: a workspace
// that was really there must not be reported as already absent, or the fix
// would trade one wrong count for another.
func TestDestroyReclaimReportsRemovedWorktree(t *testing.T) {
	git := requireGit(t)
	tmp := t.TempDir()
	repo := setupOriginClone(t, git, tmp)
	root := filepath.Join(tmp, "managed")
	ws, err := New(Options{Binary: git, ManagedRoot: root, RepoResolver: StaticRepoResolver{"proj": repo}})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	ctx := context.Background()
	cfg := ports.WorkspaceConfig{ProjectID: "proj", SessionID: "sess-present", Branch: "feature/present"}

	info, err := ws.Create(ctx, cfg)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, statErr := os.Stat(info.Path); statErr != nil {
		t.Fatalf("precondition: worktree missing before destroy: %v", statErr)
	}

	reclaim, err := ws.DestroyReclaim(ctx, info)
	if err != nil {
		t.Fatalf("destroy reclaim: %v", err)
	}
	if reclaim != ports.WorkspaceReclaimRemoved {
		t.Fatalf("reclaim = %q, want %q", reclaim, ports.WorkspaceReclaimRemoved)
	}
	if _, statErr := os.Stat(info.Path); !os.IsNotExist(statErr) {
		t.Fatalf("path after destroy stat err = %v, want not exist", statErr)
	}
}
