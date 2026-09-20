package session

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
)

func workspaceReviewService(t *testing.T, repo string) *Service {
	t.Helper()
	store := newFakeStore()
	store.sessions["ao-1"] = domain.SessionRecord{ID: "ao-1", Metadata: domain.SessionMetadata{WorkspacePath: repo}}
	return &Service{store: store}
}

func TestWorkspaceFileRevisionUsesSectionSpecificSides(t *testing.T) {
	repo := newWorkspaceRepo(t)
	writeWorkspaceFile(t, repo, "README.md", "hello\nstaged addition\n")
	runGit(t, repo, "add", "README.md")
	writeWorkspaceFile(t, repo, "README.md", "hello\nstaged addition\nunstaged addition\n")
	svc := workspaceReviewService(t, repo)

	tests := []struct {
		name    string
		scope   WorkspaceDiffScope
		side    WorkspaceFileBlobSide
		want    string
		notWant string
	}{
		{name: "staged before is HEAD", scope: WorkspaceDiffStaged, side: WorkspaceBlobBefore, want: "hello\n", notWant: "staged addition"},
		{name: "staged after is index", scope: WorkspaceDiffStaged, side: WorkspaceBlobAfter, want: "staged addition", notWant: "unstaged addition"},
		{name: "unstaged before is index", scope: WorkspaceDiffUnstaged, side: WorkspaceBlobBefore, want: "staged addition", notWant: "unstaged addition"},
		{name: "unstaged after is worktree", scope: WorkspaceDiffUnstaged, side: WorkspaceBlobAfter, want: "unstaged addition", notWant: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := svc.GetWorkspaceFileRevision(context.Background(), "ao-1", "README.md", tc.scope, tc.side, "", "")
			if err != nil {
				t.Fatal(err)
			}
			if !got.Exists || got.Binary || got.Truncated || got.Revision == "" {
				t.Fatalf("revision metadata = %#v", got)
			}
			if !strings.Contains(got.Content, tc.want) || (tc.notWant != "" && strings.Contains(got.Content, tc.notWant)) {
				t.Fatalf("content = %q, want %q and not %q", got.Content, tc.want, tc.notWant)
			}
		})
	}
}

func TestWorkspaceFileRevisionRepresentsAbsentUntrackedBeforeSide(t *testing.T) {
	repo := newWorkspaceRepo(t)
	writeWorkspaceFile(t, repo, "notes.txt", "new\n")
	svc := workspaceReviewService(t, repo)

	before, err := svc.GetWorkspaceFileRevision(context.Background(), "ao-1", "notes.txt", WorkspaceDiffUntracked, WorkspaceBlobBefore, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if before.Exists || before.Content != "" || before.Revision != "" {
		t.Fatalf("before = %#v, want explicit absent side", before)
	}
	after, err := svc.GetWorkspaceFileRevision(context.Background(), "ao-1", "notes.txt", WorkspaceDiffUntracked, WorkspaceBlobAfter, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if !after.Exists || after.Content != "new\n" {
		t.Fatalf("after = %#v", after)
	}
}

func TestGetWorkspaceDiffsBatchesPathsAndHonorsWhitespace(t *testing.T) {
	repo := newWorkspaceRepo(t)
	writeWorkspaceFile(t, repo, "README.md", "goodbye\n")
	writeWorkspaceFile(t, repo, "src/app.go", "package main\n\nfunc main() {}\n")
	svc := workspaceReviewService(t, repo)
	files, err := svc.ListWorkspaceFiles(context.Background(), "ao-1")
	if err != nil {
		t.Fatal(err)
	}

	got, err := svc.GetWorkspaceDiffs(context.Background(), "ao-1", WorkspaceDiffInput{
		Scope: WorkspaceDiffCombined, Paths: []string{"README.md", "src/app.go"}, ContextLines: 3,
		IgnoreWhitespace: true, WorkspaceVersion: files.WorkspaceVersion,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Groups) != 1 || len(got.Groups[0].IncludedPaths) != 2 {
		t.Fatalf("groups = %#v", got.Groups)
	}
	if !strings.Contains(got.Groups[0].Patch, "README.md") || !strings.Contains(got.Groups[0].Patch, "src/app.go") {
		t.Fatalf("patch did not contain both files:\n%s", got.Groups[0].Patch)
	}
	if got.WorkspaceVersion != files.WorkspaceVersion {
		t.Fatalf("workspace version = %q, want %q", got.WorkspaceVersion, files.WorkspaceVersion)
	}
}

func TestCommitReviewUsesOnlyTheSelectedCommit(t *testing.T) {
	repo := newWorkspaceRepo(t)
	base := strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))
	runGit(t, repo, "switch", "-c", "ao/commit-review")
	writeWorkspaceFile(t, repo, "README.md", "first commit\n")
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "first change")
	first := strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))
	writeWorkspaceFile(t, repo, "README.md", "second commit\n")
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "second change")

	store := newFakeStore()
	store.sessions["ao-1"] = domain.SessionRecord{ID: "ao-1", Metadata: domain.SessionMetadata{Branch: "ao/commit-review", WorkspacePath: repo, DiffBaseSHA: base, DiffBaseRef: "main"}}
	svc := &Service{store: store}
	files, err := svc.ListWorkspaceFiles(context.Background(), "ao-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(files.Commits) != 2 || files.Commits[0].Subject != "second change" || files.Commits[1].Subject != "first change" {
		t.Fatalf("commits = %+v, want newest first", files.Commits)
	}

	diffs, err := svc.GetWorkspaceDiffs(context.Background(), "ao-1", WorkspaceDiffInput{
		Scope: WorkspaceDiffCommitted, CommitSHA: first, Paths: []string{"README.md"}, ContextLines: 3, WorkspaceVersion: files.WorkspaceVersion,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(diffs.Groups) != 1 || !strings.Contains(diffs.Groups[0].Patch, "+first commit") || strings.Contains(diffs.Groups[0].Patch, "+second commit") {
		t.Fatalf("selected commit patch = %q", diffs.Groups[0].Patch)
	}

	detail, err := svc.GetWorkspaceFileAtCommit(context.Background(), "ao-1", "README.md", first)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Content != "first commit\n" || !strings.Contains(detail.Diff, "+first commit") || detail.Editable {
		t.Fatalf("selected commit detail = %#v", detail)
	}
	after, err := svc.GetWorkspaceFileRevisionAtCommit(context.Background(), "ao-1", "README.md", WorkspaceBlobAfter, files.WorkspaceVersion, "", first)
	if err != nil {
		t.Fatal(err)
	}
	if after.Content != "first commit\n" {
		t.Fatalf("selected commit after = %q", after.Content)
	}
}

func TestCommitReviewRejectsCommitOutsideComparison(t *testing.T) {
	repo := newWorkspaceRepo(t)
	svc := workspaceReviewService(t, repo)
	_, err := svc.GetWorkspaceDiffs(context.Background(), "ao-1", WorkspaceDiffInput{
		Scope: WorkspaceDiffCommitted, CommitSHA: strings.Repeat("a", 40), Paths: []string{"README.md"}, ContextLines: 3,
	})
	var apiError *apierr.Error
	if !errors.As(err, &apiError) || apiError.Code != "WORKSPACE_COMMIT_NOT_FOUND" {
		t.Fatalf("error = %#v, want WORKSPACE_COMMIT_NOT_FOUND", err)
	}
}

func TestGetWorkspaceDiffsRejectsStaleWorkspaceVersion(t *testing.T) {
	repo := newWorkspaceRepo(t)
	svc := workspaceReviewService(t, repo)
	_, err := svc.GetWorkspaceDiffs(context.Background(), "ao-1", WorkspaceDiffInput{
		Scope: WorkspaceDiffCombined, Paths: []string{"README.md"}, ContextLines: 3, WorkspaceVersion: "stale",
	})
	var apiError *apierr.Error
	if !strings.Contains(err.Error(), "Workspace changed") || !errors.As(err, &apiError) || apiError.Code != "WORKSPACE_SNAPSHOT_STALE" {
		t.Fatalf("error = %#v, want WORKSPACE_SNAPSHOT_STALE", err)
	}
}

func TestWorkspaceFileRevisionRejectsStaleWorkspaceVersion(t *testing.T) {
	repo := newWorkspaceRepo(t)
	svc := workspaceReviewService(t, repo)

	_, err := svc.GetWorkspaceFileRevision(context.Background(), "ao-1", "README.md", WorkspaceDiffCombined, WorkspaceBlobAfter, "stale", "")
	var apiError *apierr.Error
	if !errors.As(err, &apiError) || apiError.Code != "WORKSPACE_SNAPSHOT_STALE" {
		t.Fatalf("error = %#v, want WORKSPACE_SNAPSHOT_STALE", err)
	}
}

func TestCappedWorkspaceOutputRetainsBoundedPrefix(t *testing.T) {
	writer := &cappedWorkspaceOutput{limit: 5}
	if written, err := writer.Write([]byte("abcdefgh")); err != nil || written != 8 {
		t.Fatalf("Write = %d, %v", written, err)
	}
	if got := writer.buffer.String(); got != "abcde" || !writer.truncated {
		t.Fatalf("output = %q truncated=%v", got, writer.truncated)
	}
}

func TestSearchWorkspaceFilesReturnsPaginatedPathMatches(t *testing.T) {
	repo := newWorkspaceRepo(t)
	writeWorkspaceFile(t, repo, "docs/alpha.md", "a\n")
	writeWorkspaceFile(t, repo, "docs/alphabet.md", "b\n")
	writeWorkspaceFile(t, repo, "docs/beta.md", "c\n")
	svc := workspaceReviewService(t, repo)

	first, err := svc.SearchWorkspaceFiles(context.Background(), "ao-1", "ALPHA", "", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Results) != 1 || first.NextCursor == "" || !strings.Contains(first.Results[0].Path, "alpha") {
		t.Fatalf("first page = %#v", first)
	}
	second, err := svc.SearchWorkspaceFiles(context.Background(), "ao-1", "alpha", first.NextCursor, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Results) != 1 || second.Results[0].Path == first.Results[0].Path || second.NextCursor != "" {
		t.Fatalf("second page = %#v", second)
	}
}

func workspaceChildRepo(t *testing.T, root, name, content string) string {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "init")
	runGit(t, dir, "config", "user.email", "ao@example.com")
	runGit(t, dir, "config", "user.name", "AO Tests")
	writeWorkspaceFile(t, dir, "service.go", content)
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "initial "+name)
	return dir
}

// A workspace project leaves Sections and Commits zero-valued, so its review
// pane can only request the combined scope. A file an agent just created is
// untracked, and git diff never reports one — the group came back empty and the
// Files inspector sat on "Loading diff..." against successful responses.
func TestGetWorkspaceDiffsIncludesUntrackedChildRepoFile(t *testing.T) {
	root := newWorkspaceRepo(t)
	rootBase := strings.TrimSpace(runGit(t, root, "rev-parse", "HEAD"))
	alpha := workspaceChildRepo(t, root, "alpha", "package alpha\n")
	alphaBase := strings.TrimSpace(runGit(t, alpha, "rev-parse", "HEAD"))
	beta := workspaceChildRepo(t, root, "beta", "package beta\n")
	betaBase := strings.TrimSpace(runGit(t, beta, "rev-parse", "HEAD"))
	writeWorkspaceFile(t, alpha, "workspace-test.txt", "alpha workspace test\n")
	writeWorkspaceFile(t, beta, "service.go", "package beta\n\nfunc Added() {}\n")

	st := newFakeStore()
	st.projects["ws"] = domain.ProjectRecord{ID: "ws", Kind: domain.ProjectKindWorkspace}
	st.sessions["ws-1"] = domain.SessionRecord{ID: "ws-1", ProjectID: "ws", Metadata: domain.SessionMetadata{WorkspacePath: root}}
	st.worktrees["ws-1"] = []domain.SessionWorktreeRecord{
		{SessionID: "ws-1", RepoName: domain.RootWorkspaceRepoName, WorktreePath: root, BaseSHA: rootBase},
		{SessionID: "ws-1", RepoName: "alpha", WorktreePath: alpha, BaseSHA: alphaBase},
		{SessionID: "ws-1", RepoName: "beta", WorktreePath: beta, BaseSHA: betaBase},
	}
	svc := &Service{store: st}
	files, err := svc.ListWorkspaceFiles(context.Background(), "ws-1")
	if err != nil {
		t.Fatal(err)
	}

	got, err := svc.GetWorkspaceDiffs(context.Background(), "ws-1", WorkspaceDiffInput{
		Scope: WorkspaceDiffCombined, Paths: []string{"alpha/workspace-test.txt", "beta/service.go"},
		ContextLines: 3, WorkspaceVersion: files.WorkspaceVersion,
	})
	if err != nil {
		t.Fatal(err)
	}
	patches := map[string]string{}
	for _, group := range got.Groups {
		if len(group.Errors) > 0 || len(group.Deferred) > 0 {
			t.Fatalf("group %q = errors:%#v deferred:%#v", group.Repository, group.Errors, group.Deferred)
		}
		patches[group.Repository] = group.Patch
	}
	// The renderer keys its diffs by "<repository>/<path>", so an empty patch
	// leaves the file with nothing to render at all.
	if !strings.Contains(patches["alpha"], "+++ b/workspace-test.txt") || !strings.Contains(patches["alpha"], "+alpha workspace test") {
		t.Fatalf("untracked child repo patch = %q", patches["alpha"])
	}
	if !strings.Contains(patches["beta"], "+func Added() {}") {
		t.Fatalf("tracked child repo patch = %q", patches["beta"])
	}
}

func TestGetWorkspaceDiffsCombinesUntrackedAndTrackedPathsInOneRepo(t *testing.T) {
	repo := newWorkspaceRepo(t)
	writeWorkspaceFile(t, repo, "README.md", "goodbye\n")
	writeWorkspaceFile(t, repo, "notes.txt", "new notes\n")
	svc := workspaceReviewService(t, repo)
	files, err := svc.ListWorkspaceFiles(context.Background(), "ao-1")
	if err != nil {
		t.Fatal(err)
	}

	got, err := svc.GetWorkspaceDiffs(context.Background(), "ao-1", WorkspaceDiffInput{
		Scope: WorkspaceDiffCombined, Paths: []string{"README.md", "notes.txt"}, ContextLines: 3,
		WorkspaceVersion: files.WorkspaceVersion,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Groups) != 1 {
		t.Fatalf("groups = %#v", got.Groups)
	}
	patch := got.Groups[0].Patch
	if !strings.Contains(patch, "+goodbye") || !strings.Contains(patch, "+new notes") {
		t.Fatalf("patch did not contain both the tracked and untracked change:\n%s", patch)
	}
}
