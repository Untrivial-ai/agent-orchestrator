package sessionmanager

import (
	"context"
	"errors"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// newChatManager mirrors newManager() with a chat launcher injected, so both
// branches can be exercised against the same fakes.
func newChatManager(chat ChatLauncher) (*Manager, *fakeStore, *fakeRuntime) {
	st := newFakeStore()
	st.projects["mer"] = domain.ProjectRecord{ID: "mer", Config: testRoleAgents()}
	rt := &fakeRuntime{}
	lookPath := func(string) (string, error) { return "/bin/true", nil }
	m := New(Deps{
		Runtime:   rt,
		Agents:    fakeAgents{},
		Workspace: &fakeWorkspace{},
		Store:     st,
		Messenger: &fakeMessenger{},
		Chat:      chat,
		Lifecycle: &fakeLCM{store: st},
		DataDir:   "/ao-test-data",
		LookPath:  lookPath,
	})
	return m, st, rt
}

const chatTestProject = domain.ProjectID("mer")

// The load-bearing property of the split: exactly one controller starts. A chat
// spawn must not touch the terminal runtime, and a TUI spawn must not touch the
// chat launcher. Anything else means two writers on one conversation.

type recordingLauncher struct {
	preflightErr error
	startErr     error
	turnErr      error
	live         bool
	afterReady   func()

	preflighted []domain.AgentHarness
	started     []ChatStart
	turns       []string
	// relayed is what arrived through Manager.Send rather than as an initial
	// prompt, kept separate so a test can tell the two apart.
	relayed []string
	stopped []domain.SessionID
}

func (l *recordingLauncher) SupportsChat(_ domain.AgentHarness) bool { return true }

func (l *recordingLauncher) PreflightChat(_ context.Context, harness domain.AgentHarness) error {
	l.preflighted = append(l.preflighted, harness)
	return l.preflightErr
}

func (l *recordingLauncher) StartChat(_ context.Context, cfg ChatStart) (ChatStarted, error) {
	l.started = append(l.started, cfg)
	if l.startErr != nil {
		return ChatStarted{}, l.startErr
	}
	started := ChatStarted{
		ProviderConversationID: "thread-1",
		ControllerGeneration:   "gen-1",
	}
	if cfg.ControllerReady != nil {
		if err := cfg.ControllerReady(started); err != nil {
			return ChatStarted{}, err
		}
	}
	if l.afterReady != nil {
		l.afterReady()
	}
	return started, nil
}

func (l *recordingLauncher) StartChatTurn(_ context.Context, _ domain.SessionID, text string) (string, error) {
	l.turns = append(l.turns, text)
	return "turn-1", l.turnErr
}

func (l *recordingLauncher) RelayChatTurn(_ context.Context, _ domain.SessionID, text string) (string, error) {
	l.relayed = append(l.relayed, text)
	return "turn-relay", l.turnErr
}

func (l *recordingLauncher) RelayChatTurnWithID(_ context.Context, _ domain.SessionID, text, _ string) (string, error) {
	l.relayed = append(l.relayed, text)
	return "turn-relay", l.turnErr
}

func (l *recordingLauncher) StopChat(_ context.Context, id domain.SessionID) error { //nolint:unparam

	l.stopped = append(l.stopped, id)
	return nil
}

func (l *recordingLauncher) HasLiveChatController(domain.SessionID) bool {
	return l.live
}

func TestReconcileLive_ChatRelaunchesInExistingWorktree(t *testing.T) {
	launcher := &recordingLauncher{}
	m, st, rt := newChatManager(launcher)
	ws := m.workspace.(*fakeWorkspace)
	lcm := m.lcm.(*fakeLCM)
	rec := domain.SessionRecord{
		ID: "mer-1", ProjectID: chatTestProject, Kind: domain.KindWorker,
		Harness: domain.HarnessCodex, Mode: domain.SessionModeChat,
		Metadata: domain.SessionMetadata{
			Branch: "ao/mer-1/root", WorkspacePath: "/ws/mer-1",
			ProviderConversationID: "thread-existing",
		},
	}
	st.sessions[rec.ID] = rec

	if err := m.reconcileLive(context.Background(), rec); err != nil {
		t.Fatalf("reconcileLive: %v", err)
	}
	if len(launcher.started) != 1 || launcher.started[0].ProviderConversationID != "thread-existing" {
		t.Fatalf("chat starts = %+v, want one native resume", launcher.started)
	}
	if rt.created != 0 {
		t.Fatalf("terminal runtime Create calls = %d, want 0 for Chat", rt.created)
	}
	if ws.stashCalls != 0 {
		t.Fatalf("StashUncommitted calls = %d, want 0", ws.stashCalls)
	}
	if lcm.terminated[rec.ID] != 0 || st.sessions[rec.ID].IsTerminated {
		t.Fatalf("chat session was terminated during in-place recovery: calls=%d row=%+v", lcm.terminated[rec.ID], st.sessions[rec.ID])
	}
	if len(ws.restoreConfigs) != 1 || ws.restoreConfigs[0].Path != rec.Metadata.WorkspacePath {
		t.Fatalf("Restore configs = %+v, want existing Chat worktree", ws.restoreConfigs)
	}
}

func seedChatResumeSession(store *fakeStore, state domain.ActivityState) {
	store.sessions["mer-1"] = domain.SessionRecord{
		ID:        "mer-1",
		ProjectID: chatTestProject,
		Kind:      domain.KindWorker,
		Harness:   domain.HarnessCodex,
		Mode:      domain.SessionModeChat,
		Activity:  domain.Activity{State: state},
		Metadata: domain.SessionMetadata{
			WorkspacePath:          "/ws/mer-1",
			Branch:                 "ao/mer-1",
			ProviderConversationID: "thread-existing",
		},
	}
}

func TestResumeExitedChatSessionDoesNotRequireTerminalRuntimeHandle(t *testing.T) {
	launcher := &recordingLauncher{}
	mgr, store, runtime := newChatManager(launcher)
	seedChatResumeSession(store, domain.ActivityExited)

	result, err := mgr.ResumeAgentWithMode(context.Background(), "mer-1")
	if err != nil {
		t.Fatalf("ResumeAgentWithMode: %v", err)
	}
	if len(launcher.started) != 1 {
		t.Fatalf("started %d chat controllers, want 1", len(launcher.started))
	}
	if got := launcher.started[0].ProviderConversationID; got != "thread-existing" {
		t.Fatalf("provider conversation id = %q, want thread-existing", got)
	}
	if runtime.created != 0 || runtime.destroyed != 0 {
		t.Fatalf("chat resume touched terminal runtime: created=%d destroyed=%d", runtime.created, runtime.destroyed)
	}
	if result.Session.Activity.State != domain.ActivityIdle {
		t.Fatalf("resumed activity = %q, want idle", result.Session.Activity.State)
	}
}

func TestResumeChatKeepsExitReportedBeforeStartReturns(t *testing.T) {
	launcher := &recordingLauncher{}
	mgr, store, _ := newChatManager(launcher)
	seedChatResumeSession(store, domain.ActivityExited)
	launcher.afterReady = func() {
		rec := store.sessions["mer-1"]
		rec.Activity = domain.Activity{State: domain.ActivityExited}
		store.sessions["mer-1"] = rec
	}

	result, err := mgr.ResumeAgentWithMode(context.Background(), "mer-1")
	if err != nil {
		t.Fatalf("ResumeAgentWithMode: %v", err)
	}
	if result.Session.Activity.State != domain.ActivityExited {
		t.Fatalf("activity after immediate controller exit = %q, want exited", result.Session.Activity.State)
	}
}

func TestResumeStaleChatSessionWhenNoControllerIsLive(t *testing.T) {
	launcher := &recordingLauncher{}
	mgr, store, _ := newChatManager(launcher)
	seedChatResumeSession(store, domain.ActivityIdle)

	if _, err := mgr.ResumeAgentWithMode(context.Background(), "mer-1"); err != nil {
		t.Fatalf("ResumeAgentWithMode: %v", err)
	}
	if len(launcher.started) != 1 {
		t.Fatalf("started %d chat controllers, want 1", len(launcher.started))
	}
}

func TestResumeChatSessionRejectsLiveController(t *testing.T) {
	for _, state := range []domain.ActivityState{domain.ActivityIdle, domain.ActivityExited} {
		t.Run(string(state), func(t *testing.T) {
			launcher := &recordingLauncher{live: true}
			mgr, store, _ := newChatManager(launcher)
			seedChatResumeSession(store, state)

			if _, err := mgr.ResumeAgentWithMode(context.Background(), "mer-1"); !errors.Is(err, ErrAgentNotExited) {
				t.Fatalf("ResumeAgentWithMode error = %v, want ErrAgentNotExited", err)
			}
			if len(launcher.started) != 0 {
				t.Fatalf("duplicate resume started %d controllers", len(launcher.started))
			}
		})
	}
}

func TestResumeBranchlessScratchChatSession(t *testing.T) {
	launcher := &recordingLauncher{}
	mgr, store, _ := newChatManager(launcher)
	store.projects["scratch"] = domain.ProjectRecord{
		ID: "scratch", Kind: domain.ProjectKindScratch, Config: testRoleAgents(),
	}
	store.sessions["scratch-1"] = domain.SessionRecord{
		ID: "scratch-1", ProjectID: "scratch", Kind: domain.KindWorker,
		Harness: domain.HarnessCodex, Mode: domain.SessionModeChat,
		Activity: domain.Activity{State: domain.ActivityExited},
		Metadata: domain.SessionMetadata{
			WorkspacePath:          "/ws/scratch-1",
			ProviderConversationID: "thread-existing",
		},
	}

	if _, err := mgr.ResumeAgentWithMode(context.Background(), "scratch-1"); err != nil {
		t.Fatalf("ResumeAgentWithMode: %v", err)
	}
	if len(launcher.started) != 1 {
		t.Fatalf("started %d chat controllers, want 1", len(launcher.started))
	}
}

func TestResumeChatSessionRequiresProviderConversation(t *testing.T) {
	launcher := &recordingLauncher{}
	mgr, store, _ := newChatManager(launcher)
	seedChatResumeSession(store, domain.ActivityExited)
	rec := store.sessions["mer-1"]
	rec.Metadata.ProviderConversationID = ""
	store.sessions["mer-1"] = rec

	if _, err := mgr.ResumeAgentWithMode(context.Background(), "mer-1"); !errors.Is(err, ErrIncompleteHandle) {
		t.Fatalf("ResumeAgentWithMode error = %v, want ErrIncompleteHandle", err)
	}
	if len(launcher.started) != 0 {
		t.Fatalf("missing provider handle started %d controllers", len(launcher.started))
	}
}

func TestRestoreChatSessionRequiresProviderConversation(t *testing.T) {
	launcher := &recordingLauncher{}
	mgr, store, _ := newChatManager(launcher)
	seedChatResumeSession(store, domain.ActivityExited)
	rec := store.sessions["mer-1"]
	rec.IsTerminated = true
	rec.Metadata.ProviderConversationID = ""
	store.sessions["mer-1"] = rec

	if _, err := mgr.RestoreWithMode(context.Background(), "mer-1"); !errors.Is(err, ErrIncompleteHandle) {
		t.Fatalf("RestoreWithMode error = %v, want ErrIncompleteHandle", err)
	}
	if len(launcher.started) != 0 {
		t.Fatalf("missing provider handle started %d controllers", len(launcher.started))
	}
}

// An unsupported chat request must be refused before anything durable exists: no
// session row, no worktree, nothing to clean up.
func TestChatSpawnRejectedBeforeDurableStateWhenUnsupported(t *testing.T) {
	mgr, store, _ := newChatManager(&recordingLauncher{preflightErr: ports.ErrChatUnsupported})
	launcher := mgr.chat.(*recordingLauncher)

	_, _, _, err := mgr.Spawn(context.Background(), ports.SpawnConfig{
		ProjectID:     chatTestProject,
		Kind:          domain.KindWorker,
		Harness:       domain.HarnessCodex,
		Prompt:        "do the thing",
		RequestedMode: domain.SessionModeChat,
	})
	if !errors.Is(err, ports.ErrChatUnsupported) {
		t.Fatalf("err = %v, want ErrChatUnsupported", err)
	}

	sessions, listErr := store.ListAllSessions(context.Background())
	if listErr != nil {
		t.Fatalf("list sessions: %v", listErr)
	}
	if len(sessions) != 0 {
		t.Fatalf("a refused chat spawn left %d session rows behind", len(sessions))
	}
	if len(launcher.started) != 0 {
		t.Error("a refused preflight still started a controller")
	}
}

// Chat mode with no launcher wired must fail, never silently become a TUI session
// in a terminal the user did not ask for.
func TestChatSpawnWithoutLauncherIsRefusedNotDowngraded(t *testing.T) {
	mgr, _, runtime := newChatManager(nil)

	_, _, _, err := mgr.Spawn(context.Background(), ports.SpawnConfig{
		ProjectID:     chatTestProject,
		Kind:          domain.KindWorker,
		Harness:       domain.HarnessCodex,
		RequestedMode: domain.SessionModeChat,
	})
	if !errors.Is(err, ports.ErrChatUnsupported) {
		t.Fatalf("err = %v, want ErrChatUnsupported", err)
	}
	if runtime.created != 0 {
		t.Fatalf("a refused chat spawn created %d runtimes — it downgraded to TUI", runtime.created)
	}
}

// A TUI spawn must never reach the chat launcher, even when one is wired.
func TestTUISpawnNeverTouchesTheChatLauncher(t *testing.T) {
	launcher := &recordingLauncher{}
	mgr, _, runtime := newChatManager(launcher)

	rec, _, _, err := mgr.Spawn(context.Background(), ports.SpawnConfig{
		ProjectID: chatTestProject,
		Kind:      domain.KindWorker,
		Harness:   domain.HarnessClaudeCode,
		Prompt:    "hello",
		// No requested mode: resolution must land on TUI.
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if rec.Mode != domain.SessionModeTUI {
		t.Fatalf("mode = %q, want tui when none was requested", rec.Mode)
	}
	if len(launcher.preflighted) != 0 || len(launcher.started) != 0 {
		t.Fatalf("a TUI spawn reached the chat launcher: preflight=%v started=%d",
			launcher.preflighted, len(launcher.started))
	}
	if runtime.created == 0 {
		t.Error("a TUI spawn created no runtime")
	}
}

// A chat spawn must persist its mode and provider handle, start no runtime, and
// deliver the initial prompt as a turn.
func TestChatSpawnStartsControllerAndNoRuntime(t *testing.T) {
	launcher := &recordingLauncher{}
	mgr, _, runtime := newChatManager(launcher)

	rec, _, _, err := mgr.Spawn(context.Background(), ports.SpawnConfig{
		ProjectID:     chatTestProject,
		Kind:          domain.KindOrchestrator,
		Harness:       domain.HarnessCodex,
		Prompt:        "coordinate the work",
		RequestedMode: domain.SessionModeChat,
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	if rec.Mode != domain.SessionModeChat {
		t.Fatalf("mode = %q, want chat", rec.Mode)
	}
	if runtime.created != 0 {
		t.Fatalf("a chat spawn created %d terminal runtimes, want 0", runtime.created)
	}
	if len(launcher.started) != 1 {
		t.Fatalf("started %d controllers, want 1", len(launcher.started))
	}

	start := launcher.started[0]
	if start.WorkspacePath == "" {
		t.Error("controller started with no workspace path")
	}
	if start.DataDir != "/ao-test-data" {
		t.Errorf("controller data dir = %q, want manager-owned data dir", start.DataDir)
	}
	// The controller must receive the session env, which is what carries the
	// HookPATH pin in production and is how the agent's own shell commands find
	// `ao` — the mechanism an orchestrator delegates through.
	//
	// The PATH value itself is not asserted here: HookPATH deliberately declines
	// to pin when the running binary is not named "ao", which is always the case
	// under `go test`. That the pin works end to end was verified against a real
	// app-server with a fake `ao` on an injected PATH.
	if start.Env == nil {
		t.Error("controller started with no environment; the agent could not resolve `ao`")
	}
	if start.Env[EnvSessionID] == "" {
		t.Errorf("controller env missing %s; session-scoped hooks would not identify the session", EnvSessionID)
	}

	// The provider handle must be persisted, or a restart cannot resume.
	if rec.Metadata.ProviderConversationID != "thread-1" {
		t.Errorf("provider conversation id = %q", rec.Metadata.ProviderConversationID)
	}
	if rec.Metadata.ControllerGeneration != "gen-1" {
		t.Errorf("controller generation = %q", rec.Metadata.ControllerGeneration)
	}
	// A chat session has no agent pane; leaving these empty is what stops the
	// reaper probing for a terminal that never existed.
	if rec.Metadata.RuntimeHandleID != "" || rec.Metadata.RuntimeLaunchID != "" {
		t.Errorf("chat session carries runtime handles: handle=%q launch=%q",
			rec.Metadata.RuntimeHandleID, rec.Metadata.RuntimeLaunchID)
	}

	if len(launcher.turns) != 1 || launcher.turns[0] == "" {
		t.Fatalf("initial prompt was not delivered as a turn: %v", launcher.turns)
	}
}

func TestChatSpawnAppliesRequestAgentConfigOverProjectDefaults(t *testing.T) {
	launcher := &recordingLauncher{}
	mgr, store, _ := newChatManager(launcher)
	project := store.projects[string(chatTestProject)]
	project.Config.AgentConfig.Model = "project-model"
	store.projects[string(chatTestProject)] = project

	_, _, _, err := mgr.Spawn(context.Background(), ports.SpawnConfig{
		ProjectID:     chatTestProject,
		Kind:          domain.KindWorker,
		Harness:       domain.HarnessCodex,
		AgentConfig:   ports.AgentConfig{Model: "request-model"},
		RequestedMode: domain.SessionModeChat,
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if len(launcher.started) != 1 {
		t.Fatalf("started %d controllers, want 1", len(launcher.started))
	}
	if got := launcher.started[0].Model; got != "request-model" {
		t.Fatalf("controller model = %q, want request-model", got)
	}
}

// A controller that fails to start must leave nothing running and no live row.
func TestChatSpawnRollsBackWhenControllerFailsToStart(t *testing.T) {
	mgr, store, runtime := newChatManager(&recordingLauncher{startErr: errors.New("app-server exited")})

	_, _, _, err := mgr.Spawn(context.Background(), ports.SpawnConfig{
		ProjectID:     chatTestProject,
		Kind:          domain.KindWorker,
		Harness:       domain.HarnessCodex,
		RequestedMode: domain.SessionModeChat,
	})
	if err == nil {
		t.Fatal("expected a failed controller start to fail the spawn")
	}
	if runtime.created != 0 {
		t.Error("a failed chat spawn created a terminal runtime")
	}

	sessions, listErr := store.ListAllSessions(context.Background())
	if listErr != nil {
		t.Fatalf("list sessions: %v", listErr)
	}
	for _, session := range sessions {
		if !session.IsTerminated {
			t.Errorf("session %s left live after a failed chat spawn", session.ID)
		}
	}
}

// Kill must close the controller, not tear down a runtime the session never had.
// A chat controller owns an app-server child process, so skipping this leaks it.
func TestKillClosesTheChatControllerAndTouchesNoRuntime(t *testing.T) {
	launcher := &recordingLauncher{}
	mgr, _, runtime := newChatManager(launcher)
	ctx := context.Background()

	rec, _, _, err := mgr.Spawn(ctx, ports.SpawnConfig{
		ProjectID:     chatTestProject,
		Kind:          domain.KindWorker,
		Harness:       domain.HarnessCodex,
		RequestedMode: domain.SessionModeChat,
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	if _, err := mgr.Kill(ctx, rec.ID); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	if len(launcher.stopped) != 1 || launcher.stopped[0] != rec.ID {
		t.Fatalf("controller not closed on kill: %v", launcher.stopped)
	}
	if runtime.destroyed != 0 {
		t.Errorf("kill destroyed %d runtimes for a session that never had one", runtime.destroyed)
	}
}

// Restore must not cross modes. A chat session resumes its provider conversation
// with the handle it stored; giving it a terminal would hand it a controller it
// was not created with.
func TestRestoreResumesChatRatherThanRelaunchingATerminal(t *testing.T) {
	launcher := &recordingLauncher{}
	mgr, store, runtime := newChatManager(launcher)
	ctx := context.Background()

	rec, _, _, err := mgr.Spawn(ctx, ports.SpawnConfig{
		ProjectID:     chatTestProject,
		Kind:          domain.KindWorker,
		Harness:       domain.HarnessCodex,
		RequestedMode: domain.SessionModeChat,
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	startsAfterSpawn := len(launcher.started)
	runtimeAfterSpawn := runtime.created

	// Simulate a daemon restart: the controller dies and the session is marked
	// terminated with a restore marker, which is the state RestoreAll acts on.
	if _, err := mgr.Kill(ctx, rec.ID); err != nil {
		t.Fatalf("Kill: %v", err)
	}

	stored, _, err := store.GetSession(ctx, rec.ID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if stored.Metadata.ProviderConversationID == "" {
		t.Fatal("spawn stored no provider conversation id; a restart could not resume")
	}

	result, err := mgr.RestoreWithMode(ctx, rec.ID)
	if err != nil {
		t.Fatalf("RestoreWithMode: %v", err)
	}

	if runtime.created != runtimeAfterSpawn {
		t.Errorf("restore created a terminal runtime for a chat session")
	}
	if len(launcher.started) != startsAfterSpawn+1 {
		t.Fatalf("restore did not start a controller: %d starts", len(launcher.started))
	}
	resumed := launcher.started[len(launcher.started)-1]
	if resumed.ProviderConversationID != stored.Metadata.ProviderConversationID {
		t.Errorf("restore passed provider conversation %q, want the stored %q — without it this is a new conversation, not a resume",
			resumed.ProviderConversationID, stored.Metadata.ProviderConversationID)
	}
	// The provider still holds the history, so continuity is native rather than a
	// replayed prompt.
	if result.Mode != RestoreModeNative {
		t.Errorf("restore mode = %q, want native", result.Mode)
	}
}

// `ao send` and orchestrator-to-worker relay both go through Manager.Send. A chat
// session has no pane to type into, so without a mode branch the send reached the
// runtime guard and was refused as "missing runtime handles" — which is true of
// the handles and wrong about the session, and left chat workers unreachable by
// AO's own automation.
func TestSendRoutesIntoTheChatConversation(t *testing.T) {
	launcher := &recordingLauncher{}
	mgr, _, runtime := newChatManager(launcher)
	ctx := context.Background()

	rec, _, _, err := mgr.Spawn(ctx, ports.SpawnConfig{
		ProjectID:     chatTestProject,
		Kind:          domain.KindWorker,
		Harness:       domain.HarnessCodex,
		Prompt:        "initial brief",
		RequestedMode: domain.SessionModeChat,
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	if err := mgr.Send(ctx, rec.ID, "relayed from an orchestrator", nil); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if len(launcher.relayed) != 1 || launcher.relayed[0] != "relayed from an orchestrator" {
		t.Fatalf("relayed = %v, want the message routed to the conversation", launcher.relayed)
	}
	// The initial prompt is a different thing and must not be conflated with it.
	if len(launcher.turns) != 1 || launcher.turns[0] != "initial brief" {
		t.Errorf("initial prompt turns = %v", launcher.turns)
	}
	// The terminal path is not merely unused, it is unreachable here: no runtime
	// was ever created for this session, so a send that fell through would have
	// been refused by the runtime guard instead of returning nil above.
	if runtime.created != 0 {
		t.Errorf("chat spawn created %d runtimes", runtime.created)
	}
}

// A terminated chat session cannot receive a message, matching the terminal path.
// The controller is gone; accepting the send would record a message nothing will
// ever deliver.
func TestSendRefusedForTerminatedChatSession(t *testing.T) {
	launcher := &recordingLauncher{}
	mgr, _, _ := newChatManager(launcher)
	ctx := context.Background()

	rec, _, _, err := mgr.Spawn(ctx, ports.SpawnConfig{
		ProjectID:     chatTestProject,
		Kind:          domain.KindWorker,
		Harness:       domain.HarnessCodex,
		RequestedMode: domain.SessionModeChat,
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if _, err := mgr.Kill(ctx, rec.ID); err != nil {
		t.Fatalf("Kill: %v", err)
	}

	err = mgr.Send(ctx, rec.ID, "too late", nil)
	if !errors.Is(err, ErrTerminated) {
		t.Fatalf("err = %v, want ErrTerminated", err)
	}
	if len(launcher.relayed) != 0 {
		t.Errorf("a terminated session still received %v", launcher.relayed)
	}
}
