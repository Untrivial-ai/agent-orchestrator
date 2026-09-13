package sessionmanager

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"

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
	legacyCodexSwitchStoppingSessions domain.CodexAccountSwitchPhase = "stopping_sessions"
	legacyCodexSwitchSessionsStopped  domain.CodexAccountSwitchPhase = "sessions_stopped"
	codexIdleRestartIncomplete                                       = "idle_session_restart_incomplete"
)

func codexAccountSwitchFingerprint(target string, revision int64, restartIdleSessions bool) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("v4\x00%s\x00%d\x00%t", target, revision, restartIdleSessions)))
	return "v4:" + hex.EncodeToString(sum[:])
}

func codexAccountSwitchRequestMatches(existing domain.CodexAccountSwitch, target string, revision int64, restartIdleSessions bool) bool {
	if existing.TargetAccountID != target || existing.ExpectedAccountRevision != revision {
		return false
	}
	if strings.HasPrefix(existing.RequestFingerprint, "v2:") {
		return existing.RestartIdleSessions == restartIdleSessions
	}
	if strings.HasPrefix(existing.RequestFingerprint, "v3:") {
		return !restartIdleSessions
	}
	return existing.RequestFingerprint == codexAccountSwitchFingerprint(target, revision, restartIdleSessions)
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
	if m.codexAccountSwitchOperationOpen || m.codexAccountSwitchWorkerRunning || m.codexAccountSwitchLease != nil {
		lease.Release()
		return ErrCodexAccountSwitchInProgress
	}
	m.codexAccountSwitchLease = lease
	m.codexAccountSwitchWorkerRunning = true
	m.codexAccountSwitchOperationOpen = true
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
		m.codexAccountSwitchOperationOpen = true
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
	m.codexAccountSwitchOperationOpen = true
	return true
}

func (m *Manager) finishCodexAccountSwitchWorker(keepFence bool) {
	m.codexAccountSwitchMu.Lock()
	m.codexAccountSwitchWorkerRunning = false
	var release ports.CodexOperationLease
	if !keepFence {
		m.codexAccountSwitchOperationOpen = false
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

func (m *Manager) releaseCodexAccountSwitchLease() {
	m.codexAccountSwitchMu.Lock()
	lease := m.codexAccountSwitchLease
	m.codexAccountSwitchLease = nil
	m.codexAccountSwitchMu.Unlock()
	if lease != nil {
		lease.Release()
	}
}

func (m *Manager) codexAccountSwitchWorkerActive() bool {
	m.codexAccountSwitchMu.Lock()
	defer m.codexAccountSwitchMu.Unlock()
	return m.codexAccountSwitchWorkerRunning
}

func (m *Manager) codexAccountSwitchOperationActive() bool {
	m.codexAccountSwitchMu.Lock()
	defer m.codexAccountSwitchMu.Unlock()
	return m.codexAccountSwitchOperationOpen
}

func (m *Manager) codexAccountSwitchIsActive() bool {
	return m.codexOperationGate != nil && m.codexOperationGate.ExclusivePendingOrHeld()
}

// CodexAccountSwitchInProgress is the daemon-wide admission fence consumed by
// controller owners outside Session Manager.
func (m *Manager) CodexAccountSwitchInProgress() bool {
	return m.codexAccountSwitchOperationActive()
}

// StartCodexAccountSwitch admits and starts one daemon-owned global account switch.
// Credential activation and verification always finish before an optional,
// best-effort reconnect of controllers that are still durably idle.
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
	fingerprint := codexAccountSwitchFingerprint(cfg.TargetAccountID, cfg.ExpectedAccountRevision, cfg.RestartIdleSessions)
	if existing, ok, readErr := store.GetCodexAccountSwitchByIdempotency(ctx, cfg.IdempotencyKey); readErr != nil {
		return domain.CodexAccountSwitch{}, readErr
	} else if ok {
		if !codexAccountSwitchRequestMatches(existing, cfg.TargetAccountID, cfg.ExpectedAccountRevision, cfg.RestartIdleSessions) {
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
		TargetAccountID: cfg.TargetAccountID, RestartIdleSessions: cfg.RestartIdleSessions, Phase: domain.CodexAccountSwitchRequested,
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
		m.runCodexAccountSwitch(m.backgroundContext, credentials, store, sw, false)
	}()
	return sw, nil
}

func (m *Manager) runCodexAccountSwitch(ctx context.Context, credentials ports.CodexAccountCredentialManager, store ports.CodexAccountSwitchStore, sw domain.CodexAccountSwitch, recovering bool) {
	ctx = codexExclusiveOperationContext(ctx)
	credentialFenceReleased := false
	releaseCredentialFence := func() {
		if credentialFenceReleased {
			return
		}
		credentialFenceReleased = true
		credentials.EndCodexAccountMutation()
		m.releaseCodexAccountSwitchLease()
	}
	defer func() {
		keepFence := retainCodexAccountSwitchFence(sw.Phase)
		if !keepFence {
			releaseCredentialFence()
		}
		m.finishCodexAccountSwitchWorker(keepFence)
	}()
	m.dispatchCodexAccountSwitch(ctx, credentials, store, &sw, recovering, releaseCredentialFence)
}

func (m *Manager) dispatchCodexAccountSwitch(ctx context.Context, credentials ports.CodexAccountCredentialManager, store ports.CodexAccountSwitchStore, sw *domain.CodexAccountSwitch, recovering bool, releaseCredentialFence func()) {
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
		case domain.CodexAccountSwitchVerifyingAccount:
			if err := credentials.VerifyCurrentCodexAccount(ctx, sw.TargetAccountID); err != nil {
				_ = m.advanceCodexAccountSwitch(ctx, store, sw, domain.CodexAccountSwitchRecoveryRequired, "target_verification_unconfirmed")
				return
			}
			if sw.CredentialsCommittedAt == nil {
				committedAt := m.clock()
				sw.CredentialsCommittedAt = &committedAt
			}
			if !sw.RestartIdleSessions {
				m.completeCodexAccountSwitch(ctx, credentials, store, sw, "")
				return
			}
			if m.advanceCodexAccountSwitch(ctx, store, sw, domain.CodexAccountSwitchRestartingSessions, "") != nil {
				return
			}
			releaseCredentialFence()
			failureCode := ""
			if m.restartIdleCodexSessions(m.backgroundContext) {
				failureCode = codexIdleRestartIncomplete
			}
			m.completeCodexAccountSwitch(ctx, credentials, store, sw, failureCode)
			return
		case domain.CodexAccountSwitchRestartingSessions:
			// A daemon restart can occur after one controller stopped but before it
			// resumed. Without per-session restart state, replaying the batch could
			// restart already-completed sessions twice. Verify the committed target
			// and settle with the generic manual-resume warning instead.
			if err := credentials.VerifyCurrentCodexAccount(ctx, sw.TargetAccountID); err != nil {
				_ = m.advanceCodexAccountSwitch(ctx, store, sw, domain.CodexAccountSwitchRecoveryRequired, "target_verification_unconfirmed")
				return
			}
			releaseCredentialFence()
			failureCode := codexIdleRestartIncomplete
			if !recovering {
				failureCode = ""
				if m.restartIdleCodexSessions(m.backgroundContext) {
					failureCode = codexIdleRestartIncomplete
				}
			}
			m.completeCodexAccountSwitch(ctx, credentials, store, sw, failureCode)
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
				if sw.RestartIdleSessions {
					if m.advanceCodexAccountSwitch(ctx, store, sw, domain.CodexAccountSwitchRestartingSessions, "") != nil {
						return
					}
					releaseCredentialFence()
					failureCode := ""
					if m.restartIdleCodexSessions(m.backgroundContext) {
						failureCode = codexIdleRestartIncomplete
					}
					m.completeCodexAccountSwitch(ctx, credentials, store, sw, failureCode)
					return
				}
				m.completeCodexAccountSwitch(ctx, credentials, store, sw, "")
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

func (m *Manager) completeCodexAccountSwitch(ctx context.Context, credentials ports.CodexAccountCredentialManager, store ports.CodexAccountSwitchStore, sw *domain.CodexAccountSwitch, failureCode string) {
	completed := m.clock()
	sw.CompletedAt = &completed
	if m.advanceCodexAccountSwitch(ctx, store, sw, domain.CodexAccountSwitchCompleted, failureCode) == nil {
		_ = credentials.CleanupCodexAccountSwitch(ctx, sw.ID)
	}
}

// restartIdleCodexSessions discovers candidates only after the target account
// is verified. Each candidate owns its normal per-session operation fence for
// the entire stop/resume cycle; no account-switch session snapshot is stored.
// The return value reports whether the user should receive the generic manual
// resume warning.
func (m *Manager) restartIdleCodexSessions(ctx context.Context) bool {
	records, err := m.store.ListAllSessions(ctx)
	if err != nil {
		m.logger.Error("codex account switch: list idle sessions failed", "error", err)
		return true
	}
	candidates := make([]domain.SessionID, 0, len(records))
	for _, rec := range records {
		if idleCodexRestartCandidate(rec) {
			candidates = append(candidates, rec.ID)
		}
	}
	if len(candidates) == 0 {
		return false
	}

	results := make(chan bool, len(candidates))
	var workers sync.WaitGroup
	workers.Add(len(candidates))
	for _, id := range candidates {
		go func() {
			defer workers.Done()
			results <- m.restartIdleCodexSession(ctx, id)
		}()
	}
	workers.Wait()
	close(results)
	for failed := range results {
		if failed {
			return true
		}
	}
	return false
}

func idleCodexRestartCandidate(rec domain.SessionRecord) bool {
	if rec.IsTerminated || rec.Harness != domain.HarnessCodex || rec.Activity.State != domain.ActivityIdle {
		return false
	}
	mode := domain.NormalizeSessionMode(rec.Mode)
	return mode == domain.SessionModeTUI || mode == domain.SessionModeChat
}

// restartIdleCodexSession returns true only for a real restart failure. A
// candidate that became busy or entered another operation is deliberately
// skipped and left untouched.
func (m *Manager) restartIdleCodexSession(ctx context.Context, id domain.SessionID) bool {
	if err := m.beginAgentOperation(ctx, id, agentOperationAccountReconnect); err != nil {
		if !errors.Is(err, errAgentOperationInProgress) {
			m.logger.Error("codex account switch: reserve idle session failed", "sessionID", id, "error", err)
			return true
		}
		return false
	}
	defer m.endAgentOperation(id, agentOperationAccountReconnect)

	rec, ok, err := m.store.GetSession(ctx, id)
	if err != nil {
		m.logger.Error("codex account switch: refresh idle session failed", "sessionID", id, "error", err)
		return true
	}
	if !ok || !idleCodexRestartCandidate(rec) {
		return false
	}
	if domain.NormalizeSessionMode(rec.Mode) == domain.SessionModeTUI {
		// The runtime's Restart primitive replaces the command in-place (tmux uses
		// respawn-pane -k), which is the stop-and-resume boundary for TUI without
		// discarding terminal history or changing its handle.
		if _, err := m.resumeAgentRecordWithPolicy(ctx, "restart idle session after Codex account switch", rec, false, true); err != nil {
			m.recordExitedIdleTUIIfStopped(ctx, rec)
			m.logger.Error("codex account switch: resume idle TUI session failed", "sessionID", id, "error", err)
			return true
		}
		return false
	}
	if err := m.stopAgentController(ctx, rec); err != nil {
		m.logger.Error("codex account switch: stop idle session failed", "sessionID", id, "error", err)
		return true
	}
	if err := m.recordAgentExited(ctx, rec); err != nil {
		m.logger.Error("codex account switch: record idle session exit failed", "sessionID", id, "error", err)
		return true
	}
	if _, err := m.resumeAgentRecordWithPolicy(ctx, "restart idle session after Codex account switch", rec, false, true); err != nil {
		m.logger.Error("codex account switch: resume idle session failed", "sessionID", id, "error", err)
		return true
	}
	return false
}

// recordExitedIdleTUIIfStopped makes a failed in-place restart manually
// resumable only when the runtime can prove the pane no longer has a child.
// An unavailable or inconclusive probe leaves the durable row untouched.
func (m *Manager) recordExitedIdleTUIIfStopped(ctx context.Context, rec domain.SessionRecord) {
	inspector, ok := m.runtime.(ports.RuntimeChildInspector)
	if !ok {
		return
	}
	alive, err := inspector.IsChildAlive(ctx, ports.RuntimeHandle{ID: rec.Metadata.RuntimeHandleID})
	if err != nil || alive {
		return
	}
	if err := m.recordAgentExited(ctx, rec); err != nil {
		m.logger.Error("codex account switch: record stopped idle TUI session failed", "sessionID", rec.ID, "error", err)
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

// GetCodexAccountSwitch returns active or terminal state for one durable switch.
func (m *Manager) GetCodexAccountSwitch(ctx context.Context, id string) (domain.CodexAccountSwitch, error) {
	return m.getCodexAccountSwitch(ctx, id)
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
	m.runCodexAccountSwitch(ctx, credentials, store, sw, true)
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
		m.runCodexAccountSwitch(m.backgroundContext, credentials, store, sw, true)
	}()
	return nil
}
