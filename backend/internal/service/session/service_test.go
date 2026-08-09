package session

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	sessionmanager "github.com/aoagents/agent-orchestrator/backend/internal/session_manager"
)

type fakeTelemetrySink struct{ events []ports.TelemetryEvent }

func (f *fakeTelemetrySink) Emit(_ context.Context, ev ports.TelemetryEvent) {
	f.events = append(f.events, ev)
}
func (f *fakeTelemetrySink) Close(context.Context) error { return nil }

type fakeStore struct {
	sessions  map[domain.SessionID]domain.SessionRecord
	pr        map[domain.SessionID]domain.PRFacts
	prs       map[domain.SessionID][]domain.PullRequest
	projects  map[string]domain.ProjectRecord
	worktrees map[domain.SessionID][]domain.SessionWorktreeRecord
	checks    map[string][]domain.PullRequestCheck
	reviews   map[string][]domain.PullRequestReview
	threads   map[string][]domain.PullRequestReviewThread
	comments  map[string][]domain.PullRequestComment
	num       int
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		sessions:  map[domain.SessionID]domain.SessionRecord{},
		pr:        map[domain.SessionID]domain.PRFacts{},
		prs:       map[domain.SessionID][]domain.PullRequest{},
		projects:  map[string]domain.ProjectRecord{},
		worktrees: map[domain.SessionID][]domain.SessionWorktreeRecord{},
		checks:    map[string][]domain.PullRequestCheck{},
		reviews:   map[string][]domain.PullRequestReview{},
		threads:   map[string][]domain.PullRequestReviewThread{},
		comments:  map[string][]domain.PullRequestComment{},
	}
}

func newWorkspaceRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init")
	runGit(t, dir, "config", "user.email", "ao@example.com")
	runGit(t, dir, "config", "user.name", "AO Tests")
	writeWorkspaceFile(t, dir, ".gitignore", "node_modules/\n")
	writeWorkspaceFile(t, dir, "README.md", "hello\n")
	writeWorkspaceFile(t, dir, "src/app.go", "package main\n")
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "initial")
	return dir
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git -C %s %s: %v\n%s", dir, strings.Join(args, " "), err, out)
	}
	return string(out)
}

func writeWorkspaceFile(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func linkWorkspaceDir(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err == nil {
		return
	} else if runtime.GOOS != "windows" {
		t.Skipf("creating symlink: %v", err)
	} else {
		cmd := exec.Command("cmd", "/c", "mklink", "/J", link, target)
		if out, junctionErr := cmd.CombinedOutput(); junctionErr != nil {
			t.Skipf("creating symlink or junction: symlink: %v; junction: %v\n%s", err, junctionErr, out)
		}
	}
}

func (f *fakeStore) CreateSession(_ context.Context, rec domain.SessionRecord) (domain.SessionRecord, error) {
	f.num++
	rec.ID = domain.SessionID(fmt.Sprintf("%s-%d", rec.ProjectID, f.num))
	f.sessions[rec.ID] = rec
	return rec, nil
}

func (f *fakeStore) GetSession(_ context.Context, id domain.SessionID) (domain.SessionRecord, bool, error) {
	r, ok := f.sessions[id]
	return r, ok, nil
}

func (f *fakeStore) ListSessions(_ context.Context, p domain.ProjectID) ([]domain.SessionRecord, error) {
	var out []domain.SessionRecord
	for _, r := range f.sessions {
		if r.ProjectID == p {
			out = append(out, r)
		}
	}
	return out, nil
}

func (f *fakeStore) ListAllSessions(_ context.Context) ([]domain.SessionRecord, error) {
	out := make([]domain.SessionRecord, 0, len(f.sessions))
	for _, r := range f.sessions {
		out = append(out, r)
	}
	return out, nil
}

func (f *fakeStore) RenameSession(_ context.Context, id domain.SessionID, displayName string, updatedAt time.Time) (bool, error) {
	r, ok := f.sessions[id]
	if !ok {
		return false, nil
	}
	r.DisplayName = displayName
	r.UpdatedAt = updatedAt
	f.sessions[id] = r
	return true, nil
}

func (f *fakeStore) SetSessionPinned(_ context.Context, id domain.SessionID, isPinned bool, pinnedAt *time.Time, updatedAt time.Time) (bool, error) {
	r, ok := f.sessions[id]
	if !ok {
		return false, nil
	}
	r.IsPinned = isPinned
	r.PinnedAt = pinnedAt
	r.UpdatedAt = updatedAt
	f.sessions[id] = r
	return true, nil
}

func (f *fakeStore) SetSessionPreviewURL(_ context.Context, id domain.SessionID, previewURL string, updatedAt time.Time) (bool, error) {
	r, ok := f.sessions[id]
	if !ok {
		return false, nil
	}
	r.Metadata.PreviewURL = previewURL
	r.UpdatedAt = updatedAt
	f.sessions[id] = r
	return true, nil
}

func (f *fakeStore) SetSessionTerminateOnPRMerge(_ context.Context, id domain.SessionID, terminate bool, updatedAt time.Time) (bool, error) {
	r, ok := f.sessions[id]
	if !ok {
		return false, nil
	}
	r.TerminateOnPRMerge = terminate
	r.UpdatedAt = updatedAt
	f.sessions[id] = r
	return true, nil
}

func (f *fakeStore) SetSessionAutoInjectReview(_ context.Context, id domain.SessionID, autoInject bool, updatedAt time.Time) (bool, error) {
	r, ok := f.sessions[id]
	if !ok {
		return false, nil
	}
	r.AutoInjectReview = autoInject
	r.UpdatedAt = updatedAt
	f.sessions[id] = r
	return true, nil
}

func (f *fakeStore) SetSessionReviewerHarness(_ context.Context, id domain.SessionID, harness domain.ReviewerHarness, updatedAt time.Time) (bool, error) {
	r, ok := f.sessions[id]
	if !ok {
		return false, nil
	}
	r.ReviewerHarness = harness
	r.UpdatedAt = updatedAt
	f.sessions[id] = r
	return true, nil
}

func (f *fakeStore) GetDisplayPRFactsForSession(_ context.Context, id domain.SessionID) (domain.PRFacts, bool, error) {
	pr, ok := f.pr[id]
	return pr, ok, nil
}

func (f *fakeStore) ListPRsBySession(_ context.Context, id domain.SessionID) ([]domain.PullRequest, error) {
	if prs, ok := f.prs[id]; ok {
		return append([]domain.PullRequest(nil), prs...), nil
	}
	pr, ok := f.pr[id]
	if !ok {
		return nil, nil
	}
	return []domain.PullRequest{{URL: pr.URL, SessionID: id, Number: pr.Number, Draft: pr.Draft, Merged: pr.Merged, Closed: pr.Closed, CI: pr.CI, Review: pr.Review, Mergeability: pr.Mergeability, UpdatedAt: pr.UpdatedAt, TargetBranch: pr.TargetBranch}}, nil
}

func (f *fakeStore) ListPRFactsForSession(_ context.Context, id domain.SessionID) ([]domain.PRFacts, error) {
	pr, ok := f.pr[id]
	if !ok {
		return nil, nil
	}
	return []domain.PRFacts{pr}, nil
}

func (f *fakeStore) ListChecks(_ context.Context, prURL string) ([]domain.PullRequestCheck, error) {
	return append([]domain.PullRequestCheck(nil), f.checks[prURL]...), nil
}

func (f *fakeStore) ListPRReviews(_ context.Context, prURL string) ([]domain.PullRequestReview, error) {
	return append([]domain.PullRequestReview(nil), f.reviews[prURL]...), nil
}

func (f *fakeStore) ListPRReviewThreads(_ context.Context, prURL string) ([]domain.PullRequestReviewThread, error) {
	return append([]domain.PullRequestReviewThread(nil), f.threads[prURL]...), nil
}

func (f *fakeStore) ListPRComments(_ context.Context, prURL string) ([]domain.PullRequestComment, error) {
	return append([]domain.PullRequestComment(nil), f.comments[prURL]...), nil
}

func (f *fakeStore) GetProject(_ context.Context, id string) (domain.ProjectRecord, bool, error) {
	p, ok := f.projects[id]
	return p, ok, nil
}

func (f *fakeStore) ListSessionWorktrees(_ context.Context, id domain.SessionID) ([]domain.SessionWorktreeRecord, error) {
	return append([]domain.SessionWorktreeRecord(nil), f.worktrees[id]...), nil
}

func TestSessionListAppliesActivityBeforePRFacts(t *testing.T) {
	for _, tt := range []struct {
		name     string
		activity domain.ActivityState
		want     domain.SessionStatus
	}{
		{"active", domain.ActivityActive, domain.StatusWorking},
		{"idle", domain.ActivityIdle, domain.StatusCIFailed},
	} {
		t.Run(tt.name, func(t *testing.T) {
			st := newFakeStore()
			st.sessions["mer-1"] = domain.SessionRecord{ID: "mer-1", ProjectID: "mer", Activity: domain.Activity{State: tt.activity}}
			st.pr["mer-1"] = domain.PRFacts{URL: "pr1", CI: domain.CIFailing}

			list, err := (&Service{store: st}).List(context.Background(), ListFilter{ProjectID: "mer"})
			if err != nil {
				t.Fatal(err)
			}
			if len(list) != 1 || list[0].Status != tt.want {
				t.Fatalf("got %+v, want status %q", list, tt.want)
			}
		})
	}
}

func TestSessionRenameUpdatesDisplayName(t *testing.T) {
	st := newFakeStore()
	st.sessions["mer-1"] = domain.SessionRecord{ID: "mer-1", ProjectID: "mer"}

	err := (&Service{store: st}).Rename(context.Background(), "mer-1", "  Fix issue #90  ")
	if err != nil {
		t.Fatal(err)
	}
	if got := st.sessions["mer-1"].DisplayName; got != "Fix issue #90" {
		t.Fatalf("display name = %q, want trimmed rename", got)
	}
}

func TestSessionPinAndUnpin(t *testing.T) {
	st := newFakeStore()
	st.sessions["mer-1"] = domain.SessionRecord{ID: "mer-1", ProjectID: "mer"}

	sess, err := (&Service{store: st, clock: time.Now}).Pin(context.Background(), "mer-1")
	if err != nil {
		t.Fatal(err)
	}
	if !sess.IsPinned || sess.PinnedAt == nil {
		t.Fatalf("pin was not persisted: session=%+v", sess)
	}
	if !st.sessions["mer-1"].IsPinned || st.sessions["mer-1"].PinnedAt == nil {
		t.Fatalf("pin was not stored: session=%+v", st.sessions["mer-1"])
	}

	sess, err = (&Service{store: st, clock: time.Now}).Unpin(context.Background(), "mer-1")
	if err != nil {
		t.Fatal(err)
	}
	if sess.IsPinned || sess.PinnedAt != nil {
		t.Fatalf("unpin was not persisted: session=%+v", sess)
	}
	if st.sessions["mer-1"].IsPinned || st.sessions["mer-1"].PinnedAt != nil {
		t.Fatalf("unpin was not stored: session=%+v", st.sessions["mer-1"])
	}
}

func TestSessionPinUnknownSession(t *testing.T) {
	if _, err := (&Service{store: newFakeStore()}).Pin(context.Background(), "ghost-1"); err == nil {
		t.Fatal("expected missing session error")
	}
}

func TestSessionUnpinUnknownSession(t *testing.T) {
	if _, err := (&Service{store: newFakeStore()}).Unpin(context.Background(), "ghost-1"); err == nil {
		t.Fatal("expected missing session error")
	}
}

func TestSessionSetPreviewPersistsURL(t *testing.T) {
	st := newFakeStore()
	st.sessions["mer-1"] = domain.SessionRecord{ID: "mer-1", ProjectID: "mer"}

	sess, err := (&Service{store: st, clock: time.Now}).SetPreview(context.Background(), "mer-1", "file:///tmp/index.html")
	if err != nil {
		t.Fatal(err)
	}
	if sess.Metadata.PreviewURL != "file:///tmp/index.html" {
		t.Fatalf("returned preview url = %q, want set value", sess.Metadata.PreviewURL)
	}
	if got := st.sessions["mer-1"].Metadata.PreviewURL; got != "file:///tmp/index.html" {
		t.Fatalf("persisted preview url = %q, want set value", got)
	}
}

func TestSessionSetPreviewUnknownSession(t *testing.T) {
	st := newFakeStore()
	if _, err := (&Service{store: st}).SetPreview(context.Background(), "ghost-1", "http://x"); err == nil {
		t.Fatal("want error for unknown session")
	}
}

func TestSessionSetTerminateOnPRMergePersistsPolicy(t *testing.T) {
	st := newFakeStore()
	st.sessions["mer-1"] = domain.SessionRecord{ID: "mer-1", ProjectID: "mer", Kind: domain.KindWorker}

	sess, err := (&Service{store: st}).SetTerminateOnPRMerge(context.Background(), "mer-1", true)
	if err != nil {
		t.Fatal(err)
	}
	if !sess.TerminateOnPRMerge || !st.sessions["mer-1"].TerminateOnPRMerge {
		t.Fatalf("terminate-on-merge policy was not persisted: session=%+v stored=%+v", sess, st.sessions["mer-1"])
	}
}

func TestSessionSetTerminateOnPRMergeUnknownSession(t *testing.T) {
	if _, err := (&Service{store: newFakeStore()}).SetTerminateOnPRMerge(context.Background(), "ghost-1", true); err == nil {
		t.Fatal("expected missing session error")
	}
}

func TestSessionSetAutoInjectReviewPersistsPolicy(t *testing.T) {
	st := newFakeStore()
	st.sessions["mer-1"] = domain.SessionRecord{ID: "mer-1", ProjectID: "mer", Kind: domain.KindWorker, AutoInjectReview: true}

	sess, err := (&Service{store: st}).SetAutoInjectReview(context.Background(), "mer-1", false)
	if err != nil {
		t.Fatal(err)
	}
	if sess.AutoInjectReview || st.sessions["mer-1"].AutoInjectReview {
		t.Fatalf("auto-inject policy was not disabled: session=%+v stored=%+v", sess, st.sessions["mer-1"])
	}
}

func TestSessionSetAutoInjectReviewUnknownSession(t *testing.T) {
	if _, err := (&Service{store: newFakeStore()}).SetAutoInjectReview(context.Background(), "ghost-1", false); err == nil {
		t.Fatal("expected missing session error")
	}
}

func TestSessionSetReviewerHarnessPersistsPerSession(t *testing.T) {
	st := newFakeStore()
	st.sessions["mer-1"] = domain.SessionRecord{ID: "mer-1", ProjectID: "mer", Kind: domain.KindWorker}
	st.sessions["mer-2"] = domain.SessionRecord{ID: "mer-2", ProjectID: "mer", Kind: domain.KindWorker}

	sess, err := (&Service{store: st}).SetReviewerHarness(context.Background(), "mer-1", domain.ReviewerOpenCode)
	if err != nil {
		t.Fatal(err)
	}
	if sess.ReviewerHarness != domain.ReviewerOpenCode || st.sessions["mer-1"].ReviewerHarness != domain.ReviewerOpenCode {
		t.Fatalf("reviewer harness was not persisted: session=%+v stored=%+v", sess, st.sessions["mer-1"])
	}
	if got := st.sessions["mer-2"].ReviewerHarness; got != "" {
		t.Fatalf("other session reviewer harness = %q, want empty", got)
	}
}

func TestSessionSetReviewerHarnessRejectsUnknownHarness(t *testing.T) {
	st := newFakeStore()
	st.sessions["mer-1"] = domain.SessionRecord{ID: "mer-1"}
	if _, err := (&Service{store: st}).SetReviewerHarness(context.Background(), "mer-1", "unknown"); err == nil {
		t.Fatal("expected invalid harness error")
	}
}

func TestListWorkspaceFilesReturnsTrackedAndUntrackedStatus(t *testing.T) {
	repo := newWorkspaceRepo(t)
	writeWorkspaceFile(t, repo, "README.md", "goodbye\nupdated\n")
	if err := os.Remove(filepath.Join(repo, "src", "app.go")); err != nil {
		t.Fatal(err)
	}
	writeWorkspaceFile(t, repo, "notes.txt", "agent note\n")
	writeWorkspaceFile(t, repo, "node_modules/cache.txt", "ignored\n")
	st := newFakeStore()
	st.sessions["ao-1"] = domain.SessionRecord{
		ID:       "ao-1",
		Metadata: domain.SessionMetadata{WorkspacePath: repo},
		Activity: domain.Activity{State: domain.ActivityActive},
	}

	got, err := (&Service{store: st}).ListWorkspaceFiles(context.Background(), "ao-1")
	if err != nil {
		t.Fatal(err)
	}
	byPath := map[string]WorkspaceFileSummary{}
	for _, file := range got.Files {
		byPath[file.Path] = file
	}
	if got.SessionID != "ao-1" {
		t.Fatalf("session id = %q, want ao-1", got.SessionID)
	}
	if byPath["README.md"].Status != WorkspaceFileModified {
		t.Fatalf("README status = %q, want modified", byPath["README.md"].Status)
	}
	if byPath["src/app.go"].Status != WorkspaceFileDeleted {
		t.Fatalf("src/app.go status = %q, want deleted", byPath["src/app.go"].Status)
	}
	if byPath["notes.txt"].Status != WorkspaceFileAdded {
		t.Fatalf("notes.txt status = %q, want added", byPath["notes.txt"].Status)
	}
	if _, ok := byPath["node_modules/cache.txt"]; ok {
		t.Fatal("ignored file was listed")
	}
	if byPath["README.md"].Additions == 0 || byPath["README.md"].Deletions == 0 {
		t.Fatalf("README counts = +%d -%d, want non-zero diff counts", byPath["README.md"].Additions, byPath["README.md"].Deletions)
	}
}

func TestGetWorkspaceFileReturnsContentAndDiff(t *testing.T) {
	repo := newWorkspaceRepo(t)
	writeWorkspaceFile(t, repo, "README.md", "goodbye\nupdated\n")
	st := newFakeStore()
	st.sessions["ao-1"] = domain.SessionRecord{ID: "ao-1", Metadata: domain.SessionMetadata{WorkspacePath: repo}}

	got, err := (&Service{store: st}).GetWorkspaceFile(context.Background(), "ao-1", "README.md")
	if err != nil {
		t.Fatal(err)
	}
	if got.Path != "README.md" {
		t.Fatalf("path = %q, want README.md", got.Path)
	}
	if got.Content != "goodbye\nupdated\n" {
		t.Fatalf("content = %q", got.Content)
	}
	if !strings.Contains(got.Diff, "-hello") || !strings.Contains(got.Diff, "+updated") {
		t.Fatalf("diff did not include expected old/new lines:\n%s", got.Diff)
	}
}

func TestWorkspaceFilesIncludeCommittedBranchDiffAgainstRecordedBase(t *testing.T) {
	repo := newWorkspaceRepo(t)
	base := strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))
	runGit(t, repo, "switch", "-c", "ao/work")
	writeWorkspaceFile(t, repo, "README.md", "hello\ncommitted change\n")
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "agent change")

	st := newFakeStore()
	st.sessions["ao-1"] = domain.SessionRecord{
		ID: "ao-1",
		Metadata: domain.SessionMetadata{
			Branch:        "ao/work",
			WorkspacePath: repo,
			DiffBaseSHA:   base,
			DiffBaseRef:   "main",
		},
	}

	files, err := (&Service{store: st}).ListWorkspaceFiles(context.Background(), "ao-1")
	if err != nil {
		t.Fatal(err)
	}
	byPath := map[string]WorkspaceFileSummary{}
	for _, file := range files.Files {
		byPath[file.Path] = file
	}
	readme := byPath["README.md"]
	if readme.Status != WorkspaceFileModified {
		t.Fatalf("README status = %q, want modified for committed branch diff", readme.Status)
	}
	if readme.Additions == 0 {
		t.Fatalf("README additions = %d, want committed branch additions", readme.Additions)
	}
	if files.CompareMode != WorkspaceCompareBase || files.CompareBaseSHA != base || files.CompareBaseRef != "main" {
		t.Fatalf("compare metadata = mode:%q sha:%q ref:%q, want base %s main", files.CompareMode, files.CompareBaseSHA, files.CompareBaseRef, base)
	}

	detail, err := (&Service{store: st}).GetWorkspaceFile(context.Background(), "ao-1", "README.md")
	if err != nil {
		t.Fatal(err)
	}
	if detail.Status != WorkspaceFileModified {
		t.Fatalf("detail status = %q, want modified", detail.Status)
	}
	if !strings.Contains(detail.Diff, "+committed change") {
		t.Fatalf("diff did not include committed branch change:\n%s", detail.Diff)
	}
	if detail.CompareMode != WorkspaceCompareBase || detail.CompareBaseSHA != base || detail.CompareBaseRef != "main" {
		t.Fatalf("detail compare metadata = mode:%q sha:%q ref:%q, want base %s main", detail.CompareMode, detail.CompareBaseSHA, detail.CompareBaseRef, base)
	}
}

func TestWorkspaceFilesRecomputesRecordedRefAfterBaseMoves(t *testing.T) {
	repo := newWorkspaceRepo(t)
	runGit(t, repo, "branch", "-M", "main")
	oldBase := strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))
	runGit(t, repo, "switch", "-c", "ao/work")
	writeWorkspaceFile(t, repo, "agent.go", "package main\n")
	runGit(t, repo, "add", "agent.go")
	runGit(t, repo, "commit", "-m", "agent change")
	runGit(t, repo, "switch", "main")
	writeWorkspaceFile(t, repo, "mainonly.go", "package main\n")
	runGit(t, repo, "add", "mainonly.go")
	runGit(t, repo, "commit", "-m", "main moved")
	newBase := strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))
	runGit(t, repo, "switch", "ao/work")
	runGit(t, repo, "rebase", "main")

	st := newFakeStore()
	st.sessions["ao-1"] = domain.SessionRecord{
		ID: "ao-1",
		Metadata: domain.SessionMetadata{
			WorkspacePath: repo,
			DiffBaseSHA:   oldBase,
			DiffBaseRef:   "main",
		},
	}

	files, err := (&Service{store: st}).ListWorkspaceFiles(context.Background(), "ao-1")
	if err != nil {
		t.Fatal(err)
	}
	if files.CompareBaseSHA != newBase {
		t.Fatalf("compare base = %q, want recomputed merge base %q", files.CompareBaseSHA, newBase)
	}
	byPath := map[string]WorkspaceFileSummary{}
	for _, file := range files.Files {
		byPath[file.Path] = file
	}
	if byPath["agent.go"].Status != WorkspaceFileAdded {
		t.Fatalf("agent.go status = %q, want added", byPath["agent.go"].Status)
	}
	if got := byPath["mainonly.go"]; got.Status != WorkspaceFileUnmodified || got.Additions != 0 || got.Deletions != 0 {
		t.Fatalf("mainonly.go = %#v, want unmodified after recomputing base", got)
	}
}

func TestWorkspaceFilesPRFallbackPrefersDefaultTargetPR(t *testing.T) {
	repo := newWorkspaceRepo(t)
	runGit(t, repo, "branch", "-M", "main")
	rootBase := strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))
	runGit(t, repo, "switch", "-c", "ao/root")
	writeWorkspaceFile(t, repo, "lower.go", "package main\n")
	runGit(t, repo, "add", "lower.go")
	runGit(t, repo, "commit", "-m", "lower stack change")
	childBase := strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))
	writeWorkspaceFile(t, repo, "upper.go", "package main\n")
	runGit(t, repo, "add", "upper.go")
	runGit(t, repo, "commit", "-m", "upper stack change")

	st := newFakeStore()
	st.projects["mer"] = domain.ProjectRecord{ID: "mer", Config: domain.ProjectConfig{DefaultBranch: "main"}}
	st.sessions["ao-1"] = domain.SessionRecord{
		ID:        "ao-1",
		ProjectID: "mer",
		Metadata:  domain.SessionMetadata{WorkspacePath: repo},
	}
	st.prs["ao-1"] = []domain.PullRequest{
		{URL: "child", SessionID: "ao-1", Number: 2, TargetBranch: "ao/root", BaseSHA: childBase, UpdatedAt: time.Unix(200, 0)},
		{URL: "root", SessionID: "ao-1", Number: 1, TargetBranch: "main", BaseSHA: rootBase, UpdatedAt: time.Unix(100, 0)},
	}

	files, err := (&Service{store: st}).ListWorkspaceFiles(context.Background(), "ao-1")
	if err != nil {
		t.Fatal(err)
	}
	if files.CompareBaseSHA != rootBase || files.CompareBaseRef != "main" {
		t.Fatalf("compare base = sha:%q ref:%q, want root PR %s main", files.CompareBaseSHA, files.CompareBaseRef, rootBase)
	}
	byPath := map[string]WorkspaceFileSummary{}
	for _, file := range files.Files {
		byPath[file.Path] = file
	}
	if byPath["lower.go"].Status != WorkspaceFileAdded || byPath["upper.go"].Status != WorkspaceFileAdded {
		t.Fatalf("stack files = lower:%#v upper:%#v, want both visible from root PR base", byPath["lower.go"], byPath["upper.go"])
	}
}

func TestWorkspaceFilesReportCommittedDeletionsAgainstRecordedBase(t *testing.T) {
	repo := newWorkspaceRepo(t)
	base := strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))
	runGit(t, repo, "switch", "-c", "ao/work")
	runGit(t, repo, "rm", "src/app.go")
	runGit(t, repo, "commit", "-m", "delete app")

	st := newFakeStore()
	st.sessions["ao-1"] = domain.SessionRecord{
		ID:       "ao-1",
		Metadata: domain.SessionMetadata{WorkspacePath: repo, DiffBaseSHA: base, DiffBaseRef: "main"},
	}

	files, err := (&Service{store: st}).ListWorkspaceFiles(context.Background(), "ao-1")
	if err != nil {
		t.Fatal(err)
	}
	byPath := map[string]WorkspaceFileSummary{}
	for _, file := range files.Files {
		byPath[file.Path] = file
	}
	if byPath["src/app.go"].Status != WorkspaceFileDeleted {
		t.Fatalf("src/app.go summary = %#v, want deleted", byPath["src/app.go"])
	}

	detail, err := (&Service{store: st}).GetWorkspaceFile(context.Background(), "ao-1", "src/app.go")
	if err != nil {
		t.Fatal(err)
	}
	if !detail.Deleted || detail.Status != WorkspaceFileDeleted || !strings.Contains(detail.Diff, "deleted file mode") {
		t.Fatalf("deleted detail = %#v diff:\n%s", detail, detail.Diff)
	}
}

func TestWorkspaceFilesKeepBaseStatusWhenCommittedAddedFileIsModified(t *testing.T) {
	repo := newWorkspaceRepo(t)
	base := strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))
	runGit(t, repo, "switch", "-c", "ao/work")
	writeWorkspaceFile(t, repo, "new.go", "package main\n")
	runGit(t, repo, "add", "new.go")
	runGit(t, repo, "commit", "-m", "add file")
	writeWorkspaceFile(t, repo, "new.go", "package main\n\nfunc Later() {}\n")

	st := newFakeStore()
	st.sessions["ao-1"] = domain.SessionRecord{
		ID:       "ao-1",
		Metadata: domain.SessionMetadata{WorkspacePath: repo, DiffBaseSHA: base, DiffBaseRef: "main"},
	}

	files, err := (&Service{store: st}).ListWorkspaceFiles(context.Background(), "ao-1")
	if err != nil {
		t.Fatal(err)
	}
	byPath := map[string]WorkspaceFileSummary{}
	for _, file := range files.Files {
		byPath[file.Path] = file
	}
	if byPath["new.go"].Status != WorkspaceFileAdded {
		t.Fatalf("new.go status = %q, want added from base diff despite HEAD status", byPath["new.go"].Status)
	}
}

func TestWorkspaceFilesReportRenamesAgainstRecordedBase(t *testing.T) {
	repo := newWorkspaceRepo(t)
	base := strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))
	runGit(t, repo, "switch", "-c", "ao/work")
	runGit(t, repo, "mv", "src/app.go", "src/main.go")
	runGit(t, repo, "commit", "-m", "rename app")

	st := newFakeStore()
	st.sessions["ao-1"] = domain.SessionRecord{
		ID: "ao-1",
		Metadata: domain.SessionMetadata{
			WorkspacePath: repo,
			DiffBaseSHA:   base,
			DiffBaseRef:   "main",
		},
	}

	files, err := (&Service{store: st}).ListWorkspaceFiles(context.Background(), "ao-1")
	if err != nil {
		t.Fatal(err)
	}
	var renamed WorkspaceFileSummary
	for _, file := range files.Files {
		if file.Path == "src/main.go" {
			renamed = file
			break
		}
	}
	if renamed.Status != WorkspaceFileRenamed || renamed.PreviousPath != "src/app.go" {
		t.Fatalf("renamed summary = %#v, want R src/app.go -> src/main.go", renamed)
	}

	detail, err := (&Service{store: st}).GetWorkspaceFile(context.Background(), "ao-1", "src/main.go")
	if err != nil {
		t.Fatal(err)
	}
	if detail.Status != WorkspaceFileRenamed || detail.PreviousPath != "src/app.go" {
		t.Fatalf("renamed detail = %#v, want previous path src/app.go", detail)
	}
	if !strings.Contains(detail.Diff, "rename from src/app.go") || !strings.Contains(detail.Diff, "rename to src/main.go") {
		t.Fatalf("rename diff missing rename headers:\n%s", detail.Diff)
	}
}

func TestWorkspaceFilesIncludeWorkspaceProjectChildRepoDiffs(t *testing.T) {
	root := newWorkspaceRepo(t)
	rootBase := strings.TrimSpace(runGit(t, root, "rev-parse", "HEAD"))
	child := filepath.Join(root, "api")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, child, "init")
	runGit(t, child, "config", "user.email", "ao@example.com")
	runGit(t, child, "config", "user.name", "AO Tests")
	writeWorkspaceFile(t, child, "service.go", "package api\n")
	runGit(t, child, "add", ".")
	runGit(t, child, "commit", "-m", "initial child")
	childBase := strings.TrimSpace(runGit(t, child, "rev-parse", "HEAD"))
	runGit(t, child, "switch", "-c", "ao/work")
	writeWorkspaceFile(t, child, "service.go", "package api\n\nfunc Added() {}\n")
	runGit(t, child, "add", "service.go")
	runGit(t, child, "commit", "-m", "child change")

	st := newFakeStore()
	st.projects["ws"] = domain.ProjectRecord{ID: "ws", Kind: domain.ProjectKindWorkspace}
	st.sessions["ws-1"] = domain.SessionRecord{
		ID:        "ws-1",
		ProjectID: "ws",
		Metadata:  domain.SessionMetadata{WorkspacePath: root},
	}
	st.worktrees["ws-1"] = []domain.SessionWorktreeRecord{
		{SessionID: "ws-1", RepoName: domain.RootWorkspaceRepoName, WorktreePath: root, BaseSHA: rootBase},
		{SessionID: "ws-1", RepoName: "api", WorktreePath: child, BaseSHA: childBase},
	}

	svc := &Service{store: st}
	paths, err := svc.WorkspaceWatchPaths(context.Background(), "ws-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 2 || paths[0] != root || paths[1] != child {
		t.Fatalf("workspace watch paths = %v, want root and child repo", paths)
	}

	files, err := svc.ListWorkspaceFiles(context.Background(), "ws-1")
	if err != nil {
		t.Fatal(err)
	}
	byPath := map[string]WorkspaceFileSummary{}
	for _, file := range files.Files {
		byPath[file.Path] = file
	}
	childFile := byPath["api/service.go"]
	if childFile.Status != WorkspaceFileModified || childFile.Additions == 0 {
		t.Fatalf("child repo file = %#v, want modified with additions", childFile)
	}
	if _, ok := byPath["api/.git/HEAD"]; ok {
		t.Fatal("child .git internals must not be listed through the workspace root")
	}

	detail, err := svc.GetWorkspaceFile(context.Background(), "ws-1", "api/service.go")
	if err != nil {
		t.Fatal(err)
	}
	if detail.Path != "api/service.go" || !strings.Contains(detail.Diff, "+func Added() {}") {
		t.Fatalf("child repo detail = %#v diff:\n%s", detail, detail.Diff)
	}
	if detail.CompareMode != WorkspaceCompareBase || detail.CompareBaseSHA != childBase {
		t.Fatalf("child detail compare = mode:%q sha:%q, want base %s", detail.CompareMode, detail.CompareBaseSHA, childBase)
	}
	if detail.CompareBaseRef != "main" {
		t.Fatalf("child detail compare ref = %q, want main", detail.CompareBaseRef)
	}
}

func TestWorkspaceProjectChildRepoRecomputesBaseAfterRebase(t *testing.T) {
	root := newWorkspaceRepo(t)
	rootBase := strings.TrimSpace(runGit(t, root, "rev-parse", "HEAD"))
	child := filepath.Join(root, "api")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, child, "init")
	runGit(t, child, "config", "user.email", "ao@example.com")
	runGit(t, child, "config", "user.name", "AO Tests")
	writeWorkspaceFile(t, child, "service.go", "package api\n")
	runGit(t, child, "add", ".")
	runGit(t, child, "commit", "-m", "initial child")
	runGit(t, child, "branch", "-M", "main")
	oldChildBase := strings.TrimSpace(runGit(t, child, "rev-parse", "HEAD"))
	runGit(t, child, "switch", "-c", "ao/work")
	writeWorkspaceFile(t, child, "agent.go", "package api\n\nfunc Agent() {}\n")
	runGit(t, child, "add", "agent.go")
	runGit(t, child, "commit", "-m", "agent change")
	runGit(t, child, "switch", "main")
	writeWorkspaceFile(t, child, "baseonly.go", "package api\n\nfunc BaseOnly() {}\n")
	runGit(t, child, "add", "baseonly.go")
	runGit(t, child, "commit", "-m", "base moved")
	newChildBase := strings.TrimSpace(runGit(t, child, "rev-parse", "HEAD"))
	runGit(t, child, "switch", "ao/work")
	runGit(t, child, "rebase", "main")

	st := newFakeStore()
	st.projects["ws"] = domain.ProjectRecord{ID: "ws", Kind: domain.ProjectKindWorkspace, Config: domain.ProjectConfig{DefaultBranch: "main"}}
	st.sessions["ws-1"] = domain.SessionRecord{
		ID:        "ws-1",
		ProjectID: "ws",
		Metadata:  domain.SessionMetadata{WorkspacePath: root},
	}
	st.worktrees["ws-1"] = []domain.SessionWorktreeRecord{
		{SessionID: "ws-1", RepoName: domain.RootWorkspaceRepoName, WorktreePath: root, BaseSHA: rootBase},
		{SessionID: "ws-1", RepoName: "api", WorktreePath: child, BaseSHA: oldChildBase},
	}

	files, err := (&Service{store: st}).ListWorkspaceFiles(context.Background(), "ws-1")
	if err != nil {
		t.Fatal(err)
	}
	byPath := map[string]WorkspaceFileSummary{}
	for _, file := range files.Files {
		byPath[file.Path] = file
	}
	if byPath["api/agent.go"].Status != WorkspaceFileAdded {
		t.Fatalf("api/agent.go status = %q, want added", byPath["api/agent.go"].Status)
	}
	if got := byPath["api/baseonly.go"]; got.Status != WorkspaceFileUnmodified || got.Additions != 0 || got.Deletions != 0 {
		t.Fatalf("api/baseonly.go = %#v, want unmodified after recomputing child base", got)
	}

	detail, err := (&Service{store: st}).GetWorkspaceFile(context.Background(), "ws-1", "api/agent.go")
	if err != nil {
		t.Fatal(err)
	}
	if detail.CompareMode != WorkspaceCompareBase || detail.CompareBaseSHA != newChildBase || detail.CompareBaseRef != "main" {
		t.Fatalf("child detail compare = mode:%q sha:%q ref:%q, want base %s main", detail.CompareMode, detail.CompareBaseSHA, detail.CompareBaseRef, newChildBase)
	}
}

func TestWorkspaceProjectCompareModeStaysBaseWithPartialFallback(t *testing.T) {
	root := newWorkspaceRepo(t)
	rootBase := strings.TrimSpace(runGit(t, root, "rev-parse", "HEAD"))
	runGit(t, root, "switch", "-c", "ao/work")
	writeWorkspaceFile(t, root, "root.go", "package main\n")
	runGit(t, root, "add", "root.go")
	runGit(t, root, "commit", "-m", "root change")
	child := filepath.Join(root, "api")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, child, "init")
	runGit(t, child, "config", "user.email", "ao@example.com")
	runGit(t, child, "config", "user.name", "AO Tests")
	writeWorkspaceFile(t, child, "scratch.go", "package api\n")
	runGit(t, child, "add", ".")
	runGit(t, child, "commit", "-m", "initial child")
	writeWorkspaceFile(t, child, "scratch.go", "package api\n\nfunc Dirty() {}\n")

	st := newFakeStore()
	st.projects["ws"] = domain.ProjectRecord{ID: "ws", Kind: domain.ProjectKindWorkspace, Config: domain.ProjectConfig{DefaultBranch: "missing-main"}}
	st.sessions["ws-1"] = domain.SessionRecord{
		ID:        "ws-1",
		ProjectID: "ws",
		Metadata:  domain.SessionMetadata{WorkspacePath: root},
	}
	st.worktrees["ws-1"] = []domain.SessionWorktreeRecord{
		{SessionID: "ws-1", RepoName: domain.RootWorkspaceRepoName, WorktreePath: root, BaseSHA: rootBase},
		{SessionID: "ws-1", RepoName: "api", WorktreePath: child, BaseSHA: "missing-base"},
	}

	files, err := (&Service{store: st}).ListWorkspaceFiles(context.Background(), "ws-1")
	if err != nil {
		t.Fatal(err)
	}
	if files.CompareMode != WorkspaceCompareBase {
		t.Fatalf("workspace compare mode = %q, want base when at least one repo resolved", files.CompareMode)
	}
	if files.CompareBaseSHA != "" {
		t.Fatalf("workspace compare sha = %q, want empty for mixed repo bases", files.CompareBaseSHA)
	}
}

func TestAppendWorkspaceFilesWithCapEnforcesGlobalLimit(t *testing.T) {
	existing := make([]WorkspaceFileSummary, maxWorkspaceFiles-1)
	added := []WorkspaceFileSummary{{Path: "one.go"}, {Path: "two.go"}}

	got, truncated := appendWorkspaceFilesWithCap(existing, added, false)
	if len(got) != maxWorkspaceFiles || !truncated {
		t.Fatalf("len,truncated = %d,%v; want %d,true", len(got), truncated, maxWorkspaceFiles)
	}
	if got[len(got)-1].Path != "one.go" {
		t.Fatalf("last appended path = %q, want one.go", got[len(got)-1].Path)
	}
}

func TestWorkspaceBaseRefCandidatesPreferRemoteDefault(t *testing.T) {
	got := workspaceBaseRefCandidates("main")
	want := []string{"origin/main", "refs/remotes/origin/main", "main"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("workspace base candidates = %#v, want %#v", got, want)
	}

	got = workspaceBaseRefCandidates("")
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("empty workspace base candidates = %#v, want %#v", got, want)
	}
}

func TestListWorkspaceFilesScratchUsesFilesystem(t *testing.T) {
	root := t.TempDir()
	writeWorkspaceFile(t, root, "README.md", "one\ntwo\n")
	writeWorkspaceFile(t, root, ".env", "TOKEN=secret\n")
	writeWorkspaceFile(t, root, "nested/task.txt", "do it")
	writeWorkspaceFile(t, root, ".git/config", "[core]\n")
	if err := os.WriteFile(filepath.Join(root, "image.bin"), []byte{0, 1, 2, 3}, 0o644); err != nil {
		t.Fatal(err)
	}
	st := newFakeStore()
	st.projects["scratch"] = domain.ProjectRecord{ID: "scratch", Kind: domain.ProjectKindScratch}
	st.sessions["scratch-1"] = domain.SessionRecord{
		ID:        "scratch-1",
		ProjectID: "scratch",
		Metadata:  domain.SessionMetadata{WorkspacePath: root},
	}

	got, err := (&Service{store: st}).ListWorkspaceFiles(context.Background(), "scratch-1")
	if err != nil {
		t.Fatal(err)
	}
	byPath := map[string]WorkspaceFileSummary{}
	var paths []string
	for _, file := range got.Files {
		paths = append(paths, file.Path)
		byPath[file.Path] = file
		if file.Status != WorkspaceFileAdded {
			t.Fatalf("%s status = %q, want added", file.Path, file.Status)
		}
	}
	wantPaths := []string{".env", "README.md", "image.bin", "nested/task.txt"}
	if strings.Join(paths, "|") != strings.Join(wantPaths, "|") {
		t.Fatalf("paths = %#v, want %#v", paths, wantPaths)
	}
	if byPath["README.md"].Additions != 0 || byPath["README.md"].Deletions != 0 {
		t.Fatalf("README counts = +%d -%d, want +0 -0", byPath["README.md"].Additions, byPath["README.md"].Deletions)
	}
	if byPath["nested/task.txt"].Additions != 0 {
		t.Fatalf("nested/task.txt additions = %d, want 0", byPath["nested/task.txt"].Additions)
	}
	if !byPath["image.bin"].Binary || byPath["image.bin"].Additions != 0 || byPath["image.bin"].Deletions != 0 {
		t.Fatalf("binary summary = %#v, want binary with zero counts", byPath["image.bin"])
	}
	if _, ok := byPath[".git/config"]; ok {
		t.Fatal(".git content should not be listed for scratch")
	}
}

func TestListWorkspaceFilesScratchStopsAtFileLimit(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < maxWorkspaceFiles+1; i++ {
		writeWorkspaceFile(t, root, fmt.Sprintf("file-%05d.txt", i), "content\n")
	}
	st := newFakeStore()
	st.projects["scratch"] = domain.ProjectRecord{ID: "scratch", Kind: domain.ProjectKindScratch}
	st.sessions["scratch-1"] = domain.SessionRecord{
		ID:        "scratch-1",
		ProjectID: "scratch",
		Metadata:  domain.SessionMetadata{WorkspacePath: root},
	}

	got, err := (&Service{store: st}).ListWorkspaceFiles(context.Background(), "scratch-1")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Truncated {
		t.Fatal("Truncated = false, want true")
	}
	if len(got.Files) != maxWorkspaceFiles {
		t.Fatalf("file count = %d, want %d", len(got.Files), maxWorkspaceFiles)
	}
}

func TestListWorkspaceFilesScratchSkipsSymlinkEscapes(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	writeWorkspaceFile(t, root, "real.txt", "inside\n")
	writeWorkspaceFile(t, outside, "secret.txt", "outside\n")
	if err := os.Symlink(filepath.Join(root, "real.txt"), filepath.Join(root, "inside-link.txt")); err != nil {
		t.Skipf("creating inside symlink: %v", err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(root, "outside-link.txt")); err != nil {
		t.Skipf("creating outside symlink: %v", err)
	}
	st := newFakeStore()
	st.projects["scratch"] = domain.ProjectRecord{ID: "scratch", Kind: domain.ProjectKindScratch}
	st.sessions["scratch-1"] = domain.SessionRecord{
		ID:        "scratch-1",
		ProjectID: "scratch",
		Metadata:  domain.SessionMetadata{WorkspacePath: root},
	}

	got, err := (&Service{store: st}).ListWorkspaceFiles(context.Background(), "scratch-1")
	if err != nil {
		t.Fatal(err)
	}
	byPath := map[string]WorkspaceFileSummary{}
	for _, file := range got.Files {
		byPath[file.Path] = file
	}
	if _, ok := byPath["inside-link.txt"]; !ok {
		t.Fatalf("inside symlink was not listed: %#v", got.Files)
	}
	if _, ok := byPath["outside-link.txt"]; ok {
		t.Fatalf("outside symlink escape was listed: %#v", got.Files)
	}
}

func TestGetWorkspaceFileScratchReturnsContentWithEmptyDiff(t *testing.T) {
	root := t.TempDir()
	writeWorkspaceFile(t, root, "README.md", "one\ntwo\n")
	st := newFakeStore()
	st.projects["scratch"] = domain.ProjectRecord{ID: "scratch", Kind: domain.ProjectKindScratch}
	st.sessions["scratch-1"] = domain.SessionRecord{
		ID:        "scratch-1",
		ProjectID: "scratch",
		Metadata:  domain.SessionMetadata{WorkspacePath: root},
	}

	got, err := (&Service{store: st}).GetWorkspaceFile(context.Background(), "scratch-1", "README.md")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != WorkspaceFileAdded {
		t.Fatalf("status = %q, want added", got.Status)
	}
	if got.Content != "one\ntwo\n" {
		t.Fatalf("content = %q", got.Content)
	}
	if got.Additions != 2 || got.Deletions != 0 {
		t.Fatalf("counts = +%d -%d, want +2 -0", got.Additions, got.Deletions)
	}
	if got.Diff != "" || got.DiffTruncated {
		t.Fatalf("scratch diff = %q truncated=%v, want empty", got.Diff, got.DiffTruncated)
	}
}

func TestGetWorkspaceFileRejectsTraversal(t *testing.T) {
	repo := newWorkspaceRepo(t)
	st := newFakeStore()
	st.sessions["ao-1"] = domain.SessionRecord{ID: "ao-1", Metadata: domain.SessionMetadata{WorkspacePath: repo}}

	_, err := (&Service{store: st}).GetWorkspaceFile(context.Background(), "ao-1", "../secrets.txt")
	var e *apierr.Error
	if !errors.As(err, &e) || e.Kind != apierr.KindInvalid || e.Code != "INVALID_WORKSPACE_PATH" {
		t.Fatalf("err = %v, want bad request INVALID_WORKSPACE_PATH", err)
	}
}

func TestGetWorkspaceFileRejectsIntermediateSymlinkEscape(t *testing.T) {
	repo := newWorkspaceRepo(t)
	outside := t.TempDir()
	writeWorkspaceFile(t, outside, "secret.txt", "outside workspace\n")
	linkWorkspaceDir(t, outside, filepath.Join(repo, "link"))
	st := newFakeStore()
	st.sessions["ao-1"] = domain.SessionRecord{ID: "ao-1", Metadata: domain.SessionMetadata{WorkspacePath: repo}}

	_, err := (&Service{store: st}).GetWorkspaceFile(context.Background(), "ao-1", "link/secret.txt")
	var e *apierr.Error
	if !errors.As(err, &e) || e.Kind != apierr.KindInvalid || e.Code != "INVALID_WORKSPACE_PATH" {
		t.Fatalf("err = %v, want bad request INVALID_WORKSPACE_PATH", err)
	}
}

func TestSessionRenameMissingSessionReturnsNotFound(t *testing.T) {
	st := newFakeStore()

	err := (&Service{store: st}).Rename(context.Background(), "mer-404", "Missing")
	var e *apierr.Error
	if !errors.As(err, &e) || e.Kind != apierr.KindNotFound || e.Code != "SESSION_NOT_FOUND" {
		t.Fatalf("err = %v, want apierr NotFound SESSION_NOT_FOUND", err)
	}
}

// fakeCommander records Kill/Spawn calls so a test can assert the
// clean-orchestrator ordering without wiring a real session engine.
type fakeCommander struct {
	killed          []domain.SessionID
	retired         []domain.SessionID
	resumed         []domain.SessionID
	ready           []domain.SessionID
	sent            []domain.SessionID
	sentMessages    []string
	cleanupProjects []domain.ProjectID
	killErr         error
	retireErr       error
	sendErr         error
	sendFunc        func(domain.SessionID, string) error
	cleanupErr      error
	spawnErr        error
	spawnRecord     domain.SessionRecord
	spawnFunc       func(ports.SpawnConfig) domain.SessionRecord
	spawnCalls      int
	spawned         bool
	spawnedCfg      ports.SpawnConfig
	killsAtSpawn    int
	restoreErr      error
	restoreResult   sessionmanager.RestoreResult
	readyErr        error
}

func (f *fakeCommander) Spawn(_ context.Context, cfg ports.SpawnConfig) (domain.SessionRecord, int, int, error) {
	if f.spawnErr != nil {
		return domain.SessionRecord{}, 0, 0, f.spawnErr
	}
	f.spawned = true
	f.spawnCalls++
	f.spawnedCfg = cfg
	f.killsAtSpawn = len(f.retired)
	if f.spawnFunc != nil {
		return f.spawnFunc(cfg), len(cfg.Prompt), 0, nil
	}
	if f.spawnRecord.ID != "" {
		return f.spawnRecord, len(cfg.Prompt), 0, nil
	}
	return domain.SessionRecord{ID: "mer-9", ProjectID: cfg.ProjectID, Kind: cfg.Kind, Harness: cfg.Harness}, len(cfg.Prompt), 0, nil
}
func (f *fakeCommander) RestoreWithMode(context.Context, domain.SessionID) (sessionmanager.RestoreResult, error) {
	if f.restoreErr != nil {
		return sessionmanager.RestoreResult{}, f.restoreErr
	}
	return f.restoreResult, nil
}
func (f *fakeCommander) ResumeAgentWithMode(_ context.Context, id domain.SessionID) (sessionmanager.RestoreResult, error) {
	f.resumed = append(f.resumed, id)
	if f.restoreErr != nil {
		return sessionmanager.RestoreResult{}, f.restoreErr
	}
	return f.restoreResult, nil
}
func (f *fakeCommander) Kill(_ context.Context, id domain.SessionID) (bool, error) {
	if f.killErr != nil {
		return false, f.killErr
	}
	f.killed = append(f.killed, id)
	return true, nil
}
func (f *fakeCommander) RetireForReplacement(_ context.Context, id domain.SessionID) error {
	if f.retireErr != nil {
		return f.retireErr
	}
	f.retired = append(f.retired, id)
	return nil
}
func (f *fakeCommander) WaitForMessageDeliveryReady(_ context.Context, id domain.SessionID) error {
	f.ready = append(f.ready, id)
	return f.readyErr
}
func (f *fakeCommander) Send(_ context.Context, id domain.SessionID, message string) error {
	if f.sendFunc != nil {
		return f.sendFunc(id, message)
	}
	if f.sendErr != nil {
		return f.sendErr
	}
	f.sent = append(f.sent, id)
	f.sentMessages = append(f.sentMessages, message)
	return nil
}
func (f *fakeCommander) Cleanup(_ context.Context, project domain.ProjectID) (sessionmanager.CleanupResult, error) {
	f.cleanupProjects = append(f.cleanupProjects, project)
	if f.cleanupErr != nil {
		return sessionmanager.CleanupResult{}, f.cleanupErr
	}
	return sessionmanager.CleanupResult{
		Cleaned: []domain.SessionID{"mer-1"},
		Skipped: []sessionmanager.CleanupSkip{{SessionID: "mer-2", Reason: "workspace has uncommitted changes"}},
	}, nil
}
func (f *fakeCommander) RollbackSpawn(context.Context, domain.SessionID) (bool, bool, error) {
	return false, false, nil
}

func (f *fakeCommander) StageAttachments(
	context.Context,
	domain.SessionID,
	[]ports.SpawnAttachment,
) ([]string, error) {
	return nil, nil
}

// TestCleanupMapsManagerResult: the service forwards both reclaimed and
// skipped sessions, with non-nil slices so the wire shape stays stable.
func TestCleanupMapsManagerResult(t *testing.T) {
	svc := &Service{manager: &fakeCommander{}}
	out, err := svc.Cleanup(context.Background(), "mer")
	if err != nil {
		t.Fatalf("Cleanup: %v", err)
	}
	if len(out.Cleaned) != 1 || out.Cleaned[0] != "mer-1" {
		t.Fatalf("cleaned = %#v", out.Cleaned)
	}
	if len(out.Skipped) != 1 || out.Skipped[0].SessionID != "mer-2" || out.Skipped[0].Reason != "workspace has uncommitted changes" {
		t.Fatalf("skipped = %#v", out.Skipped)
	}
}

func TestTeardownProjectKillsActiveSessionsThenCleansProject(t *testing.T) {
	st := newFakeStore()
	st.sessions["mer-1"] = domain.SessionRecord{ID: "mer-1", ProjectID: "mer"}
	st.sessions["mer-2"] = domain.SessionRecord{ID: "mer-2", ProjectID: "mer", IsTerminated: true}
	st.sessions["other-1"] = domain.SessionRecord{ID: "other-1", ProjectID: "other"}
	fc := &fakeCommander{}
	svc := &Service{manager: fc, store: st}

	if err := svc.TeardownProject(context.Background(), "mer"); err != nil {
		t.Fatalf("TeardownProject: %v", err)
	}
	if len(fc.killed) != 1 || fc.killed[0] != "mer-1" {
		t.Fatalf("killed = %#v, want only mer-1", fc.killed)
	}
	if len(fc.cleanupProjects) != 1 || fc.cleanupProjects[0] != "mer" {
		t.Fatalf("cleanup projects = %#v, want [mer]", fc.cleanupProjects)
	}
}

func TestTeardownProjectStopsOnKillError(t *testing.T) {
	st := newFakeStore()
	st.sessions["mer-1"] = domain.SessionRecord{ID: "mer-1", ProjectID: "mer"}
	boom := errors.New("boom")
	fc := &fakeCommander{killErr: boom}
	svc := &Service{manager: fc, store: st}

	err := svc.TeardownProject(context.Background(), "mer")
	if !errors.Is(err, boom) {
		t.Fatalf("TeardownProject err = %v, want boom", err)
	}
	if len(fc.cleanupProjects) != 0 {
		t.Fatalf("cleanup projects = %#v, want none after kill failure", fc.cleanupProjects)
	}
}

func TestSpawnOrchestratorCleanRetiresActiveOrchestratorsBeforeSpawn(t *testing.T) {
	st := newFakeStore()
	st.projects["mer"] = domain.ProjectRecord{ID: "mer"}
	// Two active orchestrators plus an unrelated worker and a terminated
	// orchestrator that must be left alone.
	st.sessions["mer-1"] = domain.SessionRecord{ID: "mer-1", ProjectID: "mer", Kind: domain.KindOrchestrator}
	st.sessions["mer-2"] = domain.SessionRecord{ID: "mer-2", ProjectID: "mer", Kind: domain.KindOrchestrator}
	st.sessions["mer-3"] = domain.SessionRecord{ID: "mer-3", ProjectID: "mer", Kind: domain.KindWorker}
	st.sessions["mer-4"] = domain.SessionRecord{ID: "mer-4", ProjectID: "mer", Kind: domain.KindOrchestrator, IsTerminated: true}

	fc := &fakeCommander{}
	svc := &Service{manager: fc, store: st}

	if _, err := svc.SpawnOrchestrator(context.Background(), "mer", true, ""); err != nil {
		t.Fatalf("SpawnOrchestrator: %v", err)
	}

	if len(fc.retired) != 2 {
		t.Fatalf("retired = %v, want the two active orchestrators", fc.retired)
	}
	if len(fc.sent) != 2 {
		t.Fatalf("retire notices = %v, want the two active orchestrators", fc.sent)
	}
	if !fc.spawned || fc.killsAtSpawn != 2 {
		t.Fatalf("spawn must run after both retirements: spawned=%v retirementsAtSpawn=%d", fc.spawned, fc.killsAtSpawn)
	}
	if len(fc.killed) != 0 {
		t.Fatalf("interactive Kill must not be used for replacement: killed=%v", fc.killed)
	}
}

func TestSpawnOrchestratorCleanContinuesWhenRetireNoticeFails(t *testing.T) {
	st := newFakeStore()
	st.projects["mer"] = domain.ProjectRecord{ID: "mer"}
	st.sessions["mer-1"] = domain.SessionRecord{ID: "mer-1", ProjectID: "mer", Kind: domain.KindOrchestrator}
	fc := &fakeCommander{sendErr: errors.New("pane closed")}
	svc := &Service{manager: fc, store: st}

	if _, err := svc.SpawnOrchestrator(context.Background(), "mer", true, ""); err != nil {
		t.Fatalf("SpawnOrchestrator: %v", err)
	}
	if len(fc.retired) != 1 || fc.retired[0] != "mer-1" {
		t.Fatalf("retired = %v, want mer-1 despite retire notice failure", fc.retired)
	}
	if !fc.spawned {
		t.Fatal("replacement should still spawn when retire notice delivery fails")
	}
}

func TestSpawnOrchestratorCleanPreservesPersistedMode(t *testing.T) {
	st := newFakeStore()
	st.projects["mer"] = domain.ProjectRecord{ID: "mer"}
	st.sessions["mer-1"] = domain.SessionRecord{
		ID: "mer-1", ProjectID: "mer", Kind: domain.KindOrchestrator,
		Mode: domain.SessionModeChat, CreatedAt: time.Unix(100, 0).UTC(),
	}
	fc := &fakeCommander{}
	svc := &Service{manager: fc, store: st}

	if _, err := svc.SpawnOrchestrator(context.Background(), "mer", true, ""); err != nil {
		t.Fatalf("SpawnOrchestrator: %v", err)
	}
	if fc.spawnedCfg.RequestedMode != domain.SessionModeChat {
		t.Fatalf("replacement mode = %q, want persisted chat", fc.spawnedCfg.RequestedMode)
	}
}

func TestSpawnOrchestratorCleanHonorsExplicitReplacementMode(t *testing.T) {
	st := newFakeStore()
	st.projects["mer"] = domain.ProjectRecord{ID: "mer"}
	st.sessions["mer-1"] = domain.SessionRecord{
		ID: "mer-1", ProjectID: "mer", Kind: domain.KindOrchestrator,
		Mode: domain.SessionModeTUI, CreatedAt: time.Unix(100, 0).UTC(),
	}
	fc := &fakeCommander{}
	svc := &Service{manager: fc, store: st}

	if _, err := svc.SpawnOrchestrator(context.Background(), "mer", true, domain.SessionModeChat); err != nil {
		t.Fatalf("SpawnOrchestrator: %v", err)
	}
	if fc.spawnedCfg.RequestedMode != domain.SessionModeChat {
		t.Fatalf("replacement mode = %q, want explicit chat", fc.spawnedCfg.RequestedMode)
	}
}

func TestSpawnOrchestratorUsesExplicitModeForNewProjectOrchestrator(t *testing.T) {
	st := newFakeStore()
	st.projects["mer"] = domain.ProjectRecord{ID: "mer"}
	fc := &fakeCommander{}
	svc := &Service{manager: fc, store: st}

	if _, err := svc.SpawnOrchestrator(context.Background(), "mer", false, domain.SessionModeChat); err != nil {
		t.Fatalf("SpawnOrchestrator: %v", err)
	}
	if fc.spawnedCfg.RequestedMode != domain.SessionModeChat {
		t.Fatalf("requested mode = %q, want chat", fc.spawnedCfg.RequestedMode)
	}
}

func TestSpawnOrchestratorCleanRetireNoticeIsBranchNeutral(t *testing.T) {
	st := newFakeStore()
	st.projects["scratch"] = domain.ProjectRecord{ID: "scratch", Kind: domain.ProjectKindScratch}
	st.sessions["scratch-1"] = domain.SessionRecord{ID: "scratch-1", ProjectID: "scratch", Kind: domain.KindOrchestrator}
	fc := &fakeCommander{}
	svc := &Service{manager: fc, store: st}

	if _, err := svc.SpawnOrchestrator(context.Background(), "scratch", true, ""); err != nil {
		t.Fatalf("SpawnOrchestrator: %v", err)
	}
	if len(fc.sentMessages) != 1 {
		t.Fatalf("retire messages = %d, want 1", len(fc.sentMessages))
	}
	if strings.Contains(strings.ToLower(fc.sentMessages[0]), "branch") {
		t.Fatalf("retire notice must be branch-neutral, got %q", fc.sentMessages[0])
	}
}

// TestSpawnUnknownProjectReturns404 covers Bug 1: an HTTP spawn for an
// unregistered projectId must surface PROJECT_NOT_FOUND (apierr.NotFound)
// BEFORE any session row is created, so no orphan terminated row is left
// behind under `--include-terminated`.
func TestSpawnUnknownProjectReturns404(t *testing.T) {
	st := newFakeStore()
	fc := &fakeCommander{}
	svc := &Service{manager: fc, store: st}

	_, _, _, err := svc.Spawn(context.Background(), ports.SpawnConfig{ProjectID: "ghost", Kind: domain.KindWorker})
	var e *apierr.Error
	if !errors.As(err, &e) || e.Kind != apierr.KindNotFound || e.Code != "PROJECT_NOT_FOUND" {
		t.Fatalf("err = %v, want apierr.NotFound PROJECT_NOT_FOUND", err)
	}
	if fc.spawned {
		t.Fatal("manager.Spawn must NOT be invoked for an unknown project")
	}
}

func TestSpawnEmitsFirstSessionOnboardingAndDuration(t *testing.T) {
	st := newFakeStore()
	st.projects["mer"] = domain.ProjectRecord{ID: "mer", RegisteredAt: time.Unix(100, 0).UTC()}
	sink := &fakeTelemetrySink{}
	fc := &fakeCommander{}
	svc := NewWithDeps(Deps{
		Manager:   fc,
		Store:     st,
		Telemetry: sink,
		Clock:     func() time.Time { return time.Unix(102, 0).UTC() },
	})

	if _, _, _, err := svc.Spawn(context.Background(), ports.SpawnConfig{ProjectID: "mer"}); err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if len(sink.events) != 2 {
		t.Fatalf("events = %#v, want spawned + first_session", sink.events)
	}
	if sink.events[0].Name != "ao.session.spawned" || sink.events[1].Name != "ao.onboarding.first_session_spawned" {
		t.Fatalf("event names = %#v", []string{sink.events[0].Name, sink.events[1].Name})
	}
	if got := sink.events[0].Payload["duration_ms"]; got != int64(0) {
		t.Fatalf("spawn duration_ms = %#v, want 0 with fixed clock", got)
	}
	if got := sink.events[1].Payload["since_first_project_ms"]; got != int64(2000) {
		t.Fatalf("since_first_project_ms = %#v, want 2000", got)
	}
}

type fakeTracker struct {
	issue domain.Issue
	err   error
	ids   []domain.TrackerID
}

func (f *fakeTracker) Get(_ context.Context, id domain.TrackerID) (domain.Issue, error) {
	f.ids = append(f.ids, id)
	if f.err != nil {
		return domain.Issue{}, f.err
	}
	return f.issue, nil
}

func (f *fakeTracker) List(context.Context, domain.TrackerRepo, domain.ListFilter) ([]domain.Issue, error) {
	return nil, nil
}

func (f *fakeTracker) Preflight(context.Context) error { return nil }

func TestSpawnEnrichesIssueContextFromTracker(t *testing.T) {
	st := newFakeStore()
	st.projects["mer"] = domain.ProjectRecord{ID: "mer", RepoOriginURL: "https://github.com/acme/repo.git"}
	fc := &fakeCommander{}
	tracker := &fakeTracker{issue: domain.Issue{
		ID:        domain.TrackerID{Provider: domain.TrackerProviderGitHub, Native: "acme/repo#42"},
		Title:     "Fix generated prompts",
		Body:      "Prompt files should include standing instructions.",
		State:     domain.IssueInProgress,
		URL:       "https://github.com/acme/repo/issues/42",
		Labels:    []string{"bug", "prompts"},
		Assignees: []string{"dev"},
	}}
	svc := NewWithDeps(Deps{Manager: fc, Store: st, Tracker: tracker})

	if _, _, _, err := svc.Spawn(context.Background(), ports.SpawnConfig{ProjectID: "mer", Kind: domain.KindWorker, IssueID: "42"}); err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if len(tracker.ids) != 1 || tracker.ids[0].Provider != domain.TrackerProviderGitHub || tracker.ids[0].Native != "acme/repo#42" {
		t.Fatalf("tracker ids = %+v, want github acme/repo#42", tracker.ids)
	}
	issueContext := fc.spawnedCfg.IssueContext
	for _, want := range []string{
		"Issue: acme/repo#42",
		"Title: Fix generated prompts",
		"State: in_progress",
		"URL: https://github.com/acme/repo/issues/42",
		"Labels: bug, prompts",
		"Assignees: dev",
		"Body:\nPrompt files should include standing instructions.",
	} {
		if !strings.Contains(issueContext, want) {
			t.Fatalf("IssueContext missing %q:\n%s", want, issueContext)
		}
	}
}

func TestSpawnIssueContextFetchFailureFallsBack(t *testing.T) {
	st := newFakeStore()
	st.projects["mer"] = domain.ProjectRecord{ID: "mer", RepoOriginURL: "https://github.com/acme/repo"}
	fc := &fakeCommander{}
	tracker := &fakeTracker{err: errors.New("tracker unavailable")}
	svc := NewWithDeps(Deps{Manager: fc, Store: st, Tracker: tracker})

	if _, _, _, err := svc.Spawn(context.Background(), ports.SpawnConfig{ProjectID: "mer", Kind: domain.KindWorker, IssueID: "42"}); err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if len(tracker.ids) != 1 {
		t.Fatalf("tracker calls = %d, want 1", len(tracker.ids))
	}
	if fc.spawnedCfg.IssueContext != "" {
		t.Fatalf("IssueContext = %q, want fallback empty context", fc.spawnedCfg.IssueContext)
	}
}

// TestSpawnPreservesIssueIDWhenTrackerIsNil covers the issue #2685 boundary: when
// the daemon wiring cannot build a GitHub tracker (no token), it hands the session
// service a true-nil ports.Tracker. Spawn must still create the session, preserve
// IssueID, and skip only the GitHub issue-context enrichment — not panic on a
// typed-nil tracker the way the pre-fix wiring did.
func TestSpawnPreservesIssueIDWhenTrackerIsNil(t *testing.T) {
	st := newFakeStore()
	st.projects["mer"] = domain.ProjectRecord{ID: "mer", RepoOriginURL: "https://github.com/acme/repo"}
	fc := &fakeCommander{}
	svc := NewWithDeps(Deps{Manager: fc, Store: st, Tracker: nil})

	if _, _, _, err := svc.Spawn(context.Background(), ports.SpawnConfig{ProjectID: "mer", Kind: domain.KindWorker, IssueID: "107"}); err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if fc.spawnedCfg.IssueID != "107" {
		t.Fatalf("IssueID = %q, want 107 preserved", fc.spawnedCfg.IssueID)
	}
	if fc.spawnedCfg.IssueContext != "" {
		t.Fatalf("IssueContext = %q, want empty (no tracker enrichment)", fc.spawnedCfg.IssueContext)
	}
}

func TestSpawnIssueContextSkipsUnresolvableIssueRef(t *testing.T) {
	st := newFakeStore()
	st.projects["mer"] = domain.ProjectRecord{ID: "mer", RepoOriginURL: "https://github.com/acme/repo"}
	fc := &fakeCommander{}
	tracker := &fakeTracker{}
	svc := NewWithDeps(Deps{Manager: fc, Store: st, Tracker: tracker})

	if _, _, _, err := svc.Spawn(context.Background(), ports.SpawnConfig{ProjectID: "mer", Kind: domain.KindWorker, IssueID: "not-an-issue"}); err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if len(tracker.ids) != 0 {
		t.Fatalf("tracker calls = %d, want 0", len(tracker.ids))
	}
	if fc.spawnedCfg.IssueContext != "" {
		t.Fatalf("IssueContext = %q, want empty", fc.spawnedCfg.IssueContext)
	}
}

// TestSpawnParentOrchestratorIDAttribution is the service-layer half of the
// #1211 fix: cfg.ParentOrchestratorID (the caller's AO_SESSION_ID, sent by
// `ao spawn` when it runs from inside an orchestrator's own agent process)
// must only reach the write path when it actually names a live orchestrator
// session in the store — never for a worker's own AO_SESSION_ID, an unknown
// id, or a plain user/CLI spawn that supplied none at all.
func TestSpawnParentOrchestratorIDAttribution(t *testing.T) {
	newSvc := func() (*Service, *fakeStore, *fakeCommander) {
		st := newFakeStore()
		st.projects["mer"] = domain.ProjectRecord{ID: "mer"}
		fc := &fakeCommander{}
		return &Service{manager: fc, store: st}, st, fc
	}

	t.Run("attributes the orchestrator that requested the spawn", func(t *testing.T) {
		svc, st, fc := newSvc()
		st.sessions["mer-orch"] = domain.SessionRecord{ID: "mer-orch", ProjectID: "mer", Kind: domain.KindOrchestrator}

		if _, _, _, err := svc.Spawn(context.Background(), ports.SpawnConfig{
			ProjectID: "mer", Kind: domain.KindWorker, ParentOrchestratorID: "mer-orch",
		}); err != nil {
			t.Fatalf("Spawn: %v", err)
		}
		if fc.spawnedCfg.ParentOrchestratorID != "mer-orch" {
			t.Fatalf("spawnedCfg.ParentOrchestratorID = %q, want mer-orch", fc.spawnedCfg.ParentOrchestratorID)
		}
	})

	t.Run("drops a candidate that is not an orchestrator session", func(t *testing.T) {
		svc, st, fc := newSvc()
		st.sessions["mer-worker"] = domain.SessionRecord{ID: "mer-worker", ProjectID: "mer", Kind: domain.KindWorker}

		if _, _, _, err := svc.Spawn(context.Background(), ports.SpawnConfig{
			ProjectID: "mer", Kind: domain.KindWorker, ParentOrchestratorID: "mer-worker",
		}); err != nil {
			t.Fatalf("Spawn: %v", err)
		}
		if fc.spawnedCfg.ParentOrchestratorID != "" {
			t.Fatalf("spawnedCfg.ParentOrchestratorID = %q, want empty for a non-orchestrator candidate", fc.spawnedCfg.ParentOrchestratorID)
		}
	})

	t.Run("drops an unknown candidate id", func(t *testing.T) {
		svc, _, fc := newSvc()

		if _, _, _, err := svc.Spawn(context.Background(), ports.SpawnConfig{
			ProjectID: "mer", Kind: domain.KindWorker, ParentOrchestratorID: "ghost",
		}); err != nil {
			t.Fatalf("Spawn: %v", err)
		}
		if fc.spawnedCfg.ParentOrchestratorID != "" {
			t.Fatalf("spawnedCfg.ParentOrchestratorID = %q, want empty for an unknown candidate", fc.spawnedCfg.ParentOrchestratorID)
		}
	})

	t.Run("leaves it empty for a direct user/CLI spawn", func(t *testing.T) {
		svc, _, fc := newSvc()

		if _, _, _, err := svc.Spawn(context.Background(), ports.SpawnConfig{ProjectID: "mer", Kind: domain.KindWorker}); err != nil {
			t.Fatalf("Spawn: %v", err)
		}
		if fc.spawnedCfg.ParentOrchestratorID != "" {
			t.Fatalf("spawnedCfg.ParentOrchestratorID = %q, want empty when the spawn request supplied none", fc.spawnedCfg.ParentOrchestratorID)
		}
	})
}

func TestSpawnFailedEmitsDuration(t *testing.T) {
	st := newFakeStore()
	st.projects["mer"] = domain.ProjectRecord{ID: "mer"}
	sink := &fakeTelemetrySink{}
	fc := &fakeCommander{spawnErr: errors.New("boom")}
	now := time.Unix(200, 0).UTC()
	svc := NewWithDeps(Deps{
		Manager:   fc,
		Store:     st,
		Telemetry: sink,
		Clock: func() time.Time {
			v := now
			now = now.Add(1500 * time.Millisecond)
			return v
		},
	})

	if _, _, _, err := svc.Spawn(context.Background(), ports.SpawnConfig{ProjectID: "mer"}); err == nil {
		t.Fatal("Spawn should fail")
	}
	if len(sink.events) != 1 || sink.events[0].Name != "ao.session.spawn_failed" {
		t.Fatalf("events = %#v, want one spawn_failed", sink.events)
	}
	if got := sink.events[0].Payload["duration_ms"]; got != int64(1500) {
		t.Fatalf("spawn_failed duration_ms = %#v, want 1500", got)
	}
	if got := sink.events[0].Payload["error_kind"]; got != "internal" {
		t.Fatalf("spawn_failed error_kind = %#v, want internal", got)
	}
	if got := sink.events[0].Payload["component"]; got != "session_service" {
		t.Fatalf("spawn_failed component = %#v, want session_service", got)
	}
	if got := sink.events[0].Payload["operation"]; got != "spawn_session" {
		t.Fatalf("spawn_failed operation = %#v, want spawn_session", got)
	}
	if got := sink.events[0].Payload["fingerprint"]; got == "" {
		t.Fatalf("spawn_failed fingerprint = %#v, want non-empty", got)
	}
}

func TestSpawnEmitsTelemetryOnSuccess(t *testing.T) {
	st := newFakeStore()
	st.projects["mer"] = domain.ProjectRecord{ID: "mer"}
	st.sessions["old-1"] = domain.SessionRecord{ID: "old-1", ProjectID: "other"}
	fc := &fakeCommander{}
	ts := &fakeTelemetrySink{}
	svc := NewWithDeps(Deps{Manager: fc, Store: st, Telemetry: ts, Clock: func() time.Time { return time.Unix(1700000000, 0).UTC() }})

	_, _, _, err := svc.Spawn(context.Background(), ports.SpawnConfig{
		ProjectID: "mer",
		Kind:      domain.KindWorker,
		Harness:   domain.HarnessCodex,
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if len(ts.events) != 1 {
		t.Fatalf("telemetry events = %d, want 1", len(ts.events))
	}
	ev := ts.events[0]
	if ev.Name != "ao.session.spawned" || ev.Source != "session_service" {
		t.Fatalf("event = %+v", ev)
	}
	if ev.ProjectID == nil || *ev.ProjectID != "mer" || ev.SessionID == nil || *ev.SessionID != "mer-9" {
		t.Fatalf("event ids = %+v", ev)
	}
}

func TestSpawnEmitsTelemetryOnFailure(t *testing.T) {
	st := newFakeStore()
	st.projects["mer"] = domain.ProjectRecord{ID: "mer"}
	fc := &fakeCommander{spawnErr: errors.New("boom")}
	ts := &fakeTelemetrySink{}
	svc := NewWithDeps(Deps{Manager: fc, Store: st, Telemetry: ts, Clock: func() time.Time { return time.Unix(1700000000, 0).UTC() }})

	_, _, _, err := svc.Spawn(context.Background(), ports.SpawnConfig{
		ProjectID: "mer",
		Kind:      domain.KindWorker,
		Harness:   domain.HarnessCodex,
	})
	if err == nil {
		t.Fatal("Spawn error = nil, want failure")
	}
	if len(ts.events) != 1 {
		t.Fatalf("telemetry events = %d, want 1", len(ts.events))
	}
	ev := ts.events[0]
	if ev.Name != "ao.session.spawn_failed" || ev.Source != "session_service" || ev.Level != ports.TelemetryLevelError {
		t.Fatalf("event = %+v", ev)
	}
	if ev.ProjectID == nil || *ev.ProjectID != "mer" || ev.SessionID != nil {
		t.Fatalf("event ids = %+v", ev)
	}
	if got := ev.Payload["error_kind"]; got != "internal" {
		t.Fatalf("event payload error_kind = %#v, want internal", got)
	}
	if got := ev.Payload["component"]; got != "session_service" {
		t.Fatalf("event payload component = %#v, want session_service", got)
	}
	if got := ev.Payload["operation"]; got != "spawn_session" {
		t.Fatalf("event payload operation = %#v, want spawn_session", got)
	}
	if got := ev.Payload["fingerprint"]; got == "" {
		t.Fatalf("event payload fingerprint = %#v, want non-empty", got)
	}
	if _, ok := ev.Payload["error"]; ok {
		t.Fatalf("event payload leaked raw error: %+v", ev.Payload)
	}
}

func TestSpawnEmitsTypedErrorCodeOnFailure(t *testing.T) {
	st := newFakeStore()
	st.projects["mer"] = domain.ProjectRecord{ID: "mer"}
	fc := &fakeCommander{spawnErr: fmt.Errorf("spawn: %w: %q", sessionmanager.ErrUnknownHarness, "bogus")}
	ts := &fakeTelemetrySink{}
	svc := NewWithDeps(Deps{Manager: fc, Store: st, Telemetry: ts, Clock: func() time.Time { return time.Unix(1700000000, 0).UTC() }})

	_, _, _, err := svc.Spawn(context.Background(), ports.SpawnConfig{
		ProjectID: "mer",
		Kind:      domain.KindWorker,
		Harness:   domain.HarnessCodex,
	})
	if err == nil {
		t.Fatal("Spawn error = nil, want failure")
	}
	if len(ts.events) != 1 {
		t.Fatalf("telemetry events = %d, want 1", len(ts.events))
	}
	ev := ts.events[0]
	if got := ev.Payload["error_kind"]; got != "invalid" {
		t.Fatalf("event payload error_kind = %#v, want invalid", got)
	}
	if got := ev.Payload["error_code"]; got != "UNKNOWN_HARNESS" {
		t.Fatalf("event payload error_code = %#v, want UNKNOWN_HARNESS", got)
	}
}

// TestSpawnOrchestratorUnknownProjectReturns404 is the orchestrator-side guard
// for Bug 1: same pre-validation, same typed envelope.
func TestSpawnOrchestratorUnknownProjectReturns404(t *testing.T) {
	st := newFakeStore()
	fc := &fakeCommander{}
	svc := &Service{manager: fc, store: st}

	_, err := svc.SpawnOrchestrator(context.Background(), "ghost", false, "")
	var e *apierr.Error
	if !errors.As(err, &e) || e.Kind != apierr.KindNotFound || e.Code != "PROJECT_NOT_FOUND" {
		t.Fatalf("err = %v, want apierr.NotFound PROJECT_NOT_FOUND", err)
	}
	if fc.spawned {
		t.Fatal("manager.Spawn must NOT be invoked for an unknown project")
	}
}

// TestToAPIErrorMapsWorkspaceBranchSentinels covers Bug 3: the workspace
// adapter's typed branch errors map to typed envelope errors instead of
// collapsing to a 500.
func TestToAPIErrorMapsWorkspaceBranchSentinels(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		wantKind apierr.Kind
		wantCode string
	}{
		{"checked out elsewhere", fmt.Errorf("spawn mer-1: workspace: %w: \"x\" is checked out at \"/tmp\"", ports.ErrWorkspaceBranchCheckedOutElsewhere), apierr.KindConflict, "BRANCH_CHECKED_OUT_ELSEWHERE"},
		{"not fetched", fmt.Errorf("spawn mer-1: workspace: %w: \"x\" has no local head", ports.ErrWorkspaceBranchNotFetched), apierr.KindInvalid, "BRANCH_NOT_FETCHED"},
		{"invalid branch", fmt.Errorf("spawn mer-1: workspace: %w: \"bad!!\" (exit 1)", ports.ErrWorkspaceBranchInvalid), apierr.KindInvalid, "INVALID_BRANCH"},
		{"agent binary not found", fmt.Errorf("spawn mer-1: %w", ports.ErrAgentBinaryNotFound), apierr.KindInvalid, "AGENT_BINARY_NOT_FOUND"},
		{"runtime prerequisite missing", fmt.Errorf("spawn: %w: tmux required on macOS/Linux but not in PATH", ports.ErrRuntimePrerequisite), apierr.KindInvalid, "RUNTIME_PREREQUISITE_MISSING"},
		{"runtime workspace cwd mismatch", fmt.Errorf("spawn mer-1: runtime: %w: session mer-1 started in \"/deleted/shipit\", want \"/tmp/ws\"", ports.ErrRuntimeWorkspaceCwdMismatch), apierr.KindConflict, "WORKSPACE_CWD_MISMATCH"},
		{"workspace locked", fmt.Errorf("restore mer-1: %w: \"/tmp/ws\" (branch \"ao/mer-1\") is registered but its directory is missing", ports.ErrWorkspaceLocked), apierr.KindConflict, "WORKSPACE_LOCKED"},
		{"unknown harness", fmt.Errorf("spawn: %w: %q", sessionmanager.ErrUnknownHarness, "bogus"), apierr.KindInvalid, "UNKNOWN_HARNESS"},
		{"missing harness", fmt.Errorf("spawn: %w: configure project worker.agent or pass --harness", sessionmanager.ErrMissingHarness), apierr.KindInvalid, "AGENT_REQUIRED"},
		{"awaiting decision", fmt.Errorf("send mer-1: %w", sessionmanager.ErrAwaitingDecision), apierr.KindConflict, "SESSION_AWAITING_DECISION"},
		{"agent exited", fmt.Errorf("send mer-1: %w", sessionmanager.ErrAgentExited), apierr.KindConflict, "AGENT_EXITED"},
		{"agent not exited", fmt.Errorf("resume agent mer-1: %w", sessionmanager.ErrAgentNotExited), apierr.KindConflict, "AGENT_NOT_EXITED"},
		{"resume in progress", fmt.Errorf("resume agent mer-1: %w", sessionmanager.ErrResumeInProgress), apierr.KindConflict, "AGENT_RESUME_IN_PROGRESS"},
		{"chat mode unsupported", fmt.Errorf("spawn: %w", ports.ErrChatUnsupported), apierr.KindConflict, "SESSION_MODE_UNSUPPORTED"},
		{"chat driver unavailable", fmt.Errorf("spawn: %w", ports.ErrChatDriverUnavailable), apierr.KindConflict, "CHAT_DRIVER_UNAVAILABLE"},
		{"chat driver incompatible", fmt.Errorf("spawn: %w", ports.ErrChatDriverIncompatible), apierr.KindConflict, "CHAT_DRIVER_INCOMPATIBLE"},
		{"chat auth required", fmt.Errorf("spawn: %w", ports.ErrChatAuthRequired), apierr.KindConflict, "CHAT_AUTH_REQUIRED"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mapped := toAPIError(tc.err)
			var e *apierr.Error
			if !errors.As(mapped, &e) || e.Kind != tc.wantKind || e.Code != tc.wantCode {
				t.Fatalf("mapped = %v, want %s %s", mapped, tc.wantCode, e)
			}
		})
	}
}

// TestToAPIError_NotResumable asserts that ErrNotResumable (promptless worker
// with no adapter resume handle) maps to a Conflict with code SESSION_NOT_RESUMABLE.
func TestToAPIError_NotResumable(t *testing.T) {
	err := fmt.Errorf("restore mer-1: %w", sessionmanager.ErrNotResumable)
	mapped := toAPIError(err)
	var e *apierr.Error
	if !errors.As(mapped, &e) || e.Kind != apierr.KindConflict || e.Code != "SESSION_NOT_RESUMABLE" {
		t.Fatalf("mapped = %v, want Conflict SESSION_NOT_RESUMABLE", mapped)
	}
}

func TestRestoreMapsManagerModeToServiceView(t *testing.T) {
	st := newFakeStore()
	rec := domain.SessionRecord{
		ID:        "mer-1",
		ProjectID: "mer",
		Kind:      domain.KindWorker,
		Harness:   domain.HarnessCodex,
		Activity:  domain.Activity{State: domain.ActivityIdle},
	}
	fc := &fakeCommander{
		restoreResult: sessionmanager.RestoreResult{
			Session: rec,
			Mode:    sessionmanager.RestoreModeSavedPrompt,
		},
	}
	svc := &Service{manager: fc, store: st}

	got, err := svc.Restore(context.Background(), "mer-1")
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if got.Session.ID != "mer-1" {
		t.Fatalf("session id = %q, want mer-1", got.Session.ID)
	}
	if got.Mode != RestoreModeViewSavedPrompt {
		t.Fatalf("mode = %q, want %q", got.Mode, RestoreModeViewSavedPrompt)
	}
}

func TestResumeAgentMapsManagerModeToServiceView(t *testing.T) {
	st := newFakeStore()
	rec := domain.SessionRecord{
		ID:        "mer-1",
		ProjectID: "mer",
		Kind:      domain.KindWorker,
		Harness:   domain.HarnessCodex,
		Activity:  domain.Activity{State: domain.ActivityIdle},
	}
	fc := &fakeCommander{
		restoreResult: sessionmanager.RestoreResult{
			Session: rec,
			Mode:    sessionmanager.RestoreModeNative,
		},
	}
	svc := &Service{manager: fc, store: st}

	got, err := svc.ResumeAgent(context.Background(), "mer-1")
	if err != nil {
		t.Fatalf("ResumeAgent: %v", err)
	}
	if got.Session.ID != "mer-1" || got.Mode != RestoreModeViewNative {
		t.Fatalf("resume outcome = %+v", got)
	}
}

func TestSpawnGenericOrchestratorReturnsExistingActiveSession(t *testing.T) {
	st := newFakeStore()
	st.projects["mer"] = domain.ProjectRecord{ID: "mer"}
	st.sessions["mer-orch"] = domain.SessionRecord{
		ID:        "mer-orch",
		ProjectID: "mer",
		Kind:      domain.KindOrchestrator,
		Harness:   domain.HarnessCodex,
	}
	fc := &fakeCommander{}
	svc := &Service{manager: fc, store: st}

	got, promptBytes, systemPromptBytes, err := svc.Spawn(context.Background(), ports.SpawnConfig{
		ProjectID:   "mer",
		Kind:        domain.KindOrchestrator,
		Harness:     domain.HarnessClaudeCode,
		Prompt:      "start another orchestrator",
		DisplayName: "duplicate",
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if got.ID != "mer-orch" {
		t.Fatalf("returned id = %q, want existing orchestrator mer-orch", got.ID)
	}
	if promptBytes != 0 || systemPromptBytes != 0 {
		t.Fatalf("prompt sizes = (%d, %d), want zero for reused session", promptBytes, systemPromptBytes)
	}
	if fc.spawned {
		t.Fatal("manager.Spawn must not be called when an active orchestrator already exists")
	}
}

func TestSpawnGenericOrchestratorAllowsReplacementAfterTermination(t *testing.T) {
	st := newFakeStore()
	st.projects["mer"] = domain.ProjectRecord{ID: "mer"}
	st.sessions["mer-old"] = domain.SessionRecord{
		ID:           "mer-old",
		ProjectID:    "mer",
		Kind:         domain.KindOrchestrator,
		IsTerminated: true,
	}
	fc := &fakeCommander{spawnRecord: domain.SessionRecord{
		ID:        "mer-new",
		ProjectID: "mer",
		Kind:      domain.KindOrchestrator,
		Harness:   domain.HarnessClaudeCode,
	}}
	svc := &Service{manager: fc, store: st}
	cfg := ports.SpawnConfig{
		ProjectID:   "mer",
		Kind:        domain.KindOrchestrator,
		Harness:     domain.HarnessClaudeCode,
		Branch:      "feature/orchestrator",
		Prompt:      "coordinate this project",
		DisplayName: "coordinator",
	}

	got, _, _, err := svc.Spawn(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if got.ID != "mer-new" {
		t.Fatalf("returned id = %q, want replacement mer-new", got.ID)
	}
	if fc.spawnCalls != 1 {
		t.Fatalf("manager.Spawn calls = %d, want 1", fc.spawnCalls)
	}
	if fc.spawnedCfg.ProjectID != cfg.ProjectID ||
		fc.spawnedCfg.Kind != cfg.Kind ||
		fc.spawnedCfg.Harness != cfg.Harness ||
		fc.spawnedCfg.Branch != cfg.Branch ||
		fc.spawnedCfg.Prompt != cfg.Prompt ||
		fc.spawnedCfg.DisplayName != cfg.DisplayName {
		t.Fatalf("spawn config = %#v, want request config %#v", fc.spawnedCfg, cfg)
	}
}

func TestSpawnGenericWorkerUnaffectedByActiveOrchestrator(t *testing.T) {
	st := newFakeStore()
	st.projects["mer"] = domain.ProjectRecord{ID: "mer"}
	st.sessions["mer-orch"] = domain.SessionRecord{
		ID:        "mer-orch",
		ProjectID: "mer",
		Kind:      domain.KindOrchestrator,
	}
	fc := &fakeCommander{spawnRecord: domain.SessionRecord{
		ID:        "mer-worker",
		ProjectID: "mer",
		Kind:      domain.KindWorker,
	}}
	svc := &Service{manager: fc, store: st}

	got, _, _, err := svc.Spawn(context.Background(), ports.SpawnConfig{ProjectID: "mer", Kind: domain.KindWorker})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if got.ID != "mer-worker" || fc.spawnCalls != 1 {
		t.Fatalf("worker spawn = %#v, manager.Spawn calls = %d", got, fc.spawnCalls)
	}
}

func TestSpawnGenericOrchestratorSerializesConcurrentRequests(t *testing.T) {
	st := newFakeStore()
	st.projects["mer"] = domain.ProjectRecord{ID: "mer"}
	fc := &fakeCommander{}
	fc.spawnFunc = func(cfg ports.SpawnConfig) domain.SessionRecord {
		rec := domain.SessionRecord{
			ID:        "mer-orch",
			ProjectID: cfg.ProjectID,
			Kind:      domain.KindOrchestrator,
			Harness:   cfg.Harness,
		}
		st.sessions[rec.ID] = rec
		return rec
	}
	svc := &Service{manager: fc, store: st}
	cfg := ports.SpawnConfig{ProjectID: "mer", Kind: domain.KindOrchestrator, Harness: domain.HarnessCodex}

	start := make(chan struct{})
	results := make(chan domain.Session, 2)
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			session, _, _, err := svc.Spawn(context.Background(), cfg)
			results <- session
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent Spawn: %v", err)
		}
	}
	for session := range results {
		if session.ID != "mer-orch" {
			t.Fatalf("returned id = %q, want mer-orch", session.ID)
		}
	}
	if fc.spawnCalls != 1 {
		t.Fatalf("manager.Spawn calls = %d, want 1", fc.spawnCalls)
	}
	if len(st.sessions) != 1 {
		t.Fatalf("session count = %d, want 1", len(st.sessions))
	}
}

// TestSpawnOrchestratorNoCleanReturnsExistingWhenActiveExists is the RED test
// for the idempotency fix: when an active orchestrator already exists and
// clean=false, SpawnOrchestrator must return that orchestrator without minting
// a second one. Before the fix this test fails because a duplicate is spawned.
func TestSpawnOrchestratorNoCleanReturnsExistingWhenActiveExists(t *testing.T) {
	st := newFakeStore()
	st.projects["mer"] = domain.ProjectRecord{ID: "mer"}
	// Pre-load an active orchestrator.
	st.sessions["mer-1"] = domain.SessionRecord{ID: "mer-1", ProjectID: "mer", Kind: domain.KindOrchestrator}

	fc := &fakeCommander{}
	svc := &Service{manager: fc, store: st}

	got, err := svc.SpawnOrchestrator(context.Background(), "mer", false, "")
	if err != nil {
		t.Fatalf("SpawnOrchestrator: %v", err)
	}
	// Must return the existing orchestrator, not a newly minted one.
	if got.ID != "mer-1" {
		t.Fatalf("returned id = %q, want existing orchestrator mer-1", got.ID)
	}
	// Must NOT have called manager.Spawn (no duplicate created).
	if fc.spawned {
		t.Fatal("manager.Spawn must NOT be called when an active orchestrator already exists")
	}
	// Must NOT have killed anything.
	if len(fc.killed) != 0 {
		t.Fatalf("no kills expected with clean=false, got %v", fc.killed)
	}
	// Exactly one session in the store (no duplicate).
	if len(st.sessions) != 1 {
		t.Fatalf("session count = %d, want 1 (no duplicate)", len(st.sessions))
	}
}

// TestSpawnOrchestratorNoCleanSpawnsWhenNoneExists: clean=false spawns a new
// orchestrator when no active one exists for the project.
func TestSpawnOrchestratorNoCleanSpawnsWhenNoneExists(t *testing.T) {
	st := newFakeStore()
	st.projects["mer"] = domain.ProjectRecord{ID: "mer"}
	// No active orchestrator present.

	fc := &fakeCommander{}
	svc := &Service{manager: fc, store: st}

	got, err := svc.SpawnOrchestrator(context.Background(), "mer", false, "")
	if err != nil {
		t.Fatalf("SpawnOrchestrator: %v", err)
	}
	if !fc.spawned {
		t.Fatal("manager.Spawn must be called when no active orchestrator exists")
	}
	if len(fc.killed) != 0 {
		t.Fatalf("no kills expected with clean=false, got %v", fc.killed)
	}
	if got.ID == "" {
		t.Fatal("returned session must have an id")
	}
}

func TestSpawnOrchestratorVerifiesReplacementHarness(t *testing.T) {
	st := newFakeStore()
	st.projects["mer"] = domain.ProjectRecord{
		ID:     "mer",
		Config: domain.ProjectConfig{Orchestrator: domain.RoleOverride{Harness: domain.HarnessCodex}},
	}
	fc := &fakeCommander{
		spawnRecord: domain.SessionRecord{
			ID:        "mer-9",
			ProjectID: "mer",
			Kind:      domain.KindOrchestrator,
			Harness:   domain.HarnessClaudeCode,
			Metadata:  domain.SessionMetadata{Branch: "ao/mer-orchestrator"},
		},
	}
	svc := &Service{manager: fc, store: st}

	_, err := svc.SpawnOrchestrator(context.Background(), "mer", false, "")
	if err == nil || !strings.Contains(err.Error(), `uses harness "claude-code", want "codex"`) {
		t.Fatalf("SpawnOrchestrator err = %v, want harness verification failure", err)
	}
}

func TestDelegateTaskPassesAttachmentsToSpawnConfig(t *testing.T) {
	st := newFakeStore()
	st.projects["mer"] = domain.ProjectRecord{ID: "mer"}
	fc := &fakeCommander{}
	svc := NewWithDeps(Deps{Manager: fc, Store: st})
	// This test only inspects the worker spawn. Keep asynchronous title
	// refinement from issuing a second Spawn against the recording fake.
	svc.runBackground = func(func()) {}

	_, err := svc.DelegateTask(context.Background(), DelegateTaskInput{
		ProjectID:      "mer",
		Brief:          "Use the attached image.",
		RequestedAgent: domain.HarnessCodex,
		Attachments: []ports.SpawnAttachment{
			{Ext: ".png", Data: []byte{1, 2, 3}},
		},
	})
	if err != nil {
		t.Fatalf("DelegateTask: %v", err)
	}
	if !fc.spawned {
		t.Fatal("DelegateTask did not call Spawn")
	}
	if fc.spawnedCfg.ProjectID != "mer" || fc.spawnedCfg.Kind != domain.KindWorker {
		t.Fatalf("spawned cfg identity = %#v", fc.spawnedCfg)
	}
	if fc.spawnedCfg.Harness != domain.HarnessCodex || fc.spawnedCfg.Prompt != "Use the attached image." {
		t.Fatalf("spawned cfg fields = %#v", fc.spawnedCfg)
	}
	if len(fc.spawnedCfg.Attachments) != 1 {
		t.Fatalf("attachments = %#v, want one", fc.spawnedCfg.Attachments)
	}
	if got := fc.spawnedCfg.Attachments[0]; got.Ext != ".png" || string(got.Data) != "\x01\x02\x03" {
		t.Fatalf("attachment = %#v, want decoded png", got)
	}
}

type fakePRClaimer struct {
	out errorFreeClaimOutcome
	err error
}

type errorFreeClaimOutcome struct {
	ports.ClaimOutcome
}

func (f fakePRClaimer) ClaimPR(context.Context, domain.PullRequest, []domain.PullRequestCheck, []domain.PullRequestReview, []domain.PullRequestReviewThread, []domain.PullRequestComment, ports.ReviewWriteMode, bool) (ports.ClaimOutcome, error) {
	return f.out.ClaimOutcome, f.err
}

type fakeSCM struct {
	obs       ports.SCMObservation
	review    ports.SCMReviewObservation
	fetchErr  error
	reviewErr error
}

func TestClaimRowsFromSCMSnapshotsSessionReviewPolicy(t *testing.T) {
	now := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	obs := ports.SCMObservation{
		PR: ports.SCMPRObservation{URL: "https://github.com/acme/repo/pull/7", Number: 7},
		Review: ports.SCMReviewObservation{
			Reviews: []ports.SCMReviewSummaryObservation{{ID: "r1", State: string(domain.ReviewChangesRequest), Body: "review body"}},
			Threads: []ports.SCMReviewThreadObservation{{ID: "t1", Comments: []ports.SCMReviewCommentObservation{{ID: "c1", Body: "inline comment"}}}},
		},
	}
	for _, autoInject := range []bool{false, true} {
		t.Run(fmt.Sprintf("auto_inject_%t", autoInject), func(t *testing.T) {
			_, _, reviews, _, comments := claimRowsFromSCM("mer-1", obs, now, domain.SessionRecord{AutoInjectReview: autoInject})
			if len(reviews) != 1 || reviews[0].AutoInjectReview != autoInject {
				t.Fatalf("reviews = %+v, want policy %t", reviews, autoInject)
			}
			if len(comments) != 1 || comments[0].AutoInjectReview != autoInject {
				t.Fatalf("comments = %+v, want policy %t", comments, autoInject)
			}
		})
	}
}

func (f fakeSCM) ParseRepository(remote string) (ports.SCMRepo, bool) {
	owner, repo, err := githubRepoFromURL(remote)
	if err != nil {
		return ports.SCMRepo{}, false
	}
	return ports.SCMRepo{Provider: "github", Host: "github.com", Owner: owner, Name: repo, Repo: owner + "/" + repo}, true
}

func (f fakeSCM) FetchPullRequests(context.Context, []ports.SCMPRRef) ([]ports.SCMObservation, error) {
	if f.fetchErr != nil {
		return nil, f.fetchErr
	}
	if !f.obs.Fetched && f.obs.PR.URL == "" && f.obs.PR.Number == 0 {
		return nil, nil
	}
	return []ports.SCMObservation{f.obs}, nil
}

func (f fakeSCM) FetchReviewThreads(context.Context, ports.SCMPRRef) (ports.SCMReviewObservation, error) {
	return f.review, f.reviewErr
}

func TestClaimPRRejectsScratchProject(t *testing.T) {
	st := newFakeStore()
	st.sessions["scratch-1"] = domain.SessionRecord{
		ID:        "scratch-1",
		ProjectID: "scratch",
		Kind:      domain.KindWorker,
		Metadata:  domain.SessionMetadata{WorkspacePath: "/ws/scratch-1"},
	}
	st.projects["scratch"] = domain.ProjectRecord{ID: "scratch", Kind: domain.ProjectKindScratch}
	svc := NewWithDeps(Deps{
		Store:     st,
		PRClaimer: fakePRClaimer{},
		SCM: fakeSCM{
			obs: ports.SCMObservation{
				Fetched:  true,
				Provider: "github",
				Host:     "github.com",
				Repo:     "acme/repo",
				PR:       ports.SCMPRObservation{URL: "https://github.com/acme/repo/pull/7", Number: 7},
			},
		},
	})

	for _, ref := range []string{"https://github.com/acme/repo/pull/7", "7"} {
		t.Run(ref, func(t *testing.T) {
			_, err := svc.ClaimPR(context.Background(), "scratch-1", ref, ClaimPROptions{})
			if !errors.Is(err, ErrSessionNotClaimable) {
				t.Fatalf("ClaimPR scratch err = %v, want ErrSessionNotClaimable", err)
			}
		})
	}
}

func TestClaimPRMapsObserverAndStoreErrors(t *testing.T) {
	st := newFakeStore()
	now := time.Date(2026, 6, 4, 12, 0, 0, 0, time.UTC)
	st.sessions["mer-1"] = domain.SessionRecord{ID: "mer-1", ProjectID: "mer", Kind: domain.KindWorker, Metadata: domain.SessionMetadata{WorkspacePath: "/ws"}}
	st.projects["mer"] = domain.ProjectRecord{ID: "mer", RepoOriginURL: "https://github.com/acme/repo"}

	cases := []struct {
		name string
		svc  *Service
		want error
	}{
		{"missing scm", NewWithDeps(Deps{Store: st}), ErrSCMUnavailable},
		{"not found", NewWithDeps(Deps{Store: st, PRClaimer: fakePRClaimer{}, SCM: fakeSCM{fetchErr: ports.ErrSCMNotFound}}), ErrPRNotFound},
		{"closed", NewWithDeps(Deps{Store: st, PRClaimer: fakePRClaimer{}, SCM: fakeSCM{obs: ports.SCMObservation{Fetched: true, Provider: "github", Host: "github.com", Repo: "acme/repo", PR: ports.SCMPRObservation{URL: "https://github.com/acme/repo/pull/7", Number: 7, Closed: true}}}}), ErrPRNotOpen},
		{"active owner", NewWithDeps(Deps{Store: st, PRClaimer: fakePRClaimer{err: ports.PRClaimedByActiveSessionError{Owner: "mer-2"}}, SCM: fakeSCM{obs: ports.SCMObservation{Fetched: true, Provider: "github", Host: "github.com", Repo: "acme/repo", PR: ports.SCMPRObservation{URL: "https://github.com/acme/repo/pull/7", Number: 7}}}}), ports.ErrPRClaimedByActiveSession},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.svc.ClaimPR(context.Background(), "mer-1", "7", ClaimPROptions{AllowTakeover: false})
			if !errors.Is(err, tc.want) {
				t.Fatalf("err=%v, want %v", err, tc.want)
			}
		})
	}

	st.pr["mer-1"] = domain.PRFacts{URL: "https://github.com/acme/repo/pull/7", Number: 7, CI: domain.CIPassing, UpdatedAt: now}
	svc := NewWithDeps(Deps{Store: st, PRClaimer: fakePRClaimer{out: errorFreeClaimOutcome{ports.ClaimOutcome{PreviousOwner: "mer-2"}}}, SCM: fakeSCM{obs: ports.SCMObservation{Fetched: true, Provider: "github", Host: "github.com", Repo: "acme/repo", PR: ports.SCMPRObservation{URL: "https://github.com/acme/repo/pull/7", Number: 7}}}})
	res, err := svc.ClaimPR(context.Background(), "mer-1", "7", ClaimPROptions{AllowTakeover: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.TakenOverFrom) != 1 || res.TakenOverFrom[0] != "mer-2" || len(res.PRs) != 1 || res.PRs[0].URL == "" {
		t.Fatalf("claim result = %+v", res)
	}
}

func TestListPRsOrdersActiveBeforeClosedThenUpdatedDesc(t *testing.T) {
	st := newFakeStore()
	st.sessions["mer-1"] = domain.SessionRecord{ID: "mer-1", ProjectID: "mer", Kind: domain.KindWorker}
	now := time.Date(2026, 6, 4, 12, 0, 0, 0, time.UTC)
	st.pr = map[domain.SessionID]domain.PRFacts{}
	stList := &multiPRFakeStore{fakeStore: st, prs: []domain.PullRequest{
		{URL: "closed-new", SessionID: "mer-1", Number: 1, Closed: true, UpdatedAt: now.Add(2 * time.Hour)},
		{URL: "open-old", SessionID: "mer-1", Number: 2, UpdatedAt: now},
		{URL: "open-new", SessionID: "mer-1", Number: 3, UpdatedAt: now.Add(time.Hour)},
	}}
	got, err := (&Service{store: stList}).ListPRs(context.Background(), "mer-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].URL != "open-new" || got[1].URL != "open-old" || got[2].URL != "closed-new" {
		t.Fatalf("order = %+v", got)
	}
}

func TestListPRSummariesExposesReviewSummariesButKeepsRawLogsAndCommentBodiesPrivate(t *testing.T) {
	st := newFakeStore()
	now := time.Date(2026, 6, 4, 12, 0, 0, 0, time.UTC)
	st.sessions["mer-1"] = domain.SessionRecord{ID: "mer-1", ProjectID: "mer", Kind: domain.KindWorker}
	prURL := "https://github.com/acme/repo/pull/7"
	stList := &multiPRFakeStore{fakeStore: st, prs: []domain.PullRequest{{
		URL:                      prURL,
		HTMLURL:                  prURL,
		SessionID:                "mer-1",
		Number:                   7,
		CI:                       domain.CIFailing,
		Review:                   domain.ReviewChangesRequest,
		Mergeability:             domain.MergeConflicting,
		Provider:                 "github",
		Repo:                     "acme/repo",
		Title:                    "Fix dashboard",
		Author:                   "ada",
		SourceBranch:             "fix/dashboard",
		TargetBranch:             "main",
		HeadSHA:                  "abc123",
		ProviderMergeStateStatus: "dirty",
		UpdatedAt:                now,
		ObservedAt:               now.Add(-time.Minute),
		CIObservedAt:             now.Add(-time.Minute),
		ReviewObservedAt:         now.Add(-time.Minute),
	}}}
	stList.checks[prURL] = []domain.PullRequestCheck{
		{Name: "unit", Status: domain.PRCheckFailed, Conclusion: "failure", URL: "https://github.com/acme/repo/actions/runs/1", LogTail: "panic: secret"},
		{Name: "lint", Status: domain.PRCheckPassed, Conclusion: "success", URL: "https://github.com/acme/repo/actions/runs/2"},
	}
	stList.reviews[prURL] = []domain.PullRequestReview{
		{ID: "review-1", Author: "reviewer-a", State: domain.ReviewChangesRequest, URL: "https://github.com/acme/repo/pull/7#pullrequestreview-1", Body: "summary: please fix the failing unit test", SubmittedAt: now.Add(-30 * time.Second), AutoInjectReview: false},
	}
	stList.comments[prURL] = []domain.PullRequestComment{
		{Author: "reviewer-a", File: "main.go", Line: 12, Body: "raw body must stay private", URL: "https://github.com/acme/repo/pull/7#discussion_r1", AutoInjectReview: false},
		{Author: "ci-bot", File: "main.go", Line: 13, Body: "bot body", URL: "https://github.com/acme/repo/pull/7#discussion_r2", IsBot: true},
		{Author: "reviewer-a", File: "test.go", Line: 22, Body: "another raw body", URL: "https://github.com/acme/repo/pull/7#discussion_r3", AutoInjectReview: true},
	}

	got, err := (&Service{store: stList}).ListPRSummaries(context.Background(), "mer-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("summaries = %+v", got)
	}
	pr := got[0]
	if pr.Title != "Fix dashboard" || pr.State != domain.PRStateOpen || pr.Provider != "github" || pr.Repo != "acme/repo" || pr.HeadSHA != "abc123" {
		t.Fatalf("metadata = %+v", pr)
	}
	if len(pr.CI.FailingChecks) != 1 || pr.CI.FailingChecks[0].Name != "unit" || pr.CI.FailingChecks[0].URL == "" {
		t.Fatalf("failing checks = %+v", pr.CI.FailingChecks)
	}
	if pr.Review.Decision != domain.ReviewChangesRequest || !pr.Review.HasUnresolvedHumanComments || len(pr.Review.UnresolvedBy) != 1 {
		t.Fatalf("review = %+v", pr.Review)
	}
	if reviewer := pr.Review.UnresolvedBy[0]; reviewer.ReviewerID != "reviewer-a" || reviewer.Count != 2 || len(reviewer.Links) != 2 {
		t.Fatalf("reviewer = %+v", reviewer)
	} else if reviewer.ReviewURL != "https://github.com/acme/repo/pull/7#pullrequestreview-1" {
		t.Fatalf("review url = %q", reviewer.ReviewURL)
	} else if reviewer.Links[0].AutoInjectReview || !reviewer.Links[1].AutoInjectReview {
		t.Fatalf("comment injection decisions = %+v, want false then true", reviewer.Links)
	}
	if pr.Mergeability.State != domain.MergeConflicting || len(pr.Mergeability.ConflictFiles) != 0 || !containsString(pr.Mergeability.Reasons, "conflicts") {
		t.Fatalf("mergeability = %+v", pr.Mergeability)
	}
	if len(pr.Review.Reviews) != 1 {
		t.Fatalf("review summaries = %+v", pr.Review.Reviews)
	}
	if entry := pr.Review.Reviews[0]; entry.Reviewer != "reviewer-a" || entry.Verdict != domain.ReviewChangesRequest ||
		entry.Body != "summary: please fix the failing unit test" ||
		entry.URL != "https://github.com/acme/repo/pull/7#pullrequestreview-1" || entry.AutoInjectReview {
		t.Fatalf("review summary entry = %+v", entry)
	}
	// The review summary body is surfaced, but inline comment bodies and CI log
	// tails must never leak into the PR summary.
	blob := fmt.Sprintf("%+v", got)
	for _, secret := range []string{"raw body must stay private", "another raw body", "bot body", "panic: secret"} {
		if strings.Contains(blob, secret) {
			t.Fatalf("summary leaked private text %q", secret)
		}
	}
}

func TestSummarizeReviewSurfacesApprovedAndChangesRequestedSummaries(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	reviews := []domain.PullRequestReview{
		// alice's approved review supersedes her earlier changes_requested one.
		{ID: "a-old", Author: "alice", State: domain.ReviewChangesRequest, Body: "old note", URL: "url-a-old", SubmittedAt: now.Add(-time.Hour)},
		{ID: "a-new", Author: "alice", State: domain.ReviewApproved, Body: "looks good now", URL: "url-a-new", SubmittedAt: now},
		{ID: "b", Author: "bob", State: domain.ReviewChangesRequest, Body: "please fix", URL: "url-b", SubmittedAt: now},
	}

	got := summarizeReview(domain.PullRequest{URL: "u", Review: domain.ReviewChangesRequest}, nil, reviews)

	byReviewer := map[string]PRReviewEntry{}
	for _, entry := range got.Reviews {
		byReviewer[entry.Reviewer] = entry
	}
	if len(got.Reviews) != 2 {
		t.Fatalf("review summaries = %+v, want alice + bob", got.Reviews)
	}
	if a := byReviewer["alice"]; a.Verdict != domain.ReviewApproved || a.Body != "looks good now" || a.URL != "url-a-new" {
		t.Fatalf("alice entry = %+v, want latest approved with its body", a)
	}
	if b := byReviewer["bob"]; b.Verdict != domain.ReviewChangesRequest || b.Body != "please fix" {
		t.Fatalf("bob entry = %+v, want changes_requested with its body", b)
	}
}

func TestListPRSummariesExposesPRLifecycleTimes(t *testing.T) {
	st := newFakeStore()
	st.sessions["mer-1"] = domain.SessionRecord{ID: "mer-1", ProjectID: "mer", Kind: domain.KindWorker}
	createdAt := time.Date(2026, 6, 4, 9, 0, 0, 0, time.UTC)
	readyAt := time.Date(2026, 6, 4, 10, 30, 0, 0, time.UTC)
	mergedAt := time.Date(2026, 6, 4, 11, 0, 0, 0, time.UTC)
	observedAt := time.Date(2026, 6, 4, 12, 0, 0, 0, time.UTC)
	stList := &multiPRFakeStore{fakeStore: st, prs: []domain.PullRequest{
		{URL: "draft", SessionID: "mer-1", Number: 1, Draft: true, CreatedAtProvider: createdAt, UpdatedAt: observedAt},
		{URL: "ready", SessionID: "mer-1", Number: 2, StateChangedAt: readyAt, CreatedAtProvider: createdAt, UpdatedAt: observedAt},
		{URL: "merged", SessionID: "mer-1", Number: 3, Merged: true, CreatedAtProvider: createdAt, MergedAtProvider: mergedAt, UpdatedAt: observedAt},
	}}

	got, err := (&Service{store: stList}).ListPRSummaries(context.Background(), "mer-1")
	if err != nil {
		t.Fatal(err)
	}
	byURL := map[string]PRSummary{}
	for _, pr := range got {
		byURL[pr.URL] = pr
	}
	if !byURL["draft"].StateChangedAt.Equal(createdAt) {
		t.Fatalf("draft stateChangedAt = %s, want created time %s", byURL["draft"].StateChangedAt, createdAt)
	}
	if !byURL["ready"].StateChangedAt.Equal(readyAt) {
		t.Fatalf("ready stateChangedAt = %s, want ready/open transition %s", byURL["ready"].StateChangedAt, readyAt)
	}
	if !byURL["merged"].StateChangedAt.Equal(mergedAt) {
		t.Fatalf("merged stateChangedAt = %s, want merged time %s", byURL["merged"].StateChangedAt, mergedAt)
	}
	for _, url := range []string{"draft", "ready", "merged"} {
		if !byURL[url].CreatedAt.Equal(createdAt) {
			t.Fatalf("%s createdAt = %s, want provider creation time %s", url, byURL[url].CreatedAt, createdAt)
		}
	}
}

func TestListPRSummariesSuppressesFailingChecksUnlessCIFailing(t *testing.T) {
	st := newFakeStore()
	st.sessions["mer-1"] = domain.SessionRecord{ID: "mer-1", ProjectID: "mer", Kind: domain.KindWorker}
	prURL := "https://github.com/acme/repo/pull/8"
	stList := &multiPRFakeStore{fakeStore: st, prs: []domain.PullRequest{{
		URL:       prURL,
		SessionID: "mer-1",
		Number:    8,
		CI:        domain.CIPassing,
		HeadSHA:   "new-sha",
		UpdatedAt: time.Date(2026, 6, 4, 12, 0, 0, 0, time.UTC),
	}}}
	stList.checks[prURL] = []domain.PullRequestCheck{
		{Name: "copy-check", CommitHash: "old-sha", Status: domain.PRCheckFailed, Conclusion: "failure", URL: "https://github.com/acme/repo/actions/runs/1"},
	}

	got, err := (&Service{store: stList}).ListPRSummaries(context.Background(), "mer-1")
	if err != nil {
		t.Fatal(err)
	}
	if got[0].CI.State != domain.CIPassing || len(got[0].CI.FailingChecks) != 0 {
		t.Fatalf("ci summary = %+v", got[0].CI)
	}
}

func TestListPRSummariesFiltersFailedChecksToCurrentHead(t *testing.T) {
	st := newFakeStore()
	st.sessions["mer-1"] = domain.SessionRecord{ID: "mer-1", ProjectID: "mer", Kind: domain.KindWorker}
	prURL := "https://github.com/acme/repo/pull/9"
	stList := &multiPRFakeStore{fakeStore: st, prs: []domain.PullRequest{{
		URL:       prURL,
		SessionID: "mer-1",
		Number:    9,
		CI:        domain.CIFailing,
		HeadSHA:   "new-sha",
		UpdatedAt: time.Date(2026, 6, 4, 12, 0, 0, 0, time.UTC),
	}}}
	stList.checks[prURL] = []domain.PullRequestCheck{
		{Name: "old-copy-check", CommitHash: "old-sha", Status: domain.PRCheckFailed, Conclusion: "failure"},
		{Name: "current-lint", CommitHash: "new-sha", Status: domain.PRCheckFailed, Conclusion: "failure"},
	}

	got, err := (&Service{store: stList}).ListPRSummaries(context.Background(), "mer-1")
	if err != nil {
		t.Fatal(err)
	}
	checks := got[0].CI.FailingChecks
	if len(checks) != 1 || checks[0].Name != "current-lint" {
		t.Fatalf("failing checks = %+v", checks)
	}
}

func TestListPRSummariesSuppressesActiveDetailsForClosedOrMergedPRs(t *testing.T) {
	st := newFakeStore()
	st.sessions["mer-1"] = domain.SessionRecord{ID: "mer-1", ProjectID: "mer", Kind: domain.KindWorker}
	prURL := "https://github.com/acme/repo/pull/10"
	stList := &multiPRFakeStore{fakeStore: st, prs: []domain.PullRequest{{
		URL:                      prURL,
		SessionID:                "mer-1",
		Number:                   10,
		Merged:                   true,
		CI:                       domain.CIFailing,
		Review:                   domain.ReviewChangesRequest,
		Mergeability:             domain.MergeConflicting,
		ProviderMergeStateStatus: "dirty",
		UpdatedAt:                time.Date(2026, 6, 4, 12, 0, 0, 0, time.UTC),
	}}}
	stList.checks[prURL] = []domain.PullRequestCheck{{Name: "unit", Status: domain.PRCheckFailed}}
	stList.comments[prURL] = []domain.PullRequestComment{{Author: "reviewer-a", File: "main.go", Line: 12, URL: "https://github.com/acme/repo/pull/10#discussion_r1"}}

	got, err := (&Service{store: stList}).ListPRSummaries(context.Background(), "mer-1")
	if err != nil {
		t.Fatal(err)
	}
	pr := got[0]
	if pr.State != domain.PRStateMerged {
		t.Fatalf("state = %q", pr.State)
	}
	if len(pr.CI.FailingChecks) != 0 || len(pr.Review.UnresolvedBy) != 0 || len(pr.Mergeability.Reasons) != 0 {
		t.Fatalf("active details should be suppressed for merged PR: ci=%+v review=%+v merge=%+v", pr.CI, pr.Review, pr.Mergeability)
	}
}

func TestListPRSummariesOnlyEmitsMergeReasonsForBlockedStates(t *testing.T) {
	st := newFakeStore()
	st.sessions["mer-1"] = domain.SessionRecord{ID: "mer-1", ProjectID: "mer", Kind: domain.KindWorker}
	now := time.Date(2026, 6, 4, 12, 0, 0, 0, time.UTC)
	stList := &multiPRFakeStore{fakeStore: st, prs: []domain.PullRequest{
		{
			URL:                      "mergeable",
			SessionID:                "mer-1",
			Number:                   11,
			CI:                       domain.CIFailing,
			Review:                   domain.ReviewRequired,
			Mergeability:             domain.MergeMergeable,
			ProviderMergeStateStatus: "behind",
			UpdatedAt:                now,
		},
		{
			URL:                      "blocked",
			SessionID:                "mer-1",
			Number:                   12,
			Review:                   domain.ReviewRequired,
			Mergeability:             domain.MergeBlocked,
			ProviderMergeStateStatus: "behind",
			UpdatedAt:                now.Add(time.Minute),
		},
	}}

	got, err := (&Service{store: stList}).ListPRSummaries(context.Background(), "mer-1")
	if err != nil {
		t.Fatal(err)
	}
	byNumber := map[int]PRSummary{}
	for _, pr := range got {
		byNumber[pr.Number] = pr
	}
	if reasons := byNumber[11].Mergeability.Reasons; len(reasons) != 0 {
		t.Fatalf("mergeable reasons = %+v", reasons)
	}
	if reasons := byNumber[12].Mergeability.Reasons; !containsString(reasons, "behind_base") || !containsString(reasons, "review_required") {
		t.Fatalf("blocked reasons = %+v", reasons)
	}
}

func TestListPRSummariesCollapsesTransferredRepoAliases(t *testing.T) {
	st := newFakeStore()
	st.sessions["mer-1"] = domain.SessionRecord{ID: "mer-1", ProjectID: "mer", Kind: domain.KindWorker}
	now := time.Date(2026, 7, 31, 10, 0, 0, 0, time.UTC)
	oldURL := "https://github.com/AgentWrapper/agent-orchestrator/pull/3193"
	newURL := "https://github.com/Untrivial-ai/agent-orchestrator/pull/3193"
	stList := &multiPRFakeStore{fakeStore: st, prs: []domain.PullRequest{
		{
			URL:          oldURL,
			SessionID:    "mer-1",
			Number:       3193,
			Provider:     "github",
			Host:         "github.com",
			Repo:         "AgentWrapper/agent-orchestrator",
			SourceBranch: "ao/mer-1/fix-sigpipe",
			TargetBranch: "main",
			HeadSHA:      "same-head",
			Title:        "old alias",
			CI:           domain.CIFailing,
			UpdatedAt:    now,
		},
		{
			URL:          newURL,
			HTMLURL:      newURL,
			SessionID:    "mer-1",
			Number:       3193,
			Provider:     "github",
			Host:         "github.com",
			Repo:         "Untrivial-ai/agent-orchestrator",
			SourceBranch: "ao/mer-1/fix-sigpipe",
			TargetBranch: "main",
			HeadSHA:      "same-head",
			Title:        "new alias",
			CI:           domain.CIFailing,
			UpdatedAt:    now.Add(time.Minute),
		},
	}}
	stList.checks[oldURL] = []domain.PullRequestCheck{{
		Name: "old-check", CommitHash: "same-head", Status: domain.PRCheckFailed, Conclusion: "failure", URL: "https://ci.example/old",
	}}
	stList.checks[newURL] = []domain.PullRequestCheck{{
		Name: "new-check", CommitHash: "same-head", Status: domain.PRCheckFailed, Conclusion: "failure", URL: "https://ci.example/new",
	}}

	got, err := (&Service{store: stList}).ListPRSummaries(context.Background(), "mer-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("summaries = %d, want 1: %+v", len(got), got)
	}
	if got[0].URL != newURL || got[0].Title != "new alias" {
		t.Fatalf("summary primary = %q %q, want new alias", got[0].URL, got[0].Title)
	}
	if names := failingCheckNames(got[0].CI.FailingChecks); !sameStrings(names, []string{"old-check", "new-check"}) {
		t.Fatalf("failing checks = %v, want old and new alias checks", names)
	}
}

func TestDeduplicatePRFactsKeepsSameNumberAcrossDifferentRepos(t *testing.T) {
	now := time.Date(2026, 7, 31, 10, 0, 0, 0, time.UTC)
	got := deduplicatePRFacts([]domain.PRFacts{
		{
			URL:          "https://github.com/acme/api/pull/7",
			Number:       7,
			SourceBranch: "fix-tests",
			UpdatedAt:    now,
		},
		{
			URL:          "https://github.com/acme/web/pull/7",
			Number:       7,
			SourceBranch: "fix-tests",
			UpdatedAt:    now.Add(time.Minute),
		},
	})
	if len(got) != 2 {
		t.Fatalf("facts = %d, want distinct same-number PRs from different repos: %+v", len(got), got)
	}
}

func TestDeduplicatePRFactsKeepsSameBasenameAcrossDifferentOwners(t *testing.T) {
	now := time.Date(2026, 7, 31, 10, 0, 0, 0, time.UTC)
	got := deduplicatePRFacts([]domain.PRFacts{
		{
			URL:          "https://github.com/acme/repo/pull/7",
			Number:       7,
			SourceBranch: "fix-tests",
			TargetBranch: "main",
			HeadSHA:      "acme-head",
			UpdatedAt:    now,
		},
		{
			URL:          "https://github.com/other/repo/pull/7",
			Number:       7,
			SourceBranch: "fix-tests",
			TargetBranch: "main",
			HeadSHA:      "other-head",
			UpdatedAt:    now.Add(time.Minute),
		},
	})
	if len(got) != 2 {
		t.Fatalf("facts = %d, want distinct same-basename PRs from different owners: %+v", len(got), got)
	}
}

func TestDeduplicatePRFactsCollapsesTransferredRepoAliasesWithSameHead(t *testing.T) {
	now := time.Date(2026, 7, 31, 10, 0, 0, 0, time.UTC)
	got := deduplicatePRFacts([]domain.PRFacts{
		{
			URL:            "https://github.com/AgentWrapper/agent-orchestrator/pull/3193",
			Number:         3193,
			ReviewComments: true,
			SourceBranch:   "ao/mer-1/fix-sigpipe",
			TargetBranch:   "main",
			HeadSHA:        "same-head",
			UpdatedAt:      now,
		},
		{
			URL:          "https://github.com/Untrivial-ai/agent-orchestrator/pull/3193",
			Number:       3193,
			SourceBranch: "ao/mer-1/fix-sigpipe",
			TargetBranch: "main",
			HeadSHA:      "same-head",
			UpdatedAt:    now.Add(time.Minute),
		},
	})
	if len(got) != 1 {
		t.Fatalf("facts = %d, want transferred aliases collapsed: %+v", len(got), got)
	}
	if got[0].URL != "https://github.com/Untrivial-ai/agent-orchestrator/pull/3193" || !got[0].ReviewComments {
		t.Fatalf("merged facts = %+v, want newest URL and preserved comments", got[0])
	}
}

type multiPRFakeStore struct {
	*fakeStore
	prs []domain.PullRequest
}

func (f *multiPRFakeStore) ListPRsBySession(context.Context, domain.SessionID) ([]domain.PullRequest, error) {
	return f.prs, nil
}

func containsString(values []string, want string) bool {
	for _, got := range values {
		if got == want {
			return true
		}
	}
	return false
}

func failingCheckNames(checks []PRFailingCheck) []string {
	out := make([]string, 0, len(checks))
	for _, check := range checks {
		out = append(out, check.Name)
	}
	return out
}

func sameStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	seen := map[string]int{}
	for _, value := range got {
		seen[value]++
	}
	for _, value := range want {
		seen[value]--
		if seen[value] < 0 {
			return false
		}
	}
	return true
}
