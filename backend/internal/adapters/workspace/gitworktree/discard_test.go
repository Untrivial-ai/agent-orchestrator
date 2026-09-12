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

// A worktree's ignored build output (node_modules and friends) is the bulk of
// what teardown has to unlink, and unlinking it inline is what pushed a kill
// past the daemon's request timeout. Destroy must return with the path already
// gone and the registration already pruned, leaving only the unlinking behind.
func TestDestroyDiscardsWorktreeWithoutUnlinkingInline(t *testing.T) {
	git := requireGit(t)
	tmp := t.TempDir()
	repo := setupOriginClone(t, git, tmp)
	ws, err := New(Options{Binary: git, ManagedRoot: filepath.Join(tmp, "managed"), RepoResolver: StaticRepoResolver{"proj": repo}})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	ctx := context.Background()
	info, err := ws.Create(ctx, ports.WorkspaceConfig{ProjectID: "proj", SessionID: "sess", Branch: "feature/one"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// Ignored, like a real session's dependency tree: git would delete it as
	// part of `worktree remove`, which is the cost being moved off the request.
	if err := ws.AddExclude(ctx, info, "deps/"); err != nil {
		t.Fatalf("add exclude: %v", err)
	}
	writeIgnoredTree(t, info.Path)

	inner := ws.run
	var calls []string
	ws.run = func(ctx context.Context, binary string, args ...string) ([]byte, error) {
		calls = append(calls, strings.Join(args, " "))
		return inner(ctx, binary, args...)
	}
	if err := ws.Destroy(ctx, info); err != nil {
		t.Fatalf("destroy: %v", err)
	}
	for _, call := range calls {
		if strings.Contains(call, "worktree remove") {
			t.Fatalf("Destroy shelled out to %q instead of discarding the directory", call)
		}
	}
	if _, err := os.Stat(info.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("worktree path after destroy = %v, want not exist", err)
	}
	records, err := ws.listRecords(ctx, repo)
	if err != nil {
		t.Fatalf("list records: %v", err)
	}
	if _, ok := findWorktree(records, info.Path); ok {
		t.Fatalf("worktree still registered after destroy")
	}

	ws.waitForDiscards()
	entries, err := os.ReadDir(ws.discardedRoot())
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read discard root: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("discard root still holds %d entries after background removal", len(entries))
	}
}

// The discard path must not weaken the refusal that protects uncommitted agent
// work: a dirty worktree stays on disk, registered, and reported as dirty.
func TestDestroyDiscardStillRefusesDirtyWorktree(t *testing.T) {
	git := requireGit(t)
	tmp := t.TempDir()
	repo := setupOriginClone(t, git, tmp)
	ws, err := New(Options{Binary: git, ManagedRoot: filepath.Join(tmp, "managed"), RepoResolver: StaticRepoResolver{"proj": repo}})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	ctx := context.Background()
	info, err := ws.Create(ctx, ports.WorkspaceConfig{ProjectID: "proj", SessionID: "sess", Branch: "feature/one"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := os.WriteFile(filepath.Join(info.Path, "in-progress.txt"), []byte("agent work"), 0o600); err != nil {
		t.Fatalf("seed dirty file: %v", err)
	}

	err = ws.Destroy(ctx, info)
	if !errors.Is(err, ports.ErrWorkspaceDirty) {
		t.Fatalf("destroy error = %v, want ports.ErrWorkspaceDirty", err)
	}
	if _, statErr := os.Stat(filepath.Join(info.Path, "in-progress.txt")); statErr != nil {
		t.Fatalf("dirty worktree was not preserved: %v", statErr)
	}
	records, err := ws.listRecords(ctx, repo)
	if err != nil {
		t.Fatalf("list records: %v", err)
	}
	if _, ok := findWorktree(records, info.Path); !ok {
		t.Fatalf("dirty worktree lost its registration")
	}
}

// ForceDestroy has no refusal to honour, so it discards unconditionally, but
// it still owes the caller a path that is gone and a registration that is
// pruned by the time it returns.
func TestForceDestroyDiscardsWorktree(t *testing.T) {
	git := requireGit(t)
	tmp := t.TempDir()
	repo := setupOriginClone(t, git, tmp)
	ws, err := New(Options{Binary: git, ManagedRoot: filepath.Join(tmp, "managed"), RepoResolver: StaticRepoResolver{"proj": repo}})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	ctx := context.Background()
	info, err := ws.Create(ctx, ports.WorkspaceConfig{ProjectID: "proj", SessionID: "sess", Branch: "feature/one"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := os.WriteFile(filepath.Join(info.Path, "in-progress.txt"), []byte("agent work"), 0o600); err != nil {
		t.Fatalf("seed dirty file: %v", err)
	}
	// Recording the git calls is what proves the fast path ran: a ForceDestroy
	// that fell through to `git worktree remove --force` would still leave the
	// path gone and pass every assertion below.
	inner := ws.run
	var calls []string
	ws.run = func(ctx context.Context, binary string, args ...string) ([]byte, error) {
		calls = append(calls, strings.Join(args, " "))
		return inner(ctx, binary, args...)
	}
	if err := ws.ForceDestroy(ctx, info); err != nil {
		t.Fatalf("force destroy: %v", err)
	}
	for _, call := range calls {
		if strings.Contains(call, "worktree remove") {
			t.Fatalf("ForceDestroy shelled out to %q instead of discarding the directory", call)
		}
	}
	if _, err := os.Stat(info.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("worktree path after force destroy = %v, want not exist", err)
	}
	records, err := ws.listRecords(ctx, repo)
	if err != nil {
		t.Fatalf("list records: %v", err)
	}
	if _, ok := findWorktree(records, info.Path); ok {
		t.Fatalf("worktree still registered after force destroy")
	}
	ws.waitForDiscards()
}

// A daemon that dies mid-removal leaves directories in the discard root that
// nothing else ever revisits. Construction is the one moment the root is known
// to be ours, so that is where the leftovers get collected.
func TestNewSweepsLeftoverDiscards(t *testing.T) {
	tmp := t.TempDir()
	root := filepath.Join(tmp, "managed")
	leftover := filepath.Join(root, discardedDirName, "sess-123-0")
	if err := os.MkdirAll(leftover, 0o755); err != nil {
		t.Fatalf("seed leftover: %v", err)
	}
	if err := os.WriteFile(filepath.Join(leftover, "stale.txt"), []byte("x"), 0o600); err != nil {
		t.Fatalf("seed leftover file: %v", err)
	}
	ws, err := New(Options{ManagedRoot: root, RepoResolver: StaticRepoResolver{"proj": tmp}})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	ws.waitForDiscards()
	if _, err := os.Stat(leftover); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("leftover discard after sweep = %v, want not exist", err)
	}
}

func writeIgnoredTree(t *testing.T, worktree string) {
	t.Helper()
	deps := filepath.Join(worktree, "deps", "pkg")
	if err := os.MkdirAll(deps, 0o755); err != nil {
		t.Fatalf("make deps: %v", err)
	}
	for _, name := range []string{"a.js", "b.js", "c.js"} {
		if err := os.WriteFile(filepath.Join(deps, name), []byte("x"), 0o600); err != nil {
			t.Fatalf("write dep %s: %v", name, err)
		}
	}
}

// Once the registration is pruned, restoring the directory would be a worse
// outcome than losing it: the next teardown would see an unregistered stray and
// delete it with no dirty check at all. The move only happens after a
// conclusive clean status, so letting it go costs nothing that is not already
// committed on the session branch.
func TestDiscardKeepsGoingWhenThePostPruneProbeFails(t *testing.T) {
	root := t.TempDir()
	repo := t.TempDir()
	ws, err := New(Options{ManagedRoot: root, RepoResolver: StaticRepoResolver{"proj": repo}})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	path := filepath.Join(ws.managedRoot, "proj", "sess")
	if err := mkdirFile(path, "committed.txt"); err != nil {
		t.Fatalf("seed path: %v", err)
	}
	listCalls := 0
	ws.run = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		switch {
		case strings.Contains(joined, "worktree list --porcelain"):
			listCalls++
			if listCalls > 1 {
				return nil, errors.New("git blew up after the prune")
			}
			return []byte("worktree " + path + "\nbranch refs/heads/feature/one\n"), nil
		case strings.Contains(joined, "status --porcelain"):
			return nil, nil
		default:
			return nil, nil
		}
	}

	if err := ws.Destroy(context.Background(), ports.WorkspaceInfo{Path: path, ProjectID: "proj", SessionID: "sess", Branch: "feature/one"}); err != nil {
		t.Fatalf("destroy: %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("de-registered directory was restored to %q (err = %v); a later teardown would delete it unchecked", path, err)
	}
	ws.waitForDiscards()
}

// A project directory the user deleted turns every `git -C <repo> ...` in
// teardown into an opaque exit 128, which surfaced as a permanent
// "Internal server error" on delete and left the session in the sidebar
// forever. Teardown reports it as a typed refusal instead, so the caller can
// preserve the directory and still terminate the session.
func TestDestroyReportsAMissingProjectRepoAsUnavailable(t *testing.T) {
	git := requireGit(t)
	tmp := t.TempDir()
	repo := setupOriginClone(t, git, tmp)
	ws, err := New(Options{Binary: git, ManagedRoot: filepath.Join(tmp, "managed"), RepoResolver: StaticRepoResolver{"proj": repo}})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	ctx := context.Background()
	info, err := ws.Create(ctx, ports.WorkspaceConfig{ProjectID: "proj", SessionID: "sess", Branch: "feature/one"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := os.RemoveAll(repo); err != nil {
		t.Fatalf("remove project repo: %v", err)
	}

	err = ws.Destroy(ctx, info)
	if !errors.Is(err, ports.ErrWorkspaceRepoUnavailable) {
		t.Fatalf("destroy error = %v, want ports.ErrWorkspaceRepoUnavailable", err)
	}
	if _, statErr := os.Stat(info.Path); statErr != nil {
		t.Fatalf("worktree must be left alone when git cannot be asked about it: %v", statErr)
	}
	if err := ws.ForceDestroy(ctx, info); !errors.Is(err, ports.ErrWorkspaceRepoUnavailable) {
		t.Fatalf("force destroy error = %v, want ports.ErrWorkspaceRepoUnavailable", err)
	}
}

// The move is not always possible. On Windows a PTY or agent child can still
// hold a handle on the worktree directory for a moment after the process is
// gone, and a rename against an open handle fails outright, so the fast path
// has to hand back to the git-driven teardown rather than give up. Simulated
// here by blocking the discard root with a file, which is the one failure mode
// that reproduces identically on every platform.
func TestDestroyFallsBackToGitWhenTheMoveIsImpossible(t *testing.T) {
	git := requireGit(t)
	tmp := t.TempDir()
	repo := setupOriginClone(t, git, tmp)
	ws, err := New(Options{Binary: git, ManagedRoot: filepath.Join(tmp, "managed"), RepoResolver: StaticRepoResolver{"proj": repo}})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	ctx := context.Background()
	info, err := ws.Create(ctx, ports.WorkspaceConfig{ProjectID: "proj", SessionID: "sess", Branch: "feature/one"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// A plain file where the discard root belongs: MkdirAll cannot create the
	// directory, so no rename is even attempted.
	if err := os.WriteFile(ws.discardedRoot(), []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("block discard root: %v", err)
	}

	inner := ws.run
	var calls []string
	ws.run = func(ctx context.Context, binary string, args ...string) ([]byte, error) {
		calls = append(calls, strings.Join(args, " "))
		return inner(ctx, binary, args...)
	}
	if err := ws.Destroy(ctx, info); err != nil {
		t.Fatalf("destroy: %v", err)
	}
	if _, err := os.Stat(info.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("worktree path after fallback destroy = %v, want not exist", err)
	}
	var sawRemove bool
	for _, call := range calls {
		if strings.Contains(call, "worktree remove") {
			sawRemove = true
		}
	}
	if !sawRemove {
		t.Fatal("fallback did not reach `git worktree remove`; an impossible move must not silently skip teardown")
	}
	records, err := ws.listRecords(ctx, repo)
	if err != nil {
		t.Fatalf("list records: %v", err)
	}
	if _, ok := findWorktree(records, info.Path); ok {
		t.Fatal("worktree still registered after fallback destroy")
	}
}

// Work that appears between the dirty probe and the delete must not be taken.
// The probe therefore runs against the directory after it has been moved aside:
// once the worktree path no longer resolves, nothing can add to what is about
// to be unlinked, so the state git reports is the state that gets deleted.
// Probing the live path first and deleting afterwards leaves exactly that gap.
func TestDirtyProbeRunsAgainstTheIsolatedDirectory(t *testing.T) {
	git := requireGit(t)
	tmp := t.TempDir()
	repo := setupOriginClone(t, git, tmp)
	ws, err := New(Options{Binary: git, ManagedRoot: filepath.Join(tmp, "managed"), RepoResolver: StaticRepoResolver{"proj": repo}})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	ctx := context.Background()
	info, err := ws.Create(ctx, ports.WorkspaceConfig{ProjectID: "proj", SessionID: "sess", Branch: "feature/one"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	inner := ws.run
	var statusPaths []string
	ws.run = func(ctx context.Context, binary string, args ...string) ([]byte, error) {
		if joined := strings.Join(args, " "); strings.Contains(joined, "status --porcelain") {
			for i, a := range args {
				if a == "-C" && i+1 < len(args) {
					statusPaths = append(statusPaths, args[i+1])
				}
			}
		}
		return inner(ctx, binary, args...)
	}

	if err := ws.Destroy(ctx, info); err != nil {
		t.Fatalf("destroy: %v", err)
	}
	if len(statusPaths) == 0 {
		t.Fatal("no dirty probe ran; teardown must not delete a worktree it never checked")
	}
	for _, probed := range statusPaths {
		if probed == info.Path {
			t.Fatalf("dirty probe ran against the live worktree %q; anything written after it would be deleted unchecked", probed)
		}
		if !strings.HasPrefix(probed, ws.discardedRoot()) {
			t.Fatalf("dirty probe ran against %q, want a path under the discard root %q", probed, ws.discardedRoot())
		}
	}
	ws.waitForDiscards()
}

// A failed prune must leave both halves intact. Deleting the directory anyway
// reports failure while destroying the worktree and stranding its registration,
// and that dangling entry blocks the path from being reused.
func TestForceDestroyKeepsTheWorktreeWhenPruneFails(t *testing.T) {
	root := t.TempDir()
	repo := t.TempDir()
	ws, err := New(Options{ManagedRoot: root, RepoResolver: StaticRepoResolver{"proj": repo}})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	path := filepath.Join(ws.managedRoot, "proj", "sess")
	if err := mkdirFile(path, "keep.txt"); err != nil {
		t.Fatalf("seed path: %v", err)
	}
	ws.run = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if strings.Contains(strings.Join(args, " "), "worktree prune") {
			return nil, errors.New("prune exploded")
		}
		return nil, nil
	}

	err = ws.ForceDestroy(context.Background(), ports.WorkspaceInfo{Path: path, ProjectID: "proj", SessionID: "sess", Branch: "feature/one"})
	if err == nil || !strings.Contains(err.Error(), "prune") {
		t.Fatalf("force destroy error = %v, want the prune failure", err)
	}
	ws.waitForDiscards()
	if _, statErr := os.Stat(filepath.Join(path, "keep.txt")); statErr != nil {
		t.Fatalf("worktree must survive a failed prune so the caller can retry: %v", statErr)
	}
}
