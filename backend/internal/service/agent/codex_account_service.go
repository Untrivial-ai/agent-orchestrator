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
	if err := s.WaitCodexAccountStoreReady(ctx); err != nil {
		// Account management is optional for ordinary Codex use. When AO's
		// local account store cannot answer safely, fall back to the native
		// readiness path instead of treating that as proof Codex is signed out.
		return domain.AgentAuthenticationObservation{}, false
	}
	if purpose == domain.AgentReadinessPurposeLaunch {
		s.codexAccounts.mu.Lock()
		verified := s.codexAccounts.reconciliation.Status == domain.CodexDeviceReconciliationVerified
		s.codexAccounts.mu.Unlock()
		if !verified {
			return domain.AgentAuthenticationObservation{}, false
		}
	}
	id := s.codexAccounts.activeAccountID()
	if id == "" {
		s.codexAccounts.mu.Lock()
		reconciled := s.codexAccounts.reconciliation.Status == domain.CodexDeviceReconciliationVerified
		credentialPresent := s.codexAccounts.deviceCredentialPresent
		s.codexAccounts.mu.Unlock()
		if !reconciled || credentialPresent {
			return uncheckedAuthentication(), true
		}
		return successfulAuthentication(s.codexAccounts.now(), domain.AgentAuthenticationUnauthorized, domain.AgentReadinessReasonUnauthorized, "Sign in to Codex or add an account in Settings."), true
	}
	record, ok := s.codexAccounts.catalog.record(id)
	if !ok {
		return failedAuthentication(s.codexAccounts.now(), domain.AgentReadinessReasonAuthCheckInconclusive, "The active Codex account is unavailable."), true
	}
	result, err := s.codexAccounts.ensureAuthentication(ctx, record, purpose)
	if err != nil {
		return failedAuthentication(s.codexAccounts.now(), domain.AgentReadinessReasonAuthCheckFailed, "Authentication check failed."), true
	}
	record, ok = s.codexAccounts.catalog.record(id)
	if !ok {
		return domain.AgentAuthenticationObservation{}, false
	}
	if purpose == domain.AgentReadinessPurposeLaunch && result.State == domain.AgentAuthenticationAuthorized && record.Snapshot.AuthMethod == domain.CodexAuthMethodChatGPT {
		capabilities := s.codexAccounts.detectCapabilities(ctx)
		if capabilities.CapacityRead.State != domain.CodexCapabilitySupported {
			return domain.AgentAuthenticationObservation{}, false
		}
		// Launch readiness uses a protected provider call. account/read is only
		// local metadata discovery and cannot prove that the server accepts the
		// stored tokens.
		s.codexAccounts.capacity.invalidate(record.Snapshot.ID, false)
		_, capacityErr := s.codexAccounts.capacity.ensureOne(ctx, record, capabilities, true)
		if capacityErr != nil {
			return domain.AgentAuthenticationObservation{}, false
		}
		latest, ok := s.codexAccounts.catalog.record(record.Snapshot.ID)
		if !ok {
			return domain.AgentAuthenticationObservation{}, false
		}
		verified, reauthenticationRequired := s.codexAccounts.authenticationVerification(record.Snapshot.ID)
		if reauthenticationRequired {
			return latest.Snapshot.Authentication, true
		}
		if !verified {
			// Offline, timeout, and provider failures are not evidence that the
			// account is signed out. Let native launch readiness remain advisory.
			return domain.AgentAuthenticationObservation{}, false
		}
		return latest.Snapshot.Authentication, true
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
	if err := s.WaitCodexAccountStoreReady(ctx); err != nil {
		return CodexAccounts{}, err
	}
	result := s.codexAccounts.cached()
	if s.codexSwitches != nil {
		if sw, ok, err := s.codexSwitches.GetActiveCodexAccountSwitch(ctx); err == nil && ok {
			result.CurrentSwitch = &sw
		}
	}
	return result, nil
}

// CodexAccountEnsureOptions selects the account observations refreshed by EnsureCodexAccounts.
type CodexAccountEnsureOptions struct {
	IncludeUsage              bool
	ForceAuthentication       bool
	ForceDeviceReconciliation bool
}

// EnsureCodexAccounts rediscovers requested accounts and refreshes eligible observations.
func (s *Service) EnsureCodexAccounts(ctx context.Context, ids []string, options CodexAccountEnsureOptions) (CodexAccounts, error) {
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
	if err := s.WaitCodexAccountStoreReady(ctx); err != nil {
		return CodexAccounts{}, err
	}
	// Full Settings refreshes establish the current device account before any
	// global-home checks. A targeted row refresh stays in that saved account's
	// isolated home, so it must not publish a transient device reconciliation.
	if len(ids) == 0 || options.ForceDeviceReconciliation {
		_ = s.codexAccounts.reconcileGlobalWithPolicy(ctx, options.ForceDeviceReconciliation)
	}
	installation, err := s.readiness.EnsureInstallation(ctx, []string{string(domain.HarnessCodex)}, domain.AgentReadinessPurposeDisplay)
	if err != nil {
		return CodexAccounts{}, err
	}
	result, err := s.codexAccounts.ensure(ctx, ids, options.IncludeUsage, options.ForceAuthentication, installation[0].Installation.State)
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
	if err := s.WaitCodexAccountStoreReady(ctx); err != nil {
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
	if err := s.WaitCodexAccountStoreReady(ctx); err != nil {
		return nil, err
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

func (s *Service) prepareCodexAccountLogin(ctx context.Context) error {
	if s.codexAccounts == nil {
		return apierr.Unavailable("CODEX_ACCOUNT_MANAGEMENT_UNAVAILABLE", "Codex account management is unavailable")
	}
	if s.codexSwitches != nil && s.codexSwitches.CodexAccountSwitchInProgress() {
		return apierr.Conflict("CODEX_ACCOUNT_SWITCH_IN_PROGRESS", "A Codex account switch is already in progress", nil)
	}
	if err := s.WaitCodexAccountStoreReady(ctx); err != nil {
		return err
	}
	if err := s.requireCodexAccountInstallation(ctx); err != nil {
		return err
	}
	capabilities := s.codexAccounts.detectCapabilities(ctx)
	switch capabilities.NativeLogin.State {
	case domain.CodexCapabilityUnsupported:
		return apierr.NotImplemented("CODEX_ACCOUNT_MANAGEMENT_UNSUPPORTED", "This Codex version does not support account management")
	case domain.CodexCapabilityUnknown:
		return apierr.Unavailable("CODEX_ACCOUNT_MANAGEMENT_UNAVAILABLE", "Codex account management capability could not be verified")
	default:
		return nil
	}
}

// OpenCodexAccountLoginTerminal starts one private native-login operation.
func (s *Service) OpenCodexAccountLoginTerminal(ctx context.Context) (CodexAccountLoginTerminalStart, error) {
	if err := s.prepareCodexAccountLogin(ctx); err != nil {
		return CodexAccountLoginTerminalStart{}, err
	}
	return s.codexAccounts.openLoginTerminal(ctx, "")
}

// OpenCodexAccountReauthenticationTerminal starts native sign-in for one
// retained account slot. A locally validated credential replaces that slot
// instead of creating a duplicate account.
func (s *Service) OpenCodexAccountReauthenticationTerminal(ctx context.Context, accountID string) (CodexAccountLoginTerminalStart, error) {
	if err := s.prepareCodexAccountLogin(ctx); err != nil {
		return CodexAccountLoginTerminalStart{}, err
	}
	accountID = strings.TrimSpace(accountID)
	if err := s.EnsureCodexDeviceAccountReconciled(ctx); err != nil {
		return CodexAccountLoginTerminalStart{}, err
	}
	return s.codexAccounts.openLoginTerminal(ctx, accountID)
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
	if err := s.WaitCodexAccountStoreReady(ctx); err != nil {
		return CodexAccounts{}, err
	}
	accountID = strings.TrimSpace(accountID)
	// Only the verified or last-known device owner needs global reconciliation.
	// An inactive account logs out through its isolated AO home even when the
	// device-global credential cannot currently be inspected.
	if s.codexAccounts.accountMayOwnDeviceCredential(accountID) {
		if err := s.EnsureCodexDeviceAccountReconciled(ctx); err != nil {
			return CodexAccounts{}, err
		}
	}
	if err := s.codexAccounts.logout(ctx, accountID); err != nil {
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
	if err := s.WaitCodexAccountStoreReady(ctx); err != nil {
		return CodexAccounts{}, err
	}
	accountID = strings.TrimSpace(accountID)
	if s.codexAccounts.accountMayOwnDeviceCredential(accountID) {
		if err := s.EnsureCodexDeviceAccountReconciled(ctx); err != nil {
			return CodexAccounts{}, err
		}
	}
	if err := s.codexAccounts.deleteAccount(ctx, accountID); err != nil {
		return CodexAccounts{}, err
	}
	return s.CachedCodexAccounts(ctx)
}

// VerifyCodexAccountLogin completes a pending login once its local credential
// has been safely validated and committed.
func (s *Service) VerifyCodexAccountLogin(ctx context.Context, operationID string) (domain.CodexAccountLoginOperation, error) {
	if s.codexAccounts == nil {
		return domain.CodexAccountLoginOperation{}, apierr.Unavailable("CODEX_ACCOUNT_MANAGEMENT_UNAVAILABLE", "Codex account management is unavailable")
	}
	result, err := s.codexAccounts.verifyLogin(ctx, strings.TrimSpace(operationID))
	if err == nil && result.Status == domain.CodexAccountLoginCompleted && result.Account != nil {
		if result.Account.Active && s.readiness != nil {
			s.readiness.Invalidate(string(domain.HarnessCodex), readinessInvalidateAuthentication)
		}
		// Credential commit is complete. Provider-backed authentication, capacity,
		// and usage warming happens independently and cannot roll the login back.
		accountID := result.Account.ID
		go func() {
			_, _ = s.EnsureCodexAccounts(s.codexAccounts.ctx, []string{accountID}, CodexAccountEnsureOptions{
				IncludeUsage: true, ForceAuthentication: true, ForceDeviceReconciliation: true,
			})
		}()
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

// WarmCodexAccounts starts asynchronous local-store initialization, local
// device reconciliation, and then saved-account observation warming.
func (s *Service) WarmCodexAccounts() {
	if s.codexAccounts == nil {
		return
	}
	go func() {
		if err := s.codexAccounts.waitAccountStore(s.codexAccounts.ctx); err != nil {
			return
		}
		// Reconciliation is local-only and establishes the safe home for the device
		// account before any Codex process is opened for authentication or capacity.
		// A local reconciliation failure still leaves inactive saved accounts
		// eligible for their isolated checks below.
		_ = s.codexAccounts.reconcileGlobal(s.codexAccounts.ctx)
		capabilities := s.codexAccounts.detectCapabilities(s.codexAccounts.ctx)
		records, err := s.codexAccounts.catalog.recordsFor(nil)
		if err != nil {
			return
		}
		if capabilities.AccountRead.State == domain.CodexCapabilitySupported {
			for _, record := range records {
				if record.Snapshot.Status == domain.CodexAccountStatusValid {
					_, _ = s.codexAccounts.ensureAuthentication(s.codexAccounts.ctx, record, domain.AgentReadinessPurposeDisplay)
				}
			}
		}
		_ = s.codexAccounts.capacity.ensure(s.codexAccounts.ctx, records, capabilities, false)
	}()
}

// WaitCodexAccountStoreReady waits only for AO-owned local account state. It
// never starts Codex or inspects the device-global credential.
func (s *Service) WaitCodexAccountStoreReady(ctx context.Context) error {
	if s.codexAccounts == nil {
		return apierr.Unavailable("CODEX_ACCOUNT_MANAGEMENT_UNAVAILABLE", "Codex account management is unavailable")
	}
	err := s.codexAccounts.waitAccountStore(ctx)
	if err == nil {
		return nil
	}
	var failure *codexAccountLocalFailure
	if !errors.As(err, &failure) {
		return err
	}
	return apierr.New(apierr.KindUnavailable, "CODEX_ACCOUNT_MANAGEMENT_UNAVAILABLE", "Codex account setup did not complete", map[string]any{
		"reasonCode": failure.reason, "retryable": failure.retryable,
	})
}

// EnsureCodexDeviceAccountReconciled conclusively identifies the canonical
// device account before an operation is allowed to mutate it.
func (s *Service) EnsureCodexDeviceAccountReconciled(ctx context.Context) error {
	if err := s.WaitCodexAccountStoreReady(ctx); err != nil {
		return err
	}
	err := s.codexAccounts.reconcileGlobal(ctx)
	s.codexAccounts.mu.Lock()
	state := s.codexAccounts.reconciliation
	s.codexAccounts.mu.Unlock()
	if err == nil && state.Status == domain.CodexDeviceReconciliationVerified {
		return nil
	}
	reason, retryable := state.ReasonCode, state.Retryable
	if err != nil {
		failure := classifyDeviceReconciliationFailure(err)
		reason, retryable = failure.reason, failure.retryable
	}
	if strings.TrimSpace(reason) == "" {
		reason = "account_reconciliation_unavailable"
	}
	return apierr.New(apierr.KindUnavailable, "CODEX_DEVICE_ACCOUNT_UNVERIFIED", "The device Codex account could not be verified", map[string]any{
		"reasonCode": reason, "retryable": retryable,
	})
}

// BeginCodexAccountMutation gives the local switch worker exclusive ownership
// of the credential mutation path for the complete transaction.
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

// CodexAccountLoginInProgress reports whether a native login is still open.
func (s *Service) CodexAccountLoginInProgress() bool {
	if s.codexAccounts == nil {
		return false
	}
	s.codexAccounts.mu.Lock()
	defer s.codexAccounts.mu.Unlock()
	return s.codexAccounts.login != nil && !terminalLoginStatus(s.codexAccounts.login.snapshot.Status)
}

// PrepareCodexAccountForSwitch snapshots the stable source and target
// credentials before the durable journal is created. Provider state is not
// consulted; the staged files are the complete local recovery evidence.
func (s *Service) PrepareCodexAccountForSwitch(ctx context.Context, switchID, accountID string) (domain.CodexAccountSwitchSource, error) {
	notPrepared := func(err error) (domain.CodexAccountSwitchSource, error) {
		return domain.CodexAccountSwitchSource{}, errors.Join(ports.ErrCodexAccountSwitchNotCommitted, err)
	}
	if s.codexAccounts == nil {
		return notPrepared(apierr.Unavailable("CODEX_ACCOUNT_MANAGEMENT_UNAVAILABLE", "Codex account management is unavailable"))
	}
	if s.CodexAccountLoginInProgress() {
		return notPrepared(apierr.Conflict("CODEX_ACCOUNT_LOGIN_IN_PROGRESS", "Finish or close the Codex account login before switching accounts", nil))
	}
	if !isCanonicalUUIDv4(strings.TrimSpace(switchID)) {
		return notPrepared(apierr.Invalid("INVALID_CODEX_ACCOUNT_ID", "Invalid Codex account switch identifier", nil))
	}
	record, ok := s.codexAccounts.catalog.record(strings.TrimSpace(accountID))
	if !ok || record.Snapshot.Status != domain.CodexAccountStatusValid {
		return notPrepared(apierr.NotFound("CODEX_ACCOUNT_NOT_FOUND", "Codex account not found"))
	}
	if err := ctx.Err(); err != nil {
		return notPrepared(err)
	}
	credentialPath := filepath.Join(record.Home, codexCredentialFilename)
	credential, admitted, credentialErr := readCodexFileState(credentialPath, false)
	if credentialErr != nil {
		s.codexAccounts.requireReauthentication(record.Snapshot.ID)
		return notPrepared(apierr.Conflict("CODEX_ACCOUNT_REAUTHENTICATION_REQUIRED", "Sign in again before switching to this Codex account", nil))
	}
	latestCredential, latest, latestErr := readCodexFileState(credentialPath, false)
	if latestErr != nil || !sameCodexFileState(admitted, latest) || !bytes.Equal(credential, latestCredential) {
		return notPrepared(apierr.Conflict("CODEX_ACCOUNT_IDENTITY_CHANGED", "The Codex account changed while preparing the switch. Try again", nil))
	}
	if !localCredentialIdentifiesRecord(record, latestCredential) {
		return notPrepared(apierr.Conflict("CODEX_ACCOUNT_IDENTITY_CHANGED", "The saved Codex account no longer matches its credential. Sign in again", nil))
	}
	_ = s.codexAccounts.catalog.updateCredentialIdentity(ctx, record.Snapshot.ID, latestCredential)

	stagingDir := filepath.Join(s.codexAccounts.switchStagingRoot, switchID)
	if err := ensurePrivateDirectory(stagingDir); err != nil {
		return notPrepared(apierr.Unavailable("CODEX_ACCOUNT_SWITCH_ACTIVATION_UNCONFIRMED", "The Codex credential switch could not be staged"))
	}
	if err := writePrivateFileAtomic(filepath.Join(stagingDir, "target-auth.json"), latestCredential); err != nil {
		_ = os.RemoveAll(stagingDir)
		return notPrepared(apierr.Unavailable("CODEX_ACCOUNT_SWITCH_ACTIVATION_UNCONFIRMED", "The selected Codex credential could not be staged"))
	}

	globalCredential, globalState, globalErr := readCodexFileState(s.codexAccounts.globalCredentialPath(), true)
	if globalErr != nil {
		_ = os.RemoveAll(stagingDir)
		return notPrepared(apierr.NotImplemented("CODEX_GLOBAL_CREDENTIAL_STORE_UNSUPPORTED", "Device-global Codex account switching requires file-backed credentials"))
	}
	source := domain.CodexAccountSwitchSource{Kind: domain.CodexAccountSwitchSourceNone}
	if globalState.exists {
		identity, identityErr := parseCodexCredentialIdentity(globalCredential)
		matched, match := s.codexAccounts.matchGlobalCredentialForReconciliation(globalCredential, identity, identityErr)
		source.Kind = domain.CodexAccountSwitchSourceDevice
		if match == codexCredentialMatchManaged {
			source.Kind = domain.CodexAccountSwitchSourceManaged
			source.AccountID = matched.Snapshot.ID
		}
		if err := writePrivateFileAtomic(filepath.Join(stagingDir, "source-auth.json"), globalCredential); err != nil {
			_ = os.RemoveAll(stagingDir)
			return notPrepared(apierr.Unavailable("CODEX_ACCOUNT_SWITCH_ACTIVATION_UNCONFIRMED", "The current Codex credential could not be checkpointed"))
		}
	}
	finalGlobal, finalState, finalErr := readCodexFileState(s.codexAccounts.globalCredentialPath(), true)
	if finalErr != nil || !sameCodexFileState(globalState, finalState) || !bytes.Equal(globalCredential, finalGlobal) {
		_ = os.RemoveAll(stagingDir)
		return notPrepared(ports.ErrCodexGlobalAccountChanged)
	}
	return source, nil
}

// InspectCodexAccountSwitch classifies the stable device credential against
// the staged target and source. An error means local inspection was
// inconclusive and the durable switch fence must be retained.
func (s *Service) InspectCodexAccountSwitch(ctx context.Context, switchID string, sourceKind domain.CodexAccountSwitchSourceKind, sourceAccountID, accountID string) (domain.CodexAccountSwitchInstallationState, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if s.codexAccounts == nil {
		return "", apierr.Unavailable("CODEX_ACCOUNT_MANAGEMENT_UNAVAILABLE", "Codex account management is unavailable")
	}
	switchID = strings.TrimSpace(switchID)
	if !isCanonicalUUIDv4(switchID) {
		return "", apierr.Invalid("INVALID_CODEX_ACCOUNT_ID", "Invalid Codex account switch identifier", nil)
	}
	accountID = strings.TrimSpace(accountID)
	if err := s.codexAccounts.validateGlobalCredentialStore(); err != nil {
		return "", apierr.NotImplemented("CODEX_GLOBAL_CREDENTIAL_STORE_UNSUPPORTED", "Device-global Codex account switching requires file-backed credentials")
	}
	globalPath := s.codexAccounts.globalCredentialPath()
	globalCredential, admitted, credentialErr := readCodexFileState(globalPath, true)
	if credentialErr != nil {
		return "", credentialErr
	}
	latestCredential, latest, latestErr := readCodexFileState(globalPath, true)
	if latestErr != nil || !sameCodexFileState(admitted, latest) || !bytes.Equal(globalCredential, latestCredential) {
		return "", errors.Join(ports.ErrCodexGlobalAccountChanged, latestErr)
	}
	if !admitted.exists {
		return domain.CodexAccountSwitchCredentialMissing, nil
	}
	targetCredential, targetErr := readOpaqueCredential(filepath.Join(s.codexAccounts.switchStagingRoot, switchID, "target-auth.json"))
	targetFromStaging := targetErr == nil
	var targetRecord codexAccountRecord
	if targetErr != nil && !errors.Is(targetErr, os.ErrNotExist) {
		return "", targetErr
	}
	if !targetFromStaging {
		if record, ok := s.codexAccounts.catalog.record(accountID); ok && record.Snapshot.Status == domain.CodexAccountStatusValid {
			targetRecord = record
			targetCredential, targetErr = readOpaqueCredential(filepath.Join(record.Home, codexCredentialFilename))
			if targetErr != nil && !errors.Is(targetErr, os.ErrNotExist) {
				return "", targetErr
			}
		}
	}
	if targetErr == nil && bytes.Equal(globalCredential, targetCredential) && (targetFromStaging || localCredentialIdentifiesRecord(targetRecord, globalCredential)) {
		return domain.CodexAccountSwitchTargetInstalled, nil
	}
	if sourceKind != domain.CodexAccountSwitchSourceNone {
		sourceCredential, sourceErr := readOpaqueCredential(filepath.Join(s.codexAccounts.switchStagingRoot, switchID, "source-auth.json"))
		if sourceErr == nil && bytes.Equal(globalCredential, sourceCredential) {
			return domain.CodexAccountSwitchSourceInstalled, nil
		}
		if sourceErr != nil && !errors.Is(sourceErr, os.ErrNotExist) {
			return "", sourceErr
		}
		if sourceAccountID = strings.TrimSpace(sourceAccountID); sourceAccountID != "" {
			if source, found := s.codexAccounts.catalog.record(sourceAccountID); found && localCredentialIdentifiesRecord(source, globalCredential) {
				return domain.CodexAccountSwitchSourceInstalled, nil
			}
		}
	}
	return domain.CodexAccountSwitchExternalCredential, nil
}

// ActivatePreparedCodexAccountSwitch performs the single compare-and-swap
// from the staged source to the staged target.
func (s *Service) ActivatePreparedCodexAccountSwitch(ctx context.Context, sourceKind domain.CodexAccountSwitchSourceKind, switchID, targetID string) error {
	notCommitted := func(err error) error {
		return errors.Join(ports.ErrCodexAccountSwitchNotCommitted, err)
	}
	if s.codexAccounts == nil {
		return notCommitted(apierr.Unavailable("CODEX_ACCOUNT_MANAGEMENT_UNAVAILABLE", "Codex account management is unavailable"))
	}
	if !isCanonicalUUIDv4(strings.TrimSpace(switchID)) {
		return notCommitted(apierr.Invalid("INVALID_CODEX_ACCOUNT_ID", "Invalid Codex account switch identifier", nil))
	}
	stagingDir := filepath.Join(s.codexAccounts.switchStagingRoot, switchID)
	if sourceKind != domain.CodexAccountSwitchSourceManaged && sourceKind != domain.CodexAccountSwitchSourceDevice && sourceKind != domain.CodexAccountSwitchSourceNone {
		return notCommitted(apierr.Invalid("INVALID_CODEX_ACCOUNT_SWITCH_SOURCE", "Invalid Codex account switch source", nil))
	}
	target, ok := s.codexAccounts.catalog.record(strings.TrimSpace(targetID))
	if !ok || target.Snapshot.Status != domain.CodexAccountStatusValid {
		return notCommitted(apierr.NotFound("CODEX_ACCOUNT_NOT_FOUND", "Codex account not found"))
	}
	targetCredential, targetState, targetErr := readCodexFileState(filepath.Join(stagingDir, "target-auth.json"), false)
	latestTarget, latestTargetState, latestTargetErr := readCodexFileState(filepath.Join(stagingDir, "target-auth.json"), false)
	if targetErr != nil || latestTargetErr != nil || !sameCodexFileState(targetState, latestTargetState) || !bytes.Equal(targetCredential, latestTarget) || !localCredentialIdentifiesRecord(target, latestTarget) {
		s.codexAccounts.requireReauthentication(target.Snapshot.ID)
		return notCommitted(apierr.Conflict("CODEX_ACCOUNT_REAUTHENTICATION_REQUIRED", "Sign in again before switching to this Codex account", nil))
	}
	var expectedGlobal []byte
	if sourceKind == domain.CodexAccountSwitchSourceNone {
		expectedGlobal = []byte{}
	} else {
		var sourceErr error
		expectedGlobal, sourceErr = readOpaqueCredential(filepath.Join(stagingDir, "source-auth.json"))
		if sourceErr != nil {
			return notCommitted(apierr.Conflict("CODEX_GLOBAL_ACCOUNT_CHANGED", "The device Codex account changed before switching", nil))
		}
	}
	if err := ctx.Err(); err != nil {
		return notCommitted(err)
	}
	err := s.codexAccounts.activateFromCredentialLocked(ctx, strings.TrimSpace(targetID), filepath.Join(stagingDir, "target-auth.json"), expectedGlobal)
	if err == nil {
		s.readiness.Invalidate(string(domain.HarnessCodex), readinessInvalidateAuthentication)
	}
	return err
}

// CleanupCodexAccountSwitch removes private switch staging after the durable
// switch has reached a locally confirmed terminal phase.
func (s *Service) CleanupCodexAccountSwitch(_ context.Context, switchID string) error {
	if s.codexAccounts == nil || !isCanonicalUUIDv4(strings.TrimSpace(switchID)) {
		return nil
	}
	return os.RemoveAll(filepath.Join(s.codexAccounts.switchStagingRoot, switchID))
}

// CleanupInactiveCodexAccountSwitches removes credential staging that has no
// live durable journal. The active switch directory, when supplied, is kept.
func (s *Service) CleanupInactiveCodexAccountSwitches(ctx context.Context, activeSwitchID string) error {
	if s.codexAccounts == nil {
		return nil
	}
	entries, err := os.ReadDir(s.codexAccounts.switchStagingRoot)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	activeSwitchID = strings.TrimSpace(activeSwitchID)
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		name := entry.Name()
		if !entry.IsDir() || !isCanonicalUUIDv4(name) || name == activeSwitchID {
			continue
		}
		if err := os.RemoveAll(filepath.Join(s.codexAccounts.switchStagingRoot, name)); err != nil {
			return err
		}
	}
	return nil
}

var _ ports.CodexAccountCredentialManager = (*Service)(nil)

// CodexAccountSwitchInProgress reports whether the credential coordinator owns
// the device-global mutation gate.
func (s *Service) CodexAccountSwitchInProgress() bool {
	return s.codexSwitches != nil && s.codexSwitches.CodexAccountSwitchInProgress()
}

// StartCodexAccountSwitch starts a durable device credential switch.
func (s *Service) StartCodexAccountSwitch(ctx context.Context, cfg ports.CodexAccountSwitchConfig) (domain.CodexAccountSwitch, error) {
	if s.codexSwitches == nil {
		return domain.CodexAccountSwitch{}, apierr.Unavailable("CODEX_ACCOUNT_MANAGEMENT_UNAVAILABLE", "Codex account switching is unavailable")
	}
	return s.codexSwitches.StartCodexAccountSwitch(ctx, cfg)
}

// GetCodexAccountSwitch returns the durable result for one switch. The UI uses
// this journal state instead of inferring success from a reconciliation snapshot.
func (s *Service) GetCodexAccountSwitch(ctx context.Context, id string) (domain.CodexAccountSwitch, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return domain.CodexAccountSwitch{}, apierr.Invalid("CODEX_ACCOUNT_SWITCH_ID_REQUIRED", "Codex account switch ID is required", nil)
	}
	if s.codexSwitches == nil {
		return domain.CodexAccountSwitch{}, apierr.Unavailable("CODEX_ACCOUNT_MANAGEMENT_UNAVAILABLE", "Codex account switching is unavailable")
	}
	sw, ok, err := s.codexSwitches.GetCodexAccountSwitch(ctx, id)
	if err != nil {
		return domain.CodexAccountSwitch{}, err
	}
	if !ok {
		return domain.CodexAccountSwitch{}, apierr.NotFound("CODEX_ACCOUNT_SWITCH_NOT_FOUND", "Codex account switch not found")
	}
	return sw, nil
}

// ReconcileCodexAccountSwitches starts best-effort local settlement for any
// durable switch journal. Recovery failure never blocks unrelated daemon work.
func (s *Service) ReconcileCodexAccountSwitches(ctx context.Context) error {
	if s.codexSwitches == nil {
		return nil
	}
	return s.codexSwitches.ReconcileCodexAccountSwitches(ctx)
}

// WaitCodexAccountSwitchWorkers drains credential workers during shutdown.
func (s *Service) WaitCodexAccountSwitchWorkers(ctx context.Context) error {
	if s.codexSwitches == nil {
		return nil
	}
	return s.codexSwitches.Wait(ctx)
}
