package sessionmanager

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/codexops"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type bootstrapOrderingCredentials struct {
	mu           sync.Mutex
	bootstrapped bool
	mutationHeld bool
	calls        []string
}

type rollbackTrackingCredentials struct {
	bootstrapOrderingCredentials
	restoreCalls int
	verified     []string
}

func (*rollbackTrackingCredentials) CheckpointAndActivateCodexAccount(context.Context, string, string, int64) (domain.CodexActiveAccount, error) {
	return domain.CodexActiveAccount{}, errors.New("injected activation failure")
}

func (c *rollbackTrackingCredentials) RestoreCodexAccountCredential(_ context.Context, sourceAccountID, _ string) error {
	c.restoreCalls++
	if sourceAccountID != "source" {
		return fmt.Errorf("restore source = %q", sourceAccountID)
	}
	return nil
}

func (c *rollbackTrackingCredentials) VerifyCurrentCodexAccount(_ context.Context, accountID string) error {
	c.verified = append(c.verified, accountID)
	return nil
}

type blockingBootstrapAdmissionCredentials struct {
	*bootstrapOrderingCredentials
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (c *blockingBootstrapAdmissionCredentials) WaitCodexAccountBootstrap(ctx context.Context) error {
	c.once.Do(func() { close(c.entered) })
	select {
	case <-c.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *bootstrapOrderingCredentials) record(call string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, call)
}

func TestCodexControllerAdmissionWaitsForBootstrapBeforeReadingGate(t *testing.T) {
	gate := codexops.NewGate()
	bootstrapLease, err := gate.AcquireExclusive(context.Background())
	if err != nil {
		t.Fatalf("acquire bootstrap lease: %v", err)
	}
	credentials := &blockingBootstrapAdmissionCredentials{
		bootstrapOrderingCredentials: &bootstrapOrderingCredentials{},
		entered:                      make(chan struct{}),
		release:                      make(chan struct{}),
	}
	manager := New(Deps{CodexOperationGate: gate})
	manager.SetAgentReadiness(credentials)

	type admissionResult struct {
		release func()
		err     error
	}
	done := make(chan admissionResult, 1)
	go func() {
		release, acquireErr := manager.acquireCodexControllerAdmission(context.Background(), domain.HarnessCodex)
		done <- admissionResult{release: release, err: acquireErr}
	}()

	select {
	case <-credentials.entered:
	case <-time.After(time.Second):
		t.Fatal("Codex controller admission did not wait for account bootstrap")
	}
	select {
	case result := <-done:
		if result.release != nil {
			result.release()
		}
		t.Fatalf("Codex controller admission completed during bootstrap: %v", result.err)
	default:
	}

	bootstrapLease.Release()
	close(credentials.release)
	select {
	case result := <-done:
		if result.err != nil {
			t.Fatalf("Codex controller admission after bootstrap: %v", result.err)
		}
		result.release()
	case <-time.After(time.Second):
		t.Fatal("Codex controller admission did not resume after bootstrap")
	}
}

func (c *bootstrapOrderingCredentials) EnsureAgentReadiness(context.Context, string, domain.AgentReadinessPurpose) (domain.AgentReadinessSnapshot, error) {
	return domain.AgentReadinessSnapshot{}, nil
}
func (*bootstrapOrderingCredentials) InvalidateAgentInstallation(string)   {}
func (*bootstrapOrderingCredentials) InvalidateAgentAuthentication(string) {}
func (*bootstrapOrderingCredentials) RecheckAgent(string)                  {}
func (c *bootstrapOrderingCredentials) WaitCodexAccountBootstrap(context.Context) error {
	c.record("bootstrap")
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.bootstrapped {
		return nil
	}
	if c.mutationHeld {
		return errors.New("bootstrap reconciliation blocked by held mutation token")
	}
	c.bootstrapped = true
	return nil
}
func (c *bootstrapOrderingCredentials) BeginCodexAccountMutation(context.Context) error {
	c.record("begin")
	c.mu.Lock()
	c.mutationHeld = true
	c.mu.Unlock()
	return nil
}
func (c *bootstrapOrderingCredentials) EndCodexAccountMutation() {
	c.record("end")
	c.mu.Lock()
	c.mutationHeld = false
	c.mu.Unlock()
}
func (*bootstrapOrderingCredentials) CurrentCodexActiveAccount() domain.CodexActiveAccount {
	return domain.CodexActiveAccount{AccountID: "source", Revision: 1}
}
func (*bootstrapOrderingCredentials) CodexAccountLoginInProgress() bool { return false }
func (c *bootstrapOrderingCredentials) VerifyCodexAccountForSwitch(ctx context.Context, _ string) error {
	c.record("verify")
	c.mu.Lock()
	held := c.mutationHeld
	c.mu.Unlock()
	if !held {
		return errors.New("target verification ran outside mutation token")
	}
	return c.WaitCodexAccountBootstrap(ctx)
}
func (*bootstrapOrderingCredentials) VerifyCurrentCodexAccount(context.Context, string) error {
	return nil
}
func (*bootstrapOrderingCredentials) CheckpointAndActivateCodexAccount(context.Context, string, string, int64) (domain.CodexActiveAccount, error) {
	return domain.CodexActiveAccount{AccountID: "target", Revision: 2}, nil
}
func (*bootstrapOrderingCredentials) RestoreCodexAccountCredential(context.Context, string, string) error {
	return nil
}

type bootstrapOrderingStore struct {
	*fakeStore
	*collectingCodexSwitchStore
}

type collectingCodexSwitchStore struct {
	sessions []domain.CodexAccountSwitchSession
}

func (s *collectingCodexSwitchStore) CreateCodexAccountSwitch(_ context.Context, rec domain.CodexAccountSwitch) (domain.CodexAccountSwitch, bool, error) {
	return rec, true, nil
}
func (s *collectingCodexSwitchStore) GetCodexAccountSwitch(context.Context, string) (domain.CodexAccountSwitch, bool, error) {
	return domain.CodexAccountSwitch{}, false, nil
}
func (s *collectingCodexSwitchStore) GetCodexAccountSwitchByIdempotency(context.Context, string) (domain.CodexAccountSwitch, bool, error) {
	return domain.CodexAccountSwitch{}, false, nil
}
func (s *collectingCodexSwitchStore) GetActiveCodexAccountSwitch(context.Context) (domain.CodexAccountSwitch, bool, error) {
	return domain.CodexAccountSwitch{}, false, nil
}
func (s *collectingCodexSwitchStore) UpdateCodexAccountSwitch(context.Context, domain.CodexAccountSwitch, domain.CodexAccountSwitchPhase) (bool, error) {
	return true, nil
}
func (s *collectingCodexSwitchStore) InsertCodexAccountSwitchSession(_ context.Context, _ string, rec domain.CodexAccountSwitchSession) error {
	s.sessions = append(s.sessions, rec)
	return nil
}
func (s *collectingCodexSwitchStore) ListCodexAccountSwitchSessions(context.Context, string) ([]domain.CodexAccountSwitchSession, error) {
	return append([]domain.CodexAccountSwitchSession(nil), s.sessions...), nil
}
func (s *collectingCodexSwitchStore) UpdateCodexAccountSwitchSession(context.Context, string, domain.CodexAccountSwitchSession, string, string) (bool, error) {
	return true, nil
}

type ambiguousCodexSwitchStore struct {
	*fakeStore
	switchRecord domain.CodexAccountSwitch
	session      domain.CodexAccountSwitchSession
}

type settlingSwitchChatLauncher struct {
	*recordingLauncher
	attempts int
}

func (l *settlingSwitchChatLauncher) StartChat(ctx context.Context, cfg ChatStart) (ChatStarted, error) {
	var err error
	cfg, err = prepareTestChatStart(ctx, cfg)
	if err != nil {
		return ChatStarted{}, err
	}
	l.started = append(l.started, cfg)
	l.attempts++
	if l.attempts == 1 {
		return ChatStarted{}, fmt.Errorf("history is still flushing: %w", ports.ErrChatHistoryUnsettled)
	}
	started := ChatStarted{
		ProviderConversationID: cfg.ProviderConversationID,
		ControllerGeneration:   cfg.ControllerGeneration,
	}
	if cfg.ControllerReady != nil {
		if _, err := cfg.ControllerReady(started); err != nil {
			return ChatStarted{}, err
		}
	}
	l.live = true
	return started, nil
}

func (l *settlingSwitchChatLauncher) StopChat(_ context.Context, id domain.SessionID) error {
	l.stopped = append(l.stopped, id)
	l.live = false
	return nil
}

func (s *ambiguousCodexSwitchStore) CreateCodexAccountSwitch(_ context.Context, rec domain.CodexAccountSwitch) (domain.CodexAccountSwitch, bool, error) {
	s.switchRecord = rec
	return rec, true, nil
}
func (s *ambiguousCodexSwitchStore) GetCodexAccountSwitch(context.Context, string) (domain.CodexAccountSwitch, bool, error) {
	return s.switchRecord, true, nil
}
func (*ambiguousCodexSwitchStore) GetCodexAccountSwitchByIdempotency(context.Context, string) (domain.CodexAccountSwitch, bool, error) {
	return domain.CodexAccountSwitch{}, false, nil
}
func (s *ambiguousCodexSwitchStore) GetActiveCodexAccountSwitch(context.Context) (domain.CodexAccountSwitch, bool, error) {
	return s.switchRecord, !s.switchRecord.Phase.Terminal(), nil
}
func (s *ambiguousCodexSwitchStore) UpdateCodexAccountSwitch(_ context.Context, rec domain.CodexAccountSwitch, _ domain.CodexAccountSwitchPhase) (bool, error) {
	s.switchRecord = rec
	return false, errors.New("injected post-commit switch error")
}
func (s *ambiguousCodexSwitchStore) ListCodexAccountSwitchSessions(context.Context, string) ([]domain.CodexAccountSwitchSession, error) {
	return []domain.CodexAccountSwitchSession{s.session}, nil
}
func (s *ambiguousCodexSwitchStore) UpdateCodexAccountSwitchSession(_ context.Context, _ string, rec domain.CodexAccountSwitchSession, _, _ string) (bool, error) {
	s.session = rec
	return false, errors.New("injected post-commit session error")
}

func TestCodexAccountSwitchFingerprintIsVersionedAndStable(t *testing.T) {
	t.Parallel()
	first := codexAccountSwitchFingerprint("account-b", 7)
	if !strings.HasPrefix(first, "v1:") || len(first) != len("v1:")+64 {
		t.Fatalf("fingerprint = %q", first)
	}
	if got := codexAccountSwitchFingerprint("account-b", 7); got != first {
		t.Fatalf("stable fingerprint = %q, want %q", got, first)
	}
	if got := codexAccountSwitchFingerprint("account-c", 7); got == first {
		t.Fatal("target account must participate in fingerprint")
	}
	if got := codexAccountSwitchFingerprint("account-b", 8); got == first {
		t.Fatal("account revision must participate in fingerprint")
	}
}

func TestCodexAccountSwitchBootstrapsBeforeHoldingMutationToken(t *testing.T) {
	credentials := &bootstrapOrderingCredentials{}
	store := &bootstrapOrderingStore{fakeStore: newFakeStore(), collectingCodexSwitchStore: &collectingCodexSwitchStore{}}
	manager := New(Deps{Store: store, Runtime: &fakeRuntime{}})
	manager.SetAgentReadiness(credentials)

	if _, err := manager.StartCodexAccountSwitch(context.Background(), ports.CodexAccountSwitchConfig{
		TargetAccountID: "target", ExpectedAccountRevision: 1, IdempotencyKey: "bootstrap-order",
	}); err != nil {
		t.Fatalf("start switch: %v", err)
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := manager.WaitAgentSwitchWorkers(waitCtx); err != nil {
		t.Fatal(err)
	}
	credentials.mu.Lock()
	calls := append([]string(nil), credentials.calls...)
	credentials.mu.Unlock()
	if len(calls) < 4 || !slices.Equal(calls[:4], []string{"bootstrap", "begin", "verify", "bootstrap"}) {
		t.Fatalf("admission order = %v", calls)
	}
}

func TestRetainCodexAccountSwitchFence(t *testing.T) {
	t.Parallel()
	for _, phase := range []string{
		"requested", "stopping_sessions", "sessions_stopped",
		"checkpointing_source", "activating_target", "verifying_target", "restarting_sessions",
		"rollback_required", "recovery_required",
	} {
		if !retainCodexAccountSwitchFence(domain.CodexAccountSwitchPhase(phase)) {
			t.Fatalf("phase %s must retain the fence", phase)
		}
	}
	for _, phase := range []string{"completed", "failed"} {
		if retainCodexAccountSwitchFence(domain.CodexAccountSwitchPhase(phase)) {
			t.Fatalf("phase %s must release the fence", phase)
		}
	}
}

func TestCodexAccountSwitchAutomaticallyRestoresSourceAfterActivationFailure(t *testing.T) {
	credentials := &rollbackTrackingCredentials{}
	store := &bootstrapOrderingStore{fakeStore: newFakeStore(), collectingCodexSwitchStore: &collectingCodexSwitchStore{}}
	manager := New(Deps{Store: store, Runtime: &fakeRuntime{}})
	sw := domain.CodexAccountSwitch{
		ID: "switch-1", SourceAccountID: "source", TargetAccountID: "target",
		ExpectedAccountRevision: 1, Phase: domain.CodexAccountSwitchActivatingAccount,
	}

	manager.dispatchCodexAccountSwitch(context.Background(), credentials, store, &sw, nil)

	if sw.Phase != domain.CodexAccountSwitchFailed {
		t.Fatalf("phase = %q, want failed after automatic rollback", sw.Phase)
	}
	if sw.FailureCode != "activation_unconfirmed" {
		t.Fatalf("failure code = %q, want activation_unconfirmed", sw.FailureCode)
	}
	if credentials.restoreCalls != 1 {
		t.Fatalf("restore calls = %d, want 1", credentials.restoreCalls)
	}
	if !slices.Equal(credentials.verified, []string{"source"}) {
		t.Fatalf("verified accounts = %v, want source", credentials.verified)
	}
}

func TestCodexAccountSwitchAdoptsJournalWritesReportedAsErrors(t *testing.T) {
	store := &ambiguousCodexSwitchStore{
		fakeStore:    newFakeStore(),
		switchRecord: domain.CodexAccountSwitch{ID: "switch-1", Phase: domain.CodexAccountSwitchRequested},
		session: domain.CodexAccountSwitchSession{
			SessionID: "session-1", StopState: "pending", RestartState: "pending",
			ReviewerStopState: "skipped", ReviewerRestartState: "skipped",
		},
	}
	manager := New(Deps{Store: store, Runtime: &fakeRuntime{}})
	sw := store.switchRecord
	if err := manager.advanceCodexAccountSwitch(context.Background(), store, &sw, domain.CodexAccountSwitchStoppingSessions, ""); err != nil {
		t.Fatalf("advance did not adopt committed phase: %v", err)
	}
	if sw.Phase != domain.CodexAccountSwitchStoppingSessions {
		t.Fatalf("phase = %q", sw.Phase)
	}
	item := store.session
	item.ErrorCode = codexSwitchStopIntent
	if err := manager.persistCodexSwitchSession(context.Background(), store, "switch-1", item, "pending", "pending"); err != nil {
		t.Fatalf("session write did not adopt committed progress: %v", err)
	}
}

func TestCodexAccountSwitchProjectsStoppedWorkerAsRecoverable(t *testing.T) {
	store := &ambiguousCodexSwitchStore{
		fakeStore:    newFakeStore(),
		switchRecord: domain.CodexAccountSwitch{ID: "switch-1", Phase: domain.CodexAccountSwitchStoppingSessions},
		session:      domain.CodexAccountSwitchSession{SessionID: "session-1"},
	}
	manager := New(Deps{Store: store, Runtime: &fakeRuntime{}})
	loaded, err := manager.loadCodexAccountSwitchSessions(context.Background(), store, store.switchRecord)
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.CanRecover {
		t.Fatalf("nonterminal switch without worker was not recoverable: %#v", loaded)
	}
}

func TestCodexAccountSwitchPersistsStopIntentBeforeRuntimeDestroy(t *testing.T) {
	base := newFakeStore()
	base.sessions["session-1"] = domain.SessionRecord{
		ID: "session-1", Harness: domain.HarnessCodex, Mode: domain.SessionModeTUI,
		Activity: domain.Activity{State: domain.ActivityIdle},
		Metadata: domain.SessionMetadata{RuntimeHandleID: "runtime-1", RuntimeLaunchID: "source-generation", AgentSessionID: "native-1"},
	}
	item := domain.CodexAccountSwitchSession{
		SessionID: "session-1", InterfaceMode: domain.SessionModeTUI, WasRunning: true,
		NativeSessionID: "native-1", SourceHandleID: "runtime-1", SourceGeneration: "source-generation",
		StopState: "pending", RestartState: "pending", ReviewerStopState: "skipped", ReviewerRestartState: "skipped",
	}
	store := &ambiguousCodexSwitchStore{fakeStore: base, session: item}
	runtime := &fakeRuntime{aliveByHandle: map[string]bool{"runtime-1": true}, destroyErr: errors.New("injected stop failure")}
	runtime.onDestroy = func(_ int, _ ports.RuntimeHandle) {
		if store.session.ErrorCode != codexSwitchStopIntent {
			t.Errorf("runtime destroyed before durable stop intent: %#v", store.session)
		}
	}
	manager := New(Deps{Store: store, Runtime: runtime})
	if err := manager.stopCodexSwitchSessions(context.Background(), store, "switch-1", []domain.CodexAccountSwitchSession{item}); err == nil {
		t.Fatal("stop unexpectedly succeeded")
	}
}

func TestCodexAccountSwitchSkipsStoppedSessionsWithoutNativeIdentity(t *testing.T) {
	store := newFakeStore()
	store.sessions["stopped-codex"] = domain.SessionRecord{
		ID: "stopped-codex", Harness: domain.HarnessCodex, Mode: domain.SessionModeTUI,
	}
	manager := New(Deps{Store: store, Runtime: &fakeRuntime{}})
	sessions, err := manager.buildCodexAccountSwitchSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 0 {
		t.Fatalf("stopped session was included in switch: %#v", sessions)
	}
}

func TestCodexAccountSwitchRejectsRunningSessionWithoutExactNativeIdentity(t *testing.T) {
	store := newFakeStore()
	store.sessions["running-codex"] = domain.SessionRecord{
		ID: "running-codex", Harness: domain.HarnessCodex, Mode: domain.SessionModeTUI,
		Metadata: domain.SessionMetadata{RuntimeHandleID: "runtime-1", RuntimeLaunchID: "generation-1"},
	}
	manager := New(Deps{Store: store, Runtime: &fakeRuntime{aliveByHandle: map[string]bool{"runtime-1": true}}})
	_, err := manager.buildCodexAccountSwitchSnapshot(context.Background())
	if !errors.Is(err, ports.ErrCodexRunningSessionNotResumable) {
		t.Fatalf("populate error = %v, want running-session-not-resumable", err)
	}
}

func TestCodexAccountSwitchAllowsFreshRestartForUntouchedTUI(t *testing.T) {
	store := newFakeStore()
	store.sessions["untouched-codex"] = domain.SessionRecord{
		ID: "untouched-codex", Harness: domain.HarnessCodex, Kind: domain.KindOrchestrator, Mode: domain.SessionModeTUI,
		Metadata: domain.SessionMetadata{RuntimeHandleID: "runtime-1", RuntimeLaunchID: "generation-1"},
	}
	log := []string{}
	runtime := &transitionRuntime{
		fakeRuntime: &fakeRuntime{aliveByHandle: map[string]bool{"runtime-1": true}},
		log:         &log,
		outputForCall: func(int) string {
			return idleTerminalOutput
		},
	}
	manager := New(Deps{
		Store: store, Runtime: runtime,
		Agents: singleAgent{agent: untouchedEmptyTransitionAgent{}},
	})
	sessions, err := manager.buildCodexAccountSwitchSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || !sessions[0].WasRunning || sessions[0].NativeSessionID != "" {
		t.Fatalf("recorded fresh restart = %#v", sessions)
	}
	forceFresh, requireNative := codexAccountSwitchRestartPolicy(sessions[0])
	if !forceFresh || requireNative {
		t.Fatalf("restart policy = fresh %t native %t, want true/false", forceFresh, requireNative)
	}
}

func TestCodexAccountSwitchNativeRestartPolicyRemainsExact(t *testing.T) {
	forceFresh, requireNative := codexAccountSwitchRestartPolicy(domain.CodexAccountSwitchSession{
		WasRunning: true, InterfaceMode: domain.SessionModeTUI, NativeSessionID: "native-thread-1",
	})
	if forceFresh || !requireNative {
		t.Fatalf("restart policy = fresh %t native %t, want false/true", forceFresh, requireNative)
	}
}

func TestCodexAccountSwitchInterruptsChatInsteadOfWaitingForDrain(t *testing.T) {
	launcher := &recordingLauncher{}
	manager := New(Deps{Chat: launcher})
	sessions := []domain.CodexAccountSwitchSession{{
		SessionID: "chat-session", InterfaceMode: domain.SessionModeChat, WasRunning: true,
	}}
	abort, err := manager.armCodexSwitchChatInterrupt(context.Background(), sessions)
	if err != nil {
		t.Fatal(err)
	}
	defer abort()
	if err := manager.prepareCodexSwitchChatInterrupt(context.Background(), sessions); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(launcher.armPolicy, []domain.SessionInterfaceTransitionPolicy{domain.SessionInterfaceTransitionInterrupt}) {
		t.Fatalf("arm policies = %v, want interrupt", launcher.armPolicy)
	}
	if !slices.Equal(launcher.preparePolicy, []domain.SessionInterfaceTransitionPolicy{domain.SessionInterfaceTransitionInterrupt}) {
		t.Fatalf("prepare policies = %v, want interrupt", launcher.preparePolicy)
	}
}

func TestCodexAccountSwitchFreezesActiveTUIInputWithoutWaitingForIdle(t *testing.T) {
	store := newFakeStore()
	store.sessions["active-codex"] = domain.SessionRecord{
		ID: "active-codex", Harness: domain.HarnessCodex, Mode: domain.SessionModeTUI,
		Activity: domain.Activity{State: domain.ActivityActive},
		Metadata: domain.SessionMetadata{RuntimeHandleID: "runtime-1"},
	}
	manager := New(Deps{Store: store, Runtime: &fakeRuntime{}})
	gate := &transitionInputGate{acquired: make(chan string, 1), released: make(chan string, 1)}
	manager.SetTerminalInputGate(gate)
	release, err := manager.freezeCodexSwitchTerminalInput(context.Background(), []domain.CodexAccountSwitchSession{{
		SessionID: "active-codex", InterfaceMode: domain.SessionModeTUI, WasRunning: true,
	}})
	if err != nil {
		t.Fatalf("freeze active TUI input: %v", err)
	}
	if got := <-gate.acquired; got != "runtime-1" {
		t.Fatalf("drained terminal = %q, want runtime-1", got)
	}
	release()
	if got := <-gate.released; got != "runtime-1" {
		t.Fatalf("released terminal = %q, want runtime-1", got)
	}
}

func TestCodexAccountSwitchSkipsPreservedShellAfterCodexExits(t *testing.T) {
	store := newFakeStore()
	store.sessions["exited-codex"] = domain.SessionRecord{
		ID: "exited-codex", Harness: domain.HarnessCodex, Mode: domain.SessionModeTUI,
		Metadata: domain.SessionMetadata{RuntimeHandleID: "runtime-1", RuntimeLaunchID: "generation-1"},
	}
	workloadStopped := false
	manager := New(Deps{Store: store, Runtime: &fakeRuntime{
		aliveByHandle:           map[string]bool{"runtime-1": true},
		supervisedAliveOverride: &workloadStopped,
	}})
	sessions, err := manager.buildCodexAccountSwitchSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 0 {
		t.Fatalf("preserved shell was included in switch: %#v", sessions)
	}
}

func TestCodexAccountSwitchFailsClosedWhenWorkloadProbeFails(t *testing.T) {
	store := newFakeStore()
	store.sessions["unknown-codex"] = domain.SessionRecord{
		ID: "unknown-codex", Harness: domain.HarnessCodex, Mode: domain.SessionModeTUI,
		Metadata: domain.SessionMetadata{RuntimeHandleID: "runtime-1", RuntimeLaunchID: "generation-1"},
	}
	manager := New(Deps{Store: store, Runtime: &fakeRuntime{
		aliveByHandle: map[string]bool{"runtime-1": true},
		supervisedErr: errors.New("probe unavailable"),
	}})
	_, err := manager.buildCodexAccountSwitchSnapshot(context.Background())
	if err == nil || !strings.Contains(err.Error(), "inspect Codex workload") {
		t.Fatalf("populate error = %v, want workload inspection failure", err)
	}
}

func TestCodexAccountSwitchRecordsRunningSessionForSameNativeResume(t *testing.T) {
	store := newFakeStore()
	store.sessions["running-codex"] = domain.SessionRecord{
		ID: "running-codex", Harness: domain.HarnessCodex, Mode: domain.SessionModeTUI,
		Metadata: domain.SessionMetadata{
			RuntimeHandleID: "runtime-1", RuntimeLaunchID: "generation-1", AgentSessionID: "native-thread-1",
		},
	}
	manager := New(Deps{Store: store, Runtime: &fakeRuntime{aliveByHandle: map[string]bool{"runtime-1": true}}})
	sessions, err := manager.buildCodexAccountSwitchSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].NativeSessionID != "native-thread-1" || !sessions[0].WasRunning {
		t.Fatalf("recorded switch sessions = %#v", sessions)
	}
}

func TestCodexAccountSwitchRestartContinuesAfterEarlierSessionFailure(t *testing.T) {
	base := newFakeStore()
	base.projects["mer"] = domain.ProjectRecord{ID: "mer", Config: testRoleAgents()}
	for _, id := range []domain.SessionID{"session-a", "session-b"} {
		base.sessions[id] = domain.SessionRecord{
			ID: id, ProjectID: "mer", Kind: domain.KindWorker,
			Harness: domain.HarnessCodex, Mode: domain.SessionModeTUI,
			Activity: domain.Activity{State: domain.ActivityExited},
			Metadata: domain.SessionMetadata{
				WorkspacePath: "/ws/" + string(id), Branch: "ao/" + string(id),
				RuntimeHandleID: "runtime-" + string(id), RuntimeLaunchID: "source-" + string(id),
				AgentSessionID: "native-" + string(id),
			},
		}
	}
	journal := &collectingCodexSwitchStore{}
	store := &bootstrapOrderingStore{fakeStore: base, collectingCodexSwitchStore: journal}
	runtime := &fakeRuntime{createErrSequence: []error{errors.New("first restart failed"), nil}}
	manager := New(Deps{
		Store: store, Runtime: runtime, Agents: fakeAgents{}, Workspace: &fakeWorkspace{},
		Lifecycle: &fakeLCM{store: base}, LookPath: func(string) (string, error) { return "/bin/true", nil },
	})
	generation := 0
	manager.newLaunchID = func() string {
		generation++
		return fmt.Sprintf("target-%d", generation)
	}
	sessions := []domain.CodexAccountSwitchSession{
		{
			SessionID: "session-a", InterfaceMode: domain.SessionModeTUI, WasRunning: true,
			NativeSessionID: "native-session-a", SourceHandleID: "runtime-session-a", SourceGeneration: "source-session-a",
			StopState: "stopped", RestartState: "pending", ReviewerStopState: "skipped", ReviewerRestartState: "skipped",
		},
		{
			SessionID: "session-b", InterfaceMode: domain.SessionModeTUI, WasRunning: true,
			NativeSessionID: "native-session-b", SourceHandleID: "runtime-session-b", SourceGeneration: "source-session-b",
			StopState: "stopped", RestartState: "pending", ReviewerStopState: "skipped", ReviewerRestartState: "skipped",
		},
	}

	if err := manager.restartCodexSwitchSessions(context.Background(), store, "switch-1", sessions); err == nil {
		t.Fatal("restart unexpectedly reported complete success")
	}
	if sessions[0].RestartState != "failed" {
		t.Fatalf("first restart state = %q, want failed", sessions[0].RestartState)
	}
	if sessions[1].RestartState != "restarted" {
		t.Fatalf("second restart state = %q, want restarted", sessions[1].RestartState)
	}
}

func TestCodexAccountSwitchRetriesUnsettledHistoryAfterInterruptingActiveChat(t *testing.T) {
	base := newFakeStore()
	base.projects["mer"] = domain.ProjectRecord{ID: "mer", Config: testRoleAgents()}
	base.sessions["chat-session"] = domain.SessionRecord{
		ID: "chat-session", ProjectID: "mer", Kind: domain.KindWorker,
		Harness: domain.HarnessCodex, Mode: domain.SessionModeChat,
		Activity: domain.Activity{State: domain.ActivityActive},
		Metadata: domain.SessionMetadata{
			WorkspacePath: "/ws/chat-session", Branch: "ao/chat-session",
			ProviderConversationID: "native-chat", ControllerGeneration: "source-generation",
		},
	}
	journal := &collectingCodexSwitchStore{}
	store := &bootstrapOrderingStore{fakeStore: base, collectingCodexSwitchStore: journal}
	launcher := &settlingSwitchChatLauncher{recordingLauncher: &recordingLauncher{live: true}}
	manager := New(Deps{
		Store: store, Runtime: &fakeRuntime{}, Agents: fakeAgents{}, Workspace: &fakeWorkspace{}, Chat: launcher,
		Lifecycle: &fakeLCM{store: base}, DataDir: t.TempDir(), LookPath: func(string) (string, error) { return "/bin/true", nil },
		NewLaunchID: func() string { return "target-generation" },
	})
	sessions := []domain.CodexAccountSwitchSession{{
		SessionID: "chat-session", InterfaceMode: domain.SessionModeChat, WasRunning: true,
		NativeSessionID: "native-chat", SourceGeneration: "source-generation",
		StopState: "pending", RestartState: "pending", ReviewerStopState: "skipped", ReviewerRestartState: "skipped",
	}}

	if err := manager.prepareCodexSwitchChatInterrupt(context.Background(), sessions); err != nil {
		t.Fatalf("prepare interrupt: %v", err)
	}
	if err := manager.stopCodexSwitchSessions(context.Background(), store, "switch-1", sessions); err != nil {
		t.Fatalf("stop active Chat controller: %v", err)
	}
	if err := manager.restartCodexSwitchSessions(context.Background(), store, "switch-1", sessions); err != nil {
		t.Fatalf("restart active Chat controller: %v", err)
	}
	if launcher.attempts != 2 {
		t.Fatalf("Chat restart attempts = %d, want one bounded retry", launcher.attempts)
	}
	if sessions[0].RestartState != "restarted" {
		t.Fatalf("restart state = %q, want restarted", sessions[0].RestartState)
	}
	if got := base.sessions["chat-session"].Metadata.ProviderConversationID; got != "native-chat" {
		t.Fatalf("native Chat ID = %q, want native-chat", got)
	}
}
