package agent

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const otherCodexAccountID = "bb1e9a5d-37ad-43f8-83bd-13de8168f8af"

// codexLaunchReadinessFixture reproduces the important protocol distinction:
// account/read discovers local metadata while ReadCapacity is a protected call
// that confirms whether the provider accepts the account.
type codexLaunchReadinessFixture struct {
	t                        *testing.T
	manager                  *codexAccountManager
	service                  *Service
	active                   codexAccountRecord
	other                    codexAccountRecord
	mu                       sync.Mutex
	activeCapacityErrors     []error
	refreshReturnsAuthorized bool
	reads                    []codexReadCall
	activeCapacityReads      int
}

type codexReadCall struct {
	managed bool
	refresh bool
}

func newCodexLaunchReadinessFixture(t *testing.T) *codexLaunchReadinessFixture {
	t.Helper()
	root := t.TempDir()
	globalHome := filepath.Join(root, "global-codex")
	if err := ensurePrivateDirectory(globalHome); err != nil {
		t.Fatal(err)
	}
	state := &fakeCodexAccountStateStore{active: domain.CodexActiveAccount{AccountID: testAccountID, Revision: 1}, found: true}
	manager := newCodexAccountManager(context.Background(),
		filepath.Join(root, "accounts"), filepath.Join(root, "pending"),
		filepath.Join(root, "staging"), globalHome, nil, state, nil)
	ids := []string{testAccountID, otherCodexAccountID, "6f8dfc76-8db4-4621-8974-c480093e0d55"}
	manager.catalog.newID = func() string { id := ids[0]; ids = ids[1:]; return id }
	manager.newID = func() string { return "b9a4e5c6-4f31-4b1a-9d2a-7b4a4c0f9a11" }
	activeEmail, otherEmail := "active@example.com", "other@example.com"
	active := commitTestAccount(t, manager.catalog, manager.pendingRoot, "b60a377d-da68-4a61-86f2-f31f04c571f2", ports.CodexAccountObservation{
		Authentication: domain.AgentAuthenticationAuthorized, Method: domain.CodexAuthMethodChatGPT, Email: &activeEmail,
	})
	other := commitTestAccount(t, manager.catalog, manager.pendingRoot, "1c5de3ab-82d0-4a68-a06b-8495cdeab909", ports.CodexAccountObservation{
		Authentication: domain.AgentAuthenticationAuthorized, Method: domain.CodexAuthMethodChatGPT, Email: &otherEmail,
	})
	activeCredential, err := readOpaqueCredential(filepath.Join(active.Home, codexCredentialFilename))
	if err != nil {
		t.Fatal(err)
	}
	// The active account is backed by the device-global Codex home.
	if err := writeGlobalCredentialAtomic(manager.globalCredentialPath(), activeCredential); err != nil {
		t.Fatal(err)
	}
	manager.active = state.active
	manager.accountStoreReady = true
	manager.reconciliation = domain.CodexDeviceReconciliation{Status: domain.CodexDeviceReconciliationVerified, ActiveAccountVerified: true, ReasonCode: "verified"}
	fixture := &codexLaunchReadinessFixture{t: t, manager: manager, active: active, other: other}
	manager.factory = &fakeCodexAccountFactory{capabilities: supportedCodexAccountCapabilities(), open: func(account ports.CodexAccountContext) (ports.CodexAccountClient, error) {
		return &fakeCodexAccountClient{readFn: func(_ context.Context, refresh bool) (ports.CodexAccountObservation, error) {
			fixture.mu.Lock()
			fixture.reads = append(fixture.reads, codexReadCall{managed: account.Managed, refresh: refresh})
			refreshAuthorized := fixture.refreshReturnsAuthorized
			fixture.mu.Unlock()
			if account.Home == other.Home {
				return ports.CodexAccountObservation{Authentication: domain.AgentAuthenticationAuthorized, Method: domain.CodexAuthMethodChatGPT, Email: &otherEmail}, nil
			}
			if refresh && !refreshAuthorized {
				return ports.CodexAccountObservation{Authentication: domain.AgentAuthenticationUnauthorized, Method: domain.CodexAuthMethodUnknown}, nil
			}
			return ports.CodexAccountObservation{Authentication: domain.AgentAuthenticationAuthorized, Method: domain.CodexAuthMethodChatGPT, Email: &activeEmail}, nil
		}, capacityFn: func(context.Context) (ports.CodexCapacityObservation, error) {
			if account.Home == other.Home {
				return ports.CodexCapacityObservation{}, nil
			}
			fixture.mu.Lock()
			defer fixture.mu.Unlock()
			index := fixture.activeCapacityReads
			fixture.activeCapacityReads++
			if index < len(fixture.activeCapacityErrors) {
				return ports.CodexCapacityObservation{}, fixture.activeCapacityErrors[index]
			}
			return ports.CodexCapacityObservation{}, nil
		}}, nil
	}}
	fixture.service = &Service{codexAccounts: manager, readiness: newReadinessCoordinator(readinessCoordinatorConfig{})}
	return fixture
}

func (f *codexLaunchReadinessFixture) signIn() {
	f.mu.Lock()
	f.refreshReturnsAuthorized = true
	f.mu.Unlock()
}

func (f *codexLaunchReadinessFixture) rejectActiveCapacity(errors ...error) {
	f.mu.Lock()
	f.activeCapacityErrors = append([]error(nil), errors...)
	f.activeCapacityReads = 0
	f.mu.Unlock()
}

func (f *codexLaunchReadinessFixture) refreshCapableReads() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	count := 0
	for _, read := range f.reads {
		if read.refresh {
			count++
		}
	}
	return count
}

func (f *codexLaunchReadinessFixture) protectedReads() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.activeCapacityReads
}

func (f *codexLaunchReadinessFixture) ensureSettings() CodexAccounts {
	f.t.Helper()
	result, err := f.manager.ensure(context.Background(), nil, false, domain.AgentInstallationInstalled)
	if err != nil {
		f.t.Fatal(err)
	}
	return result
}

func (f *codexLaunchReadinessFixture) account(view CodexAccounts, id string) domain.CodexAccountSnapshot {
	f.t.Helper()
	for _, account := range view.Accounts {
		if account.ID == id {
			return account
		}
	}
	f.t.Fatalf("account %q missing from view %#v", id, view.Accounts)
	return domain.CodexAccountSnapshot{}
}

func TestSettingsUsesProtectedCallInsteadOfRefreshAccountRead(t *testing.T) {
	fixture := newCodexLaunchReadinessFixture(t)

	view := fixture.ensureSettings()

	active := fixture.account(view, fixture.active.Snapshot.ID)
	if active.Authentication.State != domain.AgentAuthenticationAuthorized || active.Authentication.Freshness != domain.AgentReadinessFresh {
		t.Fatalf("Settings did not retain the protected-call confirmation = %#v", active.Authentication)
	}
	if fixture.protectedReads() == 0 {
		t.Fatal("Settings classified the active account without a protected read")
	}
	if fixture.refreshCapableReads() != 0 {
		t.Fatalf("Settings used account/read refresh as authentication evidence: %#v", fixture.reads)
	}
}

func TestLaunchFallsBackToNativeReadinessWhileDeviceReconciliationIsUnavailable(t *testing.T) {
	fixture := newCodexLaunchReadinessFixture(t)
	fixture.manager.mu.Lock()
	fixture.manager.reconciliation = domain.CodexDeviceReconciliation{
		Status:     domain.CodexDeviceReconciliationTemporarilyUnavailable,
		ReasonCode: "account_read_inconclusive", Retryable: true,
	}
	fixture.manager.mu.Unlock()

	authentication, handled := fixture.service.structuredCodexAuthentication(
		context.Background(), string(domain.HarnessCodex), domain.AgentReadinessPurposeLaunch,
	)
	if handled {
		t.Fatalf("unverified device authentication was handled = %#v", authentication)
	}
	if fixture.refreshCapableReads() != 0 {
		t.Fatalf("structured account path performed a native read: %#v", fixture.reads)
	}
}

func TestLaunchFallsBackToNativeReadinessOnTransientProtectedFailure(t *testing.T) {
	fixture := newCodexLaunchReadinessFixture(t)
	fixture.rejectActiveCapacity(ports.ErrCodexCapacityProviderUnavailable)
	authentication, handled := fixture.service.structuredCodexAuthentication(context.Background(), string(domain.HarnessCodex), domain.AgentReadinessPurposeLaunch)
	if handled {
		t.Fatalf("transient provider failure became an auth decision = %#v", authentication)
	}
	latest, _ := fixture.manager.catalog.record(fixture.active.Snapshot.ID)
	if latest.Snapshot.Authentication.State == domain.AgentAuthenticationUnauthorized {
		t.Fatalf("transient provider failure signed the account out = %#v", latest.Snapshot.Authentication)
	}
}

func TestDisplayReadInFlightCannotClearARequiredReauthentication(t *testing.T) {
	started, release := make(chan struct{}, 1), make(chan struct{})
	client := &fakeCodexAccountClient{
		read:        ports.CodexAccountObservation{Authentication: domain.AgentAuthenticationAuthorized, Method: domain.CodexAuthMethodChatGPT},
		readStarted: started, readRelease: release,
	}
	factory := &fakeCodexAccountFactory{open: func(ports.CodexAccountContext) (ports.CodexAccountClient, error) { return client, nil }}
	manager := newTestCodexAccountManager(t, factory, nil)
	manager.catalog.newID = func() string { return testAccountID }
	record := commitTestAccount(t, manager.catalog, manager.pendingRoot, "b60a377d-da68-4a61-86f2-f31f04c571f2", ports.CodexAccountObservation{
		Authentication: domain.AgentAuthenticationAuthorized, Method: domain.CodexAuthMethodChatGPT,
	})
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = manager.ensureAuthentication(context.Background(), record, domain.AgentReadinessPurposeDisplay)
	}()
	<-started

	manager.requireReauthentication(record.Snapshot.ID)
	close(release)
	<-done

	latest, _ := manager.catalog.record(record.Snapshot.ID)
	if latest.Snapshot.Authentication.State != domain.AgentAuthenticationUnauthorized || latest.Snapshot.Authentication.Freshness != domain.AgentReadinessFresh {
		t.Fatalf("display read cleared the required reauthentication = %#v", latest.Snapshot.Authentication)
	}
}

func TestAuthorizedInactiveAccountDoesNotMaskTheActiveAccount(t *testing.T) {
	fixture := newCodexLaunchReadinessFixture(t)
	fixture.rejectActiveCapacity(ports.ErrCodexOAuthTokenRevoked, ports.ErrCodexOAuthTokenRevoked)

	view := fixture.ensureSettings()

	if other := fixture.account(view, fixture.other.Snapshot.ID); other.Authentication.State != domain.AgentAuthenticationAuthorized {
		t.Fatalf("inactive account authentication = %#v", other.Authentication)
	}
	authentication, ok := fixture.service.structuredCodexAuthentication(context.Background(), string(domain.HarnessCodex), domain.AgentReadinessPurposeDisplay)
	if !ok || authentication.State != domain.AgentAuthenticationUnauthorized {
		t.Fatalf("Codex readiness masked by the inactive account = %#v (structured=%t)", authentication, ok)
	}
	// Only the rejected active account is refreshed; inactive accounts are not
	// refreshed merely to render the list.
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	for _, read := range fixture.reads {
		if read.managed && read.refresh {
			t.Fatalf("an inactive account was refreshed for display: %#v", fixture.reads)
		}
	}
}

func TestSuccessfulReauthenticationRestoresLaunchReadiness(t *testing.T) {
	fixture := newCodexLaunchReadinessFixture(t)
	fixture.ensureSettings()

	fixture.signIn()
	fixture.manager.invalidate(fixture.active.Snapshot.ID)
	view := fixture.ensureSettings()

	active := fixture.account(view, fixture.active.Snapshot.ID)
	if active.Authentication.State != domain.AgentAuthenticationAuthorized || active.Authentication.Freshness != domain.AgentReadinessFresh {
		t.Fatalf("Settings did not recover after reauthentication = %#v", active.Authentication)
	}
	launch, ok := fixture.service.structuredCodexAuthentication(context.Background(), string(domain.HarnessCodex), domain.AgentReadinessPurposeLaunch)
	if !ok || launch.State != domain.AgentAuthenticationAuthorized {
		t.Fatalf("launch readiness after reauthentication = %#v (structured=%t)", launch, ok)
	}
}

func TestRepeatedSettingsEnsuresReuseProtectedConfirmation(t *testing.T) {
	fixture := newCodexLaunchReadinessFixture(t)
	first := fixture.ensureSettings()
	reads := fixture.protectedReads()
	checkedAt := fixture.account(first, fixture.active.Snapshot.ID).Authentication.CheckedAt

	second := fixture.ensureSettings()

	if got := fixture.protectedReads(); got != reads {
		t.Fatalf("protected reads grew from %d to %d across two ensures inside the display window", reads, got)
	}
	active := fixture.account(second, fixture.active.Snapshot.ID)
	if checkedAt == nil || active.Authentication.CheckedAt == nil || !active.Authentication.CheckedAt.Equal(*checkedAt) {
		t.Fatalf("observation timestamp moved: %v then %v", checkedAt, active.Authentication.CheckedAt)
	}
}

func TestRotatedCredentialsDoNotReVerifyAnAuthorizedAccount(t *testing.T) {
	fixture := newCodexLaunchReadinessFixture(t)
	fixture.signIn()
	fixture.ensureSettings()
	reads := fixture.protectedReads()

	// A refresh-capable read can rotate Codex's device-global refresh token, so
	// the saved copy stops matching. That is expected token maintenance for an
	// account already proven launch-ready, not new evidence about it.
	if err := writeGlobalCredentialAtomic(fixture.manager.globalCredentialPath(), []byte("rotated-opaque-credential")); err != nil {
		t.Fatal(err)
	}

	view := fixture.ensureSettings()

	if got := fixture.protectedReads(); got != reads {
		t.Fatalf("rotated credentials re-verified an authorized account: reads %d then %d", reads, got)
	}
	if active := fixture.account(view, fixture.active.Snapshot.ID); active.Authentication.State != domain.AgentAuthenticationAuthorized {
		t.Fatalf("active account authentication = %#v", active.Authentication)
	}
}

func TestReplacedCredentialsReVerifyALaunchFailure(t *testing.T) {
	fixture := newCodexLaunchReadinessFixture(t)
	fixture.rejectActiveCapacity(ports.ErrCodexOAuthTokenRevoked, ports.ErrCodexOAuthTokenRevoked)
	view := fixture.ensureSettings()
	if active := fixture.account(view, fixture.active.Snapshot.ID); active.Authentication.State != domain.AgentAuthenticationUnauthorized {
		t.Fatalf("active account authentication = %#v", active.Authentication)
	}
	reads := fixture.protectedReads()

	// The user signs in again outside AO. The device-global account material is
	// replaced, so the recorded failure no longer describes this account and must
	// not survive the rest of the display window.
	fixture.signIn()
	fixture.rejectActiveCapacity()
	if err := writeGlobalCredentialAtomic(fixture.manager.globalCredentialPath(), []byte("replacement-opaque-credential")); err != nil {
		t.Fatal(err)
	}
	if err := fixture.manager.reconcileGlobal(context.Background()); err != nil {
		t.Fatal(err)
	}

	view = fixture.ensureSettings()

	if got := fixture.protectedReads(); got == 0 || got > reads {
		t.Fatalf("replaced credentials did not get one fresh protected verification: before %d after reset %d", reads, got)
	}
	if active := fixture.account(view, fixture.active.Snapshot.ID); active.Authentication.State != domain.AgentAuthenticationAuthorized {
		t.Fatalf("Settings did not recover after an out-of-band sign-in = %#v", active.Authentication)
	}
}
