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
	base := strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))
	runGit(t, repo, "switch", "-c", "feature")
	writeWorkspaceFile(t, repo, "README.md", "pull request\n")
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "pr change")
	head := strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))
	writeWorkspaceFile(t, repo, "notes.txt", "workspace only\n")

	st := newFakeStore()
	st.sessions["ao-1"] = domain.SessionRecord{ID: "ao-1", Metadata: domain.SessionMetadata{WorkspacePath: repo}}
	st.prs["ao-1"] = []domain.PullRequest{{Number: 42, URL: "https://example.test/pr/42", SourceBranch: "feature", TargetBranch: "main", BaseSHA: base, HeadSHA: head}}
	svc := &Service{store: st}

	files, err := svc.ListPRFiles(context.Background(), "ao-1", 42)
	if err != nil {
		t.Fatal(err)
	}
	if len(files.Files) != 1 || files.Files[0].Path != "README.md" {
		t.Fatalf("files = %+v, want only README.md", files.Files)
	}
	if _, err := os.Stat(filepath.Join(repo, "notes.txt")); err != nil {
		t.Fatalf("workspace was unexpectedly changed: %v", err)
	}
	detail, err := svc.GetPRFile(context.Background(), "ao-1", 42, "README.md")
	if err != nil {
		t.Fatal(err)
	}
	if detail.Content != "pull request\n" || !strings.Contains(detail.Diff, "+pull request") {
		t.Fatalf("detail = %+v", detail)
	}
}

func TestPRFilesRejectUnassociatedPR(t *testing.T) {
	repo := newWorkspaceRepo(t)
	st := newFakeStore()
	st.sessions["ao-1"] = domain.SessionRecord{ID: "ao-1", Metadata: domain.SessionMetadata{WorkspacePath: repo}}
	_, err := (&Service{store: st}).ListPRFiles(context.Background(), "ao-1", 99)
	if err == nil {
		t.Fatal("ListPRFiles succeeded for an unassociated PR")
	}
}
