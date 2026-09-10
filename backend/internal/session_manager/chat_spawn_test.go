package sessionmanager

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	browsersvc "github.com/aoagents/agent-orchestrator/backend/internal/service/browser"
)

type recordingBrowserAuthority struct {
	authority *browsersvc.Authority
	tokens    []string
}

func (r *recordingBrowserAuthority) Issue(id domain.SessionID) (string, string, error) {
	token, verifier, err := r.authority.Issue(id)
	if err == nil {
		r.tokens = append(r.tokens, token)
	}
	return token, verifier, err
}

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

type fixedSessionModeDefaults domain.SessionMode

func (d fixedSessionModeDefaults) DefaultSessionMode(context.Context) domain.SessionMode {
	return domain.SessionMode(d)
}

// The load-bearing property of the split: exactly one controller starts. A chat
// spawn must not touch the terminal runtime, and a TUI spawn must not touch the
// chat launcher. Anything else means two writers on one conversation.

type recordingLauncher struct {
	preflightErr     error
	startErr         error
	turnErr          error
	live             bool
	beforeStart      func(ChatStart)
	afterReady       func()
	providerBoundary *domain.ConversationBranch

	preflighted          []domain.AgentHarness
	preflightPermissions []ports.PermissionMode
	started              []ChatStart
	turns                []string
	// relayed is what arrived through Manager.Send rather than as an initial
	// prompt, kept separate so a test can tell the two apart.
	relayed       []string
	relayIDs      []string
	stopped       []domain.SessionID
	armed         []domain.SessionID
	armPolicy     []domain.SessionInterfaceTransitionPolicy
	prepared      []domain.SessionID
	preparePolicy []domain.SessionInterfaceTransitionPolicy
	aborted       []domain.SessionID
}

type historicalChatRestoreStore struct {
	*transitionStore
	conversation    domain.ConversationRecord
	activeBranch    domain.ConversationBranch
	conversationErr error
}

func (s *historicalChatRestoreStore) ConversationForSession(
	_ context.Context,
	id domain.SessionID,
) (domain.ConversationRecord, error) {
	if s.conversationErr != nil {
		return domain.ConversationRecord{}, s.conversationErr
	}
	if s.conversation.SessionID != id {
		return domain.ConversationRecord{}, domain.ErrNoConversation
	}
	return s.conversation, nil
}

func (s *historicalChatRestoreStore) ConversationBranch(
	_ context.Context,
	conversationID, branchID string,
) (domain.ConversationBranch, error) {
	if s.activeBranch.ConversationID != conversationID || s.activeBranch.ID != branchID {
		return domain.ConversationBranch{}, domain.ErrNoConversationBranch
	}
	return s.activeBranch, nil
}

func (l *recordingLauncher) SupportsChat(_ domain.AgentHarness) bool { return true }

func (l *recordingLauncher) PreflightChat(
	_ context.Context,
	harness domain.AgentHarness,
	permissions ports.PermissionMode,
) error {
	l.preflighted = append(l.preflighted, harness)
	l.preflightPermissions = append(l.preflightPermissions, permissions)
	return l.preflightErr
}

func prepareTestChatStart(ctx context.Context, cfg ChatStart) (ChatStart, error) {
	if cfg.PrepareControllerEnv == nil {
		return cfg, nil
	}
	env, err := cfg.PrepareControllerEnv(ctx, cfg.ExpectedControllerOwner)
	if err != nil {
		return cfg, err
	}
	cfg.Env = env
	return cfg, nil
}

func (l *recordingLauncher) StartChat(ctx context.Context, cfg ChatStart) (ChatStarted, error) {
	var err error
	cfg, err = prepareTestChatStart(ctx, cfg)
	if err != nil {
		return ChatStarted{}, err
	}
	l.started = append(l.started, cfg)
	if l.beforeStart != nil {
		l.beforeStart(cfg)
	}
	if l.startErr != nil {
		return ChatStarted{}, l.startErr
	}
	started := ChatStarted{
		ProviderConversationID: "thread-1",
		ControllerGeneration:   "gen-1",
		ProviderBoundary:       l.providerBoundary,
	}
	if cfg.ControllerGeneration != "" {
		started.ControllerGeneration = cfg.ControllerGeneration
	}
	if cfg.ControllerReady != nil {
		if _, err := cfg.ControllerReady(started); err != nil {
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
	l.relayIDs = append(l.relayIDs, "")
	return "turn-relay", l.turnErr
}

func (l *recordingLauncher) RelayChatTurnWithID(_ context.Context, _ domain.SessionID, text, clientMessageID string) (string, error) {
	l.relayed = append(l.relayed, text)
	l.relayIDs = append(l.relayIDs, clientMessageID)
	return "turn-relay", l.turnErr
}

func (l *recordingLauncher) StopChat(_ context.Context, id domain.SessionID) error { //nolint:unparam

	l.stopped = append(l.stopped, id)
	return nil
}

func (l *recordingLauncher) HasLiveChatController(domain.SessionID) bool {
	return l.live
}

func (l *recordingLauncher) ArmChatHandoff(_ context.Context, id domain.SessionID, policy domain.SessionInterfaceTransitionPolicy) error {
	l.armed = append(l.armed, id)
	l.armPolicy = append(l.armPolicy, policy)
	return nil
}

func (l *recordingLauncher) PrepareChatHandoff(_ context.Context, id domain.SessionID, policy domain.SessionInterfaceTransitionPolicy) error {
	l.prepared = append(l.prepared, id)
	l.preparePolicy = append(l.preparePolicy, policy)
	return nil
}

func (l *recordingLauncher) AbortChatHandoff(id domain.SessionID) {
	l.aborted = append(l.aborted, id)
}

type generationClaimFailureLauncher struct {
	*recordingLauncher
	store      *fakeStore
	generation string
	err        error
}

func (l *generationClaimFailureLauncher) StartChat(ctx context.Context, cfg ChatStart) (ChatStarted, error) {
	var prepareErr error
	cfg, prepareErr = prepareTestChatStart(ctx, cfg)
	if prepareErr != nil {
		return ChatStarted{}, prepareErr
	}
	l.started = append(l.started, cfg)
	rec := l.store.sessions[cfg.SessionID]
	rec.Metadata.ControllerGeneration = l.generation
	rec.UpdatedAt = rec.UpdatedAt.Add(time.Second)
	l.store.sessions[cfg.SessionID] = rec
	return ChatStarted{}, l.err
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

func TestReconcileLive_ChatCompatibilityFailureLeavesNativeResumeRecoverable(t *testing.T) {
	launcher := &recordingLauncher{startErr: fmt.Errorf("read Codex version: exit status 127: %w", ports.ErrChatDriverIncompatible)}
	m, st, rt := newChatManager(launcher)
	ws := m.workspace.(*fakeWorkspace)
	lcm := m.lcm.(*fakeLCM)
	rec := domain.SessionRecord{
		ID: "mer-1", ProjectID: chatTestProject, Kind: domain.KindWorker,
		Harness: domain.HarnessCodex, Mode: domain.SessionModeChat,
		Activity: domain.Activity{State: domain.ActivityActive},
		Metadata: domain.SessionMetadata{
			Branch: "ao/mer-1/root", WorkspacePath: "/ws/mer-1",
			ProviderConversationID: "01a03c61-23a9-7111-95e9-2bacb04eb064",
		},
	}
	st.sessions[rec.ID] = rec

	err := m.reconcileLive(context.Background(), rec)
	if !errors.Is(err, ports.ErrChatDriverIncompatible) {
		t.Fatalf("reconcileLive error = %v, want ErrChatDriverIncompatible", err)
	}
	got := st.sessions[rec.ID]
	if got.IsTerminated || got.Activity.State != domain.ActivityExited {
		t.Fatalf("failed Chat resume = %+v, want live/exited", got)
	}
	if got.Metadata.ProviderConversationID != rec.Metadata.ProviderConversationID {
		t.Fatalf("provider conversation id = %q, want preserved %q", got.Metadata.ProviderConversationID, rec.Metadata.ProviderConversationID)
	}
	if rt.created != 0 || rt.destroyed != 0 || ws.stashCalls != 0 || lcm.terminated[rec.ID] != 0 {
		t.Fatalf("failed Chat resume tore down state: runtime=(%d,%d) stash=%d terminated=%d",
			rt.created, rt.destroyed, ws.stashCalls, lcm.terminated[rec.ID])
	}

	launcher.startErr = nil
	if _, err := m.ResumeAgentWithMode(context.Background(), rec.ID); err != nil {
		t.Fatalf("ResumeAgentWithMode after dependency recovery: %v", err)
	}
}

func TestReconcileLive_ChatFailureAfterGenerationClaimLeavesSessionExited(t *testing.T) {
	base := &recordingLauncher{}
	m, st, rt := newChatManager(base)
	launcher := &generationClaimFailureLauncher{
		recordingLauncher: base,
		store:             st,
		generation:        "claimed-generation",
		err:               errors.New("read native history: provider unavailable"),
	}
	m.chat = launcher
	ws := m.workspace.(*fakeWorkspace)
	lcm := m.lcm.(*fakeLCM)
	now := time.Date(2026, time.August, 27, 16, 54, 0, 0, time.UTC)
	rec := domain.SessionRecord{
		ID: "mer-1", ProjectID: chatTestProject, Kind: domain.KindWorker,
		Harness: domain.HarnessCodex, Mode: domain.SessionModeChat,
		Activity: domain.Activity{State: domain.ActivityActive}, UpdatedAt: now,
		Metadata: domain.SessionMetadata{
			Branch: "ao/mer-1/root", WorkspacePath: "/ws/mer-1",
			ProviderConversationID: "thread-existing", ControllerGeneration: "old-generation",
		},
	}
	st.sessions[rec.ID] = rec

	err := m.reconcileLive(context.Background(), rec)
	if err == nil || !strings.Contains(err.Error(), "read native history") {
		t.Fatalf("reconcileLive error = %v, want post-claim history failure", err)
	}
	got := st.sessions[rec.ID]
	if got.IsTerminated || got.Activity.State != domain.ActivityExited {
		t.Fatalf("post-claim Chat failure = %+v, want live/exited", got)
	}
	if got.Metadata.ControllerGeneration != "claimed-generation" {
		t.Fatalf("controller generation = %q, want claimed epoch retained as stale-signal fence", got.Metadata.ControllerGeneration)
	}
	if launcher.HasLiveChatController(rec.ID) {
		t.Fatal("failed post-claim launch unexpectedly published a live controller")
	}
	if got.Metadata.ProviderConversationID != rec.Metadata.ProviderConversationID || got.Metadata.WorkspacePath != rec.Metadata.WorkspacePath {
		t.Fatalf("post-claim failure lost native identity/worktree: %+v", got.Metadata)
	}
	if rt.created != 0 || rt.destroyed != 0 || ws.stashCalls != 0 || lcm.terminated[rec.ID] != 0 {
		t.Fatalf("post-claim Chat failure tore down state: runtime=(%d,%d) stash=%d terminated=%d",
			rt.created, rt.destroyed, ws.stashCalls, lcm.terminated[rec.ID])
	}
}

func TestRestoreTerminatedChatOrchestratorAfterCompatibilityRecoveryKeepsIdentity(t *testing.T) {
	launcher := &recordingLauncher{startErr: fmt.Errorf("read Codex version: exit status 127: %w", ports.ErrChatDriverIncompatible)}
	m, st, rt := newChatManager(launcher)
	rec := domain.SessionRecord{
		ID: "mer-176", ProjectID: chatTestProject, Kind: domain.KindOrchestrator,
		Harness: domain.HarnessCodex, Mode: domain.SessionModeChat,
		IsTerminated: true, Activity: domain.Activity{State: domain.ActivityExited},
		Metadata: domain.SessionMetadata{
			Branch: "main", WorkspacePath: "/ws/mer-176",
			ProviderConversationID: "01a03c61-23a9-7111-95e9-2bacb04eb064",
		},
	}
	st.sessions[rec.ID] = rec

	if _, err := m.RestoreWithMode(context.Background(), rec.ID); !errors.Is(err, ports.ErrChatDriverIncompatible) {
		t.Fatalf("first RestoreWithMode error = %v, want ErrChatDriverIncompatible", err)
	}
	afterFailure := st.sessions[rec.ID]
	if !afterFailure.IsTerminated || afterFailure.Metadata.ProviderConversationID != rec.Metadata.ProviderConversationID ||
		afterFailure.Metadata.WorkspacePath != rec.Metadata.WorkspacePath {
		t.Fatalf("failed restore changed recoverable orchestrator: %+v", afterFailure)
	}

	launcher.startErr = nil
	result, err := m.RestoreWithMode(context.Background(), rec.ID)
	if err != nil {
		t.Fatalf("RestoreWithMode after dependency recovery: %v", err)
	}
	if result.Session.ID != rec.ID || result.Session.IsTerminated || result.Mode != RestoreModeNative {
		t.Fatalf("restored orchestrator = %+v mode=%q, want original live session with native resume", result.Session, result.Mode)
	}
	resumed := launcher.started[len(launcher.started)-1]
	if resumed.ProviderConversationID != rec.Metadata.ProviderConversationID || rt.created != 0 {
		t.Fatalf("restore continuity = provider %q runtime creates %d, want %q and no terminal runtime",
			resumed.ProviderConversationID, rt.created, rec.Metadata.ProviderConversationID)
	}
}

func TestHistoricalChatProviderScopeRequiresLatestCompletedMatchingHandoff(t *testing.T) {
	const (
		sessionID = domain.SessionID("mer-248")
		provider  = "native-248"
	)
	newStore := func() *historicalChatRestoreStore {
		transitions := newTransitionStore()
		return &historicalChatRestoreStore{
			transitionStore: transitions,
			conversation: domain.ConversationRecord{
				ID: "project-conversation", Scope: domain.ConversationScopeProject,
				ProjectID: chatTestProject, SessionID: sessionID, ActiveBranchID: "old-root",
			},
			activeBranch: domain.ConversationBranch{
				ID: "old-root", ConversationID: "project-conversation",
				SessionID: "mer-88", ProviderConversationID: "native-88", Active: true,
			},
		}
	}
	record := domain.SessionRecord{
		ID: sessionID, ProjectID: chatTestProject, Kind: domain.KindOrchestrator,
		Harness: domain.HarnessCodex, Mode: domain.SessionModeChat, IsTerminated: true,
		Metadata: domain.SessionMetadata{ProviderConversationID: provider},
	}
	now := time.Date(2026, 8, 28, 7, 0, 0, 0, time.UTC)

	t.Run("matching latest transition reserves deterministic boundary", func(t *testing.T) {
		st := newStore()
		st.transitions["handoff-248"] = domain.SessionInterfaceTransition{
			ID: "handoff-248", SessionID: sessionID,
			SourceMode: domain.SessionModeTUI, TargetMode: domain.SessionModeChat,
			Phase: domain.SessionInterfaceTransitionCompleted, NativeConversationID: provider,
			CreatedAt: now, CompletedAt: now,
		}
		m := New(Deps{Store: st})
		got, err := m.historicalChatProviderScopeID(context.Background(), record)
		if err != nil {
			t.Fatalf("historicalChatProviderScopeID: %v", err)
		}
		if got != "handoff-248:provider" {
			t.Fatalf("provider scope = %q, want deterministic handoff boundary", got)
		}
	})

	t.Run("newer transition prevents stale proof", func(t *testing.T) {
		st := newStore()
		st.transitions["matching-old"] = domain.SessionInterfaceTransition{
			ID: "matching-old", SessionID: sessionID,
			SourceMode: domain.SessionModeTUI, TargetMode: domain.SessionModeChat,
			Phase: domain.SessionInterfaceTransitionCompleted, NativeConversationID: provider,
			CreatedAt: now,
		}
		st.transitions["newer-other-provider"] = domain.SessionInterfaceTransition{
			ID: "newer-other-provider", SessionID: sessionID,
			SourceMode: domain.SessionModeTUI, TargetMode: domain.SessionModeChat,
			Phase: domain.SessionInterfaceTransitionCompleted, NativeConversationID: "native-other",
			CreatedAt: now.Add(time.Minute),
		}
		m := New(Deps{Store: st})
		got, err := m.historicalChatProviderScopeID(context.Background(), record)
		if err != nil {
			t.Fatalf("historicalChatProviderScopeID: %v", err)
		}
		if got != "" {
			t.Fatalf("provider scope = %q, want no repair from stale transition proof", got)
		}
	})

	t.Run("current owner is never forked", func(t *testing.T) {
		st := newStore()
		st.transitions["handoff-current"] = domain.SessionInterfaceTransition{
			ID: "handoff-current", SessionID: sessionID,
			SourceMode: domain.SessionModeTUI, TargetMode: domain.SessionModeChat,
			Phase: domain.SessionInterfaceTransitionCompleted, NativeConversationID: provider,
			CreatedAt: now,
		}
		st.activeBranch.SessionID = sessionID
		st.activeBranch.ProviderConversationID = provider
		m := New(Deps{Store: st})
		got, err := m.historicalChatProviderScopeID(context.Background(), record)
		if err != nil {
			t.Fatalf("historicalChatProviderScopeID: %v", err)
		}
		if got != "" {
			t.Fatalf("provider scope = %q, want ordinary idempotent resume", got)
		}
	})

	t.Run("matching proof cannot rebind a newer owner", func(t *testing.T) {
		st := newStore()
		st.transitions["handoff-no-owner"] = domain.SessionInterfaceTransition{
			ID: "handoff-no-owner", SessionID: sessionID,
			SourceMode: domain.SessionModeTUI, TargetMode: domain.SessionModeChat,
			Phase: domain.SessionInterfaceTransitionCompleted, NativeConversationID: provider,
			CreatedAt: now,
		}
		st.conversationErr = domain.ErrNoConversation
		m := New(Deps{Store: st})
		if _, err := m.historicalChatProviderScopeID(context.Background(), record); !errors.Is(err, domain.ErrNoConversation) {
			t.Fatalf("historicalChatProviderScopeID error = %v, want current-owner proof failure", err)
		}
	})
}

func TestRestoreTerminatedChatOrchestratorPassesProvenProviderBoundary(t *testing.T) {
	const sessionID = domain.SessionID("mer-248")
	st := &historicalChatRestoreStore{
		transitionStore: newTransitionStore(),
		conversation: domain.ConversationRecord{
			ID: "project-conversation", Scope: domain.ConversationScopeProject,
			ProjectID: chatTestProject, SessionID: sessionID, ActiveBranchID: "old-root",
		},
		activeBranch: domain.ConversationBranch{
			ID: "old-root", ConversationID: "project-conversation",
			SessionID: "mer-88", ProviderConversationID: "native-88", Active: true,
		},
	}
	st.projects["mer"] = domain.ProjectRecord{ID: "mer", Config: testRoleAgents()}
	rec := domain.SessionRecord{
		ID: sessionID, ProjectID: chatTestProject, Kind: domain.KindOrchestrator,
		Harness: domain.HarnessCodex, Mode: domain.SessionModeChat, IsTerminated: true,
		Activity: domain.Activity{State: domain.ActivityExited},
		Metadata: domain.SessionMetadata{
			Branch: "ao/orchestrator", WorkspacePath: "/ws/mer-248",
			ProviderConversationID: "native-248",
		},
	}
	st.sessions[sessionID] = rec
	st.transitions["handoff-248"] = domain.SessionInterfaceTransition{
		ID: "handoff-248", SessionID: sessionID,
		SourceMode: domain.SessionModeTUI, TargetMode: domain.SessionModeChat,
		Phase:                domain.SessionInterfaceTransitionCompleted,
		NativeConversationID: rec.Metadata.ProviderConversationID,
		CreatedAt:            time.Now().UTC(), CompletedAt: time.Now().UTC(),
	}
	providerErr := errors.New("provider unavailable")
	launcher := &recordingLauncher{startErr: providerErr}
	m := New(Deps{
		Runtime: &fakeRuntime{}, Agents: fakeAgents{}, Workspace: &fakeWorkspace{},
		Store: st, Messenger: &fakeMessenger{}, Chat: launcher,
		Lifecycle: &fakeLCM{store: st.fakeStore}, DataDir: "/ao-test-data",
	})

	if _, err := m.RestoreWithMode(context.Background(), sessionID); !errors.Is(err, providerErr) {
		t.Fatalf("RestoreWithMode error = %v, want provider failure", err)
	}
	if len(launcher.started) != 1 {
		t.Fatalf("Chat starts = %d, want 1", len(launcher.started))
	}
	start := launcher.started[0]
	if start.ProviderConversationID != "native-248" || start.ProviderScopeID != "handoff-248:provider" {
		t.Fatalf("historical restore start = provider %q scope %q",
			start.ProviderConversationID, start.ProviderScopeID)
	}
	if got := st.sessions[sessionID]; !got.IsTerminated || got.Metadata.ProviderConversationID != "native-248" {
		t.Fatalf("failed provider resume changed terminated target: %+v", got)
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

func TestResumeChatRotatesBrowserCapabilityBeforeControllerStart(t *testing.T) {
	launcher := &recordingLauncher{}
	mgr, store, _ := newChatManager(launcher)
	seedChatResumeSession(store, domain.ActivityExited)
	rec := store.sessions["mer-1"]
	authority := browsersvc.NewAuthority()
	oldToken, oldVerifier, err := authority.Issue(rec.ID)
	if err != nil {
		t.Fatalf("issue old capability: %v", err)
	}
	rec.Metadata.BrowserCapabilityVerifier = oldVerifier
	store.sessions[rec.ID] = rec
	issuer := &recordingBrowserAuthority{authority: authority}
	mgr.browserCapabilities = issuer
	launcher.beforeStart = func(cfg ChatStart) {
		stored := store.sessions[cfg.SessionID]
		if !authority.Valid(cfg.SessionID, cfg.Env[EnvBrowserCapability], stored.Metadata.BrowserCapabilityVerifier) {
			t.Fatal("fresh capability was not authorized when the resumed controller started")
		}
	}

	result, err := mgr.ResumeAgentWithMode(context.Background(), rec.ID)
	if err != nil {
		t.Fatalf("ResumeAgentWithMode: %v", err)
	}
	newToken := launcher.started[0].Env[EnvBrowserCapability]
	newVerifier := result.Session.Metadata.BrowserCapabilityVerifier
	if authority.Valid(rec.ID, oldToken, newVerifier) {
		t.Fatal("old browser capability remained authorized after Chat resume")
	}
	if !authority.Valid(rec.ID, newToken, newVerifier) {
		t.Fatal("new browser capability was not authorized after Chat resume")
	}
	encoded, err := json.Marshal(result.Session)
	if err != nil {
		t.Fatalf("marshal resumed session: %v", err)
	}
	if strings.Contains(string(encoded), oldToken) || strings.Contains(string(encoded), newToken) {
		t.Fatalf("session API JSON leaked a browser bearer: %s", encoded)
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

func TestDefaultChatSpawnFallsBackToTUIWhenUnavailable(t *testing.T) {
	tests := []struct {
		name            string
		withoutLauncher bool
		preflightErr    error
	}{
		{name: "launcher not configured", withoutLauncher: true},
		{name: "harness unsupported", preflightErr: ports.ErrChatUnsupported},
		{name: "driver unavailable", preflightErr: ports.ErrChatDriverUnavailable},
		{name: "driver incompatible", preflightErr: ports.ErrChatDriverIncompatible},
		{name: "authentication required", preflightErr: ports.ErrChatAuthRequired},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			launcher := &recordingLauncher{preflightErr: tt.preflightErr}
			var chat ChatLauncher = launcher
			if tt.withoutLauncher {
				chat = nil
			}
			mgr, _, runtime := newChatManager(chat)
			mgr.defaults = fixedSessionModeDefaults(domain.SessionModeChat)

			rec, _, _, err := mgr.Spawn(context.Background(), ports.SpawnConfig{
				ProjectID: chatTestProject,
				Kind:      domain.KindWorker,
				Harness:   domain.HarnessCodex,
			})
			if err != nil {
				t.Fatalf("Spawn: %v", err)
			}
			if rec.Mode != domain.SessionModeTUI {
				t.Fatalf("mode = %q, want TUI fallback", rec.Mode)
			}
			if runtime.created == 0 {
				t.Fatal("TUI fallback created no terminal runtime")
			}
			if len(launcher.started) != 0 {
				t.Fatalf("fallback started %d Chat controllers, want 0", len(launcher.started))
			}
		})
	}
}

func TestDefaultChatSpawnReturnsUnexpectedPreflightError(t *testing.T) {
	preflightErr := errors.New("probe state corrupted")
	launcher := &recordingLauncher{preflightErr: preflightErr}
	mgr, store, runtime := newChatManager(launcher)
	mgr.defaults = fixedSessionModeDefaults(domain.SessionModeChat)

	_, _, _, err := mgr.Spawn(context.Background(), ports.SpawnConfig{
		ProjectID: chatTestProject,
		Kind:      domain.KindWorker,
		Harness:   domain.HarnessCodex,
	})
	if !errors.Is(err, preflightErr) {
		t.Fatalf("Spawn error = %v, want unexpected preflight error", err)
	}
	if runtime.created != 0 {
		t.Fatalf("unexpected preflight failure created %d terminal runtimes, want 0", runtime.created)
	}
	sessions, listErr := store.ListAllSessions(context.Background())
	if listErr != nil {
		t.Fatalf("list sessions: %v", listErr)
	}
	if len(sessions) != 0 {
		t.Fatalf("unexpected preflight failure left %d session rows, want 0", len(sessions))
	}
}

func TestDefaultChatSpawnUsesChatWhenAvailable(t *testing.T) {
	launcher := &recordingLauncher{}
	mgr, store, runtime := newChatManager(launcher)
	mgr.defaults = fixedSessionModeDefaults(domain.SessionModeChat)
	project := store.projects[string(chatTestProject)]
	project.Config.AgentConfig.Permissions = ports.PermissionModeBypassPermissions
	store.projects[string(chatTestProject)] = project

	rec, _, _, err := mgr.Spawn(context.Background(), ports.SpawnConfig{
		ProjectID: chatTestProject,
		Kind:      domain.KindWorker,
		Harness:   domain.HarnessCodex,
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if rec.Mode != domain.SessionModeChat {
		t.Fatalf("mode = %q, want chat", rec.Mode)
	}
	if runtime.created != 0 {
		t.Fatalf("default Chat spawn created %d terminal runtimes, want 0", runtime.created)
	}
	if len(launcher.preflighted) != 1 || len(launcher.started) != 1 {
		t.Fatalf("default Chat dispatch: preflight=%v started=%d, want one of each",
			launcher.preflighted, len(launcher.started))
	}
	if len(launcher.preflightPermissions) != 1 ||
		launcher.preflightPermissions[0] != ports.PermissionModeBypassPermissions {
		t.Fatalf("preflight permissions = %v, want bypass-permissions", launcher.preflightPermissions)
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

func TestChatSpawnPersistsBrowserCapabilityBeforeControllerStart(t *testing.T) {
	launcher := &recordingLauncher{}
	mgr, store, runtime := newChatManager(launcher)
	mgr.browserCapabilities = &scriptedBrowserCapabilities{issues: []browserCapabilityIssue{{
		token: "chat-token", verifier: "chat-verifier",
	}}}
	project := store.projects[string(chatTestProject)]
	project.Config.Env = map[string]string{
		EnvBrowserCapability:        "project-token",
		EnvBrowserRuntimeToken:      "runtime-secret",
		EnvBrowserRuntimeTokenStdin: "1",
	}
	store.projects[string(chatTestProject)] = project
	launcher.beforeStart = func(cfg ChatStart) {
		stored := store.sessions[cfg.SessionID]
		if got := stored.Metadata.BrowserCapabilityVerifier; got != "chat-verifier" {
			t.Fatalf("verifier at controller start = %q, want chat-verifier", got)
		}
	}

	rec, _, _, err := mgr.Spawn(context.Background(), ports.SpawnConfig{
		ProjectID:     chatTestProject,
		Kind:          domain.KindWorker,
		Harness:       domain.HarnessCodex,
		RequestedMode: domain.SessionModeChat,
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if runtime.created != 0 {
		t.Fatalf("Chat spawn created %d terminal runtimes", runtime.created)
	}
	start := launcher.started[0]
	if got := start.Env[EnvBrowserCapability]; got != "chat-token" {
		t.Fatalf("controller capability = %q, want issued token", got)
	}
	if start.Env[EnvBrowserRuntimeToken] != "" || start.Env[EnvBrowserRuntimeTokenStdin] != "" {
		t.Fatalf("browser runtime secrets reached Chat controller: token=%q stdin=%q",
			start.Env[EnvBrowserRuntimeToken], start.Env[EnvBrowserRuntimeTokenStdin])
	}
	if got := rec.Metadata.BrowserCapabilityVerifier; got != "chat-verifier" {
		t.Fatalf("committed verifier = %q, want chat-verifier", got)
	}
}

func TestChatSpawnCapabilityFailurePreventsControllerStart(t *testing.T) {
	tests := []struct {
		name       string
		issue      browserCapabilityIssue
		persistErr error
	}{
		{name: "issuer error", issue: browserCapabilityIssue{err: errors.New("entropy unavailable")}},
		{name: "empty token", issue: browserCapabilityIssue{verifier: "verifier"}},
		{name: "empty verifier", issue: browserCapabilityIssue{token: "token"}},
		{name: "verifier persistence", issue: browserCapabilityIssue{token: "token", verifier: "verifier"}, persistErr: errors.New("database unavailable")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			launcher := &recordingLauncher{}
			mgr, store, runtime := newChatManager(launcher)
			mgr.browserCapabilities = &scriptedBrowserCapabilities{issues: []browserCapabilityIssue{tt.issue}}
			store.updateSessionErr = tt.persistErr

			_, _, _, err := mgr.Spawn(context.Background(), ports.SpawnConfig{
				ProjectID:     chatTestProject,
				Kind:          domain.KindWorker,
				Harness:       domain.HarnessCodex,
				RequestedMode: domain.SessionModeChat,
			})
			if !errors.Is(err, ErrSpawnBrowser) {
				t.Fatalf("Spawn error = %v, want ErrSpawnBrowser", err)
			}
			if len(launcher.started) != 0 || runtime.created != 0 {
				t.Fatalf("failed capability started controller/runtime: chat=%d runtime=%d",
					len(launcher.started), runtime.created)
			}
			for _, session := range store.sessions {
				if !session.IsTerminated {
					t.Fatalf("capability failure left session %s live", session.ID)
				}
			}
		})
	}
}

func TestChatSpawnCommitsReservedProviderBoundaryWithLifecycleOwner(t *testing.T) {
	boundary := &domain.ConversationBranch{
		ID: "fresh-provider-boundary", ConversationID: "project-conversation", SessionID: "mer-1",
		ProviderConversationID: "thread-1", ParentBranchID: "source-provider-boundary",
		ProviderScopeID: "fresh-provider-boundary",
	}
	launcher := &recordingLauncher{providerBoundary: boundary}
	mgr, _, _ := newChatManager(launcher)
	lcm := mgr.lcm.(*fakeLCM)

	if _, _, _, err := mgr.Spawn(context.Background(), ports.SpawnConfig{
		ProjectID: chatTestProject, Kind: domain.KindOrchestrator, Harness: domain.HarnessCodex,
		RequestedMode: domain.SessionModeChat,
	}); err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if len(lcm.chatBoundaries) != 1 || lcm.chatBoundaries[0].ID != boundary.ID {
		t.Fatalf("lifecycle Chat boundaries = %+v, want %q", lcm.chatBoundaries, boundary.ID)
	}
	if lcm.completed != 1 {
		t.Fatalf("completed lifecycle launches = %d, want 1 atomic Chat commit", lcm.completed)
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
