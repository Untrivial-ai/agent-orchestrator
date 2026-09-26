package agent

import (
	"context"
	"errors"
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
	routeProvider                   ports.CodexRouteProvider
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
	routeProviders ...ports.CodexRouteProvider,
) *codexAccountSwitchCoordinator {
	if ctx == nil {
		ctx = context.Background()
	}
	if clock == nil {
		clock = time.Now
	}
	var routeProvider ports.CodexRouteProvider
	if len(routeProviders) > 0 {
		routeProvider = routeProviders[0]
	}
	return &codexAccountSwitchCoordinator{
		credentials: credentials, store: store, codexOperationGate: gate,
		backgroundContext: ctx, clock: clock, publish: publish, routeProvider: routeProvider,
	}
}

func (m *codexAccountSwitchCoordinator) publishCodexAccountSwitchChanged() {
	if m.publish != nil {
		m.publish()
	}
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

func (m *codexAccountSwitchCoordinator) finishCodexAccountSwitchWorker() {
	m.codexAccountSwitchMu.Lock()
	m.codexAccountSwitchWorkerRunning = false
	release := m.codexAccountSwitchLease
	m.codexAccountSwitchLease = nil
	m.codexAccountSwitchMu.Unlock()
	if release != nil {
		release.Release()
	}
}

func (m *codexAccountSwitchCoordinator) finishCodexAccountSwitchMutation() {
	m.finishCodexAccountSwitchWorker()
	m.credentials.EndCodexAccountMutation()
}

func (m *codexAccountSwitchCoordinator) codexAccountSwitchIsActive() bool {
	return m.codexOperationGate != nil && m.codexOperationGate.ExclusivePendingOrHeld()
}

// CodexAccountSwitchInProgress is the daemon-wide credential admission fence.
func (m *codexAccountSwitchCoordinator) CodexAccountSwitchInProgress() bool {
	return m.codexAccountSwitchIsActive()
}

func (m *codexAccountSwitchCoordinator) GetCodexAccountSwitch(ctx context.Context, id string) (domain.CodexAccountSwitch, bool, error) {
	if m.store == nil {
		return domain.CodexAccountSwitch{}, false, errors.New("codex account switch store is unavailable")
	}
	return m.store.GetCodexAccountSwitch(ctx, id)
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
	if m.credentials == nil || m.store == nil {
		return domain.CodexAccountSwitch{}, errors.New("codex account switching is unavailable")
	}
	if existing, ok, readErr := m.store.GetCodexAccountSwitchByIdempotency(ctx, cfg.IdempotencyKey); readErr != nil {
		return domain.CodexAccountSwitch{}, readErr
	} else if ok {
		if existing.TargetAccountID != cfg.TargetAccountID {
			return existing, ports.ErrCodexAccountSwitchIdempotencyConflict
		}
		return existing, nil
	}
	if _, active, readErr := m.store.GetActiveCodexAccountSwitch(ctx); readErr != nil {
		return domain.CodexAccountSwitch{}, readErr
	} else if active {
		return domain.CodexAccountSwitch{}, ports.ErrCodexAccountSwitchInProgress
	}
	if err := m.credentials.WaitCodexAccountStoreReady(ctx); err != nil {
		return domain.CodexAccountSwitch{}, err
	}
	if err := m.acquireCodexAccountSwitchGate(ctx); err != nil {
		return domain.CodexAccountSwitch{}, err
	}
	releaseSwitchGate := true
	defer func() {
		if releaseSwitchGate {
			m.finishCodexAccountSwitchWorker()
		}
	}()
	if err := m.credentials.BeginCodexAccountMutation(ctx); err != nil {
		return domain.CodexAccountSwitch{}, err
	}
	releaseMutation := true
	defer func() {
		if releaseMutation {
			m.credentials.EndCodexAccountMutation()
		}
	}()
	if err := m.credentials.CleanupInactiveCodexAccountSwitches(ctx, ""); err != nil {
		return domain.CodexAccountSwitch{}, err
	}

	switchID := uuid.NewString()
	source, err := m.credentials.PrepareCodexAccountForSwitch(ctx, switchID, cfg.TargetAccountID)
	if err != nil {
		return domain.CodexAccountSwitch{}, err
	}
	if source.Kind == domain.CodexAccountSwitchSourceManaged && source.AccountID == cfg.TargetAccountID {
		_ = m.credentials.CleanupCodexAccountSwitch(ctx, switchID)
		return domain.CodexAccountSwitch{}, ports.ErrCodexAccountAlreadyActive
	}
	now := m.clock()
	sw := domain.CodexAccountSwitch{
		ID: switchID, SourceKind: source.Kind, SourceAccountID: source.AccountID,
		TargetAccountID: cfg.TargetAccountID, Phase: domain.CodexAccountSwitchActivatingAccount,
		IdempotencyKey: cfg.IdempotencyKey,
		CreatedAt:      now, UpdatedAt: now,
	}
	created, inserted, err := m.store.CreateCodexAccountSwitch(ctx, sw)
	if err != nil {
		_ = m.credentials.CleanupCodexAccountSwitch(ctx, switchID)
		return domain.CodexAccountSwitch{}, err
	}
	sw = created
	if !inserted {
		_ = m.credentials.CleanupCodexAccountSwitch(ctx, switchID)
		return sw, nil
	}

	if !m.startWorker(func() {
		m.runCodexAccountSwitch(m.backgroundContext, sw)
	}) {
		// Shutdown raced with admission before the credential mutation worker
		// could start. Terminally cancel this pre-mutation journal now so it does
		// not leave a fake recovery state for the next daemon.
		settleCtx, cancel := codexAccountSwitchDurableContext(ctx)
		sw.FailureCode = "switch_cancelled_before_mutation"
		m.failAndCleanupCodexAccountSwitch(settleCtx, &sw)
		cancel()
		return sw, context.Canceled
	}
	releaseMutation = false
	releaseSwitchGate = false
	return sw, nil
}

func (m *codexAccountSwitchCoordinator) runCodexAccountSwitch(ctx context.Context, sw domain.CodexAccountSwitch) {
	defer func() {
		m.finishCodexAccountSwitchMutation()
		if sw.Phase.Terminal() {
			// The device credential is the source of truth. Reconcile after every
			// terminal outcome so an external login is matched or imported.
			_ = m.credentials.EnsureCodexDeviceAccountReconciled(m.backgroundContext)
		}
	}()
	delay := time.Second
	for !sw.Phase.Terminal() {
		m.dispatchCodexAccountSwitch(ctx, &sw)
		if sw.Phase.Terminal() {
			break
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
		if delay < 30*time.Second {
			delay *= 2
			if delay > 30*time.Second {
				delay = 30 * time.Second
			}
		}
		current, found, err := m.store.GetCodexAccountSwitch(ctx, sw.ID)
		if err != nil || !found {
			continue
		}
		sw = current
	}
}

// runCodexAccountSwitchRecovery never resumes an interrupted user request.
// It only recognizes an already-installed target; every other local state
// terminally cancels the old journal and lets reconciliation adopt reality.
func (m *codexAccountSwitchCoordinator) runCodexAccountSwitchRecovery(ctx context.Context, sw domain.CodexAccountSwitch) {
	defer func() {
		m.finishCodexAccountSwitchMutation()
		_ = m.credentials.EnsureCodexDeviceAccountReconciled(m.backgroundContext)
	}()
	delay := time.Second
	for !sw.Phase.Terminal() {
		if m.settleCodexAccountSwitch(ctx, &sw, "interrupted_switch_cancelled") {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
		if delay < 30*time.Second {
			delay *= 2
			if delay > 30*time.Second {
				delay = 30 * time.Second
			}
		}
	}
}

func (m *codexAccountSwitchCoordinator) dispatchCodexAccountSwitch(ctx context.Context, sw *domain.CodexAccountSwitch) {
	// Switches created before source_kind was introduced are managed-account
	// switches. Keep that compatibility at the credential coordinator boundary.
	if sw.SourceKind == "" {
		sw.SourceKind = domain.CodexAccountSwitchSourceManaged
	}
	for {
		switch sw.Phase {
		case domain.CodexAccountSwitchRequested, domain.CodexAccountSwitchCheckpointCredential:
			// Compatibility only: current switches are created directly at the
			// activating phase because both credentials are already staged.
			if m.advanceCodexAccountSwitch(ctx, sw, domain.CodexAccountSwitchActivatingAccount, "") != nil {
				return
			}
		case domain.CodexAccountSwitchActivatingAccount:
			state, inspectErr := m.credentials.InspectCodexAccountSwitch(ctx, sw.ID, sw.SourceKind, sw.SourceAccountID, sw.TargetAccountID)
			if inspectErr != nil {
				return
			}
			if state != domain.CodexAccountSwitchTargetInstalled {
				if err := m.credentials.ActivatePreparedCodexAccountSwitch(ctx, sw.SourceKind, sw.ID, sw.TargetAccountID); err != nil {
					if errors.Is(err, ports.ErrCodexAccountSwitchNotCommitted) {
						sw.FailureCode = "activation_failed"
						m.failAndCleanupCodexAccountSwitch(ctx, sw)
						return
					}
					m.settleCodexAccountSwitch(ctx, sw, "activation_failed")
					return
				}
			}
			committedAt := m.clock()
			sw.CredentialsCommittedAt = &committedAt
			if !m.switchAllCodexSessionRoutes(ctx, sw.TargetAccountID) {
				// Keep the durable switch in the activating phase. The worker
				// retries route reconciliation, so a transient proxy/storage
				// failure cannot leave the global credential switch falsely
				// completed while sessions still use the old account.
				return
			}
			m.completeCodexAccountSwitch(ctx, sw)
			return
		case domain.CodexAccountSwitchRecoveryRequired:
			// Compatibility for a switch journal written by an older build. There
			// is no user-driven recovery anymore: settle it from the current local
			// credential, then let normal reconciliation adopt that credential.
			m.settleCodexAccountSwitch(ctx, sw, "legacy_switch_interrupted")
			return
		case domain.CodexAccountSwitchCompleted, domain.CodexAccountSwitchFailed:
			return
		default:
			sw.FailureCode = "switch_state_unavailable"
			m.failAndCleanupCodexAccountSwitch(ctx, sw)
			return
		}
	}
}

// settleCodexAccountSwitch resolves an interrupted operation from local state.
// If the target is installed, the switch completes. Otherwise the switch ends
// and ordinary reconciliation adopts whatever credential the device now has.
func (m *codexAccountSwitchCoordinator) settleCodexAccountSwitch(ctx context.Context, sw *domain.CodexAccountSwitch, failureCode string) bool {
	state, err := m.credentials.InspectCodexAccountSwitch(ctx, sw.ID, sw.SourceKind, sw.SourceAccountID, sw.TargetAccountID)
	if err != nil {
		return false
	}
	if state == domain.CodexAccountSwitchTargetInstalled {
		if sw.CredentialsCommittedAt == nil {
			committedAt := m.clock()
			sw.CredentialsCommittedAt = &committedAt
		}
		if !m.switchAllCodexSessionRoutes(ctx, sw.TargetAccountID) {
			return false
		}
		m.completeCodexAccountSwitch(ctx, sw)
		return true
	}
	sw.FailureCode = failureCode
	m.failAndCleanupCodexAccountSwitch(ctx, sw)
	return true
}

func (m *codexAccountSwitchCoordinator) switchAllCodexSessionRoutes(ctx context.Context, accountID string) bool {
	if m.routeProvider == nil {
		return true
	}
	// The route provider keeps existing session pins intact and only changes
	// the default used when a future session is first routed.
	_, err := m.routeProvider.SwitchAllSessionsAccount(ctx, accountID)
	return err == nil
}

func (m *codexAccountSwitchCoordinator) completeCodexAccountSwitch(ctx context.Context, sw *domain.CodexAccountSwitch) {
	completed := m.clock()
	sw.CompletedAt = &completed
	if m.advanceCodexAccountSwitch(ctx, sw, domain.CodexAccountSwitchCompleted, "") == nil {
		_ = m.credentials.CleanupCodexAccountSwitch(ctx, sw.ID)
	}
}

func (m *codexAccountSwitchCoordinator) failAndCleanupCodexAccountSwitch(ctx context.Context, sw *domain.CodexAccountSwitch) {
	completed := m.clock()
	sw.CompletedAt = &completed
	if m.advanceCodexAccountSwitch(ctx, sw, domain.CodexAccountSwitchFailed, sw.FailureCode) == nil {
		_ = m.credentials.CleanupCodexAccountSwitch(ctx, sw.ID)
	}
}

func (m *codexAccountSwitchCoordinator) advanceCodexAccountSwitch(ctx context.Context, sw *domain.CodexAccountSwitch, next domain.CodexAccountSwitchPhase, code string) error {
	expected := sw.Phase
	candidate := *sw
	candidate.Phase, candidate.FailureCode, candidate.UpdatedAt = next, code, m.clock()
	ok, err := m.store.UpdateCodexAccountSwitch(ctx, candidate, expected)
	if err == nil && ok {
		*sw = candidate
		m.publishCodexAccountSwitchChanged()
		return nil
	}
	settleCtx, cancel := codexAccountSwitchDurableContext(ctx)
	defer cancel()
	current, found, readErr := m.store.GetCodexAccountSwitch(settleCtx, sw.ID)
	if readErr != nil {
		return errors.Join(err, readErr)
	}
	if found && current.Phase == candidate.Phase && current.FailureCode == candidate.FailureCode {
		*sw = current
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

// GetActiveCodexAccountSwitch returns the sole nonterminal switch when present.
func (m *codexAccountSwitchCoordinator) GetActiveCodexAccountSwitch(ctx context.Context) (domain.CodexAccountSwitch, bool, error) {
	if m.store == nil {
		return domain.CodexAccountSwitch{}, false, errors.New("codex account switch store is unavailable")
	}
	sw, ok, err := m.store.GetActiveCodexAccountSwitch(ctx)
	if err != nil || !ok {
		return sw, ok, err
	}
	return sw, true, nil
}

// ReconcileCodexAccountSwitches starts the best-effort recovery supervisor.
// Account-switch recovery must never prevent the rest of the daemon starting.
func (m *codexAccountSwitchCoordinator) ReconcileCodexAccountSwitches(ctx context.Context) error {
	if m.credentials == nil || m.store == nil {
		return nil //nolint:nilerr // account switching is optional when its feature wiring is absent.
	}
	// Publish the Codex launch/mutation fence before the first fallible journal
	// read. A crashed switch may already have crossed the credential mutation
	// boundary even when SQLite is temporarily unavailable.
	if err := m.acquireCodexAccountSwitchGate(ctx); err != nil {
		return err
	}
	sw, ok, err := m.store.GetActiveCodexAccountSwitch(ctx)
	if err != nil {
		m.startCodexAccountSwitchRecoveryRetryWithHeldGate()
		return err
	}
	if !ok {
		cleanupErr := m.credentials.CleanupInactiveCodexAccountSwitches(ctx, "")
		m.finishCodexAccountSwitchWorker()
		return cleanupErr
	}
	if err := m.startCodexAccountSwitchRecoveryWithHeldGate(ctx, sw); err != nil {
		m.startCodexAccountSwitchRecoveryRetryWithHeldGate()
		return err
	}
	return nil
}

func (m *codexAccountSwitchCoordinator) startCodexAccountSwitchRecoveryWithHeldGate(ctx context.Context, sw domain.CodexAccountSwitch) error {
	if err := m.credentials.WaitCodexAccountStoreReady(ctx); err != nil {
		return err
	}
	if err := m.credentials.BeginCodexAccountMutation(ctx); err != nil {
		return err
	}
	if err := m.credentials.CleanupInactiveCodexAccountSwitches(ctx, sw.ID); err != nil {
		m.credentials.EndCodexAccountMutation()
		return err
	}
	if !m.startWorker(func() {
		m.runCodexAccountSwitchRecovery(m.backgroundContext, sw)
	}) {
		m.credentials.EndCodexAccountMutation()
		return context.Canceled
	}
	return nil
}

func (m *codexAccountSwitchCoordinator) startCodexAccountSwitchRecoveryRetryWithHeldGate() {
	if !m.startWorker(func() {
		defer func() {
			if m.backgroundContext.Err() != nil {
				m.finishCodexAccountSwitchWorker()
			}
		}()
		delay := time.Second
		for {
			select {
			case <-m.backgroundContext.Done():
				return
			case <-time.After(delay):
			}
			sw, ok, err := m.store.GetActiveCodexAccountSwitch(m.backgroundContext)
			if err != nil {
				if delay < 30*time.Second {
					delay *= 2
					if delay > 30*time.Second {
						delay = 30 * time.Second
					}
				}
				continue
			}
			if !ok {
				_ = m.credentials.CleanupInactiveCodexAccountSwitches(m.backgroundContext, "")
				m.finishCodexAccountSwitchWorker()
				return
			}
			if err := m.startCodexAccountSwitchRecoveryWithHeldGate(m.backgroundContext, sw); err != nil {
				continue
			}
			return
		}
	}) {
		m.finishCodexAccountSwitchWorker()
	}
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
