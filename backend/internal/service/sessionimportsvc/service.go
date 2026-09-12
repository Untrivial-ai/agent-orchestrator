// Package sessionimportsvc turns an on-disk agent conversation, discovered by
// the sessionimport scanners, into a resumable AO chat session. It is the bridge
// between provider transcripts and AO's session/project services: it resolves
// the registered project the conversation ran in, then registers a dormant
// chat session bound to its native transcript. Agent startup is explicit.
package sessionimportsvc

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/sync/semaphore"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	projectsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/project"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/sessionimport"
)

// maxImportDisplayName matches the session display-name cap the rest of the app
// enforces, so an imported session's label looks native. Users can rename it.
const maxImportDisplayName = 20

// SessionService creates and reads AO sessions.
type SessionService interface {
	RegisterImport(context.Context, ports.SpawnConfig) (domain.Session, int, int, error)
	Get(context.Context, domain.SessionID) (domain.Session, error)
}

// SessionStore enumerates persisted sessions for the idempotency check.
type SessionStore interface {
	ListAllSessions(context.Context) ([]domain.SessionRecord, error)
}

// ProjectService lists the registered projects available for import.
type ProjectService interface {
	List(context.Context) ([]projectsvc.Summary, error)
}

var (
	// ErrImportSessionNotFound is returned when the requested native conversation
	// is absent or no longer eligible for this project.
	ErrImportSessionNotFound = errors.New("importable session not found")
	// ErrImportProjectUnresolved is returned when the selected project is
	// missing or the conversation is already imported into another project.
	ErrImportProjectUnresolved = errors.New("cannot resolve a project for the session working directory")
)

// Service discovers on-disk agent conversations and imports one as a resumable
// AO chat session.
type Service struct {
	disco    *sessionimport.Service
	sessions SessionService
	store    SessionStore
	projects ProjectService
	imports  *semaphore.Weighted
	search   *searchState
}

// New builds the import service over the given provider sources. Discovery flags
// already-imported conversations using the session store.
func New(sessions SessionService, store SessionStore, projects ProjectService, sources ...sessionimport.Source) *Service {
	s := &Service{sessions: sessions, store: store, projects: projects, imports: semaphore.NewWeighted(1)}
	s.disco = sessionimport.NewService(s.existingNativeIDs, sources...)
	return s
}

// DiscoveryWindowDays and MinimumTokens are the fixed import eligibility rules.
const DiscoveryWindowDays = 15

// MinimumTokens is the cumulative provider usage required for import.
const MinimumTokens int64 = 15_000

// Discover requires a registered project and limits both discovery and direct
// imports to recent conversations with enough recorded provider usage.
func (s *Service) Discover(ctx context.Context, _ sessionimport.DiscoverOptions, projectID domain.ProjectID) ([]sessionimport.ImportableSession, error) {
	opts, err := s.projectOptions(ctx, projectID)
	if err != nil {
		return nil, err
	}
	found, err := s.disco.Discover(ctx, opts)
	if err != nil {
		return nil, err
	}
	scoped := make([]sessionimport.ImportableSession, 0, len(found))
	for _, session := range found {
		if opts.IncludeCWD(session.CWD) && !session.LastActivity.Before(opts.Since) && session.TokenCount >= MinimumTokens {
			scoped = append(scoped, session)
		}
	}
	return scoped, nil
}

func (s *Service) projectOptions(ctx context.Context, projectID domain.ProjectID) (sessionimport.DiscoverOptions, error) {
	if strings.TrimSpace(string(projectID)) == "" {
		return sessionimport.DiscoverOptions{}, ErrImportProjectUnresolved
	}
	projects, err := s.projects.List(ctx)
	if err != nil {
		return sessionimport.DiscoverOptions{}, err
	}
	exists := false
	for _, p := range projects {
		if p.ID == projectID {
			exists = true
			break
		}
	}
	if !exists {
		return sessionimport.DiscoverOptions{}, ErrImportProjectUnresolved
	}
	// AO worktrees belong to their registered project even though they live
	// outside its main checkout. Resolve from durable records, without Git probes.
	records, err := s.store.ListAllSessions(ctx)
	if err != nil {
		return sessionimport.DiscoverOptions{}, err
	}
	for _, record := range records {
		if strings.TrimSpace(record.Metadata.WorkspacePath) != "" {
			projects = append(projects, projectsvc.Summary{ID: record.ProjectID, Path: record.Metadata.WorkspacePath})
		}
	}
	opts := sessionimport.DiscoverOptions{}
	opts.Since = time.Now().AddDate(0, 0, -DiscoveryWindowDays)
	opts.MinTokens = MinimumTokens
	opts.MaxPerProvider = 0
	opts.IncludeCWD = projectScope(projects, projectID)
	return opts, nil
}

// Import creates a resumable AO chat session from an existing provider
// conversation. It is idempotent: if a session already bound to nativeID exists,
// that session is returned with alreadyImported=true and nothing new is created.
func (s *Service) Import(ctx context.Context, provider domain.AgentHarness, nativeID string, projectID domain.ProjectID) (domain.Session, bool, error) {
	nativeID = strings.TrimSpace(nativeID)
	if nativeID == "" {
		return domain.Session{}, false, ErrImportSessionNotFound
	}

	// Serialize the check-and-create sequence, including direct API callers.
	// Acquiring is cancellable; no failed/abandoned request starts a later import.
	if err := s.imports.Acquire(ctx, 1); err != nil {
		return domain.Session{}, false, err
	}
	defer s.imports.Release(1)
	if strings.TrimSpace(string(projectID)) == "" {
		return domain.Session{}, false, ErrImportProjectUnresolved
	}
	if existing, ok, err := s.findExisting(ctx, provider, nativeID, projectID); err != nil {
		return domain.Session{}, false, err
	} else if ok {
		return existing, true, nil
	}
	opts, err := s.projectOptions(ctx, projectID)
	if err != nil {
		return domain.Session{}, false, err
	}
	target, ok, err := s.disco.Locate(ctx, provider, nativeID, opts)
	if err != nil {
		return domain.Session{}, false, err
	}
	if !ok || !opts.IncludeCWD(target.CWD) || target.LastActivity.Before(opts.Since) || target.TokenCount < MinimumTokens {
		return domain.Session{}, false, ErrImportSessionNotFound
	}

	session, err := s.registerTarget(ctx, target, projectID)
	return session, false, err
}

func (s *Service) registerTarget(ctx context.Context, target sessionimport.ImportableSession, projectID domain.ProjectID) (domain.Session, error) {
	session, _, _, err := s.sessions.RegisterImport(ctx, importConfig(target, projectID))
	if err != nil {
		return domain.Session{}, fmt.Errorf("import session: %w", err)
	}
	return session, nil
}

func importConfig(target sessionimport.ImportableSession, projectID domain.ProjectID) ports.SpawnConfig {
	return ports.SpawnConfig{
		ProjectID: projectID, Kind: domain.KindWorker, Harness: target.Provider,
		RequestedMode: domain.SessionModeChat, DisplayName: importDisplayName(target.Title),
		Branch: adoptableBranch(target.Branch),
		ResumeNativeSession: &ports.ResumeNativeSession{
			Provider: target.Provider, NativeSessionID: target.NativeSessionID,
			ConfigDir: target.ConfigDir, TranscriptPath: target.TranscriptPath,
			SourceBranch: strings.TrimSpace(target.Branch),
		},
	}
}

// existingNativeIDs collects the native ids AO already has a session for, used
// by discovery to flag duplicates.
func (s *Service) existingNativeIDs(ctx context.Context) (map[string]struct{}, error) {
	recs, err := s.store.ListAllSessions(ctx)
	if err != nil {
		return nil, err
	}
	return nativeIDSet(recs), nil
}

func (s *Service) findExisting(ctx context.Context, provider domain.AgentHarness, nativeID string, projectID domain.ProjectID) (domain.Session, bool, error) {
	recs, err := s.store.ListAllSessions(ctx)
	if err != nil {
		return domain.Session{}, false, err
	}
	for _, r := range recs {
		// A terminated (deleted) session must not block a fresh re-import: the
		// user expects "delete then import again" to produce a live session, with
		// the old one kept only as history.
		if r.IsTerminated || r.Harness != provider {
			continue
		}
		if r.Metadata.ProviderConversationID == nativeID || r.Metadata.AgentSessionID == nativeID {
			if r.ProjectID != projectID {
				return domain.Session{}, false, fmt.Errorf("%w: conversation already belongs to project %s", ErrImportProjectUnresolved, r.ProjectID)
			}
			sess, err := s.sessions.Get(ctx, r.ID)
			if err != nil {
				return domain.Session{}, false, err
			}
			return sess, true, nil
		}
	}
	return domain.Session{}, false, nil
}

func nativeIDSet(recs []domain.SessionRecord) map[string]struct{} {
	set := make(map[string]struct{}, len(recs))
	for _, r := range recs {
		// Terminated sessions do not count as "already imported": a deleted
		// import should reappear as importable, not greyed out.
		if r.IsTerminated {
			continue
		}
		if id := strings.TrimSpace(r.Metadata.ProviderConversationID); id != "" {
			set[sessionimport.NativeKey(r.Harness, id)] = struct{}{}
		}
		if id := strings.TrimSpace(r.Metadata.AgentSessionID); id != "" {
			set[sessionimport.NativeKey(r.Harness, id)] = struct{}{}
		}
	}
	return set
}

// bestProjectForDir returns the registered project whose path most specifically
// covers dir (exact match or nearest ancestor). Longest matching path wins so a
// nested project is preferred over its parent.
func bestProjectForDir(projects []projectsvc.Summary, dir string) (domain.ProjectID, bool) {
	dir = filepath.Clean(dir)
	var (
		bestID  domain.ProjectID
		bestLen = -1
	)
	for _, p := range projects {
		pp := filepath.Clean(strings.TrimSpace(p.Path))
		if strings.TrimSpace(p.Path) == "" || !filepath.IsAbs(p.Path) {
			continue
		}
		if pp == dir || dirIsAncestor(pp, dir) {
			if len(pp) > bestLen {
				bestID = p.ID
				bestLen = len(pp)
			}
		}
	}
	return bestID, bestLen >= 0
}

// dirIsAncestor reports whether parent is a strict ancestor directory of child.
func dirIsAncestor(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	if rel == "." || rel == ".." {
		return false
	}
	return !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// importDisplayName trims a provider title to the app's display-name cap.
func importDisplayName(title string) string {
	title = strings.TrimSpace(title)
	if utf8.RuneCountInString(title) <= maxImportDisplayName {
		return title
	}
	runes := []rune(title)
	return strings.TrimSpace(string(runes[:maxImportDisplayName-1])) + "…"
}

// defaultBranchNames are branches a session must never be checked out on. AO's
// model is one session per branch, and putting a worktree directly on the trunk
// would let session commits land there. A conversation recorded on the trunk
// therefore keeps AO's own fresh branch and simply forgoes pull-request
// association, which costs nothing: the trunk does not have a PR.
var defaultBranchNames = map[string]struct{}{
	"main": {}, "master": {}, "trunk": {}, "develop": {}, "development": {}, "default": {},
}

// adoptableBranch returns the branch an imported session may be created on, or
// "" to let AO mint its usual session branch.
//
// Claude records gitBranch "HEAD" for a detached checkout, and git rejects that
// as a branch name, so passing it through would fail the whole import. Anything
// that is not plainly a usable working branch is dropped rather than risked:
// losing the pull-request link degrades gracefully, a failed import does not.
func adoptableBranch(branch string) string {
	branch = strings.TrimSpace(branch)
	if branch == "" || branch == "HEAD" {
		return ""
	}
	if _, isDefault := defaultBranchNames[strings.ToLower(branch)]; isDefault {
		return ""
	}
	if !validBranchName(branch) {
		return ""
	}
	return branch
}

// validBranchName applies the parts of git's check-ref-format that matter here.
// It is deliberately conservative: a name this rejects only costs the PR link.
func validBranchName(branch string) bool {
	if strings.HasPrefix(branch, "-") || strings.HasPrefix(branch, "/") || strings.HasSuffix(branch, "/") {
		return false
	}
	if strings.HasSuffix(branch, ".") || strings.HasSuffix(branch, ".lock") {
		return false
	}
	if strings.Contains(branch, "..") || strings.Contains(branch, "@{") || strings.Contains(branch, "//") {
		return false
	}
	for _, r := range branch {
		if r <= 0x20 || r == 0x7f {
			return false
		}
		switch r {
		case '~', '^', ':', '?', '*', '[', '\\':
			return false
		}
	}
	return true
}
