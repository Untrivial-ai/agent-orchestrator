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

// TestWorkspaceIntegrationStashApplyRoundTrip is the primary correctness test
// for the save-on-close / restore-on-open lifecycle:
//
//  1. Create a worktree with a tracked-file edit, a new non-ignored file,
//     and a file covered by .gitignore.
//  2. StashUncommitted: assert the returned ref is non-empty.
//  3. ForceDestroy: remove the worktree unconditionally.
//  4. Re-add the worktree via Restore (simulating the re-open path).
//  5. ApplyPreserved: replay the captured state.
//  6. Assert that the tracked edit and the new non-ignored file reappear,
//     and the .gitignore-matched file does NOT reappear.
func TestWorkspaceIntegrationStashApplyRoundTrip(t *testing.T) {
	git := requireGit(t)
	tmp := t.TempDir()
	repo := setupOriginClone(t, git, tmp)
	root := filepath.Join(tmp, "managed")
	ws, err := New(Options{Binary: git, ManagedRoot: root, RepoResolver: StaticRepoResolver{"proj": repo}})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	ctx := context.Background()
	cfg := ports.WorkspaceConfig{ProjectID: "proj", SessionID: "sess-preserve", Branch: "feature/preserve"}

	info, err := ws.Create(ctx, cfg)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Stage 1: create a .gitignore that covers a secret file.
	if err := os.WriteFile(filepath.Join(info.Path, ".gitignore"), []byte("secret.txt\n"), 0o644); err != nil {
		t.Fatalf("write .gitignore: %v", err)
	}
	runGit(t, git, info.Path, "add", ".gitignore")
	runGit(t, git, info.Path, "commit", "-m", "add gitignore")

	// Stage 2: create uncommitted work:
	//   - tracked-file edit: modify README.md (already committed from seed)
	if err := os.WriteFile(filepath.Join(info.Path, "README.md"), []byte("edited by agent\n"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	//   - new non-ignored file: should be captured
	if err := os.WriteFile(filepath.Join(info.Path, "agent-work.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("write agent-work.go: %v", err)
	}
	//   - ignored file: must NOT be captured
	if err := os.WriteFile(filepath.Join(info.Path, "secret.txt"), []byte("super-secret\n"), 0o644); err != nil {
		t.Fatalf("write secret.txt: %v", err)
	}

	// StashUncommitted: must return a non-empty ref.
	ref, err := ws.StashUncommitted(ctx, info)
	if err != nil {
		t.Fatalf("StashUncommitted: %v", err)
	}
	if ref == "" {
		t.Fatal("StashUncommitted returned empty ref for dirty worktree")
	}
	if !strings.HasPrefix(ref, "refs/ao/preserved/") {
		t.Fatalf("ref = %q, want refs/ao/preserved/... prefix", ref)
	}

	// ForceDestroy: simulate session close.
	if err := ws.ForceDestroy(ctx, info); err != nil {
		t.Fatalf("ForceDestroy: %v", err)
	}
	if _, err := os.Stat(info.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("worktree path still exists after ForceDestroy")
	}

	// Restore: simulate re-open / re-attach.
	restored, err := ws.Restore(ctx, cfg)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if restored.Path != info.Path {
		t.Fatalf("restored path = %q, want %q", restored.Path, info.Path)
	}

	// ApplyPreserved: replay the captured state.
	if err := ws.ApplyPreserved(ctx, restored, ref); err != nil {
		t.Fatalf("ApplyPreserved: %v", err)
	}

	// Tracked edit must reappear.
	readmeBytes, err := os.ReadFile(filepath.Join(restored.Path, "README.md"))
	if err != nil {
		t.Fatalf("read README after apply: %v", err)
	}
	if string(readmeBytes) != "edited by agent\n" {
		t.Fatalf("README content = %q, want %q", string(readmeBytes), "edited by agent\n")
	}

	// New non-ignored file must reappear.
	if _, err := os.Stat(filepath.Join(restored.Path, "agent-work.go")); err != nil {
		t.Fatalf("agent-work.go missing after apply: %v", err)
	}

	// Ignored file must NOT reappear.
	if _, err := os.Stat(filepath.Join(restored.Path, "secret.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("secret.txt exists after apply but must not (it was .gitignore-d)")
	}

	// After a successful apply the ref must be deleted.
	checkRefArgs := revParseVerifyArgs(repo, ref)
	if out, err := ws.run(ctx, ws.binary, checkRefArgs...); err == nil {
		t.Fatalf("preserve ref %q still exists after successful ApplyPreserved (points to %s)", ref, strings.TrimSpace(string(out)))
	}
}

// TestWorkspaceIntegrationApplyPreservedConflict verifies the spec for a
// conflicting apply (plan edge case 5):
//
//  1. Set up a repo where the base HEAD has a tracked file with content "A".
//  2. StashUncommitted after editing the file to "B" (preserve commit: B over A).
//  3. After ForceDestroy and Restore, diverge the same file to content "C"
//     (simulating a base-moved or independently-edited state).
//  4. ApplyPreserved must:
//     (a) return an error that satisfies errors.Is(err, ErrPreservedConflict),
//     (b) leave the preserve ref intact (NOT delete it),
//     (c) leave textual conflict markers in the conflicting file.
func TestWorkspaceIntegrationApplyPreservedConflict(t *testing.T) {
	git := requireGit(t)
	tmp := t.TempDir()
	repo := setupOriginClone(t, git, tmp)
	root := filepath.Join(tmp, "managed")
	ws, err := New(Options{Binary: git, ManagedRoot: root, RepoResolver: StaticRepoResolver{"proj": repo}})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	ctx := context.Background()
	cfg := ports.WorkspaceConfig{ProjectID: "proj", SessionID: "sess-conflict", Branch: "feature/conflict-test"}

	info, err := ws.Create(ctx, cfg)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Write base content "A" into a tracked file and commit it so it is the
	// HEAD tree that StashUncommitted will use as the parent.
	conflictFile := filepath.Join(info.Path, "shared.txt")
	if err := os.WriteFile(conflictFile, []byte("A\n"), 0o644); err != nil {
		t.Fatalf("write base A: %v", err)
	}
	runGit(t, git, info.Path, "add", "shared.txt")
	runGit(t, git, info.Path, "commit", "-m", "base: A")

	// Edit to "B" without committing: this is what the agent had in flight.
	if err := os.WriteFile(conflictFile, []byte("B\n"), 0o644); err != nil {
		t.Fatalf("write B: %v", err)
	}

	// Preserve: StashUncommitted captures B-over-A into a ref.
	ref, err := ws.StashUncommitted(ctx, info)
	if err != nil {
		t.Fatalf("StashUncommitted: %v", err)
	}
	if ref == "" {
		t.Fatal("StashUncommitted returned empty ref for dirty worktree")
	}

	// Simulate session close.
	if err := ws.ForceDestroy(ctx, info); err != nil {
		t.Fatalf("ForceDestroy: %v", err)
	}

	// Restore: re-add the worktree (re-open path).
	restored, err := ws.Restore(ctx, cfg)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}

	// Diverge the same file to content "C" in the restored worktree so that
	// cherry-pick --no-commit will produce a real three-way conflict (A -> B
	// from preserve vs A -> C in the current tree).
	conflictFileRestored := filepath.Join(restored.Path, "shared.txt")
	if err := os.WriteFile(conflictFileRestored, []byte("C\n"), 0o644); err != nil {
		t.Fatalf("write C: %v", err)
	}
	// Stage the diverging edit so it is in the index; cherry-pick merges against
	// the index, not just the working tree.
	runGit(t, git, restored.Path, "add", "shared.txt")

	// ApplyPreserved must detect the conflict and return ErrPreservedConflict.
	applyErr := ws.ApplyPreserved(ctx, restored, ref)
	if applyErr == nil {
		t.Fatal("ApplyPreserved returned nil, want ErrPreservedConflict")
	}
	if !errors.Is(applyErr, ErrPreservedConflict) {
		t.Fatalf("ApplyPreserved error = %v, want errors.Is(..., ErrPreservedConflict)", applyErr)
	}

	// (b) The preserve ref must still exist.
	checkRefArgs := revParseVerifyArgs(repo, ref)
	if _, err := ws.run(ctx, ws.binary, checkRefArgs...); err != nil {
		t.Fatalf("preserve ref %q was deleted after a conflicting apply, must be kept: %v", ref, err)
	}

	// (c) The conflicting file must contain textual conflict markers.
	contents, err := os.ReadFile(conflictFileRestored)
	if err != nil {
		t.Fatalf("read conflicting file: %v", err)
	}
	if !strings.Contains(string(contents), "<<<<<<<") {
		t.Fatalf("conflicting file has no conflict markers after ApplyPreserved conflict; content:\n%s", string(contents))
	}
}

// TestWorkspaceIntegrationStashApplyPreservesTrackedIgnoredEdit is a
// regression test for the data-loss bug where an uncommitted edit to a file
// that is tracked in HEAD but ALSO matches a .gitignore pattern (a real
// pattern: commit a config once, then gitignore it so local edits never get
// pushed) was silently dropped by StashUncommitted and then deleted on
// restore by ApplyPreserved.
//
// Root cause: StashUncommitted stages the preserve tree through a temp index
// file that starts absent/empty. git's "an already-tracked path bypasses
// .gitignore" rule keys off what is IN THE INDEX, not what is in HEAD, so
// against an empty index `git add -A` cannot tell a tracked-but-ignored file
// apart from a genuinely untracked-and-ignored one, and silently skips both
// (no -f passed). The preserve tree then omits the tracked file; since the
// preserve commit's parent is HEAD (which still has the file), the diff reads
// as a deletion, and ApplyPreserved's cherry-pick faithfully replays that
// deletion on restore.
//
// This test also covers the negative case in the same run: a genuinely
// untracked file that matches .gitignore (never committed) must still be
// excluded from the preserve/restore round trip. That is the main regression
// risk of seeding the temp index from HEAD — it must not start capturing real
// ignored junk (build output, node_modules, etc).
func TestWorkspaceIntegrationStashApplyPreservesTrackedIgnoredEdit(t *testing.T) {
	git := requireGit(t)
	tmp := t.TempDir()
	repo := setupOriginClone(t, git, tmp)
	root := filepath.Join(tmp, "managed")
	ws, err := New(Options{Binary: git, ManagedRoot: root, RepoResolver: StaticRepoResolver{"proj": repo}})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	ctx := context.Background()
	cfg := ports.WorkspaceConfig{ProjectID: "proj", SessionID: "sess-tracked-ignored", Branch: "feature/tracked-ignored"}

	info, err := ws.Create(ctx, cfg)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Commit config.yaml (tracked in HEAD), THEN add it to .gitignore and
	// commit that too, so it stays tracked while also matching an ignore
	// pattern going forward — the exact real-world shape the issue describes.
	configPath := filepath.Join(info.Path, "config.yaml")
	if err := os.WriteFile(configPath, []byte("original\n"), 0o644); err != nil {
		t.Fatalf("write config.yaml: %v", err)
	}
	runGit(t, git, info.Path, "add", "config.yaml")
	runGit(t, git, info.Path, "commit", "-m", "add config.yaml")

	if err := os.WriteFile(filepath.Join(info.Path, ".gitignore"), []byte("config.yaml\nbuild/\n"), 0o644); err != nil {
		t.Fatalf("write .gitignore: %v", err)
	}
	runGit(t, git, info.Path, "add", ".gitignore")
	runGit(t, git, info.Path, "commit", "-m", "gitignore config.yaml and build/")

	// Uncommitted edit to the tracked-but-now-ignored file: this is the edit
	// that must survive preserve/restore.
	if err := os.WriteFile(configPath, []byte("edited by agent\n"), 0o644); err != nil {
		t.Fatalf("edit config.yaml: %v", err)
	}

	// Negative case: a genuinely untracked file that matches .gitignore (never
	// committed). It must be excluded from the round trip, same as before
	// this fix — seeding the temp index from HEAD must not start capturing
	// real ignored junk.
	buildDir := filepath.Join(info.Path, "build")
	if err := os.MkdirAll(buildDir, 0o755); err != nil {
		t.Fatalf("mkdir build: %v", err)
	}
	if err := os.WriteFile(filepath.Join(buildDir, "output.log"), []byte("junk\n"), 0o644); err != nil {
		t.Fatalf("write build/output.log: %v", err)
	}

	// StashUncommitted: must return a non-empty ref (there is real work to
	// preserve: the config.yaml edit).
	ref, err := ws.StashUncommitted(ctx, info)
	if err != nil {
		t.Fatalf("StashUncommitted: %v", err)
	}
	if ref == "" {
		t.Fatal("StashUncommitted returned empty ref, want a preserve ref for the tracked-ignored edit")
	}

	// ForceDestroy: simulate session close.
	if err := ws.ForceDestroy(ctx, info); err != nil {
		t.Fatalf("ForceDestroy: %v", err)
	}
	if _, err := os.Stat(info.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("worktree path still exists after ForceDestroy")
	}

	// Restore: simulate re-open / re-attach.
	restored, err := ws.Restore(ctx, cfg)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}

	// ApplyPreserved: replay the captured state.
	if err := ws.ApplyPreserved(ctx, restored, ref); err != nil {
		t.Fatalf("ApplyPreserved: %v", err)
	}

	// The tracked-but-ignored file must still exist AND carry the edited
	// content — not the original, and not deleted. This is the core assertion
	// for the data-loss bug: before the fix, config.yaml did not exist here.
	restoredConfig := filepath.Join(restored.Path, "config.yaml")
	configBytes, err := os.ReadFile(restoredConfig)
	if err != nil {
		t.Fatalf("config.yaml missing after apply (data loss): %v", err)
	}
	if string(configBytes) != "edited by agent\n" {
		t.Fatalf("config.yaml content = %q, want %q (edit lost or reverted to original)", string(configBytes), "edited by agent\n")
	}

	// Negative case: the genuinely untracked, ignored file must NOT reappear.
	if _, err := os.Stat(filepath.Join(restored.Path, "build", "output.log")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("build/output.log exists after apply but must not (it was untracked and .gitignore-d)")
	}
}

// TestWorkspaceIntegrationStashCleanWorktree proves that StashUncommitted on a
// clean worktree returns an empty ref and no error (nothing to preserve).
func TestWorkspaceIntegrationStashCleanWorktree(t *testing.T) {
	git := requireGit(t)
	tmp := t.TempDir()
	repo := setupOriginClone(t, git, tmp)
	root := filepath.Join(tmp, "managed")
	ws, err := New(Options{Binary: git, ManagedRoot: root, RepoResolver: StaticRepoResolver{"proj": repo}})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	ctx := context.Background()
	cfg := ports.WorkspaceConfig{ProjectID: "proj", SessionID: "sess-clean", Branch: "feature/clean-stash"}

	info, err := ws.Create(ctx, cfg)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	ref, err := ws.StashUncommitted(ctx, info)
	if err != nil {
		t.Fatalf("StashUncommitted on clean worktree: %v", err)
	}
	if ref != "" {
		t.Fatalf("StashUncommitted on clean worktree returned non-empty ref %q, want empty", ref)
	}

	// Cleanup.
	if err := ws.Destroy(ctx, info); err != nil {
		t.Fatalf("destroy clean worktree: %v", err)
	}
}
