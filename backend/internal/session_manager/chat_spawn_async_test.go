package sessionmanager

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

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

	rec, _, _, err := m.Spawn(context.Background(), asyncChatSpawnConfig("do the thing"))
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	(*deferred)[0]()

	// The controller never started, so only the early publish can have written
	// this — exactly the window the desktop was polling through.
	stored := st.sessions[rec.ID]
	if stored.Metadata.WorkspacePath == "" {
		t.Fatal("the worktree exists but the row still reports no workspace")
	}
	if stored.Metadata.Branch == "" {
		t.Fatal("the row reports a workspace with no branch")
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
