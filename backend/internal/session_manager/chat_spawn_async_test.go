package sessionmanager

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	browsersvc "github.com/aoagents/agent-orchestrator/backend/internal/service/browser"
)

// deferredBackground captures the asynchronous half so a test can inspect the
// state the API answered with before the background work runs.
func deferredBackground(m *Manager) *[]func() {
	deferred := &[]func(){}
	m.runBackground = func(work func()) { *deferred = append(*deferred, work) }
	return deferred
}

func asyncChatSpawnConfig(prompt string) ports.SpawnConfig {
	return ports.SpawnConfig{
		ProjectID:     chatTestProject,
		Kind:          domain.KindWorker,
		Harness:       domain.HarnessCodex,
		Prompt:        prompt,
		RequestedMode: domain.SessionModeChat,
		Async:         true,
	}
}

type blockingWorkspacePublishStore struct {
	*fakeStore
	entered chan context.Context
	release chan struct{}
}

type failReadyProvisionStore struct {
	*fakeStore
	failed bool
}

func (s *failReadyProvisionStore) SetSessionProvisionState(ctx context.Context, id domain.SessionID, state domain.SessionProvisionState, message string, now time.Time) (bool, error) {
	if state == domain.SessionProvisionReady && !s.failed {
		s.failed = true
		return false, errors.New("ready write failed")
	}
	return s.fakeStore.SetSessionProvisionState(ctx, id, state, message, now)
}

type countingHarnessUseGate struct{ active int }

func (g *countingHarnessUseGate) TryBeginHarnessUse(domain.AgentHarness) (func(), bool) {
	g.active++
	return func() { g.active-- }, true
}

func (s *blockingWorkspacePublishStore) SetSessionProvisionedWorkspace(
	ctx context.Context,
	id domain.SessionID,
	branch, workspacePath, workspaceRepoPath string,
	now time.Time,
) (bool, error) {
	s.entered <- ctx
	<-s.release
	return s.fakeStore.SetSessionProvisionedWorkspace(ctx, id, branch, workspacePath, workspaceRepoPath, now)
}

// The point of the asynchronous path: the caller gets an addressable session
// before the expensive work starts, and the opening prompt is already in the
// durable queue rather than waiting on a controller that does not exist.
func TestSpawnAsyncChat_AnswersBeforeWorkspaceAndController(t *testing.T) {
	launcher := &recordingLauncher{}
	m, st, rt := newChatManager(launcher)
	m.browserCapabilities = browsersvc.NewAuthority()
	deferred := deferredBackground(m)
	ws := m.workspace.(*fakeWorkspace)

	rec, _, _, err := m.Spawn(context.Background(), asyncChatSpawnConfig("do the thing"))
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	if rec.ProvisionState != domain.SessionProvisionProvisioning {
		t.Fatalf("provision state = %q, want provisioning", rec.ProvisionState)
	}
	if rec.Metadata.WorkspacePath != "" {
		t.Fatalf("workspace path = %q, want none yet", rec.Metadata.WorkspacePath)
	}
	if ws.lastCfg.SessionID != "" {
		t.Fatal("workspace was created before the caller was answered")
	}
	if len(launcher.started) != 0 {
		t.Fatal("controller started before the caller was answered")
	}
	if got := launcher.queued; len(got) != 1 || got[0] != "do the thing" {
		t.Fatalf("queued = %v, want the opening prompt", got)
	}
	if rt.created != 0 {
		t.Fatal("chat spawn touched the terminal runtime")
	}

	if len(*deferred) != 1 {
		t.Fatalf("background work = %d, want 1", len(*deferred))
	}
	(*deferred)[0]()

	if ws.lastCfg.SessionID != rec.ID {
		t.Fatalf("workspace session = %q, want %q", ws.lastCfg.SessionID, rec.ID)
	}
	if len(launcher.started) != 1 {
		t.Fatalf("controllers started = %d, want 1", len(launcher.started))
	}
	if got := launcher.drained; len(got) != 1 || got[0] != rec.ID {
		t.Fatalf("drained = %v, want %q", got, rec.ID)
	}
	// The queue owns delivery. Sending the prompt again here would run the
	// user's brief twice.
	if len(launcher.turns) != 0 {
		t.Fatalf("prompt was also sent directly: %v", launcher.turns)
	}
	if got := st.sessions[rec.ID].ProvisionState; got != domain.SessionProvisionReady {
		t.Fatalf("provision state after start = %q, want ready", got)
	}
}

func TestSpawnAsyncChatSeedsEffortBeforeBackgroundTitle(t *testing.T) {
	for _, prepared := range []bool{false, true} {
		name := "fresh"
		if prepared {
			name = "prepared"
		}
		t.Run(name, func(t *testing.T) {
			launcher := &recordingLauncher{}
			m, st, _ := newChatManager(launcher)
			m.dataDir = t.TempDir()
			m.browserCapabilities = browsersvc.NewAuthority()
			deferred := deferredBackground(m)
			project := st.projects[string(chatTestProject)]
			project.Config.AgentConfig.Effort = "high"
			st.projects[string(chatTestProject)] = project

			cfg := asyncChatSpawnConfig("do the thing")
			if prepared {
				token, err := m.PrepareTaskWorkspace(context.Background(), project)
				if err != nil {
					t.Fatal(err)
				}
				(*deferred)[0]()
				cfg.TaskPreparation = token
			}
			rec, _, _, err := m.Spawn(context.Background(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			if rec.Metadata.Effort != "high" {
				t.Fatalf("effort before controller start = %q, want high", rec.Metadata.Effort)
			}
			if _, err := m.RunBackgroundTask(context.Background(), rec.ID, "title only", "do the thing"); err != nil {
				t.Fatal(err)
			}
			if len(launcher.background) != 1 || launcher.background[0].Effort != "high" {
				t.Fatalf("background title task = %#v, want high effort", launcher.background)
			}
		})
	}
}

func TestAsyncChatSpawnHoldsHarnessGateThroughControllerStart(t *testing.T) {
	launcher := &recordingLauncher{}
	m, _, _ := newChatManager(launcher)
	m.browserCapabilities = browsersvc.NewAuthority()
	deferred := deferredBackground(m)
	gate := &countingHarnessUseGate{}
	m.SetHarnessUseGate(gate)

	if _, _, _, err := m.Spawn(context.Background(), asyncChatSpawnConfig("do the thing")); err != nil {
		t.Fatal(err)
	}
	if gate.active != 1 {
		t.Fatalf("harness uses before background start = %d, want 1", gate.active)
	}
	(*deferred)[0]()
	if len(launcher.started) != 1 || gate.active != 0 {
		t.Fatalf("controller starts = %d, harness uses after start = %d", len(launcher.started), gate.active)
	}
}

// A start that fails after the API answered must leave the session — and the
// messages queued into it — in place, with a reason the user can read.
func TestSpawnAsyncChat_FailedStartKeepsSessionAndReason(t *testing.T) {
	launcher := &recordingLauncher{}
	m, st, _ := newChatManager(launcher)
	m.browserCapabilities = browsersvc.NewAuthority()
	deferred := deferredBackground(m)
	ws := m.workspace.(*fakeWorkspace)
	ws.createErr = errors.New("branch already checked out")

	rec, _, _, err := m.Spawn(context.Background(), asyncChatSpawnConfig("do the thing"))
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	(*deferred)[0]()

	stored, ok := st.sessions[rec.ID]
	if !ok {
		t.Fatal("failed start deleted the session the user is looking at")
	}
	if stored.ProvisionState != domain.SessionProvisionFailed {
		t.Fatalf("provision state = %q, want failed", stored.ProvisionState)
	}
	if !strings.Contains(stored.ProvisionError, "branch already checked out") {
		t.Fatalf("provision error = %q, want the workspace failure", stored.ProvisionError)
	}
	if len(launcher.started) != 0 {
		t.Fatal("controller started despite the workspace failing")
	}
}

func TestSpawnAsyncChat_DrainFailureLeavesRetryableSession(t *testing.T) {
	launcher := &recordingLauncher{drainErr: errors.New("controller stopped before dispatch")}
	m, st, _ := newChatManager(launcher)
	m.browserCapabilities = browsersvc.NewAuthority()
	deferred := deferredBackground(m)
	rec, _, _, err := m.Spawn(context.Background(), asyncChatSpawnConfig("do the thing"))
	if err != nil {
		t.Fatal(err)
	}
	(*deferred)[0]()
	stored := st.sessions[rec.ID]
	if stored.ProvisionState != domain.SessionProvisionFailed || !strings.Contains(stored.ProvisionError, "controller stopped before dispatch") {
		t.Fatalf("drain failure left %+v, want failed session with retry reason", stored)
	}
	if len(launcher.stopped) != 1 || launcher.stopped[0] != rec.ID {
		t.Fatalf("stopped controllers = %v, want failed controller stopped", launcher.stopped)
	}
}

func TestResumeFailedAsyncChatSpawnRetriesSameSessionAndQueue(t *testing.T) {
	launcher := &recordingLauncher{}
	m, st, _ := newChatManager(launcher)
	m.browserCapabilities = browsersvc.NewAuthority()
	deferred := deferredBackground(m)
	gate := &countingHarnessUseGate{}
	m.SetHarnessUseGate(gate)
	ws := m.workspace.(*fakeWorkspace)
	ws.createErr = errors.New("temporary git failure")
	rec, _, _, err := m.Spawn(context.Background(), asyncChatSpawnConfig("do the thing"))
	if err != nil {
		t.Fatal(err)
	}
	(*deferred)[0]()
	if st.sessions[rec.ID].ProvisionState != domain.SessionProvisionFailed {
		t.Fatal("initial start did not fail")
	}

	ws.createErr = nil
	retried, err := m.ResumeAgentWithMode(context.Background(), rec.ID)
	if err != nil {
		t.Fatalf("retry failed start: %v", err)
	}
	if retried.Session.ID != rec.ID || retried.Session.ProvisionState != domain.SessionProvisionProvisioning {
		t.Fatalf("retry result = %+v, want same provisioning session", retried.Session)
	}
	if gate.active != 1 {
		t.Fatalf("harness uses before retry background start = %d, want 1", gate.active)
	}
	if len(launcher.queued) != 1 {
		t.Fatalf("opening prompt queued %d times, want once", len(launcher.queued))
	}
	if _, err := m.ResumeAgentWithMode(context.Background(), rec.ID); !errors.Is(err, ErrResumeInProgress) {
		t.Fatalf("second resume during background retry = %v, want ErrResumeInProgress", err)
	}
	if len(*deferred) != 2 {
		t.Fatalf("background starts = %d, want only initial attempt and one retry", len(*deferred))
	}
	(*deferred)[1]()
	if gate.active != 0 {
		t.Fatalf("harness uses after retry background start = %d, want 0", gate.active)
	}
	if got := st.sessions[rec.ID].ProvisionState; got != domain.SessionProvisionReady {
		t.Fatalf("retry provision state = %q, want ready", got)
	}
	if len(launcher.started) != 1 || len(launcher.drained) != 1 {
		t.Fatalf("controllers started = %d, queues drained = %d", len(launcher.started), len(launcher.drained))
	}
}

func TestResumeCannotStartSecondControllerDuringInitialAsyncSpawn(t *testing.T) {
	m, _, _ := newChatManager(&recordingLauncher{})
	m.browserCapabilities = browsersvc.NewAuthority()
	deferred := deferredBackground(m)
	rec, _, _, err := m.Spawn(context.Background(), asyncChatSpawnConfig("do the thing"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.ResumeAgentWithMode(context.Background(), rec.ID); !errors.Is(err, ErrResumeInProgress) {
		t.Fatalf("resume while initial start is provisioning = %v, want ErrResumeInProgress", err)
	}
	if len(*deferred) != 1 {
		t.Fatalf("background starts = %d, want only initial start", len(*deferred))
	}
	(*deferred)[0]()
}

func TestResumeFailedAsyncChatSpawnRetainsOpeningAttachments(t *testing.T) {
	dataDir := t.TempDir()
	workspaceDir := t.TempDir()
	st := newFakeStore()
	st.projects[string(chatTestProject)] = domain.ProjectRecord{ID: string(chatTestProject), Config: testRoleAgents()}
	ws := &fakeWorkspace{path: workspaceDir, createErr: errors.New("temporary git failure")}
	m := New(Deps{
		Runtime: &fakeRuntime{}, Agents: fakeAgents{}, Workspace: ws, Store: st,
		Messenger: &fakeMessenger{}, Chat: &recordingLauncher{}, Lifecycle: &fakeLCM{store: st},
		DataDir: dataDir, LookPath: func(string) (string, error) { return "/bin/true", nil },
	})
	m.browserCapabilities = browsersvc.NewAuthority()
	deferred := deferredBackground(m)
	cfg := asyncChatSpawnConfig("look at this")
	cfg.Attachments = []ports.SpawnAttachment{{Ext: ".png", Data: []byte("image")}}
	rec, _, _, err := m.Spawn(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	(*deferred)[0]()
	if got := st.sessions[rec.ID].ProvisionState; got != domain.SessionProvisionFailed {
		t.Fatalf("first start state = %q, want failed", got)
	}
	ws.createErr = nil
	if _, err := m.ResumeAgentWithMode(context.Background(), rec.ID); err != nil {
		t.Fatal(err)
	}
	(*deferred)[1]()
	attachment := filepath.Join(workspaceDir, attachmentsDir, "attachment-1.png")
	if body, err := os.ReadFile(attachment); err != nil || string(body) != "image" {
		t.Fatalf("opening attachment after retry = %q, %v", body, err)
	}
}

func TestResumeFailedAsyncChatSpawnRetriesAttachmentProjection(t *testing.T) {
	dataDir := t.TempDir()
	workspacePath := filepath.Join(t.TempDir(), "workspace")
	if err := os.WriteFile(workspacePath, []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}
	st := newFakeStore()
	st.projects[string(chatTestProject)] = domain.ProjectRecord{ID: string(chatTestProject), Config: testRoleAgents()}
	ws := &fakeWorkspace{path: workspacePath}
	m := New(Deps{
		Runtime: &fakeRuntime{}, Agents: fakeAgents{}, Workspace: ws, Store: st,
		Messenger: &fakeMessenger{}, Chat: &recordingLauncher{}, Lifecycle: &fakeLCM{store: st},
		DataDir: dataDir, LookPath: func(string) (string, error) { return "/bin/true", nil },
	})
	m.browserCapabilities = browsersvc.NewAuthority()
	deferred := deferredBackground(m)
	cfg := asyncChatSpawnConfig("look at this")
	cfg.Attachments = []ports.SpawnAttachment{{Ext: ".png", Data: []byte("image")}}
	rec, _, _, err := m.Spawn(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	(*deferred)[0]()
	failed := st.sessions[rec.ID]
	if failed.ProvisionState != domain.SessionProvisionFailed || failed.Metadata.WorkspacePath != workspacePath {
		t.Fatalf("projection failure did not retain reusable workspace: %+v", failed)
	}
	if err := os.Remove(workspacePath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(workspacePath, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := m.ResumeAgentWithMode(context.Background(), rec.ID); err != nil {
		t.Fatal(err)
	}
	(*deferred)[1]()
	attachment := filepath.Join(workspacePath, attachmentsDir, "attachment-1.png")
	if body, err := os.ReadFile(attachment); err != nil || string(body) != "image" {
		t.Fatalf("attachment after projection retry = %q, %v", body, err)
	}
}

func TestResumeFailedAsyncChatSpawnReusesPublishedWorkspace(t *testing.T) {
	launcher := &recordingLauncher{}
	m, st, _ := newChatManager(launcher)
	m.browserCapabilities = browsersvc.NewAuthority()
	deferred := deferredBackground(m)
	workspacePath := t.TempDir()
	project := st.projects[string(chatTestProject)]
	project.Config.PostCreate = []string{"printf x >> provision-runs"}
	st.projects[string(chatTestProject)] = project
	if err := os.WriteFile(filepath.Join(workspacePath, "provision-runs"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	st.sessions["mer-1"] = domain.SessionRecord{
		ID: "mer-1", ProjectID: chatTestProject, Kind: domain.KindWorker,
		Harness: domain.HarnessCodex, Mode: domain.SessionModeChat,
		ProvisionState: domain.SessionProvisionFailed,
		Metadata:       domain.SessionMetadata{Branch: "ao/mer-1/root", WorkspacePath: workspacePath},
	}
	ws := m.workspace.(*fakeWorkspace)
	ws.createErr = errors.New("must not create another worktree")

	if _, err := m.ResumeAgentWithMode(context.Background(), "mer-1"); err != nil {
		t.Fatal(err)
	}
	(*deferred)[0]()
	if ws.createCount != 0 {
		t.Fatalf("workspace creates = %d, want reuse", ws.createCount)
	}
	if got := st.sessions["mer-1"].ProvisionState; got != domain.SessionProvisionReady {
		t.Fatalf("provision state = %q, want ready", got)
	}
	if body, err := os.ReadFile(filepath.Join(workspacePath, "provision-runs")); err != nil || string(body) != "x" {
		t.Fatalf("post-create ran again on published workspace: %q, %v", body, err)
	}
}

func TestResumeFailedAsyncChatSpawnAdoptsLiveController(t *testing.T) {
	launcher := &recordingLauncher{live: true}
	m, st, _ := newChatManager(launcher)
	st.sessions["mer-1"] = domain.SessionRecord{
		ID: "mer-1", ProjectID: chatTestProject, Kind: domain.KindWorker,
		Harness: domain.HarnessCodex, Mode: domain.SessionModeChat,
		ProvisionState: domain.SessionProvisionFailed,
	}

	result, err := m.ResumeAgentWithMode(context.Background(), "mer-1")
	if err != nil {
		t.Fatal(err)
	}
	if result.Session.ProvisionState != domain.SessionProvisionReady || len(launcher.started) != 0 || len(launcher.drained) != 1 {
		t.Fatalf("adoption = %+v, controllers started = %d, queues drained = %d", result.Session, len(launcher.started), len(launcher.drained))
	}
}

func TestCancelAsyncChatSpawnClearsStaleStartingState(t *testing.T) {
	m, st, _ := newChatManager(&recordingLauncher{})
	st.sessions["mer-1"] = domain.SessionRecord{
		ID: "mer-1", ProvisionState: domain.SessionProvisionProvisioning,
	}
	if err := m.cancelAsyncChatSpawn(context.Background(), "mer-1"); err != nil {
		t.Fatal(err)
	}
	if got := st.sessions["mer-1"].ProvisionState; got != domain.SessionProvisionFailed {
		t.Fatalf("provision state = %q, want failed", got)
	}
}

// An empty brief queues no turn, so the row still matches the seed-state
// predicate that spawn rollback deletes on. Once the id has been handed to a
// client, deleting it would turn an open session into a 404.
func TestSpawnAsyncChat_PublishedSessionIsNeverDeleted(t *testing.T) {
	launcher := &recordingLauncher{startErr: errors.New("provider refused the session")}
	m, st, _ := newChatManager(launcher)
	m.browserCapabilities = browsersvc.NewAuthority()
	deferred := deferredBackground(m)

	rec, _, _, err := m.Spawn(context.Background(), asyncChatSpawnConfig(""))
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	if len(launcher.queued) != 0 {
		t.Fatalf("queued = %v, want nothing for an empty brief", launcher.queued)
	}
	(*deferred)[0]()

	stored, ok := st.sessions[rec.ID]
	if !ok {
		t.Fatal("a failed start deleted the session the client already holds")
	}
	if stored.ProvisionState != domain.SessionProvisionFailed {
		t.Fatalf("provision state = %q, want failed", stored.ProvisionState)
	}
}

// Every workspace-scoped read answers SESSION_WORKSPACE_NOT_FOUND while the row
// claims no worktree. Waiting for the controller commit to publish it leaves the
// session lying about itself for the whole provider start, which is long enough
// for the desktop's bounded readiness poll to give up on a healthy session.
func TestSpawnAsyncChat_PublishesTheWorktreeBeforeTheController(t *testing.T) {
	launcher := &recordingLauncher{startErr: errors.New("provider is slow today")}
	m, st, _ := newChatManager(launcher)
	m.browserCapabilities = browsersvc.NewAuthority()
	deferred := deferredBackground(m)

	var publishedPath string
	launcher.beforeStart = func(start ChatStart) {
		publishedPath = st.sessions[start.SessionID].Metadata.WorkspacePath
	}
	rec, _, _, err := m.Spawn(context.Background(), asyncChatSpawnConfig("do the thing"))
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	(*deferred)[0]()

	// The controller never started, so only the early publish can have made the
	// path visible to StartChat — exactly the window the desktop polls through.
	if publishedPath == "" {
		t.Fatal("the worktree was not published before controller startup")
	}
	stored := st.sessions[rec.ID]
	if stored.Metadata.WorkspacePath != "" || stored.Metadata.Branch != "" {
		t.Fatalf("failed session retained removed workspace metadata: %+v", stored.Metadata)
	}
	if stored.IsTerminated {
		t.Fatal("failed asynchronous session was hidden as terminated")
	}
}

func TestSpawnAsyncChat_DaemonShutdownCancelsAndWaitsForWorker(t *testing.T) {
	launcher := &recordingLauncher{}
	m, st, _ := newChatManager(launcher)
	m.browserCapabilities = browsersvc.NewAuthority()
	daemonCtx, cancelDaemon := context.WithCancel(context.Background())
	m.backgroundContext = daemonCtx
	deferred := deferredBackground(m)
	ws := m.workspace.(*fakeWorkspace)

	rec, _, _, err := m.Spawn(context.Background(), asyncChatSpawnConfig("do the thing"))
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	cancelDaemon()
	go (*deferred)[0]()
	waitCtx, cancelWait := context.WithTimeout(context.Background(), time.Second)
	defer cancelWait()
	if err := m.WaitBackgroundWorkers(waitCtx); err != nil {
		t.Fatalf("wait background workers: %v", err)
	}
	if len(launcher.started) != 0 {
		t.Fatal("controller started after daemon shutdown")
	}
	if ws.destroyed != 1 {
		t.Fatalf("destroyed workspaces = %d, want 1", ws.destroyed)
	}
	if got := st.sessions[rec.ID].ProvisionState; got != domain.SessionProvisionFailed {
		t.Fatalf("provision state = %q after cancellation, want failed", got)
	}
}

func TestSpawnAsyncChat_ReadyWriteFailureMarksSessionFailed(t *testing.T) {
	launcher := &recordingLauncher{}
	m, st, _ := newChatManager(launcher)
	m.store = &failReadyProvisionStore{fakeStore: st}
	m.browserCapabilities = browsersvc.NewAuthority()
	deferred := deferredBackground(m)
	rec, _, _, err := m.Spawn(context.Background(), asyncChatSpawnConfig("do the thing"))
	if err != nil {
		t.Fatal(err)
	}
	(*deferred)[0]()
	stored := st.sessions[rec.ID]
	if stored.ProvisionState != domain.SessionProvisionFailed || !strings.Contains(stored.ProvisionError, "ready write failed") {
		t.Fatalf("ready write failure left session %+v", stored)
	}
}

func TestSpawnAsyncChat_FailedCleanupRetainsWorkspacePath(t *testing.T) {
	m, st, _ := newChatManager(&recordingLauncher{})
	m.browserCapabilities = browsersvc.NewAuthority()
	deferred := deferredBackground(m)
	ws := m.workspace.(*fakeWorkspace)
	ws.path = t.TempDir()
	ws.destroyErr = errors.New("dirty worktree")
	project := st.projects[string(chatTestProject)]
	project.Config.PostCreate = []string{"exit 3"}
	st.projects[string(chatTestProject)] = project
	rec, _, _, err := m.Spawn(context.Background(), asyncChatSpawnConfig("do the thing"))
	if err != nil {
		t.Fatal(err)
	}
	(*deferred)[0]()
	stored := st.sessions[rec.ID]
	if stored.ProvisionState != domain.SessionProvisionFailed || stored.Metadata.WorkspacePath != ws.path {
		t.Fatalf("failed cleanup lost workspace: %+v", stored)
	}
}

func TestDestroySpawnWorkspace_FailedProjectCleanupRetainsWorktreeRows(t *testing.T) {
	m, st, _ := newChatManager(&recordingLauncher{})
	ws := m.workspace.(*fakeWorkspace)
	ws.destroyErr = errors.New("dirty worktree")
	info := ports.WorkspaceInfo{SessionID: "mer-1", Path: t.TempDir()}
	if err := st.UpsertSessionWorktree(context.Background(), domain.SessionWorktreeRecord{
		SessionID: info.SessionID, RepoName: "root", WorktreePath: info.Path,
	}); err != nil {
		t.Fatal(err)
	}
	project := &ports.WorkspaceProjectInfo{Root: info}
	if m.destroySpawnWorkspace(context.Background(), info, project) {
		t.Fatal("failed workspace cleanup reported success")
	}
	rows, err := st.ListSessionWorktrees(context.Background(), info.SessionID)
	if err != nil || len(rows) != 1 {
		t.Fatalf("preserved workspace rows = %v, %v; want one", rows, err)
	}
}

func TestSpawnAsyncChat_KillFencesWorkspacePublicationAndControllerStart(t *testing.T) {
	launcher := &recordingLauncher{}
	m, st, _ := newChatManager(launcher)
	m.browserCapabilities = browsersvc.NewAuthority()
	blockingStore := &blockingWorkspacePublishStore{
		fakeStore: st,
		entered:   make(chan context.Context, 1),
		release:   make(chan struct{}),
	}
	m.store = blockingStore
	deferred := deferredBackground(m)
	ws := m.workspace.(*fakeWorkspace)

	rec, _, _, err := m.Spawn(context.Background(), asyncChatSpawnConfig("do the thing"))
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	go (*deferred)[0]()
	workerCtx := <-blockingStore.entered
	type killResult struct {
		freed bool
		err   error
	}
	killed := make(chan killResult, 1)
	go func() {
		freed, killErr := m.Kill(context.Background(), rec.ID)
		killed <- killResult{freed: freed, err: killErr}
	}()
	select {
	case <-workerCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("Kill did not cancel the provisioning worker")
	}
	close(blockingStore.release)
	result := <-killed
	if result.err != nil {
		t.Fatalf("kill: %v", result.err)
	}
	if result.freed {
		t.Fatal("Kill reported freeing a workspace already reclaimed by provisioning")
	}
	stored := st.sessions[rec.ID]
	if !stored.IsTerminated {
		t.Fatal("killed session was resurrected")
	}
	if stored.ProvisionState.IsProvisioning() {
		t.Fatal("killed session still reads as starting")
	}
	if stored.Metadata.WorkspacePath != "" {
		t.Fatalf("killed session retained workspace %q", stored.Metadata.WorkspacePath)
	}
	if len(launcher.started) != 0 {
		t.Fatal("controller started after Kill")
	}
	if ws.destroyed != 1 {
		t.Fatalf("destroyed workspaces = %d, want 1", ws.destroyed)
	}
}

func TestSpawnAsyncChat_PreparedProvisionFailureClearsRemovedWorkspace(t *testing.T) {
	launcher := &recordingLauncher{}
	m, st, _ := newChatManager(launcher)
	m.browserCapabilities = browsersvc.NewAuthority()
	ws := m.workspace.(*fakeWorkspace)
	ws.path = t.TempDir()
	project := st.projects[string(chatTestProject)]
	project.Config.PostCreate = []string{"exit 3"}
	st.projects[string(chatTestProject)] = project
	m.runBackground = func(work func()) { work() }
	token, err := m.PrepareTaskWorkspace(context.Background(), project)
	if err != nil {
		t.Fatal(err)
	}
	deferred := deferredBackground(m)
	cfg := asyncChatSpawnConfig("do the thing")
	cfg.TaskPreparation = token
	rec, _, _, err := m.Spawn(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	(*deferred)[0]()

	stored := st.sessions[rec.ID]
	if stored.ProvisionState != domain.SessionProvisionFailed {
		t.Fatalf("provision state = %q, want failed", stored.ProvisionState)
	}
	if stored.Metadata.WorkspacePath != "" || stored.Metadata.Branch != "" {
		t.Fatalf("failed prepared session retained removed workspace: %+v", stored.Metadata)
	}
	if stored.IsTerminated {
		t.Fatal("failed prepared session was hidden as terminated")
	}
}

// A restart leaves nothing behind that could finish a background start, so a
// row left mid-start must not read as "still starting" forever.
func TestFailInterruptedProvisioning(t *testing.T) {
	m, st, _ := newChatManager(&recordingLauncher{})
	st.sessions["mer-1"] = domain.SessionRecord{
		ID: "mer-1", ProjectID: chatTestProject, Kind: domain.KindWorker,
		Mode: domain.SessionModeChat, ProvisionState: domain.SessionProvisionProvisioning,
	}
	st.sessions["mer-2"] = domain.SessionRecord{
		ID: "mer-2", ProjectID: chatTestProject, Kind: domain.KindWorker,
		Mode: domain.SessionModeChat, ProvisionState: domain.SessionProvisionReady,
	}

	if err := m.FailInterruptedProvisioning(context.Background()); err != nil {
		t.Fatalf("fail interrupted: %v", err)
	}
	if got := st.sessions["mer-1"].ProvisionState; got != domain.SessionProvisionFailed {
		t.Fatalf("interrupted session = %q, want failed", got)
	}
	if st.sessions["mer-1"].ProvisionError == "" {
		t.Fatal("interrupted session has no explanation")
	}
	if got := st.sessions["mer-2"].ProvisionState; got != domain.SessionProvisionReady {
		t.Fatalf("healthy session = %q, want untouched", got)
	}
}

// A session whose asynchronous start was interrupted has no workspace path —
// the same shape as the phantom seed rows reconcile now cleans up. It must not
// be cleaned up: the user can see it, its failure explains itself, and its
// queued messages are still in it. An empty brief makes this sharpest, because
// that row also still matches the seed-state predicate that rollback deletes on.
func TestReconcileLive_KeepsAnInterruptedAsyncSpawn(t *testing.T) {
	m, st, _ := newChatManager(&recordingLauncher{})
	st.sessions["mer-1"] = domain.SessionRecord{
		ID: "mer-1", ProjectID: chatTestProject, Kind: domain.KindWorker,
		Mode: domain.SessionModeChat, ProvisionState: domain.SessionProvisionFailed,
		ProvisionError: "AO restarted before this session finished starting",
	}

	if err := m.reconcileLive(context.Background(), st.sessions["mer-1"]); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	stored, ok := st.sessions["mer-1"]
	if !ok {
		t.Fatal("reconcile deleted a failed asynchronous spawn the user can still see")
	}
	if stored.ProvisionState != domain.SessionProvisionFailed {
		t.Fatalf("provision state = %q, want the failure preserved", stored.ProvisionState)
	}
}

// A session is on screen and typeable before its worktree exists, so a file
// attached to a message typed in that window has nowhere to be written. It goes
// to canonical storage and must be in the worktree before the agent can read
// the turn that names it.
func TestStageAttachments_DuringProvisioningLandsInTheWorktree(t *testing.T) {
	dataDir := t.TempDir()
	workspaceDir := t.TempDir()
	st := newFakeStore()
	st.projects[string(chatTestProject)] = domain.ProjectRecord{
		ID: string(chatTestProject), Config: testRoleAgents(),
	}
	launcher := &recordingLauncher{}
	m := New(Deps{
		Runtime:   &fakeRuntime{},
		Agents:    fakeAgents{},
		Workspace: &fakeWorkspace{path: workspaceDir},
		Store:     st,
		Messenger: &fakeMessenger{},
		Chat:      launcher,
		Lifecycle: &fakeLCM{store: st},
		DataDir:   dataDir,
		LookPath:  func(string) (string, error) { return "/bin/true", nil },
	})
	m.browserCapabilities = browsersvc.NewAuthority()
	deferred := deferredBackground(m)

	rec, _, _, err := m.Spawn(context.Background(), asyncChatSpawnConfig("look at this"))
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}

	// The worktree does not exist yet — this is the window the old code refused.
	refs, err := m.StageAttachments(context.Background(), rec.ID, []ports.SpawnAttachment{
		{Ext: ".png", Data: []byte("not really a png")},
	})
	if err != nil {
		t.Fatalf("stage while provisioning: %v", err)
	}
	if len(refs) != 1 || !strings.HasPrefix(refs[0], attachmentsDir+"/") {
		t.Fatalf("refs = %v, want one worktree-relative path", refs)
	}

	(*deferred)[0]()

	landed := filepath.Join(workspaceDir, filepath.FromSlash(refs[0]))
	body, err := os.ReadFile(landed)
	if err != nil {
		t.Fatalf("attachment never reached the worktree at %s: %v", refs[0], err)
	}
	if string(body) != "not really a png" {
		t.Fatalf("attachment content = %q", body)
	}
}

func TestStageAttachments_AtWorkspacePublicationLandsInTheWorktree(t *testing.T) {
	dataDir := t.TempDir()
	workspaceDir := t.TempDir()
	st := newFakeStore()
	st.projects[string(chatTestProject)] = domain.ProjectRecord{
		ID: string(chatTestProject), Config: testRoleAgents(),
	}
	blockingStore := &blockingWorkspacePublishStore{
		fakeStore: st,
		entered:   make(chan context.Context, 1),
		release:   make(chan struct{}),
	}
	m := New(Deps{
		Runtime: &fakeRuntime{}, Agents: fakeAgents{},
		Workspace: &fakeWorkspace{path: workspaceDir}, Store: blockingStore,
		Messenger: &fakeMessenger{}, Chat: &recordingLauncher{},
		Lifecycle: &fakeLCM{store: st}, DataDir: dataDir,
		LookPath: func(string) (string, error) { return "/bin/true", nil },
	})
	m.browserCapabilities = browsersvc.NewAuthority()
	deferred := deferredBackground(m)
	rec, _, _, err := m.Spawn(context.Background(), asyncChatSpawnConfig("look at this"))
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { (*deferred)[0](); close(done) }()
	<-blockingStore.entered
	refs, err := m.StageAttachments(context.Background(), rec.ID, []ports.SpawnAttachment{{Ext: ".png", Data: []byte("image")}})
	if err != nil {
		t.Fatal(err)
	}
	close(blockingStore.release)
	<-done
	if _, err := os.Stat(filepath.Join(workspaceDir, filepath.FromSlash(refs[0]))); err != nil {
		t.Fatalf("attachment staged at publication is missing from worktree: %v", err)
	}
}
