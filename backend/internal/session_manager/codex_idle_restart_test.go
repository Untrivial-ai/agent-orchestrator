package sessionmanager

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestIdleCodexRestartCandidate(t *testing.T) {
	t.Parallel()
	base := domain.SessionRecord{
		ID: "session-1", Kind: domain.KindWorker, Harness: domain.HarnessCodex,
		Mode: domain.SessionModeTUI, Activity: domain.Activity{State: domain.ActivityIdle},
	}
	tests := []struct {
		name string
		edit func(*domain.SessionRecord)
		want bool
	}{
		{name: "idle TUI", want: true},
		{name: "idle Chat", edit: func(rec *domain.SessionRecord) { rec.Mode = domain.SessionModeChat }, want: true},
		{name: "active", edit: func(rec *domain.SessionRecord) { rec.Activity.State = domain.ActivityActive }},
		{name: "waiting input", edit: func(rec *domain.SessionRecord) { rec.Activity.State = domain.ActivityWaitingInput }},
		{name: "blocked", edit: func(rec *domain.SessionRecord) { rec.Activity.State = domain.ActivityBlocked }},
		{name: "exited", edit: func(rec *domain.SessionRecord) { rec.Activity.State = domain.ActivityExited }},
		{name: "terminated", edit: func(rec *domain.SessionRecord) { rec.IsTerminated = true }},
		{name: "other harness", edit: func(rec *domain.SessionRecord) { rec.Harness = domain.HarnessClaudeCode }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := base
			if tt.edit != nil {
				tt.edit(&rec)
			}
			if got := idleCodexRestartCandidate(rec); got != tt.want {
				t.Fatalf("idleCodexRestartCandidate() = %v, want %v for %+v", got, tt.want, rec)
			}
		})
	}
}

func TestRestartIdleCodexTUIUsesNativeHistoryAndPreservesTerminalAndWorktree(t *testing.T) {
	baseRuntime := &fakeRuntime{aliveByHandle: map[string]bool{"tmux-mer-1": true}}
	runtime := &fakeRestartRuntime{fakeRuntime: baseRuntime}
	agent := supervisedLaunchAgent{launchArgvAgent{argv: []string{"codex", "resume", "agent-x"}}}
	manager, store, workspace := newExitedResumeManager(t, runtime, agent)
	rec := store.sessions["mer-1"]
	rec.Mode = domain.SessionModeTUI
	rec.Activity = domain.Activity{State: domain.ActivityIdle}
	store.sessions[rec.ID] = rec

	if failed := manager.restartIdleCodexSession(context.Background(), rec.ID); failed {
		t.Fatal("idle TUI restart reported a failure")
	}
	if baseRuntime.destroyed != 0 || runtime.restarted != 1 || runtime.restartHandle.ID != "tmux-mer-1" {
		t.Fatalf("stop/restart = destroyed %d restarted %d handle %q", baseRuntime.destroyed, runtime.restarted, runtime.restartHandle.ID)
	}
	wantArgv := []string{"/opt/ao", "agent-process", "supervise", "--session", "mer-1", "--launch", "launch-new", "--", "codex", "resume", "agent-x"}
	if !reflect.DeepEqual(baseRuntime.lastCfg.Argv, wantArgv) {
		t.Fatalf("resume argv = %#v, want %#v", baseRuntime.lastCfg.Argv, wantArgv)
	}
	if len(workspace.calls) != 0 || workspace.lastCfg.SessionID != "" {
		t.Fatalf("restart recreated worktree: calls=%v config=%+v", workspace.calls, workspace.lastCfg)
	}
	got := store.sessions[rec.ID]
	if got.Activity.State != domain.ActivityIdle || got.Metadata.WorkspacePath != "/ws/mer-1" || got.Metadata.RuntimeHandleID != "tmux-mer-1" || got.Metadata.AgentSessionID != "agent-x" {
		t.Fatalf("restarted TUI did not preserve identity: %+v", got)
	}
}

func TestRestartIdleCodexChatRecreatesControllerFromStoredConversation(t *testing.T) {
	launcher := &recordingLauncher{}
	manager, store, runtime := newChatManager(launcher)
	seedChatResumeSession(store, domain.ActivityIdle)

	if failed := manager.restartIdleCodexSession(context.Background(), "mer-1"); failed {
		t.Fatal("idle Chat restart reported a failure")
	}
	if !slices.Equal(launcher.stopped, []domain.SessionID{"mer-1"}) || len(launcher.started) != 1 {
		t.Fatalf("Chat stop/start = %v/%d, want one each", launcher.stopped, len(launcher.started))
	}
	start := launcher.started[0]
	if start.ProviderConversationID != "thread-existing" || start.WorkspacePath != "/ws/mer-1" || !start.RequireNativeHistory {
		t.Fatalf("Chat restart = provider %q workspace %q requireHistory=%v", start.ProviderConversationID, start.WorkspacePath, start.RequireNativeHistory)
	}
	if runtime.created != 0 || runtime.destroyed != 0 {
		t.Fatalf("Chat restart touched tmux runtime: created=%d destroyed=%d", runtime.created, runtime.destroyed)
	}
}

func TestRestartIdleCodexSessionRechecksIdleAfterTakingInputLock(t *testing.T) {
	launcher := &recordingLauncher{}
	manager, store, _ := newChatManager(launcher)
	seedChatResumeSession(store, domain.ActivityIdle)
	releaseInput, admitted := manager.AcquireSessionInput("mer-1")
	if !admitted {
		t.Fatal("failed to acquire setup input lease")
	}

	done := make(chan bool, 1)
	go func() { done <- manager.restartIdleCodexSession(context.Background(), "mer-1") }()
	deadline := time.Now().Add(time.Second)
	for !manager.SessionMutationInProgress("mer-1") && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !manager.SessionMutationInProgress("mer-1") {
		t.Fatal("restart worker did not reserve the session")
	}
	rec := store.sessions["mer-1"]
	rec.Activity = domain.Activity{State: domain.ActivityActive}
	store.sessions[rec.ID] = rec
	releaseInput()

	select {
	case failed := <-done:
		if failed {
			t.Fatal("a session that became active was treated as a restart failure")
		}
	case <-time.After(time.Second):
		t.Fatal("restart worker did not finish")
	}
	if len(launcher.stopped) != 0 || len(launcher.started) != 0 {
		t.Fatalf("session that became active was mutated: stopped=%v started=%v", launcher.stopped, launcher.started)
	}
}

type blockingIdleRestartLauncher struct {
	*recordingLauncher
	entered chan domain.SessionID
	release chan struct{}
	stopErr error
}

func (l *blockingIdleRestartLauncher) StopChat(ctx context.Context, id domain.SessionID) error {
	select {
	case l.entered <- id:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case <-l.release:
		return l.stopErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestCodexAccountSwitchRestartsIdleSessionsConcurrentlyWithoutBlockingWorkingInput(t *testing.T) {
	base := newFakeStore()
	base.projects["mer"] = domain.ProjectRecord{ID: "mer", Config: testRoleAgents()}
	for _, id := range []domain.SessionID{"idle-1", "idle-2"} {
		base.sessions[id] = domain.SessionRecord{
			ID: id, ProjectID: "mer", Kind: domain.KindWorker, Harness: domain.HarnessCodex,
			Mode: domain.SessionModeChat, Activity: domain.Activity{State: domain.ActivityIdle},
			Metadata: domain.SessionMetadata{WorkspacePath: "/ws/" + string(id), Branch: "ao/" + string(id), ProviderConversationID: "thread-" + string(id)},
		}
	}
	base.sessions["working"] = domain.SessionRecord{
		ID: "working", ProjectID: "mer", Kind: domain.KindWorker, Harness: domain.HarnessCodex,
		Mode: domain.SessionModeChat, Activity: domain.Activity{State: domain.ActivityActive},
		Metadata: domain.SessionMetadata{WorkspacePath: "/ws/working", Branch: "ao/working", ProviderConversationID: "thread-working"},
	}
	journal := &collectingCodexSwitchStore{}
	store := &bootstrapOrderingStore{fakeStore: base, collectingCodexSwitchStore: journal}
	launcher := &blockingIdleRestartLauncher{
		recordingLauncher: &recordingLauncher{live: true},
		entered:           make(chan domain.SessionID, 2), release: make(chan struct{}), stopErr: errors.New("injected stop failure"),
	}
	credentials := &blockingActivationCredentials{
		current:           domain.CodexActiveAccount{AccountID: "source", Revision: 1},
		activationEntered: make(chan struct{}), activationReleased: make(chan struct{}),
	}
	manager := New(Deps{
		Store: store, Runtime: &fakeRuntime{}, Agents: fakeAgents{}, Workspace: &fakeWorkspace{},
		Messenger: &fakeMessenger{}, Chat: launcher, Lifecycle: &fakeLCM{store: base}, DataDir: t.TempDir(),
		LookPath: func(string) (string, error) { return "/bin/true", nil },
	})
	manager.SetAgentReadiness(credentials)

	if _, err := manager.StartCodexAccountSwitch(context.Background(), ports.CodexAccountSwitchConfig{
		TargetAccountID: "target", ExpectedAccountRevision: 1, IdempotencyKey: "parallel-idle-restart", RestartIdleSessions: true,
	}); err != nil {
		t.Fatalf("start switch: %v", err)
	}
	<-credentials.activationEntered
	if base.listAllCalls != 0 {
		t.Fatalf("sessions listed before activation and verification: %d", base.listAllCalls)
	}
	close(credentials.activationReleased)

	entered := make([]domain.SessionID, 0, 2)
	for len(entered) < 2 {
		select {
		case id := <-launcher.entered:
			entered = append(entered, id)
		case <-time.After(time.Second):
			t.Fatalf("only %d restart workers reached StopChat; want concurrent entry", len(entered))
		}
	}
	if base.listAllCalls != 1 {
		t.Fatalf("session discovery calls = %d, want exactly one", base.listAllCalls)
	}
	if !manager.CodexAccountSwitchInProgress() || manager.codexAccountSwitchIsActive() {
		t.Fatalf("restart phase gates: operation=%v credential=%v, want true/false", manager.CodexAccountSwitchInProgress(), manager.codexAccountSwitchIsActive())
	}
	controllerRelease, err := manager.acquireCodexControllerAdmission(context.Background(), domain.HarnessCodex)
	if err != nil {
		t.Fatalf("new controller admission remained credential-fenced during restart: %v", err)
	}
	controllerRelease()
	if err := manager.Send(context.Background(), "working", "continue", nil); err != nil {
		t.Fatalf("working session message was blocked: %v", err)
	}
	if err := manager.Send(context.Background(), "idle-1", "race", nil); !errors.Is(err, ErrSwitchInProgress) {
		t.Fatalf("restarting idle session send error = %v, want ErrSwitchInProgress", err)
	}
	close(launcher.release)
	waitForCodexSwitchWorker(t, manager)

	if journal.switchRecord.Phase != domain.CodexAccountSwitchCompleted || journal.switchRecord.FailureCode != codexIdleRestartIncomplete {
		t.Fatalf("terminal switch = phase %q failure %q", journal.switchRecord.Phase, journal.switchRecord.FailureCode)
	}
	if got := credentials.CurrentCodexActiveAccount().AccountID; got != "target" {
		t.Fatalf("partial restart failure rolled back active account to %q", got)
	}
	if !slices.Equal(launcher.relayed, []string{"continue"}) {
		t.Fatalf("working Chat delivery = %v", launcher.relayed)
	}
}
