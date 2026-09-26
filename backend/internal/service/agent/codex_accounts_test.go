package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	agentregistry "github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/registry"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/shellterm"
)

type fakeCodexAccountFactory struct {
	mu               sync.Mutex
	opens            int
	capabilityChecks int
	capabilities     domain.CodexAccountCapabilities
	open             func(ports.CodexAccountContext) (ports.CodexAccountClient, error)
}

func (f *fakeCodexAccountFactory) Open(_ context.Context, account ports.CodexAccountContext) (ports.CodexAccountClient, error) {
	f.mu.Lock()
	f.opens++
	open := f.open
	f.mu.Unlock()
	if open == nil {
		return nil, errors.New("unexpected account client open")
	}
	return open(account)
}

func (f *fakeCodexAccountFactory) Capabilities(context.Context) domain.CodexAccountCapabilities {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.capabilityChecks++
	return f.capabilities
}

type fakeCodexAccountClient struct {
	read            ports.CodexAccountObservation
	readErr         error
	readFn          func(context.Context, bool) (ports.CodexAccountObservation, error)
	readStarted     chan struct{}
	readRelease     chan struct{}
	capacity        ports.CodexCapacityObservation
	capacityErr     error
	capacityFn      func(context.Context) (ports.CodexCapacityObservation, error)
	capacityStarted chan struct{}
	capacityRelease chan struct{}
	usage           ports.CodexUsageObservation
	resetOutcome    domain.CodexResetCreditOutcome
	resetErr        error
	resetKeys       []string
	resetFn         func(string) (domain.CodexResetCreditOutcome, error)
	events          chan ports.CodexAccountEvent
	logoutErr       error
	logoutCalls     int
	logoutFn        func(context.Context) error
}

func (c *fakeCodexAccountClient) Logout(ctx context.Context) error {
	c.logoutCalls++
	if c.logoutFn != nil {
		return c.logoutFn(ctx)
	}
	return c.logoutErr
}

func (c *fakeCodexAccountClient) Read(ctx context.Context, refreshToken bool) (ports.CodexAccountObservation, error) {
	if c.readFn != nil {
		return c.readFn(ctx, refreshToken)
	}
	if c.readStarted != nil {
		select {
		case c.readStarted <- struct{}{}:
		default:
		}
	}
	if c.readRelease != nil {
		select {
		case <-c.readRelease:
		case <-ctx.Done():
			return ports.CodexAccountObservation{}, ctx.Err()
		}
	}
	return c.read, c.readErr
}

func (c *fakeCodexAccountClient) ReadCapacity(ctx context.Context) (ports.CodexCapacityObservation, error) {
	if c.capacityStarted != nil {
		select {
		case c.capacityStarted <- struct{}{}:
		default:
		}
	}
	if c.capacityRelease != nil {
		select {
		case <-c.capacityRelease:
		case <-ctx.Done():
			return ports.CodexCapacityObservation{}, ctx.Err()
		}
	}
	if c.capacityFn != nil {
		return c.capacityFn(ctx)
	}
	return c.capacity, c.capacityErr
}

func (c *fakeCodexAccountClient) ReadUsage(context.Context) (ports.CodexUsageObservation, error) {
	return c.usage, nil
}
func (c *fakeCodexAccountClient) ConsumeResetCredit(_ context.Context, idempotencyKey string) (domain.CodexResetCreditOutcome, error) {
	c.resetKeys = append(c.resetKeys, idempotencyKey)
	if c.resetFn != nil {
		return c.resetFn(idempotencyKey)
	}
	return c.resetOutcome, c.resetErr
}
func (c *fakeCodexAccountClient) Events() <-chan ports.CodexAccountEvent {
	if c.events == nil {
		ch := make(chan ports.CodexAccountEvent)
		close(ch)
		return ch
	}
	return c.events
}
func (c *fakeCodexAccountClient) Close() error { return nil }

func fakeNativeLogoutClient(t *testing.T, account ports.CodexAccountContext) ports.CodexAccountClient {
	t.Helper()
	return &fakeCodexAccountClient{logoutFn: func(context.Context) error {
		err := os.Remove(filepath.Join(account.Home, codexCredentialFilename))
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		return nil
	}}
}

type testCodexDeviceAccount struct {
	AccountID   string
	Revision    int64
	ActivatedAt time.Time
	UpdatedAt   time.Time
}

func setTestDeviceAccount(manager *codexAccountManager, active testCodexDeviceAccount) {
	manager.deviceAccountID = active.AccountID
	manager.deviceCredentialPresent = active.AccountID != ""
	manager.snapshotRevision = active.Revision
	manager.reconciliation = domain.CodexDeviceReconciliation{
		Status: domain.CodexDeviceReconciliationVerified, ActiveAccountVerified: active.AccountID != "", ReasonCode: "verified",
	}
}

type testCodexDeviceStateSeed interface {
	testDeviceAccount() (testCodexDeviceAccount, bool)
}

type testCodexDeviceSeed struct {
	active testCodexDeviceAccount
	found  bool
}

func (s *testCodexDeviceSeed) testDeviceAccount() (testCodexDeviceAccount, bool) {
	return s.active, s.found
}

type blockingExclusiveCodexGate struct {
	entered chan struct{}
	release chan struct{}
}

type noopCodexLease struct{}

func (noopCodexLease) Release() {}
func (*blockingExclusiveCodexGate) AcquireShared(context.Context) (func(), error) {
	return func() {}, nil
}
func (*blockingExclusiveCodexGate) AcquireSharedWait(context.Context) (func(), error) {
	return func() {}, nil
}
func (g *blockingExclusiveCodexGate) AcquireExclusive(ctx context.Context) (ports.CodexOperationLease, error) {
	close(g.entered)
	select {
	case <-g.release:
		return noopCodexLease{}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
func (*blockingExclusiveCodexGate) ExclusivePendingOrHeld() bool { return false }

type fakeCodexLoginTerminal struct {
	mu              sync.Mutex
	opened          []shellterm.OpenCommandTerminalInput
	closed          []string
	result          shellterm.ShellTerminal
	closeErr        error
	writeCredential bool
	credential      []byte
	childExited     bool
}

func (f *fakeCodexLoginTerminal) OpenCommandTerminal(_ context.Context, in shellterm.OpenCommandTerminalInput) (shellterm.ShellTerminal, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.opened = append(f.opened, in)
	if f.writeCredential {
		credential := f.credential
		if credential == nil {
			credential = testOAuthCredential("", "opaque-login-credential")
		}
		if err := writePrivateFileAtomic(filepath.Join(in.Env["CODEX_HOME"], codexCredentialFilename), credential); err != nil {
			return shellterm.ShellTerminal{}, err
		}
	}
	return f.result, nil
}

func (f *fakeCodexLoginTerminal) CloseShellTerminal(_ context.Context, handle string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closeErr != nil {
		return f.closeErr
	}
	f.closed = append(f.closed, handle)
	return nil
}

func (f *fakeCodexLoginTerminal) IsShellTerminalChildAlive(_ context.Context, _ string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return !f.childExited, nil
}

func supportedCodexAccountCapabilities() domain.CodexAccountCapabilities {
	supported := domain.CodexCapabilityObservation{State: domain.CodexCapabilitySupported, ReasonCode: domain.CodexCapabilityReasonSupported, Reason: "supported"}
	return domain.CodexAccountCapabilities{
		AccountRead: supported, NativeLogin: supported, CapacityRead: supported,
		UsageRead: supported, ResetCreditConsume: supported, GlobalSwitch: supported,
	}
}

func newTestCodexAccountManager(t *testing.T, factory ports.CodexAccountClientFactory, state testCodexDeviceStateSeed) *codexAccountManager {
	t.Helper()
	root := t.TempDir()
	manager := newCodexAccountManager(context.Background(),
		filepath.Join(root, "accounts"), filepath.Join(root, "pending-accounts"),
		filepath.Join(root, "switch-staging"), filepath.Join(root, "device-home"),
		factory, nil)
	manager.snapshotRevision = 0
	if state != nil {
		if active, ok := state.testDeviceAccount(); ok && active.AccountID != "" {
			manager.deviceAccountID = active.AccountID
			manager.deviceCredentialPresent = true
			manager.snapshotRevision = active.Revision
			manager.reconciliation = domain.CodexDeviceReconciliation{
				Status: domain.CodexDeviceReconciliationVerified, ActiveAccountVerified: true, ReasonCode: "verified",
			}
		}
	}
	return manager
}

func TestCachedCodexAccountsPerformsNoFilesystemOrNativeWork(t *testing.T) {
	factory := &fakeCodexAccountFactory{open: func(ports.CodexAccountContext) (ports.CodexAccountClient, error) {
		t.Fatal("cached account read opened Codex")
		return nil, nil
	}}
	manager := newTestCodexAccountManager(t, factory, nil)
	result := manager.cached()
	if len(result.Accounts) != 0 || result.AccountRevision != 0 {
		t.Fatalf("cached accounts = %#v", result)
	}
	if factory.opens != 0 || factory.capabilityChecks != 0 {
		t.Fatalf("native work: opens=%d capability=%d", factory.opens, factory.capabilityChecks)
	}
}

func TestUnverifiedDeviceAssociationUsesSavedHomeDuringTemporaryReconciliationFailure(t *testing.T) {
	manager := newTestCodexAccountManager(t, nil, nil)
	manager.catalog.newID = func() string { return testAccountID }
	record := commitTestAccount(t, manager.catalog, manager.pendingRoot, "b60a377d-da68-4a61-86f2-f31f04c571f2", ports.CodexAccountObservation{
		Authentication: domain.AgentAuthenticationAuthorized,
		Method:         domain.CodexAuthMethodAPIKey,
	})
	manager.mu.Lock()
	setTestDeviceAccount(manager, testCodexDeviceAccount{AccountID: record.Snapshot.ID, Revision: 2})
	manager.reconciliation = domain.CodexDeviceReconciliation{
		Status:                domain.CodexDeviceReconciliationTemporarilyUnavailable,
		ActiveAccountVerified: false,
		ReasonCode:            "account_read_inconclusive",
		Retryable:             true,
	}
	manager.mu.Unlock()

	context := manager.accountContext(record)
	if context.Home != record.Home || !context.Managed {
		t.Fatalf("stale active account context = %#v, want isolated saved home", context)
	}
}

func TestCheckingReconciliationKeepsLastMatchedAccountVisibleWithoutUsingGlobalHome(t *testing.T) {
	manager := newTestCodexAccountManager(t, nil, nil)
	inactiveID := "11111111-1111-4111-8111-111111111111"
	ids := []string{inactiveID, testAccountID}
	nextID := 0
	manager.catalog.newID = func() string {
		id := ids[nextID]
		nextID++
		return id
	}
	inactiveEmail := "inactive@example.com"
	commitTestAccountWithCredential(t, manager.catalog, manager.pendingRoot, "1c5de3ab-82d0-4a68-a06b-8495cdeab909", []byte("inactive-credential"), ports.CodexAccountObservation{
		Authentication: domain.AgentAuthenticationAuthorized,
		Method:         domain.CodexAuthMethodChatGPT,
		Email:          &inactiveEmail,
	})
	activeEmail := "active@example.com"
	record := commitTestAccountWithCredential(t, manager.catalog, manager.pendingRoot, "b60a377d-da68-4a61-86f2-f31f04c571f2", []byte("active-credential"), ports.CodexAccountObservation{
		Authentication: domain.AgentAuthenticationAuthorized,
		Method:         domain.CodexAuthMethodChatGPT,
		Email:          &activeEmail,
	})
	manager.mu.Lock()
	setTestDeviceAccount(manager, testCodexDeviceAccount{AccountID: record.Snapshot.ID, Revision: 2})
	manager.deviceAccountID = record.Snapshot.ID
	manager.deferredAccountID = record.Snapshot.ID
	manager.reconciliation = domain.CodexDeviceReconciliation{
		Status:                domain.CodexDeviceReconciliationChecking,
		ActiveAccountVerified: false,
		ReasonCode:            "checking",
	}
	manager.mu.Unlock()

	view := manager.cached()
	if view.ActiveAccountID != "" || len(view.Accounts) != 2 || view.Accounts[0].Active || view.Accounts[1].Active {
		t.Fatalf("checking reconciliation exposed an unverified active account: %#v", view)
	}
	accountContext := manager.accountContext(record)
	if accountContext.Home != record.Home || !accountContext.Managed {
		t.Fatalf("checking reconciliation used the unverified global home: %#v", accountContext)
	}
}

func TestNativeLoginTerminalUsesOnePrivatePendingHomeAndNoName(t *testing.T) {
	manager := newTestCodexAccountManager(t, nil, nil)
	manager.newID = func() string { return "b60a377d-da68-4a61-86f2-f31f04c571f2" }
	manager.executable = func() (string, error) { return "/Applications/AO.app/Contents/MacOS/ao", nil }
	terminal := &fakeCodexLoginTerminal{result: shellterm.ShellTerminal{HandleID: "shellterm-login-1", Title: "Add Codex account"}}
	manager.terminal = terminal
	started, err := manager.openLoginTerminal(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if started.Operation.Status != domain.CodexAccountLoginPending || started.Operation.OperationID == "" {
		t.Fatalf("login start = %#v", started)
	}
	if len(terminal.opened) != 1 {
		t.Fatalf("terminal opens = %d", len(terminal.opened))
	}
	opened := terminal.opened[0]
	if !slices.Equal(opened.Argv, []string{"/Applications/AO.app/Contents/MacOS/ao", "codex-login"}) {
		t.Fatalf("argv = %#v", opened.Argv)
	}
	home := opened.Env["CODEX_HOME"]
	if home == "" || home != opened.WorkingDir || !pathWithin(manager.pendingRoot, home) {
		t.Fatalf("pending login home = %q, workdir = %q", home, opened.WorkingDir)
	}
}

func TestCachedCodexAccountsProjectsOnlySafeActiveLoginMetadata(t *testing.T) {
	manager := newTestCodexAccountManager(t, nil, nil)
	manager.newID = func() string { return "b60a377d-da68-4a61-86f2-f31f04c571f2" }
	manager.executable = func() (string, error) { return "/Applications/AO.app/Contents/MacOS/ao-private", nil }
	createdAt := time.Date(2026, time.September, 2, 10, 30, 0, 0, time.UTC)
	terminal := &fakeCodexLoginTerminal{result: shellterm.ShellTerminal{
		HandleID: "shellterm-login-safe", WorkingDir: "/private/login-home", Title: "Add Codex account", CreatedAt: createdAt,
	}}
	manager.terminal = terminal
	started, err := manager.openLoginTerminal(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}

	payload, err := json.Marshal(manager.cached())
	if err != nil {
		t.Fatal(err)
	}
	text := string(payload)
	for _, want := range []string{`"activeLogin"`, `"operationId":"` + started.Operation.OperationID + `"`, `"handleId":"shellterm-login-safe"`, `"title":"Add Codex account"`, `"createdAt":"2026-09-02T10:30:00Z"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("cached account login missing %s: %s", want, payload)
		}
	}
	for _, forbidden := range []string{"pending-accounts", "/private/login-home", "ao-private", "CODEX_HOME", "workingDir", "argv", "env"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("cached account login leaked %q: %s", forbidden, payload)
		}
	}

	if _, err := manager.cancelLogin(context.Background(), started.Operation.OperationID); err != nil {
		t.Fatal(err)
	}
	payload, err = json.Marshal(manager.cached())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(payload), `"activeLogin"`) {
		t.Fatalf("terminal login remained active: %s", payload)
	}
}

func TestNativeLoginCreatesAndActivatesFirstAccountWithoutProviderCheck(t *testing.T) {
	email := "person@example.com"
	client := &fakeCodexAccountClient{read: ports.CodexAccountObservation{Authentication: domain.AgentAuthenticationAuthorized, Method: domain.CodexAuthMethodChatGPT, Email: &email}}
	factory := &fakeCodexAccountFactory{capabilities: supportedCodexAccountCapabilities(), open: func(ports.CodexAccountContext) (ports.CodexAccountClient, error) { return client, nil }}
	manager := newTestCodexAccountManager(t, factory, nil)
	ids := []string{"b60a377d-da68-4a61-86f2-f31f04c571f2", testAccountID}
	index := 0
	manager.newID = func() string { id := ids[index]; index++; return id }
	manager.catalog.newID = func() string { return testAccountID }
	manager.executable = func() (string, error) { return "/ao", nil }
	terminal := &fakeCodexLoginTerminal{writeCredential: true, credential: testOAuthCredential("provider-account", "opaque-login-credential"), result: shellterm.ShellTerminal{HandleID: "shellterm-login-1", Title: "Add Codex account"}}
	manager.terminal = terminal
	started, err := manager.openLoginTerminal(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	completed, err := manager.verifyLogin(context.Background(), started.Operation.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status != domain.CodexAccountLoginCompleted || completed.Account == nil || !completed.Account.Active || completed.Account.Authentication.State != domain.AgentAuthenticationAuthorized {
		t.Fatalf("completed login = %#v", completed)
	}
	if active := manager.activeAccountID(); active != testAccountID {
		t.Fatalf("active account = %q", active)
	}
	if len(terminal.closed) != 1 || terminal.closed[0] != "shellterm-login-1" {
		t.Fatalf("closed terminals = %#v", terminal.closed)
	}
	credential := filepath.Join(manager.catalog.root, testAccountID, codexCredentialHomeDirectory, codexCredentialFilename)
	data, err := os.ReadFile(credential)
	if err != nil || !bytes.Equal(data, testOAuthCredential("provider-account", "opaque-login-credential")) {
		t.Fatalf("opaque credential = %q, err=%v", data, err)
	}
	if factory.opens != 0 {
		t.Fatalf("login transaction opened %d Codex clients", factory.opens)
	}
}

func TestNativeLoginVerificationDoesNotReplaceExistingDeviceAccount(t *testing.T) {
	email := "saved@example.com"
	client := &fakeCodexAccountClient{read: ports.CodexAccountObservation{Authentication: domain.AgentAuthenticationAuthorized, Method: domain.CodexAuthMethodChatGPT, Email: &email}}
	factory := &fakeCodexAccountFactory{capabilities: supportedCodexAccountCapabilities(), open: func(ports.CodexAccountContext) (ports.CodexAccountClient, error) { return client, nil }}
	manager := newTestCodexAccountManager(t, factory, nil)
	if err := ensurePrivateDirectory(manager.globalHome); err != nil {
		t.Fatal(err)
	}
	original := []byte("existing-device-credential")
	if err := writeGlobalCredentialAtomic(manager.globalCredentialPath(), original); err != nil {
		t.Fatal(err)
	}
	ids := []string{"b60a377d-da68-4a61-86f2-f31f04c571f2", testAccountID}
	index := 0
	manager.newID = func() string { id := ids[index]; index++; return id }
	manager.catalog.newID = func() string { return testAccountID }
	manager.executable = func() (string, error) { return "/ao", nil }
	manager.terminal = &fakeCodexLoginTerminal{writeCredential: true, result: shellterm.ShellTerminal{HandleID: "shellterm-login-1", Title: "Add Codex account"}}

	started, err := manager.openLoginTerminal(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	completed, err := manager.verifyLogin(context.Background(), started.Operation.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status != domain.CodexAccountLoginCompleted || completed.Account == nil || completed.Account.Active {
		t.Fatalf("completed login = %#v", completed)
	}
	current, err := readOpaqueCredential(manager.globalCredentialPath())
	if err != nil || !bytes.Equal(current, original) {
		t.Fatalf("existing device credential changed: %q, %v", current, err)
	}
	if manager.activeAccountID() != "" {
		t.Fatalf("saved account became active: %q", manager.activeAccountID())
	}
}

func TestNativeLoginVerificationSavesAccountWhileDeviceReconciliationRetries(t *testing.T) {
	email := "person@example.com"
	client := &fakeCodexAccountClient{read: ports.CodexAccountObservation{
		Authentication: domain.AgentAuthenticationAuthorized, Method: domain.CodexAuthMethodChatGPT, Email: &email,
	}}
	factory := &fakeCodexAccountFactory{
		capabilities: supportedCodexAccountCapabilities(),
		open:         func(ports.CodexAccountContext) (ports.CodexAccountClient, error) { return client, nil },
	}
	manager := newTestCodexAccountManager(t, factory, nil)
	manager.mu.Lock()
	manager.reconciliation = domain.CodexDeviceReconciliation{
		Status:     domain.CodexDeviceReconciliationTemporarilyUnavailable,
		ReasonCode: "account_read_inconclusive", Retryable: true,
	}
	manager.mu.Unlock()
	ids := []string{"b60a377d-da68-4a61-86f2-f31f04c571f2", testAccountID}
	index := 0
	manager.newID = func() string { id := ids[index]; index++; return id }
	manager.catalog.newID = func() string { return testAccountID }
	manager.executable = func() (string, error) { return "/ao", nil }
	manager.terminal = &fakeCodexLoginTerminal{
		writeCredential: true,
		result:          shellterm.ShellTerminal{HandleID: "shellterm-login-1", Title: "Add Codex account"},
	}

	started, err := manager.openLoginTerminal(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	completed, err := manager.verifyLogin(context.Background(), started.Operation.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status != domain.CodexAccountLoginCompleted || completed.Account == nil || !completed.Account.Active || completed.AccountID != testAccountID {
		t.Fatalf("completed login = %#v", completed)
	}
	if active := manager.activeAccountID(); active != testAccountID {
		t.Fatalf("first saved account was not activated: active=%q", active)
	}
	if _, err := readOpaqueCredential(filepath.Join(manager.catalog.root, testAccountID, codexCredentialHomeDirectory, codexCredentialFilename)); err != nil {
		t.Fatalf("saved account credential: %v", err)
	}
	if credential, err := readOpaqueCredential(manager.globalCredentialPath()); err != nil || !bytes.Equal(credential, testOAuthCredential("", "opaque-login-credential")) {
		t.Fatalf("first saved account was not installed on the device: %q, %v", credential, err)
	}
}

func TestNativeAPIKeyLoginDoesNotClaimProviderValidation(t *testing.T) {
	client := &fakeCodexAccountClient{read: ports.CodexAccountObservation{
		Authentication: domain.AgentAuthenticationAuthorized,
		Method:         domain.CodexAuthMethodAPIKey,
	}}
	manager := newTestCodexAccountManager(t, &fakeCodexAccountFactory{
		capabilities: supportedCodexAccountCapabilities(),
		open:         func(ports.CodexAccountContext) (ports.CodexAccountClient, error) { return client, nil },
	}, nil)
	manager.newID = func() string { return "b60a377d-da68-4a61-86f2-f31f04c571f2" }
	manager.catalog.newID = func() string { return testAccountID }
	manager.executable = func() (string, error) { return "/ao", nil }
	manager.terminal = &fakeCodexLoginTerminal{
		writeCredential: true,
		credential:      testAPIKeyCredential("sk-garbage"),
		result:          shellterm.ShellTerminal{HandleID: "shellterm-login-api-key", Title: "Add Codex account"},
	}

	started, err := manager.openLoginTerminal(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	completed, err := manager.verifyLogin(context.Background(), started.Operation.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status != domain.CodexAccountLoginCompleted || completed.Account == nil {
		t.Fatalf("completed login = %#v", completed)
	}
	if completed.Account.Authentication.State != domain.AgentAuthenticationUnknown || completed.Account.Authentication.ReasonCode != domain.AgentReadinessReasonAuthCheckInconclusive {
		t.Fatalf("API-key authentication = %#v", completed.Account.Authentication)
	}
	if !strings.Contains(completed.Reason, "did not verify") {
		t.Fatalf("login reason = %q", completed.Reason)
	}

	manager.invalidate(completed.Account.ID)
	record, ok := manager.catalog.record(completed.Account.ID)
	if !ok {
		t.Fatal("saved API-key account missing from catalog")
	}
	observation, err := manager.ensureAuthentication(context.Background(), record, domain.AgentReadinessPurposeDisplay)
	if err != nil {
		t.Fatal(err)
	}
	if observation.State != domain.AgentAuthenticationUnknown || observation.ReasonCode != domain.AgentReadinessReasonAuthCheckInconclusive {
		t.Fatalf("refreshed API-key authentication = %#v", observation)
	}
}

func TestNativeReauthenticationReplacesTheExistingAccountSlot(t *testing.T) {
	email := "person@example.com"
	observation := ports.CodexAccountObservation{Authentication: domain.AgentAuthenticationAuthorized, Method: domain.CodexAuthMethodChatGPT, Email: &email}
	client := &fakeCodexAccountClient{
		read: observation,
		capacity: ports.CodexCapacityObservation{Overall: &domain.CodexCapacityBucket{
			LimitID: "codex", Reached: domain.CodexCapacityNotReached,
			Primary: &domain.CodexCapacityWindow{UsedPercent: 58},
		}},
	}
	factory := &fakeCodexAccountFactory{capabilities: supportedCodexAccountCapabilities(), open: func(ports.CodexAccountContext) (ports.CodexAccountClient, error) { return client, nil }}
	manager := newTestCodexAccountManager(t, factory, nil)
	manager.catalog.newID = func() string { return testAccountID }
	record := commitTestAccountWithCredential(t, manager.catalog, manager.pendingRoot, "b60a377d-da68-4a61-86f2-f31f04c571f2", testOAuthCredential("provider-account", "old-access"), observation)
	manager.requireReauthentication(record.Snapshot.ID)
	if before := manager.cached().Accounts[0].Capacity; before.ReasonCode != domain.CodexCapacityReasonSkippedSignedOut {
		t.Fatalf("capacity before reauthentication = %#v", before)
	}
	manager.newID = func() string { return "1c5de3ab-82d0-4a68-a06b-8495cdeab909" }
	manager.executable = func() (string, error) { return "/ao", nil }
	manager.terminal = &fakeCodexLoginTerminal{
		writeCredential: true,
		credential:      testOAuthCredential("provider-account", "new-access"),
		result:          shellterm.ShellTerminal{HandleID: "shellterm-login-reauth", Title: "Sign in to Codex account"},
	}

	started, err := manager.openLoginTerminal(context.Background(), record.Snapshot.ID)
	if err != nil {
		t.Fatal(err)
	}
	if started.Operation.AccountID != record.Snapshot.ID {
		t.Fatalf("reauthentication target = %q", started.Operation.AccountID)
	}
	completed, err := manager.verifyLogin(context.Background(), started.Operation.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status != domain.CodexAccountLoginCompleted || completed.Account == nil || completed.Account.ID != record.Snapshot.ID {
		t.Fatalf("completed reauthentication = %#v", completed)
	}
	if completed.Account.Authentication.State != domain.AgentAuthenticationAuthorized || completed.Account.Capacity.State != domain.CodexCapacityUnknown {
		t.Fatalf("completed reauthentication state = %#v", completed.Account)
	}
	if completed.Account.Capacity.ReasonCode == domain.CodexCapacityReasonSkippedSignedOut {
		t.Fatalf("completed reauthentication retained signed-out capacity: %#v", completed.Account.Capacity)
	}
	cached := manager.cached().Accounts[0]
	if cached.Authentication.State != domain.AgentAuthenticationAuthorized || cached.Capacity.State != domain.CodexCapacityUnknown {
		t.Fatalf("cached reauthentication state = %#v", cached)
	}
	if snapshots := manager.catalog.snapshots(); len(snapshots) != 1 || snapshots[0].ID != record.Snapshot.ID {
		t.Fatalf("reauthentication changed account identity: %#v", snapshots)
	}
	credential, err := readOpaqueCredential(filepath.Join(record.Home, codexCredentialFilename))
	if err != nil || !bytes.Equal(credential, testOAuthCredential("provider-account", "new-access")) {
		t.Fatalf("replacement credential = %q, err=%v", credential, err)
	}
}

func TestActiveReauthenticationRevalidatesAndReplacesTheDeviceCredential(t *testing.T) {
	email := "person@example.com"
	observation := ports.CodexAccountObservation{
		Authentication: domain.AgentAuthenticationAuthorized, Method: domain.CodexAuthMethodChatGPT, Email: &email,
	}
	factory := &fakeCodexAccountFactory{
		capabilities: supportedCodexAccountCapabilities(),
		open: func(ports.CodexAccountContext) (ports.CodexAccountClient, error) {
			return &fakeCodexAccountClient{read: observation}, nil
		},
	}
	state := &testCodexDeviceSeed{
		active: testCodexDeviceAccount{AccountID: testAccountID, Revision: 1}, found: true,
	}
	manager := newTestCodexAccountManager(t, factory, state)
	manager.catalog.newID = func() string { return testAccountID }
	record := commitTestAccountWithCredential(t, manager.catalog, manager.pendingRoot, "b60a377d-da68-4a61-86f2-f31f04c571f2", testOAuthCredential("provider-account", "old-access"), observation)
	oldCredential, err := readOpaqueCredential(filepath.Join(record.Home, codexCredentialFilename))
	if err != nil {
		t.Fatal(err)
	}
	if err := writeGlobalCredentialAtomic(manager.globalCredentialPath(), oldCredential); err != nil {
		t.Fatal(err)
	}
	manager.mu.Lock()
	setTestDeviceAccount(manager, state.active)
	manager.markDeviceReconciledLocked(true, time.Now().UTC())
	manager.mu.Unlock()
	manager.newID = func() string { return "1c5de3ab-82d0-4a68-a06b-8495cdeab909" }
	manager.executable = func() (string, error) { return "/ao", nil }
	manager.terminal = &fakeCodexLoginTerminal{
		writeCredential: true,
		credential:      testOAuthCredential("provider-account", "new-access"),
		result:          shellterm.ShellTerminal{HandleID: "shellterm-login-reauth", Title: "Sign in to Codex account"},
	}

	started, err := manager.openLoginTerminal(context.Background(), record.Snapshot.ID)
	if err != nil {
		t.Fatal(err)
	}
	completed, err := manager.verifyLogin(context.Background(), started.Operation.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status != domain.CodexAccountLoginCompleted || completed.Account == nil || !completed.Account.Active {
		t.Fatalf("completed reauthentication = %#v", completed)
	}
	globalCredential, err := readOpaqueCredential(manager.globalCredentialPath())
	if err != nil || !bytes.Equal(globalCredential, testOAuthCredential("provider-account", "new-access")) {
		t.Fatalf("device credential = %q, err=%v", globalCredential, err)
	}
	if manager.activeAccountID() != testAccountID {
		t.Fatalf("active account = %q", manager.activeAccountID())
	}
}

func TestOpenReauthenticationReconcilesBeforeCapturingActiveOwnership(t *testing.T) {
	observation := ports.CodexAccountObservation{Authentication: domain.AgentAuthenticationAuthorized, Method: domain.CodexAuthMethodChatGPT}
	factory := &fakeCodexAccountFactory{capabilities: supportedCodexAccountCapabilities()}
	manager := newTestCodexAccountManager(t, factory, nil)
	manager.catalog.newID = func() string { return testAccountID }
	credential := testOAuthCredential("provider-account", "old-access")
	record := commitTestAccountWithCredential(t, manager.catalog, manager.pendingRoot, "b60a377d-da68-4a61-86f2-f31f04c571f2", credential, observation)
	if err := writeGlobalCredentialAtomic(manager.globalCredentialPath(), credential); err != nil {
		t.Fatal(err)
	}
	manager.mu.Lock()
	manager.deviceAccountID = ""
	manager.reconciliation = domain.CodexDeviceReconciliation{Status: domain.CodexDeviceReconciliationNotChecked, ActiveAccountVerified: false}
	manager.mu.Unlock()
	manager.executable = func() (string, error) { return "/ao", nil }
	manager.terminal = &fakeCodexLoginTerminal{result: shellterm.ShellTerminal{HandleID: "shellterm-login-reauth", Title: "Sign in to Codex account"}}
	installed := &readinessTestAgent{resolve: func(context.Context) (string, error) { return "/ao", nil }}
	service := &Service{codexAccounts: manager, readiness: newReadinessCoordinator(readinessCoordinatorConfig{
		Agents: []agentregistry.HarnessAgent{readinessHarness("codex", "Codex", installed)},
	})}

	started, err := service.OpenCodexAccountReauthenticationTerminal(context.Background(), record.Snapshot.ID)
	if err != nil {
		t.Fatal(err)
	}
	manager.mu.Lock()
	targetWasActive := manager.login != nil && manager.login.targetWasActive
	manager.mu.Unlock()
	if !targetWasActive {
		t.Fatalf("operation did not capture reconciled device ownership: %#v", started.Operation)
	}
}

func TestActiveReauthenticationPreservesCredentialsWhenDeviceChangesAfterTerminalOpens(t *testing.T) {
	observation := ports.CodexAccountObservation{
		Authentication: domain.AgentAuthenticationAuthorized,
		Method:         domain.CodexAuthMethodChatGPT,
	}
	manager := newTestCodexAccountManager(t, nil, nil)
	manager.catalog.newID = func() string { return testAccountID }
	oldCredential := testOAuthCredential("provider-account", "old-access")
	record := commitTestAccountWithCredential(t, manager.catalog, manager.pendingRoot, "b60a377d-da68-4a61-86f2-f31f04c571f2", oldCredential, observation)
	if err := writeGlobalCredentialAtomic(manager.globalCredentialPath(), oldCredential); err != nil {
		t.Fatal(err)
	}
	manager.mu.Lock()
	setTestDeviceAccount(manager, testCodexDeviceAccount{AccountID: record.Snapshot.ID, Revision: 1})
	manager.markDeviceReconciledLocked(true, time.Now().UTC())
	manager.mu.Unlock()
	manager.newID = func() string { return "1c5de3ab-82d0-4a68-a06b-8495cdeab909" }
	manager.executable = func() (string, error) { return "/ao", nil }
	newCredential := testOAuthCredential("provider-account", "new-access")
	manager.terminal = &fakeCodexLoginTerminal{
		writeCredential: true,
		credential:      newCredential,
		result:          shellterm.ShellTerminal{HandleID: "shellterm-login-reauth", Title: "Sign in to Codex account"},
	}

	started, err := manager.openLoginTerminal(context.Background(), record.Snapshot.ID)
	if err != nil {
		t.Fatal(err)
	}
	externalCredential := testOAuthCredential("external-account", "external-access")
	if err := writeGlobalCredentialAtomic(manager.globalCredentialPath(), externalCredential); err != nil {
		t.Fatal(err)
	}

	result, err := manager.verifyLogin(context.Background(), started.Operation.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != domain.CodexAccountLoginRetryable {
		t.Fatalf("reauthentication result = %#v", result)
	}
	globalCredential, err := readOpaqueCredential(manager.globalCredentialPath())
	if err != nil || !bytes.Equal(globalCredential, externalCredential) {
		t.Fatalf("external device credential changed: credential=%q err=%v", globalCredential, err)
	}
	savedCredential, err := readOpaqueCredential(filepath.Join(record.Home, codexCredentialFilename))
	if err != nil || !bytes.Equal(savedCredential, oldCredential) {
		t.Fatalf("saved credential changed: credential=%q err=%v", savedCredential, err)
	}
	pendingCredential, err := readOpaqueCredential(filepath.Join(manager.login.home, codexCredentialFilename))
	if err != nil || !bytes.Equal(pendingCredential, newCredential) {
		t.Fatalf("pending credential was not preserved: credential=%q err=%v", pendingCredential, err)
	}
}

func TestActiveReauthenticationCommitsWhenProviderIsUnavailable(t *testing.T) {
	email := "person@example.com"
	observation := ports.CodexAccountObservation{
		Authentication: domain.AgentAuthenticationAuthorized, Method: domain.CodexAuthMethodChatGPT, Email: &email,
	}
	factory := &fakeCodexAccountFactory{
		capabilities: supportedCodexAccountCapabilities(),
		open: func(account ports.CodexAccountContext) (ports.CodexAccountClient, error) {
			if !account.Managed {
				return nil, errors.New("temporary device verification failure")
			}
			return &fakeCodexAccountClient{read: observation}, nil
		},
	}
	state := &testCodexDeviceSeed{
		active: testCodexDeviceAccount{AccountID: testAccountID, Revision: 1}, found: true,
	}
	manager := newTestCodexAccountManager(t, factory, state)
	manager.catalog.newID = func() string { return testAccountID }
	record := commitTestAccountWithCredential(t, manager.catalog, manager.pendingRoot, "b60a377d-da68-4a61-86f2-f31f04c571f2", testOAuthCredential("provider-account", "old-access"), observation)
	oldCredential, err := readOpaqueCredential(filepath.Join(record.Home, codexCredentialFilename))
	if err != nil {
		t.Fatal(err)
	}
	if err := writeGlobalCredentialAtomic(manager.globalCredentialPath(), oldCredential); err != nil {
		t.Fatal(err)
	}
	manager.mu.Lock()
	setTestDeviceAccount(manager, state.active)
	manager.markDeviceReconciledLocked(true, time.Now().UTC())
	manager.mu.Unlock()
	manager.newID = func() string { return "1c5de3ab-82d0-4a68-a06b-8495cdeab909" }
	manager.executable = func() (string, error) { return "/ao", nil }
	manager.terminal = &fakeCodexLoginTerminal{
		writeCredential: true,
		credential:      testOAuthCredential("provider-account", "new-access"),
		result:          shellterm.ShellTerminal{HandleID: "shellterm-login-reauth", Title: "Sign in to Codex account"},
	}

	started, err := manager.openLoginTerminal(context.Background(), record.Snapshot.ID)
	if err != nil {
		t.Fatal(err)
	}
	completed, err := manager.verifyLogin(context.Background(), started.Operation.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status != domain.CodexAccountLoginCompleted || completed.Account == nil || !completed.Account.Active {
		t.Fatalf("completed reauthentication = %#v", completed)
	}
	if factory.opens != 0 {
		t.Fatalf("reauthentication opened %d Codex clients", factory.opens)
	}
}

func TestRequiredReauthenticationStaysSignedOutUntilLogin(t *testing.T) {
	factory := &fakeCodexAccountFactory{open: func(ports.CodexAccountContext) (ports.CodexAccountClient, error) {
		t.Fatal("account requiring sign-in was read again")
		return nil, nil
	}}
	manager := newTestCodexAccountManager(t, factory, nil)
	manager.catalog.newID = func() string { return testAccountID }
	record := commitTestAccount(t, manager.catalog, manager.pendingRoot, "b60a377d-da68-4a61-86f2-f31f04c571f2", ports.CodexAccountObservation{Authentication: domain.AgentAuthenticationAuthorized, Method: domain.CodexAuthMethodChatGPT})
	manager.requireReauthentication(record.Snapshot.ID)

	authentication, err := manager.ensureAuthentication(context.Background(), record, domain.AgentReadinessPurposeDisplay)
	if err != nil {
		t.Fatal(err)
	}
	if authentication.State != domain.AgentAuthenticationUnauthorized || authentication.Freshness != domain.AgentReadinessFresh {
		t.Fatalf("authentication = %#v", authentication)
	}
	view := manager.cached()
	if len(view.Accounts) != 1 || view.Accounts[0].Authentication.State != domain.AgentAuthenticationUnauthorized || view.Accounts[0].UsageSummary != nil {
		t.Fatalf("account awaiting sign-in = %#v", view.Accounts)
	}
}

func TestLogoutRetainsInactiveAccountAsSignedOut(t *testing.T) {
	factory := &fakeCodexAccountFactory{open: func(account ports.CodexAccountContext) (ports.CodexAccountClient, error) {
		return fakeNativeLogoutClient(t, account), nil
	}}
	manager := newTestCodexAccountManager(t, factory, nil)
	manager.catalog.newID = func() string { return testAccountID }
	record := commitTestAccount(t, manager.catalog, manager.pendingRoot, "b60a377d-da68-4a61-86f2-f31f04c571f2", ports.CodexAccountObservation{Authentication: domain.AgentAuthenticationAuthorized, Method: domain.CodexAuthMethodChatGPT})

	if err := manager.logout(context.Background(), record.Snapshot.ID); err != nil {
		t.Fatal(err)
	}
	view := manager.cached()
	if len(view.Accounts) != 1 || view.Accounts[0].ID != record.Snapshot.ID || view.Accounts[0].Status != domain.CodexAccountStatusSignedOut || view.Accounts[0].Authentication.State != domain.AgentAuthenticationUnauthorized {
		t.Fatalf("logged-out account = %#v", view.Accounts)
	}
}

func TestNativeLogoutFailurePreservesInactiveAccountCredential(t *testing.T) {
	client := &fakeCodexAccountClient{logoutErr: errors.New("provider unavailable")}
	manager := newTestCodexAccountManager(t, &fakeCodexAccountFactory{open: func(ports.CodexAccountContext) (ports.CodexAccountClient, error) {
		return client, nil
	}}, nil)
	manager.catalog.newID = func() string { return testAccountID }
	record := commitTestAccountWithCredential(t, manager.catalog, manager.pendingRoot, "b60a377d-da68-4a61-86f2-f31f04c571f2", testOAuthCredential("provider-account", "access"), ports.CodexAccountObservation{
		Authentication: domain.AgentAuthenticationAuthorized, Method: domain.CodexAuthMethodChatGPT,
	})

	err := manager.logout(context.Background(), record.Snapshot.ID)
	var apiError *apierr.Error
	if !errors.As(err, &apiError) || apiError.Code != "CODEX_ACCOUNT_LOGOUT_UNCONFIRMED" {
		t.Fatalf("logout error = %#v", err)
	}
	if _, readErr := readOpaqueCredential(filepath.Join(record.Home, codexCredentialFilename)); readErr != nil {
		t.Fatalf("logout failure removed credential: %v", readErr)
	}
	retained, _ := manager.catalog.record(record.Snapshot.ID)
	if retained.Snapshot.Status != domain.CodexAccountStatusValid || client.logoutCalls != 1 {
		t.Fatalf("account after failed logout = %#v, calls=%d", retained.Snapshot, client.logoutCalls)
	}
}

func TestUnsupportedNativeLogoutPreservesAccountCredential(t *testing.T) {
	client := &fakeCodexAccountClient{logoutErr: ports.ErrCodexAccountLogoutUnsupported}
	manager := newTestCodexAccountManager(t, &fakeCodexAccountFactory{open: func(ports.CodexAccountContext) (ports.CodexAccountClient, error) {
		return client, nil
	}}, nil)
	manager.catalog.newID = func() string { return testAccountID }
	record := commitTestAccountWithCredential(t, manager.catalog, manager.pendingRoot, "b60a377d-da68-4a61-86f2-f31f04c571f2", testOAuthCredential("provider-account", "access"), ports.CodexAccountObservation{
		Authentication: domain.AgentAuthenticationAuthorized, Method: domain.CodexAuthMethodChatGPT,
	})

	err := manager.logout(context.Background(), record.Snapshot.ID)
	var apiError *apierr.Error
	if !errors.As(err, &apiError) || apiError.Code != "CODEX_ACCOUNT_LOGOUT_UNSUPPORTED" {
		t.Fatalf("logout error = %#v", err)
	}
	if _, readErr := readOpaqueCredential(filepath.Join(record.Home, codexCredentialFilename)); readErr != nil {
		t.Fatalf("unsupported logout removed credential: %v", readErr)
	}
}

func TestInactiveLogoutDoesNotAcquireGlobalGate(t *testing.T) {
	state := &testCodexDeviceSeed{active: testCodexDeviceAccount{AccountID: "source-account", Revision: 1}, found: true}
	manager := newTestCodexAccountManager(t, nil, state)
	manager.catalog.newID = func() string { return testAccountID }
	record := commitTestAccount(t, manager.catalog, manager.pendingRoot, "b60a377d-da68-4a61-86f2-f31f04c571f2", ports.CodexAccountObservation{
		Authentication: domain.AgentAuthenticationAuthorized, Method: domain.CodexAuthMethodAPIKey,
	})
	credential := testAPIKeyCredential("target-api-key")
	if err := writePrivateFileAtomic(filepath.Join(record.Home, codexCredentialFilename), credential); err != nil {
		t.Fatal(err)
	}
	setTestDeviceAccount(manager, state.active)
	manager.factory = &fakeCodexAccountFactory{open: func(account ports.CodexAccountContext) (ports.CodexAccountClient, error) {
		if account.Home != record.Home {
			t.Fatalf("logout home = %q, want isolated %q", account.Home, record.Home)
		}
		return fakeNativeLogoutClient(t, account), nil
	}}
	gate := &blockingExclusiveCodexGate{entered: make(chan struct{}), release: make(chan struct{})}
	manager.operationGate = gate
	if err := manager.logout(context.Background(), record.Snapshot.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-gate.entered:
		t.Fatal("inactive logout acquired the global gate")
	default:
	}
	if _, err := os.Stat(filepath.Join(record.Home, codexCredentialFilename)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("inactive credential remains: %v", err)
	}
}

func TestServiceInactiveLogoutDoesNotDependOnDeviceReconciliation(t *testing.T) {
	manager := newTestCodexAccountManager(t, nil, nil)
	ids := []string{testAccountID, "bb1e9a5d-37ad-43f8-83bd-13de8168f8af"}
	manager.catalog.newID = func() string { id := ids[0]; ids = ids[1:]; return id }
	observation := ports.CodexAccountObservation{Authentication: domain.AgentAuthenticationAuthorized, Method: domain.CodexAuthMethodChatGPT}
	source := commitTestAccountWithCredential(t, manager.catalog, manager.pendingRoot, "b60a377d-da68-4a61-86f2-f31f04c571f2", testOAuthCredential("source-account", "source-access"), observation)
	target := commitTestAccountWithCredential(t, manager.catalog, manager.pendingRoot, "1c5de3ab-82d0-4a68-a06b-8495cdeab909", testOAuthCredential("target-account", "target-access"), observation)
	manager.mu.Lock()
	manager.reconciliation = domain.CodexDeviceReconciliation{Status: domain.CodexDeviceReconciliationChecking, ActiveAccountVerified: false}
	manager.deferredAccountID = source.Snapshot.ID
	manager.mu.Unlock()
	manager.globalHome = ""
	manager.factory = &fakeCodexAccountFactory{open: func(account ports.CodexAccountContext) (ports.CodexAccountClient, error) {
		if account.Home != target.Home || !account.Managed {
			t.Fatalf("inactive logout context = %#v", account)
		}
		return fakeNativeLogoutClient(t, account), nil
	}}
	service := &Service{codexAccounts: manager, readiness: newReadinessCoordinator(readinessCoordinatorConfig{})}

	if _, err := service.LogoutCodexAccount(context.Background(), target.Snapshot.ID); err != nil {
		t.Fatalf("inactive logout depended on global reconciliation: %v", err)
	}
	result, ok := manager.catalog.record(target.Snapshot.ID)
	if !ok || result.Snapshot.Status != domain.CodexAccountStatusSignedOut {
		t.Fatalf("inactive account = %#v, found=%t", result.Snapshot, ok)
	}
}

func TestDeleteAccountOwnsNativeLogoutBeforeCatalogRemoval(t *testing.T) {
	client := &fakeCodexAccountClient{}
	manager := newTestCodexAccountManager(t, &fakeCodexAccountFactory{open: func(account ports.CodexAccountContext) (ports.CodexAccountClient, error) {
		client.logoutFn = func(context.Context) error {
			return os.Remove(filepath.Join(account.Home, codexCredentialFilename))
		}
		return client, nil
	}}, nil)
	manager.catalog.newID = func() string { return testAccountID }
	record := commitTestAccountWithCredential(t, manager.catalog, manager.pendingRoot, "b60a377d-da68-4a61-86f2-f31f04c571f2", testOAuthCredential("provider-account", "access"), ports.CodexAccountObservation{
		Authentication: domain.AgentAuthenticationAuthorized,
		Method:         domain.CodexAuthMethodChatGPT,
	})

	if err := manager.deleteAccount(context.Background(), record.Snapshot.ID); err != nil {
		t.Fatal(err)
	}
	if client.logoutCalls != 1 {
		t.Fatalf("logout calls = %d, want 1", client.logoutCalls)
	}
	if accounts := manager.cached().Accounts; len(accounts) != 0 {
		t.Fatalf("accounts after deletion = %#v", accounts)
	}
}

func TestDeleteAlreadySignedOutAccountSkipsNativeLogout(t *testing.T) {
	manager := newTestCodexAccountManager(t, &fakeCodexAccountFactory{open: func(ports.CodexAccountContext) (ports.CodexAccountClient, error) {
		t.Fatal("native logout opened for signed-out account")
		return nil, nil
	}}, nil)
	manager.catalog.newID = func() string { return testAccountID }
	record := commitTestAccount(t, manager.catalog, manager.pendingRoot, "b60a377d-da68-4a61-86f2-f31f04c571f2", ports.CodexAccountObservation{
		Authentication: domain.AgentAuthenticationAuthorized, Method: domain.CodexAuthMethodChatGPT,
	})
	if _, err := manager.catalog.markSignedOut(record.Snapshot.ID); err != nil {
		t.Fatal(err)
	}
	if err := manager.deleteAccount(context.Background(), record.Snapshot.ID); err != nil {
		t.Fatal(err)
	}
}

func TestLogoutActiveAccountClearsDeviceCredentialAndActiveProjection(t *testing.T) {
	email := "active@example.com"
	observation := ports.CodexAccountObservation{Authentication: domain.AgentAuthenticationAuthorized, Method: domain.CodexAuthMethodChatGPT, Email: &email}
	factory := &fakeCodexAccountFactory{open: func(account ports.CodexAccountContext) (ports.CodexAccountClient, error) {
		return fakeNativeLogoutClient(t, account), nil
	}}
	state := &testCodexDeviceSeed{active: testCodexDeviceAccount{AccountID: testAccountID, Revision: 1}, found: true}
	manager := newTestCodexAccountManager(t, factory, state)
	manager.catalog.newID = func() string { return testAccountID }
	record := commitTestAccount(t, manager.catalog, manager.pendingRoot, "b60a377d-da68-4a61-86f2-f31f04c571f2", observation)
	setTestDeviceAccount(manager, state.active)
	if err := ensurePrivateDirectory(manager.globalHome); err != nil {
		t.Fatal(err)
	}
	credential := []byte("active-device-credential")
	if err := writePrivateFileAtomic(filepath.Join(record.Home, codexCredentialFilename), credential); err != nil {
		t.Fatal(err)
	}
	if err := writeGlobalCredentialAtomic(manager.globalCredentialPath(), credential); err != nil {
		t.Fatal(err)
	}

	if err := manager.logout(context.Background(), record.Snapshot.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(manager.globalCredentialPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("device credential still exists: %v", err)
	}
	if manager.activeAccountID() != "" {
		t.Fatalf("active account = %q", manager.activeAccountID())
	}
	loggedOut, _ := manager.catalog.record(record.Snapshot.ID)
	if loggedOut.Snapshot.Status != domain.CodexAccountStatusSignedOut {
		t.Fatalf("active account after logout = %#v", loggedOut.Snapshot)
	}
}

func TestLogoutActiveAPIKeyRejectsExternalCredentialReplacement(t *testing.T) {
	state := &testCodexDeviceSeed{active: testCodexDeviceAccount{AccountID: testAccountID, Revision: 1}, found: true}
	manager := newTestCodexAccountManager(t, nil, state)
	manager.catalog.newID = func() string { return testAccountID }
	record := commitTestAccount(t, manager.catalog, manager.pendingRoot, "b60a377d-da68-4a61-86f2-f31f04c571f2", ports.CodexAccountObservation{
		Authentication: domain.AgentAuthenticationAuthorized, Method: domain.CodexAuthMethodAPIKey,
	})
	setTestDeviceAccount(manager, state.active)
	if err := ensurePrivateDirectory(manager.globalHome); err != nil {
		t.Fatal(err)
	}
	if err := writePrivateFileAtomic(filepath.Join(record.Home, codexCredentialFilename), testAPIKeyCredential("saved-api-key")); err != nil {
		t.Fatal(err)
	}
	if err := writeGlobalCredentialAtomic(manager.globalCredentialPath(), testAPIKeyCredential("saved-api-key")); err != nil {
		t.Fatal(err)
	}
	manager.factory = &fakeCodexAccountFactory{open: func(ports.CodexAccountContext) (ports.CodexAccountClient, error) {
		return &fakeCodexAccountClient{logoutFn: func(context.Context) error {
			if err := writeGlobalCredentialAtomic(manager.globalCredentialPath(), testAPIKeyCredential("external-api-key")); err != nil {
				t.Fatal(err)
			}
			return nil
		}}, nil
	}}

	if err := manager.logout(context.Background(), record.Snapshot.ID); err == nil {
		t.Fatal("logout accepted an externally replaced API-key credential")
	}
	global, err := readOpaqueCredential(manager.globalCredentialPath())
	if err != nil || !bytes.Equal(global, testAPIKeyCredential("external-api-key")) {
		t.Fatalf("external global credential = %q, %v", global, err)
	}
	saved, err := readOpaqueCredential(filepath.Join(record.Home, codexCredentialFilename))
	if err != nil || !bytes.Equal(saved, testAPIKeyCredential("saved-api-key")) {
		t.Fatalf("saved credential changed = %q, %v", saved, err)
	}
	if manager.activeAccountID() != record.Snapshot.ID {
		t.Fatalf("active account changed = %q", manager.activeAccountID())
	}
}

func TestLoginCloseFailureRetainsPendingOperation(t *testing.T) {
	manager := newTestCodexAccountManager(t, nil, nil)
	manager.newID = func() string { return "b60a377d-da68-4a61-86f2-f31f04c571f2" }
	manager.executable = func() (string, error) { return "/ao", nil }
	terminal := &fakeCodexLoginTerminal{result: shellterm.ShellTerminal{HandleID: "shellterm-login-1"}, closeErr: errors.New("pty busy")}
	manager.terminal = terminal
	started, err := manager.openLoginTerminal(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.cancelLogin(context.Background(), started.Operation.OperationID); err == nil {
		t.Fatal("cancel unexpectedly succeeded")
	}
	manager.mu.Lock()
	operation := manager.login.snapshot
	manager.mu.Unlock()
	if operation.Status != domain.CodexAccountLoginRetryable || operation.ReasonCode != domain.CodexAccountLoginReasonFailed {
		t.Fatalf("operation after close failure = %#v", operation)
	}
}

func TestBootstrapImportsUnknownDeviceCredentialOnlyOnce(t *testing.T) {
	root := t.TempDir()
	device := filepath.Join(root, "device")
	if err := ensurePrivateDirectory(device); err != nil {
		t.Fatal(err)
	}
	deviceCredential := filepath.Join(device, codexCredentialFilename)
	original := testOAuthCredential("device-account", "access-one")
	if err := writePrivateFileAtomic(deviceCredential, original); err != nil {
		t.Fatal(err)
	}
	manager := newCodexAccountManager(context.Background(), filepath.Join(root, "accounts"), filepath.Join(root, "pending"), filepath.Join(root, "staging"), device, nil, nil)
	manager.catalog.newID = func() string { return testAccountID }
	if err := manager.waitAccountStore(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := manager.reconcileGlobal(context.Background()); err != nil {
		t.Fatal(err)
	}
	view := manager.cached()
	if view.ActiveAccountID != testAccountID || len(view.Accounts) != 1 || !view.Accounts[0].Active {
		t.Fatalf("imported account = %#v", view)
	}
	if err := manager.reconcileGlobal(context.Background()); err != nil {
		t.Fatal(err)
	}
	if snapshots := manager.catalog.snapshots(); len(snapshots) != 1 || snapshots[0].ID != testAccountID {
		t.Fatalf("repeated reconciliation duplicated import = %#v", snapshots)
	}
	after, err := os.ReadFile(deviceCredential)
	if err != nil || !slices.Equal(after, original) {
		t.Fatalf("device credential changed: %q err=%v", after, err)
	}
	if _, err := os.Stat(filepath.Join(root, "runtime")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("bootstrap created an obsolete private runtime: %v", err)
	}
}

func TestGlobalReconciliationKeepsMatchingDeviceAccountActiveWithoutProactiveRefresh(t *testing.T) {
	root := t.TempDir()
	globalHome := filepath.Join(root, "global-codex")
	if err := ensurePrivateDirectory(globalHome); err != nil {
		t.Fatal(err)
	}
	globalCredential := filepath.Join(globalHome, codexCredentialFilename)
	credential := []byte("opaque-codex-credential\x00\xff")
	if err := writePrivateFileAtomic(globalCredential, credential); err != nil {
		t.Fatal(err)
	}
	email := "device@example.com"
	observation := ports.CodexAccountObservation{
		Authentication: domain.AgentAuthenticationAuthorized,
		Method:         domain.CodexAuthMethodChatGPT,
		Email:          &email,
	}
	manager := newCodexAccountManager(
		context.Background(),
		filepath.Join(root, "accounts"),
		filepath.Join(root, "pending"),
		filepath.Join(root, "staging"),
		globalHome,
		nil,
		nil,
	)
	manager.catalog.newID = func() string { return testAccountID }
	record := commitTestAccount(t, manager.catalog, manager.pendingRoot, "b60a377d-da68-4a61-86f2-f31f04c571f2", observation)
	if err := writePrivateFileAtomic(filepath.Join(record.Home, codexCredentialFilename), credential); err != nil {
		t.Fatal(err)
	}
	var refreshRequests []bool
	manager.factory = &fakeCodexAccountFactory{
		capabilities: supportedCodexAccountCapabilities(),
		open: func(ports.CodexAccountContext) (ports.CodexAccountClient, error) {
			return &fakeCodexAccountClient{readFn: func(_ context.Context, refresh bool) (ports.CodexAccountObservation, error) {
				refreshRequests = append(refreshRequests, refresh)
				if refresh {
					return ports.CodexAccountObservation{}, errors.New("proactive refresh rejected for copied credential")
				}
				return observation, nil
			}}, nil
		},
	}

	if err := manager.initializeAccountStore(); err != nil {
		t.Fatal(err)
	}
	if err := manager.reconcileGlobal(context.Background()); err != nil {
		t.Fatal(err)
	}
	view := manager.cached()
	if view.ActiveAccountID != testAccountID || len(view.Accounts) != 1 || !view.Accounts[0].Active {
		t.Fatalf("reconciled device account = %#v, refresh requests = %#v", view, refreshRequests)
	}
	if slices.Contains(refreshRequests, true) {
		t.Fatalf("reconciliation requested proactive refresh: %#v", refreshRequests)
	}
}

func TestGlobalReconciliationMatchesRotatedOAuthByAccountID(t *testing.T) {
	root := t.TempDir()
	globalHome := filepath.Join(root, "global-codex")
	if err := ensurePrivateDirectory(globalHome); err != nil {
		t.Fatal(err)
	}
	globalCredential := testOAuthCredential("provider-account-a", "rotated-access")
	if err := writePrivateFileAtomic(filepath.Join(globalHome, codexCredentialFilename), globalCredential); err != nil {
		t.Fatal(err)
	}
	email := "known@example.com"
	state := &testCodexDeviceSeed{
		active: testCodexDeviceAccount{AccountID: testAccountID, Revision: 7},
	}
	manager := newCodexAccountManager(context.Background(), filepath.Join(root, "accounts"), filepath.Join(root, "pending"), filepath.Join(root, "staging"), globalHome, nil, nil)
	manager.catalog.newID = func() string { return testAccountID }
	record := commitTestAccountWithCredential(t, manager.catalog, manager.pendingRoot, "b60a377d-da68-4a61-86f2-f31f04c571f2", testOAuthCredential("provider-account-a", "old-access"), ports.CodexAccountObservation{
		Authentication: domain.AgentAuthenticationAuthorized,
		Method:         domain.CodexAuthMethodChatGPT,
		Email:          &email,
	})
	setTestDeviceAccount(manager, state.active)
	manager.catalog.updateSnapshot(record.Snapshot.ID, func(snapshot *domain.CodexAccountSnapshot) {
		snapshot.Authentication = accountAuthenticationObservation(time.Now().UTC(), domain.AgentAuthenticationAuthorized)
	})
	manager.requireReauthentication(record.Snapshot.ID)
	manager.factory = &fakeCodexAccountFactory{open: func(ports.CodexAccountContext) (ports.CodexAccountClient, error) {
		t.Fatal("local reconciliation opened Codex")
		return nil, nil
	}}

	if err := manager.reconcileGlobal(context.Background()); err != nil {
		t.Fatal(err)
	}
	view := manager.cached()
	if view.ActiveAccountID != testAccountID || len(view.Accounts) != 1 || !view.Accounts[0].Active {
		t.Fatalf("rotated OAuth account was not matched = %#v", view)
	}
	if view.Accounts[0].Authentication.State != domain.AgentAuthenticationUnknown || view.Accounts[0].Authentication.ReasonCode != domain.AgentReadinessReasonNotChecked {
		t.Fatalf("old expired-token result survived credential rotation = %#v", view.Accounts[0].Authentication)
	}
	if _, reauthenticationRequired := manager.authenticationVerification(record.Snapshot.ID); reauthenticationRequired {
		t.Fatal("rotated credential remained blocked by the previous login-expired result")
	}
	saved, err := readOpaqueCredential(filepath.Join(record.Home, codexCredentialFilename))
	if err != nil || !slices.Equal(saved, globalCredential) {
		t.Fatalf("rotated credential was not checkpointed: %v", err)
	}
}

func TestGlobalReconciliationExactCredentialMatchRemainsActiveOffline(t *testing.T) {
	root := t.TempDir()
	globalHome := filepath.Join(root, "global-codex")
	if err := ensurePrivateDirectory(globalHome); err != nil {
		t.Fatal(err)
	}
	state := &testCodexDeviceSeed{active: testCodexDeviceAccount{AccountID: testAccountID, Revision: 7}}
	manager := newCodexAccountManager(context.Background(), filepath.Join(root, "accounts"), filepath.Join(root, "pending"), filepath.Join(root, "staging"), globalHome, nil, nil)
	manager.catalog.newID = func() string { return testAccountID }
	email := "known@example.com"
	record := commitTestAccount(t, manager.catalog, manager.pendingRoot, "b60a377d-da68-4a61-86f2-f31f04c571f2", ports.CodexAccountObservation{
		Authentication: domain.AgentAuthenticationAuthorized, Method: domain.CodexAuthMethodChatGPT, Email: &email,
	})
	saved, err := readOpaqueCredential(filepath.Join(record.Home, codexCredentialFilename))
	if err != nil {
		t.Fatal(err)
	}
	if err := writeGlobalCredentialAtomic(manager.globalCredentialPath(), saved); err != nil {
		t.Fatal(err)
	}
	setTestDeviceAccount(manager, state.active)
	manager.factory = &fakeCodexAccountFactory{open: func(account ports.CodexAccountContext) (ports.CodexAccountClient, error) {
		if account.Managed || account.Home != globalHome {
			t.Fatalf("matched device verification context = %#v", account)
		}
		return nil, errors.New("offline")
	}}

	if err := manager.reconcileGlobal(context.Background()); err != nil {
		t.Fatal(err)
	}
	view := manager.cached()
	if view.ActiveAccountID != testAccountID || len(view.Accounts) != 1 || !view.Accounts[0].Active {
		t.Fatalf("byte-matched account lost device ownership while offline: %#v", view)
	}
	if view.Accounts[0].AccountEmail == nil || *view.Accounts[0].AccountEmail != email {
		t.Fatalf("cached identity changed during failed verification: %#v", view.Accounts[0])
	}
}

func TestExternalDeviceSwitchImportsBWithoutWritingIntoA(t *testing.T) {
	root := t.TempDir()
	globalHome := filepath.Join(root, "global-codex")
	if err := ensurePrivateDirectory(globalHome); err != nil {
		t.Fatal(err)
	}
	credentialB := testOAuthCredential("provider-b", "access-b")
	if err := writeGlobalCredentialAtomic(filepath.Join(globalHome, codexCredentialFilename), credentialB); err != nil {
		t.Fatal(err)
	}
	state := &testCodexDeviceSeed{active: testCodexDeviceAccount{AccountID: testAccountID, Revision: 3}}
	manager := newCodexAccountManager(context.Background(), filepath.Join(root, "accounts"), filepath.Join(root, "pending"), filepath.Join(root, "staging"), globalHome, nil, nil)
	accountBID := "bb1e9a5d-37ad-43f8-83bd-13de8168f8af"
	ids := []string{testAccountID, accountBID}
	manager.catalog.newID = func() string { id := ids[0]; ids = ids[1:]; return id }
	aEmail := "account-a@example.com"
	credentialA := testOAuthCredential("provider-a", "access-a")
	recordA := commitTestAccountWithCredential(t, manager.catalog, manager.pendingRoot, "b60a377d-da68-4a61-86f2-f31f04c571f2", credentialA, ports.CodexAccountObservation{
		Authentication: domain.AgentAuthenticationAuthorized, Method: domain.CodexAuthMethodChatGPT, Email: &aEmail,
	})
	setTestDeviceAccount(manager, state.active)
	manager.factory = &fakeCodexAccountFactory{open: func(ports.CodexAccountContext) (ports.CodexAccountClient, error) {
		t.Fatal("local reconciliation opened Codex")
		return nil, nil
	}}

	if err := manager.reconcileGlobal(context.Background()); err != nil {
		t.Fatal(err)
	}
	view := manager.cached()
	if view.ActiveAccountID != accountBID || len(view.Accounts) != 2 {
		t.Fatalf("external B contaminated saved A: %#v", view)
	}
	savedA, err := readOpaqueCredential(filepath.Join(recordA.Home, codexCredentialFilename))
	if err != nil || !slices.Equal(savedA, credentialA) {
		t.Fatalf("external B overwrote A: %v", err)
	}
	activeB, ok := manager.catalog.record(accountBID)
	if !ok || activeB.Snapshot.AccountEmail != nil {
		t.Fatalf("local import should await authentication warming: %#v", activeB)
	}
}

func TestGlobalReconciliationMissingGlobalClearsActiveProjectionWithoutRestoringSavedAccount(t *testing.T) {
	root := t.TempDir()
	globalHome := filepath.Join(root, "global-codex")
	if err := ensurePrivateDirectory(globalHome); err != nil {
		t.Fatal(err)
	}
	state := &testCodexDeviceSeed{
		active: testCodexDeviceAccount{AccountID: testAccountID, Revision: 4},
	}
	manager := newCodexAccountManager(context.Background(), filepath.Join(root, "accounts"), filepath.Join(root, "pending"), filepath.Join(root, "staging"), globalHome, nil, nil)
	manager.catalog.newID = func() string { return testAccountID }
	record := commitTestAccount(t, manager.catalog, manager.pendingRoot, "b60a377d-da68-4a61-86f2-f31f04c571f2", ports.CodexAccountObservation{
		Authentication: domain.AgentAuthenticationAuthorized,
		Method:         domain.CodexAuthMethodChatGPT,
	})
	savedCredential, err := readOpaqueCredential(filepath.Join(record.Home, codexCredentialFilename))
	if err != nil {
		t.Fatal(err)
	}
	setTestDeviceAccount(manager, state.active)
	manager.factory = &fakeCodexAccountFactory{open: func(ports.CodexAccountContext) (ports.CodexAccountClient, error) {
		t.Fatal("missing-global reconciliation opened Codex")
		return nil, nil
	}}

	if err := manager.reconcileGlobal(context.Background()); err != nil {
		t.Fatal(err)
	}
	view := manager.cached()
	if manager.activeAccountID() != "" || view.ActiveAccountID != "" || len(view.Accounts) != 1 || view.Accounts[0].Active {
		t.Fatalf("saved account remained active without a device credential: %#v", view)
	}
	storedCredential, err := readOpaqueCredential(filepath.Join(record.Home, codexCredentialFilename))
	if err != nil || !slices.Equal(storedCredential, savedCredential) {
		t.Fatalf("saved account credential changed: %v", err)
	}
	if _, err := os.Stat(manager.globalCredentialPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("saved account was silently restored globally: %v", err)
	}
}

func TestEnsureCodexAccountsReconcilesRecentlyRemovedGlobalCredentialBeforeAccountChecks(t *testing.T) {
	root := t.TempDir()
	globalHome := filepath.Join(root, "global-codex")
	if err := ensurePrivateDirectory(globalHome); err != nil {
		t.Fatal(err)
	}
	credential := testOAuthCredential("provider-account", "access-token")
	state := &testCodexDeviceSeed{
		active: testCodexDeviceAccount{AccountID: testAccountID, Revision: 1},
	}
	var opened []ports.CodexAccountContext
	email := "saved@example.com"
	factory := &fakeCodexAccountFactory{
		capabilities: supportedCodexAccountCapabilities(),
		open: func(account ports.CodexAccountContext) (ports.CodexAccountClient, error) {
			opened = append(opened, account)
			return &fakeCodexAccountClient{
				read: ports.CodexAccountObservation{
					Authentication: domain.AgentAuthenticationAuthorized,
					Method:         domain.CodexAuthMethodChatGPT,
					Email:          &email,
				},
				capacity: ports.CodexCapacityObservation{},
			}, nil
		},
	}
	manager := newCodexAccountManager(context.Background(), filepath.Join(root, "accounts"), filepath.Join(root, "pending"), filepath.Join(root, "staging"), globalHome, factory, nil)
	manager.catalog.newID = func() string { return testAccountID }
	record := commitTestAccountWithCredential(t, manager.catalog, manager.pendingRoot, "b60a377d-da68-4a61-86f2-f31f04c571f2", credential, ports.CodexAccountObservation{
		Authentication: domain.AgentAuthenticationAuthorized,
		Method:         domain.CodexAuthMethodChatGPT,
		Email:          &email,
	})
	if err := writeGlobalCredentialAtomic(manager.globalCredentialPath(), credential); err != nil {
		t.Fatal(err)
	}
	setTestDeviceAccount(manager, state.active)
	manager.accountStoreReady = true
	now := time.Now().UTC()
	manager.reconciliation = domain.CodexDeviceReconciliation{
		Status:                domain.CodexDeviceReconciliationVerified,
		ActiveAccountVerified: true,
		ReasonCode:            "verified",
		AttemptedAt:           &now,
		VerifiedAt:            &now,
	}
	manager.deviceAccountID = record.Snapshot.ID
	manager.deviceCredentialPresent = true

	// Reproduce an external `codex logout` while AO still holds a fresh device
	// association. The old five-minute cache skipped reconciliation here.
	if err := os.Remove(manager.globalCredentialPath()); err != nil {
		t.Fatal(err)
	}
	readiness := newReadinessCoordinator(readinessCoordinatorConfig{
		Agents: []agentregistry.HarnessAgent{harnessAgent(string(domain.HarnessCodex), "Codex", nil)},
	})
	service := &Service{codexAccounts: manager, readiness: readiness}

	view, err := service.EnsureCodexAccounts(context.Background(), nil, CodexAccountEnsureOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if view.ActiveAccountID != "" || len(view.Accounts) != 1 || view.Accounts[0].Active {
		t.Fatalf("removed global credential remained active: %#v", view)
	}
	if view.Accounts[0].Authentication.State != domain.AgentAuthenticationAuthorized {
		t.Fatalf("saved credential was not checked independently: %#v", view.Accounts[0].Authentication)
	}
	if manager.activeAccountID() != "" {
		t.Fatalf("active account was not cleared: %q", manager.activeAccountID())
	}
	if len(opened) == 0 {
		t.Fatal("saved account was not checked")
	}
	for _, account := range opened {
		if !account.Managed || canonicalPath(account.Home) != canonicalPath(record.Home) {
			t.Fatalf("account check used stale global home: %#v", account)
		}
	}
}

func TestEnsureCodexAccountsTargetedRefreshDoesNotReconcileDeviceCredential(t *testing.T) {
	root := t.TempDir()
	globalHome := filepath.Join(root, "global-codex")
	if err := ensurePrivateDirectory(globalHome); err != nil {
		t.Fatal(err)
	}
	credential := testOAuthCredential("provider-account", "access-token")
	var opened []ports.CodexAccountContext
	factory := &fakeCodexAccountFactory{
		capabilities: supportedCodexAccountCapabilities(),
		open: func(account ports.CodexAccountContext) (ports.CodexAccountClient, error) {
			opened = append(opened, account)
			return &fakeCodexAccountClient{
				read:     ports.CodexAccountObservation{Authentication: domain.AgentAuthenticationAuthorized, Method: domain.CodexAuthMethodChatGPT},
				capacity: ports.CodexCapacityObservation{},
			}, nil
		},
	}
	manager := newCodexAccountManager(context.Background(), filepath.Join(root, "accounts"), filepath.Join(root, "pending"), filepath.Join(root, "staging"), globalHome, factory, nil)
	manager.catalog.newID = func() string { return testAccountID }
	record := commitTestAccountWithCredential(t, manager.catalog, manager.pendingRoot, "b60a377d-da68-4a61-86f2-f31f04c571f2", credential, ports.CodexAccountObservation{
		Authentication: domain.AgentAuthenticationAuthorized,
		Method:         domain.CodexAuthMethodChatGPT,
	})
	if err := writeGlobalCredentialAtomic(manager.globalCredentialPath(), credential); err != nil {
		t.Fatal(err)
	}
	manager.accountStoreReady = true
	attemptedAt := time.Now().UTC().Add(-time.Minute)
	manager.reconciliation = domain.CodexDeviceReconciliation{
		Status:                domain.CodexDeviceReconciliationVerified,
		ActiveAccountVerified: true,
		ReasonCode:            "verified",
		AttemptedAt:           &attemptedAt,
		VerifiedAt:            &attemptedAt,
	}
	manager.deviceAccountID = record.Snapshot.ID
	manager.deviceCredentialPresent = true
	readiness := newReadinessCoordinator(readinessCoordinatorConfig{
		Agents: []agentregistry.HarnessAgent{harnessAgent(string(domain.HarnessCodex), "Codex", nil)},
	})
	service := &Service{codexAccounts: manager, readiness: readiness}

	view, err := service.EnsureCodexAccounts(context.Background(), []string{record.Snapshot.ID}, CodexAccountEnsureOptions{IncludeUsage: true})
	if err != nil {
		t.Fatal(err)
	}
	if view.DeviceReconciliation.AttemptedAt == nil || !view.DeviceReconciliation.AttemptedAt.Equal(attemptedAt) {
		t.Fatalf("targeted metadata refresh reconciled device credential: before=%v after=%v", attemptedAt, view.DeviceReconciliation.AttemptedAt)
	}
	if view.ActiveAccountID != record.Snapshot.ID || len(view.Accounts) != 1 || !view.Accounts[0].Active {
		t.Fatalf("targeted metadata refresh changed active presentation: %#v", view)
	}
	if len(opened) == 0 {
		t.Fatal("targeted metadata refresh did not check the saved account")
	}
	for _, account := range opened {
		if !account.Managed || canonicalPath(account.Home) != canonicalPath(record.Home) {
			t.Fatalf("targeted metadata refresh used device-global home: %#v", account)
		}
	}
}

func TestEnsureCodexAccountsRetriesSavedAccountWhenGlobalCredentialDisappearsDuringCheck(t *testing.T) {
	root := t.TempDir()
	globalHome := filepath.Join(root, "global-codex")
	if err := ensurePrivateDirectory(globalHome); err != nil {
		t.Fatal(err)
	}
	credential := testOAuthCredential("provider-account", "access-token")
	state := &testCodexDeviceSeed{
		active: testCodexDeviceAccount{AccountID: testAccountID, Revision: 1},
	}
	email := "saved@example.com"
	removedGlobal := false
	var opened []ports.CodexAccountContext
	manager := newCodexAccountManager(context.Background(), filepath.Join(root, "accounts"), filepath.Join(root, "pending"), filepath.Join(root, "staging"), globalHome, nil, nil)
	manager.catalog.newID = func() string { return testAccountID }
	record := commitTestAccountWithCredential(t, manager.catalog, manager.pendingRoot, "b60a377d-da68-4a61-86f2-f31f04c571f2", credential, ports.CodexAccountObservation{
		Authentication: domain.AgentAuthenticationAuthorized,
		Method:         domain.CodexAuthMethodChatGPT,
		Email:          &email,
	})
	if err := writeGlobalCredentialAtomic(manager.globalCredentialPath(), credential); err != nil {
		t.Fatal(err)
	}
	manager.factory = &fakeCodexAccountFactory{
		capabilities: supportedCodexAccountCapabilities(),
		open: func(account ports.CodexAccountContext) (ports.CodexAccountClient, error) {
			opened = append(opened, account)
			if !account.Managed && !removedGlobal {
				removedGlobal = true
				if err := os.Remove(manager.globalCredentialPath()); err != nil {
					t.Fatal(err)
				}
				return nil, errors.New("device credential disappeared")
			}
			return &fakeCodexAccountClient{
				read: ports.CodexAccountObservation{
					Authentication: domain.AgentAuthenticationAuthorized,
					Method:         domain.CodexAuthMethodChatGPT,
					Email:          &email,
				},
				capacity: ports.CodexCapacityObservation{},
			}, nil
		},
	}
	setTestDeviceAccount(manager, state.active)
	manager.accountStoreReady = true
	readiness := newReadinessCoordinator(readinessCoordinatorConfig{
		Agents: []agentregistry.HarnessAgent{harnessAgent(string(domain.HarnessCodex), "Codex", nil)},
	})
	service := &Service{codexAccounts: manager, readiness: readiness}

	view, err := service.EnsureCodexAccounts(context.Background(), nil, CodexAccountEnsureOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !removedGlobal {
		t.Fatal("test did not remove the global credential during the account check")
	}
	if view.ActiveAccountID != "" || view.Accounts[0].Active {
		t.Fatalf("removed global credential remained active: %#v", view)
	}
	if view.Accounts[0].Authentication.State != domain.AgentAuthenticationAuthorized || view.Accounts[0].Authentication.Freshness != domain.AgentReadinessFresh {
		t.Fatalf("global disappearance became a false authentication failure: %#v", view.Accounts[0].Authentication)
	}
	if len(opened) < 2 || opened[0].Managed || !opened[1].Managed || canonicalPath(opened[1].Home) != canonicalPath(record.Home) {
		t.Fatalf("account checks did not move from global to saved home: %#v", opened)
	}
}

func TestGlobalAccountMatchingUsesUniqueOpaqueCredentialIdentity(t *testing.T) {
	manager := newTestCodexAccountManager(t, nil, nil)
	accountIDs := []string{testAccountID, "bb1e9a5d-37ad-43f8-83bd-13de8168f8af"}
	manager.catalog.newID = func() string {
		id := accountIDs[0]
		accountIDs = accountIDs[1:]
		return id
	}
	first := commitTestAccount(t, manager.catalog, manager.pendingRoot, "b60a377d-da68-4a61-86f2-f31f04c571f2", ports.CodexAccountObservation{Authentication: domain.AgentAuthenticationAuthorized, Method: domain.CodexAuthMethodAPIKey})
	second := commitTestAccount(t, manager.catalog, manager.pendingRoot, "1c5de3ab-82d0-4a68-a06b-8495cdeab909", ports.CodexAccountObservation{Authentication: domain.AgentAuthenticationAuthorized, Method: domain.CodexAuthMethodAPIKey})
	if err := writePrivateFileAtomic(filepath.Join(first.Home, codexCredentialFilename), []byte("credential-a")); err != nil {
		t.Fatal(err)
	}
	if err := writePrivateFileAtomic(filepath.Join(second.Home, codexCredentialFilename), []byte("credential-b")); err != nil {
		t.Fatal(err)
	}

	matched, match := manager.matchGlobalCredentialForReconciliation([]byte("credential-b"), codexCredentialIdentity{}, errors.New("opaque credential"))
	if match != codexCredentialMatchManaged || matched.Snapshot.ID != second.Snapshot.ID {
		t.Fatalf("unique opaque credential match = (%q, %v), want second account", matched.Snapshot.ID, match)
	}
	if err := writePrivateFileAtomic(filepath.Join(first.Home, codexCredentialFilename), []byte("credential-b")); err != nil {
		t.Fatal(err)
	}
	if matched, match := manager.matchGlobalCredentialForReconciliation([]byte("credential-b"), codexCredentialIdentity{}, errors.New("opaque credential")); match != codexCredentialMatchAmbiguous {
		t.Fatalf("ambiguous opaque credential matched account %q", matched.Snapshot.ID)
	}
}

func TestGlobalReconciliationRefusesAmbiguousProviderAccountID(t *testing.T) {
	root := t.TempDir()
	globalHome := filepath.Join(root, "global-codex")
	if err := ensurePrivateDirectory(globalHome); err != nil {
		t.Fatal(err)
	}
	globalCredential := testOAuthCredential("shared-provider-account", "global-access")
	if err := writeGlobalCredentialAtomic(filepath.Join(globalHome, codexCredentialFilename), globalCredential); err != nil {
		t.Fatal(err)
	}
	manager := newCodexAccountManager(context.Background(), filepath.Join(root, "accounts"), filepath.Join(root, "pending"), filepath.Join(root, "staging"), globalHome, nil, nil, nil)
	ids := []string{testAccountID, "bb1e9a5d-37ad-43f8-83bd-13de8168f8af"}
	manager.catalog.newID = func() string { id := ids[0]; ids = ids[1:]; return id }
	first := commitTestAccountWithCredential(t, manager.catalog, manager.pendingRoot, "b60a377d-da68-4a61-86f2-f31f04c571f2", testOAuthCredential("shared-provider-account", "first-access"), ports.CodexAccountObservation{Method: domain.CodexAuthMethodChatGPT})
	second := commitTestAccountWithCredential(t, manager.catalog, manager.pendingRoot, "1c5de3ab-82d0-4a68-a06b-8495cdeab909", testOAuthCredential("shared-provider-account", "second-access"), ports.CodexAccountObservation{Method: domain.CodexAuthMethodChatGPT})

	err := manager.reconcileGlobal(context.Background())
	var failure *codexAccountLocalFailure
	if !errors.As(err, &failure) || failure.reason != "global_account_ambiguous" || failure.retryable {
		t.Fatalf("ambiguous reconciliation = %#v", err)
	}
	for _, record := range []codexAccountRecord{first, second} {
		saved, readErr := readOpaqueCredential(filepath.Join(record.Home, codexCredentialFilename))
		if readErr != nil || slices.Equal(saved, globalCredential) {
			t.Fatalf("ambiguous credential overwrote %s: %v", record.Snapshot.ID, readErr)
		}
	}
}

func TestGlobalReconciliationReusesImportedAccountAcrossExternalTokenRotation(t *testing.T) {
	root := t.TempDir()
	globalHome := filepath.Join(root, "global-codex")
	if err := ensurePrivateDirectory(globalHome); err != nil {
		t.Fatal(err)
	}
	globalCredential := filepath.Join(globalHome, codexCredentialFilename)
	credentialA := testOAuthCredential("provider-account", "access-a")
	if err := writePrivateFileAtomic(globalCredential, credentialA); err != nil {
		t.Fatal(err)
	}
	manager := newCodexAccountManager(context.Background(), filepath.Join(root, "accounts"), filepath.Join(root, "pending"), filepath.Join(root, "staging"), globalHome, nil, nil)
	manager.catalog.newID = func() string { return testAccountID }

	if err := manager.initializeAccountStore(); err != nil {
		t.Fatal(err)
	}
	if err := manager.reconcileGlobal(context.Background()); err != nil {
		t.Fatal(err)
	}
	first := manager.cached()
	if first.ActiveAccountID != testAccountID || len(first.Accounts) != 1 || !first.Accounts[0].Active {
		t.Fatalf("first imported account = %#v", first)
	}
	credentialB := testOAuthCredential("provider-account", "access-b")
	if err := writeGlobalCredentialAtomic(globalCredential, credentialB); err != nil {
		t.Fatal(err)
	}
	if err := manager.reconcileGlobal(context.Background()); err != nil {
		t.Fatal(err)
	}
	second := manager.cached()
	if second.ActiveAccountID != testAccountID || len(second.Accounts) != 1 || !second.Accounts[0].Active {
		t.Fatalf("rotated account was not reused: %#v", second)
	}
	record, ok := manager.catalog.record(testAccountID)
	if !ok {
		t.Fatal("imported account disappeared")
	}
	saved, err := readOpaqueCredential(filepath.Join(record.Home, codexCredentialFilename))
	if err != nil || !slices.Equal(saved, credentialB) {
		t.Fatalf("rotated credential was not saved: %v", err)
	}
}

func TestGlobalReconciliationMatchesAPIKeyDirectlyWhenFileBytesChange(t *testing.T) {
	root := t.TempDir()
	globalHome := filepath.Join(root, "global-codex")
	if err := ensurePrivateDirectory(globalHome); err != nil {
		t.Fatal(err)
	}
	globalCredential := filepath.Join(globalHome, codexCredentialFilename)
	credentialA := []byte(`{"OPENAI_API_KEY":"same-api-key"}`)
	if err := writePrivateFileAtomic(globalCredential, credentialA); err != nil {
		t.Fatal(err)
	}
	manager := newCodexAccountManager(context.Background(), filepath.Join(root, "accounts"), filepath.Join(root, "pending"), filepath.Join(root, "staging"), globalHome, nil, nil)
	manager.catalog.newID = func() string { return testAccountID }
	if err := manager.initializeAccountStore(); err != nil {
		t.Fatal(err)
	}
	if err := manager.reconcileGlobal(context.Background()); err != nil {
		t.Fatal(err)
	}

	credentialB := []byte("{\n  \"OPENAI_API_KEY\": \"same-api-key\",\n  \"updated\": true\n}\n")
	if err := writeGlobalCredentialAtomic(globalCredential, credentialB); err != nil {
		t.Fatal(err)
	}
	if err := manager.reconcileGlobal(context.Background()); err != nil {
		t.Fatal(err)
	}
	view := manager.cached()
	if view.ActiveAccountID != testAccountID || len(view.Accounts) != 1 || !view.Accounts[0].Active {
		t.Fatalf("same API key created a second account: %#v", view)
	}
	record, ok := manager.catalog.record(testAccountID)
	if !ok {
		t.Fatal("imported API-key account disappeared")
	}
	saved, err := readOpaqueCredential(filepath.Join(record.Home, codexCredentialFilename))
	if err != nil || !slices.Equal(saved, credentialB) {
		t.Fatalf("latest API-key credential file was not checkpointed: %v", err)
	}
}

func TestGlobalReconciliationReactivatesSignedOutIdentityFromDeviceCredential(t *testing.T) {
	root := t.TempDir()
	globalHome := filepath.Join(root, "global-codex")
	if err := ensurePrivateDirectory(globalHome); err != nil {
		t.Fatal(err)
	}
	credential := testOAuthCredential("returning-account", "restored-access")
	if err := writePrivateFileAtomic(filepath.Join(globalHome, codexCredentialFilename), credential); err != nil {
		t.Fatal(err)
	}
	email := "returning@example.com"
	observation := ports.CodexAccountObservation{Authentication: domain.AgentAuthenticationAuthorized, Method: domain.CodexAuthMethodChatGPT, Email: &email}
	factory := &fakeCodexAccountFactory{
		capabilities: supportedCodexAccountCapabilities(),
		open: func(ports.CodexAccountContext) (ports.CodexAccountClient, error) {
			return &fakeCodexAccountClient{read: observation}, nil
		},
	}
	manager := newCodexAccountManager(context.Background(), filepath.Join(root, "accounts"), filepath.Join(root, "pending"), filepath.Join(root, "staging"), globalHome, factory, nil)
	manager.catalog.newID = func() string { return testAccountID }
	record := commitTestAccountWithCredential(t, manager.catalog, manager.pendingRoot, "b60a377d-da68-4a61-86f2-f31f04c571f2", testOAuthCredential("returning-account", "old-access"), observation)
	if _, err := manager.catalog.markSignedOut(record.Snapshot.ID); err != nil {
		t.Fatal(err)
	}

	if err := manager.initializeAccountStore(); err != nil {
		t.Fatal(err)
	}
	if err := manager.reconcileGlobal(context.Background()); err != nil {
		t.Fatal(err)
	}
	view := manager.cached()
	if view.ActiveAccountID != testAccountID || len(view.Accounts) != 1 || view.Accounts[0].Status != domain.CodexAccountStatusValid || !view.Accounts[0].Active {
		t.Fatalf("device credential did not reactivate its saved account = %#v", view)
	}
	saved, err := readOpaqueCredential(filepath.Join(record.Home, codexCredentialFilename))
	if err != nil || !slices.Equal(saved, credential) {
		t.Fatalf("restored credential was not copied to its account: %v", err)
	}
}

func TestImportedGlobalCredentialDoesNotBlockNormalAuthentication(t *testing.T) {
	root := t.TempDir()
	globalHome := filepath.Join(root, "global-codex")
	if err := ensurePrivateDirectory(globalHome); err != nil {
		t.Fatal(err)
	}
	if err := writeGlobalCredentialAtomic(filepath.Join(globalHome, codexCredentialFilename), testOAuthCredential("imported-provider-account", "access")); err != nil {
		t.Fatal(err)
	}
	email := "keyring@example.com"
	factory := &fakeCodexAccountFactory{
		capabilities: supportedCodexAccountCapabilities(),
		open: func(ports.CodexAccountContext) (ports.CodexAccountClient, error) {
			return &fakeCodexAccountClient{read: ports.CodexAccountObservation{Authentication: domain.AgentAuthenticationAuthorized, Method: domain.CodexAuthMethodChatGPT, Email: &email}}, nil
		},
	}
	manager := newCodexAccountManager(context.Background(), filepath.Join(root, "accounts"), filepath.Join(root, "pending"), filepath.Join(root, "staging"), globalHome, factory, nil, nil)
	manager.catalog.newID = func() string { return testAccountID }
	if err := manager.initializeAccountStore(); err != nil {
		t.Fatal(err)
	}
	if err := manager.reconcileGlobal(context.Background()); err != nil {
		t.Fatal(err)
	}
	view := manager.cached()
	if view.ActiveAccountID != testAccountID || len(view.Accounts) != 1 || !view.Accounts[0].Active {
		t.Fatalf("imported global account = %#v", view)
	}
	if got := manager.detectCapabilities(context.Background()).GlobalSwitch.State; got != domain.CodexCapabilitySupported {
		t.Fatalf("global switch capability = %q, want supported", got)
	}
	manager.mu.Lock()
	manager.accountStoreReady = true
	manager.mu.Unlock()
	service := &Service{codexAccounts: manager}
	auth, handled := service.structuredCodexAuthentication(context.Background(), string(domain.HarnessCodex), domain.AgentReadinessPurposeDisplay)
	if !handled || auth.State != domain.AgentAuthenticationAuthorized {
		t.Fatalf("normal Codex authentication = (%#v, %v), want authorized", auth, handled)
	}
	if err := manager.catalog.refresh(); err != nil {
		t.Fatal(err)
	}
	persisted, ok := manager.catalog.record(testAccountID)
	if !ok || persisted.Snapshot.AccountEmail == nil || *persisted.Snapshot.AccountEmail != email || persisted.Snapshot.Label != email {
		t.Fatalf("imported account metadata was not persisted: %#v", persisted.Snapshot)
	}
}

func TestGlobalSwitchCapabilityAllowsMissingCredentialButRejectsUnsafeCredential(t *testing.T) {
	root := t.TempDir()
	globalHome := filepath.Join(root, "global-codex")
	if err := ensurePrivateDirectory(globalHome); err != nil {
		t.Fatal(err)
	}
	factory := &fakeCodexAccountFactory{capabilities: supportedCodexAccountCapabilities()}
	manager := newCodexAccountManager(
		context.Background(),
		filepath.Join(root, "accounts"),
		filepath.Join(root, "pending"),
		filepath.Join(root, "staging"),
		globalHome,
		factory,
		nil,
		nil,
	)

	if got := manager.detectCapabilities(context.Background()).GlobalSwitch.State; got != domain.CodexCapabilitySupported {
		t.Fatalf("missing credential global switch capability = %q, want supported", got)
	}
	if err := os.Mkdir(manager.globalCredentialPath(), 0o700); err != nil {
		t.Fatal(err)
	}
	if got := manager.detectCapabilities(context.Background()).GlobalSwitch.State; got != domain.CodexCapabilityUnsupported {
		t.Fatalf("unsafe credential global switch capability = %q, want unsupported", got)
	}
}

func TestLocalReconciliationPreservesActiveSlotProjection(t *testing.T) {
	root := t.TempDir()
	globalHome := filepath.Join(root, "global-codex")
	if err := ensurePrivateDirectory(globalHome); err != nil {
		t.Fatal(err)
	}
	if err := writeGlobalCredentialAtomic(filepath.Join(globalHome, codexCredentialFilename), []byte("opaque-codex-credential\x00\xff")); err != nil {
		t.Fatal(err)
	}
	slotEmail := "saved@example.com"
	state := &testCodexDeviceSeed{active: testCodexDeviceAccount{AccountID: testAccountID, Revision: 3}}
	var opened []ports.CodexAccountContext
	manager := newCodexAccountManager(context.Background(), filepath.Join(root, "accounts"), filepath.Join(root, "pending"), filepath.Join(root, "staging"), globalHome, nil, nil)
	manager.catalog.newID = func() string { return testAccountID }
	record := commitTestAccount(t, manager.catalog, manager.pendingRoot, "b60a377d-da68-4a61-86f2-f31f04c571f2", ports.CodexAccountObservation{
		Authentication: domain.AgentAuthenticationAuthorized, Method: domain.CodexAuthMethodChatGPT, Email: &slotEmail,
	})
	setTestDeviceAccount(manager, state.active)
	manager.factory = &fakeCodexAccountFactory{capabilities: supportedCodexAccountCapabilities(), open: func(account ports.CodexAccountContext) (ports.CodexAccountClient, error) {
		opened = append(opened, account)
		if account.Managed {
			return &fakeCodexAccountClient{read: ports.CodexAccountObservation{Authentication: domain.AgentAuthenticationAuthorized, Method: domain.CodexAuthMethodChatGPT, Email: &slotEmail}}, nil
		}
		return &fakeCodexAccountClient{read: ports.CodexAccountObservation{Authentication: domain.AgentAuthenticationAuthorized, Method: domain.CodexAuthMethodChatGPT, Email: &slotEmail}}, nil
	}}
	checked := time.Now().UTC()
	manager.catalog.updateSnapshot(record.Snapshot.ID, func(snapshot *domain.CodexAccountSnapshot) {
		snapshot.Authentication = accountAuthenticationObservation(checked, domain.AgentAuthenticationAuthorized)
	})
	manager.auth[record.Snapshot.ID] = &accountAuthState{invalidated: true}
	tokens := int64(42)
	manager.usage[record.Snapshot.ID] = &accountUsageState{value: &domain.CodexAccountUsageSummary{LatestDayTokens: &tokens, ObservedAt: checked}, checkedAt: checked}
	manager.capacity.replace(record.Snapshot.ID, domain.CodexCapacitySnapshot{State: domain.CodexCapacityAvailable, Freshness: domain.AgentReadinessFresh, ReasonCode: domain.CodexCapacityReasonAvailable, Reason: "available", CheckedAt: &checked, AdditionalBuckets: []domain.CodexCapacityBucket{}}, "test")

	if err := manager.reconcileGlobal(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.ensureAuthentication(context.Background(), record, domain.AgentReadinessPurposeDisplay); err != nil {
		t.Fatal(err)
	}
	view := manager.cached()
	if len(opened) < 1 || opened[len(opened)-1].Managed || opened[len(opened)-1].Home != globalHome {
		t.Fatalf("active slot authentication contexts = %#v", opened)
	}
	if len(view.Accounts) != 1 || view.Accounts[0].AccountEmail == nil || *view.Accounts[0].AccountEmail != slotEmail || view.Accounts[0].UsageSummary == nil || view.Accounts[0].UsageSummary.LatestDayTokens == nil || *view.Accounts[0].UsageSummary.LatestDayTokens != tokens || view.Accounts[0].Capacity.State != domain.CodexCapacityAvailable {
		t.Fatalf("active slot projection changed under unmanaged global state = %#v", view)
	}
}

type apiKeySwitchFixture struct {
	manager *codexAccountManager
	service *Service
	state   *testCodexDeviceSeed
	source  codexAccountRecord
	target  codexAccountRecord
}

func newAPIKeySwitchFixture(t *testing.T) apiKeySwitchFixture {
	t.Helper()
	root := t.TempDir()
	globalHome := filepath.Join(root, "global-codex")
	if err := ensurePrivateDirectory(globalHome); err != nil {
		t.Fatal(err)
	}
	state := &testCodexDeviceSeed{active: testCodexDeviceAccount{AccountID: testAccountID, Revision: 1}, found: true}
	manager := newCodexAccountManager(context.Background(), filepath.Join(root, "accounts"), filepath.Join(root, "pending"), filepath.Join(root, "staging"), globalHome, nil, nil)
	ids := []string{testAccountID, "bb1e9a5d-37ad-43f8-83bd-13de8168f8af"}
	manager.catalog.newID = func() string { id := ids[0]; ids = ids[1:]; return id }
	observation := ports.CodexAccountObservation{Authentication: domain.AgentAuthenticationAuthorized, Method: domain.CodexAuthMethodAPIKey}
	source := commitTestAccount(t, manager.catalog, manager.pendingRoot, "b60a377d-da68-4a61-86f2-f31f04c571f2", observation)
	target := commitTestAccount(t, manager.catalog, manager.pendingRoot, "1c5de3ab-82d0-4a68-a06b-8495cdeab909", observation)
	if err := writePrivateFileAtomic(filepath.Join(source.Home, codexCredentialFilename), testAPIKeyCredential("source-api-key")); err != nil {
		t.Fatal(err)
	}
	if err := writePrivateFileAtomic(filepath.Join(target.Home, codexCredentialFilename), testAPIKeyCredential("target-api-key")); err != nil {
		t.Fatal(err)
	}
	if err := writeGlobalCredentialAtomic(manager.globalCredentialPath(), testAPIKeyCredential("source-api-key")); err != nil {
		t.Fatal(err)
	}
	setTestDeviceAccount(manager, state.active)
	manager.accountStoreReady = true
	manager.mu.Lock()
	manager.markDeviceReconciledLocked(true, time.Now().UTC())
	manager.mu.Unlock()
	return apiKeySwitchFixture{manager: manager, service: &Service{codexAccounts: manager, readiness: newReadinessCoordinator(readinessCoordinatorConfig{})}, state: state, source: source, target: target}
}

func TestVerifySwitchTargetDoesNotRequireManagedDeviceSource(t *testing.T) {
	fixture := newAPIKeySwitchFixture(t)
	fixture.manager.mu.Lock()
	fixture.manager.reconciliation = domain.CodexDeviceReconciliation{Status: domain.CodexDeviceReconciliationNotChecked, ReasonCode: "not_checked"}
	fixture.manager.mu.Unlock()
	fixture.manager.factory = &fakeCodexAccountFactory{
		capabilities: supportedCodexAccountCapabilities(),
		open: func(ports.CodexAccountContext) (ports.CodexAccountClient, error) {
			return &fakeCodexAccountClient{read: ports.CodexAccountObservation{
				Authentication: domain.AgentAuthenticationAuthorized,
				Method:         domain.CodexAuthMethodAPIKey,
			}}, nil
		},
	}

	if _, err := fixture.service.PrepareCodexAccountForSwitch(context.Background(), "6f8dfc76-8db4-4621-8974-c480093e0d55", fixture.target.Snapshot.ID); err != nil {
		t.Fatalf("PrepareCodexAccountForSwitch: %v", err)
	}
	fixture.manager.mu.Lock()
	reconciliation := fixture.manager.reconciliation
	fixture.manager.mu.Unlock()
	if reconciliation.Status != domain.CodexDeviceReconciliationNotChecked || reconciliation.ActiveAccountVerified {
		t.Fatalf("target verification unexpectedly changed device reconciliation = %#v", reconciliation)
	}
}

func TestRecentVerifiedReconciliationDoesNotScheduleAnotherRead(t *testing.T) {
	fixture := newAPIKeySwitchFixture(t)
	factory := &fakeCodexAccountFactory{open: func(ports.CodexAccountContext) (ports.CodexAccountClient, error) {
		return nil, errors.New("fresh device reconciliation unexpectedly opened Codex")
	}}
	fixture.manager.factory = factory
	fixture.manager.requestGlobalReconciliationIfNeeded()

	fixture.manager.mu.Lock()
	requested := fixture.manager.reconcileRequested
	fixture.manager.mu.Unlock()
	factory.mu.Lock()
	opens := factory.opens
	factory.mu.Unlock()
	if requested || opens != 0 {
		t.Fatalf("fresh reconciliation scheduled work: requested=%v opens=%d", requested, opens)
	}
}

func TestRepeatedReconciliationRequestsJoinOneBackgroundRead(t *testing.T) {
	fixture := newAPIKeySwitchFixture(t)
	release, err := fixture.manager.acquireAccountMutation(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	fixture.manager.mu.Lock()
	fixture.manager.reconciliation = domain.CodexDeviceReconciliation{Status: domain.CodexDeviceReconciliationNotChecked, ReasonCode: "not_checked"}
	fixture.manager.mu.Unlock()

	for range 10 {
		fixture.manager.requestGlobalReconciliationIfNeeded()
	}
	deadline := time.Now().Add(time.Second)
	for {
		fixture.manager.mu.Lock()
		started := fixture.manager.reconcile != nil
		fixture.manager.mu.Unlock()
		if started {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("background reconciliation did not start")
		}
		time.Sleep(time.Millisecond)
	}
	for range 10 {
		fixture.manager.requestGlobalReconciliationIfNeeded()
	}
	fixture.manager.mu.Lock()
	call := fixture.manager.reconcile
	fixture.manager.mu.Unlock()
	if call == nil {
		t.Fatal("concurrent requests did not join the in-flight reconciliation")
	}
	release()

	deadline = time.Now().Add(time.Second)
	for {
		fixture.manager.mu.Lock()
		finished := fixture.manager.reconciliation.Status == domain.CodexDeviceReconciliationVerified && !fixture.manager.reconcileRequested
		fixture.manager.mu.Unlock()
		if finished {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("background reconciliation did not finish")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestPrepareSwitchRejectsChangedAPIKeyCredential(t *testing.T) {
	fixture := newAPIKeySwitchFixture(t)
	if err := writePrivateFileAtomic(filepath.Join(fixture.target.Home, codexCredentialFilename), []byte("malformed-target-credential")); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.PrepareCodexAccountForSwitch(context.Background(), "6f8dfc76-8db4-4621-8974-c480093e0d55", fixture.target.Snapshot.ID); err == nil {
		t.Fatal("switch preparation accepted a changed API-key credential")
	}
}

func TestActivationRejectsExternalAuthorizedAPIKeyReplacement(t *testing.T) {
	fixture := newAPIKeySwitchFixture(t)
	switchID := "6f8dfc76-8db4-4621-8974-c480093e0d55"
	source, err := fixture.service.PrepareCodexAccountForSwitch(context.Background(), switchID, fixture.target.Snapshot.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeGlobalCredentialAtomic(fixture.manager.globalCredentialPath(), testAPIKeyCredential("external-activation-api-key")); err != nil {
		t.Fatal(err)
	}
	err = fixture.service.ActivatePreparedCodexAccountSwitch(context.Background(), source.Kind, switchID, fixture.target.Snapshot.ID)
	if !errors.Is(err, ports.ErrCodexGlobalAccountChanged) {
		t.Fatalf("activation error = %v, want global-account-changed", err)
	}
	if fixture.manager.activeAccountID() != fixture.source.Snapshot.ID {
		t.Fatalf("device account changed = %q", fixture.manager.activeAccountID())
	}
}

func TestInspectCodexAccountSwitchClassifiesExternalAPIKeyReplacement(t *testing.T) {
	fixture := newAPIKeySwitchFixture(t)
	if err := writeGlobalCredentialAtomic(fixture.manager.globalCredentialPath(), testAPIKeyCredential("external")); err != nil {
		t.Fatal(err)
	}
	state, err := fixture.service.InspectCodexAccountSwitch(context.Background(), "6f8dfc76-8db4-4621-8974-c480093e0d55", domain.CodexAccountSwitchSourceManaged, fixture.source.Snapshot.ID, fixture.target.Snapshot.ID)
	if err != nil || state != domain.CodexAccountSwitchExternalCredential {
		t.Fatalf("inspection = %q, %v", state, err)
	}
	sourceCredential, readErr := readOpaqueCredential(filepath.Join(fixture.source.Home, codexCredentialFilename))
	if readErr != nil || !bytes.Equal(sourceCredential, testAPIKeyCredential("source-api-key")) {
		t.Fatalf("source slot overwritten: %q, err=%v", sourceCredential, readErr)
	}
}

func TestInspectCodexAccountSwitchClassifiesTargetWithoutChangingProjection(t *testing.T) {
	fixture := newAPIKeySwitchFixture(t)
	targetCredential := testAPIKeyCredential("target-api-key")
	if err := writePrivateFileAtomic(filepath.Join(fixture.target.Home, codexCredentialFilename), targetCredential); err != nil {
		t.Fatal(err)
	}
	if err := writeGlobalCredentialAtomic(fixture.manager.globalCredentialPath(), targetCredential); err != nil {
		t.Fatal(err)
	}
	state, err := fixture.service.InspectCodexAccountSwitch(context.Background(), "6f8dfc76-8db4-4621-8974-c480093e0d55", domain.CodexAccountSwitchSourceManaged, fixture.source.Snapshot.ID, fixture.target.Snapshot.ID)
	if err != nil || state != domain.CodexAccountSwitchTargetInstalled {
		t.Fatalf("inspection = %q, %v", state, err)
	}
	if fixture.manager.deviceAccountID != fixture.source.Snapshot.ID {
		t.Fatalf("inspection changed device projection = %q", fixture.manager.deviceAccountID)
	}
}

func TestCredentialActivationDoesNotOpenCodex(t *testing.T) {
	fixture := newAPIKeySwitchFixture(t)
	fixture.manager.factory = &fakeCodexAccountFactory{open: func(ports.CodexAccountContext) (ports.CodexAccountClient, error) {
		t.Fatal("credential activation opened Codex")
		return nil, nil
	}}
	err := fixture.manager.activateFromCredentialLocked(context.Background(), fixture.target.Snapshot.ID, filepath.Join(fixture.target.Home, codexCredentialFilename), testAPIKeyCredential("source-api-key"))
	if err != nil {
		t.Fatal(err)
	}
	if fixture.manager.activeAccountID() != fixture.target.Snapshot.ID {
		t.Fatalf("active account = %q", fixture.manager.activeAccountID())
	}
	if fixture.manager.factory.(*fakeCodexAccountFactory).opens != 0 {
		t.Fatal("credential activation opened Codex")
	}
}

func TestConsumeResetCreditVerifiesAvailabilityAndRefreshesCapacity(t *testing.T) {
	now := time.Now().UTC()
	available := &domain.CodexResetCreditsSummary{AvailableCount: 1}
	client := &fakeCodexAccountClient{
		read: ports.CodexAccountObservation{Authentication: domain.AgentAuthenticationAuthorized, Method: domain.CodexAuthMethodChatGPT},
		capacity: ports.CodexCapacityObservation{
			ObservedAt:   now,
			Overall:      &domain.CodexCapacityBucket{LimitID: "codex", Reached: domain.CodexCapacityReached, Primary: &domain.CodexCapacityWindow{UsedPercent: 100}},
			ResetCredits: available,
		},
	}
	client.resetFn = func(string) (domain.CodexResetCreditOutcome, error) {
		client.capacity = ports.CodexCapacityObservation{
			ObservedAt:   now.Add(time.Second),
			Overall:      &domain.CodexCapacityBucket{LimitID: "codex", Reached: domain.CodexCapacityNotReached, Primary: &domain.CodexCapacityWindow{UsedPercent: 0}},
			ResetCredits: &domain.CodexResetCreditsSummary{AvailableCount: 0},
		}
		return domain.CodexResetCreditReset, nil
	}
	factory := &fakeCodexAccountFactory{capabilities: supportedCodexAccountCapabilities(), open: func(ports.CodexAccountContext) (ports.CodexAccountClient, error) { return client, nil }}
	manager := newTestCodexAccountManager(t, factory, nil)
	manager.catalog.newID = func() string { return testAccountID }
	record := commitTestAccount(t, manager.catalog, manager.pendingRoot, "b60a377d-da68-4a61-86f2-f31f04c571f2", ports.CodexAccountObservation{Authentication: domain.AgentAuthenticationAuthorized, Method: domain.CodexAuthMethodChatGPT})
	if err := manager.consumeResetCredit(context.Background(), record.Snapshot.ID, "reset-request-1"); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(client.resetKeys, []string{"reset-request-1"}) {
		t.Fatalf("reset keys = %#v", client.resetKeys)
	}
	snapshot := manager.capacity.snapshot(record.Snapshot.ID)
	if snapshot.State != domain.CodexCapacityAvailable || snapshot.RemainingPercent == nil || *snapshot.RemainingPercent != 100 || snapshot.ResetCredits == nil || snapshot.ResetCredits.AvailableCount != 0 {
		t.Fatalf("capacity after reset = %#v", snapshot)
	}
}

func TestAuthenticationRequestCancellationDoesNotCancelSharedRead(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	client := &fakeCodexAccountClient{read: ports.CodexAccountObservation{Authentication: domain.AgentAuthenticationAuthorized}, readStarted: started, readRelease: release}
	factory := &fakeCodexAccountFactory{open: func(ports.CodexAccountContext) (ports.CodexAccountClient, error) { return client, nil }}
	manager := newTestCodexAccountManager(t, factory, nil)
	manager.catalog.newID = func() string { return testAccountID }
	record := commitTestAccount(t, manager.catalog, manager.pendingRoot, "b60a377d-da68-4a61-86f2-f31f04c571f2", ports.CodexAccountObservation{Authentication: domain.AgentAuthenticationAuthorized, Method: domain.CodexAuthMethodChatGPT})
	waitCtx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := manager.ensureAuthentication(waitCtx, record, domain.AgentReadinessPurposeDisplay)
		done <- err
	}()
	<-started
	manager.mu.Lock()
	shared := manager.auth[record.Snapshot.ID].call
	manager.mu.Unlock()
	if shared == nil {
		t.Fatal("shared authentication read was not in flight")
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("wait error = %v", err)
	}
	close(release)
	// Wait on the shared call itself, not on the snapshot. The snapshot flips to
	// authorized before the verified descriptor is persisted under the account
	// home, so polling the snapshot lets TempDir cleanup race that write.
	select {
	case <-shared.done:
	case <-time.After(5 * time.Second):
		t.Fatal("shared authentication read did not finish")
	}
	latest, _ := manager.catalog.record(record.Snapshot.ID)
	if latest.Snapshot.Authentication.State != domain.AgentAuthenticationAuthorized {
		t.Fatalf("shared authentication state = %v", latest.Snapshot.Authentication.State)
	}
}

func TestLoginDeduplicatesExistingAccountByProviderID(t *testing.T) {
	email := "duplicate@example.com"
	observation := ports.CodexAccountObservation{
		Authentication: domain.AgentAuthenticationAuthorized,
		Method:         domain.CodexAuthMethodChatGPT,
		Email:          &email,
	}
	client := &fakeCodexAccountClient{read: observation}
	factory := &fakeCodexAccountFactory{
		capabilities: supportedCodexAccountCapabilities(),
		open: func(ports.CodexAccountContext) (ports.CodexAccountClient, error) {
			return client, nil
		},
	}
	state := &testCodexDeviceSeed{}
	manager := newTestCodexAccountManager(t, factory, state)

	existingID := "a1111111-1111-4111-8111-111111111111"
	manager.catalog.newID = func() string { return existingID }
	existing := commitTestAccountWithCredential(t, manager.catalog, manager.pendingRoot, "c1111111-1111-4111-8111-111111111111", testOAuthCredential("provider-account", "old-access"), observation)
	if existing.Snapshot.ID != existingID {
		t.Fatalf("existing account id = %q", existing.Snapshot.ID)
	}

	loginID := "d2222222-2222-4222-8222-222222222222"
	newAccountID := "b2222222-2222-4222-8222-222222222222"
	manager.newID = func() string { return loginID }
	manager.catalog.newID = func() string { return newAccountID }
	manager.executable = func() (string, error) { return "/ao", nil }
	manager.terminal = &fakeCodexLoginTerminal{
		writeCredential: true,
		credential:      testOAuthCredential("provider-account", "new-access"),
		result:          shellterm.ShellTerminal{HandleID: "shellterm-dedup", Title: "Add Codex account"},
	}

	started, err := manager.openLoginTerminal(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	completed, err := manager.verifyLogin(context.Background(), started.Operation.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status != domain.CodexAccountLoginCompleted {
		t.Fatalf("login status = %q", completed.Status)
	}
	snapshots := manager.catalog.snapshots()
	if len(snapshots) != 1 {
		t.Fatalf("expected 1 account after dedup login, got %d", len(snapshots))
	}
	if snapshots[0].ID != existingID {
		t.Fatalf("expected existing account %q to be reused, got %q", existingID, snapshots[0].ID)
	}
	credential, err := readOpaqueCredential(filepath.Join(existing.Home, codexCredentialFilename))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(credential, testOAuthCredential("provider-account", "new-access")) {
		t.Fatalf("credential was not replaced: %q", credential)
	}
}

func newSwitchAdmissionFixture(t *testing.T, factory *fakeCodexAccountFactory, observation ports.CodexAccountObservation) (*codexAccountManager, *Service, codexAccountRecord) {
	t.Helper()
	root := t.TempDir()
	globalHome := filepath.Join(root, "global-codex")
	if err := ensurePrivateDirectory(globalHome); err != nil {
		t.Fatal(err)
	}
	sourceID := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	state := &testCodexDeviceSeed{
		active: testCodexDeviceAccount{AccountID: sourceID, Revision: 1},
	}
	manager := newCodexAccountManager(context.Background(),
		filepath.Join(root, "accounts"), filepath.Join(root, "pending"),
		filepath.Join(root, "staging"), globalHome, factory, nil)
	ids := []string{sourceID, testAccountID}
	idx := 0
	manager.catalog.newID = func() string { id := ids[idx]; idx++; return id }
	sourceObs := ports.CodexAccountObservation{Authentication: domain.AgentAuthenticationAuthorized, Method: domain.CodexAuthMethodChatGPT}
	sourceCredential := testOAuthCredential("switch-source", "source-access")
	targetCredential := testOAuthCredential("switch-target", "target-access")
	commitTestAccountWithCredential(t, manager.catalog, manager.pendingRoot, "b60a377d-da68-4a61-86f2-f31f04c571f2", sourceCredential, sourceObs)
	record := commitTestAccountWithCredential(t, manager.catalog, manager.pendingRoot, "1c5de3ab-82d0-4a68-a06b-8495cdeab909", targetCredential, observation)
	if err := writeGlobalCredentialAtomic(manager.globalCredentialPath(), sourceCredential); err != nil {
		t.Fatal(err)
	}
	setTestDeviceAccount(manager, state.active)
	manager.accountStoreReady = true
	manager.reconciliation = domain.CodexDeviceReconciliation{
		Status: domain.CodexDeviceReconciliationVerified, ActiveAccountVerified: true,
	}
	svc := &Service{codexAccounts: manager, readiness: newReadinessCoordinator(readinessCoordinatorConfig{})}
	return manager, svc, record
}

func TestSwitchAdmissionDoesNotOpenCodexWhenProviderIsUnavailable(t *testing.T) {
	email := "switch-test@example.com"
	observation := ports.CodexAccountObservation{
		Authentication: domain.AgentAuthenticationAuthorized,
		Method:         domain.CodexAuthMethodChatGPT,
		Email:          &email,
	}
	factory := &fakeCodexAccountFactory{
		capabilities: supportedCodexAccountCapabilities(),
		open: func(ports.CodexAccountContext) (ports.CodexAccountClient, error) {
			return nil, errors.New("provider unavailable")
		},
	}
	_, svc, record := newSwitchAdmissionFixture(t, factory, observation)

	_, err := svc.PrepareCodexAccountForSwitch(context.Background(), "6f8dfc76-8db4-4621-8974-c480093e0d55", record.Snapshot.ID)
	if err != nil {
		t.Fatalf("local switch admission was blocked by provider state: %v", err)
	}
	if factory.opens != 0 {
		t.Fatalf("switch admission opened %d Codex clients", factory.opens)
	}
}

func TestSwitchAdmissionIgnoresStaleAuthenticationFailure(t *testing.T) {
	email := "read-err@example.com"
	observation := ports.CodexAccountObservation{
		Authentication: domain.AgentAuthenticationAuthorized,
		Method:         domain.CodexAuthMethodChatGPT,
		Email:          &email,
	}
	factory := &fakeCodexAccountFactory{
		capabilities: supportedCodexAccountCapabilities(),
		open: func(ports.CodexAccountContext) (ports.CodexAccountClient, error) {
			return &fakeCodexAccountClient{readErr: errors.New("timeout reading account")}, nil
		},
	}
	_, svc, record := newSwitchAdmissionFixture(t, factory, observation)

	_, err := svc.PrepareCodexAccountForSwitch(context.Background(), "6f8dfc76-8db4-4621-8974-c480093e0d55", record.Snapshot.ID)
	if err != nil {
		t.Fatalf("stale authentication failure blocked local switch admission: %v", err)
	}
}

func TestSwitchAdmissionIgnoresCachedUnauthorizedState(t *testing.T) {
	email := "unauth@example.com"
	manager, svc, record := newSwitchAdmissionFixture(t, &fakeCodexAccountFactory{}, ports.CodexAccountObservation{
		Authentication: domain.AgentAuthenticationAuthorized,
		Method:         domain.CodexAuthMethodChatGPT,
		Email:          &email,
	})
	manager.requireReauthentication(record.Snapshot.ID)

	if _, err := svc.PrepareCodexAccountForSwitch(context.Background(), "6f8dfc76-8db4-4621-8974-c480093e0d55", record.Snapshot.ID); err != nil {
		t.Fatalf("cached provider rejection blocked a locally valid credential: %v", err)
	}
}
