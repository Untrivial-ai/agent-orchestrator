package session

import (
	"context"
	"errors"
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
