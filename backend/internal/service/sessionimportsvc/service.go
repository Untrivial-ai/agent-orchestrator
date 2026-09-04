// Package sessionimportsvc turns an on-disk agent conversation, discovered by
// the sessionimport scanners, into a resumable AO chat session. It is the bridge
// between provider transcripts and AO's session/project services: it resolves
// (or registers) the project the conversation ran in, then spawns a chat session
// bound to the provider's native id so the prior transcript is replayed.
package sessionimportsvc

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

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
	Spawn(context.Context, ports.SpawnConfig) (domain.Session, int, int, error)
	Get(context.Context, domain.SessionID) (domain.Session, error)
}

// SessionStore enumerates persisted sessions for the idempotency check.
type SessionStore interface {
	ListAllSessions(context.Context) ([]domain.SessionRecord, error)
}

// ProjectService resolves or registers the project an imported conversation
// lands in.
type ProjectService interface {
	List(context.Context) ([]projectsvc.Summary, error)
	Add(context.Context, projectsvc.AddInput) (projectsvc.Project, error)
}

var (
	// ErrImportSessionNotFound is returned when the requested native conversation
	// is no longer on disk.
	ErrImportSessionNotFound = errors.New("importable session not found")
	// ErrImportProjectUnresolved is returned when no project covers the
	// conversation's working directory and one cannot be created (e.g. the
	// directory is not a git repository with a commit).
	ErrImportProjectUnresolved = errors.New("cannot resolve a project for the session working directory")
)

// Service discovers on-disk agent conversations and imports one as a resumable
// AO chat session.
type Service struct {
	disco      *sessionimport.Service
	sessions   SessionService
	store      SessionStore
	projects   ProjectService
	classifier *classifier
	// excludeRoots are directories whose conversations are never importable.
	// AO's own data directory is one: classification asks the user's agent a
	// question, and some CLIs record that as a conversation.
	excludeRoots []string
}

// New builds the import service over the given provider sources. Discovery flags
// already-imported conversations using the session store.
func New(sessions SessionService, store SessionStore, projects ProjectService, sources ...sessionimport.Source) *Service {
	s := &Service{sessions: sessions, store: store, projects: projects}
	s.disco = sessionimport.NewService(s.existingNativeIDs, sources...)
	return s
}

// WithClassification lets the service settle conversations the local heuristic
// could not place, by asking the user's own authorized agent.
//
// It is opt-in at construction so the daemon can leave it off, and so tests get
// a service that never shells out. dataDir is where the verdict cache lives and
// where the classifier runs, and it is excluded from discovery for that reason.
func (s *Service) WithClassification(agents AgentRegistry, dataDir string, logger *slog.Logger) *Service {
	if agents == nil || strings.TrimSpace(dataDir) == "" {
		return s
	}
	if logger == nil {
		logger = slog.Default()
	}
	workDir := filepath.Join(dataDir, "classifier")
	// The directory must exist before a CLI is asked to run there.
	if err := os.MkdirAll(workDir, 0o750); err != nil {
		logger.Warn("session import: no classifier working directory; keeping local classification only", "error", err)
		return s
	}
	s.classifier = &classifier{
		agents:  agents,
		cache:   newVerdictCache(dataDir),
		workDir: workDir,
		logger:  logger,
	}
	s.excludeRoots = append(s.excludeRoots, dataDir)
	return s
}

// Discover lists importable conversations across every provider. A non-empty
// projectID narrows the list to conversations that ran inside that project, so
// a project's own settings can offer just its history instead of everything on
// the machine.
func (s *Service) Discover(ctx context.Context, opts sessionimport.DiscoverOptions, projectID domain.ProjectID) ([]sessionimport.ImportableSession, error) {
	opts.ExcludeRoots = append(opts.ExcludeRoots, s.excludeRoots...)

	// A project's listing narrows the scan itself rather than scanning every
	// conversation on the machine and discarding almost all of it. Resolving the
	// project needs only the working directory, which the head read already
	// gives, so out-of-scope transcripts are dropped before the full read.
	var projects []projectsvc.Summary
	if projectID != "" {
		var err error
		if projects, err = s.projects.List(ctx); err != nil {
			return nil, err
		}
		opts.IncludeCWD = func(cwd string) bool {
			id, ok := bestProjectForDir(projects, cwd)
			return ok && id == projectID
		}
	}

	found, err := s.disco.Discover(ctx, opts)
	if err != nil {
		return found, err
	}
	// Settle what the local heuristic could not, before scoping: a conversation
	// judged trivial should not reach any surface, project-scoped or not.
	if !opts.IncludeTrivial {
		found = s.classifier.resolve(found)
	}
	if projectID == "" {
		return found, nil
	}

	// Resolve each conversation the same way import does, so what a project
	// lists is exactly what importing from it would produce. Matching the most
	// specific project means a nested repo's conversations belong to the nested
	// project, not to its parent.
	scoped := make([]sessionimport.ImportableSession, 0, len(found))
	for _, session := range found {
		if id, ok := bestProjectForDir(projects, session.CWD); ok && id == projectID {
			scoped = append(scoped, session)
		}
	}
	return scoped, nil
}

// Import creates a resumable AO chat session from an existing provider
// conversation. It is idempotent: if a session already bound to nativeID exists,
// that session is returned with alreadyImported=true and nothing new is created.
func (s *Service) Import(ctx context.Context, provider domain.AgentHarness, nativeID string) (domain.Session, bool, error) {
	nativeID = strings.TrimSpace(nativeID)
	if nativeID == "" {
		return domain.Session{}, false, ErrImportSessionNotFound
	}

	if existing, ok, err := s.findExisting(ctx, nativeID); err != nil {
		return domain.Session{}, false, err
	} else if ok {
		return existing, true, nil
	}

	target, ok, err := s.disco.Locate(ctx, provider, nativeID)
	if err != nil {
		return domain.Session{}, false, err
	}
	if !ok {
		return domain.Session{}, false, ErrImportSessionNotFound
	}

	projectID, err := s.resolveProject(ctx, target.CWD)
	if err != nil {
		return domain.Session{}, false, err
	}

	session, _, _, err := s.sessions.Spawn(ctx, ports.SpawnConfig{
		ProjectID:     projectID,
		Kind:          domain.KindWorker,
		Harness:       provider,
		RequestedMode: domain.SessionModeChat,
		DisplayName:   importDisplayName(target.Title),
		// The branch the conversation ran on, so AO's existing SCM observer
		// discovers its pull request and the reducer places the session in
		// review / ready to merge / merged on its own. No display state is
		// invented or persisted: only the repository fact is recorded. Empty
		// when the recorded branch is not one a session may own, in which case
		// the session gets AO's usual fresh branch.
		Branch: adoptableBranch(target.Branch),
		ResumeNativeSession: &ports.ResumeNativeSession{
			Provider:        provider,
			NativeSessionID: nativeID,
			ConfigDir:       target.ConfigDir,
			// The transcript's own branch, kept even when the session has to be
			// created on a different one.
			SourceBranch: strings.TrimSpace(target.Branch),
		},
	})
	if err != nil {
		return domain.Session{}, false, fmt.Errorf("import session: %w", err)
	}
	return session, false, nil
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

func (s *Service) findExisting(ctx context.Context, nativeID string) (domain.Session, bool, error) {
	recs, err := s.store.ListAllSessions(ctx)
	if err != nil {
		return domain.Session{}, false, err
	}
	for _, r := range recs {
		// A terminated (deleted) session must not block a fresh re-import: the
		// user expects "delete then import again" to produce a live session, with
		// the old one kept only as history.
		if r.IsTerminated {
			continue
		}
		if r.Metadata.ProviderConversationID == nativeID || r.Metadata.AgentSessionID == nativeID {
			sess, err := s.sessions.Get(ctx, r.ID)
			if err != nil {
				return domain.Session{}, false, err
			}
			return sess, true, nil
		}
	}
	return domain.Session{}, false, nil
}

// resolveProject finds a registered project covering cwd, or registers the git
// repository containing cwd as a new project.
func (s *Service) resolveProject(ctx context.Context, cwd string) (domain.ProjectID, error) {
	cwd = strings.TrimSpace(cwd)
	if cwd == "" {
		return "", ErrImportProjectUnresolved
	}

	projects, err := s.projects.List(ctx)
	if err != nil {
		return "", err
	}
	if id, ok := bestProjectForDir(projects, cwd); ok {
		return id, nil
	}

	created, err := s.projects.Add(ctx, projectsvc.AddInput{Path: gitRoot(cwd)})
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrImportProjectUnresolved, err)
	}
	return created.ID, nil
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
			set[id] = struct{}{}
		}
		if id := strings.TrimSpace(r.Metadata.AgentSessionID); id != "" {
			set[id] = struct{}{}
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
		if pp == "" {
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

// gitRoot walks up from dir to the nearest directory containing a .git entry,
// returning that directory. If none is found, dir itself is returned so Add can
// surface a clear "not a git repository" error.
func gitRoot(dir string) string {
	orig := filepath.Clean(dir)
	d := orig
	for {
		if _, err := os.Stat(filepath.Join(d, ".git")); err == nil {
			return d
		}
		parent := filepath.Dir(d)
		if parent == d {
			return orig
		}
		d = parent
	}
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
