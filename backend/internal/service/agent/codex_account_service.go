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

// Service integration.
func (s *Service) structuredCodexAuthentication(ctx context.Context, agentID string, purpose domain.AgentReadinessPurpose) (domain.AgentAuthenticationObservation, bool) {
	if agentID != string(domain.HarnessCodex) || s.codexAccounts == nil || s.codexAccounts.factory == nil {
		return domain.AgentAuthenticationObservation{}, false
	}
	if purpose == domain.AgentReadinessPurposeLaunch {
		if err := s.WaitCodexAccountBootstrap(ctx); err != nil {
			return failedAuthentication(s.codexAccounts.now(), domain.AgentReadinessReasonAuthCheckFailed, "Codex account setup did not complete."), true
		}
	} else {
		s.codexAccounts.mu.Lock()
		bootstrapped := s.codexAccounts.bootstrapped
		s.codexAccounts.mu.Unlock()
		if !bootstrapped {
			return domain.AgentAuthenticationObservation{}, false
		}
	}
	id := s.codexAccounts.activeAccountID()
	if id == "" {
		s.codexAccounts.mu.Lock()
		global := s.codexAccounts.globalAuth
		s.codexAccounts.mu.Unlock()
		if global.State == domain.AgentAuthenticationAuthorized || global.State == domain.AgentAuthenticationNotApplicable || global.State == domain.AgentAuthenticationUnknown {
			return global, true
		}
		return successfulAuthentication(s.codexAccounts.now(), domain.AgentAuthenticationUnauthorized, domain.AgentReadinessReasonUnauthorized, "Sign in to Codex or add an account in Settings."), true
	}
	record, ok := s.codexAccounts.catalog.record(id)
	if !ok {
		return failedAuthentication(s.codexAccounts.now(), domain.AgentReadinessReasonAuthCheckInconclusive, "The active Codex account is unavailable."), true
	}
	result, err := s.codexAccounts.ensureAuthentication(ctx, record, purpose, purpose == domain.AgentReadinessPurposeLaunch)
	if err != nil {
		return failedAuthentication(s.codexAccounts.now(), domain.AgentReadinessReasonAuthCheckFailed, "Authentication check failed."), true
	}
	return result, true
}

// CachedCodexAccounts returns the current in-memory view without native work.
func (s *Service) CachedCodexAccounts(ctx context.Context) (CodexAccounts, error) {
	if err := ctx.Err(); err != nil {
		return CodexAccounts{}, err
	}
	if s.codexAccounts == nil {
		return CodexAccounts{}, apierr.Unavailable("CODEX_ACCOUNT_MANAGEMENT_UNAVAILABLE", "Codex account management is unavailable")
	}
	result := s.codexAccounts.cached()
	if s.codexSwitches != nil {
		if sw, ok, err := s.codexSwitches.GetActiveCodexAccountSwitch(ctx); err == nil && ok {
			result.CurrentSwitch = &sw
		}
	}
	return result, nil
}

// EnsureCodexAccounts rediscovers requested accounts and refreshes eligible observations.
func (s *Service) EnsureCodexAccounts(ctx context.Context, ids []string, includeUsage bool) (CodexAccounts, error) {
	if s.codexAccounts == nil {
		return CodexAccounts{}, apierr.Unavailable("CODEX_ACCOUNT_MANAGEMENT_UNAVAILABLE", "Codex account management is unavailable")
	}
	if s.codexSwitches != nil && s.codexSwitches.CodexAccountSwitchInProgress() {
		if sw, ok, err := s.codexSwitches.GetActiveCodexAccountSwitch(ctx); err == nil && ok {
			result := s.codexAccounts.cached()
			result.CurrentSwitch = &sw
			return result, nil
		}
		return s.codexAccounts.cached(), nil
	}
	if err := s.WaitCodexAccountBootstrap(ctx); err != nil {
		return CodexAccounts{}, err
	}
	installation, err := s.readiness.EnsureInstallation(ctx, []string{string(domain.HarnessCodex)}, domain.AgentReadinessPurposeDisplay)
	if err != nil {
		return CodexAccounts{}, err
	}
	result, err := s.codexAccounts.ensure(ctx, ids, includeUsage, installation[0].Installation.State)
	if err == nil && s.codexSwitches != nil {
		if sw, ok, switchErr := s.codexSwitches.GetActiveCodexAccountSwitch(ctx); switchErr == nil && ok {
			result.CurrentSwitch = &sw
		}
	}
	return result, err
}

// ConsumeCodexAccountResetCredit redeems one provider-reported usage-limit
// reset and returns the refreshed cached account view. The provider chooses the
// credit; opaque credit identifiers never cross the daemon boundary.
func (s *Service) ConsumeCodexAccountResetCredit(ctx context.Context, accountID, idempotencyKey string) (CodexAccounts, error) {
	if s.codexAccounts == nil {
		return CodexAccounts{}, apierr.Unavailable("CODEX_ACCOUNT_MANAGEMENT_UNAVAILABLE", "Codex account management is unavailable")
	}
	if s.codexSwitches != nil && s.codexSwitches.CodexAccountSwitchInProgress() {
		return CodexAccounts{}, apierr.Conflict("CODEX_ACCOUNT_SWITCH_IN_PROGRESS", "Wait for the Codex account switch to finish before using a reset", nil)
	}
	if err := s.WaitCodexAccountBootstrap(ctx); err != nil {
		return CodexAccounts{}, err
	}
	if err := s.codexAccounts.consumeResetCredit(ctx, accountID, idempotencyKey); err != nil {
		return CodexAccounts{}, err
	}
	return s.CachedCodexAccounts(ctx)
}

// SubscribeCodexAccounts returns cached state followed by latest-wins updates.
func (s *Service) SubscribeCodexAccounts(ctx context.Context) (<-chan CodexAccounts, error) {
	if s.codexAccounts == nil {
		return nil, apierr.Unavailable("CODEX_ACCOUNT_MANAGEMENT_UNAVAILABLE", "Codex account management is unavailable")
	}
	source := s.codexAccounts.subscribe(ctx)
	out := make(chan CodexAccounts, 1)
	go func() {
		defer close(out)
		for snapshot := range source {
			if s.codexSwitches != nil {
				if sw, ok, err := s.codexSwitches.GetActiveCodexAccountSwitch(ctx); err == nil && ok {
					snapshot.CurrentSwitch = &sw
				}
			}
			select {
			case out <- snapshot:
			default:
				select {
				case <-out:
				default:
				}
				select {
				case out <- snapshot:
				default:
				}
			}
		}
	}()
	return out, nil
}

// PublishCodexAccounts notifies subscribers after externally owned switch changes.
func (s *Service) PublishCodexAccounts() {
	if s.codexAccounts != nil {
		s.codexAccounts.publish()
	}
}

// SetCodexAccountLoginTerminalOpener wires the trusted shell-terminal boundary.
func (s *Service) SetCodexAccountLoginTerminalOpener(opener codexAccountLoginTerminalService) {
	if s.codexAccounts != nil {
		s.codexAccounts.terminal = opener
	}
}

// OpenCodexAccountLoginTerminal starts one private native-login operation.
func (s *Service) OpenCodexAccountLoginTerminal(ctx context.Context) (CodexAccountLoginTerminalStart, error) {
	if s.codexAccounts == nil {
		return CodexAccountLoginTerminalStart{}, apierr.Unavailable("CODEX_ACCOUNT_MANAGEMENT_UNAVAILABLE", "Codex account management is unavailable")
	}
	if s.codexSwitches != nil && s.codexSwitches.CodexAccountSwitchInProgress() {
		return CodexAccountLoginTerminalStart{}, apierr.Conflict("CODEX_ACCOUNT_SWITCH_IN_PROGRESS", "A Codex account switch is already in progress", nil)
	}
	if err := s.WaitCodexAccountBootstrap(ctx); err != nil {
		return CodexAccountLoginTerminalStart{}, err
	}
	if err := s.requireCodexAccountInstallation(ctx); err != nil {
		return CodexAccountLoginTerminalStart{}, err
	}
	capabilities := s.codexAccounts.detectCapabilities(ctx)
	switch capabilities.AccountManagement.State {
	case domain.CodexCapabilityUnsupported:
		return CodexAccountLoginTerminalStart{}, apierr.NotImplemented("CODEX_ACCOUNT_MANAGEMENT_UNSUPPORTED", "This Codex version does not support account management")
	case domain.CodexCapabilityUnknown:
		return CodexAccountLoginTerminalStart{}, apierr.Unavailable("CODEX_ACCOUNT_MANAGEMENT_UNAVAILABLE", "Codex account management capability could not be verified")
	}
	return s.codexAccounts.openLoginTerminal(ctx, "")
}

// OpenCodexAccountReauthenticationTerminal starts native sign-in for one
// retained account slot. Successful verification replaces that slot instead of
// creating a duplicate account.
func (s *Service) OpenCodexAccountReauthenticationTerminal(ctx context.Context, accountID string) (CodexAccountLoginTerminalStart, error) {
	if s.codexAccounts == nil {
		return CodexAccountLoginTerminalStart{}, apierr.Unavailable("CODEX_ACCOUNT_MANAGEMENT_UNAVAILABLE", "Codex account management is unavailable")
	}
	if s.codexSwitches != nil && s.codexSwitches.CodexAccountSwitchInProgress() {
		return CodexAccountLoginTerminalStart{}, apierr.Conflict("CODEX_ACCOUNT_SWITCH_IN_PROGRESS", "A Codex account switch is already in progress", nil)
	}
	if err := s.WaitCodexAccountBootstrap(ctx); err != nil {
		return CodexAccountLoginTerminalStart{}, err
	}
	if err := s.requireCodexAccountInstallation(ctx); err != nil {
		return CodexAccountLoginTerminalStart{}, err
	}
	capabilities := s.codexAccounts.detectCapabilities(ctx)
	if capabilities.AccountManagement.State != domain.CodexCapabilitySupported {
		return CodexAccountLoginTerminalStart{}, apierr.Unavailable("CODEX_ACCOUNT_MANAGEMENT_UNAVAILABLE", "Codex account management capability could not be verified")
	}
	return s.codexAccounts.openLoginTerminal(ctx, strings.TrimSpace(accountID))
}

// LogoutCodexAccount removes one AO-saved credential while retaining the
// account card. Active-account logout also clears the device-global file-backed
// credential after exact structured identity confirmation.
func (s *Service) LogoutCodexAccount(ctx context.Context, accountID string) (CodexAccounts, error) {
	if s.codexAccounts == nil || s.codexAccounts.factory == nil {
		return CodexAccounts{}, apierr.Unavailable("CODEX_ACCOUNT_MANAGEMENT_UNAVAILABLE", "Codex account management is unavailable")
	}
	if s.codexSwitches != nil && s.codexSwitches.CodexAccountSwitchInProgress() {
		return CodexAccounts{}, apierr.Conflict("CODEX_ACCOUNT_SWITCH_IN_PROGRESS", "A Codex account switch is already in progress", nil)
	}
	if s.CodexAccountLoginInProgress() {
		return CodexAccounts{}, apierr.Conflict("CODEX_ACCOUNT_LOGIN_IN_PROGRESS", "Finish or close the Codex account login before logging out", nil)
	}
	if err := s.WaitCodexAccountBootstrap(ctx); err != nil {
		return CodexAccounts{}, err
	}
	if err := s.codexAccounts.logout(ctx, strings.TrimSpace(accountID)); err != nil {
		return CodexAccounts{}, err
	}
	s.readiness.Invalidate(string(domain.HarnessCodex), readinessInvalidateAuthentication)
	return s.CachedCodexAccounts(ctx)
}

// DeleteCodexAccount permanently removes one inactive signed-out account slot.
func (s *Service) DeleteCodexAccount(ctx context.Context, accountID string) (CodexAccounts, error) {
	if s.codexAccounts == nil {
		return CodexAccounts{}, apierr.Unavailable("CODEX_ACCOUNT_MANAGEMENT_UNAVAILABLE", "Codex account management is unavailable")
	}
	if s.codexSwitches != nil && s.codexSwitches.CodexAccountSwitchInProgress() {
		return CodexAccounts{}, apierr.Conflict("CODEX_ACCOUNT_SWITCH_IN_PROGRESS", "A Codex account switch is already in progress", nil)
	}
	if s.CodexAccountLoginInProgress() {
		return CodexAccounts{}, apierr.Conflict("CODEX_ACCOUNT_LOGIN_IN_PROGRESS", "Finish or close the Codex account login before deleting an account", nil)
	}
	if err := s.WaitCodexAccountBootstrap(ctx); err != nil {
		return CodexAccounts{}, err
	}
	if err := s.codexAccounts.deleteAccount(ctx, strings.TrimSpace(accountID)); err != nil {
		return CodexAccounts{}, err
	}
	return s.CachedCodexAccounts(ctx)
}

// VerifyCodexAccountLogin verifies one pending login through structured account read.
func (s *Service) VerifyCodexAccountLogin(ctx context.Context, operationID string) (domain.CodexAccountLoginOperation, error) {
	if s.codexAccounts == nil {
		return domain.CodexAccountLoginOperation{}, apierr.Unavailable("CODEX_ACCOUNT_MANAGEMENT_UNAVAILABLE", "Codex account management is unavailable")
	}
	result, err := s.codexAccounts.verifyLogin(ctx, strings.TrimSpace(operationID))
	if err == nil && result.Status == domain.CodexAccountLoginCompleted && result.Account != nil && result.Account.Active && s.readiness != nil {
		s.readiness.Invalidate(string(domain.HarnessCodex), readinessInvalidateAuthentication)
	}
	return result, err
}

// CancelCodexAccountLogin destroys a pending login and its credential staging.
func (s *Service) CancelCodexAccountLogin(ctx context.Context, operationID string) (domain.CodexAccountLoginOperation, error) {
	if s.codexAccounts == nil {
		return domain.CodexAccountLoginOperation{}, apierr.Unavailable("CODEX_ACCOUNT_MANAGEMENT_UNAVAILABLE", "Codex account management is unavailable")
	}
	return s.codexAccounts.cancelLogin(ctx, strings.TrimSpace(operationID))
}
func (s *Service) requireCodexAccountInstallation(ctx context.Context) error {
	observations, err := s.readiness.EnsureInstallation(ctx, []string{string(domain.HarnessCodex)}, domain.AgentReadinessPurposeDisplay)
	if err != nil {
		return err
	}
	if observations[0].Installation.State == domain.AgentInstallationNotInstalled && observations[0].Installation.Freshness == domain.AgentReadinessFresh {
		return apierr.NotImplemented("CODEX_ACCOUNT_MANAGEMENT_UNSUPPORTED", "Codex is not installed")
	}
	return nil
}

// InvalidateCodexAccountAuthentication invalidates the globally active account.
func (s *Service) InvalidateCodexAccountAuthentication() {
	if s.codexAccounts == nil {
		return
	}
	id := s.codexAccounts.activeAccountID()
	if id != "" {
		s.codexAccounts.invalidate(id)
	}
	s.readiness.Invalidate(string(domain.HarnessCodex), readinessInvalidateAuthentication)
	go func() { _ = s.codexAccounts.reconcileGlobal(s.codexAccounts.ctx) }()
}

// ObserveActiveCodexAccountCapacity attributes a provider event to the active account.
func (s *Service) ObserveActiveCodexAccountCapacity(observation ports.CodexCapacityObservation) {
	if s.codexAccounts == nil {
		return
	}
	id := s.codexAccounts.activeAccountID()
	if id != "" {
		s.codexAccounts.capacity.updateFromEvent(id, observation)
	}
}

// WarmCodexAccounts starts asynchronous bootstrap and observation warming.
func (s *Service) WarmCodexAccounts() {
	if s.codexAccounts == nil {
		return
	}
	_, _ = s.codexAccounts.startOrJoinBootstrap()
}

func (m *codexAccountManager) warmAfterBootstrap() {
	capabilities := m.detectCapabilities(m.ctx)
	records, err := m.catalog.recordsFor(nil)
	if err != nil {
		return
	}
	if capabilities.AccountRead.State == domain.CodexCapabilitySupported {
		for _, record := range records {
			if record.Snapshot.Status == domain.CodexAccountStatusValid {
				_, _ = m.ensureAuthentication(m.ctx, record, domain.AgentReadinessPurposeDisplay, false)
			}
		}
	}
	_ = m.capacity.ensure(m.ctx, records, capabilities)
}

// WaitCodexAccountBootstrap waits for the daemon-owned bootstrap gate.
func (s *Service) WaitCodexAccountBootstrap(ctx context.Context) error {
	if s.codexAccounts == nil {
		return apierr.Unavailable("CODEX_ACCOUNT_MANAGEMENT_UNAVAILABLE", "Codex account management is unavailable")
	}
	err := s.codexAccounts.waitBootstrap(ctx)
	if err == nil {
		return nil
	}
	var failure *codexBootstrapFailure
	if !errors.As(err, &failure) {
		return err
	}
	return apierr.New(apierr.KindUnavailable, "CODEX_ACCOUNT_MANAGEMENT_UNAVAILABLE", "Codex account setup did not complete", map[string]any{
		"reasonCode": failure.reason, "retryable": failure.retryable,
	})
}

// BeginCodexAccountMutation gives Session Manager exclusive ownership of the
// credential mutation path for the complete lifetime of a global switch.
func (s *Service) BeginCodexAccountMutation(ctx context.Context) error {
	if s.codexAccounts == nil {
		return apierr.Unavailable("CODEX_ACCOUNT_MANAGEMENT_UNAVAILABLE", "Codex account management is unavailable")
	}
	_, err := s.codexAccounts.acquireAccountMutation(ctx)
	return err
}

// EndCodexAccountMutation releases the switch-owned credential mutation path.
func (s *Service) EndCodexAccountMutation() {
	if s.codexAccounts == nil {
		return
	}
	select {
	case s.codexAccounts.mutations <- struct{}{}:
	default:
	}
}

// CurrentCodexActiveAccount returns the current durable active-account pointer.
func (s *Service) CurrentCodexActiveAccount() domain.CodexActiveAccount {
	if s.codexAccounts == nil {
		return domain.CodexActiveAccount{}
	}
	s.codexAccounts.mu.Lock()
	defer s.codexAccounts.mu.Unlock()
	return s.codexAccounts.active
}

// CodexAccountLoginInProgress reports whether native login owns its mutation gate.
func (s *Service) CodexAccountLoginInProgress() bool {
	if s.codexAccounts == nil {
		return false
	}
	s.codexAccounts.mu.Lock()
	defer s.codexAccounts.mu.Unlock()
	return s.codexAccounts.login != nil && !terminalLoginStatus(s.codexAccounts.login.snapshot.Status)
}

// VerifyCodexAccountForSwitch strictly verifies an inactive switch target.
func (s *Service) VerifyCodexAccountForSwitch(ctx context.Context, accountID string) error {
	if s.codexAccounts == nil || s.codexAccounts.factory == nil {
		return apierr.Unavailable("CODEX_ACCOUNT_MANAGEMENT_UNAVAILABLE", "Codex account management is unavailable")
	}
	if err := s.WaitCodexAccountBootstrap(ctx); err != nil {
		return err
	}
	if s.CodexAccountLoginInProgress() {
		return apierr.Conflict("CODEX_ACCOUNT_LOGIN_IN_PROGRESS", "Finish or close the Codex account login before switching accounts", nil)
	}
	capabilities := s.codexAccounts.detectCapabilities(ctx)
	switch capabilities.GlobalSwitch.State {
	case domain.CodexCapabilityUnsupported:
		return apierr.NotImplemented("CODEX_GLOBAL_CREDENTIAL_STORE_UNSUPPORTED", "Device-global Codex account switching requires file-backed credentials")
	case domain.CodexCapabilityUnknown:
		return apierr.Unavailable("CODEX_ACCOUNT_MANAGEMENT_UNAVAILABLE", "Codex account switching capability could not be verified")
	}
	record, ok := s.codexAccounts.catalog.record(strings.TrimSpace(accountID))
	if !ok || record.Snapshot.Status != domain.CodexAccountStatusValid {
		return apierr.NotFound("CODEX_ACCOUNT_NOT_FOUND", "Codex account not found")
	}
	verifyCtx, cancel := context.WithTimeout(ctx, codexAccountAuthTimeout)
	defer cancel()
	credentialPath := filepath.Join(record.Home, codexCredentialFilename)
	credential, admitted, credentialErr := readCodexFileState(credentialPath, false)
	if credentialErr != nil {
		s.codexAccounts.requireReauthentication(record.Snapshot.ID)
		return apierr.Conflict("CODEX_ACCOUNT_REAUTHENTICATION_REQUIRED", "Sign in again before switching to this Codex account", nil)
	}
	client, err := s.codexAccounts.factory.Open(verifyCtx, ports.CodexAccountContext{Home: record.Home, Managed: true})
	if err != nil {
		s.codexAccounts.requireReauthentication(record.Snapshot.ID)
		return apierr.Conflict("CODEX_ACCOUNT_REAUTHENTICATION_REQUIRED", "Sign in again before switching to this Codex account", nil)
	}
	refresh := record.Snapshot.AccountEmail != nil && safeAccountEmail(*record.Snapshot.AccountEmail)
	observation, err := client.Read(verifyCtx, refresh)
	_ = client.Close()
	latestCredential, latest, latestErr := readCodexFileState(credentialPath, false)
	stableOpaqueIdentity := distinguishableCodexIdentity(observation) || (latestErr == nil && sameCodexFileState(admitted, latest))
	if err != nil || latestErr != nil || !stableOpaqueIdentity || (observation.Authentication != domain.AgentAuthenticationAuthorized && observation.Authentication != domain.AgentAuthenticationNotApplicable) || !s.codexAccounts.observationAndCredentialIdentifyRecord(record, observation, latestCredential) || (!distinguishableCodexIdentity(observation) && !bytes.Equal(credential, latestCredential)) {
		s.codexAccounts.requireReauthentication(record.Snapshot.ID)
		return apierr.Conflict("CODEX_ACCOUNT_REAUTHENTICATION_REQUIRED", "Sign in again before switching to this Codex account", nil)
	}
	s.codexAccounts.clearReauthenticationRequired(record.Snapshot.ID)
	return nil
}

// VerifyCurrentCodexAccount confirms that the normal device-global Codex home
// still represents the expected active AO account.
func (s *Service) VerifyCurrentCodexAccount(ctx context.Context, accountID string) error {
	if s.codexAccounts == nil || s.codexAccounts.factory == nil {
		return apierr.Unavailable("CODEX_ACCOUNT_MANAGEMENT_UNAVAILABLE", "Codex account management is unavailable")
	}
	accountID = strings.TrimSpace(accountID)
	if s.CurrentCodexActiveAccount().AccountID != accountID {
		return apierr.Conflict("CODEX_GLOBAL_ACCOUNT_CHANGED", "The device Codex account changed", nil)
	}
	record, ok := s.codexAccounts.catalog.record(accountID)
	if !ok || record.Snapshot.Status != domain.CodexAccountStatusValid {
		return apierr.NotFound("CODEX_ACCOUNT_NOT_FOUND", "Codex account not found")
	}
	if err := s.codexAccounts.validateGlobalCredentialStore(); err != nil {
		return apierr.NotImplemented("CODEX_GLOBAL_CREDENTIAL_STORE_UNSUPPORTED", "Device-global Codex account switching requires file-backed credentials")
	}
	globalPath := s.codexAccounts.globalCredentialPath()
	globalCredential, admitted, credentialErr := readCodexFileState(globalPath, false)
	if credentialErr != nil {
		return apierr.Conflict("CODEX_GLOBAL_ACCOUNT_CHANGED", "The device Codex account could not be confirmed", nil)
	}
	verifyCtx, cancel := context.WithTimeout(ctx, codexAccountAuthTimeout)
	defer cancel()
	client, err := s.codexAccounts.factory.Open(verifyCtx, ports.CodexAccountContext{Home: s.codexAccounts.globalHome, Managed: false})
	if err != nil {
		return apierr.Conflict("CODEX_GLOBAL_ACCOUNT_CHANGED", "The device Codex account could not be confirmed", nil)
	}
	observation, readErr := client.Read(verifyCtx, false)
	_ = client.Close()
	latestCredential, latest, latestErr := readCodexFileState(globalPath, false)
	if readErr != nil || (observation.Authentication != domain.AgentAuthenticationAuthorized && observation.Authentication != domain.AgentAuthenticationNotApplicable) ||
		latestErr != nil || !sameCodexFileState(admitted, latest) || !bytes.Equal(globalCredential, latestCredential) ||
		!s.codexAccounts.observationAndCredentialIdentifyRecord(record, observation, latestCredential) {
		return apierr.Conflict("CODEX_GLOBAL_ACCOUNT_CHANGED", "The device Codex account changed", nil)
	}
	_ = writePrivateFileAtomic(filepath.Join(record.Home, codexCredentialFilename), latestCredential)
	return nil
}

// CheckpointAndActivateCodexAccount journals and verifies a credential activation.
func (s *Service) CheckpointAndActivateCodexAccount(ctx context.Context, switchID, targetID string, expectedRevision int64) (domain.CodexActiveAccount, error) {
	if s.codexAccounts == nil {
		return domain.CodexActiveAccount{}, apierr.Unavailable("CODEX_ACCOUNT_MANAGEMENT_UNAVAILABLE", "Codex account management is unavailable")
	}
	if !isCanonicalUUIDv4(strings.TrimSpace(switchID)) {
		return domain.CodexActiveAccount{}, apierr.Invalid("INVALID_CODEX_ACCOUNT_ID", "Invalid Codex account switch identifier", nil)
	}
	current := s.CurrentCodexActiveAccount()
	if current.Revision != expectedRevision {
		return domain.CodexActiveAccount{}, apierr.Conflict("CODEX_ACCOUNT_REVISION_CONFLICT", "The active Codex account changed", nil)
	}
	stagingDir := filepath.Join(s.codexAccounts.switchStagingRoot, switchID)
	if err := ensurePrivateDirectory(stagingDir); err != nil {
		return domain.CodexActiveAccount{}, apierr.Unavailable("CODEX_ACCOUNT_SWITCH_ACTIVATION_UNCONFIRMED", "The Codex credential switch could not be staged")
	}
	defer func() { _ = os.RemoveAll(stagingDir) }()
	if err := s.codexAccounts.validateGlobalCredentialStore(); err != nil {
		return domain.CodexActiveAccount{}, apierr.NotImplemented("CODEX_GLOBAL_CREDENTIAL_STORE_UNSUPPORTED", "Device-global Codex account switching requires file-backed credentials")
	}
	globalCredential, globalState, err := readCodexFileState(s.codexAccounts.globalCredentialPath(), false)
	if err != nil {
		return domain.CodexActiveAccount{}, apierr.NotImplemented("CODEX_GLOBAL_CREDENTIAL_STORE_UNSUPPORTED", "Device-global Codex account switching requires file-backed credentials")
	}
	if current.AccountID != "" {
		record, ok := s.codexAccounts.catalog.record(current.AccountID)
		if !ok {
			return domain.CodexActiveAccount{}, apierr.Conflict("CODEX_ACCOUNT_SWITCH_RECOVERY_REQUIRED", "The active Codex account slot is unavailable", nil)
		}
		verifyCtx, cancel := context.WithTimeout(ctx, codexAccountAuthTimeout)
		client, openErr := s.codexAccounts.factory.Open(verifyCtx, ports.CodexAccountContext{Home: s.codexAccounts.globalHome, Managed: false})
		if openErr != nil {
			cancel()
			return domain.CodexActiveAccount{}, apierr.Conflict("CODEX_GLOBAL_ACCOUNT_CHANGED", "The device Codex account could not be confirmed", nil)
		}
		observation, readErr := client.Read(verifyCtx, false)
		_ = client.Close()
		cancel()
		if readErr != nil {
			return domain.CodexActiveAccount{}, apierr.Conflict("CODEX_GLOBAL_ACCOUNT_CHANGED", "The device Codex account changed before switching", nil)
		}
		latestGlobal, latestState, latestErr := readCodexFileState(s.codexAccounts.globalCredentialPath(), false)
		if latestErr != nil || (!distinguishableCodexIdentity(observation) && !sameCodexFileState(globalState, latestState)) || !s.codexAccounts.observationAndCredentialIdentifyRecord(record, observation, latestGlobal) {
			return domain.CodexActiveAccount{}, apierr.Conflict("CODEX_GLOBAL_ACCOUNT_CHANGED", "The device Codex account changed before switching", nil)
		}
		globalCredential = latestGlobal
		checkpoint := filepath.Join(stagingDir, "source-auth.json")
		if err := writePrivateFileAtomic(checkpoint, globalCredential); err != nil {
			return domain.CodexActiveAccount{}, apierr.Unavailable("CODEX_ACCOUNT_SWITCH_ACTIVATION_UNCONFIRMED", "The active Codex credential could not be checkpointed")
		}
		if err := copyOpaqueCredential(checkpoint, filepath.Join(record.Home, codexCredentialFilename)); err != nil {
			return domain.CodexActiveAccount{}, apierr.Unavailable("CODEX_ACCOUNT_SWITCH_ACTIVATION_UNCONFIRMED", "The active Codex credential could not be checkpointed")
		}
	}
	target, ok := s.codexAccounts.catalog.record(strings.TrimSpace(targetID))
	if !ok {
		return domain.CodexActiveAccount{}, apierr.NotFound("CODEX_ACCOUNT_NOT_FOUND", "Codex account not found")
	}
	if err := copyOpaqueCredential(filepath.Join(target.Home, codexCredentialFilename), filepath.Join(stagingDir, "target-auth.json")); err != nil {
		return domain.CodexActiveAccount{}, apierr.Unavailable("CODEX_ACCOUNT_SWITCH_ACTIVATION_UNCONFIRMED", "The selected Codex credential could not be staged")
	}
	active, err := s.codexAccounts.activateFromCredentialLocked(ctx, strings.TrimSpace(targetID), expectedRevision, filepath.Join(stagingDir, "target-auth.json"), globalCredential)
	if err == nil {
		s.readiness.Invalidate(string(domain.HarnessCodex), readinessInvalidateAuthentication)
	}
	return active, err
}

// RestoreCodexAccountCredential restores and verifies the recorded source
// account only when the global credential still exactly matches the recorded
// source or target slot. Any other bytes may belong to an external login and
// are never overwritten by recovery.
func (s *Service) RestoreCodexAccountCredential(ctx context.Context, sourceAccountID, targetAccountID string) error {
	if s.codexAccounts == nil {
		return apierr.Unavailable("CODEX_ACCOUNT_MANAGEMENT_UNAVAILABLE", "Codex account management is unavailable")
	}
	source, ok := s.codexAccounts.catalog.record(strings.TrimSpace(sourceAccountID))
	if !ok || source.Snapshot.Status != domain.CodexAccountStatusValid {
		return apierr.NotFound("CODEX_ACCOUNT_NOT_FOUND", "Codex account not found")
	}
	target, ok := s.codexAccounts.catalog.record(strings.TrimSpace(targetAccountID))
	if !ok || target.Snapshot.Status != domain.CodexAccountStatusValid {
		return apierr.NotFound("CODEX_ACCOUNT_NOT_FOUND", "Codex account not found")
	}
	sourceCredential, sourceErr := readOpaqueCredential(filepath.Join(source.Home, codexCredentialFilename))
	targetCredential, targetErr := readOpaqueCredential(filepath.Join(target.Home, codexCredentialFilename))
	globalPath := s.codexAccounts.globalCredentialPath()
	globalCredential, globalErr := readOpaqueCredential(globalPath)
	if sourceErr != nil || targetErr != nil || globalErr != nil {
		return apierr.Unavailable("CODEX_ACCOUNT_SWITCH_ACTIVATION_UNCONFIRMED", "The previous Codex credential could not be restored")
	}
	if !bytes.Equal(globalCredential, sourceCredential) {
		if !bytes.Equal(globalCredential, targetCredential) {
			return ports.ErrCodexGlobalAccountChanged
		}
		latestGlobal, latestErr := readOpaqueCredential(globalPath)
		if latestErr != nil || !bytes.Equal(latestGlobal, targetCredential) {
			return ports.ErrCodexGlobalAccountChanged
		}
		if err := writeGlobalCredentialAtomic(globalPath, sourceCredential); err != nil {
			return apierr.Unavailable("CODEX_ACCOUNT_SWITCH_ACTIVATION_UNCONFIRMED", "The previous Codex credential could not be restored")
		}
	}
	admittedCredential, admitted, admittedErr := readCodexFileState(globalPath, false)
	if admittedErr != nil || !bytes.Equal(admittedCredential, sourceCredential) {
		return ports.ErrCodexGlobalAccountChanged
	}
	verifyCtx, cancel := context.WithTimeout(ctx, codexAccountAuthTimeout)
	defer cancel()
	client, err := s.codexAccounts.factory.Open(verifyCtx, ports.CodexAccountContext{Home: s.codexAccounts.globalHome, Managed: false})
	if err != nil {
		return apierr.Unavailable("CODEX_ACCOUNT_SWITCH_ACTIVATION_UNCONFIRMED", "The previous Codex account could not be verified")
	}
	observation, readErr := client.Read(verifyCtx, false)
	_ = client.Close()
	latestCredential, latest, latestErr := readCodexFileState(globalPath, false)
	if readErr != nil ||
		(observation.Authentication != domain.AgentAuthenticationAuthorized && observation.Authentication != domain.AgentAuthenticationNotApplicable) ||
		latestErr != nil || !sameCodexFileState(admitted, latest) || !bytes.Equal(admittedCredential, latestCredential) ||
		!s.codexAccounts.observationAndCredentialIdentifyRecord(source, observation, latestCredential) {
		return apierr.Unavailable("CODEX_ACCOUNT_SWITCH_ACTIVATION_UNCONFIRMED", "The previous Codex account could not be verified")
	}
	_ = writePrivateFileAtomic(filepath.Join(source.Home, codexCredentialFilename), latestCredential)
	s.readiness.Invalidate(string(domain.HarnessCodex), readinessInvalidateAuthentication)
	return nil
}

var _ ports.CodexAccountCredentialManager = (*Service)(nil)

// SetCodexAccountSwitchCoordinator wires the daemon-owned global switch coordinator.
func (s *Service) SetCodexAccountSwitchCoordinator(coordinator CodexAccountSwitchCoordinator) {
	s.codexSwitches = coordinator
}

// StartCodexAccountSwitch delegates an accepted switch to Session Manager.
func (s *Service) StartCodexAccountSwitch(ctx context.Context, cfg ports.CodexAccountSwitchConfig) (domain.CodexAccountSwitch, error) {
	if s.codexSwitches == nil {
		return domain.CodexAccountSwitch{}, apierr.Unavailable("CODEX_ACCOUNT_MANAGEMENT_UNAVAILABLE", "Codex account switching is unavailable")
	}
	return s.codexSwitches.StartCodexAccountSwitch(ctx, cfg)
}

// RecoverCodexAccountSwitch retries recorded incomplete work only.
func (s *Service) RecoverCodexAccountSwitch(ctx context.Context, id string) (domain.CodexAccountSwitch, error) {
	if s.codexSwitches == nil {
		return domain.CodexAccountSwitch{}, apierr.Unavailable("CODEX_ACCOUNT_MANAGEMENT_UNAVAILABLE", "Codex account switching is unavailable")
	}
	return s.codexSwitches.RecoverCodexAccountSwitch(ctx, id)
}
