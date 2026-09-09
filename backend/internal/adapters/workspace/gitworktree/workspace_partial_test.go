package gitworktree

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func partialWorkspaceFixture(t *testing.T) (*Workspace, ports.WorkspaceProjectConfig) {
	t.Helper()
	git := requireGit(t)
	dir := t.TempDir()
	rootRepo := setupOriginClone(t, git, filepath.Join(dir, "root"))
	childRepo := setupOriginClone(t, git, filepath.Join(dir, "child"))
	w, err := New(Options{Binary: git, ManagedRoot: filepath.Join(dir, "managed"), RepoResolver: StaticRepoResolver{"proj": rootRepo}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(w.discards.Wait)
	return w, ports.WorkspaceProjectConfig{
		ProjectID: "proj", SessionID: "worker", Kind: "worker", Branch: "test/partial-create", RootRepoPath: rootRepo,
		Repos: []ports.WorkspaceProjectRepoConfig{{Name: "child", RelativePath: "child", RepoPath: childRepo}},
	}
}

func TestWorkspaceProjectCreateReturnsPartialOwnershipAfterCancellation(t *testing.T) {
	for _, childCreated := range []bool{false, true} {
		name := "before_child_creation"
		if childCreated {
			name = "lost_child_reply_with_dirty_work"
		}
		t.Run(name, func(t *testing.T) {
			w, cfg := partialWorkspaceFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			runner := w.run
			var cleanupAttempts int
			w.run = func(ctx context.Context, binary string, args ...string) ([]byte, error) {
				joined := strings.Join(args, " ")
				if strings.Contains(joined, "worktree prune") || strings.Contains(joined, "worktree remove") {
					cleanupAttempts++
				}
				if len(args) >= 4 && args[0] == "-C" && args[1] == cfg.Repos[0].RepoPath && args[2] == "worktree" && args[3] == "add" {
					if childCreated {
						if _, err := runner(ctx, binary, args...); err != nil {
							return nil, err
						}
						childPath := filepath.Join(w.managedRoot, "proj", "worker", "child")
						if err := os.WriteFile(filepath.Join(childPath, "pending-work.txt"), []byte("keep this work"), 0o600); err != nil {
							t.Fatal(err)
						}
					}
					cancel()
					return nil, ctx.Err()
				}
				return runner(ctx, binary, args...)
			}
			info, err := w.CreateWorkspaceProject(ctx, cfg)
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("create error = %v, want cancellation", err)
			}
			if info.Root.Path == "" || len(info.Worktrees) != 2 || info.Worktrees[1].RepoName != "child" {
				t.Fatalf("create discarded partial resource identities: %+v", info)
			}
			if cleanupAttempts != 0 {
				t.Fatalf("adapter attempted cleanup under the expired request: %d calls", cleanupAttempts)
			}
			if _, err := os.Stat(filepath.Join(info.Root.Path, "README.md")); err != nil {
				t.Fatalf("created root vanished before caller cleanup: %v", err)
			}
			w.run = runner
			err = w.DestroyWorkspaceProject(context.Background(), info)
			if childCreated {
				if !errors.Is(err, ports.ErrWorkspaceDirty) {
					t.Fatalf("dirty child cleanup = %v, want preserved workspace", err)
				}
				if data, err := os.ReadFile(filepath.Join(info.Worktrees[1].Path, "pending-work.txt")); err != nil || string(data) != "keep this work" {
					t.Fatalf("partial child work was deleted: data=%q err=%v", data, err)
				}
				if _, err := os.Stat(info.Root.Path); err != nil {
					t.Fatalf("parent removed despite child refusal: %v", err)
				}
			} else if err != nil {
				t.Fatalf("caller cleanup of partial clean workspace: %v", err)
			} else if _, err := os.Stat(info.Root.Path); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("clean partial workspace remains: %v", err)
			}
		})
	}
}

func TestWorkspaceProjectCleanupPreservesUnprovenChildOwnership(t *testing.T) {
	for _, wrongBranch := range []bool{false, true} {
		name := "unregistered_directory"
		if wrongBranch {
			name = "replacement_branch"
		}
		t.Run(name, func(t *testing.T) {
			w, cfg := partialWorkspaceFixture(t)
			info, err := w.CreateWorkspaceProject(context.Background(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			child := info.Worktrees[1]
			if wrongBranch {
				runGit(t, w.binary, child.Path, "checkout", "-b", "replacement-owner")
			} else {
				runGit(t, w.binary, child.RepoPath, "worktree", "remove", child.Path)
				if err := os.MkdirAll(child.Path, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(child.Path, "unowned.txt"), []byte("preserve"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := w.DestroyWorkspaceProject(context.Background(), info); err == nil {
				t.Fatal("cleanup accepted an unproven child identity")
			}
			if data, err := os.ReadFile(filepath.Join(child.Path, "unowned.txt")); err != nil || string(data) != "preserve" {
				t.Fatalf("unproven child data deleted: data=%q err=%v", data, err)
			}
			if _, err := os.Stat(filepath.Join(info.Root.Path, "README.md")); err != nil {
				t.Fatalf("parent removed despite child ownership mismatch: %v", err)
			}
		})
	}
}
