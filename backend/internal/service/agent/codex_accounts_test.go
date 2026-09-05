package agent

import (
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
	capacityStarted chan struct{}
	capacityRelease chan struct{}
	usage           ports.CodexUsageObservation
	resetOutcome    domain.CodexResetCreditOutcome
	resetErr        error
	resetKeys       []string
	resetFn         func(string) (domain.CodexResetCreditOutcome, error)
	events          chan ports.CodexAccountEvent
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

type fakeCodexAccountStateStore struct {
	mu     sync.Mutex
	active domain.CodexActiveAccount
	found  bool
}

type committedErrorCodexAccountStateStore struct {
	mu     sync.Mutex
	active domain.CodexActiveAccount
	err    error
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

func (s *committedErrorCodexAccountStateStore) GetCodexActiveAccount(context.Context) (domain.CodexActiveAccount, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active, true, nil
}

func (s *committedErrorCodexAccountStateStore) SetCodexActiveAccount(_ context.Context, id string, expected int64, at time.Time) (domain.CodexActiveAccount, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active.Revision != expected {
		return domain.CodexActiveAccount{}, ports.ErrCodexAccountRevisionConflict
	}
	s.active = domain.CodexActiveAccount{AccountID: id, Revision: expected + 1, ActivatedAt: at, UpdatedAt: at}
	return domain.CodexActiveAccount{}, s.err
}

func (s *fakeCodexAccountStateStore) GetCodexActiveAccount(context.Context) (domain.CodexActiveAccount, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active, s.found, nil
}

func (s *fakeCodexAccountStateStore) SetCodexActiveAccount(_ context.Context, id string, expected int64, at time.Time) (domain.CodexActiveAccount, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if (!s.found && expected != 0) || (s.found && s.active.Revision != expected) {
		return domain.CodexActiveAccount{}, ports.ErrCodexAccountRevisionConflict
	}
	s.active = domain.CodexActiveAccount{AccountID: id, Revision: expected + 1, ActivatedAt: at, UpdatedAt: at}
	s.found = true
	return s.active, nil
}

type fakeCodexLoginTerminal struct {
	mu              sync.Mutex
	opened          []shellterm.OpenCommandTerminalInput
	closed          []string
	result          shellterm.ShellTerminal
	closeErr        error
	writeCredential bool
}

func (f *fakeCodexLoginTerminal) OpenCommandTerminal(_ context.Context, in shellterm.OpenCommandTerminalInput) (shellterm.ShellTerminal, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.opened = append(f.opened, in)
	if f.writeCredential {
		if err := writePrivateFileAtomic(filepath.Join(in.Env["CODEX_HOME"], codexCredentialFilename), []byte("opaque-login-credential")); err != nil {
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

func supportedCodexAccountCapabilities() domain.CodexAccountCapabilities {
	supported := domain.CodexCapabilityObservation{State: domain.CodexCapabilitySupported, ReasonCode: domain.CodexCapabilityReasonSupported, Reason: "supported"}
	return domain.CodexAccountCapabilities{
		AccountRead: supported, NativeLogin: supported, CapacityRead: supported,
		UsageRead: supported, ResetCreditConsume: supported, ThreadResume: supported, AccountManagement: supported, GlobalSwitch: supported,
	}
}

func newTestCodexAccountManager(t *testing.T, factory ports.CodexAccountClientFactory, state CodexAccountStateStore) *codexAccountManager {
	t.Helper()
	return newTestCodexAccountManagerWithContext(context.Background(), t, factory, state)
}

func newTestCodexAccountManagerWithContext(ctx context.Context, t *testing.T, factory ports.CodexAccountClientFactory, state CodexAccountStateStore) *codexAccountManager {
	t.Helper()
	root := t.TempDir()
	return newCodexAccountManager(ctx,
		filepath.Join(root, "accounts"), filepath.Join(root, "pending-accounts"),
		filepath.Join(root, "switch-staging"), filepath.Join(root, "device-home"),
		factory, state, nil)
}

func waitForCodexAccountTestCondition(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		if condition() {
			return
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatal("timed out waiting for Codex account condition")
		}
	}
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

func TestNativeLoginVerificationCreatesAndActivatesFirstAccount(t *testing.T) {
	email := "person@example.com"
	client := &fakeCodexAccountClient{read: ports.CodexAccountObservation{Authentication: domain.AgentAuthenticationAuthorized, Method: domain.CodexAuthMethodChatGPT, Email: &email}}
	factory := &fakeCodexAccountFactory{capabilities: supportedCodexAccountCapabilities(), open: func(ports.CodexAccountContext) (ports.CodexAccountClient, error) { return client, nil }}
	state := &fakeCodexAccountStateStore{}
	manager := newTestCodexAccountManager(t, factory, state)
	manager.globalAuth = accountAuthenticationObservation(time.Now().UTC(), domain.AgentAuthenticationUnauthorized)
	ids := []string{"b60a377d-da68-4a61-86f2-f31f04c571f2", testAccountID}
	index := 0
	manager.newID = func() string { id := ids[index]; index++; return id }
	manager.catalog.newID = func() string { return testAccountID }
	manager.executable = func() (string, error) { return "/ao", nil }
	terminal := &fakeCodexLoginTerminal{writeCredential: true, result: shellterm.ShellTerminal{HandleID: "shellterm-login-1", Title: "Add Codex account"}}
	manager.terminal = terminal
	started, err := manager.openLoginTerminal(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	completed, err := manager.verifyLogin(context.Background(), started.Operation.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status != domain.CodexAccountLoginCompleted || completed.Account == nil || !completed.Account.Active || completed.Account.Label != email {
		t.Fatalf("completed login = %#v", completed)
	}
	if active := manager.activeAccountID(); active != testAccountID {
		t.Fatalf("active account = %q", active)
	}
	if state.active.Revision != 1 || state.active.AccountID != testAccountID {
		t.Fatalf("durable active account = %#v", state.active)
	}
	if len(terminal.closed) != 1 || terminal.closed[0] != "shellterm-login-1" {
		t.Fatalf("closed terminals = %#v", terminal.closed)
	}
	credential := filepath.Join(manager.catalog.root, testAccountID, codexCredentialHomeDirectory, codexCredentialFilename)
	data, err := os.ReadFile(credential)
	if err != nil || string(data) != "opaque-login-credential" {
		t.Fatalf("opaque credential = %q, err=%v", data, err)
	}
}

func TestNativeReauthenticationReplacesTheExistingAccountSlot(t *testing.T) {
	email := "person@example.com"
	observation := ports.CodexAccountObservation{Authentication: domain.AgentAuthenticationAuthorized, Method: domain.CodexAuthMethodChatGPT, Email: &email}
	client := &fakeCodexAccountClient{read: observation}
	factory := &fakeCodexAccountFactory{capabilities: supportedCodexAccountCapabilities(), open: func(ports.CodexAccountContext) (ports.CodexAccountClient, error) { return client, nil }}
	manager := newTestCodexAccountManager(t, factory, nil)
	manager.catalog.newID = func() string { return testAccountID }
	record := commitTestAccount(t, manager.catalog, manager.pendingRoot, "b60a377d-da68-4a61-86f2-f31f04c571f2", observation)
	manager.newID = func() string { return "1c5de3ab-82d0-4a68-a06b-8495cdeab909" }
	manager.executable = func() (string, error) { return "/ao", nil }
	manager.terminal = &fakeCodexLoginTerminal{
		writeCredential: true,
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
	if snapshots := manager.catalog.snapshots(); len(snapshots) != 1 || snapshots[0].ID != record.Snapshot.ID {
		t.Fatalf("reauthentication changed account identity: %#v", snapshots)
	}
	credential, err := readOpaqueCredential(filepath.Join(record.Home, codexCredentialFilename))
	if err != nil || string(credential) != "opaque-login-credential" {
		t.Fatalf("replacement credential = %q, err=%v", credential, err)
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

	authentication, err := manager.ensureAuthentication(context.Background(), record, domain.AgentReadinessPurposeDisplay, false)
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
	manager := newTestCodexAccountManager(t, nil, nil)
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

func TestInactiveLogoutReclassifiesAccountAfterGlobalGateAdmission(t *testing.T) {
	state := &fakeCodexAccountStateStore{active: domain.CodexActiveAccount{AccountID: "source-account", Revision: 1}, found: true}
	manager := newTestCodexAccountManager(t, nil, state)
	manager.catalog.newID = func() string { return testAccountID }
	record := commitTestAccount(t, manager.catalog, manager.pendingRoot, "b60a377d-da68-4a61-86f2-f31f04c571f2", ports.CodexAccountObservation{
		Authentication: domain.AgentAuthenticationAuthorized, Method: domain.CodexAuthMethodAPIKey,
	})
	credential := []byte("target-api-key")
	if err := writePrivateFileAtomic(filepath.Join(record.Home, codexCredentialFilename), credential); err != nil {
		t.Fatal(err)
	}
	if err := ensurePrivateDirectory(manager.globalHome); err != nil {
		t.Fatal(err)
	}
	if err := writeGlobalCredentialAtomic(manager.globalCredentialPath(), credential); err != nil {
		t.Fatal(err)
	}
	manager.factory = &fakeCodexAccountFactory{open: func(ports.CodexAccountContext) (ports.CodexAccountClient, error) {
		return &fakeCodexAccountClient{read: ports.CodexAccountObservation{Authentication: domain.AgentAuthenticationAuthorized, Method: domain.CodexAuthMethodAPIKey}}, nil
	}}
	gate := &blockingExclusiveCodexGate{entered: make(chan struct{}), release: make(chan struct{})}
	manager.operationGate = gate
	done := make(chan error, 1)
	go func() { done <- manager.logout(context.Background(), record.Snapshot.ID) }()
	select {
	case <-gate.entered:
	case <-time.After(time.Second):
		t.Fatal("inactive logout mutated without acquiring the global gate")
	}
	manager.mu.Lock()
	manager.active = domain.CodexActiveAccount{AccountID: record.Snapshot.ID, Revision: 2}
	manager.mu.Unlock()
	state.mu.Lock()
	state.active = manager.active
	state.mu.Unlock()
	close(gate.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(manager.globalCredentialPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("newly active credential was not cleared: %v", err)
	}
}

func TestDeleteAccountRequiresSignedOutInactiveAccount(t *testing.T) {
	manager := newTestCodexAccountManager(t, nil, nil)
	manager.catalog.newID = func() string { return testAccountID }
	record := commitTestAccount(t, manager.catalog, manager.pendingRoot, "b60a377d-da68-4a61-86f2-f31f04c571f2", ports.CodexAccountObservation{
		Authentication: domain.AgentAuthenticationAuthorized,
		Method:         domain.CodexAuthMethodChatGPT,
	})

	err := manager.deleteAccount(context.Background(), record.Snapshot.ID)
	var apiError *apierr.Error
	if err == nil {
		t.Fatal("delete signed-in account succeeded")
	} else if !errors.As(err, &apiError) || apiError.Code != "CODEX_ACCOUNT_DELETE_REQUIRES_LOGOUT" {
		t.Fatalf("delete signed-in account error = %#v", err)
	}
	if _, err := manager.catalog.markSignedOut(record.Snapshot.ID); err != nil {
		t.Fatal(err)
	}
	manager.mu.Lock()
	manager.active = domain.CodexActiveAccount{AccountID: record.Snapshot.ID, Revision: 1}
	manager.mu.Unlock()
	err = manager.deleteAccount(context.Background(), record.Snapshot.ID)
	apiError = nil
	if err == nil {
		t.Fatal("delete active account succeeded")
	} else if !errors.As(err, &apiError) || apiError.Code != "CODEX_ACCOUNT_DELETE_ACTIVE" {
		t.Fatalf("delete active account error = %#v", err)
	}
	manager.mu.Lock()
	manager.active = domain.CodexActiveAccount{}
	manager.mu.Unlock()
	if err := manager.deleteAccount(context.Background(), record.Snapshot.ID); err != nil {
		t.Fatal(err)
	}
	if accounts := manager.cached().Accounts; len(accounts) != 0 {
		t.Fatalf("accounts after deletion = %#v", accounts)
	}
}

func TestLogoutActiveAccountClearsDeviceCredentialAndPointer(t *testing.T) {
	email := "active@example.com"
	observation := ports.CodexAccountObservation{Authentication: domain.AgentAuthenticationAuthorized, Method: domain.CodexAuthMethodChatGPT, Email: &email}
	client := &fakeCodexAccountClient{read: observation}
	factory := &fakeCodexAccountFactory{open: func(ports.CodexAccountContext) (ports.CodexAccountClient, error) { return client, nil }}
	state := &fakeCodexAccountStateStore{active: domain.CodexActiveAccount{AccountID: testAccountID, Revision: 1}, found: true}
	manager := newTestCodexAccountManager(t, factory, state)
	manager.catalog.newID = func() string { return testAccountID }
	record := commitTestAccount(t, manager.catalog, manager.pendingRoot, "b60a377d-da68-4a61-86f2-f31f04c571f2", observation)
	manager.active = state.active
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
	if state.active.AccountID != "" || state.active.Revision != 2 || manager.activeAccountID() != "" {
		t.Fatalf("active pointer = %#v, manager=%q", state.active, manager.activeAccountID())
	}
	loggedOut, _ := manager.catalog.record(record.Snapshot.ID)
	if loggedOut.Snapshot.Status != domain.CodexAccountStatusSignedOut {
		t.Fatalf("active account after logout = %#v", loggedOut.Snapshot)
	}
}

func TestLogoutAdoptsActivePointerCommitReportedAsError(t *testing.T) {
	email := "active@example.com"
	observation := ports.CodexAccountObservation{Authentication: domain.AgentAuthenticationAuthorized, Method: domain.CodexAuthMethodChatGPT, Email: &email}
	state := &committedErrorCodexAccountStateStore{
		active: domain.CodexActiveAccount{AccountID: testAccountID, Revision: 1},
		err:    errors.New("injected post-commit failure"),
	}
	manager := newTestCodexAccountManager(t, &fakeCodexAccountFactory{open: func(ports.CodexAccountContext) (ports.CodexAccountClient, error) {
		return &fakeCodexAccountClient{read: observation}, nil
	}}, state)
	manager.catalog.newID = func() string { return testAccountID }
	record := commitTestAccount(t, manager.catalog, manager.pendingRoot, "b60a377d-da68-4a61-86f2-f31f04c571f2", observation)
	manager.active = state.active
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
		t.Fatalf("logout rejected a confirmed pointer commit: %v", err)
	}
	if _, err := os.Stat(manager.globalCredentialPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("device credential restored after pointer commit: %v", err)
	}
	if state.active.AccountID != "" || manager.activeAccountID() != "" {
		t.Fatalf("active pointer = %#v, manager=%q", state.active, manager.activeAccountID())
	}
}

func TestLogoutActiveAPIKeyRejectsExternalCredentialReplacement(t *testing.T) {
	state := &fakeCodexAccountStateStore{active: domain.CodexActiveAccount{AccountID: testAccountID, Revision: 1}, found: true}
	manager := newTestCodexAccountManager(t, nil, state)
	manager.catalog.newID = func() string { return testAccountID }
	record := commitTestAccount(t, manager.catalog, manager.pendingRoot, "b60a377d-da68-4a61-86f2-f31f04c571f2", ports.CodexAccountObservation{
		Authentication: domain.AgentAuthenticationAuthorized, Method: domain.CodexAuthMethodAPIKey,
	})
	manager.active = state.active
	if err := ensurePrivateDirectory(manager.globalHome); err != nil {
		t.Fatal(err)
	}
	if err := writePrivateFileAtomic(filepath.Join(record.Home, codexCredentialFilename), []byte("saved-api-key")); err != nil {
		t.Fatal(err)
	}
	if err := writeGlobalCredentialAtomic(manager.globalCredentialPath(), []byte("saved-api-key")); err != nil {
		t.Fatal(err)
	}
	manager.factory = &fakeCodexAccountFactory{open: func(ports.CodexAccountContext) (ports.CodexAccountClient, error) {
		return &fakeCodexAccountClient{readFn: func(context.Context, bool) (ports.CodexAccountObservation, error) {
			if err := writeGlobalCredentialAtomic(manager.globalCredentialPath(), []byte("external-api-key")); err != nil {
				t.Fatal(err)
			}
			return ports.CodexAccountObservation{Authentication: domain.AgentAuthenticationAuthorized, Method: domain.CodexAuthMethodAPIKey}, nil
		}}, nil
	}}

	if err := manager.logout(context.Background(), record.Snapshot.ID); err == nil {
		t.Fatal("logout accepted an externally replaced API-key credential")
	}
	global, err := readOpaqueCredential(manager.globalCredentialPath())
	if err != nil || string(global) != "external-api-key" {
		t.Fatalf("external global credential = %q, %v", global, err)
	}
	saved, err := readOpaqueCredential(filepath.Join(record.Home, codexCredentialFilename))
	if err != nil || string(saved) != "saved-api-key" {
		t.Fatalf("saved credential changed = %q, %v", saved, err)
	}
	if state.active.AccountID != record.Snapshot.ID {
		t.Fatalf("active pointer changed = %#v", state.active)
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
	if operation.Status != domain.CodexAccountLoginUnverified {
		t.Fatalf("operation after close failure = %#v", operation)
	}
}

func TestBootstrapImportsOpaqueDeviceCredentialWithoutMutatingDeviceHome(t *testing.T) {
	root := t.TempDir()
	device := filepath.Join(root, "device")
	if err := ensurePrivateDirectory(device); err != nil {
		t.Fatal(err)
	}
	deviceCredential := filepath.Join(device, codexCredentialFilename)
	original := []byte("opaque-device-auth\x00\xff")
	if err := writePrivateFileAtomic(deviceCredential, original); err != nil {
		t.Fatal(err)
	}
	email := "device@example.com"
	client := &fakeCodexAccountClient{read: ports.CodexAccountObservation{Authentication: domain.AgentAuthenticationAuthorized, Method: domain.CodexAuthMethodChatGPT, Email: &email}}
	factory := &fakeCodexAccountFactory{capabilities: supportedCodexAccountCapabilities(), open: func(ports.CodexAccountContext) (ports.CodexAccountClient, error) { return client, nil }}
	state := &fakeCodexAccountStateStore{}
	manager := newCodexAccountManager(context.Background(), filepath.Join(root, "accounts"), filepath.Join(root, "pending"), filepath.Join(root, "staging"), device, factory, state, nil)
	ids := []string{"b60a377d-da68-4a61-86f2-f31f04c571f2", testAccountID}
	index := 0
	manager.newID = func() string { id := ids[index]; index++; return id }
	manager.catalog.newID = func() string { return testAccountID }
	if err := manager.waitBootstrap(context.Background()); err != nil {
		t.Fatal(err)
	}
	if manager.activeAccountID() != testAccountID {
		t.Fatalf("active account = %q", manager.activeAccountID())
	}
	after, err := os.ReadFile(deviceCredential)
	if err != nil || !slices.Equal(after, original) {
		t.Fatalf("device credential changed: %q err=%v", after, err)
	}
	if _, err := os.Stat(filepath.Join(root, "runtime")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("bootstrap created an obsolete private runtime: %v", err)
	}
}

func TestCodexAccountBootstrapRetriesAfterTransientOpenFailure(t *testing.T) {
	daemonCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var failureMu sync.Mutex
	failOpen := true
	factory := &fakeCodexAccountFactory{
		capabilities: supportedCodexAccountCapabilities(),
		open: func(ports.CodexAccountContext) (ports.CodexAccountClient, error) {
			failureMu.Lock()
			defer failureMu.Unlock()
			if failOpen {
				failOpen = false
				return nil, errors.New("transient open failure")
			}
			return &fakeCodexAccountClient{read: ports.CodexAccountObservation{Authentication: domain.AgentAuthenticationUnauthorized}}, nil
		},
	}
	manager := newTestCodexAccountManagerWithContext(daemonCtx, t, factory, nil)
	manager.bootstrapRetryDelays = []time.Duration{time.Millisecond}
	service := &Service{codexAccounts: manager}

	err := service.WaitCodexAccountBootstrap(context.Background())
	var apiError *apierr.Error
	if !errors.As(err, &apiError) || apiError.Kind != apierr.KindUnavailable || apiError.Code != "CODEX_ACCOUNT_MANAGEMENT_UNAVAILABLE" || apiError.Message != "Codex account setup did not complete" {
		t.Fatalf("first bootstrap error = %#v, want stable unavailable envelope", err)
	}
	waitForCodexAccountTestCondition(t, func() bool {
		manager.mu.Lock()
		ready := manager.bootstrapped
		manager.mu.Unlock()
		return ready
	})
	if err := service.WaitCodexAccountBootstrap(context.Background()); err != nil {
		t.Fatalf("bootstrap after automatic retry: %v", err)
	}
	waitForCodexAccountTestCondition(t, func() bool {
		factory.mu.Lock()
		defer factory.mu.Unlock()
		return factory.capabilityChecks == 1
	})
	service.WarmCodexAccounts()
	if err := service.WaitCodexAccountBootstrap(context.Background()); err != nil {
		t.Fatal(err)
	}
	factory.mu.Lock()
	opens, capabilityChecks := factory.opens, factory.capabilityChecks
	factory.mu.Unlock()
	if opens != 2 || capabilityChecks != 1 {
		t.Fatalf("native calls after recovery: opens=%d capabilities=%d, want 2 and 1", opens, capabilityChecks)
	}
}

func TestCodexAccountBootstrapRetriesAfterTransientReadFailure(t *testing.T) {
	daemonCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var failureMu sync.Mutex
	failRead := true
	factory := &fakeCodexAccountFactory{open: func(ports.CodexAccountContext) (ports.CodexAccountClient, error) {
		return &fakeCodexAccountClient{readFn: func(context.Context, bool) (ports.CodexAccountObservation, error) {
			failureMu.Lock()
			defer failureMu.Unlock()
			if failRead {
				failRead = false
				return ports.CodexAccountObservation{}, context.DeadlineExceeded
			}
			return ports.CodexAccountObservation{Authentication: domain.AgentAuthenticationUnauthorized}, nil
		}}, nil
	}}
	manager := newTestCodexAccountManagerWithContext(daemonCtx, t, factory, nil)
	manager.bootstrapRetryDelays = []time.Duration{time.Millisecond}

	err := manager.waitBootstrap(context.Background())
	var failure *codexBootstrapFailure
	if !errors.As(err, &failure) || failure.reason != "account_read_inconclusive" || !failure.retryable {
		t.Fatalf("first bootstrap error = %#v, want retryable account_read_inconclusive", err)
	}
	waitForCodexAccountTestCondition(t, func() bool {
		manager.mu.Lock()
		ready := manager.bootstrapped
		manager.mu.Unlock()
		return ready
	})
	factory.mu.Lock()
	opens := factory.opens
	factory.mu.Unlock()
	if opens != 2 {
		t.Fatalf("account client opens = %d, want 2", opens)
	}
}

func TestCodexAccountBootstrapWarmAndConcurrentWaitersShareAttempt(t *testing.T) {
	daemonCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	readStarted := make(chan struct{})
	readRelease := make(chan struct{})
	factory := &fakeCodexAccountFactory{open: func(ports.CodexAccountContext) (ports.CodexAccountClient, error) {
		return &fakeCodexAccountClient{
			read:        ports.CodexAccountObservation{Authentication: domain.AgentAuthenticationUnauthorized},
			readStarted: readStarted,
			readRelease: readRelease,
		}, nil
	}}
	manager := newTestCodexAccountManagerWithContext(daemonCtx, t, factory, nil)
	service := &Service{codexAccounts: manager}
	service.WarmCodexAccounts()
	select {
	case <-readStarted:
	case <-time.After(time.Second):
		t.Fatal("bootstrap account read did not start")
	}

	const waiters = 20
	results := make(chan error, waiters)
	for range waiters {
		go func() { results <- service.WaitCodexAccountBootstrap(context.Background()) }()
	}
	factory.mu.Lock()
	opens := factory.opens
	factory.mu.Unlock()
	if opens != 1 {
		t.Fatalf("account client opens during shared bootstrap = %d, want 1", opens)
	}
	close(readRelease)
	for range waiters {
		if err := <-results; err != nil {
			t.Fatalf("shared bootstrap waiter: %v", err)
		}
	}
	factory.mu.Lock()
	opens = factory.opens
	factory.mu.Unlock()
	if opens != 1 {
		t.Fatalf("account client opens after shared bootstrap = %d, want 1", opens)
	}
}

func TestCodexAccountBootstrapBackoffProgressionAndCooldown(t *testing.T) {
	daemonCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	factory := &fakeCodexAccountFactory{open: func(ports.CodexAccountContext) (ports.CodexAccountClient, error) {
		return nil, errors.New("persistent open failure")
	}}
	manager := newTestCodexAccountManagerWithContext(daemonCtx, t, factory, nil)
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	manager.now = func() time.Time { return now }

	for attempt, wantDelay := range []time.Duration{15 * time.Second, time.Minute, 5 * time.Minute, 5 * time.Minute} {
		if err := manager.waitBootstrap(context.Background()); err == nil {
			t.Fatalf("attempt %d unexpectedly succeeded", attempt+1)
		}
		manager.mu.Lock()
		nextRetryAt := manager.bootstrapNextRetryAt
		manager.mu.Unlock()
		if got := nextRetryAt.Sub(now); got != wantDelay {
			t.Fatalf("attempt %d retry delay = %s, want %s", attempt+1, got, wantDelay)
		}
		factory.mu.Lock()
		opensBeforeCooldownCall := factory.opens
		factory.mu.Unlock()
		if err := manager.waitBootstrap(context.Background()); err == nil {
			t.Fatalf("attempt %d cooldown call unexpectedly succeeded", attempt+1)
		}
		factory.mu.Lock()
		opensAfterCooldownCall := factory.opens
		factory.mu.Unlock()
		if opensAfterCooldownCall != opensBeforeCooldownCall {
			t.Fatalf("attempt %d cooldown started native work: before=%d after=%d", attempt+1, opensBeforeCooldownCall, opensAfterCooldownCall)
		}
		now = nextRetryAt
	}
}

func TestCodexAccountBootstrapCallerCancellationDoesNotCancelAttempt(t *testing.T) {
	daemonCtx, cancelDaemon := context.WithCancel(context.Background())
	defer cancelDaemon()
	readStarted := make(chan struct{})
	readRelease := make(chan struct{})
	factory := &fakeCodexAccountFactory{open: func(ports.CodexAccountContext) (ports.CodexAccountClient, error) {
		return &fakeCodexAccountClient{
			read:        ports.CodexAccountObservation{Authentication: domain.AgentAuthenticationUnauthorized},
			readStarted: readStarted,
			readRelease: readRelease,
		}, nil
	}}
	manager := newTestCodexAccountManagerWithContext(daemonCtx, t, factory, nil)
	service := &Service{codexAccounts: manager}
	callerCtx, cancelCaller := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- service.WaitCodexAccountBootstrap(callerCtx) }()
	select {
	case <-readStarted:
	case <-time.After(time.Second):
		t.Fatal("bootstrap account read did not start")
	}
	cancelCaller()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled waiter error = %v, want context canceled", err)
	}
	manager.mu.Lock()
	callStillRunning := manager.bootstrapCall != nil
	manager.mu.Unlock()
	if !callStillRunning {
		t.Fatal("canceling one waiter canceled the daemon bootstrap")
	}
	close(readRelease)
	if err := service.WaitCodexAccountBootstrap(context.Background()); err != nil {
		t.Fatalf("joining bootstrap after canceled waiter: %v", err)
	}
}

func TestCodexAccountBootstrapDaemonCancellationStopsScheduledRetry(t *testing.T) {
	daemonCtx, cancelDaemon := context.WithCancel(context.Background())
	factory := &fakeCodexAccountFactory{open: func(ports.CodexAccountContext) (ports.CodexAccountClient, error) {
		return nil, errors.New("persistent open failure")
	}}
	manager := newTestCodexAccountManagerWithContext(daemonCtx, t, factory, nil)
	manager.bootstrapRetryDelays = []time.Duration{10 * time.Millisecond}
	if err := manager.waitBootstrap(context.Background()); err == nil {
		t.Fatal("bootstrap unexpectedly succeeded")
	}
	cancelDaemon()
	time.Sleep(30 * time.Millisecond)
	factory.mu.Lock()
	opens := factory.opens
	factory.mu.Unlock()
	if opens != 1 {
		t.Fatalf("account client opens after daemon cancellation = %d, want 1", opens)
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
	state := &fakeCodexAccountStateStore{
		active: domain.CodexActiveAccount{AccountID: testAccountID, Revision: 1},
		found:  true,
	}
	manager := newCodexAccountManager(
		context.Background(),
		filepath.Join(root, "accounts"),
		filepath.Join(root, "pending"),
		filepath.Join(root, "staging"),
		globalHome,
		nil,
		state,
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

	if err := manager.bootstrapInner(); err != nil {
		t.Fatal(err)
	}
	view := manager.cached()
	if view.ActiveAccountID != testAccountID || view.UnmanagedGlobalAccount != nil || len(view.Accounts) != 1 || !view.Accounts[0].Active {
		t.Fatalf("reconciled device account = %#v, refresh requests = %#v", view, refreshRequests)
	}
	if slices.Contains(refreshRequests, true) {
		t.Fatalf("reconciliation requested proactive refresh: %#v", refreshRequests)
	}
}

func TestGlobalReconciliationInconclusiveReadPreservesActiveAccount(t *testing.T) {
	root := t.TempDir()
	globalHome := filepath.Join(root, "global-codex")
	if err := ensurePrivateDirectory(globalHome); err != nil {
		t.Fatal(err)
	}
	if err := writePrivateFileAtomic(filepath.Join(globalHome, codexCredentialFilename), []byte("opaque-device-credential")); err != nil {
		t.Fatal(err)
	}
	email := "known@example.com"
	state := &fakeCodexAccountStateStore{
		active: domain.CodexActiveAccount{AccountID: testAccountID, Revision: 7},
		found:  true,
	}
	manager := newCodexAccountManager(context.Background(), filepath.Join(root, "accounts"), filepath.Join(root, "pending"), filepath.Join(root, "staging"), globalHome, nil, state, nil)
	manager.catalog.newID = func() string { return testAccountID }
	record := commitTestAccount(t, manager.catalog, manager.pendingRoot, "b60a377d-da68-4a61-86f2-f31f04c571f2", ports.CodexAccountObservation{
		Authentication: domain.AgentAuthenticationAuthorized,
		Method:         domain.CodexAuthMethodChatGPT,
		Email:          &email,
	})
	manager.active = state.active
	manager.catalog.updateSnapshot(record.Snapshot.ID, func(snapshot *domain.CodexAccountSnapshot) {
		snapshot.Authentication = accountAuthenticationObservation(time.Now().UTC(), domain.AgentAuthenticationAuthorized)
	})
	manager.factory = &fakeCodexAccountFactory{open: func(ports.CodexAccountContext) (ports.CodexAccountClient, error) {
		return &fakeCodexAccountClient{readErr: errors.New("temporary native account/read failure")}, nil
	}}

	_ = manager.reconcileGlobal(context.Background())
	view := manager.cached()
	if state.active.AccountID != testAccountID || state.active.Revision != 7 {
		t.Fatalf("durable active account changed = %#v", state.active)
	}
	if view.ActiveAccountID != testAccountID || len(view.Accounts) != 1 || !view.Accounts[0].Active {
		t.Fatalf("cached active account was discarded = %#v", view)
	}
	if view.UnmanagedGlobalAccount == nil || view.UnmanagedGlobalAccount.ReasonCode != "global_account_unverified" {
		t.Fatalf("inconclusive global projection = %#v", view.UnmanagedGlobalAccount)
	}
}

func TestGlobalReconciliationExplicitSignedOutClearsActiveAccount(t *testing.T) {
	root := t.TempDir()
	globalHome := filepath.Join(root, "global-codex")
	if err := ensurePrivateDirectory(globalHome); err != nil {
		t.Fatal(err)
	}
	state := &fakeCodexAccountStateStore{
		active: domain.CodexActiveAccount{AccountID: testAccountID, Revision: 4},
		found:  true,
	}
	manager := newCodexAccountManager(context.Background(), filepath.Join(root, "accounts"), filepath.Join(root, "pending"), filepath.Join(root, "staging"), globalHome, nil, state, nil)
	manager.active = state.active
	manager.factory = &fakeCodexAccountFactory{open: func(ports.CodexAccountContext) (ports.CodexAccountClient, error) {
		return &fakeCodexAccountClient{read: ports.CodexAccountObservation{Authentication: domain.AgentAuthenticationUnauthorized}}, nil
	}}

	if err := manager.reconcileGlobal(context.Background()); err != nil {
		t.Fatal(err)
	}
	if state.active.AccountID != "" || state.active.Revision != 5 {
		t.Fatalf("explicit signed-out state did not clear active pointer = %#v", state.active)
	}
}

func TestGlobalReconciliationSerializesCredentialMutationWithLogout(t *testing.T) {
	root := t.TempDir()
	globalHome := filepath.Join(root, "global-codex")
	if err := ensurePrivateDirectory(globalHome); err != nil {
		t.Fatal(err)
	}
	if err := writePrivateFileAtomic(filepath.Join(globalHome, codexCredentialFilename), []byte("device-credential")); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	manager := newCodexAccountManager(
		context.Background(), filepath.Join(root, "accounts"), filepath.Join(root, "pending"), filepath.Join(root, "staging"), globalHome,
		&fakeCodexAccountFactory{open: func(ports.CodexAccountContext) (ports.CodexAccountClient, error) {
			return &fakeCodexAccountClient{
				read:        ports.CodexAccountObservation{Authentication: domain.AgentAuthenticationUnauthorized},
				readStarted: started,
				readRelease: release,
			}, nil
		}},
		nil,
		nil,
	)
	manager.catalog.newID = func() string { return testAccountID }
	record := commitTestAccount(t, manager.catalog, manager.pendingRoot, "b60a377d-da68-4a61-86f2-f31f04c571f2", ports.CodexAccountObservation{
		Authentication: domain.AgentAuthenticationAuthorized,
		Method:         domain.CodexAuthMethodAPIKey,
	})

	reconcileDone := make(chan error, 1)
	go func() { reconcileDone <- manager.reconcileGlobal(context.Background()) }()
	<-started
	logoutDone := make(chan error, 1)
	go func() { logoutDone <- manager.logout(context.Background(), record.Snapshot.ID) }()
	select {
	case err := <-logoutDone:
		t.Fatalf("logout crossed an active reconciliation boundary: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	if _, err := os.Stat(filepath.Join(record.Home, codexCredentialFilename)); err != nil {
		t.Fatalf("credential changed before reconciliation released it: %v", err)
	}
	close(release)
	if err := <-reconcileDone; err != nil {
		t.Fatal(err)
	}
	if err := <-logoutDone; err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(record.Home, codexCredentialFilename)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("logout did not remove the credential after admission: %v", err)
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

	matched, ok := manager.matchGlobalAccount(ports.CodexAccountObservation{Authentication: domain.AgentAuthenticationAuthorized, Method: domain.CodexAuthMethodAPIKey}, []byte("credential-b"))
	if !ok || matched.Snapshot.ID != second.Snapshot.ID {
		t.Fatalf("unique opaque credential match = (%q, %v), want second account", matched.Snapshot.ID, ok)
	}
	if err := writePrivateFileAtomic(filepath.Join(first.Home, codexCredentialFilename), []byte("credential-b")); err != nil {
		t.Fatal(err)
	}
	if matched, ok := manager.matchGlobalAccount(ports.CodexAccountObservation{Authentication: domain.AgentAuthenticationAuthorized, Method: domain.CodexAuthMethodAPIKey}, []byte("credential-b")); ok {
		t.Fatalf("ambiguous opaque credential matched account %q", matched.Snapshot.ID)
	}
}

func TestGlobalReconciliationAutoImportsExternalAccountChanges(t *testing.T) {
	root := t.TempDir()
	globalHome := filepath.Join(root, "global-codex")
	if err := ensurePrivateDirectory(globalHome); err != nil {
		t.Fatal(err)
	}
	globalCredential := filepath.Join(globalHome, codexCredentialFilename)
	if err := writePrivateFileAtomic(globalCredential, []byte("credential-a")); err != nil {
		t.Fatal(err)
	}
	emailA, emailB := "a@example.com", "b@example.com"
	observationForHome := func(home string) (ports.CodexAccountObservation, error) {
		credential, err := readOpaqueCredential(filepath.Join(home, codexCredentialFilename))
		if err != nil {
			return ports.CodexAccountObservation{}, err
		}
		switch string(credential) {
		case "credential-a":
			return ports.CodexAccountObservation{Authentication: domain.AgentAuthenticationAuthorized, Method: domain.CodexAuthMethodChatGPT, Email: &emailA}, nil
		case "credential-b":
			return ports.CodexAccountObservation{Authentication: domain.AgentAuthenticationAuthorized, Method: domain.CodexAuthMethodChatGPT, Email: &emailB}, nil
		default:
			return ports.CodexAccountObservation{Authentication: domain.AgentAuthenticationUnknown}, nil
		}
	}
	factory := &fakeCodexAccountFactory{capabilities: supportedCodexAccountCapabilities()}
	factory.open = func(account ports.CodexAccountContext) (ports.CodexAccountClient, error) {
		observation, err := observationForHome(account.Home)
		return &fakeCodexAccountClient{read: observation, readErr: err}, nil
	}
	state := &fakeCodexAccountStateStore{}
	manager := newCodexAccountManager(context.Background(), filepath.Join(root, "accounts"), filepath.Join(root, "pending"), filepath.Join(root, "staging"), globalHome, factory, state, nil)
	operationIDs := []string{
		"b60a377d-da68-4a61-86f2-f31f04c571f2", "a8600b36-0e78-461d-9dd8-378fe18e271d",
		"8b71ac75-4d81-45ee-bbe2-952cbe15e353", "82db3fd8-e87f-4c5c-8511-b63fde7937ae",
	}
	accountIDs := []string{testAccountID, "bb1e9a5d-37ad-43f8-83bd-13de8168f8af"}
	manager.newID = func() string {
		id := operationIDs[0]
		operationIDs = operationIDs[1:]
		return id
	}
	manager.catalog.newID = func() string {
		id := accountIDs[0]
		accountIDs = accountIDs[1:]
		return id
	}

	if err := manager.bootstrapInner(); err != nil {
		t.Fatal(err)
	}
	first := manager.activeAccountID()
	if first != testAccountID || state.active.Revision != 1 {
		t.Fatalf("first imported active account = %q, state=%#v", first, state.active)
	}
	if err := writeGlobalCredentialAtomic(globalCredential, []byte("credential-b")); err != nil {
		t.Fatal(err)
	}
	if err := manager.reconcileGlobal(context.Background()); err != nil {
		t.Fatal(err)
	}
	second := manager.activeAccountID()
	if second != "bb1e9a5d-37ad-43f8-83bd-13de8168f8af" || state.active.Revision != 2 {
		t.Fatalf("external login was not imported as active: %q state=%#v", second, state.active)
	}
	if snapshots := manager.catalog.snapshots(); len(snapshots) != 2 {
		t.Fatalf("accounts after external login = %#v", snapshots)
	}
	stored, err := readOpaqueCredential(filepath.Join(manager.catalog.root, second, codexCredentialHomeDirectory, codexCredentialFilename))
	if err != nil || string(stored) != "credential-b" {
		t.Fatalf("imported external credential = %q, err=%v", stored, err)
	}
}

func TestGlobalReconciliationReactivatesMatchingSignedOutAccount(t *testing.T) {
	root := t.TempDir()
	globalHome := filepath.Join(root, "global-codex")
	if err := ensurePrivateDirectory(globalHome); err != nil {
		t.Fatal(err)
	}
	credential := []byte("externally-restored-credential")
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
	state := &fakeCodexAccountStateStore{}
	manager := newCodexAccountManager(context.Background(), filepath.Join(root, "accounts"), filepath.Join(root, "pending"), filepath.Join(root, "staging"), globalHome, factory, state, nil)
	manager.catalog.newID = func() string { return testAccountID }
	record := commitTestAccount(t, manager.catalog, manager.pendingRoot, "b60a377d-da68-4a61-86f2-f31f04c571f2", observation)
	if _, err := manager.catalog.markSignedOut(record.Snapshot.ID); err != nil {
		t.Fatal(err)
	}

	if err := manager.bootstrapInner(); err != nil {
		t.Fatal(err)
	}
	view := manager.cached()
	if view.ActiveAccountID != record.Snapshot.ID || len(view.Accounts) != 1 || view.Accounts[0].Status != domain.CodexAccountStatusValid || !view.Accounts[0].Active {
		t.Fatalf("reconciled signed-out account = %#v", view)
	}
	stored, err := readOpaqueCredential(filepath.Join(record.Home, codexCredentialFilename))
	if err != nil || !slices.Equal(stored, credential) {
		t.Fatalf("reactivated credential = %q, err=%v", stored, err)
	}
}

func TestUnmanagedGlobalCredentialDoesNotBlockNormalAuthentication(t *testing.T) {
	root := t.TempDir()
	globalHome := filepath.Join(root, "global-codex")
	if err := ensurePrivateDirectory(globalHome); err != nil {
		t.Fatal(err)
	}
	email := "keyring@example.com"
	factory := &fakeCodexAccountFactory{
		capabilities: supportedCodexAccountCapabilities(),
		open: func(account ports.CodexAccountContext) (ports.CodexAccountClient, error) {
			if account.Managed {
				t.Fatal("keyring-backed global account was copied into a managed home")
			}
			return &fakeCodexAccountClient{read: ports.CodexAccountObservation{Authentication: domain.AgentAuthenticationAuthorized, Method: domain.CodexAuthMethodChatGPT, Email: &email}}, nil
		},
	}
	manager := newCodexAccountManager(context.Background(), filepath.Join(root, "accounts"), filepath.Join(root, "pending"), filepath.Join(root, "staging"), globalHome, factory, nil, nil)
	if err := manager.bootstrapInner(); err != nil {
		t.Fatal(err)
	}
	view := manager.cached()
	if view.ActiveAccountID != "" || view.UnmanagedGlobalAccount == nil || view.UnmanagedGlobalAccount.AccountEmail == nil || *view.UnmanagedGlobalAccount.AccountEmail != email {
		t.Fatalf("unmanaged global account = %#v", view)
	}
	if got := manager.detectCapabilities(context.Background()).GlobalSwitch.State; got != domain.CodexCapabilityUnsupported {
		t.Fatalf("global switch capability = %q, want unsupported", got)
	}
	manager.mu.Lock()
	manager.bootstrapped = true
	manager.mu.Unlock()
	service := &Service{codexAccounts: manager}
	auth, handled := service.structuredCodexAuthentication(context.Background(), string(domain.HarnessCodex), domain.AgentReadinessPurposeDisplay)
	if !handled || auth.State != domain.AgentAuthenticationAuthorized {
		t.Fatalf("normal Codex authentication = (%#v, %v), want authorized", auth, handled)
	}
}

func TestUnmanagedGlobalStatePreservesActiveSlotProjection(t *testing.T) {
	root := t.TempDir()
	globalHome := filepath.Join(root, "global-codex")
	if err := ensurePrivateDirectory(globalHome); err != nil {
		t.Fatal(err)
	}
	slotEmail, globalEmail := "saved@example.com", "keyring@example.com"
	state := &fakeCodexAccountStateStore{active: domain.CodexActiveAccount{AccountID: testAccountID, Revision: 3}, found: true}
	var opened []ports.CodexAccountContext
	manager := newCodexAccountManager(context.Background(), filepath.Join(root, "accounts"), filepath.Join(root, "pending"), filepath.Join(root, "staging"), globalHome, nil, state, nil)
	manager.catalog.newID = func() string { return testAccountID }
	record := commitTestAccount(t, manager.catalog, manager.pendingRoot, "b60a377d-da68-4a61-86f2-f31f04c571f2", ports.CodexAccountObservation{
		Authentication: domain.AgentAuthenticationAuthorized, Method: domain.CodexAuthMethodChatGPT, Email: &slotEmail,
	})
	manager.active = state.active
	manager.factory = &fakeCodexAccountFactory{capabilities: supportedCodexAccountCapabilities(), open: func(account ports.CodexAccountContext) (ports.CodexAccountClient, error) {
		opened = append(opened, account)
		if account.Managed {
			return &fakeCodexAccountClient{read: ports.CodexAccountObservation{Authentication: domain.AgentAuthenticationAuthorized, Method: domain.CodexAuthMethodChatGPT, Email: &slotEmail}}, nil
		}
		return &fakeCodexAccountClient{read: ports.CodexAccountObservation{Authentication: domain.AgentAuthenticationAuthorized, Method: domain.CodexAuthMethodChatGPT, Email: &globalEmail}}, nil
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
	if _, err := manager.ensureAuthentication(context.Background(), record, domain.AgentReadinessPurposeDisplay, false); err != nil {
		t.Fatal(err)
	}
	view := manager.cached()
	if len(opened) < 2 || !opened[len(opened)-1].Managed || opened[len(opened)-1].Home != record.Home {
		t.Fatalf("active slot authentication contexts = %#v", opened)
	}
	if len(view.Accounts) != 1 || view.Accounts[0].AccountEmail == nil || *view.Accounts[0].AccountEmail != slotEmail || view.Accounts[0].UsageSummary == nil || view.Accounts[0].UsageSummary.LatestDayTokens == nil || *view.Accounts[0].UsageSummary.LatestDayTokens != tokens || view.Accounts[0].Capacity.State != domain.CodexCapacityAvailable {
		t.Fatalf("active slot projection changed under unmanaged global state = %#v", view)
	}
}

type apiKeySwitchFixture struct {
	manager *codexAccountManager
	service *Service
	state   *fakeCodexAccountStateStore
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
	state := &fakeCodexAccountStateStore{active: domain.CodexActiveAccount{AccountID: testAccountID, Revision: 1}, found: true}
	manager := newCodexAccountManager(context.Background(), filepath.Join(root, "accounts"), filepath.Join(root, "pending"), filepath.Join(root, "staging"), globalHome, nil, state, nil)
	ids := []string{testAccountID, "bb1e9a5d-37ad-43f8-83bd-13de8168f8af"}
	manager.catalog.newID = func() string { id := ids[0]; ids = ids[1:]; return id }
	observation := ports.CodexAccountObservation{Authentication: domain.AgentAuthenticationAuthorized, Method: domain.CodexAuthMethodAPIKey}
	source := commitTestAccount(t, manager.catalog, manager.pendingRoot, "b60a377d-da68-4a61-86f2-f31f04c571f2", observation)
	target := commitTestAccount(t, manager.catalog, manager.pendingRoot, "1c5de3ab-82d0-4a68-a06b-8495cdeab909", observation)
	if err := writePrivateFileAtomic(filepath.Join(source.Home, codexCredentialFilename), []byte("source-api-key")); err != nil {
		t.Fatal(err)
	}
	if err := writePrivateFileAtomic(filepath.Join(target.Home, codexCredentialFilename), []byte("target-api-key")); err != nil {
		t.Fatal(err)
	}
	if err := writeGlobalCredentialAtomic(manager.globalCredentialPath(), []byte("source-api-key")); err != nil {
		t.Fatal(err)
	}
	manager.active = state.active
	manager.bootstrapped = true
	return apiKeySwitchFixture{manager: manager, service: &Service{codexAccounts: manager, readiness: newReadinessCoordinator(readinessCoordinatorConfig{})}, state: state, source: source, target: target}
}

func TestVerifySwitchTargetRejectsExternalAPIKeyReplacement(t *testing.T) {
	fixture := newAPIKeySwitchFixture(t)
	fixture.manager.factory = &fakeCodexAccountFactory{capabilities: supportedCodexAccountCapabilities(), open: func(account ports.CodexAccountContext) (ports.CodexAccountClient, error) {
		return &fakeCodexAccountClient{readFn: func(context.Context, bool) (ports.CodexAccountObservation, error) {
			if err := writePrivateFileAtomic(filepath.Join(account.Home, codexCredentialFilename), []byte("external-target-api-key")); err != nil {
				t.Fatal(err)
			}
			return ports.CodexAccountObservation{Authentication: domain.AgentAuthenticationAuthorized, Method: domain.CodexAuthMethodAPIKey}, nil
		}}, nil
	}}
	if err := fixture.service.VerifyCodexAccountForSwitch(context.Background(), fixture.target.Snapshot.ID); err == nil {
		t.Fatal("target verification accepted an externally replaced API-key credential")
	}
}

func TestCheckpointRejectsExternalAPIKeySourceReplacement(t *testing.T) {
	fixture := newAPIKeySwitchFixture(t)
	fixture.manager.factory = &fakeCodexAccountFactory{capabilities: supportedCodexAccountCapabilities(), open: func(ports.CodexAccountContext) (ports.CodexAccountClient, error) {
		return &fakeCodexAccountClient{readFn: func(context.Context, bool) (ports.CodexAccountObservation, error) {
			if err := writeGlobalCredentialAtomic(fixture.manager.globalCredentialPath(), []byte("external-source-api-key")); err != nil {
				t.Fatal(err)
			}
			return ports.CodexAccountObservation{Authentication: domain.AgentAuthenticationAuthorized, Method: domain.CodexAuthMethodAPIKey}, nil
		}}, nil
	}}
	if _, err := fixture.service.CheckpointAndActivateCodexAccount(context.Background(), "6f8dfc76-8db4-4621-8974-c480093e0d55", fixture.target.Snapshot.ID, 1); err == nil {
		t.Fatal("checkpoint accepted an externally replaced source API-key credential")
	}
	saved, err := readOpaqueCredential(filepath.Join(fixture.source.Home, codexCredentialFilename))
	if err != nil || string(saved) != "source-api-key" {
		t.Fatalf("source slot was overwritten = %q, %v", saved, err)
	}
}

func TestActivationRejectsExternalAuthorizedAPIKeyReplacement(t *testing.T) {
	fixture := newAPIKeySwitchFixture(t)
	fixture.manager.factory = &fakeCodexAccountFactory{open: func(ports.CodexAccountContext) (ports.CodexAccountClient, error) {
		return &fakeCodexAccountClient{readFn: func(context.Context, bool) (ports.CodexAccountObservation, error) {
			if err := writeGlobalCredentialAtomic(fixture.manager.globalCredentialPath(), []byte("external-activation-api-key")); err != nil {
				t.Fatal(err)
			}
			return ports.CodexAccountObservation{Authentication: domain.AgentAuthenticationAuthorized, Method: domain.CodexAuthMethodAPIKey}, nil
		}}, nil
	}}
	_, err := fixture.manager.activateFromCredentialLocked(context.Background(), fixture.target.Snapshot.ID, 1, filepath.Join(fixture.target.Home, codexCredentialFilename), []byte("source-api-key"))
	if !errors.Is(err, ports.ErrCodexGlobalAccountChanged) {
		t.Fatalf("activation error = %v, want global-account-changed", err)
	}
	if fixture.state.active.AccountID != fixture.source.Snapshot.ID || fixture.state.active.Revision != 1 {
		t.Fatalf("active pointer changed = %#v", fixture.state.active)
	}
}

func TestVerifyCurrentCodexAccountRejectsExternalAPIKeyReplacement(t *testing.T) {
	fixture := newAPIKeySwitchFixture(t)
	fixture.manager.factory = &fakeCodexAccountFactory{open: func(account ports.CodexAccountContext) (ports.CodexAccountClient, error) {
		if account.Home != fixture.manager.globalHome || account.Managed {
			t.Fatalf("verification context = %#v", account)
		}
		return &fakeCodexAccountClient{readFn: func(context.Context, bool) (ports.CodexAccountObservation, error) {
			if err := writeGlobalCredentialAtomic(fixture.manager.globalCredentialPath(), []byte("external-api-key")); err != nil {
				t.Fatal(err)
			}
			return ports.CodexAccountObservation{Authentication: domain.AgentAuthenticationAuthorized, Method: domain.CodexAuthMethodAPIKey}, nil
		}}, nil
	}}

	err := fixture.service.VerifyCurrentCodexAccount(context.Background(), fixture.source.Snapshot.ID)
	if err == nil {
		t.Fatal("verification accepted an externally replaced API key")
	}
	sourceCredential, readErr := readOpaqueCredential(filepath.Join(fixture.source.Home, codexCredentialFilename))
	if readErr != nil || string(sourceCredential) != "source-api-key" {
		t.Fatalf("source slot overwritten: %q, err=%v", sourceCredential, readErr)
	}
}

func TestRestoreCodexAccountCredentialRejectsExternalAPIKeyReplacement(t *testing.T) {
	fixture := newAPIKeySwitchFixture(t)
	if err := writeGlobalCredentialAtomic(fixture.manager.globalCredentialPath(), []byte("target-api-key")); err != nil {
		t.Fatal(err)
	}
	fixture.manager.factory = &fakeCodexAccountFactory{open: func(account ports.CodexAccountContext) (ports.CodexAccountClient, error) {
		if account.Home != fixture.manager.globalHome || account.Managed {
			t.Fatalf("restore context = %#v", account)
		}
		return &fakeCodexAccountClient{readFn: func(context.Context, bool) (ports.CodexAccountObservation, error) {
			if err := writeGlobalCredentialAtomic(fixture.manager.globalCredentialPath(), []byte("external-api-key")); err != nil {
				t.Fatal(err)
			}
			return ports.CodexAccountObservation{Authentication: domain.AgentAuthenticationAuthorized, Method: domain.CodexAuthMethodAPIKey}, nil
		}}, nil
	}}

	err := fixture.service.RestoreCodexAccountCredential(context.Background(), fixture.source.Snapshot.ID, fixture.target.Snapshot.ID)
	if err == nil {
		t.Fatal("restore accepted an externally replaced API key")
	}
	sourceCredential, readErr := readOpaqueCredential(filepath.Join(fixture.source.Home, codexCredentialFilename))
	if readErr != nil || string(sourceCredential) != "source-api-key" {
		t.Fatalf("source slot overwritten: %q, err=%v", sourceCredential, readErr)
	}
	globalCredential, readErr := readOpaqueCredential(fixture.manager.globalCredentialPath())
	if readErr != nil || string(globalCredential) != "external-api-key" {
		t.Fatalf("external global credential overwritten: %q, err=%v", globalCredential, readErr)
	}
}

func TestCredentialActivationDoesNotOverwriteExternalRace(t *testing.T) {
	root := t.TempDir()
	globalHome := filepath.Join(root, "global-codex")
	if err := ensurePrivateDirectory(globalHome); err != nil {
		t.Fatal(err)
	}
	globalPath := filepath.Join(globalHome, codexCredentialFilename)
	if err := writePrivateFileAtomic(globalPath, []byte("source-credential")); err != nil {
		t.Fatal(err)
	}
	sourceEmail, targetEmail := "source@example.com", "target@example.com"
	state := &fakeCodexAccountStateStore{active: domain.CodexActiveAccount{AccountID: testAccountID, Revision: 1}, found: true}
	manager := newCodexAccountManager(context.Background(), filepath.Join(root, "accounts"), filepath.Join(root, "pending"), filepath.Join(root, "staging"), globalHome, nil, state, nil)
	accountIDs := []string{testAccountID, "bb1e9a5d-37ad-43f8-83bd-13de8168f8af"}
	manager.catalog.newID = func() string {
		id := accountIDs[0]
		accountIDs = accountIDs[1:]
		return id
	}
	source := commitTestAccount(t, manager.catalog, manager.pendingRoot, "b60a377d-da68-4a61-86f2-f31f04c571f2", ports.CodexAccountObservation{Authentication: domain.AgentAuthenticationAuthorized, Method: domain.CodexAuthMethodChatGPT, Email: &sourceEmail})
	target := commitTestAccount(t, manager.catalog, manager.pendingRoot, "1c5de3ab-82d0-4a68-a06b-8495cdeab909", ports.CodexAccountObservation{Authentication: domain.AgentAuthenticationAuthorized, Method: domain.CodexAuthMethodChatGPT, Email: &targetEmail})
	if err := writePrivateFileAtomic(filepath.Join(source.Home, codexCredentialFilename), []byte("source-credential")); err != nil {
		t.Fatal(err)
	}
	if err := writePrivateFileAtomic(filepath.Join(target.Home, codexCredentialFilename), []byte("target-credential")); err != nil {
		t.Fatal(err)
	}
	manager.active = state.active
	manager.factory = &fakeCodexAccountFactory{open: func(account ports.CodexAccountContext) (ports.CodexAccountClient, error) {
		if account.Home != globalHome || account.Managed {
			t.Fatalf("activation context = %#v", account)
		}
		if err := writeGlobalCredentialAtomic(globalPath, []byte("external-credential")); err != nil {
			t.Fatal(err)
		}
		return &fakeCodexAccountClient{read: ports.CodexAccountObservation{Authentication: domain.AgentAuthenticationUnauthorized}}, nil
	}}
	_, err := manager.activateFromCredentialLocked(context.Background(), target.Snapshot.ID, 1, filepath.Join(target.Home, codexCredentialFilename), []byte("source-credential"))
	if !errors.Is(err, ports.ErrCodexGlobalAccountChanged) {
		t.Fatalf("activation error = %v, want global-account-changed", err)
	}
	current, readErr := readOpaqueCredential(globalPath)
	if readErr != nil || string(current) != "external-credential" {
		t.Fatalf("external credential was overwritten: %q, err=%v", current, readErr)
	}
	if state.active.AccountID != source.Snapshot.ID || state.active.Revision != 1 {
		t.Fatalf("active pointer changed during race: %#v", state.active)
	}
}

func TestCredentialActivationVerifiesWithoutASecondProactiveRefresh(t *testing.T) {
	root := t.TempDir()
	globalHome := filepath.Join(root, "global-codex")
	if err := ensurePrivateDirectory(globalHome); err != nil {
		t.Fatal(err)
	}
	globalPath := filepath.Join(globalHome, codexCredentialFilename)
	if err := writeGlobalCredentialAtomic(globalPath, []byte("source-credential")); err != nil {
		t.Fatal(err)
	}
	sourceEmail, targetEmail := "source@example.com", "target@example.com"
	state := &fakeCodexAccountStateStore{active: domain.CodexActiveAccount{AccountID: testAccountID, Revision: 1}, found: true}
	manager := newCodexAccountManager(context.Background(), filepath.Join(root, "accounts"), filepath.Join(root, "pending"), filepath.Join(root, "staging"), globalHome, nil, state, nil)
	accountIDs := []string{testAccountID, "bb1e9a5d-37ad-43f8-83bd-13de8168f8af"}
	manager.catalog.newID = func() string {
		id := accountIDs[0]
		accountIDs = accountIDs[1:]
		return id
	}
	source := commitTestAccount(t, manager.catalog, manager.pendingRoot, "b60a377d-da68-4a61-86f2-f31f04c571f2", ports.CodexAccountObservation{Authentication: domain.AgentAuthenticationAuthorized, Method: domain.CodexAuthMethodChatGPT, Email: &sourceEmail})
	target := commitTestAccount(t, manager.catalog, manager.pendingRoot, "1c5de3ab-82d0-4a68-a06b-8495cdeab909", ports.CodexAccountObservation{Authentication: domain.AgentAuthenticationAuthorized, Method: domain.CodexAuthMethodChatGPT, Email: &targetEmail})
	if err := writePrivateFileAtomic(filepath.Join(source.Home, codexCredentialFilename), []byte("source-credential")); err != nil {
		t.Fatal(err)
	}
	if err := writePrivateFileAtomic(filepath.Join(target.Home, codexCredentialFilename), []byte("target-credential")); err != nil {
		t.Fatal(err)
	}
	manager.active = state.active
	var refreshRequests []bool
	manager.factory = &fakeCodexAccountFactory{open: func(account ports.CodexAccountContext) (ports.CodexAccountClient, error) {
		if account.Home != globalHome || account.Managed {
			t.Fatalf("activation context = %#v", account)
		}
		return &fakeCodexAccountClient{readFn: func(_ context.Context, refresh bool) (ports.CodexAccountObservation, error) {
			refreshRequests = append(refreshRequests, refresh)
			return ports.CodexAccountObservation{Authentication: domain.AgentAuthenticationAuthorized, Method: domain.CodexAuthMethodChatGPT, Email: &targetEmail}, nil
		}}, nil
	}}
	active, err := manager.activateFromCredentialLocked(context.Background(), target.Snapshot.ID, 1, filepath.Join(target.Home, codexCredentialFilename), []byte("source-credential"))
	if err != nil {
		t.Fatal(err)
	}
	if active.AccountID != target.Snapshot.ID || active.Revision != 2 {
		t.Fatalf("active account = %#v", active)
	}
	if !slices.Equal(refreshRequests, []bool{false}) {
		t.Fatalf("activation refresh requests = %#v, want one non-refreshing verification", refreshRequests)
	}
}

func TestCredentialActivationAdoptsPointerCommitReportedAsError(t *testing.T) {
	fixture := newAPIKeySwitchFixture(t)
	state := &committedErrorCodexAccountStateStore{
		active: fixture.state.active,
		err:    errors.New("injected post-commit failure"),
	}
	fixture.manager.stateStore = state
	fixture.manager.factory = &fakeCodexAccountFactory{open: func(ports.CodexAccountContext) (ports.CodexAccountClient, error) {
		return &fakeCodexAccountClient{read: ports.CodexAccountObservation{
			Authentication: domain.AgentAuthenticationAuthorized,
			Method:         domain.CodexAuthMethodAPIKey,
		}}, nil
	}}

	active, err := fixture.manager.activateFromCredentialLocked(
		context.Background(), fixture.target.Snapshot.ID, 1,
		filepath.Join(fixture.target.Home, codexCredentialFilename), []byte("source-api-key"),
	)
	if err != nil {
		t.Fatalf("activation rejected a confirmed pointer commit: %v", err)
	}
	if active.AccountID != fixture.target.Snapshot.ID || fixture.manager.activeAccountID() != fixture.target.Snapshot.ID {
		t.Fatalf("active account = %#v, manager=%q", active, fixture.manager.activeAccountID())
	}
	global, readErr := readOpaqueCredential(fixture.manager.globalCredentialPath())
	if readErr != nil || string(global) != "target-api-key" {
		t.Fatalf("target credential was rolled back after pointer commit: %q, %v", global, readErr)
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
		_, err := manager.ensureAuthentication(waitCtx, record, domain.AgentReadinessPurposeDisplay, false)
		done <- err
	}()
	<-started
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("wait error = %v", err)
	}
	close(release)
	deadline := time.After(time.Second)
	for {
		latest, _ := manager.catalog.record(record.Snapshot.ID)
		if latest.Snapshot.Authentication.State == domain.AgentAuthenticationAuthorized {
			break
		}
		select {
		case <-deadline:
			t.Fatal("shared authentication read did not finish")
		default:
			time.Sleep(time.Millisecond)
		}
	}
}
