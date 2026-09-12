package sessionimportsvc

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	projectsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/project"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/sessionimport"
)

type selectedStore struct {
	mu      sync.Mutex
	records []domain.SessionRecord
	creates int
	fail    bool
}

func (s *selectedStore) ListAllSessions(context.Context) ([]domain.SessionRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]domain.SessionRecord{}, s.records...), nil
}
func (s *selectedStore) Get(_ context.Context, id domain.SessionID) (domain.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.records {
		if r.ID == id {
			return domain.Session{SessionRecord: r}, nil
		}
	}
	return domain.Session{}, errors.New("missing")
}
func (s *selectedStore) RegisterImport(_ context.Context, c ports.SpawnConfig) (domain.Session, int, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail {
		return domain.Session{}, 0, 0, errors.New("injected session failure")
	}
	s.creates++
	r := domain.SessionRecord{ID: domain.SessionID("saved"), ProjectID: c.ProjectID, Harness: c.Harness, DisplayName: c.DisplayName, Metadata: domain.SessionMetadata{ProviderConversationID: c.ResumeNativeSession.NativeSessionID, NativeTranscriptPath: c.ResumeNativeSession.TranscriptPath, SourceBranch: c.ResumeNativeSession.SourceBranch}}
	s.records = append(s.records, r)
	return domain.Session{SessionRecord: r}, 0, 0, nil
}

type selectedProjects struct {
	fakeProjects
	creates int
}

func (p *selectedProjects) Add(_ context.Context, in projectsvc.AddInput) (projectsvc.Project, error) {
	p.creates++
	p.added = in
	p.list = append(p.list, projectsvc.Summary{ID: "new-project", Path: in.Path})
	return projectsvc.Project{ID: "new-project", Path: in.Path}, nil
}
func searchFixture(t *testing.T) (*Service, *selectedStore, *selectedProjects, string, string) {
	t.Helper()
	repo := t.TempDir()
	if out, err := exec.Command("git", "init", repo).CombinedOutput(); err != nil {
		t.Fatalf("git init: %s %v", out, err)
	}
	root := t.TempDir()
	path := filepath.Join(root, "projects", "repo", "11111111-1111-1111-1111-111111111111.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(map[string]any{"type": "user", "cwd": repo, "gitBranch": "feature/history", "timestamp": "2020-01-01T00:00:00Z", "message": map[string]string{"role": "user", "content": "Ancient small session"}})
	if err := os.WriteFile(path, append(data, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	store := &selectedStore{}
	projects := &selectedProjects{}
	svc := New(store, store, projects, sessionimport.NewClaudeSourceAt(root))
	if err := svc.EnableSearch(context.Background(), t.TempDir()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = svc.CloseSearch() })
	return svc, store, projects, path, repo
}
func refreshWait(t *testing.T, s *Service) {
	t.Helper()
	s.RefreshSearch()
	s.search.wg.Wait()
	if status := s.SearchStatus(); len(status.Errors) > 0 {
		t.Fatal(status.Errors)
	}
}
func TestSelectedOldSmallNewProjectExplicitAndConcurrentIdempotency(t *testing.T) {
	s, store, projects, path, repo := searchFixture(t)
	ctx := context.Background()
	page, err := s.Search(ctx, "ancient", 50, "")
	if err != nil || len(page.Results) != 0 {
		t.Fatal(page, err)
	}
	refreshWait(t, s)
	page, err = s.Search(ctx, "ancient", 50, "")
	if err != nil || len(page.Results) != 1 {
		t.Fatal(page, err)
	}
	id := page.Results[0].ID
	d, err := s.Destination(ctx, id, "")
	canonicalRepo, canonicalErr := filepath.EvalSymlinks(repo)
	if canonicalErr != nil {
		t.Fatal(canonicalErr)
	}
	if err != nil || d.Action != "add_project" || d.Path != canonicalRepo {
		t.Fatal(d, err)
	}
	if _, err = s.ImportSelected(ctx, id, SelectedInput{ConfirmationToken: d.ConfirmationToken}); err == nil || projects.creates != 0 {
		t.Fatal("new project lacked explicit confirmation")
	}
	var wg sync.WaitGroup
	for n := 0; n < 8; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := s.ImportSelected(ctx, id, SelectedInput{ConfirmationToken: d.ConfirmationToken, AddProject: true})
			if err != nil || result.SessionID == "" {
				t.Errorf("%+v %v", result, err)
			}
		}()
	}
	wg.Wait()
	if projects.creates != 1 || store.creates != 1 || projects.added.AsWorkspace {
		t.Fatal(projects.creates, store.creates)
	}
	if store.records[0].Metadata.SourceBranch != "feature/history" {
		t.Fatal(store.records)
	}
	if err = os.Remove(path); err != nil {
		t.Fatal(err)
	}
	refreshWait(t, s)
	result, err := s.ImportSelected(ctx, id, SelectedInput{})
	if err != nil || !result.AlreadyImported {
		t.Fatal(result, err)
	}
	d, err = s.Destination(ctx, id, "")
	if err != nil || d.Action != "open" {
		t.Fatal(d, err)
	}
}
func TestSelectedPartialProjectRegistrationRetry(t *testing.T) {
	s, store, projects, _, _ := searchFixture(t)
	ctx := context.Background()
	refreshWait(t, s)
	page, _ := s.Search(ctx, "", 50, "")
	id := page.Results[0].ID
	d, _ := s.Destination(ctx, id, "")
	store.fail = true
	result, err := s.ImportSelected(ctx, id, SelectedInput{ConfirmationToken: d.ConfirmationToken, AddProject: true})
	if err != nil || !result.ProjectCreated || result.ProjectID == "" || result.Error == "" {
		t.Fatal(result, err)
	}
	store.fail = false
	d, _ = s.Destination(ctx, id, "")
	if d.Action != "import" {
		t.Fatal(d)
	}
	result, err = s.ImportSelected(ctx, id, SelectedInput{ConfirmationToken: d.ConfirmationToken})
	if err != nil || result.SessionID == "" || projects.creates != 1 {
		t.Fatal(result, err, projects.creates)
	}
}
func TestSelectedMissingAndUnrelatedDestination(t *testing.T) {
	s, _, _, _, repo := searchFixture(t)
	refreshWait(t, s)
	page, _ := s.Search(context.Background(), "", 50, "")
	id := page.Results[0].ID
	d, err := s.Destination(context.Background(), id, t.TempDir())
	if err != nil || d.Action != "unavailable" {
		t.Fatal(d, err)
	}
	if err = os.RemoveAll(repo); err != nil {
		t.Fatal(err)
	}
	d, err = s.Destination(context.Background(), id, "")
	if err != nil || d.Action != "unavailable" {
		t.Fatal(d, err)
	}
}

// A controlled provider proves refresh coalescing and read availability while traversal is paused.
type pausedMetadata struct {
	*sessionimport.ClaudeSource
	started chan struct{}
	release chan struct{}
}

func (p *pausedMetadata) VisitMetadata(ctx context.Context, visit func(string, os.FileInfo) error) error {
	select {
	case p.started <- struct{}{}:
	default:
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-p.release:
		return p.ClaudeSource.VisitMetadata(ctx, visit)
	}
}
func TestRefreshCoalescesAndCancelsWithoutDeleting(t *testing.T) {
	s, _, _, _, _ := searchFixture(t)
	refreshWait(t, s)
	src := s.disco.Sources()[0].(*sessionimport.ClaudeSource)
	paused := &pausedMetadata{ClaudeSource: src, started: make(chan struct{}, 1), release: make(chan struct{})}
	s.disco = sessionimport.NewService(nil, paused)
	s.RefreshSearch()
	<-paused.started
	started := s.SearchStatus().StartedAt
	for n := 0; n < 5; n++ {
		if status := s.RefreshSearch(); status.StartedAt != started {
			t.Fatal("refresh did not coalesce")
		}
	}
	page, err := s.Search(context.Background(), "ancient", 50, "")
	if err != nil || len(page.Results) != 1 || !page.Status.Running {
		t.Fatal(page, err)
	}
	s.search.cancel()
	done := make(chan struct{})
	go func() { s.search.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("cancel did not stop scan")
	}
	page, err = s.Search(context.Background(), "ancient", 50, "")
	if err != nil || len(page.Results) != 1 || len(page.Status.Errors) == 0 {
		t.Fatal(page, err)
	}
}

func TestSelectedLocateMissingSubdirectoryUsesPersistedRepository(t *testing.T) {
	s, _, projects, path, repo := searchFixture(t)
	sub := filepath.Join(repo, "removed")
	if err := os.Mkdir(sub, 0700); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var line map[string]any
	if err = json.Unmarshal(raw, &line); err != nil {
		t.Fatal(err)
	}
	line["cwd"] = sub
	raw, _ = json.Marshal(line)
	if err = os.WriteFile(path, append(raw, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	refreshWait(t, s)
	page, _ := s.Search(context.Background(), "", 50, "")
	if err = os.Remove(sub); err != nil {
		t.Fatal(err)
	}
	d, err := s.Destination(context.Background(), page.Results[0].ID, repo)
	if err != nil || d.Action != "add_project" {
		t.Fatal(d, err)
	}
	result, err := s.ImportSelected(context.Background(), d.ID, SelectedInput{ConfirmationToken: d.ConfirmationToken, AddProject: true, LocateFolder: repo})
	if err != nil || result.SessionID == "" || projects.creates != 1 {
		t.Fatal(result, err)
	}
}

func TestSearchCodexGroupedSegmentsAndTitleIndexRemoval(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "sessions", "2020", "01", "01")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"old", "new"} {
		stamp := "2020-01-01T00:00:00Z"
		if name == "new" {
			stamp = "2020-01-02T00:00:00Z"
		}
		raw, _ := json.Marshal(map[string]any{"type": "session_meta", "timestamp": stamp, "payload": map[string]string{"id": name, "session_id": "root-thread", "cwd": "/missing"}})
		if err := os.WriteFile(filepath.Join(dir, "rollout-"+name+".jsonl"), append(raw, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
	}
	indexPath := filepath.Join(root, "session_index.jsonl")
	if err := os.WriteFile(indexPath, []byte("{\"id\":\"root-thread\",\"thread_name\":\"Named history\"}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	store := &selectedStore{}
	s := New(store, store, &selectedProjects{}, sessionimport.NewCodexSourceAt(root, true))
	if err := s.EnableSearch(context.Background(), t.TempDir()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.CloseSearch() }()
	refreshWait(t, s)
	page, err := s.Search(context.Background(), "", 50, "")
	if err != nil || len(page.Results) != 1 || page.Results[0].Title != "Named history" {
		t.Fatal(page, err)
	}
	refreshWait(t, s)
	if s.SearchStatus().Updated != 0 {
		t.Fatal("warm refresh reparsed files")
	}
	if err = os.WriteFile(indexPath, []byte("{\"id\":\"root-thread\",\"thread_name\":\"Renamed history\"}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	refreshWait(t, s)
	page, _ = s.Search(context.Background(), "renamed", 50, "")
	if len(page.Results) != 1 {
		t.Fatal(page)
	}
	if err = os.Remove(indexPath); err != nil {
		t.Fatal(err)
	}
	refreshWait(t, s)
	page, _ = s.Search(context.Background(), "renamed", 50, "")
	if len(page.Results) != 0 {
		t.Fatal("removed title survived", page)
	}
}

func (s *selectedStore) FindImportedSessions(ctx context.Context, ids []ports.ImportIdentity) ([]domain.SessionRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []domain.SessionRecord{}
	for _, id := range ids {
		target := sessionimport.ImportableSession{Provider: id.Provider, NativeSessionID: id.NativeSessionID, ConfigDir: id.ConfigDir}
		for _, r := range s.records {
			if matchesSource(r, target) {
				out = append(out, r)
				break
			}
		}
	}
	return out, nil
}

type boundedSearchStore struct {
	*selectedStore
	lookupSize int
}

func (s *boundedSearchStore) ListAllSessions(context.Context) ([]domain.SessionRecord, error) {
	return nil, errors.New("unbounded session enumeration forbidden")
}
func (s *boundedSearchStore) FindImportedSessions(ctx context.Context, ids []ports.ImportIdentity) ([]domain.SessionRecord, error) {
	s.lookupSize = len(ids)
	return s.selectedStore.FindImportedSessions(ctx, ids)
}
func TestSearchUsesBoundedDurableLookup(t *testing.T) {
	s, store, _, _, _ := searchFixture(t)
	refreshWait(t, s)
	bounded := &boundedSearchStore{selectedStore: store}
	s.store = bounded
	page, err := s.Search(context.Background(), "ancient", 1, "")
	if err != nil || len(page.Results) != 1 || bounded.lookupSize != 1 {
		t.Fatal(page, err, bounded.lookupSize)
	}
}
func TestDestinationPropagatesCacheErrors(t *testing.T) {
	s, _, _, _, _ := searchFixture(t)
	refreshWait(t, s)
	page, _ := s.Search(context.Background(), "", 1, "")
	if err := s.search.index.Close(); err != nil {
		t.Fatal(err)
	}
	if d, err := s.Destination(context.Background(), page.Results[0].ID, ""); err == nil {
		t.Fatal("cache failure masked as destination", d)
	}
}
func TestConcurrentCloseAndRefresh(t *testing.T) {
	for n := 0; n < 10; n++ {
		store := &selectedStore{}
		s := New(store, store, &selectedProjects{})
		if err := s.EnableSearch(context.Background(), t.TempDir()); err != nil {
			t.Fatal(err)
		}
		start := make(chan struct{})
		done := make(chan struct{})
		go func() {
			defer close(done)
			<-start
			for j := 0; j < 5; j++ {
				s.RefreshSearch()
			}
		}()
		close(start)
		if err := s.CloseSearch(); err != nil {
			t.Fatal(err)
		}
		<-done
		if s.SearchStatus().Running {
			t.Fatal("refresh outlived shutdown")
		}
	}
}

func TestBulkRootAliasSearchAndSelectedRetry(t *testing.T) {
	s, store, projects, path, repo := searchFixture(t)
	ctx := context.Background()
	root := filepath.Dir(filepath.Dir(filepath.Dir(path)))
	alias := filepath.Join(t.TempDir(), "claude-alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var header map[string]any
	if err = json.Unmarshal(raw, &header); err != nil {
		t.Fatal(err)
	}
	header["timestamp"] = time.Now().UTC().Format(time.RFC3339Nano)
	raw, _ = json.Marshal(header)
	raw = append(raw, []byte("\n{\"type\":\"assistant\",\"message\":{\"id\":\"message\",\"usage\":{\"input_tokens\":15000}}}\n")...)
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	projects.list = []projectsvc.Summary{{ID: "existing", Path: repo}}
	s.disco = sessionimport.NewService(s.existingNativeIDs, sessionimport.NewClaudeSourceAt(alias))
	native := "11111111-1111-1111-1111-111111111111"
	saved, already, err := s.Import(ctx, domain.HarnessClaudeCode, native, "existing")
	if err != nil || already {
		t.Fatal(saved, already, err)
	}
	if !strings.HasPrefix(store.records[0].Metadata.NativeTranscriptPath, alias) {
		t.Fatal("bulk did not preserve alias fixture", store.records)
	}
	refreshWait(t, s)
	page, err := s.Search(ctx, "ancient", 50, "")
	if err != nil || len(page.Results) != 1 || page.Results[0].SessionID != string(saved.ID) {
		t.Fatal(page, err)
	}
	id := page.Results[0].ID
	// Removing the transcript and project shard must not erase root-alias identity.
	if err = os.RemoveAll(filepath.Dir(path)); err != nil {
		t.Fatal(err)
	}
	d, err := s.Destination(ctx, id, "")
	if err != nil || d.Action != "open" {
		t.Fatal(d, err)
	}
	result, err := s.ImportSelected(ctx, id, SelectedInput{})
	if err != nil || !result.AlreadyImported || store.creates != 1 {
		t.Fatal(result, err, store.creates)
	}
}

func TestOpenDestinationKeepsMetadataWithoutSource(t *testing.T) {
	s, _, _, path, _ := searchFixture(t)
	ctx := context.Background()
	title := "A complete provider title that is longer than the imported display name"
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]string{"type": "custom-title", "customTitle": title})
	if _, err = f.Write(append(raw, '\n')); err != nil {
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	refreshWait(t, s)
	page, err := s.Search(ctx, "complete provider", 50, "")
	if err != nil || len(page.Results) != 1 {
		t.Fatal(page, err)
	}
	id := page.Results[0].ID
	preview, err := s.Destination(ctx, id, "")
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.ImportSelected(ctx, id, SelectedInput{ConfirmationToken: preview.ConfirmationToken, AddProject: true})
	if err != nil || result.SessionID == "" {
		t.Fatal(result, err)
	}
	for _, missing := range []bool{false, true} {
		if missing {
			if err = os.Remove(path); err != nil {
				t.Fatal(err)
			}
		}
		d, err := s.Destination(ctx, id, "")
		if err != nil || d.Action != "open" || d.Title != title || d.Provider != "claude-code" || d.Path != preview.Path || d.SessionID != result.SessionID {
			t.Fatal(missing, d, err)
		}
	}
	refreshWait(t, s)
	d, err := s.Destination(ctx, id, "")
	if err != nil || d.Action != "open" || d.Title == "" || d.Provider != "claude-code" || d.Path != preview.Path {
		t.Fatal("durable fallback after index deletion", d, err)
	}
}
