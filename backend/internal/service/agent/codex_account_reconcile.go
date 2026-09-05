package agent

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// Bootstrap failures are serialized into one daemon-owned attempt. Retryable
// failures schedule bounded background recovery, while callers retain the
// result of the specific attempt they joined.
func (m *codexAccountManager) startOrJoinBootstrap() (*codexAccountBootstrapCall, error) {
	if err := m.ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	if m.bootstrapped {
		m.mu.Unlock()
		return nil, nil
	}
	if m.bootstrapCall != nil {
		call := m.bootstrapCall
		m.mu.Unlock()
		return call, nil
	}
	if m.bootstrapLastErr != nil {
		var failure *codexBootstrapFailure
		if !errors.As(m.bootstrapLastErr, &failure) || !failure.retryable || (!m.bootstrapNextRetryAt.IsZero() && m.now().Before(m.bootstrapNextRetryAt)) {
			err := m.bootstrapLastErr
			m.mu.Unlock()
			return nil, err
		}
	}
	if !m.bootstrapNextRetryAt.IsZero() && m.now().Before(m.bootstrapNextRetryAt) {
		err := m.bootstrapLastErr
		m.mu.Unlock()
		if err == nil {
			err = bootstrapFailure("account_bootstrap_unavailable", true)
		}
		return nil, err
	}
	call := m.startBootstrapLocked()
	m.mu.Unlock()
	go m.runBootstrap(call)
	return call, nil
}

func (m *codexAccountManager) startBootstrapLocked() *codexAccountBootstrapCall {
	m.bootstrapRetryGeneration++
	m.bootstrapNextRetryAt = time.Time{}
	call := &codexAccountBootstrapCall{done: make(chan struct{}), attempt: m.bootstrapFailures + 1}
	m.bootstrapCall = call
	return call
}

func (m *codexAccountManager) waitBootstrap(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	call, err := m.startOrJoinBootstrap()
	if err != nil || call == nil {
		return err
	}
	select {
	case <-call.done:
		return call.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *codexAccountManager) runBootstrap(call *codexAccountBootstrapCall) {
	startedAt := m.now()
	err := m.bootstrapInner()
	finishedAt := m.now()
	reason, retryable := "unknown", false
	var failure *codexBootstrapFailure
	if errors.As(err, &failure) {
		reason, retryable = failure.reason, failure.retryable
	}
	var (
		retryDelay      time.Duration
		nextRetryAt     time.Time
		retryGeneration uint64
		shouldRetry     bool
	)

	m.mu.Lock()
	if m.bootstrapCall != call {
		m.mu.Unlock()
		return
	}
	call.err = err
	m.bootstrapCall = nil
	if err == nil {
		m.bootstrapped = true
		m.bootstrapLastErr = nil
		m.bootstrapFailures = 0
		m.bootstrapNextRetryAt = time.Time{}
		m.bootstrapRetryGeneration++
	} else {
		m.bootstrapLastErr = err
		if m.ctx.Err() == nil && retryable {
			m.bootstrapFailures++
			retryDelay = m.bootstrapRetryDelayLocked()
			nextRetryAt = finishedAt.Add(retryDelay)
			m.bootstrapNextRetryAt = nextRetryAt
			m.bootstrapRetryGeneration++
			retryGeneration = m.bootstrapRetryGeneration
			shouldRetry = true
		}
	}
	m.mu.Unlock()
	m.publish()

	duration := finishedAt.Sub(startedAt)
	if err == nil {
		m.logger.Info("Codex account bootstrap completed", "attempt", call.attempt, "duration_ms", duration.Milliseconds())
		close(call.done)
		go m.warmAfterBootstrap()
		return
	}
	if !shouldRetry {
		m.logger.Warn("Codex account bootstrap failed", "attempt", call.attempt, "duration_ms", duration.Milliseconds(), "reasonCode", reason, "retryable", retryable)
		close(call.done)
		return
	}
	m.logger.Warn("Codex account bootstrap failed", "attempt", call.attempt, "duration_ms", duration.Milliseconds(), "reasonCode", reason, "retryable", retryable, "nextRetryAt", nextRetryAt)
	close(call.done)
	go m.scheduleBootstrapRetry(retryGeneration, retryDelay)
}

func (m *codexAccountManager) bootstrapRetryDelayLocked() time.Duration {
	delays := m.bootstrapRetryDelays
	if len(delays) == 0 {
		delays = defaultCodexAccountBootstrapRetryDelays
	}
	return delays[min(m.bootstrapFailures-1, len(delays)-1)]
}

func (m *codexAccountManager) scheduleBootstrapRetry(generation uint64, delay time.Duration) {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-m.ctx.Done():
		return
	}

	m.mu.Lock()
	valid := m.ctx.Err() == nil && !m.bootstrapped && m.bootstrapCall == nil && m.bootstrapRetryGeneration == generation && !m.bootstrapNextRetryAt.IsZero() && !m.now().Before(m.bootstrapNextRetryAt)
	if !valid {
		m.mu.Unlock()
		return
	}
	call := m.startBootstrapLocked()
	m.mu.Unlock()
	go m.runBootstrap(call)
}

// Only allowlisted metadata crosses the API/log boundary. Provider errors can
// contain credential bytes or paths and must never be rendered there.
type codexBootstrapFailure struct {
	reason    string
	retryable bool
}

func (e *codexBootstrapFailure) Error() string { return e.reason }

func bootstrapFailure(reason string, retryable bool) error {
	return &codexBootstrapFailure{reason: reason, retryable: retryable}
}

func bootstrapStorageFailure(err error) error {
	// Filesystem validation failures are plain errors, deliberately fail closed.
	// Only recognizable I/O failures are eligible for another attempt. The
	// secure-file helpers summarize their cause behind an opaque, path-free
	// message but preserve the underlying os error through Unwrap, so a transient
	// disk or I/O fault is not misclassified as unsafe storage and left blocked
	// until AO restarts.
	var pathErr *os.PathError
	var linkErr *os.LinkError
	isIOFault := errors.As(err, &pathErr) || errors.As(err, &linkErr)
	retryable := isIOFault && !errors.Is(err, os.ErrPermission) && !errors.Is(err, os.ErrExist) && !errors.Is(err, syscall.ENOTDIR) && !errors.Is(err, syscall.ELOOP)
	if !retryable {
		return bootstrapFailure("account_storage_unsafe", false)
	}
	return bootstrapFailure("account_storage_unavailable", true)
}

func bootstrapStateFailure(err error) error {
	if errors.Is(err, ports.ErrCodexGlobalAccountChanged) {
		return bootstrapFailure("global_account_changed", false)
	}
	return bootstrapFailure("account_state_unavailable", !errors.Is(err, context.Canceled))
}

func (m *codexAccountManager) bootstrapInner() error {
	if err := cleanupPendingCredentialHomes(m.pendingRoot); err != nil {
		return bootstrapStorageFailure(err)
	}
	if err := cleanupPendingCredentialHomes(m.switchStagingRoot); err != nil {
		return bootstrapStorageFailure(err)
	}
	if err := m.catalog.refresh(); err != nil {
		return bootstrapStorageFailure(err)
	}
	if m.stateStore != nil {
		ctx, cancel := context.WithTimeout(m.ctx, codexAccountAuthTimeout)
		defer cancel()
		active, ok, err := m.stateStore.GetCodexActiveAccount(ctx)
		if err != nil {
			return bootstrapStateFailure(err)
		}
		if ok {
			m.mu.Lock()
			m.active = active
			m.mu.Unlock()
		}
	}
	err := m.reconcileGlobal(m.ctx)
	if err == nil {
		return nil
	}
	var failure *codexBootstrapFailure
	if errors.As(err, &failure) {
		return err
	}
	if errors.Is(err, ports.ErrCodexGlobalAccountChanged) {
		return bootstrapFailure("global_account_changed", false)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return bootstrapFailure("account_reconciliation_timeout", true)
	}
	return bootstrapStorageFailure(err)
}

func (m *codexAccountManager) reconcileGlobal(ctx context.Context) error {
	m.mu.Lock()
	call := m.reconcile
	if call == nil {
		call = &accountReconcileCall{done: make(chan struct{})}
		m.reconcile = call
		go m.runGlobalReconciliation(call)
	}
	m.mu.Unlock()
	select {
	case <-call.done:
		return call.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *codexAccountManager) runGlobalReconciliation(call *accountReconcileCall) {
	ctx, cancel := context.WithTimeout(m.ctx, codexAccountReconcileTimeout)
	defer cancel()
	call.err = m.reconcileGlobalInner(ctx)
	m.mu.Lock()
	if m.reconcile == call {
		m.reconcile = nil
	}
	close(call.done)
	m.mu.Unlock()
	m.publish()
}

func (m *codexAccountManager) reconcileGlobalInner(ctx context.Context) error {
	exclusive, err := m.acquireGlobalMutation(ctx)
	if err != nil {
		return err
	}
	if exclusive != nil {
		defer exclusive.Release()
	}
	release, err := m.acquireAccountMutation(ctx)
	if err != nil {
		return err
	}
	defer release()
	if m.factory == nil || m.globalHome == "" {
		return bootstrapFailure("account_discovery_unavailable", false)
	}
	select {
	case m.processes <- struct{}{}:
		defer func() { <-m.processes }()
	case <-ctx.Done():
		return ctx.Err()
	}
	readCtx, cancel := context.WithTimeout(ctx, codexAccountAuthTimeout)
	defer cancel()
	client, err := m.factory.Open(readCtx, ports.CodexAccountContext{Home: m.globalHome, Managed: false})
	if err != nil {
		m.setGlobalAuthenticationFailure(failedAuthentication(m.now(), domain.AgentReadinessReasonAuthCheckFailed, "Authentication check failed."))
		m.setUnmanagedGlobal("Device Codex account", domain.CodexAuthMethodUnknown, nil, "global_account_unverified", "AO could not verify the device's current Codex account.")
		return bootstrapFailure("account_client_unavailable", true)
	}
	observation, readErr := client.Read(readCtx, false)
	_ = client.Close()
	if readErr != nil || observation.Authentication == domain.AgentAuthenticationUnknown {
		m.setGlobalAuthenticationFailure(failedAuthentication(m.now(), domain.AgentReadinessReasonAuthCheckInconclusive, "Authentication check was inconclusive."))
		m.setUnmanagedGlobal("Device Codex account", observation.Method, observation.Email, "global_account_unverified", "AO could not verify the device's current Codex account.")
		return bootstrapFailure("account_read_inconclusive", true)
	}
	if observation.Authentication == domain.AgentAuthenticationUnauthorized {
		m.setGlobalAuthentication(accountAuthenticationObservation(m.now(), observation.Authentication))
		m.mu.Lock()
		m.unmanaged = nil
		m.mu.Unlock()
		if err := m.setActivePointer(ctx, ""); err != nil {
			return bootstrapStateFailure(err)
		}
		return nil
	}
	if observation.Authentication != domain.AgentAuthenticationAuthorized && observation.Authentication != domain.AgentAuthenticationNotApplicable {
		m.setGlobalAuthenticationFailure(failedAuthentication(m.now(), domain.AgentReadinessReasonAuthCheckInconclusive, "Authentication check was inconclusive."))
		m.setUnmanagedGlobal("Device Codex account", observation.Method, observation.Email, "global_account_unverified", "AO could not verify the device's current Codex account.")
		return nil
	}
	m.setGlobalAuthentication(accountAuthenticationObservation(m.now(), observation.Authentication))
	globalCredential, credentialErr := readOpaqueCredential(m.globalCredentialPath())
	if credentialErr != nil || m.validateGlobalCredentialStore() != nil {
		m.setUnmanagedGlobal(accountLabel("device", observation.Method, observation.Email), observation.Method, observation.Email, "global_credential_store_unsupported", "This Codex account is active on the device, but its credential store cannot be switched safely.")
		return nil
	}
	credentialObservation, credentialErr := m.verifyOpaqueGlobalCredential(ctx, globalCredential)
	if credentialErr != nil || !codexObservationsMatch(observation, credentialObservation) {
		m.setUnmanagedGlobal(accountLabel("device", observation.Method, observation.Email), observation.Method, observation.Email, "global_credential_store_unsupported", "This Codex account is active on the device, but its credential store cannot be switched safely.")
		return nil
	}
	observation = credentialObservation
	if err := m.catalog.refresh(); err != nil {
		return err
	}
	record, found := m.matchGlobalAccount(observation, globalCredential)
	if !found {
		if !distinguishableCodexIdentity(observation) {
			m.setUnmanagedGlobal(accountLabel("device", observation.Method, observation.Email), observation.Method, observation.Email, "global_account_identity_unverified", "AO cannot safely distinguish this device Codex account from saved accounts.")
			return nil
		}
		pendingID := m.newID()
		pendingDir, home, createErr := createPendingCredentialHome(m.pendingRoot, pendingID)
		if createErr != nil {
			return createErr
		}
		defer func() { _ = os.RemoveAll(pendingDir) }()
		if err := writePrivateFileAtomic(filepath.Join(home, codexCredentialFilename), globalCredential); err != nil {
			return err
		}
		verifyCtx, verifyCancel := context.WithTimeout(ctx, codexAccountAuthTimeout)
		verifiedClient, openErr := m.factory.Open(verifyCtx, ports.CodexAccountContext{Home: home, Managed: true})
		if openErr != nil {
			verifyCancel()
			return bootstrapFailure("account_client_unavailable", true)
		}
		checked, checkErr := verifiedClient.Read(verifyCtx, true)
		_ = verifiedClient.Close()
		verifyCancel()
		if checkErr != nil || (checked.Authentication != domain.AgentAuthenticationAuthorized && checked.Authentication != domain.AgentAuthenticationNotApplicable) {
			m.setUnmanagedGlobal(accountLabel("device", observation.Method, observation.Email), observation.Method, observation.Email, "global_account_unverified", "AO could not verify the device's current Codex account for import.")
			return nil
		}
		record, err = m.catalog.commitPending(pendingDir, checked)
		if err != nil {
			return err
		}
		observation = checked
	}
	if err := writePrivateFileAtomic(filepath.Join(record.Home, codexCredentialFilename), globalCredential); err != nil {
		return err
	}
	if err := m.catalog.updateVerifiedDescriptor(record.Snapshot.ID, observation); err != nil {
		return err
	}
	if err := m.catalog.refresh(); err != nil {
		return err
	}
	if latestGlobal, latestErr := readOpaqueCredential(m.globalCredentialPath()); latestErr != nil || !bytes.Equal(latestGlobal, globalCredential) {
		m.setUnmanagedGlobal(accountLabel("device", observation.Method, observation.Email), observation.Method, observation.Email, "global_account_changed", "The device Codex account changed while AO was reconciling it.")
		return ports.ErrCodexGlobalAccountChanged
	}
	m.catalog.updateSnapshot(record.Snapshot.ID, func(snapshot *domain.CodexAccountSnapshot) {
		snapshot.Authentication = accountAuthenticationObservation(m.now(), observation.Authentication)
	})
	if err := m.setActivePointer(ctx, record.Snapshot.ID); err != nil {
		return bootstrapStateFailure(err)
	}
	m.mu.Lock()
	m.unmanaged = nil
	m.mu.Unlock()
	return nil
}

func (m *codexAccountManager) matchGlobalAccount(observation ports.CodexAccountObservation, globalCredential []byte) (codexAccountRecord, bool) {
	records, err := m.catalog.recordsFor(nil)
	if err != nil {
		return codexAccountRecord{}, false
	}
	m.mu.Lock()
	activeID := m.active.AccountID
	m.mu.Unlock()
	if active, ok := m.catalog.record(activeID); ok && (active.Snapshot.Status == domain.CodexAccountStatusValid || active.Snapshot.Status == domain.CodexAccountStatusSignedOut) {
		if sameCodexStructuredIdentity(active.Snapshot, observation) {
			return active, true
		}
	}
	if distinguishableCodexIdentity(observation) {
		var matched *codexAccountRecord
		for i := range records {
			record := records[i]
			if (record.Snapshot.Status == domain.CodexAccountStatusValid || record.Snapshot.Status == domain.CodexAccountStatusSignedOut) && sameCodexStructuredIdentity(record.Snapshot, observation) && (matched == nil || record.VerifiedAt.After(matched.VerifiedAt)) {
				candidate := record
				matched = &candidate
			}
		}
		if matched != nil {
			return *matched, true
		}
		return codexAccountRecord{}, false
	}
	var opaqueMatch *codexAccountRecord
	for i := range records {
		record := records[i]
		if record.Snapshot.Status != domain.CodexAccountStatusValid || !credentialMatchesRecord(record, globalCredential) {
			continue
		}
		if opaqueMatch != nil {
			return codexAccountRecord{}, false
		}
		candidate := record
		opaqueMatch = &candidate
	}
	if opaqueMatch != nil {
		return *opaqueMatch, true
	}
	return codexAccountRecord{}, false
}

func credentialMatchesRecord(record codexAccountRecord, credential []byte) bool {
	stored, err := readOpaqueCredential(filepath.Join(record.Home, codexCredentialFilename))
	return err == nil && bytes.Equal(stored, credential)
}

func (m *codexAccountManager) observationAndCredentialIdentifyRecord(record codexAccountRecord, observation ports.CodexAccountObservation, credential []byte) bool {
	if distinguishableCodexIdentity(observation) {
		return sameCodexStructuredIdentity(record.Snapshot, observation)
	}
	matched, ok := m.matchGlobalAccount(observation, credential)
	return ok && matched.Snapshot.ID == record.Snapshot.ID
}

func distinguishableCodexIdentity(observation ports.CodexAccountObservation) bool {
	return observation.Method != domain.CodexAuthMethodUnknown && observation.Email != nil && safeAccountEmail(*observation.Email)
}

func sameCodexStructuredIdentity(snapshot domain.CodexAccountSnapshot, observation ports.CodexAccountObservation) bool {
	if snapshot.AuthMethod != observation.Method || !distinguishableCodexIdentity(observation) || snapshot.AccountEmail == nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(*snapshot.AccountEmail), strings.TrimSpace(*observation.Email))
}

func codexObservationMatchesAccount(snapshot domain.CodexAccountSnapshot, observation ports.CodexAccountObservation) bool {
	if sameCodexStructuredIdentity(snapshot, observation) {
		return true
	}
	return snapshot.AuthMethod == domain.CodexAuthMethodAPIKey && observation.Method == domain.CodexAuthMethodAPIKey
}

func codexObservationsMatch(left, right ports.CodexAccountObservation) bool {
	if left.Method != right.Method {
		return false
	}
	if left.Email != nil && right.Email != nil && safeAccountEmail(*left.Email) && safeAccountEmail(*right.Email) {
		return strings.EqualFold(strings.TrimSpace(*left.Email), strings.TrimSpace(*right.Email))
	}
	return left.Method == domain.CodexAuthMethodAPIKey
}

func (m *codexAccountManager) verifyOpaqueGlobalCredential(ctx context.Context, credential []byte) (ports.CodexAccountObservation, error) {
	pendingID := m.newID()
	pendingDir, home, err := createPendingCredentialHome(m.pendingRoot, pendingID)
	if err != nil {
		return ports.CodexAccountObservation{}, err
	}
	defer func() { _ = os.RemoveAll(pendingDir) }()
	if err := writePrivateFileAtomic(filepath.Join(home, codexCredentialFilename), credential); err != nil {
		return ports.CodexAccountObservation{}, err
	}
	verifyCtx, cancel := context.WithTimeout(ctx, codexAccountAuthTimeout)
	defer cancel()
	client, err := m.factory.Open(verifyCtx, ports.CodexAccountContext{Home: home, Managed: true})
	if err != nil {
		return ports.CodexAccountObservation{}, err
	}
	// Reconciliation only needs to prove that the opaque global credential is
	// usable from a file-backed home. A proactive refresh here can race Codex's
	// live global credential and rotate the copied refresh token, incorrectly
	// classifying the device account as unmanaged. Strict refresh remains part
	// of login and switch admission, where no duplicate live credential is being
	// introduced.
	observation, readErr := client.Read(verifyCtx, false)
	_ = client.Close()
	if readErr != nil {
		return ports.CodexAccountObservation{}, readErr
	}
	if observation.Authentication != domain.AgentAuthenticationAuthorized && observation.Authentication != domain.AgentAuthenticationNotApplicable {
		return ports.CodexAccountObservation{}, errors.New("global Codex credential could not be verified in a file-backed store")
	}
	return observation, nil
}

func (m *codexAccountManager) setUnmanagedGlobal(label string, method domain.CodexAuthMethod, email *string, code, reason string) {
	m.mu.Lock()
	m.unmanaged = &domain.CodexUnmanagedGlobalAccount{Label: label, AuthMethod: method, AccountEmail: email, ReasonCode: code, Reason: reason}
	m.mu.Unlock()
}

func (m *codexAccountManager) setGlobalAuthentication(observation domain.AgentAuthenticationObservation) {
	m.mu.Lock()
	m.globalAuth = observation
	m.mu.Unlock()
}

func (m *codexAccountManager) setGlobalAuthenticationFailure(observation domain.AgentAuthenticationObservation) {
	m.mu.Lock()
	preserveAuthenticationFailure(&m.globalAuth, observation)
	m.mu.Unlock()
}

func (m *codexAccountManager) setActivePointer(ctx context.Context, accountID string) error {
	m.mu.Lock()
	current := m.active
	m.mu.Unlock()
	if current.AccountID == accountID {
		return nil
	}
	now := m.now()
	active, _, err := m.commitActivePointer(ctx, accountID, current, now)
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.active = active
	m.mu.Unlock()
	return nil
}

type activePointerCommitOutcome uint8

const (
	activePointerUnchanged activePointerCommitOutcome = iota
	activePointerCommitted
	activePointerUncertain
)

func (m *codexAccountManager) commitActivePointer(
	ctx context.Context,
	accountID string,
	current domain.CodexActiveAccount,
	at time.Time,
) (domain.CodexActiveAccount, activePointerCommitOutcome, error) {
	if m.stateStore == nil {
		return domain.CodexActiveAccount{
			AccountID: accountID, Revision: current.Revision + 1, ActivatedAt: at, UpdatedAt: at,
		}, activePointerCommitted, nil
	}
	active, err := m.stateStore.SetCodexActiveAccount(ctx, accountID, current.Revision, at)
	if err == nil {
		return active, activePointerCommitted, nil
	}
	settleCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), codexAccountAuthTimeout)
	defer cancel()
	settled, found, readErr := m.stateStore.GetCodexActiveAccount(settleCtx)
	if readErr != nil {
		return domain.CodexActiveAccount{}, activePointerUncertain, errors.Join(err, readErr)
	}
	if found && settled.AccountID == accountID && settled.Revision == current.Revision+1 {
		return settled, activePointerCommitted, nil
	}
	if (found && settled.AccountID == current.AccountID && settled.Revision == current.Revision) ||
		(!found && current.Revision == 0) {
		return domain.CodexActiveAccount{}, activePointerUnchanged, err
	}
	return settled, activePointerUncertain, errors.Join(err, ports.ErrCodexGlobalAccountChanged)
}

func mapUnknownCodexAccount(err error) error {
	var unknown unknownCodexAccountError
	if errors.As(err, &unknown) {
		return apierr.Invalid("INVALID_CODEX_ACCOUNT_ID", "Unknown Codex account", map[string]any{"accountId": unknown.id})
	}
	return err
}
