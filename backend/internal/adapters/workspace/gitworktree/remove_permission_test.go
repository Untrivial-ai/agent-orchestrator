package gitworktree

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestRemoveAllWithRetryOwnerReadonlySymlink(t *testing.T) {

	if runtime.GOOS == "windows" {
		t.Skip("Unix permission probe")
	}

	root := t.TempDir()

	worktree := filepath.Join(root, "worktree")
	restricted := filepath.Join(
		worktree,
		"renv",
		"sandbox",
		"linux",
		"R-4.5",
		"hash",
	)

	if err := os.MkdirAll(restricted, 0o750); err != nil {
		t.Fatal(err)
	}

	systemLibrary := filepath.Join(root, "system-library")

	if err := os.MkdirAll(systemLibrary, 0o750); err != nil {
		t.Fatal(err)
	}

	marker := filepath.Join(systemLibrary, "base-package")

	if err := os.WriteFile(
		marker,
		[]byte("must survive"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}

	// Make the symlink target read-only too.
	if err := os.Chmod(systemLibrary, 0o500); err != nil {
		t.Fatal(err)
	}

	// Restore permissions so TempDir cleanup can remove it.
	t.Cleanup(func() {
		_ = os.Chmod(systemLibrary, 0o700)
	})

	if err := os.Symlink(
		systemLibrary,
		filepath.Join(restricted, "base"),
	); err != nil {
		t.Fatal(err)
	}

	// Critical part of the reproducer:
	// owner can read/traverse but cannot modify this directory.
	if err := os.Chmod(restricted, 0o500); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		_ = os.Chmod(restricted, 0o700)
	})

	err := removeAllWithRetry(context.Background(), worktree)
	if err != nil {
		t.Fatalf("remove AO-managed worktree: %v", err)
	}

	if _, err := os.Stat(worktree); !os.IsNotExist(err) {
		t.Fatalf("worktree still exists: %v", err)
	}

	got, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("read symlink target: %v", err)
	}

	if string(got) != "must survive" {
		t.Fatalf("symlink target changed: content=%q", got)
	}
}

func TestRemoveAllWithRetryDirectoryPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission tests")
	}

	tests := []struct {
		name  string
		setup func(t *testing.T, worktree string)
	}{
		{
			name: "readonly directory with file",
			setup: func(t *testing.T, worktree string) {
				dir := filepath.Join(worktree, "readonly")
				mkdir(t, dir, 0o750)
				writeFile(t, filepath.Join(dir, "file.txt"), "hello", 0o600)
				chmod(t, dir, 0o500)
			},
		},
		{
			name: "nested readonly directories",
			setup: func(t *testing.T, worktree string) {
				dir := filepath.Join(worktree, "a", "b", "c")
				mkdir(t, dir, 0o750)
				writeFile(t, filepath.Join(dir, "file.txt"), "hello", 0o600)

				chmod(t, filepath.Join(worktree, "a", "b", "c"), 0o500)
				chmod(t, filepath.Join(worktree, "a", "b"), 0o500)
			},
		},
		{
			name: "zero permission directory",
			setup: func(t *testing.T, worktree string) {
				dir := filepath.Join(worktree, "locked")
				mkdir(t, dir, 0o750)
				writeFile(t, filepath.Join(dir, "file.txt"), "hello", 0o600)
				chmod(t, dir, 0o000)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			worktree := filepath.Join(root, "worktree")

			mkdir(t, worktree, 0o750)
			tt.setup(t, worktree)

			if err := removeAllWithRetry(
				context.Background(),
				worktree,
			); err != nil {
				t.Fatalf("remove worktree: %v", err)
			}

			assertNotExists(t, worktree)
		})
	}
}

func TestRemoveAllWithRetryReadonlyDirectoryWithExternalFileSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission tests")
	}

	root := t.TempDir()

	worktree := filepath.Join(root, "worktree")
	restricted := filepath.Join(worktree, "restricted")

	external := filepath.Join(root, "external.txt")
	externalContents := "must survive"

	mkdir(t, restricted, 0o750)
	writeFile(t, external, externalContents, 0o600)

	link := filepath.Join(restricted, "link")
	if err := os.Symlink(external, link); err != nil {
		t.Fatal(err)
	}

	chmod(t, restricted, 0o500)

	if err := removeAllWithRetry(
		context.Background(),
		worktree,
	); err != nil {
		t.Fatalf("remove worktree: %v", err)
	}

	assertNotExists(t, worktree)

	// The symlink itself should be gone, but its target must survive.
	assertFileContents(t, external, externalContents)
}

func TestRemoveAllWithRetryReadonlyDirectoryWithReadonlyExternalSymlinkTarget(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission tests")
	}

	root := t.TempDir()

	worktree := filepath.Join(root, "worktree")
	restricted := filepath.Join(worktree, "restricted")

	external := filepath.Join(root, "external")
	externalFile := filepath.Join(external, "important.txt")

	mkdir(t, restricted, 0o750)
	mkdir(t, external, 0o750)
	writeFile(t, externalFile, "must survive", 0o600)

	// Make the external target itself restrictive.
	chmod(t, external, 0o500)

	// Restore it after the test so TempDir cleanup works.
	t.Cleanup(func() {
		_ = os.Chmod(external, 0o700)
	})

	link := filepath.Join(restricted, "external")
	if err := os.Symlink(external, link); err != nil {
		t.Fatal(err)
	}

	chmod(t, restricted, 0o500)

	if err := removeAllWithRetry(
		context.Background(),
		worktree,
	); err != nil {
		t.Fatalf("remove worktree: %v", err)
	}

	assertNotExists(t, worktree)

	// The external target must survive.
	assertExists(t, external)
	assertFileContents(t, externalFile, "must survive")

	info, err := os.Stat(external)
	if err != nil {
		t.Fatal(err)
	}

	if info.Mode().Perm() != 0o500 {
		t.Fatalf(
			"external directory permissions changed: got %o, want %o",
			info.Mode().Perm(),
			0o500,
		)
	}
}

func TestRemoveAllWithRetryBrokenSymlinkInReadonlyDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission tests")
	}

	root := t.TempDir()

	worktree := filepath.Join(root, "worktree")
	restricted := filepath.Join(worktree, "restricted")

	mkdir(t, restricted, 0o750)

	link := filepath.Join(restricted, "broken")
	if err := os.Symlink(
		"/this/path/does/not/exist",
		link,
	); err != nil {
		t.Fatal(err)
	}

	chmod(t, restricted, 0o500)

	if err := removeAllWithRetry(
		context.Background(),
		worktree,
	); err != nil {
		t.Fatalf("remove worktree: %v", err)
	}

	assertNotExists(t, worktree)
}

func TestRemoveAllWithRetryDeepReadonlyTree(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission tests")
	}

	root := t.TempDir()
	worktree := filepath.Join(root, "worktree")

	// Build the complete tree while all directories are accessible.
	current := worktree
	dirs := make([]string, 0, 10)

	for i := 0; i < 10; i++ {
		current = filepath.Join(
			current,
			"level-"+string(rune('0'+i)),
		)

		mkdir(t, current, 0o750)
		dirs = append(dirs, current)
	}

	// Populate the tree before making directories inaccessible.
	writeFile(
		t,
		filepath.Join(current, "deep-file"),
		"hello",
		0o600,
	)

	// Lock directories deepest-first so we can still reach every
	// directory while changing its permissions.
	for i := len(dirs) - 1; i >= 0; i-- {
		dir := dirs[i]

		if i%2 == 0 {
			chmod(t, dir, 0o500)
		} else {
			chmod(t, dir, 0o000)
		}
	}

	if err := removeAllWithRetry(
		context.Background(),
		worktree,
	); err != nil {
		t.Fatalf("remove worktree: %v", err)
	}

	assertNotExists(t, worktree)
}

func TestRemoveAllWithRetryNonPermissionErrorDoesNotRetry(t *testing.T) {
	if removeAllRetryEnabled {
		t.Skip("Unix-only permission repair behavior")
	}

	originalRemoveAll := removeAll
	t.Cleanup(func() {
		removeAll = originalRemoveAll
	})

	var calls int
	expectedErr := errors.New("synthetic filesystem failure")

	removeAll = func(path string) error {
		calls++
		return expectedErr
	}

	err := removeAllWithRetry(context.Background(), "/does/not/matter")

	if !errors.Is(err, expectedErr) {
		t.Fatalf("got error %v, want %v", err, expectedErr)
	}

	if calls != 1 {
		t.Fatalf("removeAll called %d times, want 1", calls)
	}
}

// Helpers

func mkdir(t *testing.T, path string, mode os.FileMode) {
	t.Helper()

	if err := os.MkdirAll(path, mode); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}

	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("chmod %s: %v", path, err)
	}
}

func chmod(t *testing.T, path string, mode os.FileMode) {
	t.Helper()

	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("chmod %s to %o: %v", path, mode, err)
	}
}

func writeFile(
	t *testing.T,
	path string,
	contents string,
	mode os.FileMode,
) {
	t.Helper()

	if err := os.WriteFile(
		path,
		[]byte(contents),
		mode,
	); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}

	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("chmod %s to %o: %v", path, mode, err)
	}
}

func assertExists(t *testing.T, path string) {
	t.Helper()

	if _, err := os.Lstat(path); err != nil {
		t.Fatalf("expected %s to exist: %v", path, err)
	}
}

func assertNotExists(t *testing.T, path string) {
	t.Helper()

	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf(
			"expected %s to not exist, got err=%v",
			path,
			err,
		)
	}
}

func assertFileContents(
	t *testing.T,
	path string,
	expected string,
) {
	t.Helper()

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	if string(got) != expected {
		t.Fatalf(
			"unexpected contents for %s: got %q, want %q",
			path,
			string(got),
			expected,
		)
	}
}
