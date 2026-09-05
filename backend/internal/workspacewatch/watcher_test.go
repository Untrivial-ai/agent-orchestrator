package workspacewatch

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
)

func TestHandleEventIgnoresMetadataOnlyChmod(t *testing.T) {
	root := t.TempDir()
	if handleEvent(context.Background(), nil, root, gitWorkspace{}, fsnotify.Event{
		Name: filepath.Join(root, "README.md"),
		Op:   fsnotify.Chmod,
	}) {
		t.Fatal("metadata-only chmod was treated as a workspace content change")
	}
}

func TestWatchReportsExistingAndNewDirectoryChanges(t *testing.T) {
	root := t.TempDir()
	existing := filepath.Join(root, "src")
	if err := os.Mkdir(existing, 0o755); err != nil {
		t.Fatalf("mkdir existing directory: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	changes, err := Watch(ctx, root)
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}

	if err := os.WriteFile(filepath.Join(existing, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("write existing-directory file: %v", err)
	}
	waitForChange(t, changes)
	drainChanges(changes)

	created := filepath.Join(root, "docs")
	if err := os.Mkdir(created, 0o755); err != nil {
		t.Fatalf("mkdir new directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(created, "guide.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("write new-directory file: %v", err)
	}
	waitForChange(t, changes)
}

func TestWatchReportsChangesAcrossWorkspaceRoots(t *testing.T) {
	first := t.TempDir()
	second := t.TempDir()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	changes, err := Watch(ctx, first, second)
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}

	if err := os.WriteFile(filepath.Join(second, "child.txt"), []byte("updated\n"), 0o644); err != nil {
		t.Fatalf("write second workspace file: %v", err)
	}
	waitForChange(t, changes)
}

func TestWatchClosesWhenContextIsCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	changes, err := Watch(ctx, t.TempDir())
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	cancel()

	select {
	case _, ok := <-changes:
		if ok {
			t.Fatal("received a change after cancellation")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("watch channel did not close after cancellation")
	}
}

func TestWatchDoesNotTurnGitStatusIndexRefreshIntoAChange(t *testing.T) {
	root := t.TempDir()
	runGit(t, root, "init")
	runGit(t, root, "config", "user.email", "ao@example.com")
	runGit(t, root, "config", "user.name", "AO Tests")
	tracked := filepath.Join(root, "README.md")
	if err := os.WriteFile(tracked, []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("write tracked file: %v", err)
	}
	runGit(t, root, "add", "README.md")
	runGit(t, root, "commit", "-m", "initial")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	changes, err := Watch(ctx, root)
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	runGit(t, root, "status", "--porcelain")
	select {
	case <-changes:
		t.Fatal("read-only git status produced a workspace change")
	case <-time.After(250 * time.Millisecond):
	}

	if err := os.WriteFile(tracked, []byte("updated\n"), 0o644); err != nil {
		t.Fatalf("update tracked file: %v", err)
	}
	waitForChange(t, changes)
}

func TestWatchStillReportsChangesWhenWorkspaceIsNestedInAnotherRepository(t *testing.T) {
	parent := t.TempDir()
	runGit(t, parent, "init")
	runGit(t, parent, "config", "user.email", "ao@example.com")
	runGit(t, parent, "config", "user.name", "AO Tests")

	// The workspace sits inside the parent repository. Git's upward search
	// resolves the parent repo, which tracks no files under the workspace; the
	// watch must fall back to walking the tree instead of trusting that answer.
	root := filepath.Join(parent, "workspace", "session")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir nested workspace: %v", err)
	}
	existing := filepath.Join(root, "src")
	if err := os.Mkdir(existing, 0o755); err != nil {
		t.Fatalf("mkdir existing directory: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	changes, err := Watch(ctx, root)
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}

	if err := os.WriteFile(filepath.Join(existing, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("write existing-directory file: %v", err)
	}
	waitForChange(t, changes)
}

func waitForChange(t *testing.T, changes <-chan struct{}) {
	t.Helper()
	select {
	case _, ok := <-changes:
		if !ok {
			t.Fatal("watch channel closed before change")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for workspace change")
	}
}

func drainChanges(changes <-chan struct{}) {
	for {
		select {
		case _, ok := <-changes:
			if !ok {
				return
			}
		default:
			return
		}
	}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}
