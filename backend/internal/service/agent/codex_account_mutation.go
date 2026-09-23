package agent

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func (m *codexAccountManager) logout(ctx context.Context, accountID string) error {
	accountID = strings.TrimSpace(accountID)
	active := m.activeAccountID() == accountID
	if active {
		exclusive, err := m.acquireGlobalMutation(ctx)
		if err != nil {
			return err
		}
		if exclusive != nil {
			defer exclusive.Release()
		}
	}
	release, err := m.acquireAccountMutation(ctx)
	if err != nil {
		return err
	}
	defer release()

	record, ok := m.catalog.record(accountID)
	if !ok || (record.Snapshot.Status != domain.CodexAccountStatusValid && record.Snapshot.Status != domain.CodexAccountStatusSignedOut) {
		return apierr.NotFound("CODEX_ACCOUNT_NOT_FOUND", "Codex account not found")
	}
	if record.Snapshot.Status == domain.CodexAccountStatusSignedOut {
		return nil
	}
	active = m.activeAccountID() == accountID
	credentialPath := filepath.Join(record.Home, codexCredentialFilename)
	logoutHome := record.Home
	logoutCredentialPath := credentialPath
	if active {
		globalCredential, admitted, globalErr := readCodexDeviceFileState(m.globalCredentialPath(), true)
		if globalErr != nil {
			return apierr.Conflict("CODEX_ACCOUNT_LOGOUT_UNCONFIRMED", "Codex could not safely log out this account", nil)
		}
		if admitted.exists {
			identity, identityErr := parseCodexCredentialIdentity(globalCredential)
			matched, match := m.matchGlobalCredentialForReconciliation(globalCredential, identity, identityErr)
			latest, latestState, latestErr := readCodexDeviceFileState(m.globalCredentialPath(), false)
			if match != codexCredentialMatchManaged || matched.Snapshot.ID != accountID || latestErr != nil ||
				!sameCodexFileState(admitted, latestState) || !bytes.Equal(globalCredential, latest) {
				return apierr.Conflict("CODEX_GLOBAL_ACCOUNT_CHANGED", "The device Codex account changed", nil)
			}
			logoutHome = m.globalHome
			logoutCredentialPath = m.globalCredentialPath()
		}
	}

	readLogoutCredential := readCodexFileState
	if canonicalPath(logoutHome) == m.globalHome {
		readLogoutCredential = readCodexDeviceFileState
	}
	_, credentialState, credentialErr := readLogoutCredential(logoutCredentialPath, true)
	if credentialErr != nil {
		return apierr.Conflict("CODEX_ACCOUNT_LOGOUT_UNCONFIRMED", "Codex could not safely log out this account", nil)
	}
	if credentialState.exists {
		logoutCtx, cancel := context.WithTimeout(ctx, codexAccountAuthTimeout)
		client, openErr := m.factory.Open(logoutCtx, ports.CodexAccountContext{Home: logoutHome, Managed: logoutHome != m.globalHome})
		if openErr != nil {
			cancel()
			return apierr.Conflict("CODEX_ACCOUNT_LOGOUT_UNCONFIRMED", "Couldn't log out. Try again.", nil)
		}
		logoutErr := client.Logout(logoutCtx)
		_ = client.Close()
		cancel()
		_, after, afterErr := readLogoutCredential(logoutCredentialPath, true)
		if errors.Is(logoutErr, ports.ErrCodexAccountLogoutUnsupported) {
			return apierr.NotImplemented("CODEX_ACCOUNT_LOGOUT_UNSUPPORTED", "Update Codex to log out this account")
		}
		if afterErr != nil || after.exists {
			return apierr.Conflict("CODEX_ACCOUNT_LOGOUT_UNCONFIRMED", "Couldn't log out. Try again.", nil)
		}
		// If Codex removed the credential before its transport reported an error,
		// local logout is already committed and must never be rolled back.
		if logoutErr != nil {
			m.logger.Warn("Codex logout completed with an unconfirmed provider result", "accountID", accountID)
		}
	}

	if _, err := m.catalog.markSignedOut(accountID); err != nil {
		return apierr.Conflict("CODEX_ACCOUNT_LOGOUT_UNCONFIRMED", "Codex logged out, but AO could not update the account. Try again.", nil)
	}
	if active {
		m.mu.Lock()
		m.deviceAccountID = ""
		m.deviceCredentialPresent = false
		m.markDeviceReconciledLocked(false, m.now())
		m.mu.Unlock()
	}
	m.clearReauthenticationRequired(accountID)
	m.mu.Lock()
	delete(m.usage, accountID)
	m.mu.Unlock()
	m.capacity.replace(accountID, staticCodexCapacity(domain.CodexCapacityUnknown, domain.CodexCapacityReasonSkippedSignedOut, "Sign in to Codex to see subscription capacity."), "signed_out")
	m.publish()
	return nil
}

func (m *codexAccountManager) deleteAccount(ctx context.Context, accountID string) error {
	accountID = strings.TrimSpace(accountID)
	record, ok := m.catalog.record(accountID)
	if !ok || (record.Snapshot.Status != domain.CodexAccountStatusValid && record.Snapshot.Status != domain.CodexAccountStatusSignedOut) {
		return apierr.NotFound("CODEX_ACCOUNT_NOT_FOUND", "Codex account not found")
	}
	if record.Snapshot.Status == domain.CodexAccountStatusValid {
		if err := m.logout(ctx, accountID); err != nil {
			return err
		}
	}
	release, err := m.acquireAccountMutation(ctx)
	if err != nil {
		return err
	}
	defer release()
	record, ok = m.catalog.record(accountID)
	if !ok || (record.Snapshot.Status != domain.CodexAccountStatusValid && record.Snapshot.Status != domain.CodexAccountStatusSignedOut) {
		return apierr.NotFound("CODEX_ACCOUNT_NOT_FOUND", "Codex account not found")
	}
	if record.Snapshot.Status != domain.CodexAccountStatusSignedOut {
		return apierr.Conflict("CODEX_ACCOUNT_DELETE_REQUIRES_LOGOUT", "Log out of this Codex account before deleting it", nil)
	}
	if m.activeAccountID() == accountID {
		return apierr.Conflict("CODEX_ACCOUNT_DELETE_ACTIVE", "The active Codex account cannot be deleted", nil)
	}
	if err := m.catalog.deleteSignedOut(accountID); err != nil {
		return apierr.Conflict("CODEX_ACCOUNT_DELETE_UNCONFIRMED", "Codex could not safely delete this account", nil)
	}
	m.mu.Lock()
	delete(m.auth, accountID)
	delete(m.usage, accountID)
	m.mu.Unlock()
	return nil
}

func (m *codexAccountManager) activeAccountID() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.reconciliation.ActiveAccountVerified {
		return ""
	}
	return m.deviceAccountID
}

func (m *codexAccountManager) accountMayOwnDeviceCredential(accountID string) bool {
	accountID = strings.TrimSpace(accountID)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.reconciliation.ActiveAccountVerified {
		return accountID != "" && accountID == m.deviceAccountID
	}
	lastKnownDeviceAccountID := m.deferredAccountID
	if lastKnownDeviceAccountID == "" {
		lastKnownDeviceAccountID = m.deviceAccountID
	}
	if lastKnownDeviceAccountID == "" {
		// Without any local ownership result, every saved account is potentially
		// the device owner. Reconcile before choosing a logout home.
		return accountID != ""
	}
	return accountID != "" && accountID == lastKnownDeviceAccountID
}

func (m *codexAccountManager) activateFromCredentialLocked(ctx context.Context, accountID, sourceCredential string, expectedGlobal []byte) error {
	notCommitted := func(err error) error {
		return errors.Join(ports.ErrCodexAccountSwitchNotCommitted, err)
	}
	if err := ctx.Err(); err != nil {
		return notCommitted(err)
	}
	record, ok := m.catalog.record(accountID)
	if !ok || record.Snapshot.Status != domain.CodexAccountStatusValid {
		return notCommitted(apierr.NotFound("CODEX_ACCOUNT_NOT_FOUND", "Codex account not found"))
	}
	targetCredential, err := readOpaqueCredential(sourceCredential)
	if err != nil {
		return notCommitted(err)
	}
	if !localCredentialIdentifiesRecord(record, targetCredential) {
		return notCommitted(apierr.Conflict("CODEX_ACCOUNT_IDENTITY_CHANGED", "The selected Codex credential does not match this account", nil))
	}
	if err := ctx.Err(); err != nil {
		return notCommitted(err)
	}
	globalPath := m.globalCredentialPath()
	previousCredential, previousErr := readDeviceOpaqueCredential(globalPath)
	if previousErr != nil && !errors.Is(previousErr, os.ErrNotExist) {
		return notCommitted(ports.ErrCodexGlobalCredentialStoreUnsupported)
	}
	if expectedGlobal != nil {
		expectsMissing := len(expectedGlobal) == 0
		if (expectsMissing && !errors.Is(previousErr, os.ErrNotExist)) || (!expectsMissing && (previousErr != nil || !bytes.Equal(previousCredential, expectedGlobal))) {
			return notCommitted(ports.ErrCodexGlobalAccountChanged)
		}
	}
	if err := ctx.Err(); err != nil {
		return notCommitted(err)
	}
	if err := writeGlobalCredentialSettled(globalPath, targetCredential); err != nil {
		return err
	}
	currentCredential, currentState, currentErr := readCodexDeviceFileState(globalPath, false)
	latestCredential, latestState, latestErr := readCodexDeviceFileState(globalPath, false)
	if currentErr != nil || latestErr != nil || !sameCodexFileState(currentState, latestState) ||
		!bytes.Equal(currentCredential, latestCredential) || !bytes.Equal(currentCredential, targetCredential) {
		return ports.ErrCodexGlobalAccountChanged
	}
	now := m.now()
	m.mu.Lock()
	m.deviceAccountID = accountID
	m.deferredAccountID = ""
	m.deviceCredentialPresent = true
	m.markDeviceReconciledLocked(true, now)
	m.mu.Unlock()
	if refreshed, readErr := readDeviceOpaqueCredential(globalPath); readErr == nil {
		_ = writePrivateFileAtomic(filepath.Join(record.Home, codexCredentialFilename), refreshed)
	}
	m.invalidate(accountID)
	m.publish()
	return nil
}

func writeGlobalCredentialSettled(path string, data []byte) error {
	err := writeGlobalCredentialAtomic(path, data)
	if err == nil {
		return nil
	}
	var mutationErr *codexFileMutationError
	if errors.As(err, &mutationErr) && mutationErr.committed {
		if protectErr := protectCodexDeviceCredentialFile(path); protectErr != nil {
			return errors.Join(err, protectErr)
		}
	}
	current, readErr := readDeviceOpaqueCredential(path)
	if readErr == nil && bytes.Equal(current, data) {
		return nil
	}
	return errors.Join(err, readErr)
}

func readOpaqueCredential(source string) ([]byte, error) {
	data, _, err := readCodexFileState(source, false)
	return data, err
}

func readDeviceOpaqueCredential(source string) ([]byte, error) {
	data, _, err := readCodexDeviceFileState(source, false)
	return data, err
}

func writeGlobalCredentialAtomic(path string, data []byte) error {
	if len(data) == 0 || len(data) > 8<<20 {
		return errors.New("global Codex credential is empty or too large")
	}
	parent := filepath.Dir(path)
	info, err := os.Lstat(parent)
	if errors.Is(err, os.ErrNotExist) {
		if err := ensurePrivateDirectory(parent); err != nil {
			return err
		}
		info, err = os.Lstat(parent)
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || validateCodexDirectory(parent, false) != nil {
		return ports.ErrCodexGlobalCredentialStoreUnsupported
	}
	replacement, err := prepareCodexDeviceFileReplacementInDirectory(path, data)
	if err != nil {
		return err
	}
	defer replacement.Abort()
	if err := replacement.Commit(); err != nil {
		return err
	}
	if err := protectCodexDeviceCredentialFile(path); err != nil {
		return &codexFileMutationError{err: errors.New("global Codex credential ACL could not be protected"), committed: true}
	}
	return nil
}

func (m *codexAccountManager) globalCredentialPath() string {
	return filepath.Join(m.globalHome, codexCredentialFilename)
}

func (m *codexAccountManager) validateGlobalCredentialStore() error {
	if m.globalHome == "" {
		return ports.ErrCodexGlobalCredentialStoreUnsupported
	}
	// A missing auth.json means that no device account is active; it does not
	// mean that Codex is using a non-file-backed credential store. Validate the
	// path and any credential that is present, while allowing activation to
	// create the file (and, when needed, its private parent directory).
	_, _, err := readCodexDeviceFileState(m.globalCredentialPath(), true)
	if err != nil {
		return ports.ErrCodexGlobalCredentialStoreUnsupported
	}
	return nil
}
