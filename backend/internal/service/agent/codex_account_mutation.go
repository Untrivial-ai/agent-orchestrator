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

	record, ok := m.catalog.record(accountID)
	if !ok || (record.Snapshot.Status != domain.CodexAccountStatusValid && record.Snapshot.Status != domain.CodexAccountStatusSignedOut) {
		return apierr.NotFound("CODEX_ACCOUNT_NOT_FOUND", "Codex account not found")
	}
	if record.Snapshot.Status == domain.CodexAccountStatusSignedOut {
		return nil
	}
	active := m.activeAccountID() == accountID
	credential, err := readOpaqueCredential(filepath.Join(record.Home, codexCredentialFilename))
	if err != nil {
		return apierr.Conflict("CODEX_ACCOUNT_LOGOUT_UNCONFIRMED", "Codex could not safely log out this account", nil)
	}
	observation := ports.CodexAccountObservation{
		Authentication: domain.AgentAuthenticationAuthorized,
		Method:         record.Snapshot.AuthMethod,
		Email:          record.Snapshot.AccountEmail,
	}
	var globalCredential []byte
	var globalState codexFileState
	if active {
		verifyCtx, cancel := context.WithTimeout(ctx, codexAccountAuthTimeout)
		client, openErr := m.factory.Open(verifyCtx, ports.CodexAccountContext{Home: m.globalHome, Managed: false})
		if openErr != nil {
			cancel()
			return apierr.Conflict("CODEX_ACCOUNT_LOGOUT_UNCONFIRMED", "The device Codex account could not be confirmed", nil)
		}
		current, readErr := client.Read(verifyCtx, false)
		_ = client.Close()
		cancel()
		if readErr != nil {
			return apierr.Conflict("CODEX_GLOBAL_ACCOUNT_CHANGED", "The device Codex account changed", nil)
		}
		globalCredential, globalState, err = readCodexFileState(m.globalCredentialPath(), false)
		if err != nil {
			return apierr.Conflict("CODEX_ACCOUNT_LOGOUT_UNCONFIRMED", "The device Codex credential could not be confirmed", nil)
		}
		if !m.observationAndCredentialIdentifyRecord(record, current, globalCredential) {
			return apierr.Conflict("CODEX_GLOBAL_ACCOUNT_CHANGED", "The device Codex account changed", nil)
		}
		_, latestState, latestErr := readCodexFileState(m.globalCredentialPath(), false)
		if latestErr != nil || !sameCodexFileState(globalState, latestState) {
			return apierr.Conflict("CODEX_GLOBAL_ACCOUNT_CHANGED", "The device Codex account changed", nil)
		}
	}
	// The global gate and account-mutation token are both held. Refresh the two
	// durable classifications at the commit boundary rather than relying on the
	// values observed before either lock was acquired.
	record, ok = m.catalog.record(accountID)
	if !ok || record.Snapshot.Status != domain.CodexAccountStatusValid {
		return apierr.Conflict("CODEX_ACCOUNT_LOGOUT_UNCONFIRMED", "Codex could not safely log out this account", nil)
	}
	active = m.activeAccountID() == accountID
	if _, err := m.catalog.markSignedOut(accountID); err != nil {
		return apierr.Conflict("CODEX_ACCOUNT_LOGOUT_UNCONFIRMED", "Codex could not safely log out this account", nil)
	}
	rollbackSlot := func() { _, _ = m.catalog.replaceCredential(accountID, credential, observation) }
	if active {
		latest, latestState, latestErr := readCodexFileState(m.globalCredentialPath(), false)
		if latestErr != nil || !sameCodexFileState(globalState, latestState) || !bytes.Equal(latest, globalCredential) {
			rollbackSlot()
			return apierr.Conflict("CODEX_GLOBAL_ACCOUNT_CHANGED", "The device Codex account changed", nil)
		}
		if removeErr := removeGlobalCredentialSettled(m.globalCredentialPath()); removeErr != nil {
			rollbackSlot()
			return apierr.Conflict("CODEX_ACCOUNT_LOGOUT_UNCONFIRMED", "The device Codex account could not be logged out", nil)
		}
		m.mu.Lock()
		current := m.active
		m.mu.Unlock()
		cleared, outcome, pointerErr := m.commitActivePointer(ctx, "", current, m.now())
		if pointerErr != nil {
			if outcome == activePointerUnchanged {
				_ = writeGlobalCredentialSettled(m.globalCredentialPath(), globalCredential)
				rollbackSlot()
			}
			return pointerErr
		}
		m.mu.Lock()
		m.active = cleared
		m.mu.Unlock()
		m.setGlobalAuthentication(signedOutAuthentication(m.now(), "Codex is signed out."))
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
	release, err := m.acquireAccountMutation(ctx)
	if err != nil {
		return err
	}
	defer release()
	accountID = strings.TrimSpace(accountID)
	record, ok := m.catalog.record(accountID)
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
	return m.active.AccountID
}

func (m *codexAccountManager) activateLocked(ctx context.Context, accountID string, expectedRevision int64) error {
	record, ok := m.catalog.record(accountID)
	if !ok || record.Snapshot.Status != domain.CodexAccountStatusValid {
		return apierr.NotFound("CODEX_ACCOUNT_NOT_FOUND", "Codex account not found")
	}
	_, err := m.activateFromCredentialLocked(ctx, accountID, expectedRevision, filepath.Join(record.Home, codexCredentialFilename), nil)
	return err
}

func (m *codexAccountManager) activateFromCredentialLocked(ctx context.Context, accountID string, expectedRevision int64, sourceCredential string, expectedGlobal []byte) (domain.CodexActiveAccount, error) {
	record, ok := m.catalog.record(accountID)
	if !ok || record.Snapshot.Status != domain.CodexAccountStatusValid {
		return domain.CodexActiveAccount{}, apierr.NotFound("CODEX_ACCOUNT_NOT_FOUND", "Codex account not found")
	}
	targetCredential, err := readOpaqueCredential(sourceCredential)
	if err != nil {
		return domain.CodexActiveAccount{}, err
	}
	globalPath := m.globalCredentialPath()
	previousCredential, previousErr := readOpaqueCredential(globalPath)
	if previousErr != nil && !errors.Is(previousErr, os.ErrNotExist) {
		return domain.CodexActiveAccount{}, ports.ErrCodexGlobalCredentialStoreUnsupported
	}
	if expectedGlobal != nil && (previousErr != nil || !bytes.Equal(previousCredential, expectedGlobal)) {
		return domain.CodexActiveAccount{}, ports.ErrCodexGlobalAccountChanged
	}
	restorePrevious := func() error {
		current, currentErr := readOpaqueCredential(globalPath)
		if currentErr != nil || !bytes.Equal(current, targetCredential) {
			return ports.ErrCodexGlobalAccountChanged
		}
		if previousErr == nil {
			return writeGlobalCredentialSettled(globalPath, previousCredential)
		}
		return removeGlobalCredentialSettled(globalPath)
	}
	if err := writeGlobalCredentialSettled(globalPath, targetCredential); err != nil {
		return domain.CodexActiveAccount{}, err
	}
	verifyCtx, cancel := context.WithTimeout(ctx, codexAccountAuthTimeout)
	defer cancel()
	client, err := m.factory.Open(verifyCtx, ports.CodexAccountContext{Home: m.globalHome, Managed: false})
	if err != nil {
		if restoreErr := restorePrevious(); restoreErr != nil {
			return domain.CodexActiveAccount{}, restoreErr
		}
		return domain.CodexActiveAccount{}, err
	}
	// The target slot was proactively refreshed during switch admission. This
	// read verifies that the same structured account is now active globally
	// without rotating its refresh token a second time inside the transaction.
	observation, err := client.Read(verifyCtx, false)
	_ = client.Close()
	currentCredential, currentErr := readOpaqueCredential(globalPath)
	if currentErr != nil || !bytes.Equal(currentCredential, targetCredential) {
		return domain.CodexActiveAccount{}, ports.ErrCodexGlobalAccountChanged
	}
	if err != nil || (observation.Authentication != domain.AgentAuthenticationAuthorized && observation.Authentication != domain.AgentAuthenticationNotApplicable) || !m.observationAndCredentialIdentifyRecord(record, observation, currentCredential) {
		currentCredential, currentErr := readOpaqueCredential(globalPath)
		if currentErr != nil || !bytes.Equal(currentCredential, targetCredential) {
			return domain.CodexActiveAccount{}, ports.ErrCodexGlobalAccountChanged
		}
		if restoreErr := restorePrevious(); restoreErr != nil {
			return domain.CodexActiveAccount{}, restoreErr
		}
		return domain.CodexActiveAccount{}, apierr.Conflict("CODEX_ACCOUNT_AUTH_UNVERIFIED", "Codex could not verify the selected account", nil)
	}
	now := m.now()
	current := domain.CodexActiveAccount{Revision: expectedRevision}
	m.mu.Lock()
	if m.active.Revision == expectedRevision {
		current = m.active
	}
	m.mu.Unlock()
	active, outcome, err := m.commitActivePointer(ctx, accountID, current, now)
	if err != nil {
		if outcome == activePointerUnchanged {
			if restoreErr := restorePrevious(); restoreErr != nil {
				return domain.CodexActiveAccount{}, restoreErr
			}
		}
		return domain.CodexActiveAccount{}, err
	}
	m.mu.Lock()
	m.active = active
	m.unmanaged = nil
	m.mu.Unlock()
	if refreshed, readErr := readOpaqueCredential(globalPath); readErr == nil {
		_ = writePrivateFileAtomic(filepath.Join(record.Home, codexCredentialFilename), refreshed)
	}
	m.invalidate(accountID)
	m.publish()
	return active, nil
}

func removeGlobalCredential(path string) error {
	return removeCodexFileIdentityBound(path)
}

func writeGlobalCredentialSettled(path string, data []byte) error {
	err := writeGlobalCredentialAtomic(path, data)
	if err == nil {
		return nil
	}
	current, readErr := readOpaqueCredential(path)
	if readErr == nil && bytes.Equal(current, data) {
		return nil
	}
	return errors.Join(err, readErr)
}

func removeGlobalCredentialSettled(path string) error {
	err := removeGlobalCredential(path)
	if err == nil {
		return nil
	}
	_, state, readErr := readCodexFileState(path, true)
	if readErr == nil && !state.exists {
		return nil
	}
	return errors.Join(err, readErr)
}

func readOpaqueCredential(source string) ([]byte, error) {
	data, _, err := readCodexFileState(source, false)
	return data, err
}

func copyOpaqueCredential(source, target string) error {
	data, err := readOpaqueCredential(source)
	if err != nil {
		return err
	}
	return writePrivateFileAtomic(target, data)
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
	replacement, err := prepareCodexFileReplacementInDirectory(path, data, false)
	if err != nil {
		return err
	}
	defer replacement.Abort()
	return replacement.Commit()
}

func (m *codexAccountManager) globalCredentialPath() string {
	return filepath.Join(m.globalHome, codexCredentialFilename)
}

func (m *codexAccountManager) validateGlobalCredentialStore() error {
	if err := validateCodexDirectory(m.globalHome, false); err != nil {
		return ports.ErrCodexGlobalCredentialStoreUnsupported
	}
	_, err := readOpaqueCredential(m.globalCredentialPath())
	if err != nil {
		return ports.ErrCodexGlobalCredentialStoreUnsupported
	}
	return nil
}
