package sessionmanager

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

var (
	// ErrCodexAccountSwitchInProgress means the daemon-wide mutation gate is held.
	ErrCodexAccountSwitchInProgress = ports.ErrCodexAccountSwitchInProgress
	// ErrCodexAccountAlreadyActive rejects selecting the current account.
	ErrCodexAccountAlreadyActive = ports.ErrCodexAccountAlreadyActive
	// ErrCodexActiveAccountUnavailable rejects switching without a reconciled source account.
	ErrCodexActiveAccountUnavailable = ports.ErrCodexActiveAccountUnavailable
	// ErrCodexAccountSwitchNotFound means the durable operation does not exist.
	ErrCodexAccountSwitchNotFound = ports.ErrCodexAccountSwitchNotFound
	// ErrCodexAccountRevisionConflict reports a stale active-account revision.
	ErrCodexAccountRevisionConflict = ports.ErrCodexAccountRevisionConflict
	// ErrCodexAccountSwitchIdempotencyConflict rejects reused mismatched keys.
	ErrCodexAccountSwitchIdempotencyConflict = ports.ErrCodexAccountSwitchIdempotencyConflict
)

// These phases were emitted by the retired stop-before-switch implementation.
// New switches never write them, but startup recovery must settle an operation
// created by an older desktop build without returning to the deleted workflow.
const (
	legacyCodexSwitchStoppingSessions  domain.CodexAccountSwitchPhase = "stopping_sessions"
	legacyCodexSwitchSessionsStopped   domain.CodexAccountSwitchPhase = "sessions_stopped"
	legacyCodexSwitchRestartingSession domain.CodexAccountSwitchPhase = "restarting_sessions"
)

func codexAccountSwitchFingerprint(target string, revision int64) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("v3\x00%s\x00%d", target, revision)))
	return "v3:" + hex.EncodeToString(sum[:])
}

func (m *Manager) codexAccountSwitchDependencies() (ports.CodexAccountCredentialManager, ports.CodexAccountSwitchStore, error) {
	credentials, ok := m.agentReadiness.(ports.CodexAccountCredentialManager)
	if !ok {
		return nil, nil, errors.New("codex account credential manager is unavailable")
	}
	store, ok := m.store.(ports.CodexAccountSwitchStore)
	if !ok {
		return nil, nil, errors.New("codex account switch store is unavailable")
	}
	return credentials, store, nil
}

func (m *Manager) acquireCodexAccountSwitchGate(ctx context.Context) error {
	lease, err := m.codexOperationGate.AcquireExclusive(ctx)
	if err != nil {
		return err
	}
	m.codexAccountSwitchMu.Lock()
	defer m.codexAccountSwitchMu.Unlock()
	if m.codexAccountSwitchWorkerRunning || m.codexAccountSwitchLease != nil {
		lease.Release()
		return ErrCodexAccountSwitchInProgress
	}
	m.codexAccountSwitchLease = lease
	m.codexAccountSwitchWorkerRunning = true
	return nil
}

// claimCodexAccountSwitchRecoveryWorker starts one recovery worker while the
// durable global mutation fence remains active. Recovery-required operations
// intentionally retain that fence between HTTP requests so no new Codex
// process can start against an ambiguous runtime credential.
func (m *Manager) claimCodexAccountSwitchRecoveryWorker(ctx context.Context) bool {
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

func (m *Manager) finishCodexAccountSwitchWorker(keepFence bool) {
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

func (m *Manager) codexAccountSwitchWorkerActive() bool {
	m.codexAccountSwitchMu.Lock()
	defer m.codexAccountSwitchMu.Unlock()
	return m.codexAccountSwitchWorkerRunning
}

func (m *Manager) finishCodexAccountSwitchMutation(credentials ports.CodexAccountCredentialManager, keepFence bool) {
	m.finishCodexAccountSwitchWorker(keepFence)
	if !keepFence {
		credentials.EndCodexAccountMutation()
	}
}

func (m *Manager) codexAccountSwitchIsActive() bool {
	return m.codexOperationGate != nil && m.codexOperationGate.ExclusivePendingOrHeld()
}

// CodexAccountSwitchInProgress is the daemon-wide admission fence consumed by
// controller owners outside Session Manager.
func (m *Manager) CodexAccountSwitchInProgress() bool { return m.codexAccountSwitchIsActive() }

// StartCodexAccountSwitch admits and starts one daemon-owned global account switch.
// Existing controllers are deliberately outside this transaction: the operation
// changes and verifies the device credential only.
func (m *Manager) StartCodexAccountSwitch(ctx context.Context, cfg ports.CodexAccountSwitchConfig) (domain.CodexAccountSwitch, error) {
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
			return existing, ErrCodexAccountSwitchIdempotencyConflict
		}
		return m.decorateCodexAccountSwitch(existing), nil
	}
	if _, active, readErr := store.GetActiveCodexAccountSwitch(ctx); readErr != nil {
		return domain.CodexAccountSwitch{}, readErr
	} else if active {
		return domain.CodexAccountSwitch{}, ErrCodexAccountSwitchInProgress
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
		return domain.CodexAccountSwitch{}, ErrCodexAccountAlreadyActive
	}
	if source.Revision != cfg.ExpectedAccountRevision {
		return domain.CodexAccountSwitch{}, ErrCodexAccountRevisionConflict
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

	releaseMutation = false
	releaseSwitchGate = false
	m.agentSwitchWorkers.Add(1)
	go func() {
		defer m.agentSwitchWorkers.Done()
		m.runCodexAccountSwitch(m.backgroundContext, credentials, store, sw)
	}()
	return sw, nil
}

func (m *Manager) runCodexAccountSwitch(ctx context.Context, credentials ports.CodexAccountCredentialManager, store ports.CodexAccountSwitchStore, sw domain.CodexAccountSwitch) {
	ctx = codexExclusiveOperationContext(ctx)
	defer func() {
		m.finishCodexAccountSwitchMutation(credentials, retainCodexAccountSwitchFence(sw.Phase))
	}()
	m.dispatchCodexAccountSwitch(ctx, credentials, store, &sw)
}

func (m *Manager) dispatchCodexAccountSwitch(ctx context.Context, credentials ports.CodexAccountCredentialManager, store ports.CodexAccountSwitchStore, sw *domain.CodexAccountSwitch) {
	// Switches created before source_kind was introduced are managed-account
	// switches. Keep that compatibility at the credential coordinator boundary.
	if sw.SourceKind == "" {
		sw.SourceKind = domain.CodexAccountSwitchSourceManaged
	}
	for {
		switch sw.Phase {
		case domain.CodexAccountSwitchRequested,
			legacyCodexSwitchStoppingSessions,
			legacyCodexSwitchSessionsStopped:
			// Older builds may have stopped some controllers already. Do not
			// continue that retired workflow; finish the requested credential
			// switch and leave ordinary Resume responsible for stopped sessions.
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
		case domain.CodexAccountSwitchVerifyingAccount, legacyCodexSwitchRestartingSession:
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
			// stop_unconfirmed was written only by the retired pre-activation
			// session shutdown. The source credential was never checkpointed or
			// replaced, so settle it without attempting credential rollback.
			if sw.FailureCode == "stop_unconfirmed" {
				m.failAndCleanupCodexAccountSwitch(ctx, credentials, store, sw)
				return
			}
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

func (m *Manager) completeCodexAccountSwitch(ctx context.Context, credentials ports.CodexAccountCredentialManager, store ports.CodexAccountSwitchStore, sw *domain.CodexAccountSwitch) {
	completed := m.clock()
	sw.CompletedAt = &completed
	if m.advanceCodexAccountSwitch(ctx, store, sw, domain.CodexAccountSwitchCompleted, "") == nil {
		_ = credentials.CleanupCodexAccountSwitch(ctx, sw.ID)
	}
}

func (m *Manager) failAndCleanupCodexAccountSwitch(ctx context.Context, credentials ports.CodexAccountCredentialManager, store ports.CodexAccountSwitchStore, sw *domain.CodexAccountSwitch) {
	completed := m.clock()
	sw.CompletedAt = &completed
	if m.advanceCodexAccountSwitch(ctx, store, sw, domain.CodexAccountSwitchFailed, sw.FailureCode) == nil {
		_ = credentials.CleanupCodexAccountSwitch(ctx, sw.ID)
	}
}

func retainCodexAccountSwitchFence(phase domain.CodexAccountSwitchPhase) bool {
	return !phase.Terminal()
}

func (m *Manager) advanceCodexAccountSwitch(ctx context.Context, store ports.CodexAccountSwitchStore, sw *domain.CodexAccountSwitch, next domain.CodexAccountSwitchPhase, code string) error {
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
	settleCtx, cancel := switchDurableContext(ctx)
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

func (m *Manager) decorateCodexAccountSwitch(sw domain.CodexAccountSwitch) domain.CodexAccountSwitch {
	sw.CanRecover = !sw.Phase.Terminal() && !m.codexAccountSwitchWorkerActive()
	return sw
}

func (m *Manager) getCodexAccountSwitch(ctx context.Context, id string) (domain.CodexAccountSwitch, error) {
	_, store, err := m.codexAccountSwitchDependencies()
	if err != nil {
		return domain.CodexAccountSwitch{}, err
	}
	sw, ok, err := store.GetCodexAccountSwitch(ctx, strings.TrimSpace(id))
	if err != nil {
		return sw, err
	}
	if !ok {
		return sw, ErrCodexAccountSwitchNotFound
	}
	return m.decorateCodexAccountSwitch(sw), nil
}

// GetActiveCodexAccountSwitch returns the sole nonterminal switch when present.
func (m *Manager) GetActiveCodexAccountSwitch(ctx context.Context) (domain.CodexAccountSwitch, bool, error) {
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
func (m *Manager) RecoverCodexAccountSwitch(ctx context.Context, id string) (domain.CodexAccountSwitch, error) {
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
		return sw, ErrCodexAccountSwitchInProgress
	}
	m.agentSwitchWorkers.Add(1)
	go func() {
		defer m.agentSwitchWorkers.Done()
		m.recoverCodexAccountSwitch(m.backgroundContext, credentials, store, sw)
	}()
	return sw, nil
}

func (m *Manager) recoverCodexAccountSwitch(ctx context.Context, credentials ports.CodexAccountCredentialManager, store ports.CodexAccountSwitchStore, sw domain.CodexAccountSwitch) {
	ctx = codexExclusiveOperationContext(ctx)
	defer func() {
		m.finishCodexAccountSwitchMutation(credentials, retainCodexAccountSwitchFence(sw.Phase))
	}()
	m.dispatchCodexAccountSwitch(ctx, credentials, store, &sw)
}

// ReconcileCodexAccountSwitches restores the daemon-wide credential mutation
// fence before ordinary session adoption, then resumes the durable operation.
func (m *Manager) ReconcileCodexAccountSwitches(ctx context.Context) error {
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
	m.agentSwitchWorkers.Add(1)
	go func() {
		defer m.agentSwitchWorkers.Done()
		m.runCodexAccountSwitch(m.backgroundContext, credentials, store, sw)
	}()
	return nil
}
