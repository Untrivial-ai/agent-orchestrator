package agent

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/codexops"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type coordinatorCredentialFake struct {
	mu              sync.Mutex
	source          domain.CodexAccountSwitchSource
	installedTarget bool
	inspectionState domain.CodexAccountSwitchInstallationState
	confirmErrs     []error
	confirmStarted  chan struct{}
	activateErr     error
	calls           []string
}

func (f *coordinatorCredentialFake) call(value string) {
	f.mu.Lock()
	f.calls = append(f.calls, value)
	f.mu.Unlock()
}

func (f *coordinatorCredentialFake) WaitCodexAccountStoreReady(context.Context) error {
	f.call("store")
	return nil
}
func (f *coordinatorCredentialFake) EnsureCodexDeviceAccountReconciled(context.Context) error {
	f.call("reconcile")
	return nil
}
func (f *coordinatorCredentialFake) BeginCodexAccountMutation(context.Context) error {
	f.call("begin")
	return nil
}
func (f *coordinatorCredentialFake) EndCodexAccountMutation() { f.call("end") }
func (f *coordinatorCredentialFake) PrepareCodexAccountForSwitch(_ context.Context, switchID, targetID string) (domain.CodexAccountSwitchSource, error) {
	f.call("prepare:" + switchID + ":" + targetID)
	return f.source, nil
}
func (f *coordinatorCredentialFake) InspectCodexAccountSwitch(_ context.Context, switchID string, _ domain.CodexAccountSwitchSourceKind, _, targetID string) (domain.CodexAccountSwitchInstallationState, error) {
	f.call("confirm:" + switchID + ":" + targetID)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.confirmStarted != nil {
		select {
		case f.confirmStarted <- struct{}{}:
		default:
		}
	}
	if len(f.confirmErrs) > 0 {
		err := f.confirmErrs[0]
		f.confirmErrs = f.confirmErrs[1:]
		if err != nil {
			return "", err
		}
	}
	if !f.installedTarget {
		if f.inspectionState != "" {
			return f.inspectionState, nil
		}
		return domain.CodexAccountSwitchExternalCredential, nil
	}
	return domain.CodexAccountSwitchTargetInstalled, nil
}
func (f *coordinatorCredentialFake) ActivatePreparedCodexAccountSwitch(_ context.Context, _ domain.CodexAccountSwitchSourceKind, switchID, targetID string) error {
	f.call("activate:" + switchID + ":" + targetID)
	if f.activateErr != nil {
		return f.activateErr
	}
	f.mu.Lock()
	f.installedTarget = true
	f.mu.Unlock()
	return nil
}
func (f *coordinatorCredentialFake) CleanupCodexAccountSwitch(_ context.Context, switchID string) error {
	f.call("cleanup:" + switchID)
	return nil
}
func (f *coordinatorCredentialFake) CleanupInactiveCodexAccountSwitches(_ context.Context, activeSwitchID string) error {
	f.call("cleanup-inactive:" + activeSwitchID)
	return nil
}

type coordinatorSwitchStoreFake struct {
	mu      sync.Mutex
	record  domain.CodexAccountSwitch
	readErr error
}

func (s *coordinatorSwitchStoreFake) CreateCodexAccountSwitch(_ context.Context, record domain.CodexAccountSwitch) (domain.CodexAccountSwitch, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.record.ID != "" {
		return s.record, false, nil
	}
	s.record = record
	return record, true, nil
}
func (s *coordinatorSwitchStoreFake) GetCodexAccountSwitch(_ context.Context, id string) (domain.CodexAccountSwitch, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.record, s.record.ID == id, nil
}
func (s *coordinatorSwitchStoreFake) GetCodexAccountSwitchByIdempotency(_ context.Context, key string) (domain.CodexAccountSwitch, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.record, s.record.IdempotencyKey == key && key != "", nil
}
func (s *coordinatorSwitchStoreFake) GetActiveCodexAccountSwitch(context.Context) (domain.CodexAccountSwitch, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.readErr != nil {
		return domain.CodexAccountSwitch{}, false, s.readErr
	}
	return s.record, s.record.ID != "" && !s.record.Phase.Terminal(), nil
}
func (s *coordinatorSwitchStoreFake) UpdateCodexAccountSwitch(_ context.Context, record domain.CodexAccountSwitch, expected domain.CodexAccountSwitchPhase) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.record.ID != "" && s.record.Phase != expected {
		return false, nil
	}
	s.record = record
	return true, nil
}

func TestCodexAccountSwitchCoordinatorCompletesLocalCredentialSwitch(t *testing.T) {
	credentials := &coordinatorCredentialFake{source: domain.CodexAccountSwitchSource{Kind: domain.CodexAccountSwitchSourceManaged, AccountID: "source"}}
	store := &coordinatorSwitchStoreFake{}
	coordinator := newCodexAccountSwitchCoordinator(context.Background(), credentials, store, codexops.NewGate(), time.Now, nil)

	if _, err := coordinator.StartCodexAccountSwitch(context.Background(), ports.CodexAccountSwitchConfig{TargetAccountID: "target", IdempotencyKey: "request-1"}); err != nil {
		t.Fatal(err)
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := coordinator.Wait(waitCtx); err != nil {
		t.Fatal(err)
	}

	store.mu.Lock()
	completed := store.record
	store.mu.Unlock()
	if completed.Phase != domain.CodexAccountSwitchCompleted {
		t.Fatalf("phase = %q, want completed", completed.Phase)
	}
	credentials.mu.Lock()
	calls := append([]string(nil), credentials.calls...)
	credentials.mu.Unlock()
	for _, prefix := range []string{"store", "begin", "prepare:", "activate:", "confirm:", "cleanup:", "end", "reconcile"} {
		if !slices.ContainsFunc(calls, func(call string) bool { return strings.HasPrefix(call, prefix) }) {
			t.Fatalf("calls = %v, missing %q", calls, prefix)
		}
	}
}

func TestSwitchAdmissionCancelledBeforeWorkerLeavesNoPendingJournal(t *testing.T) {
	credentials := &coordinatorCredentialFake{source: domain.CodexAccountSwitchSource{Kind: domain.CodexAccountSwitchSourceManaged, AccountID: "source"}}
	store := &coordinatorSwitchStoreFake{}
	coordinator := newCodexAccountSwitchCoordinator(context.Background(), credentials, store, codexops.NewGate(), time.Now, nil)
	if err := coordinator.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}

	_, err := coordinator.StartCodexAccountSwitch(context.Background(), ports.CodexAccountSwitchConfig{TargetAccountID: "target", IdempotencyKey: "request-cancelled"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context canceled", err)
	}
	store.mu.Lock()
	settled := store.record
	store.mu.Unlock()
	if settled.Phase != domain.CodexAccountSwitchFailed || settled.FailureCode != "switch_cancelled_before_mutation" {
		t.Fatalf("switch = (%q,%q), want terminal pre-mutation cancellation", settled.Phase, settled.FailureCode)
	}
	credentials.mu.Lock()
	calls := append([]string(nil), credentials.calls...)
	credentials.mu.Unlock()
	if slices.ContainsFunc(calls, func(call string) bool { return strings.HasPrefix(call, "activate:") }) {
		t.Fatalf("cancelled admission mutated credentials: %v", calls)
	}
	if !slices.ContainsFunc(calls, func(call string) bool { return strings.HasPrefix(call, "cleanup:") }) {
		t.Fatalf("cancelled admission did not clean staging: %v", calls)
	}
}

func TestInterruptedSwitchWithInstalledTargetCompletesLocally(t *testing.T) {
	credentials := &coordinatorCredentialFake{installedTarget: true}
	store := &coordinatorSwitchStoreFake{record: domain.CodexAccountSwitch{
		ID: "switch-1", SourceKind: domain.CodexAccountSwitchSourceManaged, SourceAccountID: "source",
		TargetAccountID: "target", Phase: domain.CodexAccountSwitchActivatingAccount,
	}}
	coordinator := newCodexAccountSwitchCoordinator(context.Background(), credentials, store, codexops.NewGate(), time.Now, nil)
	if err := coordinator.ReconcileCodexAccountSwitches(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := coordinator.Wait(waitCtx); err != nil {
		t.Fatal(err)
	}
	if store.record.Phase != domain.CodexAccountSwitchCompleted {
		t.Fatalf("phase = %q, want completed", store.record.Phase)
	}
}

func TestInterruptedSwitchConclusiveNonTargetStatesCancelWithoutWriting(t *testing.T) {
	for _, state := range []domain.CodexAccountSwitchInstallationState{
		domain.CodexAccountSwitchSourceInstalled,
		domain.CodexAccountSwitchCredentialMissing,
		domain.CodexAccountSwitchExternalCredential,
	} {
		t.Run(string(state), func(t *testing.T) {
			credentials := &coordinatorCredentialFake{inspectionState: state}
			store := &coordinatorSwitchStoreFake{record: domain.CodexAccountSwitch{
				ID: "switch-1", SourceKind: domain.CodexAccountSwitchSourceDevice,
				TargetAccountID: "target", Phase: domain.CodexAccountSwitchRequested,
			}}
			coordinator := newCodexAccountSwitchCoordinator(context.Background(), credentials, store, codexops.NewGate(), time.Now, nil)
			if err := coordinator.ReconcileCodexAccountSwitches(context.Background()); err != nil {
				t.Fatal(err)
			}
			waitCtx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := coordinator.Wait(waitCtx); err != nil {
				t.Fatal(err)
			}
			if store.record.Phase != domain.CodexAccountSwitchFailed || store.record.FailureCode != "interrupted_switch_cancelled" {
				t.Fatalf("switch = (%q,%q), want terminal cancellation", store.record.Phase, store.record.FailureCode)
			}
			credentials.mu.Lock()
			defer credentials.mu.Unlock()
			if slices.ContainsFunc(credentials.calls, func(call string) bool { return strings.HasPrefix(call, "activate:") }) {
				t.Fatalf("recovery resumed credential mutation: %v", credentials.calls)
			}
		})
	}
}

func TestInterruptedSwitchRetainsFenceAcrossTransientInspectionFailure(t *testing.T) {
	transient := errors.New("temporary credential read failure")
	credentials := &coordinatorCredentialFake{
		installedTarget: true,
		confirmErrs:     []error{transient, nil},
		confirmStarted:  make(chan struct{}, 2),
	}
	store := &coordinatorSwitchStoreFake{record: domain.CodexAccountSwitch{
		ID: "switch-1", SourceKind: domain.CodexAccountSwitchSourceManaged, SourceAccountID: "source",
		TargetAccountID: "target", Phase: domain.CodexAccountSwitchActivatingAccount,
	}}
	gate := codexops.NewGate()
	coordinator := newCodexAccountSwitchCoordinator(context.Background(), credentials, store, gate, time.Now, nil)
	if err := coordinator.ReconcileCodexAccountSwitches(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-credentials.confirmStarted:
	case <-time.After(time.Second):
		t.Fatal("first credential inspection did not run")
	}
	if !gate.ExclusivePendingOrHeld() {
		t.Fatal("transient inspection released the Codex operation fence")
	}
	store.mu.Lock()
	phaseAfterTransient := store.record.Phase
	store.mu.Unlock()
	if phaseAfterTransient.Terminal() {
		t.Fatalf("transient inspection terminally settled switch as %q", phaseAfterTransient)
	}

	waitCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := coordinator.Wait(waitCtx); err != nil {
		t.Fatal(err)
	}
	if store.record.Phase != domain.CodexAccountSwitchCompleted {
		t.Fatalf("phase = %q, want completed after retry", store.record.Phase)
	}
}

func TestRecoveryReadFailureDoesNotClaimDaemonBlockingRecovery(t *testing.T) {
	credentials := &coordinatorCredentialFake{}
	store := &coordinatorSwitchStoreFake{readErr: errors.New("database busy")}
	ctx, cancel := context.WithCancel(context.Background())
	gate := codexops.NewGate()
	coordinator := newCodexAccountSwitchCoordinator(ctx, credentials, store, gate, time.Now, nil)
	if err := coordinator.ReconcileCodexAccountSwitches(context.Background()); err == nil {
		t.Fatal("expected the initial diagnostic error")
	}
	if !gate.ExclusivePendingOrHeld() {
		t.Fatal("startup recovery read failure left Codex launches unfenced")
	}
	cancel()
	waitCtx, waitCancel := context.WithTimeout(context.Background(), time.Second)
	defer waitCancel()
	if err := coordinator.Wait(waitCtx); err != nil {
		t.Fatal(err)
	}
	if gate.ExclusivePendingOrHeld() {
		t.Fatal("shutdown did not release the startup recovery fence")
	}
}

func TestRecoveryWithoutPendingJournalReleasesStartupFence(t *testing.T) {
	credentials := &coordinatorCredentialFake{}
	store := &coordinatorSwitchStoreFake{}
	gate := codexops.NewGate()
	coordinator := newCodexAccountSwitchCoordinator(context.Background(), credentials, store, gate, time.Now, nil)

	if err := coordinator.ReconcileCodexAccountSwitches(context.Background()); err != nil {
		t.Fatal(err)
	}
	if gate.ExclusivePendingOrHeld() {
		t.Fatal("no pending journal left the startup fence held")
	}
	credentials.mu.Lock()
	defer credentials.mu.Unlock()
	if !slices.Contains(credentials.calls, "cleanup-inactive:") {
		t.Fatalf("inactive staging was not cleaned: %v", credentials.calls)
	}
}
