package agent

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/shellterm"
)

const (
	codexAccountDisplayTTL       = 5 * time.Minute
	codexAccountLaunchTTL        = 30 * time.Second
	codexAccountAuthTimeout      = 10 * time.Second
	codexAccountReconcileTimeout = 45 * time.Second
	codexAccountUsageTTL         = 5 * time.Minute
	codexResetCreditTimeout      = 15 * time.Second
	codexAccountLoginLifetime    = 15 * time.Minute
	codexAccountProcessLimit     = 2
)

var defaultCodexAccountBootstrapRetryDelays = []time.Duration{15 * time.Second, time.Minute, 5 * time.Minute}

// CodexAccounts is the display-safe account-management view. Credentials and
// filesystem locations remain daemon-private.
type CodexAccounts struct {
	ActiveAccountID        string                              `json:"activeAccountId,omitempty"`
	AccountRevision        int64                               `json:"accountRevision"`
	Accounts               []domain.CodexAccountSnapshot       `json:"accounts"`
	Capabilities           domain.CodexAccountCapabilities     `json:"capabilities"`
	UnmanagedGlobalAccount *domain.CodexUnmanagedGlobalAccount `json:"unmanagedGlobalAccount,omitempty"`
	ActiveLogin            *CodexActiveLogin                   `json:"activeLogin,omitempty"`
	CurrentSwitch          *domain.CodexAccountSwitch          `json:"currentSwitch,omitempty"`
}

// CodexActiveLogin is the safe in-memory login state needed to reattach the
// Settings terminal after a renderer remount.
type CodexActiveLogin struct {
	OperationID   string                         `json:"operationId"`
	AccountID     string                         `json:"accountId,omitempty"`
	Status        domain.CodexAccountLoginStatus `json:"status"`
	ReasonCode    string                         `json:"reasonCode"`
	Reason        string                         `json:"reason"`
	ExpiresAt     time.Time                      `json:"expiresAt"`
	ShellTerminal CodexLoginTerminalDisplay      `json:"shellTerminal"`
}

// CodexLoginTerminalDisplay excludes the terminal command and filesystem
// context while retaining the mux identity and display metadata.
type CodexLoginTerminalDisplay struct {
	HandleID  string    `json:"handleId"`
	Title     string    `json:"title"`
	CreatedAt time.Time `json:"createdAt"`
}

// CodexAccountLoginTerminalStart combines safe login state with its trusted terminal.
type CodexAccountLoginTerminalStart struct {
	Operation     domain.CodexAccountLoginOperation `json:"operation"`
	ShellTerminal shellterm.ShellTerminal           `json:"shellTerminal"`
}

type codexAccountLoginTerminalService interface {
	OpenCommandTerminal(context.Context, shellterm.OpenCommandTerminalInput) (shellterm.ShellTerminal, error)
	CloseShellTerminal(context.Context, string) error
}

// CodexAccountStateStore persists only the active-account pointer and revision.
// Account descriptors and credentials remain filesystem-owned.
type CodexAccountStateStore interface {
	GetCodexActiveAccount(context.Context) (domain.CodexActiveAccount, bool, error)
	SetCodexActiveAccount(context.Context, string, int64, time.Time) (domain.CodexActiveAccount, error)
}

type accountAuthCall struct{ done chan struct{} }
type accountAuthState struct {
	invalidated              bool
	reauthenticationRequired bool
	failures                 int
	nextRetryAt              time.Time
	call                     *accountAuthCall
}
type accountUsageState struct {
	value     *domain.CodexAccountUsageSummary
	checkedAt time.Time
	call      chan struct{}
}
type accountReconcileCall struct {
	done chan struct{}
	err  error
}
type codexAccountBootstrapCall struct {
	done    chan struct{}
	err     error
	attempt int
}
type accountLoginOperation struct {
	snapshot        domain.CodexAccountLoginOperation
	targetAccountID string
	pendingDir      string
	home            string
	terminalHandle  string
	terminalTitle   string
	terminalCreated time.Time
	closing         bool
	committing      bool
	commitDone      chan struct{}
}

type codexAccountManager struct {
	ctx               context.Context
	catalog           *codexAccountCatalog
	factory           ports.CodexAccountClientFactory
	stateStore        CodexAccountStateStore
	operationGate     ports.CodexOperationGate
	logger            *slog.Logger
	now               func() time.Time
	newID             func() string
	processes         chan struct{}
	mutations         chan struct{}
	executable        func() (string, error)
	terminal          codexAccountLoginTerminalService
	globalHome        string
	pendingRoot       string
	switchStagingRoot string

	mu                       sync.Mutex
	bootstrapCall            *codexAccountBootstrapCall
	bootstrapLastErr         error
	bootstrapFailures        int
	bootstrapNextRetryAt     time.Time
	bootstrapRetryGeneration uint64
	bootstrapRetryDelays     []time.Duration
	auth                     map[string]*accountAuthState
	usage                    map[string]*accountUsageState
	capabilities             domain.CodexAccountCapabilities
	active                   domain.CodexActiveAccount
	globalAuth               domain.AgentAuthenticationObservation
	unmanaged                *domain.CodexUnmanagedGlobalAccount
	login                    *accountLoginOperation
	reconcile                *accountReconcileCall
	bootstrapped             bool
	capacity                 *codexCapacityCoordinator
	subscribers              map[chan CodexAccounts]struct{}
}

func newCodexAccountManager(ctx context.Context, accountRoot, pendingRoot, switchStagingRoot, globalHome string, factory ports.CodexAccountClientFactory, stateStore CodexAccountStateStore, logger *slog.Logger, operationGates ...ports.CodexOperationGate) *codexAccountManager {
	if ctx == nil {
		ctx = context.Background()
	}
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	var operationGate ports.CodexOperationGate
	if len(operationGates) > 0 {
		operationGate = operationGates[0]
	}
	m := &codexAccountManager{
		ctx: ctx, catalog: newCodexAccountCatalog(accountRoot, logger), factory: factory, stateStore: stateStore, operationGate: operationGate,
		logger: logger, now: func() time.Time { return time.Now().UTC() }, newID: uuid.NewString,
		processes: make(chan struct{}, codexAccountProcessLimit), mutations: make(chan struct{}, 1), executable: os.Executable,
		globalHome: canonicalPath(globalHome), pendingRoot: canonicalPath(pendingRoot), switchStagingRoot: canonicalPath(switchStagingRoot),
		auth: map[string]*accountAuthState{}, usage: map[string]*accountUsageState{},
		capabilities: unavailableCodexCapabilities(), subscribers: map[chan CodexAccounts]struct{}{},
		bootstrapRetryDelays: append([]time.Duration(nil), defaultCodexAccountBootstrapRetryDelays...),
		globalAuth:           uncheckedAuthentication(),
	}
	m.mutations <- struct{}{}
	m.capacity = newCodexCapacityCoordinator(m)
	m.catalog.setOnRemoved(func(ids []string) { m.capacity.removeAccounts(ids); m.publish() })
	return m
}

func (m *codexAccountManager) acquireAccountMutation(ctx context.Context) (func(), error) {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-m.mutations:
		var once sync.Once
		return func() { once.Do(func() { m.mutations <- struct{}{} }) }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (m *codexAccountManager) acquireGlobalRead(ctx context.Context, account ports.CodexAccountContext) (func(), error) {
	if m.operationGate == nil || canonicalPath(account.Home) != m.globalHome {
		return func() {}, nil
	}
	return m.operationGate.AcquireShared(ctx)
}

func (m *codexAccountManager) acquireGlobalMutation(ctx context.Context) (ports.CodexOperationLease, error) {
	if m.operationGate == nil {
		return nil, nil
	}
	return m.operationGate.AcquireExclusive(ctx)
}

func unavailableCodexCapabilities() domain.CodexAccountCapabilities {
	unknown := domain.CodexCapabilityObservation{State: domain.CodexCapabilityUnknown, ReasonCode: domain.CodexCapabilityReasonUnknown, Reason: "Codex capability detection has not completed."}
	return domain.CodexAccountCapabilities{AccountRead: unknown, NativeLogin: unknown, CapacityRead: unknown, UsageRead: unknown, ResetCreditConsume: unknown, ThreadResume: unknown, AccountManagement: unknown, GlobalSwitch: unknown}
}

func (m *codexAccountManager) detectCapabilities(ctx context.Context) domain.CodexAccountCapabilities {
	if m.factory == nil {
		return unavailableCodexCapabilities()
	}
	capabilities := m.factory.Capabilities(ctx)
	if capabilities.AccountRead.State == domain.CodexCapabilitySupported {
		if err := m.validateGlobalCredentialStore(); err != nil {
			capabilities.GlobalSwitch = domain.CodexCapabilityObservation{
				State: domain.CodexCapabilityUnsupported, ReasonCode: "global_credential_store_unsupported",
				Reason: "Device-global account switching requires a file-backed Codex sign-in.",
			}
		}
	}
	m.mu.Lock()
	m.capabilities = capabilities
	m.mu.Unlock()
	return capabilities
}

func (m *codexAccountManager) view(ids []string) (CodexAccounts, error) {
	records, err := m.catalog.recordsFor(ids)
	if err != nil {
		return CodexAccounts{}, mapUnknownCodexAccount(err)
	}
	m.mu.Lock()
	active, capabilities, unmanaged := m.active, m.capabilities, m.unmanaged
	var activeLogin *CodexActiveLogin
	if m.login != nil && m.login.terminalHandle != "" && !terminalLoginStatus(m.login.snapshot.Status) {
		activeLogin = &CodexActiveLogin{
			OperationID: m.login.snapshot.OperationID, AccountID: m.login.snapshot.AccountID,
			Status: m.login.snapshot.Status, ReasonCode: m.login.snapshot.ReasonCode, Reason: m.login.snapshot.Reason,
			ExpiresAt: m.login.snapshot.ExpiresAt,
			ShellTerminal: CodexLoginTerminalDisplay{
				HandleID: m.login.terminalHandle, Title: m.login.terminalTitle, CreatedAt: m.login.terminalCreated,
			},
		}
	}
	m.mu.Unlock()
	accounts := make([]domain.CodexAccountSnapshot, 0, len(records))
	for _, record := range records {
		snapshot := record.Snapshot
		snapshot.Active = snapshot.ID == active.AccountID
		snapshot.Capacity = m.capacity.snapshot(snapshot.ID)
		m.mu.Lock()
		if usage := m.usage[snapshot.ID]; usage != nil && usage.value != nil {
			usageCopy := *usage.value
			snapshot.UsageSummary = &usageCopy
		}
		m.mu.Unlock()
		accounts = append(accounts, snapshot)
	}
	if active.AccountID != "" {
		for i := range accounts {
			if accounts[i].ID == active.AccountID && i > 0 {
				item := accounts[i]
				copy(accounts[1:i+1], accounts[0:i])
				accounts[0] = item
				break
			}
		}
	}
	return CodexAccounts{ActiveAccountID: active.AccountID, AccountRevision: active.Revision, Accounts: accounts, Capabilities: capabilities, UnmanagedGlobalAccount: unmanaged, ActiveLogin: activeLogin}, nil
}

func (m *codexAccountManager) cached() CodexAccounts { result, _ := m.view(nil); return result }

func (m *codexAccountManager) accountContext(record codexAccountRecord) ports.CodexAccountContext {
	home := record.Home
	m.mu.Lock()
	active := m.active.AccountID
	unmanaged := m.unmanaged != nil
	m.mu.Unlock()
	if record.Snapshot.ID == active && !unmanaged {
		return ports.CodexAccountContext{Home: m.globalHome, Managed: false}
	}
	return ports.CodexAccountContext{Home: home, Managed: true}
}

func (m *codexAccountManager) ensure(ctx context.Context, ids []string, includeUsage bool, installation domain.AgentInstallationState) (CodexAccounts, error) {
	if err := m.catalog.refresh(); err != nil {
		return CodexAccounts{}, apierr.Unavailable("CODEX_ACCOUNT_MANAGEMENT_UNAVAILABLE", "Codex account discovery is unavailable")
	}
	records, err := m.catalog.recordsFor(ids)
	if err != nil {
		return CodexAccounts{}, mapUnknownCodexAccount(err)
	}
	if installation == domain.AgentInstallationNotInstalled {
		return m.view(ids)
	}
	if err := m.reconcileGlobal(ctx); err != nil && !errors.Is(err, context.Canceled) {
		m.logger.Debug("Codex global account reconciliation degraded", "failure_category", "global_account_reconciliation")
	}
	capabilities := m.detectCapabilities(ctx)
	for _, record := range records {
		if record.Snapshot.Status == domain.CodexAccountStatusValid && capabilities.AccountRead.State == domain.CodexCapabilitySupported {
			if _, err := m.ensureAuthentication(ctx, record, domain.AgentReadinessPurposeDisplay, false); err != nil {
				return CodexAccounts{}, err
			}
		}
	}
	records, err = m.catalog.recordsFor(ids)
	if err != nil {
		return CodexAccounts{}, mapUnknownCodexAccount(err)
	}
	if err := m.capacity.ensure(ctx, records, capabilities); err != nil && !errors.Is(err, context.Canceled) {
		m.logger.Debug("Codex account capacity ensure degraded", "failure_category", "capacity_read")
	}
	if includeUsage && capabilities.UsageRead.State == domain.CodexCapabilitySupported {
		for _, record := range records {
			if record.Snapshot.Status == domain.CodexAccountStatusValid &&
				record.Snapshot.Authentication.State == domain.AgentAuthenticationAuthorized &&
				record.Snapshot.AuthMethod == domain.CodexAuthMethodChatGPT {
				_ = m.ensureUsage(ctx, record)
			}
		}
	}
	result, err := m.view(ids)
	m.publish()
	return result, err
}

func (m *codexAccountManager) ensureAuthentication(ctx context.Context, record codexAccountRecord, purpose domain.AgentReadinessPurpose, refreshToken bool) (domain.AgentAuthenticationObservation, error) {
	m.mu.Lock()
	state := m.auth[record.Snapshot.ID]
	if state == nil {
		state = &accountAuthState{invalidated: true}
		m.auth[record.Snapshot.ID] = state
	}
	current, _ := m.catalog.record(record.Snapshot.ID)
	if state.reauthenticationRequired {
		out := current.Snapshot.Authentication
		m.mu.Unlock()
		return out, nil
	}
	ttl := codexAccountDisplayTTL
	if purpose == domain.AgentReadinessPurposeLaunch {
		ttl = codexAccountLaunchTTL
	}
	fresh := current.Snapshot.Authentication.CheckedAt != nil && m.now().Sub(*current.Snapshot.Authentication.CheckedAt) < ttl
	if !state.invalidated && fresh {
		out := current.Snapshot.Authentication
		m.mu.Unlock()
		return out, nil
	}
	if purpose == domain.AgentReadinessPurposeDisplay && !state.nextRetryAt.IsZero() && m.now().Before(state.nextRetryAt) {
		out := current.Snapshot.Authentication
		m.mu.Unlock()
		return out, nil
	}
	if state.call != nil {
		call := state.call
		m.mu.Unlock()
		select {
		case <-call.done:
			latest, _ := m.catalog.record(record.Snapshot.ID)
			return latest.Snapshot.Authentication, nil
		case <-ctx.Done():
			return domain.AgentAuthenticationObservation{}, ctx.Err()
		}
	}
	call := &accountAuthCall{done: make(chan struct{})}
	state.call = call
	m.catalog.updateSnapshot(record.Snapshot.ID, func(s *domain.CodexAccountSnapshot) { s.Authentication.Freshness = domain.AgentReadinessChecking })
	m.mu.Unlock()
	go m.runAuthentication(record, refreshToken, call)
	select {
	case <-call.done:
		latest, _ := m.catalog.record(record.Snapshot.ID)
		return latest.Snapshot.Authentication, nil
	case <-ctx.Done():
		return domain.AgentAuthenticationObservation{}, ctx.Err()
	}
}

func (m *codexAccountManager) runAuthentication(record codexAccountRecord, refresh bool, call *accountAuthCall) {
	attempted := m.now()
	select {
	case m.processes <- struct{}{}:
		defer func() { <-m.processes }()
	case <-m.ctx.Done():
		m.finishAuthentication(record.Snapshot.ID, failedAuthentication(attempted, domain.AgentReadinessReasonAuthCheckFailed, "Authentication check stopped."), domain.CodexAuthMethodUnknown, nil, true, call)
		return
	}
	ctx, cancel := context.WithTimeout(m.ctx, codexAccountAuthTimeout)
	defer cancel()
	account := m.accountContext(record)
	releaseGlobal, err := m.acquireGlobalRead(ctx, account)
	if err != nil {
		m.finishAuthentication(record.Snapshot.ID, failedAuthentication(attempted, domain.AgentReadinessReasonAuthCheckFailed, "Authentication check stopped."), domain.CodexAuthMethodUnknown, nil, true, call)
		return
	}
	defer releaseGlobal()
	if refresh {
		release, err := m.acquireAccountMutation(ctx)
		if err != nil {
			m.finishAuthentication(record.Snapshot.ID, failedAuthentication(attempted, domain.AgentReadinessReasonAuthCheckFailed, "Authentication check stopped."), domain.CodexAuthMethodUnknown, nil, true, call)
			return
		}
		defer release()
	}
	client, err := m.factory.Open(ctx, account)
	if err != nil {
		m.finishAuthentication(record.Snapshot.ID, failedAuthentication(attempted, domain.AgentReadinessReasonAuthCheckFailed, "Authentication check failed."), domain.CodexAuthMethodUnknown, nil, true, call)
		return
	}
	defer func() { _ = client.Close() }()
	observation, err := client.Read(ctx, refresh)
	if err != nil {
		code, reason := domain.AgentReadinessReasonAuthCheckFailed, "Authentication check failed."
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			code, reason = domain.AgentReadinessReasonAuthCheckTimeout, "Authentication check timed out."
		}
		m.finishAuthentication(record.Snapshot.ID, failedAuthentication(attempted, code, reason), domain.CodexAuthMethodUnknown, nil, true, call)
		return
	}
	result := accountAuthenticationObservation(m.now(), observation.Authentication)
	m.finishAuthentication(record.Snapshot.ID, result, observation.Method, observation.Email, observation.Authentication == domain.AgentAuthenticationUnknown, call)
}

func accountAuthenticationObservation(at time.Time, state domain.AgentAuthenticationState) domain.AgentAuthenticationObservation {
	switch state {
	case domain.AgentAuthenticationAuthorized:
		return successfulAuthentication(at, state, domain.AgentReadinessReasonAuthorized, "Codex appears signed in.")
	case domain.AgentAuthenticationUnauthorized:
		return successfulAuthentication(at, state, domain.AgentReadinessReasonUnauthorized, "Codex needs authentication.")
	case domain.AgentAuthenticationNotApplicable:
		return successfulAuthentication(at, state, domain.AgentReadinessReasonAuthNotApplicable, "Codex authentication is not required.")
	default:
		return failedAuthentication(at, domain.AgentReadinessReasonAuthCheckInconclusive, "Authentication check was inconclusive.")
	}
}

func (m *codexAccountManager) finishAuthentication(id string, observation domain.AgentAuthenticationObservation, method domain.CodexAuthMethod, email *string, failed bool, call *accountAuthCall) {
	m.mu.Lock()
	state := m.auth[id]
	if failed {
		m.catalog.updateSnapshot(id, func(s *domain.CodexAccountSnapshot) { preserveAuthenticationFailure(&s.Authentication, observation) })
		state.invalidated = true
		state.failures++
		if state.failures <= len(defaultReadinessRetryDelays) {
			state.nextRetryAt = m.now().Add(defaultReadinessRetryDelays[state.failures-1])
		}
	} else {
		m.catalog.updateSnapshot(id, func(s *domain.CodexAccountSnapshot) {
			s.Authentication = observation
			s.AuthMethod = method
			s.AccountEmail = email
			s.Label = accountLabel(id, method, email)
		})
		state.invalidated = false
		state.failures = 0
		state.nextRetryAt = time.Time{}
	}
	state.call = nil
	close(call.done)
	m.mu.Unlock()
	m.publish()
}

func (m *codexAccountManager) invalidate(id string) {
	m.mu.Lock()
	state := m.auth[id]
	if state == nil {
		state = &accountAuthState{}
		m.auth[id] = state
	}
	state.invalidated = true
	state.nextRetryAt = time.Time{}
	m.mu.Unlock()
	m.catalog.updateSnapshot(id, func(s *domain.CodexAccountSnapshot) { s.Authentication.Freshness = domain.AgentReadinessStale })
	m.capacity.invalidate(id, true)
	m.publish()
}

func (m *codexAccountManager) requireReauthentication(id string) {
	m.mu.Lock()
	state := m.auth[id]
	if state == nil {
		state = &accountAuthState{}
		m.auth[id] = state
	}
	state.reauthenticationRequired = true
	state.invalidated = false
	state.failures = 0
	state.nextRetryAt = time.Time{}
	delete(m.usage, id)
	m.mu.Unlock()
	m.catalog.updateSnapshot(id, func(snapshot *domain.CodexAccountSnapshot) {
		snapshot.Authentication = signedOutAuthentication(m.now(), "Codex could not refresh this account. Sign in again to continue.")
		snapshot.Capacity = unavailableCodexCapacity()
		snapshot.UsageSummary = nil
	})
	m.capacity.replace(id, staticCodexCapacity(domain.CodexCapacityUnknown, domain.CodexCapacityReasonSkippedSignedOut, "Sign in to Codex to see subscription capacity."), "reauthentication_required")
	m.publish()
}

func (m *codexAccountManager) clearReauthenticationRequired(id string) {
	m.mu.Lock()
	state := m.auth[id]
	if state == nil {
		state = &accountAuthState{}
		m.auth[id] = state
	}
	state.reauthenticationRequired = false
	state.invalidated = false
	state.failures = 0
	state.nextRetryAt = time.Time{}
	m.mu.Unlock()
}

func (m *codexAccountManager) ensureUsage(ctx context.Context, record codexAccountRecord) error {
	m.mu.Lock()
	state := m.usage[record.Snapshot.ID]
	if state == nil {
		state = &accountUsageState{}
		m.usage[record.Snapshot.ID] = state
	}
	if state.value != nil && m.now().Sub(state.checkedAt) < codexAccountUsageTTL {
		m.mu.Unlock()
		return nil
	}
	if state.call != nil {
		call := state.call
		m.mu.Unlock()
		select {
		case <-call:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	call := make(chan struct{})
	state.call = call
	m.mu.Unlock()
	go m.runUsage(record, state, call)
	select {
	case <-call:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *codexAccountManager) runUsage(record codexAccountRecord, state *accountUsageState, call chan struct{}) {
	defer func() {
		m.mu.Lock()
		if state.call == call {
			state.call = nil
			close(call)
		}
		m.mu.Unlock()
	}()
	select {
	case m.processes <- struct{}{}:
		defer func() { <-m.processes }()
	case <-m.ctx.Done():
		return
	}
	readCtx, cancel := context.WithTimeout(m.ctx, codexAccountAuthTimeout)
	defer cancel()
	account := m.accountContext(record)
	releaseGlobal, err := m.acquireGlobalRead(readCtx, account)
	if err != nil {
		return
	}
	defer releaseGlobal()
	client, err := m.factory.Open(readCtx, account)
	if err != nil {
		return
	}
	defer func() { _ = client.Close() }()
	usage, err := client.ReadUsage(readCtx)
	if err != nil {
		return
	}
	value := &domain.CodexAccountUsageSummary{
		LatestDayTokens: usage.LatestDayTokens, LatestDayStartDate: usage.LatestDayStartDate,
		LifetimeTokens: usage.LifetimeTokens, PeakDailyTokens: usage.PeakDailyTokens,
		LongestRunningTurnSeconds: usage.LongestRunningTurnSeconds,
		CurrentStreakDays:         usage.CurrentStreakDays, LongestStreakDays: usage.LongestStreakDays,
		ObservedAt: usage.ObservedAt,
	}
	m.mu.Lock()
	state.value = value
	state.checkedAt = m.now()
	m.mu.Unlock()
	m.publish()
}

func (m *codexAccountManager) consumeResetCredit(ctx context.Context, accountID, idempotencyKey string) error {
	accountID, idempotencyKey = strings.TrimSpace(accountID), strings.TrimSpace(idempotencyKey)
	if idempotencyKey == "" {
		return apierr.Invalid("IDEMPOTENCY_KEY_REQUIRED", "Idempotency key is required", nil)
	}
	if err := m.catalog.refresh(); err != nil {
		return apierr.Unavailable("CODEX_ACCOUNT_MANAGEMENT_UNAVAILABLE", "Codex account discovery is unavailable")
	}
	record, ok := m.catalog.record(accountID)
	if !ok {
		return apierr.NotFound("CODEX_ACCOUNT_NOT_FOUND", "Codex account not found")
	}
	if record.Snapshot.Status != domain.CodexAccountStatusValid {
		return apierr.Conflict("CODEX_ACCOUNT_UNAVAILABLE", "This Codex account is unavailable", nil)
	}
	account := m.accountContext(record)
	releaseGlobal, err := m.acquireGlobalRead(ctx, account)
	if err != nil {
		return err
	}
	defer releaseGlobal()
	release, err := m.acquireAccountMutation(ctx)
	if err != nil {
		return err
	}
	defer release()
	m.mu.Lock()
	loginActive := m.login != nil && !terminalLoginStatus(m.login.snapshot.Status)
	m.mu.Unlock()
	if loginActive {
		return apierr.Conflict("CODEX_ACCOUNT_LOGIN_IN_PROGRESS", "Finish or close the Codex account login before using a reset", nil)
	}
	if m.factory == nil {
		return apierr.Unavailable("CODEX_RESET_CREDIT_UNAVAILABLE", "Codex usage-limit reset is unavailable")
	}
	capabilities := m.detectCapabilities(ctx)
	switch capabilities.ResetCreditConsume.State {
	case domain.CodexCapabilityUnsupported:
		return apierr.NotImplemented("CODEX_RESET_CREDIT_UNSUPPORTED", "This Codex version does not support usage-limit resets")
	case domain.CodexCapabilityUnknown, "":
		return apierr.Unavailable("CODEX_RESET_CREDIT_UNAVAILABLE", "Codex usage-limit reset support could not be confirmed")
	}
	select {
	case m.processes <- struct{}{}:
		defer func() { <-m.processes }()
	case <-ctx.Done():
		return ctx.Err()
	}
	operationCtx, cancel := context.WithTimeout(ctx, codexResetCreditTimeout)
	defer cancel()
	client, err := m.factory.Open(operationCtx, account)
	if err != nil {
		return apierr.Unavailable("CODEX_RESET_CREDIT_UNAVAILABLE", "Codex usage-limit reset could not be started")
	}
	defer func() { _ = client.Close() }()
	auth, err := client.Read(operationCtx, true)
	if err != nil || auth.Authentication != domain.AgentAuthenticationAuthorized || auth.Method != domain.CodexAuthMethodChatGPT {
		return apierr.Conflict("CODEX_ACCOUNT_AUTH_UNVERIFIED", "Confirm this Codex account before using a reset", nil)
	}
	attemptedAt := m.now()
	before, err := client.ReadCapacity(operationCtx)
	if err != nil {
		return apierr.Unavailable("CODEX_RESET_CREDIT_UNAVAILABLE", "Available Codex usage-limit resets could not be confirmed")
	}
	m.capacity.acceptDirect(accountID, before, attemptedAt)
	if before.ResetCredits == nil || before.ResetCredits.AvailableCount <= 0 {
		return apierr.Conflict("CODEX_RESET_CREDIT_UNAVAILABLE", "No Codex usage-limit reset is currently available", nil)
	}
	outcome, err := client.ConsumeResetCredit(operationCtx, idempotencyKey)
	if err != nil {
		m.capacity.invalidateAfterReset(accountID)
		return apierr.Unavailable("CODEX_RESET_CREDIT_UNAVAILABLE", "Codex could not confirm the usage-limit reset")
	}
	switch outcome {
	case domain.CodexResetCreditNoCredit:
		m.capacity.invalidateAfterReset(accountID)
		return apierr.Conflict("CODEX_RESET_CREDIT_UNAVAILABLE", "No Codex usage-limit reset is currently available", nil)
	case domain.CodexResetCreditNothingToReset:
		return apierr.Conflict("CODEX_USAGE_LIMIT_RESET_NOT_APPLICABLE", "No current Codex usage limit is eligible for reset", nil)
	case domain.CodexResetCreditReset, domain.CodexResetCreditAlreadyRedeemed:
		m.capacity.invalidateAfterReset(accountID)
	default:
		m.capacity.invalidateAfterReset(accountID)
		return apierr.Unavailable("CODEX_RESET_CREDIT_UNAVAILABLE", "Codex returned an unknown usage-limit reset result")
	}
	after, readErr := client.ReadCapacity(operationCtx)
	if readErr == nil {
		m.capacity.acceptDirect(accountID, after, attemptedAt)
	}
	m.publish()
	return nil
}

func (m *codexAccountManager) subscribe(ctx context.Context) <-chan CodexAccounts {
	ch := make(chan CodexAccounts, 1)
	m.mu.Lock()
	m.subscribers[ch] = struct{}{}
	m.mu.Unlock()
	ch <- m.cached()
	go func() { <-ctx.Done(); m.mu.Lock(); delete(m.subscribers, ch); close(ch); m.mu.Unlock() }()
	return ch
}
func (m *codexAccountManager) publish() {
	snapshot := m.cached()
	m.mu.Lock()
	defer m.mu.Unlock()
	for ch := range m.subscribers {
		select {
		case ch <- snapshot:
		default:
			select {
			case <-ch:
			default:
			}
			select {
			case ch <- snapshot:
			default:
			}
		}
	}
}
