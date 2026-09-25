package integration

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/workspace/gitworktree"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/lifecycle"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	sessionmanager "github.com/aoagents/agent-orchestrator/backend/internal/session_manager"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/sqlitetest"
)

func runGit(t *testing.T, git, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command(git, args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// seedOriginClone builds a clone with a resolvable default branch, which is
// what the gitworktree adapter requires before it will create a worktree.
func seedOriginClone(t *testing.T, git, tmp string) string {
	t.Helper()
	origin := filepath.Join(tmp, "origin.git")
	seed := filepath.Join(tmp, "seed")
	repo := filepath.Join(tmp, "repo")
	runGit(t, git, tmp, "init", "--bare", origin)
	runGit(t, git, tmp, "init", seed)
	runGit(t, git, seed, "config", "user.email", "ao@example.com")
	runGit(t, git, seed, "config", "user.name", "Ao Agents")
	if err := os.WriteFile(filepath.Join(seed, "README.md"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, git, seed, "add", "README.md")
	runGit(t, git, seed, "commit", "-m", "seed")
	runGit(t, git, seed, "branch", "-M", "main")
	runGit(t, git, seed, "remote", "add", "origin", origin)
	runGit(t, git, seed, "push", "-u", "origin", "main")
	runGit(t, git, origin, "symbolic-ref", "HEAD", "refs/heads/main")
	runGit(t, git, tmp, "clone", origin, repo)
	runGit(t, git, repo, "config", "user.email", "ao@example.com")
	runGit(t, git, repo, "config", "user.name", "Ao Agents")
	return repo
}

// TestKillAndCleanupReachASessionGitWillNotRelease is the #5463 regression
// against real git, which no fake workspace can stand in for: a worktree
// directory that disappeared out of band while git still holds a lock on the
// registration. `git worktree remove` refuses it, `git worktree prune` declines
// to clear it, and the adapter reports a refusal AO has no typed name for.
//
// That used to fail the kill, which left `terminated` false — and since
// `ao session cleanup` only walks terminated sessions, the row was reachable by
// no path at all and stayed on the board forever.
func TestKillAndCleanupReachASessionGitWillNotRelease(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not available")
	}
	ctx := context.Background()
	tmp := t.TempDir()
	repo := seedOriginClone(t, git, tmp)

	workspace, err := gitworktree.New(gitworktree.Options{
		Binary:       git,
		ManagedRoot:  filepath.Join(tmp, "managed"),
		RepoResolver: gitworktree.StaticRepoResolver{"mer": repo},
	})
	if err != nil {
		t.Fatal(err)
	}
	store, err := sqlitetest.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.UpsertProject(ctx, domain.ProjectRecord{
		ID: "mer", Path: repo, RegisteredAt: time.Now(),
		Config: domain.ProjectConfig{
			Worker:       domain.RoleOverride{Harness: domain.HarnessClaudeCode},
			Orchestrator: domain.RoleOverride{Harness: domain.HarnessClaudeCode},
		},
	}); err != nil {
		t.Fatal(err)
	}
	messenger := &captureMessenger{}
	lcm := lifecycle.New(store, messenger)
	manager := sessionmanager.New(sessionmanager.Deps{
		Runtime: &stubRuntime{}, Agents: stubAgents{}, Workspace: workspace, Store: store,
		Messenger: messenger, Lifecycle: lcm,
		LookPath: func(string) (string, error) { return "/usr/bin/true", nil },
	})
	lcm.SetCompletionTerminator(manager)

	sess, _, _, err := manager.Spawn(ctx, ports.SpawnConfig{ProjectID: "mer", Kind: domain.KindWorker, Prompt: "do it"})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	rec, _, err := store.GetSession(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	worktree := rec.Metadata.WorkspacePath
	runGit(t, git, repo, "worktree", "lock", worktree)
	if err := os.RemoveAll(worktree); err != nil {
		t.Fatal(err)
	}

	freed, err := manager.Kill(ctx, sess.ID)
	if err != nil {
		t.Fatalf("Kill: %v", err)
	}
	if freed {
		t.Fatal("freed = true, want false: git never released the worktree")
	}
	if rec, _, _ = store.GetSession(ctx, sess.ID); !rec.IsTerminated {
		t.Fatal("session must be terminated so it leaves the board and cleanup can see it")
	}

	// Cleanup now reaches the row and names the refusal instead of skipping it
	// silently, so the operator has something to act on.
	res, err := manager.Cleanup(ctx, "mer")
	if err != nil {
		t.Fatalf("Cleanup: %v", err)
	}
	if len(res.Skipped) != 1 || res.Skipped[0].SessionID != sess.ID {
		t.Fatalf("cleanup skipped = %+v, want one entry for %s", res.Skipped, sess.ID)
	}
}
