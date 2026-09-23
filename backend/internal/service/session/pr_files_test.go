package session

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
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
