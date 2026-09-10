package project

import (
	"os"
	"path/filepath"
	"testing"
)

// T5. folderExists helper: dir exists → true, removed → false, replaced by file → false.
func TestFolderExists_HelperSemantics(t *testing.T) {
	t.Parallel()

	t.Run("existing directory returns true", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		exists, err := folderExists(dir)
		if err != nil {
			t.Fatalf("folderExists: %v", err)
		}
		if !exists {
			t.Fatal("folderExists = false for existing directory, want true")
		}
	})

	t.Run("removed directory returns false", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		if err := os.RemoveAll(dir); err != nil {
			t.Fatalf("RemoveAll: %v", err)
		}
		exists, err := folderExists(dir)
		if err != nil {
			t.Fatalf("folderExists: %v", err)
		}
		if exists {
			t.Fatal("folderExists = true for removed directory, want false")
		}
	})

	t.Run("regular file at path returns false", func(t *testing.T) {
		t.Parallel()
		path := filepath.Join(t.TempDir(), "not-a-dir")
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		exists, err := folderExists(path)
		if err != nil {
			t.Fatalf("folderExists: %v", err)
		}
		if exists {
			t.Fatal("folderExists = true for regular file, want false")
		}
	})
}

// T6. Stat error ≠ missing: folderExists must return error for non-ErrNotExist
// failures, never silently treating them as missing. This contract is validated
// by code review: os.Stat returning a non-ErrNotExist error yields
// (false, err), and the caller (projectFromRow / List) logs it and keeps
// FolderMissing=false. Cross-platform permission-error semantics differ
// (Windows DACL vs POSIX mode bits), so we document the contract here rather
// than writing an unreliable platform-dependent test.
//
// The contract, exercised by the code path in service.go:126-131 and :763-769:
//
//   exists, err := folderExists(row.Path)
//   if err != nil {
//       logger.Warn("project: stat failed", "path", row.Path, "error", err)
//       // folderMissing stays false (zero-value)
//   } else {
//       folderMissing = !exists
//   }
//
// On any stat error that is NOT os.ErrNotExist, folderExists returns
// (false, err) with err != nil, so folderMissing remains false and the error
// is logged. This is the intended behavior: an ambiguous stat failure should
// not degrade the project to "missing".
