package session

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
)

func TestPRFilesUsePersistedBaseAndHeadWithoutReadingWorkspaceChanges(t *testing.T) {
	repo := newWorkspaceRepo(t)
	runGit(t, repo, "remote", "add", "origin", "https://example.test/acme/repo.git")
	base := strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))
	runGit(t, repo, "switch", "-c", "feature")
	writeWorkspaceFile(t, repo, "README.md", "pull request\n")
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "pr change")
	head := strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))
	writeWorkspaceFile(t, repo, "notes.txt", "workspace only\n")

	st := newFakeStore()
	st.sessions["ao-1"] = domain.SessionRecord{ID: "ao-1", Metadata: domain.SessionMetadata{WorkspacePath: repo}}
	st.prs["ao-1"] = []domain.PullRequest{{Number: 42, URL: "https://example.test/acme/repo/-/merge_requests/42", Provider: "gitlab", Host: "example.test", Repo: "acme/repo", SourceBranch: "feature", TargetBranch: "main", BaseSHA: base, HeadSHA: head}}
	svc := &Service{store: st}

	files, err := svc.ListPRFiles(context.Background(), "ao-1", 42, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(files.Files) != 1 || files.Files[0].Path != "README.md" {
		t.Fatalf("files = %+v, want only README.md", files.Files)
	}
	if _, err := os.Stat(filepath.Join(repo, "notes.txt")); err != nil {
		t.Fatalf("workspace was unexpectedly changed: %v", err)
	}
	previousPath := ""
	detail, err := svc.GetPRFile(context.Background(), "ao-1", 42, "", "README.md", &previousPath)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Content != "pull request\n" || !strings.Contains(detail.Diff, "+pull request") {
		t.Fatalf("detail = %+v", detail)
	}
}

func TestPRFilesListCommitsAndReadOneCommit(t *testing.T) {
	repo := newWorkspaceRepo(t)
	runGit(t, repo, "remote", "add", "origin", "https://example.test/acme/repo.git")
	base := strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))
	runGit(t, repo, "switch", "-c", "feature")
	writeWorkspaceFile(t, repo, "README.md", "first\n")
	runGit(t, repo, "commit", "-am", "first change")
	first := strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))
	writeWorkspaceFile(t, repo, "README.md", "second\n")
	writeWorkspaceFile(t, repo, "notes.txt", "notes\n")
	runGit(t, repo, "add", ".")
	runGit(t, repo, "commit", "-m", "second change")
	head := strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))
	writeWorkspaceFile(t, repo, "README.md", "workspace only\n")

	st := newFakeStore()
	st.sessions["ao-1"] = domain.SessionRecord{ID: "ao-1", Metadata: domain.SessionMetadata{WorkspacePath: repo}}
	st.prs["ao-1"] = []domain.PullRequest{{Number: 42, URL: "https://example.test/acme/repo/-/merge_requests/42", Provider: "gitlab", Host: "example.test", Repo: "acme/repo", BaseSHA: base, HeadSHA: head}}
	svc := &Service{store: st}
	ctx := context.Background()

	files, err := svc.ListPRFiles(ctx, "ao-1", 42, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(files.Commits) != 2 || files.Commits[0].SHA != head || files.Commits[1].SHA != first {
		t.Fatalf("commits = %+v, want second then first", files.Commits)
	}
	if got := files.Commits[0].Files; len(got) != 2 || got[0].Path != "README.md" || got[1].Path != "notes.txt" || got[1].Status != WorkspaceFileAdded {
		t.Fatalf("second commit files = %+v", got)
	}
	if got := files.Commits[1].Files; len(got) != 1 || got[0].Path != "README.md" || got[0].Additions != 1 || got[0].Deletions != 1 {
		t.Fatalf("first commit files = %+v", got)
	}

	detail, err := svc.GetPRFileAtCommit(ctx, "ao-1", 42, "", "README.md", first)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Content != "first\n" || !detail.Historical || !strings.Contains(detail.Diff, "-hello") || !strings.Contains(detail.Diff, "+first") || strings.Contains(detail.Diff, "second") {
		t.Fatalf("first commit README.md = %+v", detail)
	}
	before, err := svc.GetPRFileRevisionAtCommit(ctx, "ao-1", 42, "", "README.md", WorkspaceBlobBefore, head)
	if err != nil || before.Content != "first\n" {
		t.Fatalf("before = %#v, %v; want the first commit's content", before, err)
	}
	after, err := svc.GetPRFileRevisionAtCommit(ctx, "ao-1", 42, "", "README.md", WorkspaceBlobAfter, head)
	if err != nil || after.Content != "second\n" {
		t.Fatalf("after = %#v, %v; want the second commit's content", after, err)
	}
	added, err := svc.GetPRFileRevisionAtCommit(ctx, "ao-1", 42, "", "notes.txt", WorkspaceBlobBefore, head)
	if err != nil || added.Exists {
		t.Fatalf("added file before = %#v, %v; want a missing revision", added, err)
	}

	if _, err := svc.GetPRFileAtCommit(ctx, "ao-1", 42, "", "notes.txt", first); err == nil {
		t.Fatal("read a file the selected commit did not change")
	}
	if _, err := svc.GetPRFileRevisionAtCommit(ctx, "ao-1", 42, "", "README.md", WorkspaceBlobAfter, base); err == nil {
		t.Fatal("read a commit that is not part of the pull request")
	}
}

func TestPRCommitListStopsAtTheCapButEveryPRCommitStillOpens(t *testing.T) {
	repo := newWorkspaceRepo(t)
	runGit(t, repo, "remote", "add", "origin", "https://example.test/acme/repo.git")
	base := strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))
	fixture := make([]fixtureCommit, maxCommitLogCommits+10)
	for i := range fixture {
		fixture[i] = fixtureCommit{message: fmt.Sprintf("change %d", i), files: map[string]string{"changes.txt": fmt.Sprintf("change %d\n", i)}}
	}
	commits := importCommits(t, repo, "feature", base, fixture)
	head := commits[len(commits)-1]
	runGit(t, repo, "commit", "--allow-empty", "-m", "outside the pull request")
	outside := strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))

	st := newFakeStore()
	st.sessions["ao-1"] = domain.SessionRecord{ID: "ao-1", Metadata: domain.SessionMetadata{WorkspacePath: repo}}
	st.prs["ao-1"] = []domain.PullRequest{{Number: 42, URL: "https://example.test/acme/repo/-/merge_requests/42", Provider: "gitlab", Host: "example.test", Repo: "acme/repo", BaseSHA: base, HeadSHA: head}}
	svc := &Service{store: st}
	ctx := context.Background()

	files, err := svc.ListPRFiles(ctx, "ao-1", 42, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(files.Commits) != maxCommitLogCommits || !files.CommitsTruncated {
		t.Fatalf("listed %d commits, truncated=%v; want the newest %d and truncated", len(files.Commits), files.CommitsTruncated, maxCommitLogCommits)
	}
	if files.Commits[0].SHA != head || files.Commits[maxCommitLogCommits-1].SHA != commits[10] {
		t.Fatalf("commits run %s to %s, want newest first from %s to %s", files.Commits[0].SHA, files.Commits[maxCommitLogCommits-1].SHA, head, commits[10])
	}

	// The oldest commit is past the list's cap but still part of the PR.
	detail, err := svc.GetPRFileAtCommit(ctx, "ao-1", 42, "", "changes.txt", commits[0])
	if err != nil || detail.Content != "change 0\n" || detail.Status != WorkspaceFileAdded {
		t.Fatalf("oldest commit file = %#v, %v", detail, err)
	}
	after, err := svc.GetPRFileRevisionAtCommit(ctx, "ao-1", 42, "", "changes.txt", WorkspaceBlobAfter, commits[0])
	if err != nil || after.Content != "change 0\n" {
		t.Fatalf("oldest commit after = %#v, %v", after, err)
	}

	// Only a full SHA inside base..head reaches git, and never as an option.
	escaped := filepath.Join(t.TempDir(), "escaped")
	for _, sha := range []string{base, outside, commits[0][:12], strings.ToUpper(commits[0]), "--output=" + escaped} {
		_, err := svc.GetPRFileAtCommit(ctx, "ao-1", 42, "", "changes.txt", sha)
		var apiErr *apierr.Error
		if !errors.As(err, &apiErr) || apiErr.Code != "PR_COMMIT_NOT_FOUND" {
			t.Fatalf("commitSha %q: error = %v, want PR_COMMIT_NOT_FOUND", sha, err)
		}
	}
	if _, err := os.Stat(escaped); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a commitSha reached git as an option: %v", err)
	}
}

func TestCommitLogCapsLeaveOutWholeCommitsOnly(t *testing.T) {
	repo := newWorkspaceRepo(t)
	base := strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))
	// Sixty files with four-digit line counts make a commit's numstat output
	// about 260 bytes longer than its name-status output, so some byte caps
	// cut the numstat pass inside a commit the name-status pass read whole.
	lines := strings.Repeat("line\n", 1000)
	wide := func(name string) fixtureCommit {
		files := map[string]string{}
		for i := range 60 {
			files[fmt.Sprintf("wide/%s/%02d.txt", name, i)] = lines
		}
		return fixtureCommit{message: name, files: files}
	}
	importCommits(t, repo, "feature", base, []fixtureCommit{
		{message: "small", files: map[string]string{"small.txt": "small\n"}},
		wide("a"),
		{message: strings.Repeat("long subject ", 20), files: map[string]string{"subject.txt": "subject\n"}},
		wide("b"),
	})
	ctx := context.Background()
	revRange := base + "..feature"

	want, wantChanges, wantCounts, truncated, err := gitCommitLogChanges(ctx, repo, revRange, maxCommitLogCommits, maxCommitLogBytes)
	if err != nil || truncated || len(want) != 4 {
		t.Fatalf("uncapped log = %d commits, truncated=%v, err=%v; want all 4", len(want), truncated, err)
	}
	if got, _, _, truncated, _ := gitCommitLogChanges(ctx, repo, revRange, 4, maxCommitLogBytes); len(got) != 4 || truncated {
		t.Fatalf("a cap of exactly 4 commits = %d commits, truncated=%v", len(got), truncated)
	}
	if got, _, _, truncated, _ := gitCommitLogChanges(ctx, repo, revRange, 3, maxCommitLogBytes); len(got) != 3 || !truncated || got[0].SHA != want[0].SHA {
		t.Fatalf("a cap of 3 commits = %d commits, truncated=%v; want the newest 3", len(got), truncated)
	}

	// Every byte cap yields the newest commits, each exactly as the uncapped
	// log reads it, and flags the rest as left out.
	for limit := 1; ; limit += 50 {
		got, changes, counts, truncated, err := gitCommitLogChanges(ctx, repo, revRange, maxCommitLogCommits, limit)
		if err != nil {
			t.Fatal(err)
		}
		if truncated != (len(got) < len(want)) {
			t.Fatalf("cap %d bytes: %d of %d commits, truncated=%v", limit, len(got), len(want), truncated)
		}
		for i, commit := range got {
			sha := want[i].SHA
			if commit.SHA != sha || !reflect.DeepEqual(changes[sha], wantChanges[sha]) || !reflect.DeepEqual(counts[sha], wantCounts[sha]) {
				t.Fatalf("cap %d bytes: commit %d is not %s read whole", limit, i, sha)
			}
		}
		if !truncated {
			return
		}
	}
}

func TestGetPRFileScopesRenameMetadataToSelectedPaths(t *testing.T) {
	repo := newWorkspaceRepo(t)
	runGit(t, repo, "remote", "add", "origin", "https://example.test/acme/repo.git")
	base := strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))
	runGit(t, repo, "mv", "README.md", "RENAMED.md")
	runGit(t, repo, "commit", "-m", "rename readme")
	head := strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))
	st := newFakeStore()
	st.sessions["ao-1"] = domain.SessionRecord{ID: "ao-1", Metadata: domain.SessionMetadata{WorkspacePath: repo}}
	st.prs["ao-1"] = []domain.PullRequest{{Number: 42, URL: "https://example.test/acme/repo/-/merge_requests/42", Provider: "gitlab", Host: "example.test", Repo: "acme/repo", BaseSHA: base, HeadSHA: head}}

	previousPath := "README.md"
	detail, err := (&Service{store: st}).GetPRFile(context.Background(), "ao-1", 42, "", "RENAMED.md", &previousPath)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Status != WorkspaceFileRenamed || detail.PreviousPath != previousPath {
		t.Fatalf("detail = %#v, want rename from %q", detail, previousPath)
	}
}

func TestPRFilesRejectUnassociatedPR(t *testing.T) {
	repo := newWorkspaceRepo(t)
	st := newFakeStore()
	st.sessions["ao-1"] = domain.SessionRecord{ID: "ao-1", Metadata: domain.SessionMetadata{WorkspacePath: repo}}
	_, err := (&Service{store: st}).ListPRFiles(context.Background(), "ao-1", 99, "")
	if err == nil {
		t.Fatal("ListPRFiles succeeded for an unassociated PR")
	}
}

func TestPRFileRevisionReadsPRSidesInsteadOfWorkspace(t *testing.T) {
	repo := newWorkspaceRepo(t)
	runGit(t, repo, "remote", "add", "origin", "https://example.test/acme/repo.git")
	base := strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))
	writeWorkspaceFile(t, repo, "README.md", "pull request\n")
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "pr change")
	head := strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))
	writeWorkspaceFile(t, repo, "README.md", "workspace only\n")
	st := newFakeStore()
	st.sessions["ao-1"] = domain.SessionRecord{ID: "ao-1", Metadata: domain.SessionMetadata{WorkspacePath: repo}}
	st.prs["ao-1"] = []domain.PullRequest{{Number: 42, URL: "https://example.test/acme/repo/-/merge_requests/42", Provider: "gitlab", Host: "example.test", Repo: "acme/repo", BaseSHA: base, HeadSHA: head}}
	svc := &Service{store: st}
	after, err := svc.GetPRFileRevision(context.Background(), "ao-1", 42, "", "README.md", WorkspaceBlobAfter)
	if err != nil || after.Content != "pull request\n" {
		t.Fatalf("after = %#v, %v", after, err)
	}
	before, err := svc.GetPRFileRevision(context.Background(), "ao-1", 42, "", "README.md", WorkspaceBlobBefore)
	if err != nil {
		t.Fatal(err)
	}
	if !before.Exists || before.Content == "workspace only\n" {
		t.Fatalf("before leaked worktree: %#v", before)
	}
}

func TestPRFileRevisionRepresentsDeletedAfterSideAsMissing(t *testing.T) {
	repo := newWorkspaceRepo(t)
	runGit(t, repo, "remote", "add", "origin", "https://example.test/acme/repo.git")
	base := strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))
	runGit(t, repo, "rm", "README.md")
	runGit(t, repo, "commit", "-m", "delete readme")
	head := strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))
	st := newFakeStore()
	st.sessions["ao-1"] = domain.SessionRecord{ID: "ao-1", Metadata: domain.SessionMetadata{WorkspacePath: repo}}
	st.prs["ao-1"] = []domain.PullRequest{{Number: 42, URL: "https://example.test/acme/repo/-/merge_requests/42", Provider: "gitlab", Host: "example.test", Repo: "acme/repo", BaseSHA: base, HeadSHA: head}}

	after, err := (&Service{store: st}).GetPRFileRevision(context.Background(), "ao-1", 42, "", "README.md", WorkspaceBlobAfter)
	if err != nil {
		t.Fatal(err)
	}
	if after.Exists || after.Path != "README.md" || after.Side != WorkspaceBlobAfter {
		t.Fatalf("after = %#v, want an explicit missing revision", after)
	}
}

func TestPRFilesSelectMatchingChildRepository(t *testing.T) {
	root := t.TempDir()
	child := filepath.Join(root, "child")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, child, "init")
	runGit(t, child, "config", "user.email", "test@example.test")
	runGit(t, child, "config", "user.name", "Test")
	runGit(t, child, "remote", "add", "origin", "https://example.test/acme/child.git")
	writeWorkspaceFile(t, child, "child.txt", "base\n")
	runGit(t, child, "add", "child.txt")
	runGit(t, child, "commit", "-m", "base")
	base := strings.TrimSpace(runGit(t, child, "rev-parse", "HEAD"))
	writeWorkspaceFile(t, child, "child.txt", "pull request\n")
	runGit(t, child, "commit", "-am", "change")
	head := strings.TrimSpace(runGit(t, child, "rev-parse", "HEAD"))
	st := newFakeStore()
	st.sessions["ao-1"] = domain.SessionRecord{ID: "ao-1", Metadata: domain.SessionMetadata{WorkspacePath: root}}
	st.worktrees["ao-1"] = []domain.SessionWorktreeRecord{{RepoName: "child", WorktreePath: child}}
	st.prs["ao-1"] = []domain.PullRequest{{Number: 7, URL: "https://example.test/acme/child/-/merge_requests/7", Provider: "gitlab", Host: "example.test", Repo: "acme/child", BaseSHA: base, HeadSHA: head}}
	files, err := (&Service{store: st}).ListPRFiles(context.Background(), "ao-1", 7, "https://example.test/acme/child/-/merge_requests/7")
	if err != nil || len(files.Files) != 1 || files.Files[0].Path != "child.txt" {
		t.Fatalf("files=%#v err=%v", files, err)
	}
}

func TestPRFilesFetchBothRevisionsFromMatchingBaseRemote(t *testing.T) {
	remoteRoot := t.TempDir()
	upstream := filepath.Join(remoteRoot, "upstream.git")
	if err := os.MkdirAll(upstream, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, upstream, "init", "--bare")

	source := newWorkspaceRepo(t)
	runGit(t, source, "branch", "-M", "main")
	runGit(t, source, "switch", "-c", "feature")
	writeWorkspaceFile(t, source, "feature.txt", "feature\n")
	runGit(t, source, "add", "feature.txt")
	runGit(t, source, "commit", "-m", "feature")
	head := strings.TrimSpace(runGit(t, source, "rev-parse", "HEAD"))
	runGit(t, source, "switch", "main")
	writeWorkspaceFile(t, source, "base.txt", "base advanced\n")
	runGit(t, source, "add", "base.txt")
	runGit(t, source, "commit", "-m", "advance base")
	base := strings.TrimSpace(runGit(t, source, "rev-parse", "HEAD"))
	runGit(t, source, "remote", "add", "publish", upstream)
	runGit(t, source, "push", "publish", "main:refs/heads/main", head+":refs/pull/42/head")

	checkout := filepath.Join(remoteRoot, "checkout")
	if err := os.MkdirAll(checkout, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, checkout, "init")
	runGit(t, checkout, "remote", "add", "origin", "https://gitlab.example.com/acme/repo.git")
	runGit(t, checkout, "remote", "add", "fork", "https://github.com/fork/repo.git")
	runGit(t, checkout, "remote", "add", "upstream", "https://github.com/acme/repo.git")
	runGit(t, checkout, "config", "url.file://"+upstream+".insteadOf", "https://github.com/acme/repo.git")

	st := newFakeStore()
	st.sessions["ao-1"] = domain.SessionRecord{ID: "ao-1", Metadata: domain.SessionMetadata{WorkspacePath: checkout}}
	st.prs["ao-1"] = []domain.PullRequest{{Number: 42, URL: "https://github.com/acme/repo/pull/42", Provider: "github", Host: "github.com", Repo: "acme/repo", TargetBranch: "main", BaseSHA: base, HeadSHA: head}}

	files, err := (&Service{store: st}).ListPRFiles(context.Background(), "ao-1", 42, "https://github.com/acme/repo/pull/42")
	if err != nil {
		t.Fatal(err)
	}
	if len(files.Files) != 1 || files.Files[0].Path != "feature.txt" {
		t.Fatalf("files = %#v, want feature.txt", files.Files)
	}
	if !gitCommitExists(context.Background(), checkout, base) || !gitCommitExists(context.Background(), checkout, head) {
		t.Fatal("persisted base and head revisions were not both fetched")
	}
}
