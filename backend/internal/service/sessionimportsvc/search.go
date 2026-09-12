package sessionimportsvc

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	projectsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/project"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/sessionimport"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/importindex"
)

var ErrInvalidSearch = importindex.ErrInvalidQuery
var ErrDestinationConfirmation = errors.New("destination confirmation required")

type SearchStatus struct {
	Running     bool     `json:"running"`
	Scanned     int      `json:"scanned"`
	Updated     int      `json:"updated"`
	StartedAt   string   `json:"startedAt,omitempty"`
	CompletedAt string   `json:"completedAt,omitempty"`
	Errors      []string `json:"errors"`
}
type SearchResult struct {
	ID           string `json:"id"`
	Title        string `json:"title"`
	Provider     string `json:"provider"`
	LastActivity string `json:"lastActivity"`
	FolderHint   string `json:"folderHint,omitempty"`
	SessionID    string `json:"sessionId,omitempty"`
	ProjectID    string `json:"projectId,omitempty"`
}
type SearchPage struct {
	Results    []SearchResult `json:"results"`
	NextCursor string         `json:"nextCursor,omitempty"`
	Status     SearchStatus   `json:"status"`
}
type Destination struct {
	ID                string `json:"id"`
	Title             string `json:"title"`
	Provider          string `json:"provider"`
	Action            string `json:"action" enum:"import,add_project,open,unavailable"`
	ProjectID         string `json:"projectId,omitempty"`
	SessionID         string `json:"sessionId,omitempty"`
	Path              string `json:"path,omitempty"`
	SourceCWD         string `json:"sourceCwd,omitempty"`
	Reason            string `json:"reason,omitempty"`
	ConfirmationToken string `json:"confirmationToken,omitempty"`
}
type SelectedInput struct {
	ConfirmationToken string `json:"confirmationToken"`
	AddProject        bool   `json:"addProject"`
	LocateFolder      string `json:"locateFolder,omitempty"`
}
type SelectedResult struct {
	SessionID       string `json:"sessionId,omitempty"`
	ProjectID       string `json:"projectId,omitempty"`
	AlreadyImported bool   `json:"alreadyImported"`
	ProjectCreated  bool   `json:"projectCreated"`
	Error           string `json:"error,omitempty"`
}
type searchState struct {
	index   *importindex.Index
	ctx     context.Context
	cancel  context.CancelFunc
	mu      sync.Mutex
	titleMu sync.Mutex
	status  SearchStatus
	wg      sync.WaitGroup
}

// EnableSearch opens the rebuildable cache. Ordinary reads never start discovery.
func (s *Service) EnableSearch(ctx context.Context, dir string) error {
	index, err := importindex.Open(dir)
	if err != nil {
		return err
	}
	incomplete, err := index.Incomplete(ctx)
	if err != nil {
		_ = index.Close()
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	s.search = &searchState{index: index, ctx: ctx, cancel: cancel, status: SearchStatus{Errors: []string{}}}
	if incomplete {
		s.search.status.Errors = append(s.search.status.Errors, "Previous discovery was incomplete. Refresh to retry.")
	}
	return nil
}
func (s *Service) CloseSearch() error {
	if s.search == nil {
		return nil
	}
	s.search.mu.Lock()
	s.search.cancel()
	s.search.mu.Unlock()
	s.search.wg.Wait()
	return s.search.index.Close()
}
func (s *Service) SearchStatus() SearchStatus {
	if s.search == nil {
		return SearchStatus{Errors: []string{"Search is unavailable"}}
	}
	state := s.search
	state.mu.Lock()
	defer state.mu.Unlock()
	status := state.status
	status.Errors = append([]string{}, status.Errors...)
	return status
}
func (s *Service) RefreshSearch() SearchStatus {
	state := s.search
	if state == nil {
		return s.SearchStatus()
	}
	state.mu.Lock()
	if state.status.Running || state.ctx.Err() != nil {
		state.mu.Unlock()
		return s.SearchStatus()
	}
	state.status = SearchStatus{Running: true, StartedAt: time.Now().UTC().Format(time.RFC3339Nano), Errors: []string{}}
	state.wg.Add(1)
	state.mu.Unlock()
	go s.refreshSearch()
	return s.SearchStatus()
}
func (s *Service) refreshSearch() {
	state := s.search
	defer state.wg.Done()
	generation := strconv.FormatInt(time.Now().UnixNano(), 10)
	recordError := func(err error) {
		if err == nil {
			return
		}
		state.mu.Lock()
		state.status.Errors = append(state.status.Errors, err.Error())
		state.mu.Unlock()
	}
	for _, source := range s.disco.Sources() {
		src, ok := source.(sessionimport.MetadataSource)
		if !ok {
			recordError(fmt.Errorf("%s does not support metadata search", source.Provider()))
			continue
		}
		root, err := src.MetadataRoot()
		if err != nil {
			recordError(err)
			continue
		}
		root = string(src.Provider()) + "\x00" + root
		if err := state.index.MarkScan(state.ctx, root, true); err != nil {
			recordError(err)
			continue
		}
		titleErr := s.refreshTitles(state.ctx, src, root)
		scanErr := src.VisitMetadata(state.ctx, func(path string, info os.FileInfo) error {
			cached, err := state.index.Seen(state.ctx, root, path, info.Size(), info.ModTime().UnixNano(), generation)
			if err != nil {
				return err
			}
			state.mu.Lock()
			state.status.Scanned++
			state.mu.Unlock()
			if cached {
				return nil
			}
			value, ok, err := src.ReadMetadata(state.ctx, path)
			if err != nil {
				return err
			}
			if !ok {
				return nil
			}
			value.RepositoryCommonDir = gitCommonDir(value.CWD)
			err = state.index.Put(state.ctx, root, path, info.Size(), info.ModTime().UnixNano(), generation, value)
			if err == nil {
				state.mu.Lock()
				state.status.Updated++
				state.mu.Unlock()
			}
			return err
		})
		err = errors.Join(titleErr, scanErr, state.ctx.Err())
		if err == nil {
			err = state.index.Complete(state.ctx, root, generation)
			if err == nil {
				err = state.index.MarkScan(state.ctx, root, false)
			}
		}
		recordError(err)
	}
	state.mu.Lock()
	state.status.Running = false
	state.status.CompletedAt = time.Now().UTC().Format(time.RFC3339Nano)
	state.mu.Unlock()
}
func (s *Service) Search(ctx context.Context, query string, limit int, cursor string) (SearchPage, error) {
	if s.search == nil {
		return SearchPage{}, fmt.Errorf("search unavailable")
	}
	offset := 0
	if cursor != "" {
		var err error
		offset, err = strconv.Atoi(cursor)
		if err != nil {
			return SearchPage{}, fmt.Errorf("%w: invalid cursor", ErrInvalidSearch)
		}
	}
	results, more, err := s.search.index.Search(ctx, query, limit, offset)
	if err != nil {
		return SearchPage{}, err
	}
	page := SearchPage{Results: make([]SearchResult, 0, len(results)), Status: s.SearchStatus()}
	lookup, ok := s.store.(interface {
		FindImportedSessions(context.Context, []ports.ImportIdentity) ([]domain.SessionRecord, error)
	})
	if !ok && len(results) > 0 {
		return SearchPage{}, fmt.Errorf("targeted import lookup unavailable")
	}
	identities := make([]ports.ImportIdentity, 0, len(results))
	for _, r := range results {
		identities = append(identities, ports.ImportIdentity{Provider: r.Session.Provider, NativeSessionID: r.Session.NativeSessionID, ConfigDir: r.Session.ConfigDir})
	}
	records := []domain.SessionRecord{}
	if len(identities) > 0 {
		records, err = lookup.FindImportedSessions(ctx, identities)
		if err != nil {
			return SearchPage{}, err
		}
	}

	for _, r := range results {
		v := SearchResult{ID: r.ID, Title: r.Session.Title, Provider: string(r.Session.Provider), LastActivity: r.Session.LastActivity.UTC().Format(time.RFC3339Nano), FolderHint: filepath.Base(r.Session.CWD)}
		for _, record := range records {
			if matchesSource(record, r.Session) {
				v.SessionID = string(record.ID)
				v.ProjectID = string(record.ProjectID)
				break
			}
		}

		page.Results = append(page.Results, v)
	}
	if more {
		page.NextCursor = strconv.Itoa(offset + len(results))
	}
	return page, nil
}
func matchesSource(r domain.SessionRecord, target sessionimport.ImportableSession) bool {
	if r.IsTerminated || r.Harness != target.Provider || (r.Metadata.ProviderConversationID != target.NativeSessionID && r.Metadata.AgentSessionID != target.NativeSessionID) {
		return false
	}
	path := r.Metadata.NativeTranscriptPath
	if path == "" {
		return false
	} // Unknown roots must never make different source profiles collide.
	rel, err := filepath.Rel(target.ConfigDir, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	parts := strings.Split(rel, string(filepath.Separator))
	if target.Provider == domain.HarnessClaudeCode {
		return len(parts) == 3 && parts[0] == "projects"
	}
	if target.Provider == domain.HarnessCodex {
		return len(parts) == 5 && (parts[0] == "sessions" || parts[0] == "archived_sessions")
	}
	return false
}
func (s *Service) existingSelected(ctx context.Context, id string) (domain.Session, bool, error) {
	records, err := s.store.ListAllSessions(ctx)
	if err != nil {
		return domain.Session{}, false, err
	}
	for _, src := range s.disco.Sources() {
		source, ok := src.(sessionimport.MetadataSource)
		if !ok {
			continue
		}
		root, err := source.MetadataRoot()
		if err != nil {
			continue
		}
		for _, r := range records {
			for _, native := range []string{r.Metadata.ProviderConversationID, r.Metadata.AgentSessionID} {
				target := sessionimport.ImportableSession{Provider: src.Provider(), NativeSessionID: native, ConfigDir: root}
				if native != "" && importindex.ID(string(src.Provider()), string(src.Provider())+"\x00"+root, native) == id && matchesSource(r, target) {
					session, err := s.sessions.Get(ctx, r.ID)
					return session, true, err
				}
			}
		}
	}
	return domain.Session{}, false, nil
}
func (s *Service) selected(ctx context.Context, id string) (sessionimport.ImportableSession, error) {
	if s.search == nil {
		return sessionimport.ImportableSession{}, ErrImportSessionNotFound
	}
	r, err := s.search.index.Get(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return sessionimport.ImportableSession{}, ErrImportSessionNotFound
	}
	if err != nil {
		return sessionimport.ImportableSession{}, err
	}
	for _, src := range s.disco.Sources() {
		source, ok := src.(sessionimport.MetadataSource)
		if !ok || src.Provider() != r.Session.Provider {
			continue
		}
		root, err := source.MetadataRoot()
		if err != nil || root != r.Session.ConfigDir {
			continue
		}
		fresh, ok, err := source.ReadMetadata(ctx, r.Session.TranscriptPath)
		if err != nil {
			return sessionimport.ImportableSession{}, err
		}
		if !ok || fresh.NativeSessionID != r.Session.NativeSessionID {
			return sessionimport.ImportableSession{}, ErrImportSessionNotFound
		}
		fresh.RepositoryCommonDir = r.Session.RepositoryCommonDir
		if fresh.Provider == domain.HarnessCodex {
			key := string(src.Provider()) + "\x00" + root
			if err := s.refreshTitles(ctx, source, key); err != nil {
				return sessionimport.ImportableSession{}, err
			}
			cached, err := s.search.index.Get(ctx, id)
			if err != nil {
				return sessionimport.ImportableSession{}, err
			}
			fresh.Title = cached.Session.Title
		}

		return fresh, nil
	}
	return sessionimport.ImportableSession{}, ErrImportSessionNotFound
}
func (s *Service) Destination(ctx context.Context, id, locate string) (Destination, error) {
	if existing, ok, err := s.existingSelected(ctx, id); err != nil {
		return Destination{}, err
	} else if ok {
		return Destination{ID: id, Action: "open", SessionID: string(existing.ID), ProjectID: string(existing.ProjectID)}, nil
	}
	target, err := s.selected(ctx, id)
	if err != nil {
		if !errors.Is(err, ErrImportSessionNotFound) && !errors.Is(err, os.ErrNotExist) && !errors.Is(err, sessionimport.ErrInvalidMetadataSource) {
			return Destination{}, err
		}
		return Destination{ID: id, Action: "unavailable", Reason: "The source history is unavailable. Restore its original location and refresh."}, nil
	}
	return s.destination(ctx, id, target, locate)
}
func (s *Service) destination(ctx context.Context, id string, target sessionimport.ImportableSession, locate string) (Destination, error) {
	d := Destination{ID: id, Title: target.Title, Provider: string(target.Provider), SourceCWD: target.CWD, Action: "unavailable"}
	cwd := target.CWD
	if !filepath.IsAbs(cwd) {
		d.Reason = "This conversation has no repository working directory. Projectless import is not available yet."
		return d, nil
	}
	common := gitCommonDir(cwd)
	if _, err := os.Stat(cwd); err != nil {
		common = target.RepositoryCommonDir
		if locate == "" {
			d.Reason = "The original working directory is missing. Locate another checkout of the same repository."
			return d, nil
		}
	}
	if common == "" {
		d.Reason = "The source folder is not an available Git repository. Restore the original checkout and refresh."
		return d, nil
	}
	if locate != "" {
		if !filepath.IsAbs(locate) || gitCommonDir(locate) != common {
			d.Reason = "Choose a folder in the same repository as the source conversation."
			return d, nil
		}
		if info, err := os.Stat(locate); err != nil || !info.IsDir() {
			d.Reason = "Choose an existing repository folder."
			return d, nil
		}
		cwd = locate
	}

	projects, err := s.projects.List(ctx)
	if err != nil {
		return d, err
	}
	for _, p := range projects {
		if gitCommonDir(p.Path) == common {
			if d.ProjectID != "" && d.ProjectID != string(p.ID) {
				d.Reason = "Multiple registered projects refer to this repository. Resolve the duplicate registrations first."
				d.ProjectID = ""
				return d, nil
			}
			d.ProjectID = string(p.ID)
			d.Path = p.Path
		}
	}
	if d.ProjectID != "" {
		d.Action = "import"
	} else { // Linked worktrees share the owning checkout's .git directory.
		if filepath.Base(common) != ".git" {
			d.Reason = "The repository uses a separate Git directory. Register its main checkout first."
			return d, nil
		}
		d.Path = filepath.Dir(common)
		if gitCommonDir(d.Path) != common {
			d.Reason = "The owning checkout is missing. Restore it before importing."
			return d, nil
		}
		d.Action = "add_project"
	}
	d.ConfirmationToken = importindex.ID(id, common, d.Path)
	return d, nil
}
func (s *Service) ImportSelected(ctx context.Context, id string, in SelectedInput) (SelectedResult, error) {
	if err := s.imports.Acquire(ctx, 1); err != nil {
		return SelectedResult{}, err
	}
	defer s.imports.Release(1)
	if session, ok, err := s.existingSelected(ctx, id); err != nil {
		return SelectedResult{}, err
	} else if ok {
		return SelectedResult{SessionID: string(session.ID), ProjectID: string(session.ProjectID), AlreadyImported: true}, nil
	}
	target, err := s.selected(ctx, id)
	if err != nil {
		return SelectedResult{}, err
	}
	d, err := s.destination(ctx, id, target, in.LocateFolder)
	if err != nil {
		return SelectedResult{}, err
	}
	if d.Action == "unavailable" || d.ConfirmationToken == "" || d.ConfirmationToken != in.ConfirmationToken {
		return SelectedResult{}, fmt.Errorf("%w: destination changed or unavailable; preview and confirm again", ErrDestinationConfirmation)
	}
	result := SelectedResult{ProjectID: d.ProjectID}
	if d.Action == "add_project" {
		if !in.AddProject {
			return result, fmt.Errorf("%w: explicit addProject confirmation is required", ErrDestinationConfirmation)
		}
		adder, ok := s.projects.(interface {
			Add(context.Context, projectsvc.AddInput) (projectsvc.Project, error)
		})
		if !ok {
			return result, fmt.Errorf("project registration unavailable")
		}
		p, err := adder.Add(ctx, projectsvc.AddInput{Path: d.Path})
		if err != nil {
			return result, err
		}
		result.ProjectID = string(p.ID)
		result.ProjectCreated = true
	}
	session, err := s.registerTarget(ctx, target, domain.ProjectID(result.ProjectID))
	if err != nil {
		result.Error = err.Error()
		return result, nil
	}
	result.SessionID = string(session.ID)
	return result, nil
}

func (s *Service) refreshTitles(ctx context.Context, src sessionimport.MetadataSource, key string) error {
	if src.Provider() != domain.HarnessCodex {
		return nil
	}
	s.search.titleMu.Lock()
	defer s.search.titleMu.Unlock()
	root, err := src.MetadataRoot()
	if err != nil {
		return err
	}
	info, err := os.Stat(filepath.Join(root, "session_index.jsonl"))
	var size, mtime int64
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if info != nil {
		size = info.Size()
		mtime = info.ModTime().UnixNano()
	}
	unchanged, err := s.search.index.TitlesUnchanged(ctx, key, size, mtime)
	if err != nil || unchanged {
		return err
	}
	if err = s.search.index.BeginTitles(ctx, key); err != nil {
		return err
	}
	if info != nil {
		if err = src.VisitTitles(ctx, func(id, title string) error {
			if err := s.search.index.SeenTitle(ctx, key, id); err != nil {
				return err
			}
			return s.search.index.Title(ctx, key, id, title)
		}); err != nil {
			return err
		}
	}
	if err = s.search.index.CompleteTitles(ctx, key); err != nil {
		return err
	}
	return s.search.index.MarkTitles(ctx, key, size, mtime)
}
