package sessionmanager

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/workspace/gitworktree"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	browsersvc "github.com/aoagents/agent-orchestrator/backend/internal/service/browser"
)

func TestTaskPreparationIsClaimedWithoutCreatingAnotherWorktree(t *testing.T) {
	m, st, _, ws := newManager()
	m.runBackground = func(work func()) { work() }
	project := st.projects["mer"]

	token, err := m.PrepareTaskWorkspace(context.Background(), project)
	if err != nil {
		t.Fatal(err)
	}
	prepared := st.sessions["mer-1"]
	if !prepared.IsTaskPreparation || prepared.Metadata.WorkspacePath == "" {
		t.Fatalf("prepared row = %+v, want hidden row with workspace", prepared)
	}

	spawned, _, _, err := m.Spawn(context.Background(), ports.SpawnConfig{
		ProjectID:       "mer",
		Kind:            domain.KindWorker,
		TaskPreparation: token,
	})
	if err != nil {
		t.Fatal(err)
	}
	if spawned.ID != "mer-1" {
		t.Fatalf("session id = %q, want reserved mer-1", spawned.ID)
	}
	if got := ws.createCount; got != 1 {
		t.Fatalf("workspace creates = %d, want speculative create only", got)
	}
	if st.sessions[spawned.ID].IsTaskPreparation {
		t.Fatal("claimed session remained hidden")
	}
	if got := st.sessions[spawned.ID].ProvisionState.WithDefault(); got != domain.SessionProvisionReady {
		t.Fatalf("synchronous task provision state = %q, want ready", got)
	}
}

func TestCancelTaskPreparationRemovesWorkspaceAndRow(t *testing.T) {
	m, st, _, ws := newManager()
	m.runBackground = func(work func()) { work() }

	token, err := m.PrepareTaskWorkspace(context.Background(), st.projects["mer"])
	if err != nil {
		t.Fatal(err)
	}
	if err := m.CancelTaskPreparation(context.Background(), token); err != nil {
		t.Fatal(err)
	}
	if ws.destroyed != 1 {
		t.Fatalf("destroy calls = %d, want 1", ws.destroyed)
	}
	if _, ok := st.sessions["mer-1"]; ok {
		t.Fatal("canceled preparation row still exists")
	}
}

func TestTaskPreparationCapsOutstandingPerProject(t *testing.T) {
	m, st, _, _ := newManager()
	deferred := deferredBackground(m)
	project := st.projects["mer"]
	var tokens []domain.TaskPreparationToken
	for i := 0; i < 3; i++ {
		token, err := m.PrepareTaskWorkspace(context.Background(), project)
		if err != nil {
			t.Fatal(err)
		}
		tokens = append(tokens, token)
	}
	if tokens[0] == "" || tokens[1] == "" || tokens[2] != "" {
		t.Fatalf("preparation tokens = %q, want two owned preparations and a skipped third", tokens)
	}
	if len(*deferred) != 2 {
		t.Fatalf("background creates = %d, want 2", len(*deferred))
	}
	if len(st.sessions) != 2 {
		t.Fatalf("hidden session rows = %d, want 2", len(st.sessions))
	}
	for _, token := range tokens[:2] {
		(*deferred)[0]()
		*deferred = (*deferred)[1:]
		if err := m.CancelTaskPreparation(context.Background(), token); err != nil {
			t.Fatal(err)
		}
		if token == tokens[0] {
			if _, ok := st.sessions[domain.SessionID(tokens[1])]; !ok {
				t.Fatal("closing one composer canceled another composer's preparation")
			}
		}
	}
}

type partialPreparedWorkspace struct{ *fakeWorkspace }

type cancelledClaimedWorkspace struct {
	*fakeWorkspace
	entered    chan struct{}
	release    chan struct{}
	destroyErr error
}

func (w *cancelledClaimedWorkspace) Create(ctx context.Context, cfg ports.WorkspaceConfig) (ports.WorkspaceInfo, error) {
	close(w.entered)
	<-ctx.Done()
	<-w.release
	return ports.WorkspaceInfo{Path: "/ws/partial", Branch: cfg.Branch,
		SessionID: cfg.SessionID, ProjectID: cfg.ProjectID}, ctx.Err()
}

func (w *cancelledClaimedWorkspace) Destroy(context.Context, ports.WorkspaceInfo) error {
	return w.destroyErr
}

type failedStartSignalStore struct {
	*fakeStore
	failed chan struct{}
	once   sync.Once
}

func (s *failedStartSignalStore) GetSession(ctx context.Context, id domain.SessionID) (domain.SessionRecord, bool, error) {
	rec, ok, err := s.fakeStore.GetSession(ctx, id)
	if ok && rec.ProvisionState == domain.SessionProvisionFailed {
		s.once.Do(func() { close(s.failed) })
	}
	return rec, ok, err
}

func TestCancelledClaimedPreparationAccountsForPartialWorkspace(t *testing.T) {
	for _, tc := range []struct {
		name       string
		destroyErr error
		wantPath   string
	}{
		{name: "dirty worktree is retained", destroyErr: ports.ErrWorkspaceDirty, wantPath: "/ws/partial"},
		{name: "clean worktree is removed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, st, _ := newChatManager(&recordingLauncher{})
			workspace := &cancelledClaimedWorkspace{fakeWorkspace: m.workspace.(*fakeWorkspace),
				entered: make(chan struct{}), release: make(chan struct{}), destroyErr: tc.destroyErr}
			m.workspace = workspace
			signalStore := &failedStartSignalStore{fakeStore: st, failed: make(chan struct{})}
			m.store = signalStore
			m.browserCapabilities = browsersvc.NewAuthority()
			token, err := m.PrepareTaskWorkspace(context.Background(), st.projects[string(chatTestProject)])
			if err != nil {
				t.Fatal(err)
			}
			<-workspace.entered
			cfg := asyncChatSpawnConfig("do the thing")
			cfg.TaskPreparation = token
			rec, _, _, err := m.Spawn(context.Background(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			m.asyncChatSpawnsMu.Lock()
			run := m.asyncChatSpawns[rec.ID]
			m.asyncChatSpawnsMu.Unlock()
			run.cancel()
			<-signalStore.failed
			close(workspace.release)
			<-run.done
			stored := st.sessions[rec.ID]
			if stored.Metadata.WorkspacePath != tc.wantPath {
				t.Fatalf("cancelled partial worktree path = %q, want %q", stored.Metadata.WorkspacePath, tc.wantPath)
			}
		})
	}
}

func (w *partialPreparedWorkspace) Create(_ context.Context, cfg ports.WorkspaceConfig) (ports.WorkspaceInfo, error) {
	return ports.WorkspaceInfo{
		Path: "/ws/partial", Branch: cfg.Branch, BaseSHA: "creation-sha", BaseRef: "main",
		SessionID: cfg.SessionID, ProjectID: cfg.ProjectID,
	}, context.Canceled
}

func (w *partialPreparedWorkspace) Destroy(context.Context, ports.WorkspaceInfo) error {
	return ports.ErrWorkspaceDirty
}

func TestTaskPreparationPersistsPartialWorktreeForRetry(t *testing.T) {
	m, st, _, base := newManager()
	m.workspace = &partialPreparedWorkspace{base}
	m.runBackground = func(work func()) { work() }
	token, err := m.PrepareTaskWorkspace(context.Background(), st.projects["mer"])
	if err != nil {
		t.Fatal(err)
	}
	rec := st.sessions[domain.SessionID(token)]
	if rec.Metadata.WorkspacePath != "/ws/partial" || rec.Metadata.DiffBaseSHA != "creation-sha" {
		t.Fatalf("partial worktree was not durably retained: %+v", rec.Metadata)
	}
	if err := m.CancelTaskPreparation(context.Background(), token); !errors.Is(err, ports.ErrWorkspaceDirty) {
		t.Fatalf("cancel partial preparation = %v, want preserve dirty path", err)
	}
	if _, ok := st.sessions[domain.SessionID(token)]; !ok {
		t.Fatal("partial worktree lost its hidden retry row")
	}
}

type partialProjectWorkspace struct {
	*fakeWorkspace
	partial ports.WorkspaceProjectInfo
}

func (w *partialProjectWorkspace) CreateWorkspaceProject(context.Context, ports.WorkspaceProjectConfig) (ports.WorkspaceProjectInfo, error) {
	return w.partial, context.Canceled
}

func TestTaskPreparationPersistsPartialWorkspaceProjectAcrossStartup(t *testing.T) {
	m, st, _, base := newManager()
	project := st.projects["mer"]
	project.Kind = domain.ProjectKindWorkspace
	project.Path = "/repo/root"
	st.projects["mer"] = project
	st.workspaceRepo["mer"] = []domain.WorkspaceRepoRecord{{Name: "api", RelativePath: "api", GitStatus: domain.GitStatusReady}}
	branch := "ao/mer-1/root"
	root := ports.WorkspaceInfo{Path: "/ws/mer-1", Branch: branch, BaseSHA: "root-created", BaseRef: "main", SessionID: "mer-1", ProjectID: "mer"}
	base.destroyErr = ports.ErrWorkspaceDirty
	m.workspace = &partialProjectWorkspace{fakeWorkspace: base, partial: ports.WorkspaceProjectInfo{
		Root: root,
		Worktrees: []ports.WorkspaceRepoInfo{
			{RepoName: domain.RootWorkspaceRepoName, RepoPath: project.Path, Path: root.Path, Branch: branch, BaseSHA: "root-base", CreationSHA: root.BaseSHA, BaseRef: "main", SessionID: "mer-1", ProjectID: "mer"},
			{RepoName: "api", RepoPath: "/repo/root/api", Path: "/ws/mer-1/api", Branch: branch, BaseSHA: "api-base", CreationSHA: "api-created", BaseRef: "main", SessionID: "mer-1", ProjectID: "mer", RelativePath: "api"},
		},
	}}
	m.runBackground = func(work func()) { work() }
	token, err := m.PrepareTaskWorkspace(context.Background(), project)
	if err != nil {
		t.Fatal(err)
	}
	if got := st.worktrees[domain.SessionID(token)]; len(got) != 2 || got[1].CreationSHA != "api-created" {
		t.Fatalf("partial project rows = %+v, want both durably recorded", got)
	}
	m.taskPreparationsMu.Lock()
	prep := m.taskPreparations[token]
	prep.timer.Stop()
	delete(m.taskPreparations, token)
	m.taskPreparationsMu.Unlock()
	if err := m.CleanupInterruptedTaskPreparations(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := st.sessions[domain.SessionID(token)]; !ok {
		t.Fatal("startup cleanup deleted a dirty partial workspace project row")
	}
}

func TestRecoveredPreparationCreationSHAMatchesExactWorktreeIdentity(t *testing.T) {
	for _, tc := range []struct {
		name, branch, wantSHA string
	}{
		{name: "same worktree", branch: "ao/mer-1/root", wantSHA: "original-sha"},
		{name: "new suffixed branch", branch: "ao/mer-1/root-2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, st, _, ws := newManager()
			project := st.projects["mer"]
			project.Kind = domain.ProjectKindWorkspace
			st.projects["mer"] = project
			st.worktrees["mer-1"] = []domain.SessionWorktreeRecord{{
				SessionID: "mer-1", RepoName: domain.RootWorkspaceRepoName,
				Branch: "ao/mer-1/root", BaseRef: "main", CreationSHA: "original-sha", WorktreePath: "/ws/mer-1",
			}}
			ws.projectCreateInfo = ports.WorkspaceProjectInfo{
				Root: ports.WorkspaceInfo{Path: "/ws/mer-1", Branch: tc.branch, ProjectID: "mer", SessionID: "mer-1"},
				Worktrees: []ports.WorkspaceRepoInfo{{
					RepoName: domain.RootWorkspaceRepoName, Path: "/ws/mer-1", Branch: tc.branch,
					BaseRef: "main", ProjectID: "mer", SessionID: "mer-1",
				}},
			}
			root, info, err := m.createSessionWorkspace(context.Background(), project, ports.SpawnConfig{
				ProjectID: "mer", Kind: domain.KindWorker, TaskPreparation: "mer-1",
			}, "mer-1", tc.branch, nil)
			if err != nil {
				t.Fatal(err)
			}
			if root.BaseSHA != tc.wantSHA || info.Worktrees[0].CreationSHA != tc.wantSHA || st.worktrees["mer-1"][0].CreationSHA != tc.wantSHA {
				t.Fatalf("creation SHA = root:%q worktree:%q stored:%q, want %q", root.BaseSHA, info.Worktrees[0].CreationSHA, st.worktrees["mer-1"][0].CreationSHA, tc.wantSHA)
			}
		})
	}
}

type blockedPreparationWorkspace struct {
	*fakeWorkspace
	mu            sync.Mutex
	calls         int
	firstEntered  chan struct{}
	secondEntered chan struct{}
	releaseFirst  chan struct{}
}

func (w *blockedPreparationWorkspace) Create(ctx context.Context, cfg ports.WorkspaceConfig) (ports.WorkspaceInfo, error) {
	w.mu.Lock()
	w.calls++
	call := w.calls
	w.mu.Unlock()
	switch call {
	case 1:
		close(w.firstEntered)
		<-w.releaseFirst
	case 2:
		close(w.secondEntered)
	}
	return w.fakeWorkspace.Create(ctx, cfg)
}

func TestTaskPreparationSerializesWorkspaceCreationPerProject(t *testing.T) {
	m, st, _, base := newManager()
	ws := &blockedPreparationWorkspace{
		fakeWorkspace: base, firstEntered: make(chan struct{}), secondEntered: make(chan struct{}), releaseFirst: make(chan struct{}),
	}
	m.workspace = ws
	deferred := deferredBackground(m)
	project := st.projects["mer"]
	first, err := m.PrepareTaskWorkspace(context.Background(), project)
	if err != nil {
		t.Fatal(err)
	}
	second, err := m.PrepareTaskWorkspace(context.Background(), project)
	if err != nil {
		t.Fatal(err)
	}
	firstDone := make(chan struct{})
	go func() { (*deferred)[0](); close(firstDone) }()
	<-ws.firstEntered
	secondDone := make(chan struct{})
	go func() { (*deferred)[1](); close(secondDone) }()
	secondBeforeRelease := false
	select {
	case <-ws.secondEntered:
		secondBeforeRelease = true
	case <-time.After(100 * time.Millisecond):
	}
	close(ws.releaseFirst)
	<-firstDone
	<-secondDone
	if secondBeforeRelease {
		t.Fatal("second worktree creation entered while first still held the project workspace gate")
	}
	if err := m.CancelTaskPreparation(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	if err := m.CancelTaskPreparation(context.Background(), second); err != nil {
		t.Fatal(err)
	}
}

func TestAsyncPreparedSpawnAnswersWhilePreparationOwnsWorkspaceGate(t *testing.T) {
	m, st, _ := newChatManager(&recordingLauncher{})
	ws := &blockedPreparationWorkspace{
		fakeWorkspace: m.workspace.(*fakeWorkspace),
		firstEntered:  make(chan struct{}), secondEntered: make(chan struct{}), releaseFirst: make(chan struct{}),
	}
	m.workspace = ws
	jobs := make(chan func(), 2)
	m.runBackground = func(work func()) { jobs <- work }
	project := st.projects["mer"]
	token, err := m.PrepareTaskWorkspace(context.Background(), project)
	if err != nil {
		t.Fatal(err)
	}
	prepDone := make(chan struct{})
	go func() { (<-jobs)(); close(prepDone) }()
	<-ws.firstEntered
	result := make(chan error, 1)
	go func() {
		cfg := asyncChatSpawnConfig("do the thing")
		cfg.TaskPreparation = token
		_, _, _, err := m.Spawn(context.Background(), cfg)
		result <- err
	}()
	answeredBeforeGit := false
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("async prepared spawn: %v", err)
		}
		answeredBeforeGit = true
	case <-time.After(500 * time.Millisecond):
	}
	close(ws.releaseFirst)
	<-prepDone
	if !answeredBeforeGit {
		if err := <-result; err != nil {
			t.Fatal(err)
		}
		t.Fatal("async Start Task waited for speculative git work to finish")
	}
	(<-jobs)()
	if ws.calls != 1 {
		t.Fatalf("worktree creates = %d, want only the prepared worktree", ws.calls)
	}
}

func TestCancelTaskPreparationDoesNotReuseStaleBranch(t *testing.T) {
	m, st, ws, repo := newGitTaskPreparationManager(t)
	project := st.projects["mer"]

	token, err := m.PrepareTaskWorkspace(context.Background(), project)
	if err != nil {
		t.Fatal(err)
	}
	branch := st.sessions["mer-1"].Metadata.Branch
	if err := m.CancelTaskPreparation(context.Background(), token); err != nil {
		t.Fatal(err)
	}
	assertManagerBranchAbsent(t, repo, branch)

	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("new main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runManagerGit(t, repo, "add", "README.md")
	runManagerGit(t, repo, "commit", "-m", "advance main")
	info, err := ws.Create(context.Background(), ports.WorkspaceConfig{
		ProjectID: "mer", SessionID: "mer-1", Kind: domain.KindWorker,
		Branch: branch, BaseBranch: "main",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := runManagerGit(t, info.Path, "rev-parse", "HEAD"), runManagerGit(t, repo, "rev-parse", "HEAD"); got != want {
		t.Fatalf("recycled branch HEAD = %q, want current main %q", got, want)
	}
}

func newGitTaskPreparationManager(t *testing.T) (*Manager, *fakeStore, *gitworktree.Workspace, string) {
	t.Helper()
	repo := newManagerGitRepo(t)
	ws, err := gitworktree.New(gitworktree.Options{
		ManagedRoot:  t.TempDir(),
		RepoResolver: gitworktree.StaticRepoResolver{"mer": repo},
	})
	if err != nil {
		t.Fatal(err)
	}
	m, st, _, _ := newManager()
	m.workspace = ws
	m.runBackground = func(work func()) { work() }
	project := st.projects["mer"]
	project.Path = repo
	project.Config.DefaultBranch = "main"
	st.projects["mer"] = project
	return m, st, ws, repo
}

func assertManagerBranchAbsent(t *testing.T, repo, branch string) {
	t.Helper()
	out, err := exec.Command("git", "-C", repo, "show-ref", "--verify", "--quiet", "refs/heads/"+branch).CombinedOutput()
	if err == nil {
		t.Fatalf("discarded preparation left branch %q behind", branch)
	}
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1 {
		t.Fatalf("inspect branch: %v: %s", err, out)
	}
}

func TestCancelTaskPreparationPreservesDirtyWorktree(t *testing.T) {
	m, st, _, repo := newGitTaskPreparationManager(t)
	token, err := m.PrepareTaskWorkspace(context.Background(), st.projects["mer"])
	if err != nil {
		t.Fatal(err)
	}
	rec := st.sessions["mer-1"]
	file := filepath.Join(rec.Metadata.WorkspacePath, "user-work.txt")
	if err := os.WriteFile(file, []byte("keep me\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := m.CancelTaskPreparation(context.Background(), token); !errors.Is(err, ports.ErrWorkspaceDirty) {
		t.Fatalf("cancel error = %v, want dirty-worktree refusal", err)
	}
	if _, err := os.Stat(file); err != nil {
		t.Fatalf("dirty worktree removed: %v", err)
	}
	if _, ok := st.sessions["mer-1"]; !ok {
		t.Fatal("dirty preparation lost its hidden row")
	}
	if got := runManagerGit(t, repo, "rev-parse", "refs/heads/"+rec.Metadata.Branch); got == "" {
		t.Fatal("dirty preparation lost its branch")
	}
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	if err := m.CancelTaskPreparation(context.Background(), token); err != nil {
		t.Fatal(err)
	}
	assertManagerBranchAbsent(t, repo, rec.Metadata.Branch)
}

func TestCancelTaskPreparationPreservesCommittedBranchButDoesNotReuseIt(t *testing.T) {
	m, st, _, repo := newGitTaskPreparationManager(t)
	project := st.projects["mer"]
	token, err := m.PrepareTaskWorkspace(context.Background(), project)
	if err != nil {
		t.Fatal(err)
	}
	rec := st.sessions["mer-1"]
	if err := os.WriteFile(filepath.Join(rec.Metadata.WorkspacePath, "user-work.txt"), []byte("keep me\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runManagerGit(t, rec.Metadata.WorkspacePath, "add", "user-work.txt")
	runManagerGit(t, rec.Metadata.WorkspacePath, "commit", "-m", "user commit")
	committed := runManagerGit(t, rec.Metadata.WorkspacePath, "rev-parse", "HEAD")
	// Even if main later contains this commit, it was not part of the branch
	// when the preparation started and must not be pruned by cleanup.
	runManagerGit(t, repo, "merge", "--ff-only", rec.Metadata.Branch)
	if err := m.CancelTaskPreparation(context.Background(), token); err != nil {
		t.Fatal(err)
	}
	if got := runManagerGit(t, repo, "rev-parse", "refs/heads/"+rec.Metadata.Branch); got != committed {
		t.Fatalf("committed branch moved or deleted: got %q, want %q", got, committed)
	}
	// SQLite assigns MAX(num)+1, so a deleted hidden row can reuse this ID.
	st.num = 0
	next, err := m.PrepareTaskWorkspace(context.Background(), project)
	if err != nil {
		t.Fatal(err)
	}
	prepared := st.sessions["mer-1"]
	if prepared.Metadata.Branch != rec.Metadata.Branch+"-2" {
		t.Fatalf("new branch = %q, want suffix -2 after retained user branch", prepared.Metadata.Branch)
	}
	if got, want := runManagerGit(t, prepared.Metadata.WorkspacePath, "rev-parse", "HEAD"), runManagerGit(t, repo, "rev-parse", "HEAD"); got != want {
		t.Fatalf("fresh task started at %q, want main %q", got, want)
	}
	if err := m.CancelTaskPreparation(context.Background(), next); err != nil {
		t.Fatal(err)
	}
}

func TestCancelWorkspaceProjectPreparationRemovesEachBranch(t *testing.T) {
	m, st, _, root := newGitTaskPreparationManager(t)
	child := newManagerGitRepo(t)
	childPath := filepath.Join(root, "api")
	if err := os.Rename(child, childPath); err != nil {
		t.Fatal(err)
	}
	runManagerGit(t, childPath, "remote", "add", "origin", childPath)
	runManagerGit(t, childPath, "fetch", "origin", "main")
	runManagerGit(t, childPath, "remote", "set-head", "origin", "main")
	assetPath := filepath.Join(root, "local-assets")
	if err := os.MkdirAll(assetPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(assetPath, "note.txt"), []byte("copied by AO\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("api/\nlocal-assets/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runManagerGit(t, root, "add", ".gitignore")
	runManagerGit(t, root, "commit", "-m", "ignore workspace children")
	project := st.projects["mer"]
	project.Kind = domain.ProjectKindWorkspace
	st.projects["mer"] = project
	st.workspaceRepo["mer"] = []domain.WorkspaceRepoRecord{
		{Name: "api", RelativePath: "api", GitStatus: domain.GitStatusReady},
		{Name: "local-assets", RelativePath: "local-assets", GitStatus: domain.GitStatusNeedsInit},
	}

	token, err := m.PrepareTaskWorkspace(context.Background(), project)
	if err != nil {
		t.Fatal(err)
	}
	if prep := m.taskPreparations[token]; prep.err != nil {
		t.Fatalf("prepare workspace project: %v", prep.err)
	}
	branch := st.sessions["mer-1"].Metadata.Branch
	if _, err := os.Stat(filepath.Join(st.sessions["mer-1"].Metadata.WorkspacePath, "local-assets", "note.txt")); err != nil {
		t.Fatalf("prepared workspace missing copied asset: %v", err)
	}
	if err := m.CancelTaskPreparation(context.Background(), token); err != nil {
		t.Fatal(err)
	}
	assertManagerBranchAbsent(t, root, branch)
	assertManagerBranchAbsent(t, childPath, branch)
}

func TestStartupCleanupPreservesUnrecordedDirtyChildWorktree(t *testing.T) {
	m, st, _, root := newGitTaskPreparationManager(t)
	child := newManagerGitRepo(t)
	childPath := filepath.Join(root, "api")
	if err := os.Rename(child, childPath); err != nil {
		t.Fatal(err)
	}
	runManagerGit(t, childPath, "remote", "add", "origin", childPath)
	runManagerGit(t, childPath, "fetch", "origin", "main")
	runManagerGit(t, childPath, "remote", "set-head", "origin", "main")
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("api/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runManagerGit(t, root, "add", ".gitignore")
	runManagerGit(t, root, "commit", "-m", "ignore child checkout")
	project := st.projects["mer"]
	project.Kind = domain.ProjectKindWorkspace
	st.projects["mer"] = project
	st.workspaceRepo["mer"] = []domain.WorkspaceRepoRecord{{Name: "api", RelativePath: "api", GitStatus: domain.GitStatusReady}}
	token, err := m.PrepareTaskWorkspace(context.Background(), project)
	if err != nil {
		t.Fatal(err)
	}
	if prep := m.taskPreparations[token]; prep.err != nil {
		t.Fatal(prep.err)
	}
	rows := st.worktrees[domain.SessionID(token)]
	if len(rows) != 2 {
		t.Fatalf("worktree rows = %+v, want root and child", rows)
	}
	childWorktree := rows[1].WorktreePath
	file := filepath.Join(childWorktree, "user.txt")
	if err := os.WriteFile(file, []byte("keep me\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Simulate a crash after Git created the child but before its DB upsert.
	st.worktrees[domain.SessionID(token)] = rows[:1]
	prep := m.taskPreparations[token]
	prep.timer.Stop()
	delete(m.taskPreparations, token)
	if err := m.CleanupInterruptedTaskPreparations(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(file); err != nil {
		t.Fatalf("unrecorded dirty child contents removed: %v", err)
	}
	if _, err := os.Stat(rows[0].WorktreePath); err != nil {
		t.Fatalf("parent removed before unrecorded child cleanup: %v", err)
	}
	if _, ok := st.sessions[domain.SessionID(token)]; !ok {
		t.Fatal("startup cleanup deleted retry row for unrecorded child")
	}
}

func TestStartupCleanupPreservesWorkspaceProjectWithNoWorktreeRows(t *testing.T) {
	m, st, _, ws := newManager()
	project := st.projects["mer"]
	project.Kind = domain.ProjectKindWorkspace
	st.projects["mer"] = project
	st.workspaceRepo["mer"] = []domain.WorkspaceRepoRecord{{Name: "api", RelativePath: "api", GitStatus: domain.GitStatusReady}}
	st.sessions["mer-1"] = domain.SessionRecord{
		ID: "mer-1", ProjectID: "mer", Kind: domain.KindWorker, IsTaskPreparation: true,
		Metadata: domain.SessionMetadata{Branch: "ao/mer-1/root"},
	}
	if err := m.CleanupInterruptedTaskPreparations(context.Background()); err != nil {
		t.Fatal(err)
	}
	if ws.destroyed != 0 {
		t.Fatalf("destroy calls = %d before any worktree was accounted for", ws.destroyed)
	}
	if _, ok := st.sessions["mer-1"]; !ok {
		t.Fatal("preparation without worktree rows lost its recovery handle")
	}
}

func TestStartupCleanupPreservesUnregisteredRecoveredChildWithUnknownCreationSHA(t *testing.T) {
	m, st, _, root := newGitTaskPreparationManager(t)
	child := newManagerGitRepo(t)
	childPath := filepath.Join(root, "api")
	if err := os.Rename(child, childPath); err != nil {
		t.Fatal(err)
	}
	runManagerGit(t, childPath, "remote", "add", "origin", childPath)
	runManagerGit(t, childPath, "fetch", "origin", "main")
	runManagerGit(t, childPath, "remote", "set-head", "origin", "main")
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("api/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runManagerGit(t, root, "add", ".gitignore")
	runManagerGit(t, root, "commit", "-m", "ignore child checkout")
	project := st.projects["mer"]
	project.Kind = domain.ProjectKindWorkspace
	st.projects["mer"] = project
	st.workspaceRepo["mer"] = []domain.WorkspaceRepoRecord{{Name: "api", RelativePath: "api", GitStatus: domain.GitStatusReady}}
	token, err := m.PrepareTaskWorkspace(context.Background(), project)
	if err != nil {
		t.Fatal(err)
	}
	if prep := m.taskPreparations[token]; prep.err != nil {
		t.Fatal(prep.err)
	}
	rows := st.worktrees[domain.SessionID(token)]
	if len(rows) != 2 || rows[1].BaseSHA == "" {
		t.Fatalf("prepared rows = %+v, want root and child comparison base", rows)
	}
	// A recovered branch has no trusted creation SHA. Then Git loses the
	// child registration while an unregistered, nonempty path remains.
	rows[1].CreationSHA = ""
	st.worktrees[domain.SessionID(token)] = rows
	runManagerGit(t, childPath, "worktree", "remove", "--force", rows[1].WorktreePath)
	if err := os.MkdirAll(rows[1].WorktreePath, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(rows[1].WorktreePath, "user.txt")
	if err := os.WriteFile(file, []byte("keep me\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	prep := m.taskPreparations[token]
	prep.timer.Stop()
	delete(m.taskPreparations, token)
	if err := m.CleanupInterruptedTaskPreparations(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(file); err != nil {
		t.Fatalf("unregistered child contents removed: %v", err)
	}
	if _, err := os.Stat(rows[0].WorktreePath); err != nil {
		t.Fatalf("parent removed after child refusal: %v", err)
	}
	if _, ok := st.sessions[domain.SessionID(token)]; !ok {
		t.Fatal("dirty recovered child lost its hidden cleanup row")
	}
}

func TestCancelTaskPreparationCanRetryAfterCleanupFailure(t *testing.T) {
	m, st, _, ws := newManager()
	m.runBackground = func(work func()) { work() }
	m.taskPreparationTTL = time.Hour
	token, err := m.PrepareTaskWorkspace(context.Background(), st.projects["mer"])
	if err != nil {
		t.Fatal(err)
	}
	st.deletePrepErr = errors.New("database busy")
	if err := m.CancelTaskPreparation(context.Background(), token); err == nil {
		t.Fatal("cancel succeeded despite delete failure")
	}
	m.taskPreparationsMu.Lock()
	_, retained := m.taskPreparations[token]
	m.taskPreparationsMu.Unlock()
	if !retained {
		t.Fatal("failed cleanup consumed its retry handle")
	}

	st.deletePrepErr = nil
	if err := m.CancelTaskPreparation(context.Background(), token); err != nil {
		t.Fatal(err)
	}
	if ws.destroyed != 1 {
		t.Fatalf("workspace destroy calls = %d, want one successful cleanup followed by a row-only retry", ws.destroyed)
	}
	if _, ok := st.sessions["mer-1"]; ok {
		t.Fatal("retried cleanup left preparation row behind")
	}
}

func TestClaimedPreparationRollsBackWhenChatQueueFails(t *testing.T) {
	launcher := &recordingLauncher{queueErr: errors.New("queue unavailable")}
	m, st, _ := newChatManager(launcher)
	m.runBackground = func(work func()) { work() }
	ws := m.workspace.(*fakeWorkspace)

	token, err := m.PrepareTaskWorkspace(context.Background(), st.projects[string(chatTestProject)])
	if err != nil {
		t.Fatal(err)
	}
	cfg := asyncChatSpawnConfig("do the thing")
	cfg.TaskPreparation = token
	if _, _, _, err := m.Spawn(context.Background(), cfg); err == nil {
		t.Fatal("spawn succeeded despite queue failure")
	}
	if _, ok := st.sessions["mer-1"]; ok {
		t.Fatal("failed prepared spawn left its session row")
	}
	if ws.destroyed != 1 {
		t.Fatalf("destroy calls = %d, want 1", ws.destroyed)
	}
}

func TestTaskPreparationExpires(t *testing.T) {
	m, st, _, ws := newManager()
	m.runBackground = func(work func()) { work() }
	m.taskPreparationTTL = 5 * time.Millisecond

	token, err := m.PrepareTaskWorkspace(context.Background(), st.projects["mer"])
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		m.taskPreparationsMu.Lock()
		_, active := m.taskPreparations[token]
		m.taskPreparationsMu.Unlock()
		if !active {
			if _, ok := st.sessions["mer-1"]; ok {
				t.Fatal("expired preparation row still exists")
			}
			if ws.destroyed != 1 {
				t.Fatalf("destroy calls = %d, want 1", ws.destroyed)
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("preparation did not expire")
}

func TestTaskPreparationExpiryDeletesUnmodifiedBranch(t *testing.T) {
	m, st, _, repo := newGitTaskPreparationManager(t)
	token, err := m.PrepareTaskWorkspace(context.Background(), st.projects["mer"])
	if err != nil {
		t.Fatal(err)
	}
	branch := st.sessions["mer-1"].Metadata.Branch
	m.taskPreparationsMu.Lock()
	prep := m.taskPreparations[token]
	prep.timer.Stop()
	m.taskPreparationTTL = time.Millisecond
	m.scheduleTaskPreparationCleanup(token, prep)
	m.taskPreparationsMu.Unlock()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		m.taskPreparationsMu.Lock()
		_, active := m.taskPreparations[token]
		m.taskPreparationsMu.Unlock()
		if !active {
			assertManagerBranchAbsent(t, repo, branch)
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("preparation expiry did not finish cleanup")
}

func TestStartupCleansInterruptedTaskPreparation(t *testing.T) {
	m, st, _, ws := newManager()
	st.sessions["mer-1"] = domain.SessionRecord{
		ID:                "mer-1",
		ProjectID:         "mer",
		Kind:              domain.KindWorker,
		IsTaskPreparation: true,
		Metadata: domain.SessionMetadata{
			Branch:        "ao/mer-1/root",
			WorkspacePath: "/ws/mer-1",
		},
	}

	if err := m.CleanupInterruptedTaskPreparations(context.Background()); err != nil {
		t.Fatal(err)
	}
	if ws.destroyed != 1 {
		t.Fatalf("destroy calls = %d, want 1", ws.destroyed)
	}
	if _, ok := st.sessions["mer-1"]; ok {
		t.Fatal("interrupted preparation row still exists")
	}
}

func TestStartupCleanupDeletesUnmodifiedBranch(t *testing.T) {
	m, st, _, repo := newGitTaskPreparationManager(t)
	token, err := m.PrepareTaskWorkspace(context.Background(), st.projects["mer"])
	if err != nil {
		t.Fatal(err)
	}
	branch := st.sessions["mer-1"].Metadata.Branch
	m.taskPreparationsMu.Lock()
	prep := m.taskPreparations[token]
	prep.timer.Stop()
	delete(m.taskPreparations, token)
	m.taskPreparationsMu.Unlock()
	if err := m.CleanupInterruptedTaskPreparations(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertManagerBranchAbsent(t, repo, branch)
	if _, ok := st.sessions["mer-1"]; ok {
		t.Fatal("startup cleanup left hidden reservation")
	}
}

func TestStartupDropsInterruptedPreparationWithoutCreatingWorkspace(t *testing.T) {
	m, st, _, ws := newManager()
	delete(st.projects, "mer")
	ws.createErr = errors.New("repository moved")
	st.sessions["mer-1"] = domain.SessionRecord{
		ID: "mer-1", ProjectID: "mer", Kind: domain.KindWorker,
		IsTaskPreparation: true,
		Metadata:          domain.SessionMetadata{Branch: "ao/mer-1/root"},
	}

	if err := m.CleanupInterruptedTaskPreparations(context.Background()); err != nil {
		t.Fatalf("startup cleanup blocked daemon: %v", err)
	}
	if ws.createCount != 0 {
		t.Fatalf("workspace creates = %d, want zero", ws.createCount)
	}
	if _, ok := st.sessions["mer-1"]; ok {
		t.Fatal("interrupted preparation row still exists")
	}
}

func TestStartupCleanupFailureDoesNotBlockDaemon(t *testing.T) {
	m, st, _, ws := newManager()
	ws.destroyErr = errors.New("worktree is busy")
	st.sessions["mer-1"] = domain.SessionRecord{
		ID: "mer-1", ProjectID: "mer", Kind: domain.KindWorker,
		IsTaskPreparation: true,
		Metadata:          domain.SessionMetadata{Branch: "ao/mer-1/root", WorkspacePath: "/ws/mer-1"},
	}

	if err := m.CleanupInterruptedTaskPreparations(context.Background()); err != nil {
		t.Fatalf("cleanup failure blocked daemon startup: %v", err)
	}
	if _, ok := st.sessions["mer-1"]; !ok {
		t.Fatal("failed cleanup lost the hidden row needed for a later retry")
	}
}

func TestStartupSafetyDefersInterruptedPreparationGitCleanup(t *testing.T) {
	m, st, _, ws := newManager()
	st.sessions["mer-1"] = domain.SessionRecord{
		ID: "mer-1", ProjectID: "mer", Kind: domain.KindWorker,
		IsTaskPreparation: true, ProvisionState: domain.SessionProvisionProvisioning,
		CreatedAt: time.Date(2026, time.September, 20, 12, 0, 0, 0, time.UTC),
		Metadata:  domain.SessionMetadata{Branch: "ao/mer-1/root", WorkspacePath: "/ws/mer-1"},
	}
	if err := m.ReconcileStartupSafety(context.Background()); err != nil {
		t.Fatal(err)
	}
	if ws.destroyed != 0 {
		t.Fatalf("startup safety destroyed %d worktrees before the listener bound", ws.destroyed)
	}
	if rec, ok := st.sessions["mer-1"]; !ok || rec.ProvisionState != domain.SessionProvisionProvisioning {
		t.Fatalf("startup safety touched interrupted preparation: %+v, exists=%v", rec, ok)
	}
	// A preparation opened after the listener binds belongs to this daemon,
	// not the interrupted set captured by startup safety.
	st.sessions["mer-2"] = domain.SessionRecord{
		ID: "mer-2", ProjectID: "mer", Kind: domain.KindWorker,
		IsTaskPreparation: true, ProvisionState: domain.SessionProvisionProvisioning,
		Metadata: domain.SessionMetadata{Branch: "ao/mer-2/root", WorkspacePath: "/ws/mer-2"},
	}
	st.sessions["mer-3"] = domain.SessionRecord{
		ID: "mer-3", ProjectID: "mer", Kind: domain.KindWorker,
		Mode: domain.SessionModeChat, ProvisionState: domain.SessionProvisionProvisioning,
	}
	if err := m.ReconcileBackground(context.Background()); err != nil {
		t.Fatal(err)
	}
	if ws.destroyed != 1 {
		t.Fatalf("background cleanup destroyed %d worktrees, want 1", ws.destroyed)
	}
	if _, ok := st.sessions["mer-1"]; ok {
		t.Fatal("background cleanup left the preparation row")
	}
	if rec, ok := st.sessions["mer-2"]; !ok || rec.ProvisionState != domain.SessionProvisionProvisioning {
		t.Fatalf("background cleanup touched a new preparation: %+v, exists=%v", rec, ok)
	}
	if got := st.sessions["mer-3"].ProvisionState; got != domain.SessionProvisionProvisioning {
		t.Fatalf("background retry marked a new async spawn %q, want provisioning", got)
	}
}
