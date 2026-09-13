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
	mu          sync.Mutex
	active      domain.CodexActiveAccount
	calls       []string
	activateErr error
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
func (f *coordinatorCredentialFake) CurrentCodexActiveAccount() domain.CodexActiveAccount {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.active
}
func (f *coordinatorCredentialFake) CurrentCodexAccountSwitchSource() domain.CodexAccountSwitchSource {
	f.mu.Lock()
	defer f.mu.Unlock()
	return domain.CodexAccountSwitchSource{
		Kind: domain.CodexAccountSwitchSourceManaged, AccountID: f.active.AccountID, Revision: f.active.Revision,
	}
}
func (*coordinatorCredentialFake) CodexAccountLoginInProgress() bool { return false }
func (f *coordinatorCredentialFake) VerifyCodexAccountForSwitch(_ context.Context, id string) error {
	f.call("verify-target:" + id)
	return nil
}
func (f *coordinatorCredentialFake) VerifyCurrentCodexAccount(_ context.Context, id string) error {
	f.call("verify-current:" + id)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.active.AccountID != id {
		return errors.New("unexpected active account")
	}
	return nil
}
func (f *coordinatorCredentialFake) CheckpointAndActivateCodexAccount(_ context.Context, _ domain.CodexAccountSwitchSourceKind, _ string, target string, expected int64) (domain.CodexActiveAccount, error) {
	f.call("activate:" + target)
	if f.activateErr != nil {
		return domain.CodexActiveAccount{}, f.activateErr
	}
	f.mu.Lock()
	f.active = domain.CodexActiveAccount{AccountID: target, Revision: expected + 1}
	active := f.active
	f.mu.Unlock()
	return active, nil
}
func (f *coordinatorCredentialFake) RestoreCodexAccountCredential(_ context.Context, _ string, _ domain.CodexAccountSwitchSourceKind, source, _ string) error {
	f.call("restore:" + source)
	f.mu.Lock()
	f.active.AccountID = source
	f.mu.Unlock()
	return nil
}
func (f *coordinatorCredentialFake) CleanupCodexAccountSwitch(context.Context, string) error {
	f.call("cleanup")
	return nil
}

type coordinatorSwitchStoreFake struct {
	mu     sync.Mutex
	record domain.CodexAccountSwitch
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

func TestCodexAccountSwitchFingerprintIsVersionedAndStable(t *testing.T) {
	first := codexAccountSwitchFingerprint("account-b", 7)
	if !strings.HasPrefix(first, "v3:") || len(first) != len("v3:")+64 {
		t.Fatalf("fingerprint = %q", first)
	}
	if first != codexAccountSwitchFingerprint("account-b", 7) || first == codexAccountSwitchFingerprint("account-b", 8) {
		t.Fatal("fingerprint is not stable and revision-specific")
	}
}

func TestCodexAccountSwitchCoordinatorCompletesCredentialOnlySwitch(t *testing.T) {
	credentials := &coordinatorCredentialFake{active: domain.CodexActiveAccount{AccountID: "source", Revision: 1}}
	store := &coordinatorSwitchStoreFake{}
	coordinator := newCodexAccountSwitchCoordinator(context.Background(), credentials, store, codexops.NewGate(), time.Now, nil)

	if _, err := coordinator.StartCodexAccountSwitch(context.Background(), ports.CodexAccountSwitchConfig{
		TargetAccountID: "target", ExpectedAccountRevision: 1, IdempotencyKey: "request-1",
	}); err != nil {
		t.Fatal(err)
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := coordinator.Wait(waitCtx); err != nil {
		t.Fatal(err)
	}

	store.mu.Lock()
	phase := store.record.Phase
	store.mu.Unlock()
	if phase != domain.CodexAccountSwitchCompleted {
		t.Fatalf("phase = %q, want completed", phase)
	}
	credentials.mu.Lock()
	calls := append([]string(nil), credentials.calls...)
	credentials.mu.Unlock()
	for _, want := range []string{"store", "reconcile", "begin", "verify-target:target", "activate:target", "verify-current:target", "cleanup", "end"} {
		if !slices.Contains(calls, want) {
			t.Fatalf("calls = %v, missing %q", calls, want)
		}
	}
}

func TestCodexAccountSwitchCoordinatorRollsBackActivationFailure(t *testing.T) {
	credentials := &coordinatorCredentialFake{
		active: domain.CodexActiveAccount{AccountID: "source", Revision: 1}, activateErr: errors.New("activate failed"),
	}
	store := &coordinatorSwitchStoreFake{record: domain.CodexAccountSwitch{
		ID: "switch-1", SourceKind: domain.CodexAccountSwitchSourceManaged,
		SourceAccountID: "source", TargetAccountID: "target", ExpectedAccountRevision: 1,
		Phase: domain.CodexAccountSwitchActivatingAccount,
	}}
	coordinator := newCodexAccountSwitchCoordinator(context.Background(), credentials, store, codexops.NewGate(), time.Now, nil)
	sw := store.record
	coordinator.dispatchCodexAccountSwitch(context.Background(), credentials, store, &sw)

	if sw.Phase != domain.CodexAccountSwitchFailed || sw.FailureCode != "activation_unconfirmed" {
		t.Fatalf("switch = (%q,%q), want failed activation_unconfirmed", sw.Phase, sw.FailureCode)
	}
	credentials.mu.Lock()
	calls := append([]string(nil), credentials.calls...)
	credentials.mu.Unlock()
	if !slices.Contains(calls, "restore:source") || !slices.Contains(calls, "verify-current:source") {
		t.Fatalf("rollback calls = %v", calls)
	}
}
