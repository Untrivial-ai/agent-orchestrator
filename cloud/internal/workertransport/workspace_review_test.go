package workertransport

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
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

func TestWorkspaceTreeListsOneLevelWithChangedDirectories(t *testing.T) {
	repo := newGitWorkspace(t)
	writeReviewBytes(t, repo, "src/app.ts", []byte("export const app = true;\n"))
	writeReviewBytes(t, repo, "src/lib/clean.ts", []byte("clean\n"))
	writeWorkspaceFile(t, repo, "README.md", "readme\n")
	writeWorkspaceFile(t, repo, ".gitignore", "ignored.log\n")
	gitWorkspace(t, repo, "add", ".")
	gitWorkspace(t, repo, "commit", "-m", "base")
	gitWorkspace(t, repo, "update-ref", worker.WorkspaceReviewBaseRef, "HEAD")
	writeReviewBytes(t, repo, "src/app.ts", []byte("export const app = false;\n"))
	writeWorkspaceFile(t, repo, "ignored.log", "ignored\n")

	workspace, err := openWorkspace(repo)
	if err != nil {
		t.Fatal(err)
	}
	defer workspace.Close()
	root, err := workspace.ReviewTree(context.Background(), worker.WorkspaceReviewTreeRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(root.Entries) != 3 || root.Entries[0].Name != "src" || root.Entries[0].Type != "dir" || !root.Entries[0].HasChanges {
		t.Fatalf("root entries = %+v", root.Entries)
	}
	if containsTreePath(root.Entries, "ignored.log") {
		t.Fatalf("ignored path leaked into tree: %+v", root.Entries)
	}
	src, err := workspace.ReviewTree(context.Background(), worker.WorkspaceReviewTreeRequest{Path: "src"})
	if err != nil {
		t.Fatal(err)
	}
	if len(src.Entries) != 2 || src.Entries[0].Name != "lib" || src.Entries[0].Type != "dir" || src.Entries[1].Path != "src/app.ts" || src.Entries[1].Status != worker.WorkspaceReviewModified {
		t.Fatalf("src entries = %+v", src.Entries)
	}
	if _, err := workspace.ReviewTree(context.Background(), worker.WorkspaceReviewTreeRequest{Path: "../outside"}); !errors.Is(err, errUnsafePath) {
		t.Fatalf("traversal error = %v, want %v", err, errUnsafePath)
	}
}

func TestWorkspaceSearchMatchesPathsAndBoundedText(t *testing.T) {
	repo := newGitWorkspace(t)
	writeReviewBytes(t, repo, "src/app.ts", []byte("needle in content\n"))
	writeReviewBytes(t, repo, "docs/needle.md", []byte("other\n"))
	writeReviewBytes(t, repo, "image.bin", []byte{0, 'n', 'e', 'e', 'd', 'l', 'e'})
	gitWorkspace(t, repo, "add", ".")
	gitWorkspace(t, repo, "commit", "-m", "base")
	gitWorkspace(t, repo, "update-ref", worker.WorkspaceReviewBaseRef, "HEAD")

	workspace, err := openWorkspace(repo)
	if err != nil {
		t.Fatal(err)
	}
	defer workspace.Close()
	page, err := workspace.ReviewSearch(context.Background(), worker.WorkspaceReviewSearchRequest{Query: "needle", Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Results) != 1 || page.NextCursor == "" || !page.Truncated {
		t.Fatalf("first page = %+v", page)
	}
	next, err := workspace.ReviewSearch(context.Background(), worker.WorkspaceReviewSearchRequest{Query: "needle", Cursor: page.NextCursor, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	paths := []string{page.Results[0].Path}
	for _, result := range next.Results {
		paths = append(paths, result.Path)
	}
	if !slices.Contains(paths, "docs/needle.md") || !slices.Contains(paths, "src/app.ts") || slices.Contains(paths, "image.bin") {
		t.Fatalf("search paths = %v", paths)
	}
}

func TestWorkspaceReviewDiffsHonorEveryScopeAndCommit(t *testing.T) {
	repo := newGitWorkspace(t)
	writeWorkspaceFile(t, repo, "README.md", "base\n")
	gitWorkspace(t, repo, "add", ".")
	gitWorkspace(t, repo, "commit", "-m", "base")
	gitWorkspace(t, repo, "update-ref", worker.WorkspaceReviewBaseRef, "HEAD")
	writeWorkspaceFile(t, repo, "README.md", "committed\n")
	gitWorkspace(t, repo, "commit", "-am", "commit one")
	commit := gitWorkspaceOutput(t, repo, "rev-parse", "HEAD")
	writeWorkspaceFile(t, repo, "README.md", "working\n")
	writeWorkspaceFile(t, repo, "staged.txt", "staged\n")
	gitWorkspace(t, repo, "add", "staged.txt")
	writeWorkspaceFile(t, repo, "notes.txt", "note\n")

	workspace, err := openWorkspace(repo)
	if err != nil {
		t.Fatal(err)
	}
	defer workspace.Close()
	review, err := workspace.ReviewSummary(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name       string
		request    worker.WorkspaceReviewDiffsRequest
		contains   string
		notContain string
	}{
		{"combined", worker.WorkspaceReviewDiffsRequest{Scope: worker.WorkspaceReviewCombined, Paths: []string{"README.md"}, ContextLines: 3, WorkspaceVersion: review.WorkspaceVersion}, "+working", ""},
		{"committed", worker.WorkspaceReviewDiffsRequest{Scope: worker.WorkspaceReviewCommitted, CommitSHA: commit, Paths: []string{"README.md"}, ContextLines: 3, WorkspaceVersion: review.WorkspaceVersion}, "+committed", "+working"},
		{"staged", worker.WorkspaceReviewDiffsRequest{Scope: worker.WorkspaceReviewStaged, Paths: []string{"staged.txt"}, ContextLines: 3, WorkspaceVersion: review.WorkspaceVersion}, "+staged", ""},
		{"unstaged", worker.WorkspaceReviewDiffsRequest{Scope: worker.WorkspaceReviewUnstaged, Paths: []string{"README.md"}, ContextLines: 3, WorkspaceVersion: review.WorkspaceVersion}, "+working", ""},
		{"untracked", worker.WorkspaceReviewDiffsRequest{Scope: worker.WorkspaceReviewUntracked, Paths: []string{"notes.txt"}, ContextLines: 3, WorkspaceVersion: review.WorkspaceVersion}, "+note", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			response, diffErr := workspace.ReviewDiffs(context.Background(), tc.request)
			if diffErr != nil {
				t.Fatal(diffErr)
			}
			if len(response.Groups) != 1 || !strings.Contains(response.Groups[0].Patch, tc.contains) {
				t.Fatalf("response = %+v", response)
			}
			if tc.notContain != "" && strings.Contains(response.Groups[0].Patch, tc.notContain) {
				t.Fatalf("patch unexpectedly contains %q: %s", tc.notContain, response.Groups[0].Patch)
			}
		})
	}
	_, err = workspace.ReviewDiffs(context.Background(), worker.WorkspaceReviewDiffsRequest{
		Scope: worker.WorkspaceReviewCombined, Paths: []string{"README.md"}, WorkspaceVersion: "stale",
	})
	if !errors.Is(err, ErrWorkspaceSnapshotStale) {
		t.Fatalf("stale error = %v", err)
	}
}

func TestWorkspaceReviewFileAndRevisionsReturnSelectedStates(t *testing.T) {
	repo := newGitWorkspace(t)
	writeWorkspaceFile(t, repo, "README.md", "base\n")
	gitWorkspace(t, repo, "add", ".")
	gitWorkspace(t, repo, "commit", "-m", "base")
	gitWorkspace(t, repo, "update-ref", worker.WorkspaceReviewBaseRef, "HEAD")
	writeWorkspaceFile(t, repo, "README.md", "committed\n")
	gitWorkspace(t, repo, "commit", "-am", "commit one")
	commit := gitWorkspaceOutput(t, repo, "rev-parse", "HEAD")
	writeWorkspaceFile(t, repo, "README.md", "working\n")

	workspace, err := openWorkspace(repo)
	if err != nil {
		t.Fatal(err)
	}
	defer workspace.Close()
	detail, err := workspace.ReviewFile(context.Background(), worker.WorkspaceReviewFileRequest{
		Path: "README.md", Scope: worker.WorkspaceReviewCommitted, CommitSHA: commit,
	})
	if err != nil {
		t.Fatal(err)
	}
	if detail.Content != "committed\n" || !detail.Historical || !strings.Contains(detail.Diff, "+committed") {
		t.Fatalf("commit detail = %+v", detail)
	}
	review, err := workspace.ReviewSummary(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	before, err := workspace.ReviewRevision(context.Background(), worker.WorkspaceReviewRevisionRequest{
		Path: "README.md", Scope: worker.WorkspaceReviewCombined, Side: worker.WorkspaceReviewBefore, WorkspaceVersion: review.WorkspaceVersion,
	})
	if err != nil {
		t.Fatal(err)
	}
	after, err := workspace.ReviewRevision(context.Background(), worker.WorkspaceReviewRevisionRequest{
		Path: "README.md", Scope: worker.WorkspaceReviewCombined, Side: worker.WorkspaceReviewAfter, WorkspaceVersion: review.WorkspaceVersion,
	})
	if err != nil {
		t.Fatal(err)
	}
	if before.Content != "base\n" || after.Content != "working\n" || before.Revision == after.Revision {
		t.Fatalf("before=%+v after=%+v", before, after)
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

func containsTreePath(entries []worker.WorkspaceReviewTreeEntry, path string) bool {
	for _, entry := range entries {
		if entry.Path == path {
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

func gitWorkspaceOutput(t *testing.T, workspacePath string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", workspacePath}, args...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}
