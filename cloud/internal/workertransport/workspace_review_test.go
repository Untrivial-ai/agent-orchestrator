package workertransport

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/aoagents/agent-orchestrator/cloud/internal/worker"
)

func TestWorkspaceReviewSummaryReturnsAllFilesAndCategorizedChanges(t *testing.T) {
	repo := newGitWorkspace(t)
	writeWorkspaceFile(t, repo, "README.md", "base\n")
	writeWorkspaceFile(t, repo, "unchanged.txt", "same\n")
	writeWorkspaceFile(t, repo, "old.txt", "rename me\n")
	writeWorkspaceFile(t, repo, "image.bin", string([]byte{0, 1, 2}))
	gitWorkspace(t, repo, "add", ".")
	gitWorkspace(t, repo, "commit", "-m", "base")
	gitWorkspace(t, repo, "update-ref", worker.WorkspaceReviewBaseRef, "HEAD")

	writeWorkspaceFile(t, repo, "README.md", "committed\n")
	gitWorkspace(t, repo, "mv", "old.txt", "renamed.txt")
	gitWorkspace(t, repo, "commit", "-am", "committed change")
	writeWorkspaceFile(t, repo, "README.md", "working\n")
	writeWorkspaceFile(t, repo, "staged.txt", "one\ntwo\n")
	gitWorkspace(t, repo, "add", "staged.txt")
	writeWorkspaceFile(t, repo, "notes.txt", "untracked\n")
	writeWorkspaceFile(t, repo, "image.bin", string([]byte{0, 3, 4}))

	workspace, err := openWorkspace(repo)
	if err != nil {
		t.Fatal(err)
	}
	defer workspace.Close()

	review, err := workspace.ReviewSummary(context.Background())
	if err != nil {
		t.Fatalf("ReviewSummary: %v", err)
	}
	if review.WorkspaceVersion == "" || review.CompareBaseSHA == "" || review.CompareBaseRef != worker.WorkspaceReviewBaseRef {
		t.Fatalf("review identity = %+v", review)
	}
	for _, path := range []string{"README.md", "image.bin", "notes.txt", "renamed.txt", "staged.txt", "unchanged.txt"} {
		if !containsReviewPath(review.Files, path) {
			t.Fatalf("all files missing %q: %+v", path, review.Files)
		}
	}
	if !containsReviewPath(review.Sections.Committed, "README.md") || !containsReviewRename(review.Sections.Committed, "renamed.txt", "old.txt") {
		t.Fatalf("committed = %+v", review.Sections.Committed)
	}
	if !containsReviewPath(review.Sections.Staged, "staged.txt") {
		t.Fatalf("staged = %+v", review.Sections.Staged)
	}
	if !containsReviewPath(review.Sections.Unstaged, "README.md") || !containsReviewPath(review.Sections.Unstaged, "image.bin") {
		t.Fatalf("unstaged = %+v", review.Sections.Unstaged)
	}
	if !containsReviewPath(review.Sections.Untracked, "notes.txt") {
		t.Fatalf("untracked = %+v", review.Sections.Untracked)
	}
	if len(review.Commits) != 1 || review.Commits[0].Subject != "committed change" {
		t.Fatalf("commits = %+v", review.Commits)
	}
	if review.Summary.Files < 5 || review.Summary.Additions == 0 || review.Summary.Deletions == 0 {
		t.Fatalf("summary = %+v", review.Summary)
	}
}

func TestWorkspaceReviewSummaryCleanRepositoryStillListsTrackedFiles(t *testing.T) {
	repo := newGitWorkspace(t)
	writeWorkspaceFile(t, repo, "README.md", "clean\n")
	gitWorkspace(t, repo, "add", ".")
	gitWorkspace(t, repo, "commit", "-m", "base")
	gitWorkspace(t, repo, "update-ref", worker.WorkspaceReviewBaseRef, "HEAD")

	workspace, err := openWorkspace(repo)
	if err != nil {
		t.Fatal(err)
	}
	defer workspace.Close()
	review, err := workspace.ReviewSummary(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(review.Files) != 1 || review.Files[0].Path != "README.md" || review.Files[0].Status != worker.WorkspaceReviewUnmodified {
		t.Fatalf("clean files = %+v", review.Files)
	}
	if review.Summary != (worker.WorkspaceReviewSummary{}) {
		t.Fatalf("clean summary = %+v", review.Summary)
	}
}

func TestWorkspaceVersionChangesWithIndexAndWorktree(t *testing.T) {
	repo := newGitWorkspace(t)
	writeWorkspaceFile(t, repo, "README.md", "base\n")
	gitWorkspace(t, repo, "add", ".")
	gitWorkspace(t, repo, "commit", "-m", "base")
	gitWorkspace(t, repo, "update-ref", worker.WorkspaceReviewBaseRef, "HEAD")
	workspace, err := openWorkspace(repo)
	if err != nil {
		t.Fatal(err)
	}
	defer workspace.Close()

	first, err := workspace.ReviewSummary(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	writeWorkspaceFile(t, repo, "README.md", "changed\n")
	second, err := workspace.ReviewSummary(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if first.WorkspaceVersion == second.WorkspaceVersion {
		t.Fatalf("workspace version did not change after worktree edit: %q", first.WorkspaceVersion)
	}
	gitWorkspace(t, repo, "add", "README.md")
	third, err := workspace.ReviewSummary(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if second.WorkspaceVersion == third.WorkspaceVersion {
		t.Fatalf("workspace version did not change after index edit: %q", second.WorkspaceVersion)
	}
}

func containsReviewPath(files []worker.WorkspaceReviewFileSummary, path string) bool {
	for _, file := range files {
		if file.Path == path {
			return true
		}
	}
	return false
}

func containsReviewRename(files []worker.WorkspaceReviewFileSummary, path, previous string) bool {
	for _, file := range files {
		if file.Path == path && file.PreviousPath == previous && file.Status == worker.WorkspaceReviewRenamed {
			return true
		}
	}
	return false
}

func writeReviewBytes(t *testing.T, repo, name string, content []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filepath.Join(repo, name)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, name), content, 0o600); err != nil {
		t.Fatal(err)
	}
}
