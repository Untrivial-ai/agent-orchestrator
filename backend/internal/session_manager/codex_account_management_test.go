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

type blockingActivationCredentials struct {
	bootstrapOrderingCredentials
	muCurrent          sync.Mutex
	current            domain.CodexActiveAccount
	activationEntered  chan struct{}
	activationReleased chan struct{}
}

func (c *blockingActivationCredentials) CurrentCodexActiveAccount() domain.CodexActiveAccount {
	c.muCurrent.Lock()
	defer c.muCurrent.Unlock()
	return c.current
}

func (c *blockingActivationCredentials) CheckpointAndActivateCodexAccount(ctx context.Context, _ domain.CodexAccountSwitchSourceKind, _ string, target string, expected int64) (domain.CodexActiveAccount, error) {
	close(c.activationEntered)
	select {
	case <-c.activationReleased:
	case <-ctx.Done():
		return domain.CodexActiveAccount{}, ctx.Err()
	}
	c.muCurrent.Lock()
	c.current = domain.CodexActiveAccount{AccountID: target, Revision: expected + 1}
	active := c.current
	c.muCurrent.Unlock()
	return active, nil
}

func (*rollbackTrackingCredentials) CheckpointAndActivateCodexAccount(context.Context, domain.CodexAccountSwitchSourceKind, string, string, int64) (domain.CodexActiveAccount, error) {
	return domain.CodexActiveAccount{}, errors.New("injected activation failure")
}

func (c *rollbackTrackingCredentials) RestoreCodexAccountCredential(_ context.Context, _ string, _ domain.CodexAccountSwitchSourceKind, sourceAccountID, _ string) error {
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

func (c *bootstrapOrderingCredentials) record(call string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, call)
}

type waitingControllerGate struct {
	entered chan struct{}
	release chan struct{}
}

func (*waitingControllerGate) AcquireShared(context.Context) (func(), error) {
	return nil, errors.New("fail-fast shared admission must not be used")
}

func (g *waitingControllerGate) AcquireSharedWait(ctx context.Context) (func(), error) {
	close(g.entered)
	select {
	case <-g.release:
		return func() {}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (*waitingControllerGate) AcquireExclusive(context.Context) (ports.CodexOperationLease, error) {
	return nil, errors.New("unexpected exclusive admission")
}

func (*waitingControllerGate) ExclusivePendingOrHeld() bool { return true }

func TestCodexControllerAdmissionWaitsOnlyForActiveDeviceGate(t *testing.T) {
	gate := &waitingControllerGate{entered: make(chan struct{}), release: make(chan struct{})}
	manager := New(Deps{CodexOperationGate: gate})

	type admissionResult struct {
		release func()
		err     error
	}
	done := make(chan admissionResult, 1)
	go func() {
		release, acquireErr := manager.acquireCodexControllerAdmission(context.Background(), domain.HarnessCodex)
		done <- admissionResult{release: release, err: acquireErr}
	}()
	<-gate.entered

	select {
	case result := <-done:
		if result.release != nil {
			result.release()
		}
		t.Fatalf("Codex controller admission completed during bootstrap: %v", result.err)
	default:
	}

	close(gate.release)
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
func (c *bootstrapOrderingCredentials) WaitCodexAccountStoreReady(context.Context) error {
	c.record("store")
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.bootstrapped {
		return nil
	}
	if c.mutationHeld {
		return errors.New("account store initialization blocked by held mutation token")
	}
	c.bootstrapped = true
	return nil
}
func (c *bootstrapOrderingCredentials) EnsureCodexDeviceAccountReconciled(context.Context) error {
	c.record("reconcile")
	c.mu.Lock()
	held := c.mutationHeld
	c.mu.Unlock()
	if held {
		return errors.New("device reconciliation started after mutation token")
	}
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
func (*bootstrapOrderingCredentials) CurrentCodexAccountSwitchSource() domain.CodexAccountSwitchSource {
	return domain.CodexAccountSwitchSource{Kind: domain.CodexAccountSwitchSourceManaged, AccountID: "source", Revision: 1}
}
func (*bootstrapOrderingCredentials) CodexAccountLoginInProgress() bool { return false }
func (c *bootstrapOrderingCredentials) VerifyCodexAccountForSwitch(_ context.Context, _ string) error {
	c.record("verify")
	c.mu.Lock()
	held := c.mutationHeld
	c.mu.Unlock()
	if !held {
		return errors.New("target verification ran outside mutation token")
	}
	return nil
}
func (c *bootstrapOrderingCredentials) VerifyCurrentCodexAccount(_ context.Context, accountID string) error {
	c.record("verify-current:" + accountID)
	return nil
}
func (*bootstrapOrderingCredentials) CheckpointAndActivateCodexAccount(context.Context, domain.CodexAccountSwitchSourceKind, string, string, int64) (domain.CodexActiveAccount, error) {
	return domain.CodexActiveAccount{AccountID: "target", Revision: 2}, nil
}
func (*bootstrapOrderingCredentials) RestoreCodexAccountCredential(context.Context, string, domain.CodexAccountSwitchSourceKind, string, string) error {
	return nil
}
func (*bootstrapOrderingCredentials) CleanupCodexAccountSwitch(context.Context, string) error {
	return nil
}

type bootstrapOrderingStore struct {
	*fakeStore
	*collectingCodexSwitchStore
}

type collectingCodexSwitchStore struct {
	switchRecord domain.CodexAccountSwitch
	active       bool
}

func (s *collectingCodexSwitchStore) CreateCodexAccountSwitch(_ context.Context, rec domain.CodexAccountSwitch) (domain.CodexAccountSwitch, bool, error) {
	s.switchRecord = rec
	s.active = !rec.Phase.Terminal()
	return rec, true, nil
}
func (s *collectingCodexSwitchStore) GetCodexAccountSwitch(_ context.Context, id string) (domain.CodexAccountSwitch, bool, error) {
	return s.switchRecord, s.switchRecord.ID == id && id != "", nil
}
func (s *collectingCodexSwitchStore) GetCodexAccountSwitchByIdempotency(_ context.Context, key string) (domain.CodexAccountSwitch, bool, error) {
	return s.switchRecord, s.switchRecord.IdempotencyKey == key && key != "", nil
}
func (s *collectingCodexSwitchStore) GetActiveCodexAccountSwitch(context.Context) (domain.CodexAccountSwitch, bool, error) {
	return s.switchRecord, s.active, nil
}
func (s *collectingCodexSwitchStore) UpdateCodexAccountSwitch(_ context.Context, rec domain.CodexAccountSwitch, expected domain.CodexAccountSwitchPhase) (bool, error) {
	if s.switchRecord.ID != "" && s.switchRecord.Phase != expected {
		return false, nil
	}
	s.switchRecord = rec
	s.active = !rec.Phase.Terminal()
	return true, nil
}

type ambiguousCodexSwitchStore struct {
	*fakeStore
	switchRecord domain.CodexAccountSwitch
}

func (s *ambiguousCodexSwitchStore) CreateCodexAccountSwitch(_ context.Context, rec domain.CodexAccountSwitch) (domain.CodexAccountSwitch, bool, error) {
	s.switchRecord = rec
	return rec, true, nil
}
func (s *ambiguousCodexSwitchStore) GetCodexAccountSwitch(context.Context, string) (domain.CodexAccountSwitch, bool, error) {
	return s.switchRecord, true, nil
}
func (s *ambiguousCodexSwitchStore) GetCodexAccountSwitchByIdempotency(_ context.Context, key string) (domain.CodexAccountSwitch, bool, error) {
	if s.switchRecord.IdempotencyKey == key && key != "" {
		return s.switchRecord, true, nil
	}
	return domain.CodexAccountSwitch{}, false, nil
}
func (s *ambiguousCodexSwitchStore) GetActiveCodexAccountSwitch(context.Context) (domain.CodexAccountSwitch, bool, error) {
	return s.switchRecord, !s.switchRecord.Phase.Terminal(), nil
}
func (s *ambiguousCodexSwitchStore) UpdateCodexAccountSwitch(_ context.Context, rec domain.CodexAccountSwitch, _ domain.CodexAccountSwitchPhase) (bool, error) {
	s.switchRecord = rec
	return false, errors.New("injected post-commit switch error")
}

func TestCodexAccountSwitchFingerprintIsVersionedAndStable(t *testing.T) {
	t.Parallel()
	first := codexAccountSwitchFingerprint("account-b", 7, false)
	if !strings.HasPrefix(first, "v4:") || len(first) != len("v4:")+64 {
		t.Fatalf("fingerprint = %q", first)
	}
	if got := codexAccountSwitchFingerprint("account-b", 7, false); got != first {
		t.Fatalf("stable fingerprint = %q, want %q", got, first)
	}
	if got := codexAccountSwitchFingerprint("account-c", 7, false); got == first {
		t.Fatal("target account must participate in fingerprint")
	}
	if got := codexAccountSwitchFingerprint("account-b", 8, false); got == first {
		t.Fatal("account revision must participate in fingerprint")
	}
	if got := codexAccountSwitchFingerprint("account-b", 7, true); got == first {
		t.Fatal("idle restart preference must participate in fingerprint")
	}
}

func TestCodexAccountSwitchRequestMatchesHistoricalFingerprints(t *testing.T) {
	t.Parallel()
	base := domain.CodexAccountSwitch{TargetAccountID: "target", ExpectedAccountRevision: 7}
	for _, tt := range []struct {
		name        string
		fingerprint string
		stored      bool
		requested   bool
		want        bool
	}{
		{name: "v2 restart off", fingerprint: "v2:legacy", want: true},
		{name: "v2 restart on", fingerprint: "v2:legacy", stored: true, requested: true, want: true},
		{name: "v2 changed option", fingerprint: "v2:legacy", stored: true, want: false},
		{name: "v3 default off", fingerprint: "v3:credential-only", want: true},
		{name: "v3 rejects new option", fingerprint: "v3:credential-only", requested: true, want: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			existing := base
			existing.RequestFingerprint = tt.fingerprint
			existing.RestartIdleSessions = tt.stored
			if got := codexAccountSwitchRequestMatches(existing, "target", 7, tt.requested); got != tt.want {
				t.Fatalf("match = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCodexAccountSwitchIdempotencyConflictsWhenRequestChanges(t *testing.T) {
	store := &ambiguousCodexSwitchStore{
		fakeStore: newFakeStore(),
		switchRecord: domain.CodexAccountSwitch{
			ID: "switch-1", IdempotencyKey: "same-request",
			RequestFingerprint: codexAccountSwitchFingerprint("different-target", 1, false),
		},
	}
	manager := New(Deps{Store: store, Runtime: &fakeRuntime{}})
	manager.SetAgentReadiness(&bootstrapOrderingCredentials{})

	_, err := manager.StartCodexAccountSwitch(context.Background(), ports.CodexAccountSwitchConfig{
		TargetAccountID: "target", ExpectedAccountRevision: 1, IdempotencyKey: "same-request",
	})
	if !errors.Is(err, ErrCodexAccountSwitchIdempotencyConflict) {
		t.Fatalf("idempotency error = %v", err)
	}
}

func TestCodexAccountSwitchReconcilesBeforeHoldingMutationToken(t *testing.T) {
	credentials := &bootstrapOrderingCredentials{}
	store := &bootstrapOrderingStore{fakeStore: newFakeStore(), collectingCodexSwitchStore: &collectingCodexSwitchStore{}}
	manager := New(Deps{Store: store, Runtime: &fakeRuntime{}})
	manager.SetAgentReadiness(credentials)

	if _, err := manager.StartCodexAccountSwitch(context.Background(), ports.CodexAccountSwitchConfig{
		TargetAccountID: "target", ExpectedAccountRevision: 1, IdempotencyKey: "bootstrap-order",
	}); err != nil {
		t.Fatalf("start switch: %v", err)
	}
	waitForCodexSwitchWorker(t, manager)
	credentials.mu.Lock()
	calls := append([]string(nil), credentials.calls...)
	credentials.mu.Unlock()
	if len(calls) < 4 || !slices.Equal(calls[:4], []string{"store", "reconcile", "begin", "verify"}) {
		t.Fatalf("admission order = %v", calls)
	}
}

func TestCodexAccountSwitchLeavesRunningControllersUntouched(t *testing.T) {
	base := newFakeStore()
	base.sessions["tui-session"] = domain.SessionRecord{
		ID: "tui-session", Harness: domain.HarnessCodex, Mode: domain.SessionModeTUI,
		Activity: domain.Activity{State: domain.ActivityActive},
		Metadata: domain.SessionMetadata{RuntimeHandleID: "tui-handle", RuntimeLaunchID: "tui-generation"},
	}
	base.sessions["chat-session"] = domain.SessionRecord{
		ID: "chat-session", Harness: domain.HarnessCodex, Mode: domain.SessionModeChat,
		Activity: domain.Activity{State: domain.ActivityActive},
		Metadata: domain.SessionMetadata{ControllerGeneration: "chat-generation"},
	}
	journal := &collectingCodexSwitchStore{}
	store := &bootstrapOrderingStore{fakeStore: base, collectingCodexSwitchStore: journal}
	credentials := &blockingActivationCredentials{
		current:            domain.CodexActiveAccount{AccountID: "source", Revision: 1},
		activationEntered:  make(chan struct{}),
		activationReleased: make(chan struct{}),
	}
	runtime := &fakeRuntime{aliveByHandle: map[string]bool{"tui-handle": true}}
	chat := &recordingLauncher{live: true}
	input := &transitionInputGate{acquired: make(chan string, 1), released: make(chan string, 1)}
	manager := New(Deps{Store: store, Runtime: runtime, Chat: chat})
	manager.SetAgentReadiness(credentials)
	manager.SetTerminalInputGate(input)

	if _, err := manager.StartCodexAccountSwitch(context.Background(), ports.CodexAccountSwitchConfig{
		TargetAccountID: "target", ExpectedAccountRevision: 1, IdempotencyKey: "leave-running", RestartIdleSessions: true,
	}); err != nil {
		t.Fatalf("start switch: %v", err)
	}
	<-credentials.activationEntered
	if len(manager.agentOperations) != 0 {
		t.Fatalf("credential switch acquired per-session operations: %#v", manager.agentOperations)
	}
	if base.listAllCalls != 0 {
		t.Fatalf("sessions listed before target activation: %d", base.listAllCalls)
	}
	select {
	case terminalID := <-input.acquired:
		t.Fatalf("credential switch froze terminal input for %q", terminalID)
	default:
	}

	admissionDone := make(chan error, 1)
	go func() {
		release, acquireErr := manager.acquireCodexControllerAdmission(context.Background(), domain.HarnessCodex)
		if release != nil {
			release()
		}
		admissionDone <- acquireErr
	}()
	select {
	case err := <-admissionDone:
		t.Fatalf("new Codex launch was admitted before credential activation completed: %v", err)
	default:
	}

	close(credentials.activationReleased)
	waitForCodexSwitchWorker(t, manager)
	select {
	case err := <-admissionDone:
		if err != nil {
			t.Fatalf("new Codex launch was not admitted after credential activation: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("new Codex launch remained fenced after switch completion")
	}

	if runtime.created != 0 || runtime.destroyed != 0 || len(runtime.interrupts) != 0 ||
		len(chat.stopped) != 0 || len(chat.started) != 0 {
		t.Fatalf("credential switch touched controllers: runtime create/destroy/interrupt=%d/%d/%d chat stop/start=%d/%d",
			runtime.created, runtime.destroyed, len(runtime.interrupts), len(chat.stopped), len(chat.started))
	}
	if got := base.sessions["tui-session"].Metadata; got.RuntimeHandleID != "tui-handle" || got.RuntimeLaunchID != "tui-generation" {
		t.Fatalf("TUI controller identity changed: %#v", got)
	}
	if got := base.sessions["chat-session"].Metadata; got.ControllerGeneration != "chat-generation" {
		t.Fatalf("Chat controller identity changed: %#v", got)
	}
	if journal.switchRecord.Phase != domain.CodexAccountSwitchCompleted {
		t.Fatalf("switch phase = %q, want completed", journal.switchRecord.Phase)
	}
	if base.listAllCalls != 1 {
		t.Fatalf("session discovery calls = %d, want one after verification", base.listAllCalls)
	}
}

func TestCodexAccountSwitchDoesNotListSessionsWhenIdleRestartIsDisabled(t *testing.T) {
	credentials := &bootstrapOrderingCredentials{}
	base := newFakeStore()
	base.listAllErr = errors.New("must not list sessions")
	store := &bootstrapOrderingStore{fakeStore: base, collectingCodexSwitchStore: &collectingCodexSwitchStore{}}
	manager := New(Deps{Store: store, Runtime: &fakeRuntime{}})
	sw := domain.CodexAccountSwitch{
		ID: "switch-1", SourceAccountID: "source", TargetAccountID: "target",
		ExpectedAccountRevision: 1, Phase: domain.CodexAccountSwitchVerifyingAccount,
	}

	manager.dispatchCodexAccountSwitch(context.Background(), credentials, store, &sw, false, func() {})
	if sw.Phase != domain.CodexAccountSwitchCompleted || sw.FailureCode != "" {
		t.Fatalf("switch = phase %q failure %q", sw.Phase, sw.FailureCode)
	}
	if base.listAllCalls != 0 {
		t.Fatalf("session discovery calls = %d, want zero", base.listAllCalls)
	}
}

func TestCodexAccountSwitchRecoveryLeavesControllersUntouched(t *testing.T) {
	base := newFakeStore()
	base.sessions["running-codex"] = domain.SessionRecord{
		ID: "running-codex", Harness: domain.HarnessCodex, Mode: domain.SessionModeTUI,
		Metadata: domain.SessionMetadata{RuntimeHandleID: "source-handle", RuntimeLaunchID: "source-generation"},
	}
	journal := &collectingCodexSwitchStore{
		switchRecord: domain.CodexAccountSwitch{
			ID: "switch-recovery", SourceAccountID: "source", TargetAccountID: "target",
			IdempotencyKey: "recover", RequestFingerprint: codexAccountSwitchFingerprint("target", 1, false),
			ExpectedAccountRevision: 1, Phase: domain.CodexAccountSwitchRecoveryRequired,
		},
		active: true,
	}
	store := &bootstrapOrderingStore{fakeStore: base, collectingCodexSwitchStore: journal}
	credentials := &blockingActivationCredentials{current: domain.CodexActiveAccount{AccountID: "target", Revision: 2}}
	runtime := &fakeRuntime{aliveByHandle: map[string]bool{"source-handle": true}}
	chat := &recordingLauncher{live: true}
	manager := New(Deps{Store: store, Runtime: runtime, Chat: chat})
	manager.SetAgentReadiness(credentials)

	if err := manager.ReconcileCodexAccountSwitches(context.Background()); err != nil {
		t.Fatalf("reconcile switch: %v", err)
	}
	waitForCodexSwitchWorker(t, manager)
	if journal.switchRecord.Phase != domain.CodexAccountSwitchCompleted {
		t.Fatalf("recovered switch phase = %q, want completed", journal.switchRecord.Phase)
	}
	if runtime.created != 0 || runtime.destroyed != 0 || runtime.outputCalls != 0 || len(runtime.interrupts) != 0 ||
		len(chat.armed) != 0 || len(chat.prepared) != 0 || len(chat.stopped) != 0 || len(chat.started) != 0 {
		t.Fatalf("credential recovery touched controllers: runtime=%d/%d/%d/%d chat=%d/%d/%d/%d",
			runtime.created, runtime.destroyed, runtime.outputCalls, len(runtime.interrupts),
			len(chat.armed), len(chat.prepared), len(chat.stopped), len(chat.started))
	}
}

func TestRetainCodexAccountSwitchFence(t *testing.T) {
	t.Parallel()
	for _, phase := range []domain.CodexAccountSwitchPhase{
		domain.CodexAccountSwitchRequested,
		legacyCodexSwitchStoppingSessions,
		legacyCodexSwitchSessionsStopped,
		domain.CodexAccountSwitchCheckpointCredential,
		domain.CodexAccountSwitchActivatingAccount,
		domain.CodexAccountSwitchVerifyingAccount,
		domain.CodexAccountSwitchRestartingSessions,
		domain.CodexAccountSwitchRollbackRequired,
		domain.CodexAccountSwitchRecoveryRequired,
	} {
		if !retainCodexAccountSwitchFence(phase) {
			t.Fatalf("phase %s must retain the fence", phase)
		}
	}
	for _, phase := range []domain.CodexAccountSwitchPhase{domain.CodexAccountSwitchCompleted, domain.CodexAccountSwitchFailed} {
		if retainCodexAccountSwitchFence(phase) {
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
		ExpectedAccountRevision: 1, RestartIdleSessions: true, Phase: domain.CodexAccountSwitchActivatingAccount,
	}

	manager.dispatchCodexAccountSwitch(context.Background(), credentials, store, &sw, false, func() {})

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
	if store.listAllCalls != 0 {
		t.Fatalf("credential rollback listed sessions %d times", store.listAllCalls)
	}
}

func TestCodexAccountSwitchRecoveryUsesVerifiedDeviceTargetInsteadOfStalePointer(t *testing.T) {
	credentials := &bootstrapOrderingCredentials{}
	store := &bootstrapOrderingStore{fakeStore: newFakeStore(), collectingCodexSwitchStore: &collectingCodexSwitchStore{}}
	manager := New(Deps{Store: store, Runtime: &fakeRuntime{}})
	sw := domain.CodexAccountSwitch{
		ID: "switch-1", SourceKind: domain.CodexAccountSwitchSourceManaged,
		SourceAccountID: "source", TargetAccountID: "target",
		ExpectedAccountRevision: 1, Phase: domain.CodexAccountSwitchRecoveryRequired,
	}

	manager.dispatchCodexAccountSwitch(context.Background(), credentials, store, &sw, false, func() {})

	if sw.Phase != domain.CodexAccountSwitchCompleted {
		t.Fatalf("phase = %q, want completed", sw.Phase)
	}
	credentials.mu.Lock()
	calls := append([]string(nil), credentials.calls...)
	credentials.mu.Unlock()
	if !slices.Contains(calls, "verify-current:target") {
		t.Fatalf("recovery calls = %v, want target verification", calls)
	}
}

func TestCodexAccountSwitchAdoptsJournalWritesReportedAsErrors(t *testing.T) {
	store := &ambiguousCodexSwitchStore{
		fakeStore:    newFakeStore(),
		switchRecord: domain.CodexAccountSwitch{ID: "switch-1", Phase: domain.CodexAccountSwitchRequested},
	}
	manager := New(Deps{Store: store, Runtime: &fakeRuntime{}})
	sw := store.switchRecord
	if err := manager.advanceCodexAccountSwitch(context.Background(), store, &sw, domain.CodexAccountSwitchCheckpointCredential, ""); err != nil {
		t.Fatalf("advance did not adopt committed phase: %v", err)
	}
	if sw.Phase != domain.CodexAccountSwitchCheckpointCredential {
		t.Fatalf("phase = %q", sw.Phase)
	}
}

func TestCodexAccountSwitchSettlesLegacySessionPhasesWithoutSessionWork(t *testing.T) {
	for _, phase := range []domain.CodexAccountSwitchPhase{
		legacyCodexSwitchStoppingSessions,
		legacyCodexSwitchSessionsStopped,
	} {
		t.Run(string(phase), func(t *testing.T) {
			credentials := &bootstrapOrderingCredentials{}
			store := &bootstrapOrderingStore{fakeStore: newFakeStore(), collectingCodexSwitchStore: &collectingCodexSwitchStore{}}
			manager := New(Deps{Store: store, Runtime: &fakeRuntime{}})
			sw := domain.CodexAccountSwitch{
				ID: "legacy", SourceAccountID: "source", TargetAccountID: "target",
				ExpectedAccountRevision: 1, Phase: phase,
			}

			manager.dispatchCodexAccountSwitch(context.Background(), credentials, store, &sw, true, func() {})
			if sw.Phase != domain.CodexAccountSwitchCompleted {
				t.Fatalf("legacy phase %q settled as %q, want completed", phase, sw.Phase)
			}
		})
	}
}

func TestCodexAccountSwitchCrashRecoverySettlesRestartWithoutReplayingBatch(t *testing.T) {
	credentials := &bootstrapOrderingCredentials{}
	base := newFakeStore()
	base.sessions["idle"] = domain.SessionRecord{
		ID: "idle", ProjectID: "mer", Kind: domain.KindWorker, Harness: domain.HarnessCodex,
		Mode: domain.SessionModeChat, Activity: domain.Activity{State: domain.ActivityIdle},
	}
	store := &bootstrapOrderingStore{fakeStore: base, collectingCodexSwitchStore: &collectingCodexSwitchStore{}}
	launcher := &recordingLauncher{}
	manager := New(Deps{Store: store, Runtime: &fakeRuntime{}, Chat: launcher})
	sw := domain.CodexAccountSwitch{
		ID: "switch-1", SourceAccountID: "source", TargetAccountID: "target",
		ExpectedAccountRevision: 1, RestartIdleSessions: true, Phase: domain.CodexAccountSwitchRestartingSessions,
	}

	manager.dispatchCodexAccountSwitch(context.Background(), credentials, store, &sw, true, func() {})
	if sw.Phase != domain.CodexAccountSwitchCompleted || sw.FailureCode != codexIdleRestartIncomplete {
		t.Fatalf("recovered switch = phase %q failure %q", sw.Phase, sw.FailureCode)
	}
	if base.listAllCalls != 0 || len(launcher.stopped) != 0 || len(launcher.started) != 0 {
		t.Fatalf("crash recovery replayed restart batch: lists=%d stopped=%v started=%v", base.listAllCalls, launcher.stopped, launcher.started)
	}
}

func TestCodexAccountSwitchSettlesLegacyStopRecoveryWithoutRollback(t *testing.T) {
	credentials := &rollbackTrackingCredentials{}
	store := &bootstrapOrderingStore{fakeStore: newFakeStore(), collectingCodexSwitchStore: &collectingCodexSwitchStore{}}
	manager := New(Deps{Store: store, Runtime: &fakeRuntime{}})
	sw := domain.CodexAccountSwitch{
		ID: "legacy", SourceAccountID: "source", TargetAccountID: "target",
		ExpectedAccountRevision: 1, Phase: domain.CodexAccountSwitchRecoveryRequired,
		FailureCode: "stop_unconfirmed",
	}

	manager.dispatchCodexAccountSwitch(context.Background(), credentials, store, &sw, false, func() {})
	if sw.Phase != domain.CodexAccountSwitchFailed {
		t.Fatalf("phase = %q, want failed", sw.Phase)
	}
	if credentials.restoreCalls != 0 {
		t.Fatalf("legacy pre-activation recovery attempted %d credential restores", credentials.restoreCalls)
	}
}

func waitForCodexSwitchWorker(t *testing.T, manager *Manager) {
	t.Helper()
	waitCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := manager.WaitAgentSwitchWorkers(waitCtx); err != nil {
		t.Fatal(err)
	}
}
