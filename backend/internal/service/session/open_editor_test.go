package session

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// The file pick is the interesting half of OpenInEditor: OpenInEditor itself
// spawns a real editor, so these exercise the resolution steps directly.

func TestMostRecentlyChangedFilePicksNewestChange(t *testing.T) {
	repo := newWorkspaceRepo(t)
	writeWorkspaceFile(t, repo, "README.md", "goodbye\nupdated\n")
	writeWorkspaceFile(t, repo, "src/download.go", "package src\n")
	// Age README so download.go is unambiguously the newest change.
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(filepath.Join(repo, "README.md"), old, old); err != nil {
		t.Fatal(err)
	}

	got := mostRecentlyChangedFile(context.Background(), repo)
	if got != "src/download.go" {
		t.Fatalf("picked %q, want src/download.go", got)
	}
}

func TestMostRecentlyChangedFileSkipsDeletedPaths(t *testing.T) {
	repo := newWorkspaceRepo(t)
	if err := os.Remove(filepath.Join(repo, "src", "app.go")); err != nil {
		t.Fatal(err)
	}

	got := mostRecentlyChangedFile(context.Background(), repo)
	if got != "" {
		t.Fatalf("picked %q, want no file (only change is a deletion)", got)
	}
}

func TestMostRecentlyChangedFileCleanWorktreeOpensFolderOnly(t *testing.T) {
	repo := newWorkspaceRepo(t)

	got := mostRecentlyChangedFile(context.Background(), repo)
	if got != "" {
		t.Fatalf("picked %q, want no file on a clean worktree", got)
	}
}

// An untracked symlink pointing outside the worktree is exactly the "newest
// change" mostRecentlyChangedFile is meant to surface, and editorTargetFile
// must not hand its resolved target to the editor unconfined.
func TestEditorTargetFileAutoPickSkipsASymlinkEscape(t *testing.T) {
	repo := newWorkspaceRepo(t)
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("outside the workspace\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(repo, "escape.txt")
	if err := os.Symlink(secret, link); err != nil {
		t.Skipf("creating symlink: %v", err)
	}

	rel, abs, err := (&Service{}).editorTargetFile(context.Background(), repo, "workspace", "")
	if err != nil {
		t.Fatal(err)
	}
	if rel != "" || abs != "" {
		t.Fatalf("got (%q, %q), want the escaping symlink skipped like no change at all", rel, abs)
	}
}

func TestEditorRootFallsBackToProjectCheckout(t *testing.T) {
	project := newWorkspaceRepo(t)
	st := newFakeStore()
	st.sessions["ao-1"] = domain.SessionRecord{ID: "ao-1", ProjectID: "ao"}
	st.projects["ao"] = domain.ProjectRecord{ID: "ao", Path: project}

	root, scope, err := (&Service{store: st}).editorRoot(context.Background(), "ao-1")
	if err != nil {
		t.Fatal(err)
	}
	if root != project {
		t.Fatalf("root = %q, want %q", root, project)
	}
	if scope != "project" {
		t.Fatalf("scope = %q, want project", scope)
	}
}

func TestEditorRootPrefersLiveWorktree(t *testing.T) {
	worktree := newWorkspaceRepo(t)
	project := t.TempDir()
	st := newFakeStore()
	st.sessions["ao-1"] = domain.SessionRecord{
		ID:        "ao-1",
		ProjectID: "ao",
		Metadata:  domain.SessionMetadata{WorkspacePath: worktree},
	}
	st.projects["ao"] = domain.ProjectRecord{ID: "ao", Path: project}

	root, scope, err := (&Service{store: st}).editorRoot(context.Background(), "ao-1")
	if err != nil {
		t.Fatal(err)
	}
	if root != worktree {
		t.Fatalf("root = %q, want the worktree %q", root, worktree)
	}
	if scope != "workspace" {
		t.Fatalf("scope = %q, want workspace", scope)
	}
}

func TestEditorTargetFileRejectsEscapingPath(t *testing.T) {
	repo := newWorkspaceRepo(t)

	if _, _, err := (&Service{}).editorTargetFile(context.Background(), repo, "workspace", "../../etc/passwd"); err == nil {
		t.Fatal("traversal path was accepted")
	}
}

func TestEditorTargetFileDotOpensFolderOnly(t *testing.T) {
	repo := newWorkspaceRepo(t)
	writeWorkspaceFile(t, repo, "README.md", "changed\n")

	rel, abs, err := (&Service{}).editorTargetFile(context.Background(), repo, "workspace", ".")
	if err != nil {
		t.Fatal(err)
	}
	if rel != "" || abs != "" {
		t.Fatalf("got (%q, %q), want no file", rel, abs)
	}
}

func TestEditorTargetFileProjectScopeOpensNoFiles(t *testing.T) {
	repo := newWorkspaceRepo(t)
	writeWorkspaceFile(t, repo, "README.md", "changed\n")

	rel, abs, err := (&Service{}).editorTargetFile(context.Background(), repo, "project", "")
	if err != nil {
		t.Fatal(err)
	}
	if rel != "" || abs != "" {
		t.Fatalf("got (%q, %q), want no file for a project-scope open", rel, abs)
	}
}
