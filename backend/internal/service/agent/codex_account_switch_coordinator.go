package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type codexAccountSwitchCoordinator struct {
	credentials                     ports.CodexAccountCredentialManager
	store                           ports.CodexAccountSwitchStore
	codexOperationGate              ports.CodexOperationGate
	codexAccountSwitchMu            sync.Mutex
	codexAccountSwitchWorkerRunning bool
	codexAccountSwitchLease         ports.CodexOperationLease
	backgroundContext               context.Context
	workers                         sync.WaitGroup
	workersMu                       sync.Mutex
	workersClosed                   bool
	clock                           func() time.Time
	publish                         func()
}

const codexAccountSwitchDurableBoundaryWait = 5 * time.Second

func codexAccountSwitchDurableContext(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(parent), codexAccountSwitchDurableBoundaryWait)
}

func newCodexAccountSwitchCoordinator(
	ctx context.Context,
	credentials ports.CodexAccountCredentialManager,
	store ports.CodexAccountSwitchStore,
	gate ports.CodexOperationGate,
	clock func() time.Time,
	publish func(),
) *codexAccountSwitchCoordinator {
	if ctx == nil {
		ctx = context.Background()
	}
	if clock == nil {
		clock = time.Now
	}
	return &codexAccountSwitchCoordinator{
		credentials: credentials, store: store, codexOperationGate: gate,
		backgroundContext: ctx, clock: clock, publish: publish,
	}
}

func (m *codexAccountSwitchCoordinator) publishCodexAccountSwitchChanged() {
	if m.publish != nil {
		m.publish()
	}
}

func codexAccountSwitchFingerprint(target string, revision int64) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("v3\x00%s\x00%d", target, revision)))
	return "v3:" + hex.EncodeToString(sum[:])
}

func (m *codexAccountSwitchCoordinator) codexAccountSwitchDependencies() (ports.CodexAccountCredentialManager, ports.CodexAccountSwitchStore, error) {
	credentials := m.credentials
	if credentials == nil {
		return nil, nil, errors.New("codex account credential manager is unavailable")
	}
	store := m.store
	if store == nil {
		return nil, nil, errors.New("codex account switch store is unavailable")
	}
	return credentials, store, nil
}

func (m *codexAccountSwitchCoordinator) acquireCodexAccountSwitchGate(ctx context.Context) error {
	lease, err := m.codexOperationGate.AcquireExclusive(ctx)
	if err != nil {
		return err
	}
	m.codexAccountSwitchMu.Lock()
	defer m.codexAccountSwitchMu.Unlock()
	if m.codexAccountSwitchWorkerRunning || m.codexAccountSwitchLease != nil {
		lease.Release()
		return ports.ErrCodexAccountSwitchInProgress
	}
	m.codexAccountSwitchLease = lease
	m.codexAccountSwitchWorkerRunning = true
	return nil
}

// claimCodexAccountSwitchRecoveryWorker starts one recovery worker while the
// durable global mutation fence remains active. Recovery-required operations
// intentionally retain that fence between HTTP requests so no new Codex
// process can start against an ambiguous runtime credential.
func (m *codexAccountSwitchCoordinator) claimCodexAccountSwitchRecoveryWorker(ctx context.Context) bool {
	m.codexAccountSwitchMu.Lock()
	if m.codexAccountSwitchWorkerRunning {
		m.codexAccountSwitchMu.Unlock()
		return false
	}
	if m.codexAccountSwitchLease != nil {
		m.codexAccountSwitchWorkerRunning = true
		m.codexAccountSwitchMu.Unlock()
		return true
	}
	m.codexAccountSwitchMu.Unlock()
	lease, err := m.codexOperationGate.AcquireExclusive(ctx)
	if err != nil {
		return false
	}
	m.codexAccountSwitchMu.Lock()
	defer m.codexAccountSwitchMu.Unlock()
	if m.codexAccountSwitchWorkerRunning || m.codexAccountSwitchLease != nil {
		lease.Release()
		return false
	}
	m.codexAccountSwitchLease = lease
	m.codexAccountSwitchWorkerRunning = true
	return true
}

func (m *codexAccountSwitchCoordinator) finishCodexAccountSwitchWorker(keepFence bool) {
	m.codexAccountSwitchMu.Lock()
	m.codexAccountSwitchWorkerRunning = false
	var release ports.CodexOperationLease
	if !keepFence {
		release = m.codexAccountSwitchLease
		m.codexAccountSwitchLease = nil
	}
	m.codexAccountSwitchMu.Unlock()
	if release != nil {
		release.Release()
	}
	if keepFence {
		m.publishCodexAccountSwitchChanged()
	}
}

func (m *codexAccountSwitchCoordinator) codexAccountSwitchWorkerActive() bool {
	m.codexAccountSwitchMu.Lock()
	defer m.codexAccountSwitchMu.Unlock()
	return m.codexAccountSwitchWorkerRunning
}

func (m *codexAccountSwitchCoordinator) finishCodexAccountSwitchMutation(credentials ports.CodexAccountCredentialManager, keepFence bool) {
	m.finishCodexAccountSwitchWorker(keepFence)
	if !keepFence {
		credentials.EndCodexAccountMutation()
	}
}

func (m *codexAccountSwitchCoordinator) codexAccountSwitchIsActive() bool {
	return m.codexOperationGate != nil && m.codexOperationGate.ExclusivePendingOrHeld()
}

// CodexAccountSwitchInProgress is the daemon-wide credential admission fence.
func (m *codexAccountSwitchCoordinator) CodexAccountSwitchInProgress() bool {
	return m.codexAccountSwitchIsActive()
}

// StartCodexAccountSwitch admits and starts one account-service-owned global switch.
// Existing controllers are deliberately outside this transaction: the operation
// changes and verifies the device credential only.
func (m *codexAccountSwitchCoordinator) StartCodexAccountSwitch(ctx context.Context, cfg ports.CodexAccountSwitchConfig) (domain.CodexAccountSwitch, error) {
	cfg.TargetAccountID = strings.TrimSpace(cfg.TargetAccountID)
	cfg.IdempotencyKey = strings.TrimSpace(cfg.IdempotencyKey)
	if cfg.IdempotencyKey == "" {
		return domain.CodexAccountSwitch{}, errors.New("idempotency key is required")
	}
	credentials, store, err := m.codexAccountSwitchDependencies()
	if err != nil {
		return domain.CodexAccountSwitch{}, err
	}
	fingerprint := codexAccountSwitchFingerprint(cfg.TargetAccountID, cfg.ExpectedAccountRevision)
	if existing, ok, readErr := store.GetCodexAccountSwitchByIdempotency(ctx, cfg.IdempotencyKey); readErr != nil {
		return domain.CodexAccountSwitch{}, readErr
	} else if ok {
		if existing.RequestFingerprint != fingerprint {
			return existing, ports.ErrCodexAccountSwitchIdempotencyConflict
		}
		return m.decorateCodexAccountSwitch(existing), nil
	}
	if _, active, readErr := store.GetActiveCodexAccountSwitch(ctx); readErr != nil {
		return domain.CodexAccountSwitch{}, readErr
	} else if active {
		return domain.CodexAccountSwitch{}, ports.ErrCodexAccountSwitchInProgress
	}
	if err := credentials.WaitCodexAccountStoreReady(ctx); err != nil {
		return domain.CodexAccountSwitch{}, err
	}
	// Device-global mutation remains fail-closed. Reconcile immediately before
	// taking the durable switch admission fences, then revalidate again inside
	// the activation transaction. A temporary provider failure may leave a
	// safely observed device-only source, which is still a valid switch source.
	_ = credentials.EnsureCodexDeviceAccountReconciled(ctx)
	if err := m.acquireCodexAccountSwitchGate(ctx); err != nil {
		return domain.CodexAccountSwitch{}, err
	}
	releaseSwitchGate := true
	defer func() {
		if releaseSwitchGate {
			m.finishCodexAccountSwitchWorker(false)
		}
	}()
	if err := credentials.BeginCodexAccountMutation(ctx); err != nil {
		return domain.CodexAccountSwitch{}, err
	}
	releaseMutation := true
	defer func() {
		if releaseMutation {
			credentials.EndCodexAccountMutation()
		}
	}()

	source := credentials.CurrentCodexAccountSwitchSource()
	if source.Kind == "" {
		source.Kind = domain.CodexAccountSwitchSourceManaged
	}
	if source.Kind == domain.CodexAccountSwitchSourceManaged && source.AccountID == cfg.TargetAccountID {
		return domain.CodexAccountSwitch{}, ports.ErrCodexAccountAlreadyActive
	}
	if source.Revision != cfg.ExpectedAccountRevision {
		return domain.CodexAccountSwitch{}, ports.ErrCodexAccountRevisionConflict
	}
	if err := credentials.VerifyCodexAccountForSwitch(ctx, cfg.TargetAccountID); err != nil {
		return domain.CodexAccountSwitch{}, err
	}
	if credentials.CodexAccountLoginInProgress() {
		return domain.CodexAccountSwitch{}, ports.ErrCodexAccountLoginInProgress
	}

	now := m.clock()
	sw := domain.CodexAccountSwitch{
		ID: uuid.NewString(), SourceKind: source.Kind, SourceAccountID: source.AccountID,
		TargetAccountID: cfg.TargetAccountID, Phase: domain.CodexAccountSwitchRequested,
		IdempotencyKey: cfg.IdempotencyKey, RequestFingerprint: fingerprint,
		ExpectedAccountRevision: cfg.ExpectedAccountRevision, CreatedAt: now, UpdatedAt: now,
	}
	created, inserted, err := store.CreateCodexAccountSwitch(ctx, sw)
	if err != nil {
		return domain.CodexAccountSwitch{}, err
	}
	sw = created
	if !inserted {
		return m.decorateCodexAccountSwitch(sw), nil
	}

	if !m.startWorker(func() {
		m.runCodexAccountSwitch(m.backgroundContext, credentials, store, sw)
	}) {
		return sw, context.Canceled
	}
	releaseMutation = false
	releaseSwitchGate = false
	return sw, nil
}

func (m *codexAccountSwitchCoordinator) runCodexAccountSwitch(ctx context.Context, credentials ports.CodexAccountCredentialManager, store ports.CodexAccountSwitchStore, sw domain.CodexAccountSwitch) {
	defer func() {
		m.finishCodexAccountSwitchMutation(credentials, retainCodexAccountSwitchFence(sw.Phase))
	}()
	m.dispatchCodexAccountSwitch(ctx, credentials, store, &sw)
}

func (m *codexAccountSwitchCoordinator) dispatchCodexAccountSwitch(ctx context.Context, credentials ports.CodexAccountCredentialManager, store ports.CodexAccountSwitchStore, sw *domain.CodexAccountSwitch) {
	// Switches created before source_kind was introduced are managed-account
	// switches. Keep that compatibility at the credential coordinator boundary.
	if sw.SourceKind == "" {
		sw.SourceKind = domain.CodexAccountSwitchSourceManaged
	}
	for {
		switch sw.Phase {
		case domain.CodexAccountSwitchRequested:
			if m.advanceCodexAccountSwitch(ctx, store, sw, domain.CodexAccountSwitchCheckpointCredential, "") != nil {
				return
			}
		case domain.CodexAccountSwitchCheckpointCredential:
			if m.advanceCodexAccountSwitch(ctx, store, sw, domain.CodexAccountSwitchActivatingAccount, "") != nil {
				return
			}
		case domain.CodexAccountSwitchActivatingAccount:
			active := credentials.CurrentCodexActiveAccount()
			if active.AccountID != sw.TargetAccountID {
				if active.Revision != sw.ExpectedAccountRevision ||
					(sw.SourceKind == domain.CodexAccountSwitchSourceManaged && active.AccountID != sw.SourceAccountID) {
					_ = m.advanceCodexAccountSwitch(ctx, store, sw, domain.CodexAccountSwitchRecoveryRequired, "activation_unconfirmed")
					return
				}
				if _, err := credentials.CheckpointAndActivateCodexAccount(
					ctx, sw.SourceKind, sw.ID, sw.TargetAccountID, sw.ExpectedAccountRevision,
				); err != nil {
					if m.advanceCodexAccountSwitch(ctx, store, sw, domain.CodexAccountSwitchRollbackRequired, "activation_unconfirmed") != nil {
						return
					}
					continue
				}
			}
			committedAt := m.clock()
			sw.CredentialsCommittedAt = &committedAt
			if m.advanceCodexAccountSwitch(ctx, store, sw, domain.CodexAccountSwitchVerifyingAccount, "") != nil {
				return
			}
		case domain.CodexAccountSwitchVerifyingAccount:
			if err := credentials.VerifyCurrentCodexAccount(ctx, sw.TargetAccountID); err != nil {
				_ = m.advanceCodexAccountSwitch(ctx, store, sw, domain.CodexAccountSwitchRecoveryRequired, "target_verification_unconfirmed")
				return
			}
			if sw.CredentialsCommittedAt == nil {
				committedAt := m.clock()
				sw.CredentialsCommittedAt = &committedAt
			}
			m.completeCodexAccountSwitch(ctx, credentials, store, sw)
			return
		case domain.CodexAccountSwitchRollbackRequired:
			if err := credentials.RestoreCodexAccountCredential(
				ctx, sw.ID, sw.SourceKind, sw.SourceAccountID, sw.TargetAccountID,
			); err != nil {
				_ = m.advanceCodexAccountSwitch(ctx, store, sw, domain.CodexAccountSwitchRecoveryRequired, "rollback_unconfirmed")
				return
			}
			if sw.SourceKind == domain.CodexAccountSwitchSourceManaged {
				if err := credentials.VerifyCurrentCodexAccount(ctx, sw.SourceAccountID); err != nil {
					_ = m.advanceCodexAccountSwitch(ctx, store, sw, domain.CodexAccountSwitchRecoveryRequired, "rollback_unconfirmed")
					return
				}
			}
			m.failAndCleanupCodexAccountSwitch(ctx, credentials, store, sw)
			return
		case domain.CodexAccountSwitchRecoveryRequired:
			// Device credential truth decides ambiguous activation recovery.
			if err := credentials.VerifyCurrentCodexAccount(ctx, sw.TargetAccountID); err == nil {
				if sw.CredentialsCommittedAt == nil {
					committedAt := m.clock()
					sw.CredentialsCommittedAt = &committedAt
				}
				m.completeCodexAccountSwitch(ctx, credentials, store, sw)
				return
			}
			if m.advanceCodexAccountSwitch(ctx, store, sw, domain.CodexAccountSwitchRollbackRequired, sw.FailureCode) != nil {
				return
			}
		case domain.CodexAccountSwitchCompleted, domain.CodexAccountSwitchFailed:
			return
		default:
			_ = m.advanceCodexAccountSwitch(ctx, store, sw, domain.CodexAccountSwitchRecoveryRequired, "switch_state_unavailable")
			return
		}
	}
}

func (m *codexAccountSwitchCoordinator) completeCodexAccountSwitch(ctx context.Context, credentials ports.CodexAccountCredentialManager, store ports.CodexAccountSwitchStore, sw *domain.CodexAccountSwitch) {
	completed := m.clock()
	sw.CompletedAt = &completed
	if m.advanceCodexAccountSwitch(ctx, store, sw, domain.CodexAccountSwitchCompleted, "") == nil {
		_ = credentials.CleanupCodexAccountSwitch(ctx, sw.ID)
	}
}

func (m *codexAccountSwitchCoordinator) failAndCleanupCodexAccountSwitch(ctx context.Context, credentials ports.CodexAccountCredentialManager, store ports.CodexAccountSwitchStore, sw *domain.CodexAccountSwitch) {
	completed := m.clock()
	sw.CompletedAt = &completed
	if m.advanceCodexAccountSwitch(ctx, store, sw, domain.CodexAccountSwitchFailed, sw.FailureCode) == nil {
		_ = credentials.CleanupCodexAccountSwitch(ctx, sw.ID)
	}
}

func retainCodexAccountSwitchFence(phase domain.CodexAccountSwitchPhase) bool {
	return !phase.Terminal()
}

func (m *codexAccountSwitchCoordinator) advanceCodexAccountSwitch(ctx context.Context, store ports.CodexAccountSwitchStore, sw *domain.CodexAccountSwitch, next domain.CodexAccountSwitchPhase, code string) error {
	expected := sw.Phase
	candidate := *sw
	candidate.Phase, candidate.FailureCode, candidate.UpdatedAt = next, code, m.clock()
	candidate.CanRecover = next == domain.CodexAccountSwitchRecoveryRequired
	ok, err := store.UpdateCodexAccountSwitch(ctx, candidate, expected)
	if err == nil && ok {
		*sw = candidate
		m.publishCodexAccountSwitchChanged()
		return nil
	}
	settleCtx, cancel := codexAccountSwitchDurableContext(ctx)
	defer cancel()
	current, found, readErr := store.GetCodexAccountSwitch(settleCtx, sw.ID)
	if readErr != nil {
		return errors.Join(err, readErr)
	}
	if found && current.Phase == candidate.Phase && current.FailureCode == candidate.FailureCode {
		*sw = current
		sw.CanRecover = false
		return nil
	}
	if found {
		*sw = current
	}
	if err != nil {
		return err
	}
	return errors.New("codex account switch changed concurrently")
}

func (m *codexAccountSwitchCoordinator) decorateCodexAccountSwitch(sw domain.CodexAccountSwitch) domain.CodexAccountSwitch {
	sw.CanRecover = !sw.Phase.Terminal() && !m.codexAccountSwitchWorkerActive()
	return sw
}

func (m *codexAccountSwitchCoordinator) getCodexAccountSwitch(ctx context.Context, id string) (domain.CodexAccountSwitch, error) {
	_, store, err := m.codexAccountSwitchDependencies()
	if err != nil {
		return domain.CodexAccountSwitch{}, err
	}
	sw, ok, err := store.GetCodexAccountSwitch(ctx, strings.TrimSpace(id))
	if err != nil {
		return sw, err
	}
	if !ok {
		return sw, ports.ErrCodexAccountSwitchNotFound
	}
	return m.decorateCodexAccountSwitch(sw), nil
}

// GetActiveCodexAccountSwitch returns the sole nonterminal switch when present.
func (m *codexAccountSwitchCoordinator) GetActiveCodexAccountSwitch(ctx context.Context) (domain.CodexAccountSwitch, bool, error) {
	_, store, err := m.codexAccountSwitchDependencies()
	if err != nil {
		return domain.CodexAccountSwitch{}, false, err
	}
	sw, ok, err := store.GetActiveCodexAccountSwitch(ctx)
	if err != nil || !ok {
		return sw, ok, err
	}
	return m.decorateCodexAccountSwitch(sw), true, nil
}

// RecoverCodexAccountSwitch retries the exact incomplete credential operation.
func (m *codexAccountSwitchCoordinator) RecoverCodexAccountSwitch(ctx context.Context, id string) (domain.CodexAccountSwitch, error) {
	credentials, store, err := m.codexAccountSwitchDependencies()
	if err != nil {
		return domain.CodexAccountSwitch{}, err
	}
	sw, err := m.getCodexAccountSwitch(ctx, id)
	if err != nil {
		return sw, err
	}
	if sw.Phase.Terminal() {
		return sw, errors.New("codex account switch is already terminal")
	}
	if !m.claimCodexAccountSwitchRecoveryWorker(ctx) {
		return sw, ports.ErrCodexAccountSwitchInProgress
	}
	if !m.startWorker(func() {
		m.recoverCodexAccountSwitch(m.backgroundContext, credentials, store, sw)
	}) {
		m.finishCodexAccountSwitchWorker(false)
		return sw, context.Canceled
	}
	return sw, nil
}

func (m *codexAccountSwitchCoordinator) recoverCodexAccountSwitch(ctx context.Context, credentials ports.CodexAccountCredentialManager, store ports.CodexAccountSwitchStore, sw domain.CodexAccountSwitch) {
	defer func() {
		m.finishCodexAccountSwitchMutation(credentials, retainCodexAccountSwitchFence(sw.Phase))
	}()
	m.dispatchCodexAccountSwitch(ctx, credentials, store, &sw)
}

// ReconcileCodexAccountSwitches restores the device-global credential fence
// before any new Codex process is admitted, then resumes the durable operation.
func (m *codexAccountSwitchCoordinator) ReconcileCodexAccountSwitches(ctx context.Context) error {
	credentials, store, err := m.codexAccountSwitchDependencies()
	if err != nil {
		return nil //nolint:nilerr // account switching is optional when its feature wiring is absent.
	}
	sw, ok, err := store.GetActiveCodexAccountSwitch(ctx)
	if err != nil || !ok {
		return err
	}
	if err := credentials.WaitCodexAccountStoreReady(ctx); err != nil {
		return err
	}
	if err := m.acquireCodexAccountSwitchGate(ctx); err != nil {
		return err
	}
	if err := credentials.BeginCodexAccountMutation(ctx); err != nil {
		m.finishCodexAccountSwitchWorker(false)
		return err
	}
	if !m.startWorker(func() {
		m.runCodexAccountSwitch(m.backgroundContext, credentials, store, sw)
	}) {
		credentials.EndCodexAccountMutation()
		m.finishCodexAccountSwitchWorker(false)
		return context.Canceled
	}
	return nil
}

func (m *codexAccountSwitchCoordinator) startWorker(run func()) bool {
	m.workersMu.Lock()
	defer m.workersMu.Unlock()
	if m.workersClosed {
		return false
	}
	m.workers.Add(1)
	go func() {
		defer m.workers.Done()
		run()
	}()
	return true
}

func (m *codexAccountSwitchCoordinator) Wait(ctx context.Context) error {
	m.workersMu.Lock()
	m.workersClosed = true
	m.workersMu.Unlock()
	done := make(chan struct{})
	go func() {
		m.workers.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
