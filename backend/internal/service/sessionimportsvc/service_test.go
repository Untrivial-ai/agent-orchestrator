package sessionimportsvc

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	projectsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/project"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/sessionimport"
)

// fakeSource is a discovery source returning canned conversations, so import
// orchestration can be exercised without touching disk.
type fakeSource struct {
	provider domain.AgentHarness
	sessions []sessionimport.ImportableSession
}

func (f *fakeSource) Provider() domain.AgentHarness { return f.provider }
func (f *fakeSource) Discover(context.Context, sessionimport.DiscoverOptions) ([]sessionimport.ImportableSession, error) {
	return f.sessions, nil
}

func TestBestProjectForDir(t *testing.T) {
	projects := []projectsvc.Summary{
		{ID: "root", Path: "/Users/dev/code"},
		{ID: "nested", Path: "/Users/dev/code/app"},
		{ID: "other", Path: "/Users/dev/other"},
	}
	cases := map[string]domain.ProjectID{
		"/Users/dev/code/app/src": "nested", // nearest ancestor wins over root
		"/Users/dev/code/app":     "nested", // exact match
		"/Users/dev/code/lib":     "root",   // only root covers it
		"/Users/dev/elsewhere":    "",       // no cover
	}
	for dir, want := range cases {
		got, ok := bestProjectForDir(projects, dir)
		if want == "" {
			if ok {
				t.Errorf("%s: expected no match, got %s", dir, got)
			}
			continue
		}
		if !ok || got != want {
			t.Errorf("%s: got %q (ok=%v), want %q", dir, got, ok, want)
		}
	}
}

func TestDirIsAncestor(t *testing.T) {
	if !dirIsAncestor("/a/b", "/a/b/c") {
		t.Error("/a/b should be ancestor of /a/b/c")
	}
	if dirIsAncestor("/a/b", "/a/b") {
		t.Error("a dir is not a strict ancestor of itself")
	}
	if dirIsAncestor("/a/b/c", "/a/b") {
		t.Error("child is not an ancestor of parent")
	}
	if dirIsAncestor("/a/bc", "/a/b") {
		t.Error("sibling prefix should not count as ancestor")
	}
}

func TestImportDisplayName(t *testing.T) {
	if got := importDisplayName("short"); got != "short" {
		t.Errorf("short title changed: %q", got)
	}
	long := importDisplayName("This is a very long conversation title that exceeds the cap")
	if r := []rune(long); len(r) > maxImportDisplayName {
		t.Errorf("display name not truncated to cap: %q (%d runes)", long, len(r))
	}
}

// --- Import orchestration with fakes ---

type fakeSessions struct {
	spawned  ports.SpawnConfig
	spawnErr error
	get      map[domain.SessionID]domain.Session
}

func (f *fakeSessions) RegisterImport(_ context.Context, cfg ports.SpawnConfig) (domain.Session, int, int, error) {
	f.spawned = cfg
	if f.spawnErr != nil {
		return domain.Session{}, 0, 0, f.spawnErr
	}
	return domain.Session{SessionRecord: domain.SessionRecord{ID: "proj-1", Harness: cfg.Harness}}, 0, 0, nil
}

func (f *fakeSessions) Get(_ context.Context, id domain.SessionID) (domain.Session, error) {
	return f.get[id], nil
}

type fakeStore struct{ recs []domain.SessionRecord }

func (f *fakeStore) ListAllSessions(context.Context) ([]domain.SessionRecord, error) {
	return f.recs, nil
}

type fakeProjects struct {
	list  []projectsvc.Summary
	added projectsvc.AddInput
}

func (f *fakeProjects) List(context.Context) ([]projectsvc.Summary, error) { return f.list, nil }
func (f *fakeProjects) Add(_ context.Context, in projectsvc.AddInput) (projectsvc.Project, error) {
	f.added = in
	return projectsvc.Project{ID: "created-1", Path: in.Path}, nil
}

func TestImportIsIdempotent(t *testing.T) {
	existing := domain.Session{SessionRecord: domain.SessionRecord{ID: "proj-9"}}
	sessions := &fakeSessions{get: map[domain.SessionID]domain.Session{"proj-9": existing}}
	store := &fakeStore{recs: []domain.SessionRecord{
		{ID: "proj-9", ProjectID: "proj", Harness: domain.HarnessClaudeCode, Metadata: domain.SessionMetadata{ProviderConversationID: "native-abc"}},
	}}
	svc := New(sessions, store, &fakeProjects{})

	got, already, err := svc.Import(context.Background(), domain.HarnessClaudeCode, "native-abc", "proj")
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if !already {
		t.Error("expected alreadyImported=true for a native id already bound")
	}
	if got.ID != "proj-9" {
		t.Errorf("expected the existing session, got %s", got.ID)
	}
	if sessions.spawned.Harness != "" {
		t.Error("idempotent import must not spawn a new session")
	}
}

func TestImportSpawnsChatSessionBoundToNativeID(t *testing.T) {
	target := sessionimport.ImportableSession{
		TokenCount: MinimumTokens, LastActivity: time.Now(),
		Provider:        domain.HarnessClaudeCode,
		NativeSessionID: "nat-1",
		ConfigDir:       "/home/user/.claude",
		CWD:             "/Users/dev/code",
		Branch:          "feat/payments",
		Title:           "A conversation worth continuing",
	}
	src := &fakeSource{provider: domain.HarnessClaudeCode, sessions: []sessionimport.ImportableSession{target}}
	sessions := &fakeSessions{}
	store := &fakeStore{}
	projects := &fakeProjects{list: []projectsvc.Summary{{ID: "proj-existing", Path: "/Users/dev/code"}}}
	svc := New(sessions, store, projects, src)

	got, already, err := svc.Import(context.Background(), domain.HarnessClaudeCode, "nat-1", "proj-existing")
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if already {
		t.Error("fresh import should not be flagged already imported")
	}
	if got.ID == "" {
		t.Error("expected a spawned session")
	}

	cfg := sessions.spawned
	if cfg.ProjectID != "proj-existing" {
		t.Errorf("expected the covering project, got %q", cfg.ProjectID)
	}
	if cfg.Harness != domain.HarnessClaudeCode {
		t.Errorf("harness: got %q", cfg.Harness)
	}
	if cfg.RequestedMode != domain.SessionModeChat {
		t.Errorf("import must be chat mode, got %q", cfg.RequestedMode)
	}
	if cfg.ResumeNativeSession == nil {
		t.Fatal("ResumeNativeSession must be set so the transcript is replayed")
	}
	if cfg.ResumeNativeSession.NativeSessionID != "nat-1" ||
		cfg.ResumeNativeSession.ConfigDir != "/home/user/.claude" ||
		cfg.ResumeNativeSession.Provider != domain.HarnessClaudeCode {
		t.Errorf("ResumeNativeSession not populated correctly: %+v", cfg.ResumeNativeSession)
	}
	if projects.added.Path != "" {
		t.Errorf("a covering project existed; no new project should be registered (added %q)", projects.added.Path)
	}
	// The branch the conversation ran on is a repository fact, and recording it
	// is what lets the SCM observer find the pull request and the board place
	// the session in review / ready to merge / merged without inventing state.
	if cfg.Branch != "feat/payments" {
		t.Errorf("the conversation branch must be carried into the spawn, got %q", cfg.Branch)
	}
}

func TestImportNeverRegistersProject(t *testing.T) {
	projects := &fakeProjects{}
	svc := New(&fakeSessions{}, &fakeStore{}, projects)
	if _, _, err := svc.Import(context.Background(), domain.HarnessCodex, "native", "missing"); !errors.Is(err, ErrImportProjectUnresolved) {
		t.Fatalf("expected unregistered project error, got %v", err)
	}
	if projects.added.Path != "" {
		t.Fatal("import must not register a project")
	}
}

func TestImportAfterDeleteCreatesFresh(t *testing.T) {
	// A terminated session bound to the native id must not block re-import.
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	target := sessionimport.ImportableSession{
		TokenCount: MinimumTokens, LastActivity: time.Now(),
		Provider:        domain.HarnessClaudeCode,
		NativeSessionID: "nat-9",
		ConfigDir:       "/home/user/.claude",
		CWD:             repo,
		Title:           "deleted then reimported",
	}
	src := &fakeSource{provider: domain.HarnessClaudeCode, sessions: []sessionimport.ImportableSession{target}}
	sessions := &fakeSessions{}
	store := &fakeStore{recs: []domain.SessionRecord{
		{ID: "old", IsTerminated: true, Metadata: domain.SessionMetadata{ProviderConversationID: "nat-9"}},
	}}
	projects := &fakeProjects{list: []projectsvc.Summary{{ID: "p1", Path: repo}}}

	svc := New(sessions, store, projects, src)
	got, already, err := svc.Import(context.Background(), domain.HarnessClaudeCode, "nat-9", "p1")
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if already {
		t.Error("a terminated prior import must not count as already imported")
	}
	if sessions.spawned.ResumeNativeSession == nil {
		t.Error("expected a fresh spawn after the prior import was deleted")
	}
	if got.ID == "" {
		t.Error("expected a new session")
	}
}

func TestNativeIDSetSkipsTerminated(t *testing.T) {
	set := nativeIDSet([]domain.SessionRecord{
		{IsTerminated: true, Metadata: domain.SessionMetadata{ProviderConversationID: "dead"}},
		{Metadata: domain.SessionMetadata{ProviderConversationID: "live"}},
	})
	if _, ok := set[sessionimport.NativeKey("", "dead")]; ok {
		t.Error("terminated session should not be flagged already imported")
	}
	if _, ok := set[sessionimport.NativeKey("", "live")]; !ok {
		t.Error("live session should be flagged already imported")
	}
}

func TestNativeIDSetCollectsBothFields(t *testing.T) {
	set := nativeIDSet([]domain.SessionRecord{
		{Metadata: domain.SessionMetadata{ProviderConversationID: "pc1"}},
		{Metadata: domain.SessionMetadata{AgentSessionID: "as1"}},
		{Metadata: domain.SessionMetadata{}},
	})
	if _, ok := set[sessionimport.NativeKey("", "pc1")]; !ok {
		t.Error("missing provider conversation id")
	}
	if _, ok := set[sessionimport.NativeKey("", "as1")]; !ok {
		t.Error("missing agent session id")
	}
	if len(set) != 2 {
		t.Errorf("unexpected set size: %d", len(set))
	}
}

func TestDiscoverScopedToProject(t *testing.T) {
	// Three conversations: one in a project, one in a nested project inside it,
	// and one in a directory no project covers.
	sessions := []sessionimport.ImportableSession{
		{Provider: domain.HarnessClaudeCode, NativeSessionID: "in-root", CWD: "/Users/dev/code", TokenCount: MinimumTokens, LastActivity: time.Now()},
		{Provider: domain.HarnessClaudeCode, NativeSessionID: "in-nested", CWD: "/Users/dev/code/app/sub", TokenCount: MinimumTokens, LastActivity: time.Now()},
		{Provider: domain.HarnessClaudeCode, NativeSessionID: "elsewhere", CWD: "/tmp/scratch", TokenCount: MinimumTokens, LastActivity: time.Now()},
	}
	src := &fakeSource{provider: domain.HarnessClaudeCode, sessions: sessions}
	projects := &fakeProjects{list: []projectsvc.Summary{
		{ID: "root", Path: "/Users/dev/code"},
		{ID: "nested", Path: "/Users/dev/code/app"},
	}}
	svc := New(&fakeSessions{}, &fakeStore{}, projects, src)

	// A project lists only its own history, and the nested project's
	// conversation belongs to the nested project rather than its parent.
	scoped, err := svc.Discover(context.Background(), sessionimport.DiscoverOptions{}, "root")
	if err != nil {
		t.Fatalf("discover scoped: %v", err)
	}
	if len(scoped) != 1 || scoped[0].NativeSessionID != "in-root" {
		t.Fatalf("want only the root project's conversation, got %+v", scoped)
	}

	nested, err := svc.Discover(context.Background(), sessionimport.DiscoverOptions{}, "nested")
	if err != nil {
		t.Fatalf("discover nested: %v", err)
	}
	if len(nested) != 1 || nested[0].NativeSessionID != "in-nested" {
		t.Fatalf("the most specific project should win, got %+v", nested)
	}
}

func TestAdoptableBranch(t *testing.T) {
	cases := []struct {
		branch string
		want   string
		why    string
	}{
		{"feat/payments", "feat/payments", "a normal working branch is adopted"},
		{"  feat/payments  ", "feat/payments", "surrounding space is trimmed"},
		{"HEAD", "", "Claude records HEAD for a detached checkout and git rejects it as a branch name"},
		{"main", "", "a session must never be created directly on the trunk"},
		{"Master", "", "trunk names are matched regardless of case"},
		{"develop", "", "long-lived integration branches are trunks too"},
		{"", "", "nothing recorded means nothing to adopt"},
		{"-dashed", "", "git rejects a leading dash"},
		{"has space", "", "git rejects whitespace"},
		{"a..b", "", "git rejects a double dot"},
		{"ends/", "", "git rejects a trailing slash"},
		{"we.lock", "", "git rejects a .lock suffix"},
		{"tilde~1", "", "git rejects a tilde"},
		{"colon:name", "", "git rejects a colon"},
	}
	for _, tc := range cases {
		if got := adoptableBranch(tc.branch); got != tc.want {
			t.Errorf("adoptableBranch(%q) = %q, want %q — %s", tc.branch, got, tc.want, tc.why)
		}
	}
}

// A detached-HEAD conversation must still import. Passing "HEAD" through would
// fail git's branch validation and take the whole import down with it.
func TestImportOfDetachedHeadConversationDropsTheBranch(t *testing.T) {
	target := sessionimport.ImportableSession{
		TokenCount: MinimumTokens, LastActivity: time.Now(),
		Provider:        domain.HarnessClaudeCode,
		NativeSessionID: "detached-1",
		CWD:             "/Users/dev/code",
		Branch:          "HEAD",
		Title:           "Work from a detached checkout",
	}
	src := &fakeSource{provider: domain.HarnessClaudeCode, sessions: []sessionimport.ImportableSession{target}}
	sessions := &fakeSessions{}
	projects := &fakeProjects{list: []projectsvc.Summary{{ID: "proj-existing", Path: "/Users/dev/code"}}}
	svc := New(sessions, &fakeStore{}, projects, src)

	if _, _, err := svc.Import(context.Background(), domain.HarnessClaudeCode, "detached-1", "proj-existing"); err != nil {
		t.Fatalf("a detached-HEAD conversation must still import: %v", err)
	}
	if sessions.spawned.Branch != "" {
		t.Errorf("HEAD must not be requested as a branch, got %q", sessions.spawned.Branch)
	}
}

// An imported conversation must keep the branch it ran on even when the session
// cannot be created there, because that branch is the only link back to its
// pull request. Without it every import lands awaiting a PR that is never found.
func TestImportRecordsTheConversationBranchForPRDiscovery(t *testing.T) {
	target := sessionimport.ImportableSession{
		TokenCount: MinimumTokens, LastActivity: time.Now(),
		Provider:        domain.HarnessClaudeCode,
		NativeSessionID: "nat-branch",
		CWD:             "/Users/dev/code",
		Branch:          "feat/payments",
		Title:           "Payments work",
	}
	src := &fakeSource{provider: domain.HarnessClaudeCode, sessions: []sessionimport.ImportableSession{target}}
	sessions := &fakeSessions{}
	projects := &fakeProjects{list: []projectsvc.Summary{{ID: "proj", Path: "/Users/dev/code"}}}
	svc := New(sessions, &fakeStore{}, projects, src)

	if _, _, err := svc.Import(context.Background(), domain.HarnessClaudeCode, "nat-branch", "proj"); err != nil {
		t.Fatalf("import: %v", err)
	}
	rns := sessions.spawned.ResumeNativeSession
	if rns == nil {
		t.Fatal("ResumeNativeSession must be set")
	}
	if rns.SourceBranch != "feat/payments" {
		t.Errorf("the conversation's branch must be recorded, got %q", rns.SourceBranch)
	}
}

// The trunk is refused as a session branch, but it is still worth recording as
// where the conversation ran.
func TestImportRecordsTrunkAsSourceEvenThoughItIsNotAdopted(t *testing.T) {
	target := sessionimport.ImportableSession{
		TokenCount: MinimumTokens, LastActivity: time.Now(),
		Provider:        domain.HarnessClaudeCode,
		NativeSessionID: "nat-main",
		CWD:             "/Users/dev/code",
		Branch:          "main",
		Title:           "Work on main",
	}
	src := &fakeSource{provider: domain.HarnessClaudeCode, sessions: []sessionimport.ImportableSession{target}}
	sessions := &fakeSessions{}
	projects := &fakeProjects{list: []projectsvc.Summary{{ID: "proj", Path: "/Users/dev/code"}}}
	svc := New(sessions, &fakeStore{}, projects, src)

	if _, _, err := svc.Import(context.Background(), domain.HarnessClaudeCode, "nat-main", "proj"); err != nil {
		t.Fatalf("import: %v", err)
	}
	if sessions.spawned.Branch != "" {
		t.Errorf("a session must never be created on the trunk, got %q", sessions.spawned.Branch)
	}
	if sessions.spawned.ResumeNativeSession.SourceBranch != "main" {
		t.Errorf("the source branch should still be recorded, got %q", sessions.spawned.ResumeNativeSession.SourceBranch)
	}
}

// A project's listing must not pay to fully read every conversation on the
// machine just to show its own handful.
func TestScopedDiscoveryNarrowsTheScan(t *testing.T) {
	sessions := []sessionimport.ImportableSession{
		{Provider: domain.HarnessClaudeCode, NativeSessionID: "mine", CWD: "/Users/dev/code", TokenCount: MinimumTokens, LastActivity: time.Now()},
		{Provider: domain.HarnessClaudeCode, NativeSessionID: "elsewhere", CWD: "/tmp/other", TokenCount: MinimumTokens, LastActivity: time.Now()},
	}
	src := &scopeRecordingSource{inner: &fakeSource{provider: domain.HarnessClaudeCode, sessions: sessions}}
	projects := &fakeProjects{list: []projectsvc.Summary{{ID: "proj", Path: "/Users/dev/code"}}}
	svc := New(&fakeSessions{}, &fakeStore{}, projects, src)

	got, err := svc.Discover(context.Background(), sessionimport.DiscoverOptions{}, "proj")
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if len(got) != 1 || got[0].NativeSessionID != "mine" {
		t.Fatalf("want only the project's conversation, got %+v", got)
	}
	if src.lastOpts.IncludeCWD == nil {
		t.Fatal("a scoped listing must push the scope into the scan, not filter afterwards")
	}
	if src.lastOpts.IncludeCWD("/tmp/other") {
		t.Error("a conversation outside the project must be skipped before it is read")
	}
	if !src.lastOpts.IncludeCWD("/Users/dev/code") {
		t.Error("the project's own conversations must still be read")
	}

}

// scopeRecordingSource captures the options discovery was asked for.
type scopeRecordingSource struct {
	inner    *fakeSource
	lastOpts sessionimport.DiscoverOptions
}

func (s *scopeRecordingSource) Provider() domain.AgentHarness { return s.inner.Provider() }
func (s *scopeRecordingSource) Discover(ctx context.Context, opts sessionimport.DiscoverOptions) ([]sessionimport.ImportableSession, error) {
	s.lastOpts = opts
	found, err := s.inner.Discover(ctx, opts)
	if err != nil || opts.IncludeCWD == nil {
		return found, err
	}
	kept := found[:0]
	for _, f := range found {
		if opts.IncludeCWD(f.CWD) {
			kept = append(kept, f)
		}
	}
	return kept, nil
}

func TestDiscoveryKeepsSameIDInOtherHarnessImportable(t *testing.T) {
	store := &fakeStore{recs: []domain.SessionRecord{{ID: "existing", ProjectID: "p", Harness: domain.HarnessCodex, Metadata: domain.SessionMetadata{ProviderConversationID: "shared"}}}}
	var sources []sessionimport.Source
	for _, provider := range []domain.AgentHarness{domain.HarnessCodex, domain.HarnessClaudeCode} {
		sources = append(sources, &fakeSource{provider: provider, sessions: []sessionimport.ImportableSession{{Provider: provider, NativeSessionID: "shared", CWD: "/project", TokenCount: MinimumTokens, LastActivity: time.Now()}}})
	}
	svc := New(&fakeSessions{}, store, &fakeProjects{list: []projectsvc.Summary{{ID: "p", Path: "/project"}}}, sources...)
	got, err := svc.Discover(context.Background(), sessionimport.DiscoverOptions{}, "p")
	if err != nil || len(got) != 2 {
		t.Fatalf("discovery: %+v %v", got, err)
	}
	for _, session := range got {
		if session.AlreadyImported != (session.Provider == domain.HarnessCodex) {
			t.Fatalf("wrong duplicate marker: %+v", session)
		}
	}
}
