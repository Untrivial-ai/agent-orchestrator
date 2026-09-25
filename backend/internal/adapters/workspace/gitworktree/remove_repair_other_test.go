//go:build !windows

package gitworktree

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// Regression for issue #3807: an renv sandbox leaves 0500 directories inside
// the worktree with symlinks into the system R library. Unlinking the symlink
// needs write on its 0500 parent, so os.RemoveAll fails and the worktree is
// stranded. Cleanup must repair the parent, remove the worktree, and never
// follow the symlink into the target.
func TestRemoveAllWithRetryOwnerReadonlySymlink(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root bypasses directory permissions")
	}

	root := t.TempDir()
	worktree := filepath.Join(root, "worktree")
	restricted := filepath.Join(worktree, "renv", "sandbox", "linux", "R-4.5", "hash")
	if err := os.MkdirAll(restricted, 0o750); err != nil {
		t.Fatal(err)
	}

	systemLibrary := filepath.Join(root, "system-library")
	if err := os.MkdirAll(systemLibrary, 0o750); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(systemLibrary, "base-package")
	if err := os.WriteFile(marker, []byte("must survive"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(systemLibrary, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(systemLibrary, 0o700) })

	if err := os.Symlink(systemLibrary, filepath.Join(restricted, "base")); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(restricted, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(restricted, 0o700) })

	if err := removeAllWithRetry(context.Background(), worktree); err != nil {
		t.Fatalf("remove AO-managed worktree: %v", err)
	}
	if _, err := os.Stat(worktree); !os.IsNotExist(err) {
		t.Fatalf("worktree still exists: %v", err)
	}
	if got, err := os.ReadFile(marker); err != nil || string(got) != "must survive" {
		t.Fatalf("symlink target changed: content=%q err=%v", got, err)
	}
	info, err := os.Stat(systemLibrary)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o500 {
		t.Fatalf("symlink target mode changed: got %o, want 500", got)
	}
}

// A read-only directory with plain files inside (no symlinks) is the same
// failure: the files' own modes are irrelevant, only the parent's write bit.
func TestRemoveAllWithRetryOwnerReadonlyNestedFiles(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root bypasses directory permissions")
	}

	worktree := filepath.Join(t.TempDir(), "worktree")
	inner := filepath.Join(worktree, "a", "b")
	if err := os.MkdirAll(inner, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(inner, "f"), []byte("x"), 0o400); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{inner, filepath.Join(worktree, "a")} {
		if err := os.Chmod(dir, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	}

	if err := removeAllWithRetry(context.Background(), worktree); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, err := os.Stat(worktree); !os.IsNotExist(err) {
		t.Fatalf("worktree still exists: %v", err)
	}
}

// A directory with no permissions at all (0000) cannot even be listed. The
// repair must chmod it before descending, or WalkDir never sees its children.
func TestRemoveAllWithRetryOwnerNoPermsDirectory(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root bypasses directory permissions")
	}

	worktree := filepath.Join(t.TempDir(), "worktree")
	inner := filepath.Join(worktree, "locked")
	if err := os.MkdirAll(inner, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(inner, "f"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(inner, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(inner, 0o700) })

	if err := removeAllWithRetry(context.Background(), worktree); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, err := os.Stat(worktree); !os.IsNotExist(err) {
		t.Fatalf("worktree still exists: %v", err)
	}
}
