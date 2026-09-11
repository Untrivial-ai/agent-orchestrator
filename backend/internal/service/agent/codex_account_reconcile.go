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

// Account-store initialization covers AO-owned local state only. Device-global
// discovery is deliberately a separate, repeatable operation below.
func (m *codexAccountManager) waitAccountStore(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	if m.accountStoreReady {
		m.mu.Unlock()
		return nil
	}
	call := m.accountStoreCall
	if call == nil {
		if m.accountStoreErr != nil {
			var failure *codexAccountStoreFailure
			if !errors.As(m.accountStoreErr, &failure) || !failure.retryable || m.now().Before(m.accountStoreNextRetry) {
				err := m.accountStoreErr
				m.mu.Unlock()
				return err
			}
		}
		if err := m.ctx.Err(); err != nil {
			m.mu.Unlock()
			return err
		}
		call = &accountReconcileCall{done: make(chan struct{})}
		m.accountStoreCall = call
		go m.runAccountStoreInitialization(call)
	}
	m.mu.Unlock()
	select {
	case <-call.done:
		return call.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *codexAccountManager) runAccountStoreInitialization(call *accountReconcileCall) {
	err := m.initializeAccountStore()
	m.mu.Lock()
	m.accountStoreErr = err
	m.accountStoreReady = err == nil
	if err != nil {
		m.accountStoreFailures++
		// A bounded cooldown prevents repeated local callers from hammering a
		// temporarily unavailable disk or database.
		delay := time.Second << min(m.accountStoreFailures-1, 5)
		m.accountStoreNextRetry = m.now().Add(delay)
		var failure *codexAccountStoreFailure
		if errors.As(err, &failure) {
			m.logger.Warn("Codex account store initialization failed", "reasonCode", failure.reason, "retryable", failure.retryable)
		}
	}
	call.err = err
	m.accountStoreCall = nil
	close(call.done)
	m.mu.Unlock()
	m.publish()
}

// Only allowlisted metadata crosses the API/log boundary. Provider errors can
// contain credential bytes or paths and must never be rendered there.
type codexAccountStoreFailure struct {
	reason    string
	retryable bool
}

func (e *codexAccountStoreFailure) Error() string { return e.reason }

func accountStoreFailure(reason string, retryable bool) error {
	return &codexAccountStoreFailure{reason: reason, retryable: retryable}
}

func accountStoreStorageFailure(err error) error {
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
		return accountStoreFailure("account_storage_unsafe", false)
	}
	return accountStoreFailure("account_storage_unavailable", true)
}

func accountStoreStateFailure(err error) error {
	if errors.Is(err, ports.ErrCodexGlobalAccountChanged) {
		return accountStoreFailure("global_account_changed", false)
	}
	return accountStoreFailure("account_state_unavailable", !errors.Is(err, context.Canceled))
}

func (m *codexAccountManager) initializeAccountStore() error {
	if err := cleanupPendingCredentialHomes(m.pendingRoot); err != nil {
		return accountStoreStorageFailure(err)
	}
	if err := cleanupPendingCredentialHomes(m.switchStagingRoot); err != nil {
		return accountStoreStorageFailure(err)
	}
	if err := m.catalog.refresh(); err != nil {
		return accountStoreStorageFailure(err)
	}
	if m.stateStore != nil {
		ctx, cancel := context.WithTimeout(m.ctx, codexAccountAuthTimeout)
		defer cancel()
		active, ok, err := m.stateStore.GetCodexActiveAccount(ctx)
		if err != nil {
			return accountStoreStateFailure(err)
		}
		if ok {
			m.mu.Lock()
			m.active = active
			m.mu.Unlock()
		}
	}
	return nil
}

// codexDeviceReconciliationFailure contains only a safe category. The wrapped
// provider error is intentionally not retained because it can contain tokens
// or credential paths.
type codexDeviceReconciliationFailure struct {
	reason    string
	retryable bool
}

func (e *codexDeviceReconciliationFailure) Error() string { return e.reason }

func deviceReconciliationFailure(reason string, retryable bool) error {
	return &codexDeviceReconciliationFailure{reason: reason, retryable: retryable}
}

func deviceReconciliationStateFailure(err error) error {
	if errors.Is(err, ports.ErrCodexGlobalAccountChanged) {
		return deviceReconciliationFailure("global_account_changed", true)
	}
	return deviceReconciliationFailure("account_state_unavailable", !errors.Is(err, context.Canceled))
}

func deviceReconciliationStorageFailure(err error) error {
	var localFailure *codexAccountStoreFailure
	if errors.As(accountStoreStorageFailure(err), &localFailure) {
		return deviceReconciliationFailure(localFailure.reason, localFailure.retryable)
	}
	return deviceReconciliationFailure("account_reconciliation_unavailable", true)
}

func (m *codexAccountManager) reconcileGlobal(ctx context.Context) error {
	return m.reconcileGlobalWithPolicy(ctx, false)
}

func (m *codexAccountManager) reconcileGlobalWithPolicy(ctx context.Context, force bool) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	started := false
	m.mu.Lock()
	call := m.reconcile
	if call == nil {
		if !force && m.reconciliation.Status == domain.CodexDeviceReconciliationTemporarilyUnavailable && m.reconciliation.NextRetryAt != nil && m.now().Before(*m.reconciliation.NextRetryAt) {
			reason := m.reconciliation.ReasonCode
			m.mu.Unlock()
			return deviceReconciliationFailure(reason, true)
		}
		call = &accountReconcileCall{done: make(chan struct{})}
		m.reconcile = call
		now := m.now()
		m.reconciliation.Status = domain.CodexDeviceReconciliationChecking
		m.reconciliation.ActiveAccountVerified = false
		m.reconciliation.ReasonCode = "checking"
		m.reconciliation.Retryable = false
		m.reconciliation.AttemptedAt = timePointer(now)
		m.reconciliation.NextRetryAt = nil
		// An unmanaged account is a conclusion from one completed observation,
		// not durable truth. Clear it while a fresh observation is pending so a
		// transient failure cannot keep showing a stale device-account warning.
		m.unmanaged = nil
		started = true
		go m.runGlobalReconciliation(call)
	}
	m.mu.Unlock()
	if started {
		m.publish()
	}
	select {
	case <-call.done:
		return call.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// requestGlobalReconciliationIfNeeded schedules background device discovery
// only when no fresh result or existing attempt can answer the request.
func (m *codexAccountManager) requestGlobalReconciliationIfNeeded() {
	m.mu.Lock()
	if m.reconcile != nil || m.reconcileRequested || m.reconcileScheduled {
		m.mu.Unlock()
		return
	}
	now := m.now()
	needed := m.reconciliation.Status == domain.CodexDeviceReconciliationNotChecked ||
		(m.reconciliation.Status == domain.CodexDeviceReconciliationVerified &&
			(m.reconciliation.VerifiedAt == nil || now.Sub(*m.reconciliation.VerifiedAt) >= codexAccountDisplayTTL))
	if !needed {
		m.mu.Unlock()
		return
	}
	m.reconcileRequested = true
	m.mu.Unlock()

	go func() {
		_ = m.reconcileGlobal(m.ctx)
		m.mu.Lock()
		m.reconcileRequested = false
		m.mu.Unlock()
	}()
}

func (m *codexAccountManager) runGlobalReconciliation(call *accountReconcileCall) {
	ctx, cancel := context.WithTimeout(m.ctx, codexAccountReconcileTimeout)
	defer cancel()
	call.err = m.reconcileGlobalInner(ctx)
	now := m.now()
	var schedule bool
	var delay time.Duration
	m.mu.Lock()
	if call.err == nil {
		m.reconcileFailures = 0
		m.reconciliation.Retryable = false
		m.reconciliation.NextRetryAt = nil
		if m.unmanaged != nil {
			m.reconciliation.Status = domain.CodexDeviceReconciliationBlocked
			m.reconciliation.ActiveAccountVerified = false
			m.reconciliation.ReasonCode = m.unmanaged.ReasonCode
		} else {
			m.reconciliation.Status = domain.CodexDeviceReconciliationVerified
			m.reconciliation.ActiveAccountVerified = m.active.AccountID != ""
			m.reconciliation.ReasonCode = "verified"
			m.reconciliation.VerifiedAt = timePointer(now)
		}
	} else if !errors.Is(call.err, context.Canceled) || m.ctx.Err() == nil {
		failure := classifyDeviceReconciliationFailure(call.err)
		m.reconciliation.ActiveAccountVerified = false
		m.reconciliation.ReasonCode = failure.reason
		m.reconciliation.Retryable = failure.retryable
		if failure.retryable {
			m.reconciliation.Status = domain.CodexDeviceReconciliationTemporarilyUnavailable
			m.reconcileFailures++
			delay = time.Second << min(m.reconcileFailures-1, 5)
			next := now.Add(delay)
			m.reconciliation.NextRetryAt = timePointer(next)
			schedule = !m.reconcileScheduled
			if schedule {
				m.reconcileScheduled = true
			}
		} else {
			m.reconciliation.Status = domain.CodexDeviceReconciliationBlocked
			m.reconciliation.NextRetryAt = nil
		}
		m.logger.Warn("Codex device reconciliation failed", "reasonCode", failure.reason, "retryable", failure.retryable)
	}
	if m.reconcile == call {
		m.reconcile = nil
	}
	close(call.done)
	m.mu.Unlock()
	m.publish()
	if schedule {
		m.scheduleGlobalReconciliation(delay)
	}
}

func classifyDeviceReconciliationFailure(err error) *codexDeviceReconciliationFailure {
	var failure *codexDeviceReconciliationFailure
	if errors.As(err, &failure) {
		return failure
	}
	if errors.Is(err, ports.ErrCodexGlobalAccountChanged) {
		return &codexDeviceReconciliationFailure{reason: "global_account_changed", retryable: true}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &codexDeviceReconciliationFailure{reason: "account_reconciliation_timeout", retryable: true}
	}
	return &codexDeviceReconciliationFailure{reason: "account_reconciliation_unavailable", retryable: true}
}

func (m *codexAccountManager) scheduleGlobalReconciliation(delay time.Duration) {
	go func() {
		select {
		case <-m.after(delay):
			m.mu.Lock()
			m.reconcileScheduled = false
			shouldRetry := m.reconciliation.Status == domain.CodexDeviceReconciliationTemporarilyUnavailable
			m.mu.Unlock()
			if shouldRetry {
				_ = m.reconcileGlobalWithPolicy(m.ctx, true)
			}
		case <-m.ctx.Done():
			m.mu.Lock()
			m.reconcileScheduled = false
			m.mu.Unlock()
		}
	}()
}

func timePointer(value time.Time) *time.Time {
	result := value
	return &result
}

func (m *codexAccountManager) markDeviceReconciledLocked(active bool, at time.Time) {
	m.reconcileFailures = 0
	m.reconciliation.Status = domain.CodexDeviceReconciliationVerified
	m.reconciliation.ActiveAccountVerified = active
	m.reconciliation.ReasonCode = "verified"
	m.reconciliation.Retryable = false
	m.reconciliation.AttemptedAt = timePointer(at)
	m.reconciliation.VerifiedAt = timePointer(at)
	m.reconciliation.NextRetryAt = nil
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
		return deviceReconciliationFailure("account_discovery_unavailable", false)
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
		return deviceReconciliationFailure("account_client_unavailable", true)
	}
	observation, readErr := client.Read(readCtx, false)
	_ = client.Close()
	if readErr != nil || observation.Authentication == domain.AgentAuthenticationUnknown {
		m.setGlobalAuthenticationFailure(failedAuthentication(m.now(), domain.AgentReadinessReasonAuthCheckInconclusive, "Authentication check was inconclusive."))
		return deviceReconciliationFailure("account_read_inconclusive", true)
	}
	if observation.Authentication == domain.AgentAuthenticationUnauthorized {
		m.setGlobalAuthentication(accountAuthenticationObservation(m.now(), observation.Authentication))
		m.mu.Lock()
		m.unmanaged = nil
		m.mu.Unlock()
		if err := m.setActivePointer(ctx, ""); err != nil {
			return deviceReconciliationStateFailure(err)
		}
		return nil
	}
	if observation.Authentication != domain.AgentAuthenticationAuthorized && observation.Authentication != domain.AgentAuthenticationNotApplicable {
		m.setGlobalAuthenticationFailure(failedAuthentication(m.now(), domain.AgentReadinessReasonAuthCheckInconclusive, "Authentication check was inconclusive."))
		return deviceReconciliationFailure("account_read_inconclusive", true)
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
		return deviceReconciliationStorageFailure(err)
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
			return deviceReconciliationStorageFailure(createErr)
		}
		defer func() { _ = os.RemoveAll(pendingDir) }()
		if err := writePrivateFileAtomic(filepath.Join(home, codexCredentialFilename), globalCredential); err != nil {
			return deviceReconciliationStorageFailure(err)
		}
		verifyCtx, verifyCancel := context.WithTimeout(ctx, codexAccountAuthTimeout)
		verifiedClient, openErr := m.factory.Open(verifyCtx, ports.CodexAccountContext{Home: home, Managed: true})
		if openErr != nil {
			verifyCancel()
			return deviceReconciliationFailure("account_client_unavailable", true)
		}
		// Reconciliation identifies and imports the device account. Remote token
		// validity is checked separately through a protected account call so a
		// refresh-only account/read result cannot block local account management.
		checked, checkErr := verifiedClient.Read(verifyCtx, false)
		_ = verifiedClient.Close()
		verifyCancel()
		if checkErr != nil || (checked.Authentication != domain.AgentAuthenticationAuthorized && checked.Authentication != domain.AgentAuthenticationNotApplicable) {
			m.setUnmanagedGlobal(accountLabel("device", observation.Method, observation.Email), observation.Method, observation.Email, "global_account_unverified", "AO could not verify the device's current Codex account for import.")
			return nil
		}
		record, err = m.catalog.commitPending(pendingDir, checked)
		if err != nil {
			return deviceReconciliationStorageFailure(err)
		}
		observation = checked
	}
	// Compared before the copy below overwrites it: byte-identical material means
	// any launch verification AO already holds still describes this account.
	saved, savedErr := readOpaqueCredential(filepath.Join(record.Home, codexCredentialFilename))
	credentialChanged := savedErr != nil || !bytes.Equal(saved, globalCredential)
	if err := writePrivateFileAtomic(filepath.Join(record.Home, codexCredentialFilename), globalCredential); err != nil {
		return deviceReconciliationStorageFailure(err)
	}
	if err := m.catalog.updateVerifiedDescriptor(record.Snapshot.ID, observation); err != nil {
		return deviceReconciliationStorageFailure(err)
	}
	if err := m.catalog.refresh(); err != nil {
		return deviceReconciliationStorageFailure(err)
	}
	if latestGlobal, latestErr := readOpaqueCredential(m.globalCredentialPath()); latestErr != nil || !bytes.Equal(latestGlobal, globalCredential) {
		return deviceReconciliationFailure("global_account_changed", true)
	}
	// Reconciliation identifies the device account with a non-refresh read, so its
	// result is discovery only and must not present an account the launch path
	// has already rejected as signed in again.
	m.applyDiscoveryAuthentication(record.Snapshot.ID, accountAuthenticationObservation(m.now(), observation.Authentication), credentialChanged)
	if err := m.setActivePointer(ctx, record.Snapshot.ID); err != nil {
		return deviceReconciliationStateFailure(err)
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
	// classifying the device account as unmanaged. Login and switch admission use
	// a protected account call, refreshing only after an explicit token rejection.
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
