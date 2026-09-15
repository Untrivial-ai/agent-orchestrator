package gitworktree

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestWorkspaceIntegrationCreateRestoreDestroy(t *testing.T) {
	git := requireGit(t)
	tmp := t.TempDir()
	repo := setupOriginClone(t, git, tmp)
	root := filepath.Join(tmp, "managed")
	ws, err := New(Options{Binary: git, ManagedRoot: root, RepoResolver: StaticRepoResolver{"proj": repo}})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	ctx := context.Background()
	cfg := ports.WorkspaceConfig{ProjectID: "proj", SessionID: "sess", Branch: "feature/one"}

	info, err := ws.Create(ctx, cfg)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if info.Path != filepath.Join(ws.managedRoot, "proj", "sess") || info.Branch != cfg.Branch || info.SessionID != cfg.SessionID || info.ProjectID != cfg.ProjectID {
		t.Fatalf("info = %#v", info)
	}
	if _, err := os.Stat(filepath.Join(info.Path, "README.md")); err != nil {
		t.Fatalf("created worktree missing seed file: %v", err)
	}

	restored, err := ws.Restore(ctx, cfg)
	if err != nil {
		t.Fatalf("restore registered: %v", err)
	}
	if restored.Path != info.Path || restored.Branch != cfg.Branch {
		t.Fatalf("restored = %#v", restored)
	}

	if err := ws.Destroy(ctx, info); err != nil {
		t.Fatalf("destroy: %v", err)
	}
	if _, err := os.Stat(info.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("path after destroy stat err = %v, want not exist", err)
	}

	restored, err = ws.Restore(ctx, cfg)
	if err != nil {
		t.Fatalf("restore after destroy: %v", err)
	}
	if restored.Path != info.Path || restored.Branch != cfg.Branch {
		t.Fatalf("restored after destroy = %#v", restored)
	}
	if err := ws.Destroy(ctx, restored); err != nil {
		t.Fatalf("destroy restored: %v", err)
	}
}

func TestWorkspaceIntegrationMixedWorkspaceContentReachesEverySessionKind(t *testing.T) {
	git := requireGit(t)
	tmp := t.TempDir()
	rootRepo := setupOriginClone(t, git, filepath.Join(tmp, "root"))
	childRepo := setupOriginClone(t, git, filepath.Join(tmp, "child"))
	if err := os.MkdirAll(filepath.Join(rootRepo, "docs"), 0o755); err != nil {
		t.Fatalf("create ordinary folder: %v", err)
	}
	if err := os.WriteFile(filepath.Join(rootRepo, "docs", "guide.md"), []byte("workspace guide\n"), 0o644); err != nil {
		t.Fatalf("write ordinary workspace file: %v", err)
	}
	runGit(t, git, rootRepo, "add", "docs/guide.md")
	runGit(t, git, rootRepo, "commit", "-m", "add workspace docs")
	runGit(t, git, rootRepo, "push", "origin", "HEAD:main")

	ws, err := New(Options{
		Binary: git, ManagedRoot: filepath.Join(tmp, "managed"),
		RepoResolver: StaticRepoResolver{"proj": rootRepo},
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	for _, kind := range []domain.SessionKind{domain.KindWorker, domain.KindOrchestrator} {
		t.Run(string(kind), func(t *testing.T) {
			info, err := ws.CreateWorkspaceProject(context.Background(), ports.WorkspaceProjectConfig{
				ProjectID: "proj", SessionID: domain.SessionID("mixed-" + kind), Kind: kind, Branch: "ao/mixed-" + string(kind),
				RootRepoPath: rootRepo,
				Repos: []ports.WorkspaceProjectRepoConfig{{
					Name: "api", RelativePath: "services/api", RepoPath: childRepo,
				}},
			})
			if err != nil {
				t.Fatalf("create mixed workspace: %v", err)
			}
			t.Cleanup(func() {
				if err := ws.DestroyWorkspaceProject(context.Background(), info); err != nil {
					t.Errorf("destroy mixed workspace: %v", err)
				}
			})
			for _, want := range []string{"docs/guide.md", "services/api/README.md"} {
				if _, err := os.Stat(filepath.Join(info.Root.Path, filepath.FromSlash(want))); err != nil {
					t.Errorf("%s session missing %s: %v", kind, want, err)
				}
			}
		})
	}
}

func TestWorkspaceIntegrationRestoreExistingBranchDoesNotResolveDefault(t *testing.T) {
	git := requireGit(t)
	tmp := t.TempDir()
	repo := setupOriginClone(t, git, tmp)
	ws, err := New(Options{
		Binary:       git,
		ManagedRoot:  filepath.Join(tmp, "managed"),
		RepoResolver: StaticRepoResolver{"proj": repo},
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	ctx := context.Background()
	cfg := ports.WorkspaceConfig{ProjectID: "proj", SessionID: "sess", Branch: "feature/resume"}
	created, err := ws.Create(ctx, cfg)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := ws.Destroy(ctx, created); err != nil {
		t.Fatalf("destroy: %v", err)
	}

	// Simulate restoring after the repository's authoritative remote metadata
	// became unavailable. The session branch still exists locally, so restore
	// only needs to reattach it and must not attempt automatic default
	// resolution again.
	runGit(t, git, repo, "remote", "remove", "origin")
	_, err = ws.Create(ctx, ports.WorkspaceConfig{ProjectID: "proj", SessionID: "probe", Branch: "feature/new"})
	if !errors.Is(err, ports.ErrWorkspaceDefaultBranchUnresolved) {
		t.Fatalf("Create without remote metadata error = %v, want ErrWorkspaceDefaultBranchUnresolved", err)
	}

	cfg.Path = created.Path
	cfg.BaseRef = created.BaseRef
	restored, err := ws.Restore(ctx, cfg)
	if err != nil {
		t.Fatalf("restore existing session branch: %v", err)
	}
	if restored.Path != created.Path || restored.Branch != created.Branch || restored.BaseRef != created.BaseRef {
		t.Fatalf("restored = %#v, want path %q branch %q base ref %q", restored, created.Path, created.Branch, created.BaseRef)
	}
	if got := gitOutput(t, git, restored.Path, "rev-parse", "--abbrev-ref", "HEAD"); got != created.Branch {
		t.Fatalf("restored branch = %q, want %q", got, created.Branch)
	}
}

func TestWorkspaceIntegrationDestroyRefusesLockedWorktree(t *testing.T) {
	git := requireGit(t)
	tmp := t.TempDir()
	repo := setupOriginClone(t, git, tmp)
	root := filepath.Join(tmp, "managed")
	ws, err := New(Options{Binary: git, ManagedRoot: root, RepoResolver: StaticRepoResolver{"proj": repo}})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	ctx := context.Background()
	info, err := ws.Create(ctx, ports.WorkspaceConfig{ProjectID: "proj", SessionID: "sess", Branch: "feature/lock"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	runGit(t, git, repo, "worktree", "lock", info.Path)

	err = ws.Destroy(ctx, info)
	if err == nil || !strings.Contains(err.Error(), "still registered") {
		t.Fatalf("destroy locked error = %v, want still registered refusal", err)
	}
	if _, statErr := os.Stat(filepath.Join(info.Path, "README.md")); statErr != nil {
		t.Fatalf("locked worktree was not preserved: %v", statErr)
	}

	runGit(t, git, repo, "worktree", "unlock", info.Path)
	if err := ws.Destroy(ctx, info); err != nil {
		t.Fatalf("destroy after unlock: %v", err)
	}
}

// TestWorkspaceIntegrationDestroyDirtyWorktree proves the two halves of the
// dirty-teardown contract against real git:
//
//  1. A worktree whose only untracked files are covered by a self-ignoring
//     .gitignore (the shape agent adapters install for their hook files) is
//     clean in git's eyes, so Destroy succeeds without --force.
//  2. Real uncommitted work makes Destroy refuse with ports.ErrWorkspaceDirty
//     and preserves the worktree.
func TestWorkspaceIntegrationDestroyDirtyWorktree(t *testing.T) {
	git := requireGit(t)
	tmp := t.TempDir()
	repo := setupOriginClone(t, git, tmp)
	root := filepath.Join(tmp, "managed")
	ws, err := New(Options{Binary: git, ManagedRoot: root, RepoResolver: StaticRepoResolver{"proj": repo}})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	ctx := context.Background()
	info, err := ws.Create(ctx, ports.WorkspaceConfig{ProjectID: "proj", SessionID: "sess", Branch: "feature/dirty"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// AO-managed hook files behind a self-ignoring .gitignore: invisible to git
	// status, so they must not block teardown.
	hookDir := filepath.Join(info.Path, ".codex")
	if err := os.MkdirAll(hookDir, 0o750); err != nil {
		t.Fatalf("mkdir hook dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(hookDir, "hooks.json"), []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("write hooks.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(hookDir, ".gitignore"), []byte(".gitignore\nhooks.json\n"), 0o600); err != nil {
		t.Fatalf("write .gitignore: %v", err)
	}

	// Real agent work must keep blocking teardown, typed as ErrWorkspaceDirty.
	wip := filepath.Join(info.Path, "wip.txt")
	if err := os.WriteFile(wip, []byte("uncommitted\n"), 0o600); err != nil {
		t.Fatalf("write wip: %v", err)
	}
	err = ws.Destroy(ctx, info)
	if !errors.Is(err, ports.ErrWorkspaceDirty) {
		t.Fatalf("destroy dirty error = %v, want ports.ErrWorkspaceDirty", err)
	}
	if _, statErr := os.Stat(wip); statErr != nil {
		t.Fatalf("dirty worktree was not preserved: %v", statErr)
	}

	// With the real work gone, only the ignored AO files remain — git considers
	// the worktree clean and Destroy succeeds without --force.
	if err := os.Remove(wip); err != nil {
		t.Fatalf("remove wip: %v", err)
	}
	if err := ws.Destroy(ctx, info); err != nil {
		t.Fatalf("destroy with ignored-only files: %v", err)
	}
	if _, err := os.Stat(info.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("path after destroy stat err = %v, want not exist", err)
	}
}

// TestWorkspaceIntegrationRestoreRecreatesSiblingsIndependently is the
// real-git regression test for the sibling-registration-wipe finding on
// pruneIfWorktreeDirMissing (PR #3098 review, illegalcall): two sessions in
// the SAME repo each have their worktree directory deleted out of band (their
// git registrations survive, exactly the #2775 shape). Restoring the first
// must not touch the second's registration.
//
// Verified against real git before the fix: with two worktrees on distinct
// branches both missing their directories, a single `git worktree prune`
// removed BOTH stale registrations at once. So restoring session A pruned
// session B's registration as a side effect, and session B's later Restore
// then found no record and fell back to whatever branch its caller passed
// instead of the branch it was actually on: the exact "recreated on the
// wrong branch" bug recreateBranch exists to prevent, reintroduced for every
// sibling session that never asked to be touched by A's restore.
func TestWorkspaceIntegrationRestoreRecreatesSiblingsIndependently(t *testing.T) {
	git := requireGit(t)
	tmp := t.TempDir()
	repo := setupOriginClone(t, git, tmp)
	root := filepath.Join(tmp, "managed")
	ws, err := New(Options{Binary: git, ManagedRoot: root, RepoResolver: StaticRepoResolver{"proj": repo}})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	ctx := context.Background()

	infoA, err := ws.Create(ctx, ports.WorkspaceConfig{ProjectID: "proj", SessionID: "sess-a", Branch: "child-a"})
	if err != nil {
		t.Fatalf("create A: %v", err)
	}
	infoB, err := ws.Create(ctx, ports.WorkspaceConfig{ProjectID: "proj", SessionID: "sess-b", Branch: "child-b"})
	if err != nil {
		t.Fatalf("create B: %v", err)
	}

	// Simulate the out-of-band deletion #2775 hit: the directory is gone, the
	// git registration (and, in production, AO's DB row) is not.
	if err := os.RemoveAll(infoA.Path); err != nil {
		t.Fatalf("remove A dir: %v", err)
	}
	if err := os.RemoveAll(infoB.Path); err != nil {
		t.Fatalf("remove B dir: %v", err)
	}

	// cfg.Branch is deliberately wrong for both restores: if either recreates
	// on cfg.Branch instead of its own registration's branch, this test
	// catches it (same shape as TestRestoreRecreatesOnRegisteredBranchNotCfgBranch,
	// but against real git instead of the fake runner, since this bug lives in
	// git's own prune semantics).
	restoredA, err := ws.Restore(ctx, ports.WorkspaceConfig{ProjectID: "proj", SessionID: "sess-a", Branch: "wrong-branch-a", Path: infoA.Path})
	if err != nil {
		t.Fatalf("restore A: %v", err)
	}
	if restoredA.Branch != "child-a" {
		t.Fatalf("restored A branch = %q, want child-a", restoredA.Branch)
	}

	// The finding's assertion: B's registration must have survived A's restore.
	restoredB, err := ws.Restore(ctx, ports.WorkspaceConfig{ProjectID: "proj", SessionID: "sess-b", Branch: "wrong-branch-b", Path: infoB.Path})
	if err != nil {
		t.Fatalf("restore B: %v", err)
	}
	if restoredB.Branch != "child-b" {
		t.Fatalf("restored B branch = %q, want child-b (A's restore must not have wiped B's registration)", restoredB.Branch)
	}

	if err := ws.Destroy(ctx, restoredA); err != nil {
		t.Fatalf("destroy A: %v", err)
	}
	if err := ws.Destroy(ctx, restoredB); err != nil {
		t.Fatalf("destroy B: %v", err)
	}
}

// TestWorkspaceIntegrationRestoreLockedMissingWorktreeIsTypedError is the
// real-git regression test for the locked-worktree finding on
// pruneIfWorktreeDirMissing (PR #3098 review, illegalcall): `git worktree
// prune` deliberately leaves a locked registration in place even when its
// directory is gone, and both `git worktree add` and
// `git worktree remove --force` at that path then fail with an opaque
// "missing but locked worktree" git error. pruneIfWorktreeDirMissing must
// detect the lock itself from the registration and fail with the typed
// ports.ErrWorkspaceLocked before ever calling git to add or remove, rather
// than relaying that opaque failure downstream.
func TestWorkspaceIntegrationRestoreLockedMissingWorktreeIsTypedError(t *testing.T) {
	git := requireGit(t)
	tmp := t.TempDir()
	repo := setupOriginClone(t, git, tmp)
	root := filepath.Join(tmp, "managed")
	ws, err := New(Options{Binary: git, ManagedRoot: root, RepoResolver: StaticRepoResolver{"proj": repo}})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	ctx := context.Background()
	info, err := ws.Create(ctx, ports.WorkspaceConfig{ProjectID: "proj", SessionID: "sess-locked", Branch: "child-locked"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	runGit(t, git, repo, "worktree", "lock", info.Path)
	if err := os.RemoveAll(info.Path); err != nil {
		t.Fatalf("remove dir: %v", err)
	}

	// Wrap the real runner (still executes real git for every call) to catch
	// whether Restore ever attempts `worktree add` at the locked path: it must
	// not, since attempting it is exactly the opaque failure this fix avoids.
	var attemptedAdd bool
	ws.run = func(ctx context.Context, binary string, args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		if strings.Contains(joined, "worktree add") && strings.Contains(joined, info.Path) {
			attemptedAdd = true
		}
		return runCommand(ctx, binary, args...)
	}

	_, restoreErr := ws.Restore(ctx, ports.WorkspaceConfig{ProjectID: "proj", SessionID: "sess-locked", Branch: "child-locked", Path: info.Path})
	if !errors.Is(restoreErr, ports.ErrWorkspaceLocked) {
		t.Fatalf("restore locked-missing error = %v, want ports.ErrWorkspaceLocked", restoreErr)
	}
	if attemptedAdd {
		t.Fatal("Restore attempted `git worktree add` at a locked, missing worktree path")
	}

	// Clean up: unlock so TempDir teardown (and, defensively, any leftover
	// registration) does not leave a locked path behind.
	runGit(t, git, repo, "worktree", "unlock", info.Path)
}

// TestWorkspaceIntegrationRestoreDoesNotDestroyWorktreeRecreatedMidRecovery is
// the real-git regression test for the concurrent-restore finding on stale
// registration recovery (PR #3098 review, illegalcall). The "this registration's
// directory is missing" observation and the recovery that acts on it cannot be
// one atomic step, and RestoreWithMode has no per-session restore guard, so a
// second restore of the same session can recreate the worktree in between. The
// earlier fix recovered with `git worktree remove --force <path>`, which in that
// window deletes a live worktree and every uncommitted file in it. Recovery now
// goes through `git worktree add --force`, git's own override for a
// missing-but-registered path, which git refuses outright when the directory
// exists and is non-empty: the losing restore fails loudly and the winner's work
// survives.
//
// The interleaving is forced deterministically rather than raced: the hooked
// runner recreates the worktree and writes an uncommitted sentinel file just
// before the first registration-mutating git command Restore issues, which is
// exactly the window the finding describes.
func TestWorkspaceIntegrationRestoreDoesNotDestroyWorktreeRecreatedMidRecovery(t *testing.T) {
	git := requireGit(t)
	tmp := t.TempDir()
	repo := setupOriginClone(t, git, tmp)
	root := filepath.Join(tmp, "managed")
	ws, err := New(Options{Binary: git, ManagedRoot: root, RepoResolver: StaticRepoResolver{"proj": repo}})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	ctx := context.Background()
	info, err := ws.Create(ctx, ports.WorkspaceConfig{ProjectID: "proj", SessionID: "sess", Branch: "child"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// The out-of-band deletion #2775 hit: directory gone, registration intact.
	if err := os.RemoveAll(info.Path); err != nil {
		t.Fatalf("remove dir: %v", err)
	}

	sentinel := filepath.Join(info.Path, "AGENT_WORK.txt")
	raced := false
	ws.run = func(ctx context.Context, binary string, args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		mutatesRegistration := strings.Contains(joined, "worktree add") ||
			strings.Contains(joined, "worktree remove") ||
			strings.Contains(joined, "worktree prune")
		if !raced && mutatesRegistration {
			raced = true
			// Stand in for the concurrent restore that wins the race: it
			// recreates the worktree at the same path, and its agent
			// immediately has uncommitted work there.
			runGit(t, git, repo, "worktree", "add", "--force", info.Path, "child")
			if err := os.WriteFile(sentinel, []byte("uncommitted agent work\n"), 0o644); err != nil {
				t.Fatalf("write sentinel: %v", err)
			}
		}
		return runCommand(ctx, binary, args...)
	}

	restored, restoreErr := ws.Restore(ctx, ports.WorkspaceConfig{ProjectID: "proj", SessionID: "sess", Branch: "child", Path: info.Path})
	if !raced {
		t.Fatal("Restore issued no registration-mutating git command, so the race window was never forced")
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("uncommitted work in the concurrently recreated worktree did not survive stale-registration recovery: %v", err)
	}
	// Losing the race must not be reported as success for a worktree this
	// restore did not materialize.
	if restoreErr == nil && restored.Path != info.Path {
		t.Fatalf("restored = %#v, want a handle to the live worktree at %q", restored, info.Path)
	}
}

// TestWorkspaceIntegrationAddNewBranchRecoversStaleRegistration is the real-git
// regression test for the branch-already-created finding (PR #3098 review,
// illegalcall): `git worktree add -b <branch> <path> <base>` creates
// refs/heads/<branch> BEFORE it validates <path>, so an attempt that fails on a
// stale registration still leaves the branch behind. Retrying the same `-b`
// form with --force therefore never recovered, it failed with "a branch named
// ... already exists", leaving the stray ref and no worktree.
//
// The fake runner could not catch this: it modelled the failed `-b` invocation
// as side-effect-free, so the `--force -b` retry "succeeded" there while real
// git refused it. This drives real git instead, at the same entry point both
// callers use (force=false against a path git already has a registration for:
// createWorkspaceProjectRepo's shape before its pre-check existed, and
// addWorktree's shape when the registration goes stale after its pre-check).
//
// Verified to fail on the previous commit with
// `fatal: a branch named 'child-new' already exists`.
func TestWorkspaceIntegrationAddNewBranchRecoversStaleRegistration(t *testing.T) {
	git := requireGit(t)
	tmp := t.TempDir()
	repo := setupOriginClone(t, git, tmp)
	root := filepath.Join(tmp, "managed")
	ws, err := New(Options{Binary: git, ManagedRoot: root, RepoResolver: StaticRepoResolver{"proj": repo}})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	ctx := context.Background()

	// A registration whose directory is gone: the #2775 shape.
	info, err := ws.Create(ctx, ports.WorkspaceConfig{ProjectID: "proj", SessionID: "sess", Branch: "child-old"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := os.RemoveAll(info.Path); err != nil {
		t.Fatalf("remove dir: %v", err)
	}

	// force=false is what a caller passes when its own pre-check did not see the
	// registration go stale, so this exercises the recovery, not the pre-check.
	if err := ws.addNewBranchWorktree(ctx, repo, "child-new", info.Path, "origin/main", false); err != nil {
		t.Fatalf("addNewBranchWorktree over a stale registration: %v", err)
	}

	if _, err := os.Stat(filepath.Join(info.Path, "README.md")); err != nil {
		t.Fatalf("recovered worktree was not materialized: %v", err)
	}
	out, err := exec.Command(git, "-C", info.Path, "rev-parse", "--abbrev-ref", "HEAD").Output()
	if err != nil {
		t.Fatalf("rev-parse recovered worktree: %v", err)
	}
	if got := strings.TrimSpace(string(out)); got != "child-new" {
		t.Fatalf("recovered worktree branch = %q, want child-new", got)
	}
	// The branch must sit on the requested base, i.e. the retry produced what
	// the `-b` form would have, not some other ref.
	head, err := exec.Command(git, "-C", repo, "rev-parse", "refs/heads/child-new").Output()
	if err != nil {
		t.Fatalf("rev-parse child-new: %v", err)
	}
	base, err := exec.Command(git, "-C", repo, "rev-parse", "origin/main").Output()
	if err != nil {
		t.Fatalf("rev-parse origin/main: %v", err)
	}
	if strings.TrimSpace(string(head)) != strings.TrimSpace(string(base)) {
		t.Fatalf("child-new = %s, want origin/main %s", head, base)
	}
}

// TestWorkspaceIntegrationAutoRejectsUnmarkedRemotelessRepo proves automatic
// selection never silently treats the active (or conventionally named) local
// branch as the repository default.
func TestWorkspaceIntegrationAutoRejectsUnmarkedRemotelessRepo(t *testing.T) {
	git := requireGit(t)
	tmp := t.TempDir()
	repo := filepath.Join(tmp, "unmarked")
	run(t, git, "init", "-b", "main", repo)
	runGit(t, git, repo, "config", "core.autocrlf", "false")
	runGit(t, git, repo, "config", "user.email", "ao@example.com")
	runGit(t, git, repo, "config", "user.name", "Ao Agents")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("seed\n"), 0o644); err != nil {
		t.Fatalf("write seed: %v", err)
	}
	runGit(t, git, repo, "add", "README.md")
	runGit(t, git, repo, "commit", "-m", "seed")
	runGit(t, git, repo, "switch", "-c", "feature/temporary")

	root := filepath.Join(tmp, "managed")
	ws, err := New(Options{Binary: git, ManagedRoot: root, RepoResolver: StaticRepoResolver{"proj": repo}})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	ctx := context.Background()
	_, err = ws.Create(ctx, ports.WorkspaceConfig{ProjectID: "proj", SessionID: "sess", Branch: "ao/proj-1"})
	if !errors.Is(err, ports.ErrWorkspaceDefaultBranchUnresolved) {
		t.Fatalf("Create error = %v, want ErrWorkspaceDefaultBranchUnresolved", err)
	}
	if !strings.Contains(err.Error(), "remote set-head") || !strings.Contains(err.Error(), repo) {
		t.Fatalf("Create error = %v, want repository-specific remote HEAD remediation", err)
	}
	if out, err := exec.Command(git, "-C", repo, "show-ref", "--verify", "--quiet", "refs/heads/ao/proj-1").CombinedOutput(); err == nil {
		t.Fatalf("unresolved auto selection leaked a session branch: %s", out)
	}
}

func TestWorkspaceIntegrationAutoUsesAOInitializedRemotelessDefault(t *testing.T) {
	git := requireGit(t)
	tmp := t.TempDir()
	repo := filepath.Join(tmp, "repo")
	run(t, git, "init", "-b", "main", repo)
	runGit(t, git, repo, "config", "core.autocrlf", "false")
	runGit(t, git, repo, "config", "user.email", "ao@example.com")
	runGit(t, git, repo, "config", "user.name", "Ao Agents")
	runGit(t, git, repo, "config", "ao.defaultBranch", "main")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("seed\n"), 0o644); err != nil {
		t.Fatalf("write seed: %v", err)
	}
	runGit(t, git, repo, "add", "README.md")
	runGit(t, git, repo, "commit", "-m", "seed")
	runGit(t, git, repo, "switch", "-c", "feature/temporary")

	ws, err := New(Options{
		Binary: git, ManagedRoot: filepath.Join(tmp, "managed"), RepoResolver: StaticRepoResolver{"proj": repo},
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	ctx := context.Background()
	info, err := ws.Create(ctx, ports.WorkspaceConfig{ProjectID: "proj", SessionID: "sess", Branch: "ao/proj-1"})
	if err != nil {
		t.Fatalf("create in AO-initialized repo: %v", err)
	}
	if got := gitOutput(t, git, info.Path, "merge-base", "HEAD", "refs/heads/main"); got != gitOutput(t, git, repo, "rev-parse", "refs/heads/main") {
		t.Fatalf("worktree base = %s, want AO-recorded main", got)
	}
	if err := ws.Destroy(ctx, info); err != nil {
		t.Fatalf("destroy: %v", err)
	}
}

func TestWorkspaceIntegrationAutoUsesRequestedRemoteBranchWithoutRemoteHead(t *testing.T) {
	git := requireGit(t)
	tmp := t.TempDir()
	repo := setupOriginClone(t, git, tmp)
	branch := "feature/resume"
	sha := gitOutput(t, git, repo, "rev-parse", "HEAD")
	runGit(t, git, repo, "update-ref", "refs/remotes/origin/"+branch, sha)
	runGit(t, git, repo, "symbolic-ref", "--delete", "refs/remotes/origin/HEAD")
	origin := gitOutput(t, git, repo, "remote", "get-url", "origin")
	runGit(t, git, origin, "update-ref", "--no-deref", "HEAD", sha)

	ws, err := New(Options{
		Binary: git, ManagedRoot: filepath.Join(tmp, "managed"), RepoResolver: StaticRepoResolver{"proj": repo},
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	info, err := ws.Create(context.Background(), ports.WorkspaceConfig{
		ProjectID: "proj", SessionID: "sess", Branch: branch,
	})
	if err != nil {
		t.Fatalf("create from requested remote branch: %v", err)
	}
	if info.BaseRef != "refs/remotes/origin/"+branch {
		t.Fatalf("base ref = %q, want requested origin branch", info.BaseRef)
	}
	if got := gitOutput(t, git, info.Path, "rev-parse", "HEAD"); got != sha {
		t.Fatalf("worktree HEAD = %s, want requested remote SHA %s", got, sha)
	}
}

func TestWorkspaceIntegrationRequestedRemoteBranchKeepsDefaultComparisonBase(t *testing.T) {
	git := requireGit(t)
	for _, tc := range []struct {
		name        string
		baseBranch  string
		wantBaseRef string
	}{
		{name: "automatic default", wantBaseRef: "refs/remotes/origin/main"},
		{name: "explicit default", baseBranch: "main", wantBaseRef: "origin/main"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tmp := t.TempDir()
			repo := setupOriginClone(t, git, tmp)
			branch := "feature/resume"
			defaultSHA, featureSHA := pushRemoteBranchCommit(t, git, repo, "main", branch)

			ws, err := New(Options{
				Binary: git, ManagedRoot: filepath.Join(tmp, "managed"), RepoResolver: StaticRepoResolver{"proj": repo},
			})
			if err != nil {
				t.Fatalf("new: %v", err)
			}
			info, err := ws.Create(context.Background(), ports.WorkspaceConfig{
				ProjectID: "proj", SessionID: "sess", Branch: branch, BaseBranch: tc.baseBranch,
			})
			if err != nil {
				t.Fatalf("create from requested remote branch: %v", err)
			}
			if info.BaseRef != tc.wantBaseRef {
				t.Fatalf("base ref = %q, want repository default %q", info.BaseRef, tc.wantBaseRef)
			}
			if got := gitOutput(t, git, info.Path, "rev-parse", "HEAD"); got != featureSHA {
				t.Fatalf("worktree HEAD = %s, want requested remote branch %s", got, featureSHA)
			}
			if got := gitOutput(t, git, info.Path, "merge-base", "HEAD", info.BaseRef); got != defaultSHA {
				t.Fatalf("comparison merge-base = %s, want repository default %s", got, defaultSHA)
			}
		})
	}
}

func TestWorkspaceIntegrationRemotelessRootUsesImportedDefaultBranch(t *testing.T) {
	git := requireGit(t)
	tmp := t.TempDir()
	rootRepo := setupOriginCloneOnBranch(t, git, filepath.Join(tmp, "root"), "trunk")
	gitOutput(t, git, rootRepo, "remote", "remove", "origin")
	childRepo := setupOriginCloneOnBranch(t, git, filepath.Join(tmp, "child"), "dev")
	ws, err := New(Options{Binary: git, ManagedRoot: filepath.Join(tmp, "managed"), RepoResolver: StaticRepoResolver{"proj": rootRepo}})
	if err != nil {
		t.Fatal(err)
	}
	cfg := ports.WorkspaceProjectConfig{
		ProjectID: "proj", SessionID: "orch", Kind: "orchestrator", Branch: "ao/proj-orch",
		RootRepoPath: rootRepo,
		BaseBranch:   "trunk",
		Repos:        []ports.WorkspaceProjectRepoConfig{{Name: "api", RelativePath: "api", RepoPath: childRepo}},
	}
	info, err := ws.CreateWorkspaceProject(context.Background(), cfg)
	if err != nil {
		t.Fatalf("spawn with imported root default: %v", err)
	}
	defer func() {
		if err := ws.DestroyWorkspaceProject(context.Background(), info); err != nil {
			t.Errorf("destroy: %v", err)
		}
	}()
	if len(info.Worktrees) != 2 {
		t.Fatalf("worktrees = %d, want root and child", len(info.Worktrees))
	}
	for _, wt := range info.Worktrees {
		want := gitOutput(t, git, rootRepo, "rev-parse", "trunk")
		if wt.RepoName == "api" {
			want = gitOutput(t, git, childRepo, "rev-parse", "origin/dev")
		}
		if got := gitOutput(t, git, wt.Path, "rev-parse", "HEAD"); got != want {
			t.Errorf("%s HEAD = %s, want %s", wt.RepoName, got, want)
		}
	}
}

func TestWorkspaceIntegrationWorkspaceProjectInfersPerRepoDefaultBranches(t *testing.T) {
	git := requireGit(t)
	tmp := t.TempDir()
	rootRepo := setupOriginCloneOnBranch(t, git, filepath.Join(tmp, "root"), "trunk")
	devChildRepo := setupOriginCloneOnBranch(t, git, filepath.Join(tmp, "dev-child"), "dev")
	mainChildRepo := setupOriginClone(t, git, filepath.Join(tmp, "main-child"))
	devDefaultSHA, devSessionSHA := pushRemoteBranchCommit(t, git, devChildRepo, "dev", "ao/proj-1")

	root := filepath.Join(tmp, "managed")
	ws, err := New(Options{Binary: git, ManagedRoot: root, RepoResolver: StaticRepoResolver{"proj": rootRepo}})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	info, err := ws.CreateWorkspaceProject(context.Background(), ports.WorkspaceProjectConfig{
		ProjectID:    "proj",
		SessionID:    "sess",
		Kind:         "worker",
		Branch:       "ao/proj-1",
		RootRepoPath: rootRepo,
		Repos: []ports.WorkspaceProjectRepoConfig{
			{
				Name:         "api",
				RelativePath: "services/api",
				RepoPath:     devChildRepo,
			},
			{
				Name:         "web",
				RelativePath: "apps/web",
				RepoPath:     mainChildRepo,
			},
		},
	})
	if err != nil {
		t.Fatalf("create workspace project: %v", err)
	}
	if len(info.Worktrees) != 3 {
		t.Fatalf("worktrees = %d, want root and two children: %#v", len(info.Worktrees), info.Worktrees)
	}
	wantRefs := map[string]string{
		"__root__": "refs/heads/trunk",
		"api":      "refs/remotes/origin/dev",
		"web":      "refs/remotes/origin/main",
	}
	for _, wt := range info.Worktrees {
		if wt.BaseRef != wantRefs[wt.RepoName] {
			t.Fatalf("worktree %q base ref = %q, want %q", wt.RepoName, wt.BaseRef, wantRefs[wt.RepoName])
		}
		if got := gitOutput(t, git, wt.RepoPath, "rev-parse", wt.BaseRef); wt.BaseSHA != got {
			t.Fatalf("worktree %q base SHA = %q, want default SHA %q", wt.RepoName, wt.BaseSHA, got)
		}
	}
	devChildPath := filepath.Join(info.Root.Path, "services", "api")
	if _, err := os.Stat(filepath.Join(devChildPath, "README.md")); err != nil {
		t.Fatalf("dev child worktree missing seed file: %v", err)
	}
	devChildHead := gitOutput(t, git, devChildRepo, "rev-parse", "refs/heads/ao/proj-1")
	devChildBase := gitOutput(t, git, devChildRepo, "rev-parse", "origin/dev")
	if devChildBase != devDefaultSHA {
		t.Fatalf("dev child default = %s, want original default %s", devChildBase, devDefaultSHA)
	}
	if devChildHead != devSessionSHA {
		t.Fatalf("dev child branch seed = %s, want requested remote branch %s", devChildHead, devSessionSHA)
	}
	mainChildHead := gitOutput(t, git, mainChildRepo, "rev-parse", "refs/heads/ao/proj-1")
	mainChildBase := gitOutput(t, git, mainChildRepo, "rev-parse", "origin/main")
	if mainChildHead != mainChildBase {
		t.Fatalf("main child branch base = %s, want origin/main %s", mainChildHead, mainChildBase)
	}
	rootHead := gitOutput(t, git, rootRepo, "rev-parse", "refs/heads/ao/proj-1")
	rootBase := gitOutput(t, git, rootRepo, "rev-parse", "origin/trunk")
	if rootHead != rootBase {
		t.Fatalf("root branch base = %s, want origin/trunk %s", rootHead, rootBase)
	}
	if err := ws.DestroyWorkspaceProject(context.Background(), info); err != nil {
		t.Fatalf("destroy workspace project: %v", err)
	}
}

func TestWorkspaceIntegrationWorkspaceProjectCopiesAssetsAndCleansSessionCopy(t *testing.T) {
	git := requireGit(t)
	tmp := t.TempDir()
	rootRepo := setupOriginClone(t, git, filepath.Join(tmp, "root"))
	childRepo := setupOriginClone(t, git, filepath.Join(rootRepo, "api"))
	asset := filepath.Join(rootRepo, "notes")
	if err := os.MkdirAll(filepath.Join(asset, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	sourceFile := filepath.Join(asset, "nested", "context.txt")
	if err := os.WriteFile(sourceFile, []byte("source context"), 0o640); err != nil {
		t.Fatal(err)
	}
	runGit(t, git, rootRepo, "add", "notes/nested/context.txt")
	runGit(t, git, rootRepo, "commit", "-m", "track workspace notes")
	runGit(t, git, rootRepo, "push", "origin", "main")
	if err := os.Mkdir(filepath.Join(asset, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(asset, ".git", "secret"), []byte("never copy"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("nested/context.txt", filepath.Join(asset, "latest")); err != nil {
		t.Fatal(err)
	}

	ws, err := New(Options{Binary: git, ManagedRoot: filepath.Join(tmp, "managed"), RepoResolver: StaticRepoResolver{"proj": rootRepo}})
	if err != nil {
		t.Fatal(err)
	}
	info, err := ws.CreateWorkspaceProject(context.Background(), ports.WorkspaceProjectConfig{
		ProjectID: "proj", SessionID: "sess", Kind: "worker", Branch: "ao/assets",
		RootRepoPath: rootRepo,
		Repos:        []ports.WorkspaceProjectRepoConfig{{Name: "api", RelativePath: "api", RepoPath: childRepo}},
		Assets:       []ports.WorkspaceProjectAssetConfig{{RelativePath: "notes", SourcePath: asset}},
	})
	if err != nil {
		t.Fatalf("create workspace project: %v", err)
	}
	copied, err := os.ReadFile(filepath.Join(info.Root.Path, "notes", "nested", "context.txt"))
	if err != nil || string(copied) != "source context" {
		t.Fatalf("copied asset = %q, %v", copied, err)
	}
	if target, err := os.Readlink(filepath.Join(info.Root.Path, "notes", "latest")); err != nil || target != "nested/context.txt" {
		t.Fatalf("copied symlink = %q, %v", target, err)
	}
	if _, err := os.Stat(filepath.Join(info.Root.Path, "notes", ".git")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("asset .git metadata was copied: %v", err)
	}
	if len(info.Worktrees) != 2 || info.Worktrees[1].RepoName != "api" {
		t.Fatalf("worktrees = %#v, want root and independent api child", info.Worktrees)
	}
	if _, err := os.Stat(filepath.Join(info.Root.Path, "api", "README.md")); err != nil {
		t.Fatalf("child worktree missing: %v", err)
	}
	if err := ws.DestroyWorkspaceProject(context.Background(), info); err != nil {
		t.Fatalf("destroy workspace project: %v", err)
	}
	if _, err := os.Stat(info.Root.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("session copy still exists after cleanup: %v", err)
	}
	if got, err := os.ReadFile(sourceFile); err != nil || string(got) != "source context" {
		t.Fatalf("source asset changed during lifecycle: %q, %v", got, err)
	}
}

func TestWorkspaceIntegrationWorkspaceProjectRepairsRemotelessRootDefault(t *testing.T) {
	git := requireGit(t)
	for _, kind := range []domain.SessionKind{domain.KindWorker, domain.KindOrchestrator} {
		t.Run(string(kind), func(t *testing.T) {
			tmp := t.TempDir()
			rootRepo := filepath.Join(tmp, "root")
			run(t, git, "init", "-b", "trunk", rootRepo)
			runGit(t, git, rootRepo, "config", "user.email", "ao@example.com")
			runGit(t, git, rootRepo, "config", "user.name", "AO Test")
			if err := os.WriteFile(filepath.Join(rootRepo, "README.md"), []byte("root\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			runGit(t, git, rootRepo, "add", "README.md")
			runGit(t, git, rootRepo, "commit", "-m", "initial")
			childRepo := setupOriginClone(t, git, filepath.Join(rootRepo, "api"))

			ws, err := New(Options{Binary: git, ManagedRoot: filepath.Join(tmp, "managed"), RepoResolver: StaticRepoResolver{"proj": rootRepo}})
			if err != nil {
				t.Fatal(err)
			}
			info, err := ws.CreateWorkspaceProject(context.Background(), ports.WorkspaceProjectConfig{
				ProjectID: "proj", SessionID: domain.SessionID("sess-" + string(kind)), Kind: kind, Branch: "ao/local-root-" + string(kind),
				RootRepoPath: rootRepo,
				Repos:        []ports.WorkspaceProjectRepoConfig{{Name: "api", RelativePath: "api", RepoPath: childRepo}},
			})
			if err != nil {
				t.Fatalf("create workspace project: %v", err)
			}
			defer func() { _ = ws.DestroyWorkspaceProject(context.Background(), info) }()
			if info.Root.BaseRef != "refs/heads/trunk" {
				t.Fatalf("root BaseRef = %q, want local trunk", info.Root.BaseRef)
			}
			if got := gitOutput(t, git, rootRepo, "config", "--local", "--get", "ao.defaultBranch"); got != "trunk" {
				t.Fatalf("recorded root default = %q, want trunk", got)
			}
			if len(info.Worktrees) != 2 || info.Worktrees[1].RepoName != "api" {
				t.Fatalf("worktrees = %#v, want repaired root and api child", info.Worktrees)
			}
		})
	}
}

func TestWorkspaceIntegrationWorkspaceProjectAssetCopyFailureRollsBackRoot(t *testing.T) {
	git := requireGit(t)
	tmp := t.TempDir()
	rootRepo := setupOriginClone(t, git, filepath.Join(tmp, "root"))
	asset := filepath.Join(rootRepo, "notes")
	if err := os.Mkdir(asset, 0o755); err != nil {
		t.Fatal(err)
	}
	conflictingFile := filepath.Join(rootRepo, "conflict.txt")
	if err := os.WriteFile(conflictingFile, []byte("conflict"), 0o644); err != nil {
		t.Fatal(err)
	}
	ws, err := New(Options{Binary: git, ManagedRoot: filepath.Join(tmp, "managed"), RepoResolver: StaticRepoResolver{"proj": rootRepo}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = ws.CreateWorkspaceProject(context.Background(), ports.WorkspaceProjectConfig{
		ProjectID: "proj", SessionID: "sess", Kind: "worker", Branch: "ao/assets-fail",
		RootRepoPath: rootRepo,
		Assets: []ports.WorkspaceProjectAssetConfig{
			{RelativePath: "notes", SourcePath: asset},
			{RelativePath: "notes", SourcePath: conflictingFile},
		},
	})
	if err == nil {
		t.Fatal("expected conflicting asset destination to fail")
	}
	rootPath := filepath.Join(tmp, "managed", "proj", "worker", "sess")
	if _, statErr := os.Stat(rootPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("root worktree was not rolled back: %v", statErr)
	}
}

func TestFetchDefaultBranchRefreshesRemoteTrackingRef(t *testing.T) {
	git := requireGit(t)
	tmp := t.TempDir()
	repo := setupOriginClone(t, git, tmp)
	root := filepath.Join(tmp, "managed")
	ws, err := New(Options{Binary: git, ManagedRoot: root, RepoResolver: StaticRepoResolver{"proj": repo}})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	// Exclude main from remote.origin.fetch. The explicit destination refspec
	// must still update origin/main instead of succeeding with only FETCH_HEAD.
	runGit(t, git, repo, "config", "remote.origin.fetch", "+refs/heads/restricted-only:refs/remotes/origin/restricted-only")
	originURL := gitOutput(t, git, repo, "remote", "get-url", "origin")
	updater := filepath.Join(tmp, "updater")
	run(t, git, "clone", originURL, updater)
	runGit(t, git, updater, "config", "user.email", "ao@example.com")
	runGit(t, git, updater, "config", "user.name", "Ao Agents")
	runGit(t, git, updater, "checkout", "-B", "main", "origin/main")
	if err := os.WriteFile(filepath.Join(updater, "fresh.txt"), []byte("fresh\n"), 0o644); err != nil {
		t.Fatalf("write fresh file: %v", err)
	}
	runGit(t, git, updater, "add", "fresh.txt")
	runGit(t, git, updater, "commit", "-m", "fresh")
	freshMain := gitOutput(t, git, updater, "rev-parse", "HEAD")
	runGit(t, git, updater, "push", "origin", "HEAD:main")

	staleOriginMain := gitOutput(t, git, repo, "rev-parse", "refs/remotes/origin/main")
	if staleOriginMain == freshMain {
		t.Fatal("test setup did not leave local origin/main stale")
	}
	target, err := ws.ResolveDefaultBranch(context.Background(), repo, "main")
	if err != nil {
		t.Fatalf("resolve default branch: %v", err)
	}
	if target.BaseRef != "refs/remotes/origin/main" {
		t.Fatalf("base ref = %q, want refs/remotes/origin/main", target.BaseRef)
	}
	if err := ws.FetchDefaultBranch(context.Background(), repo, target); err != nil {
		t.Fatalf("fetch default branch: %v", err)
	}
	if got := gitOutput(t, git, repo, "rev-parse", "refs/remotes/origin/main"); got != freshMain {
		t.Fatalf("origin/main = %s, want refreshed %s", got, freshMain)
	}
}

func TestResolveDefaultBranchKeepsSlashBranchOnOriginWithoutMatchingRemote(t *testing.T) {
	git := requireGit(t)
	tmp := t.TempDir()
	repo := setupOriginClone(t, git, tmp)
	ws, err := New(Options{Binary: git, ManagedRoot: filepath.Join(tmp, "managed"), RepoResolver: StaticRepoResolver{"proj": repo}})
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	target, err := ws.ResolveDefaultBranch(context.Background(), repo, "release/2026")
	if err != nil {
		t.Fatalf("resolve slash branch: %v", err)
	}
	want := ports.WorkspaceDefaultBranch{Remote: "origin", Branch: "release/2026", BaseRef: "refs/remotes/origin/release/2026"}
	if target != want {
		t.Fatalf("target = %#v, want %#v", target, want)
	}
}

func TestResolveDefaultBranchFallsBackToLocalHeadWhenExplicitBranchHasNoRemoteTrackingRef(t *testing.T) {
	git := requireGit(t)
	tmp := t.TempDir()
	repo := filepath.Join(tmp, "repo")
	run(t, git, "init", "-b", "main", repo)
	runGit(t, git, repo, "config", "user.email", "ao@example.com")
	runGit(t, git, repo, "config", "user.name", "Ao Agents")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("seed\n"), 0o644); err != nil {
		t.Fatalf("write seed: %v", err)
	}
	runGit(t, git, repo, "add", "README.md")
	runGit(t, git, repo, "commit", "-m", "seed")
	runGit(t, git, repo, "remote", "add", "origin", "https://github.com/example/missing.git")

	ws, err := New(Options{Binary: git, ManagedRoot: filepath.Join(tmp, "managed"), RepoResolver: StaticRepoResolver{"proj": repo}})
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	target, err := ws.ResolveDefaultBranch(context.Background(), repo, "main")
	if err != nil {
		t.Fatalf("resolve default branch: %v", err)
	}
	want := ports.WorkspaceDefaultBranch{Remote: "origin", Branch: "main", BaseRef: "refs/heads/main"}
	if target != want {
		t.Fatalf("target = %#v, want %#v", target, want)
	}
	if err := ws.FetchDefaultBranch(context.Background(), repo, target); err != nil {
		t.Fatalf("fetch default branch local fallback: %v", err)
	}
}

func TestWorkspaceIntegrationRefreshesInferredChildBaseBeforeMaterialization(t *testing.T) {
	git := requireGit(t)
	tmp := t.TempDir()
	rootRepo := setupOriginClone(t, git, filepath.Join(tmp, "root"))
	childRepo := setupOriginCloneOnBranch(t, git, filepath.Join(tmp, "child"), "dev")
	originURL := gitOutput(t, git, childRepo, "remote", "get-url", "origin")
	updater := filepath.Join(tmp, "child-updater")
	run(t, git, "clone", originURL, updater)
	runGit(t, git, updater, "config", "user.email", "ao@example.com")
	runGit(t, git, updater, "config", "user.name", "Ao Agents")
	runGit(t, git, updater, "checkout", "-B", "dev", "origin/dev")
	if err := os.WriteFile(filepath.Join(updater, "fresh-child.txt"), []byte("fresh\n"), 0o644); err != nil {
		t.Fatalf("write fresh child file: %v", err)
	}
	runGit(t, git, updater, "add", "fresh-child.txt")
	runGit(t, git, updater, "commit", "-m", "advance child dev")
	freshChild := gitOutput(t, git, updater, "rev-parse", "HEAD")
	runGit(t, git, updater, "push", "origin", "HEAD:dev")

	ws, err := New(Options{Binary: git, ManagedRoot: filepath.Join(tmp, "managed"), RepoResolver: StaticRepoResolver{"proj": rootRepo}})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	childTarget, err := ws.ResolveDefaultBranch(context.Background(), childRepo, "")
	if err != nil {
		t.Fatalf("resolve inferred child default: %v", err)
	}
	if got, want := childTarget.BaseRef, "refs/remotes/origin/dev"; got != want {
		t.Fatalf("inferred child base ref = %q, want %q", got, want)
	}
	if err := ws.FetchDefaultBranch(context.Background(), childRepo, childTarget); err != nil {
		t.Fatalf("refresh inferred child default: %v", err)
	}

	info, err := ws.CreateWorkspaceProject(context.Background(), ports.WorkspaceProjectConfig{
		ProjectID:    "proj",
		SessionID:    "sess-fresh-child",
		Kind:         "worker",
		Branch:       "ao/proj-fresh-child",
		RootRepoPath: rootRepo,
		BaseBranch:   "main",
		Repos: []ports.WorkspaceProjectRepoConfig{{
			Name:         "api",
			RelativePath: "services/api",
			RepoPath:     childRepo,
			BaseRef:      childTarget.BaseRef,
		}},
	})
	if err != nil {
		t.Fatalf("create workspace project: %v", err)
	}
	childWorktree := info.Worktrees[1]
	if got := gitOutput(t, git, childWorktree.Path, "rev-parse", "HEAD"); got != freshChild {
		t.Fatalf("child worktree HEAD = %s, want refreshed %s", got, freshChild)
	}
	if childWorktree.BaseSHA != freshChild {
		t.Fatalf("child BaseSHA = %s, want refreshed %s", childWorktree.BaseSHA, freshChild)
	}
}

func TestWorkspaceIntegrationCanonicalBaseRefAvoidsRemoteNameCollision(t *testing.T) {
	git := requireGit(t)
	tmp := t.TempDir()
	repo := setupOriginClone(t, git, filepath.Join(tmp, "repo"))
	originURL := gitOutput(t, git, repo, "remote", "get-url", "origin")

	collisionUpdater := filepath.Join(tmp, "collision-updater")
	run(t, git, "clone", originURL, collisionUpdater)
	runGit(t, git, collisionUpdater, "config", "user.email", "ao@example.com")
	runGit(t, git, collisionUpdater, "config", "user.name", "Ao Agents")
	runGit(t, git, collisionUpdater, "checkout", "-b", "upstream/main", "origin/main")
	if err := os.WriteFile(filepath.Join(collisionUpdater, "collision.txt"), []byte("origin collision\n"), 0o644); err != nil {
		t.Fatalf("write collision file: %v", err)
	}
	runGit(t, git, collisionUpdater, "add", "collision.txt")
	runGit(t, git, collisionUpdater, "commit", "-m", "origin collision branch")
	collisionSHA := gitOutput(t, git, collisionUpdater, "rev-parse", "HEAD")
	runGit(t, git, collisionUpdater, "push", "origin", "HEAD:refs/heads/upstream/main")
	runGit(t, git, repo, "fetch", "origin", "+refs/heads/upstream/main:refs/remotes/origin/upstream/main")

	upstreamBare := filepath.Join(tmp, "upstream.git")
	run(t, git, "init", "--bare", upstreamBare)
	upstreamUpdater := filepath.Join(tmp, "upstream-updater")
	run(t, git, "clone", originURL, upstreamUpdater)
	runGit(t, git, upstreamUpdater, "config", "user.email", "ao@example.com")
	runGit(t, git, upstreamUpdater, "config", "user.name", "Ao Agents")
	runGit(t, git, upstreamUpdater, "checkout", "-B", "main", "origin/main")
	if err := os.WriteFile(filepath.Join(upstreamUpdater, "upstream.txt"), []byte("configured upstream\n"), 0o644); err != nil {
		t.Fatalf("write upstream file: %v", err)
	}
	runGit(t, git, upstreamUpdater, "add", "upstream.txt")
	runGit(t, git, upstreamUpdater, "commit", "-m", "configured upstream main")
	upstreamSHA := gitOutput(t, git, upstreamUpdater, "rev-parse", "HEAD")
	runGit(t, git, upstreamUpdater, "remote", "add", "upstream-target", upstreamBare)
	runGit(t, git, upstreamUpdater, "push", "upstream-target", "HEAD:main")
	runGit(t, git, repo, "remote", "add", "upstream", upstreamBare)

	ws, err := New(Options{Binary: git, ManagedRoot: filepath.Join(tmp, "managed"), RepoResolver: StaticRepoResolver{"proj": repo}})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	target, err := ws.ResolveDefaultBranch(context.Background(), repo, "upstream/main")
	if err != nil {
		t.Fatalf("resolve qualified default: %v", err)
	}
	if got, want := target.BaseRef, "refs/remotes/upstream/main"; got != want {
		t.Fatalf("qualified base ref = %q, want %q", got, want)
	}
	if err := ws.FetchDefaultBranch(context.Background(), repo, target); err != nil {
		t.Fatalf("fetch qualified default: %v", err)
	}
	info, err := ws.Create(context.Background(), ports.WorkspaceConfig{
		ProjectID:  "proj",
		SessionID:  "sess-upstream",
		Branch:     "ao/proj-upstream",
		BaseBranch: "upstream/main",
		BaseRef:    target.BaseRef,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if got := gitOutput(t, git, info.Path, "rev-parse", "HEAD"); got != upstreamSHA {
		t.Fatalf("worktree HEAD = %s, want configured upstream %s (origin collision was %s)", got, upstreamSHA, collisionSHA)
	}
}

func requireGit(t *testing.T) string {
	t.Helper()
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not found")
	}
	return git
}

func setupOriginClone(t *testing.T, git, tmp string) string {
	t.Helper()
	origin := filepath.Join(tmp, "origin.git")
	seed := filepath.Join(tmp, "seed")
	repo := filepath.Join(tmp, "repo")
	run(t, git, "init", "--bare", origin)
	run(t, git, "init", seed)
	runGit(t, git, seed, "config", "core.autocrlf", "false")
	runGit(t, git, seed, "config", "user.email", "ao@example.com")
	runGit(t, git, seed, "config", "user.name", "Ao Agents")
	if err := os.WriteFile(filepath.Join(seed, "README.md"), []byte("seed\n"), 0o644); err != nil {
		t.Fatalf("write seed: %v", err)
	}
	runGit(t, git, seed, "add", "README.md")
	runGit(t, git, seed, "commit", "-m", "seed")
	runGit(t, git, seed, "branch", "-M", "main")
	runGit(t, git, seed, "remote", "add", "origin", origin)
	runGit(t, git, seed, "push", "-u", "origin", "main")
	runGit(t, git, origin, "symbolic-ref", "HEAD", "refs/heads/main")
	run(t, git, "clone", origin, repo)
	runGit(t, git, repo, "config", "core.autocrlf", "false")
	// A clone does not copy the seed's local identity, and CI runners have no
	// global git identity to fall back on, so commit/commit-tree in this repo's
	// worktrees would fail with "empty ident name". Set it on the clone; worktrees
	// inherit the common dir config.
	runGit(t, git, repo, "config", "user.email", "ao@example.com")
	runGit(t, git, repo, "config", "user.name", "Ao Agents")
	runGit(t, git, repo, "checkout", "main")
	runGit(t, git, repo, "reset", "--hard", "HEAD")
	return repo
}

func setupOriginCloneOnBranch(t *testing.T, git, tmp, branch string) string {
	t.Helper()
	repo := setupOriginClone(t, git, tmp)
	runGit(t, git, repo, "branch", "-m", "main", branch)
	runGit(t, git, repo, "push", "-u", "origin", branch)
	origin := gitOutput(t, git, repo, "remote", "get-url", "origin")
	runGit(t, git, origin, "symbolic-ref", "HEAD", "refs/heads/"+branch)
	runGit(t, git, repo, "remote", "set-head", "origin", branch)
	return repo
}

func pushRemoteBranchCommit(t *testing.T, git, repo, defaultBranch, branch string) (defaultSHA, branchSHA string) {
	t.Helper()
	defaultSHA = gitOutput(t, git, repo, "rev-parse", "refs/heads/"+defaultBranch)
	runGit(t, git, repo, "switch", "-c", branch)
	if err := os.WriteFile(filepath.Join(repo, "SESSION.md"), []byte("remote session work\n"), 0o644); err != nil {
		t.Fatalf("write session file: %v", err)
	}
	runGit(t, git, repo, "add", "SESSION.md")
	runGit(t, git, repo, "commit", "-m", "session work")
	runGit(t, git, repo, "push", "-u", "origin", branch)
	branchSHA = gitOutput(t, git, repo, "rev-parse", "HEAD")
	runGit(t, git, repo, "switch", defaultBranch)
	runGit(t, git, repo, "branch", "-D", branch)
	return defaultSHA, branchSHA
}

func gitOutput(t *testing.T, git, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command(git, append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s -C %s %s: %v\n%s", git, dir, strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func runGit(t *testing.T, git, dir string, args ...string) {
	t.Helper()
	run(t, git, append([]string{"-C", dir}, args...)...)
}

func run(t *testing.T, binary string, args ...string) {
	t.Helper()
	cmd := exec.Command(binary, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", binary, strings.Join(args, " "), err, out)
	}
}
