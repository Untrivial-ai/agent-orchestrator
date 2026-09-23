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

// TestArchiveDropsIgnoredCheckout is the CI check for the archive disk win.
// One worktree holds a tracked edit, a new file, and a 1 MiB gitignored
// payload. Archive removes the checkout. Restore comes back on the same
// commit without the payload. An unstaged edit on the same lines keeps both
// sides and does not create a commit.
func TestArchiveDropsIgnoredCheckout(t *testing.T) {
	git := requireGit(t)
	tmp := t.TempDir()
	repo := setupOriginClone(t, git, tmp)
	root := filepath.Join(tmp, "managed")
	ws, err := New(Options{Binary: git, ManagedRoot: root, RepoResolver: StaticRepoResolver{"proj": repo}})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	ctx := context.Background()
	cfg := ports.WorkspaceConfig{ProjectID: "proj", SessionID: "sess-ci", Branch: "feature/ci"}
	info, err := ws.Create(ctx, cfg)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := os.WriteFile(filepath.Join(info.Path, ".gitignore"), []byte("secret.bin\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, git, info.Path, "add", ".gitignore")
	runGit(t, git, info.Path, "commit", "-m", "ignore payload")
	head := gitOutput(t, git, info.Path, "rev-parse", "HEAD")

	if err := os.WriteFile(filepath.Join(info.Path, "README.md"), []byte("edited by agent\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(info.Path, "notes.txt"), []byte("new file\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	const payload = 1 << 20
	if err := writePayload(filepath.Join(info.Path, "secret.bin"), payload); err != nil {
		t.Fatal(err)
	}
	checkoutBefore := dirBytes(t, info.Path)
	gitBefore := dirBytes(t, filepath.Join(repo, ".git"))
	if checkoutBefore < payload {
		t.Fatalf("checkout = %d bytes, want at least the %d byte payload", checkoutBefore, payload)
	}

	ref, err := ws.StashUncommitted(ctx, info)
	if err != nil || ref == "" {
		t.Fatalf("stash ref=%q err=%v", ref, err)
	}
	if err := ws.ForceDestroy(ctx, info); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(info.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("checkout still on disk after archive")
	}
	if grew := dirBytes(t, filepath.Join(repo, ".git")) - gitBefore; grew > payload/4 {
		t.Fatalf("git grew by %d bytes after dropping a %d byte ignored file", grew, payload)
	}

	restored, err := ws.Restore(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got := gitOutput(t, git, restored.Path, "rev-parse", "HEAD"); got != head {
		t.Fatalf("HEAD = %s, want %s", got, head)
	}
	if status := gitOutput(t, git, restored.Path, "status", "--porcelain"); status != "" {
		t.Fatalf("restore status = %q, want clean", status)
	}
	readme, err := os.ReadFile(filepath.Join(restored.Path, "README.md"))
	if err != nil || string(readme) != "seed\n" {
		t.Fatalf("README after restore = %q, %v", readme, err)
	}
	if _, err := os.Stat(filepath.Join(restored.Path, "notes.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("restore brought the new file back")
	}
	if _, err := os.Stat(filepath.Join(restored.Path, "secret.bin")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("restore brought the ignored payload back")
	}
	if dirBytes(t, restored.Path) >= payload {
		t.Fatal("restored checkout is as large as the ignored payload")
	}

	if err := os.WriteFile(filepath.Join(restored.Path, "README.md"), []byte("typed after restore\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	applyErr := ws.ApplyPreserved(ctx, restored, ref)
	if !errors.Is(applyErr, ErrPreservedConflict) {
		t.Fatalf("apply = %v, want a conflict", applyErr)
	}
	if got := gitOutput(t, git, restored.Path, "rev-parse", "HEAD"); got != head {
		t.Fatalf("HEAD after apply = %s, want %s", got, head)
	}
	merged, err := os.ReadFile(filepath.Join(restored.Path, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(merged)
	if !strings.Contains(text, "edited by agent") || !strings.Contains(text, "typed after restore") || !strings.Contains(text, "<<<<<<<") {
		t.Fatalf("README = %q, want both edits and a conflict marker", text)
	}
	if _, err := os.Stat(filepath.Join(restored.Path, "secret.bin")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("conflict apply brought the ignored payload back")
	}
	if _, err := ws.run(ctx, ws.binary, revParseVerifyArgs(repo, ref)...); err != nil {
		t.Fatal("conflict apply deleted the snapshot")
	}
}

func TestStashUncommittedDoesNotReplaceAnExistingSnapshot(t *testing.T) {
	git := requireGit(t)
	tmp := t.TempDir()
	repo := setupOriginClone(t, git, tmp)
	ws, err := New(Options{Binary: git, ManagedRoot: filepath.Join(tmp, "managed"), RepoResolver: StaticRepoResolver{"proj": repo}})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	ctx := context.Background()
	cfg := ports.WorkspaceConfig{ProjectID: "proj", SessionID: "sess-rearchive", Branch: "feature/rearchive"}
	info, err := ws.Create(ctx, cfg)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := os.WriteFile(filepath.Join(info.Path, "README.md"), []byte("first saved edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ref, err := ws.StashUncommitted(ctx, info)
	if err != nil || ref == "" {
		t.Fatalf("first capture ref=%q err=%v", ref, err)
	}
	firstSHA := gitOutput(t, git, repo, "rev-parse", ref)
	if err := ws.ForceDestroy(ctx, info); err != nil {
		t.Fatal(err)
	}
	restored, err := ws.Restore(ctx, cfg)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if err := os.WriteFile(filepath.Join(restored.Path, "notes.txt"), []byte("second edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ws.StashUncommitted(ctx, restored); err == nil {
		t.Fatal("second capture succeeded over the existing snapshot")
	}
	if got := gitOutput(t, git, repo, "rev-parse", ref); got != firstSHA {
		t.Fatalf("preserved ref moved from %s to %s", firstSHA, got)
	}
	if got := gitOutput(t, git, repo, "show", ref+":README.md"); got != "first saved edit" {
		t.Fatalf("earlier saved edit = %q", got)
	}
	if _, err := ws.run(ctx, git, "-C", repo, "show", ref+":notes.txt"); err == nil {
		t.Fatal("second edit was written into the earlier snapshot")
	}
}

func TestApplyPreservedCapturesCollidingUntrackedLocalFile(t *testing.T) {
	git := requireGit(t)
	tmp := t.TempDir()
	repo := setupOriginClone(t, git, tmp)
	ws, err := New(Options{Binary: git, ManagedRoot: filepath.Join(tmp, "managed"), RepoResolver: StaticRepoResolver{"proj": repo}})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	ctx := context.Background()
	cfg := ports.WorkspaceConfig{ProjectID: "proj", SessionID: "sess-untracked-collision", Branch: "feature/untracked-collision"}
	info, err := ws.Create(ctx, cfg)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	localPath := filepath.Join(info.Path, "shared-new.txt")
	if err := os.WriteFile(localPath, []byte("saved side\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ref, err := ws.StashUncommitted(ctx, info)
	if err != nil || ref == "" {
		t.Fatalf("capture ref=%q err=%v", ref, err)
	}
	if err := ws.ForceDestroy(ctx, info); err != nil {
		t.Fatal(err)
	}
	restored, err := ws.Restore(ctx, cfg)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	localPath = filepath.Join(restored.Path, "shared-new.txt")
	if err := os.WriteFile(localPath, []byte("local side\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	head := gitOutput(t, git, restored.Path, "rev-parse", "HEAD")
	applyErr := ws.ApplyPreserved(ctx, restored, ref)
	if !errors.Is(applyErr, ErrPreservedConflict) {
		t.Fatalf("apply = %v, want ErrPreservedConflict", applyErr)
	}
	if got := gitOutput(t, git, restored.Path, "rev-parse", "HEAD"); got != head {
		t.Fatalf("HEAD after apply = %s, want %s", got, head)
	}
	got, err := os.ReadFile(localPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "local side") || !strings.Contains(string(got), "saved side") || !strings.Contains(string(got), "<<<<<<<") {
		t.Fatalf("merged untracked file = %q, want both sides and conflict markers", got)
	}
	if _, err := ws.run(ctx, git, revParseVerifyArgs(repo, ref)...); err != nil {
		t.Fatalf("snapshot ref deleted after conflict: %v", err)
	}
}

func TestApplyPreservedMergesNonOverlappingDirtyWorktree(t *testing.T) {
	git := requireGit(t)
	tmp := t.TempDir()
	repo := setupOriginClone(t, git, tmp)
	ws, err := New(Options{Binary: git, ManagedRoot: filepath.Join(tmp, "managed"), RepoResolver: StaticRepoResolver{"proj": repo}})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	ctx := context.Background()
	cfg := ports.WorkspaceConfig{ProjectID: "proj", SessionID: "sess-dirty-merge", Branch: "feature/dirty-merge"}
	info, err := ws.Create(ctx, cfg)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := os.WriteFile(filepath.Join(info.Path, "saved.txt"), []byte("saved edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ref, err := ws.StashUncommitted(ctx, info)
	if err != nil || ref == "" {
		t.Fatalf("capture ref=%q err=%v", ref, err)
	}
	if err := ws.ForceDestroy(ctx, info); err != nil {
		t.Fatal(err)
	}
	restored, err := ws.Restore(ctx, cfg)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if err := os.WriteFile(filepath.Join(restored.Path, "README.md"), []byte("independent local edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	head := gitOutput(t, git, restored.Path, "rev-parse", "HEAD")
	if err := ws.ApplyPreserved(ctx, restored, ref); err != nil {
		t.Fatalf("apply non-overlapping edits: %v", err)
	}
	if got := gitOutput(t, git, restored.Path, "rev-parse", "HEAD"); got != head {
		t.Fatalf("HEAD after apply = %s, want %s", got, head)
	}
	for path, want := range map[string]string{
		"README.md": "independent local edit\n",
		"saved.txt": "saved edit\n",
	} {
		got, err := os.ReadFile(filepath.Join(restored.Path, path))
		if err != nil || string(got) != want {
			t.Fatalf("%s = %q, err=%v; want %q", path, got, err, want)
		}
	}
	if _, err := ws.run(ctx, git, revParseVerifyArgs(repo, ref)...); err == nil {
		t.Fatal("successful apply left the snapshot ref")
	}
}

func writePayload(path string, n int) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	block := make([]byte, 32<<10)
	for i := range block {
		block[i] = byte(i)
	}
	written := 0
	for written < n {
		chunk := block
		if n-written < len(chunk) {
			chunk = chunk[:n-written]
		}
		if _, err := f.Write(chunk); err != nil {
			return err
		}
		written += len(chunk)
	}
	return f.Close()
}

func dirBytes(t *testing.T, root string) int64 {
	t.Helper()
	var total int64
	err := filepath.WalkDir(root, func(_ string, d os.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		total += info.Size()
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("size %s: %v", root, err)
	}
	return total
}
