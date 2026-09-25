//go:build !windows

package gitworktree

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// repairRemovePermissions restores owner write+search on every real directory
// under root so a following os.RemoveAll can unlink their children. Unlinking
// an entry needs write permission on its PARENT directory, not on the entry
// itself, so a dependency manager that leaves a directory at 0500 (renv's
// sandbox does this) makes everything inside it undeletable even for the
// owner. Regular files are left alone: their own mode never blocks unlink.
//
// Symlinks are skipped entirely. filepath.WalkDir reports them via Lstat and
// never descends into them, and a plain os.Chmod would follow the link and
// change the TARGET's mode — which may live far outside the worktree (renv
// links straight into the system R library). A symlink is unlinked from its
// parent like any other entry, so the parent's repair is all it needs.
//
// The chmod itself goes through os.Root so that even if an entry is swapped
// for a symlink between the walk's Lstat and the chmod, nothing outside the
// worktree can be reached: Root refuses any path that escapes it.
//
// Reports whether anything changed so the caller only retries when a retry
// can plausibly succeed. Chmod failures are ignored: a directory AO does not
// own cannot be repaired here, and the retry surfaces the real error.
func repairRemovePermissions(root string) bool {
	scoped, err := os.OpenRoot(root)
	if err != nil {
		return false
	}
	defer func() { _ = scoped.Close() }()

	repaired := false
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d == nil {
			// ReadDir failed on a directory (0000 before repair, or gone):
			// its own entry callback already ran and chmod'd it, so the next
			// RemoveAll pass gets further. Nothing more to do in this subtree.
			return fs.SkipDir
		}
		if d.Type()&fs.ModeSymlink != 0 || !d.IsDir() {
			return nil
		}
		info, statErr := d.Info()
		if statErr != nil {
			return fs.SkipDir
		}
		const need = 0o700
		if info.Mode().Perm()&need == need {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return fs.SkipDir
		}
		if chmodErr := scoped.Chmod(rel, info.Mode().Perm()|need); chmodErr == nil {
			repaired = true
		}
		return nil
	})
	return repaired
}

func isPermissionRemoveError(err error) bool { return errors.Is(err, fs.ErrPermission) }
