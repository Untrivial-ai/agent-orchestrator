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
	childExited     bool
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

func (f *fakeCodexLoginTerminal) IsShellTerminalChildAlive(_ context.Context, _ string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.childExited {
		return false, nil
	}
	// Existing tests explicitly drive verification and use the default fake.
	// Keep its command alive unless a test opts into the exit path.
	return true, nil
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
	root := t.TempDir()
	return newCodexAccountManager(context.Background(),
		filepath.Join(root, "accounts"), filepath.Join(root, "pending-accounts"),
		filepath.Join(root, "switch-staging"), filepath.Join(root, "device-home"),
		factory, state, nil)
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

func TestStaleActivePointerUsesSavedHomeDuringTemporaryReconciliationFailure(t *testing.T) {
	manager := newTestCodexAccountManager(t, nil, nil)
	manager.catalog.newID = func() string { return testAccountID }
	record := commitTestAccount(t, manager.catalog, manager.pendingRoot, "b60a377d-da68-4a61-86f2-f31f04c571f2", ports.CodexAccountObservation{
		Authentication: domain.AgentAuthenticationAuthorized,
		Method:         domain.CodexAuthMethodAPIKey,
	})
	manager.mu.Lock()
	manager.active = domain.CodexActiveAccount{AccountID: record.Snapshot.ID, Revision: 2}
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
	manager.active = domain.CodexActiveAccount{AccountID: record.Snapshot.ID, Revision: 2}
	manager.deviceAccountID = record.Snapshot.ID
	manager.deferredAccountID = record.Snapshot.ID
	manager.reconciliation = domain.CodexDeviceReconciliation{
		Status:                domain.CodexDeviceReconciliationChecking,
		ActiveAccountVerified: false,
		ReasonCode:            "checking",
	}
	manager.mu.Unlock()

	view := manager.cached()
	if view.ActiveAccountID != record.Snapshot.ID || len(view.Accounts) != 2 || view.Accounts[0].ID != record.Snapshot.ID || !view.Accounts[0].Active || view.Accounts[1].ID != inactiveID || view.Accounts[1].Active {
		t.Fatalf("checking reconciliation hid the last matched account: %#v", view)
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
	started, err := manager.openLoginTerminal(context.Background(), "", false)
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
	started, err := manager.openLoginTerminal(context.Background(), "", false)
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
	started, err := manager.openLoginTerminal(context.Background(), "", false)
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

func TestNativeLoginAutomaticallyVerifiesWhenCommandExits(t *testing.T) {
	email := "automatic@example.com"
	client := &fakeCodexAccountClient{
		read: ports.CodexAccountObservation{
			Authentication: domain.AgentAuthenticationAuthorized,
			Method:         domain.CodexAuthMethodChatGPT,
			Email:          &email,
		},
	}
	factory := &fakeCodexAccountFactory{
		capabilities: supportedCodexAccountCapabilities(),
		open: func(ports.CodexAccountContext) (ports.CodexAccountClient, error) {
			return client, nil
		},
	}
	state := &fakeCodexAccountStateStore{}
	manager := newTestCodexAccountManager(t, factory, state)
	manager.globalAuth = accountAuthenticationObservation(time.Now().UTC(), domain.AgentAuthenticationUnauthorized)
	ids := []string{"b60a377d-da68-4a61-86f2-f31f04c571f2", testAccountID}
	index := 0
	manager.newID = func() string { id := ids[index]; index++; return id }
	manager.catalog.newID = func() string { return testAccountID }
	manager.executable = func() (string, error) { return "/ao", nil }
	terminal := &fakeCodexLoginTerminal{
		writeCredential: true,
		childExited:     true,
		result:          shellterm.ShellTerminal{HandleID: "shellterm-login-auto", Title: "Add Codex account"},
	}
	manager.terminal = terminal

	started, err := manager.openLoginTerminal(context.Background(), "", false)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		manager.mu.Lock()
		operation := manager.login.snapshot
		manager.mu.Unlock()
		if operation.Status == domain.CodexAccountLoginCompleted {
			if operation.Account == nil || operation.Account.Label != email {
				t.Fatalf("completed login = %#v", operation)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("automatic login verification did not complete: %#v", operation)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if active := manager.activeAccountID(); active != testAccountID {
		t.Fatalf("active account = %q", active)
	}
	terminal.mu.Lock()
	defer terminal.mu.Unlock()
	if len(terminal.closed) != 1 || terminal.closed[0] != started.ShellTerminal.HandleID {
		t.Fatalf("closed terminals = %#v", terminal.closed)
	}
}

func TestNativeLoginVerificationDoesNotReplaceExistingDeviceAccount(t *testing.T) {
	email := "saved@example.com"
	client := &fakeCodexAccountClient{read: ports.CodexAccountObservation{Authentication: domain.AgentAuthenticationAuthorized, Method: domain.CodexAuthMethodChatGPT, Email: &email}}
	factory := &fakeCodexAccountFactory{capabilities: supportedCodexAccountCapabilities(), open: func(ports.CodexAccountContext) (ports.CodexAccountClient, error) { return client, nil }}
	state := &fakeCodexAccountStateStore{}
	manager := newTestCodexAccountManager(t, factory, state)
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

	started, err := manager.openLoginTerminal(context.Background(), "", false)
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
	if state.found || manager.activeAccountID() != "" {
		t.Fatalf("saved account became active: %#v", state.active)
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
	state := &fakeCodexAccountStateStore{}
	manager := newTestCodexAccountManager(t, factory, state)
	manager.mu.Lock()
	manager.reconciliation = domain.CodexDeviceReconciliation{
		Status:     domain.CodexDeviceReconciliationTemporarilyUnavailable,
		ReasonCode: "account_read_inconclusive", Retryable: true,
	}
	manager.globalAuth = failedAuthentication(time.Now().UTC(), domain.AgentReadinessReasonAuthCheckInconclusive, "Authentication check was inconclusive.")
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

	started, err := manager.openLoginTerminal(context.Background(), "", false)
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
	if active := manager.activeAccountID(); active != testAccountID || !state.found {
		t.Fatalf("first saved account was not activated: active=%q durable=%#v", active, state.active)
	}
	if _, err := readOpaqueCredential(filepath.Join(manager.catalog.root, testAccountID, codexCredentialHomeDirectory, codexCredentialFilename)); err != nil {
		t.Fatalf("saved account credential: %v", err)
	}
	if credential, err := readOpaqueCredential(manager.globalCredentialPath()); err != nil || string(credential) != "opaque-login-credential" {
		t.Fatalf("first saved account was not installed on the device: %q, %v", credential, err)
	}
}

func TestDeviceReloginAtomicallyReplacesUnmanagedCredential(t *testing.T) {
	email := "person@example.com"
	observation := ports.CodexAccountObservation{Authentication: domain.AgentAuthenticationAuthorized, Method: domain.CodexAuthMethodChatGPT, Email: &email}
	factory := &fakeCodexAccountFactory{capabilities: supportedCodexAccountCapabilities(), open: func(ports.CodexAccountContext) (ports.CodexAccountClient, error) {
		return &fakeCodexAccountClient{read: observation}, nil
	}}
	state := &fakeCodexAccountStateStore{}
	manager := newTestCodexAccountManager(t, factory, state)
	if err := ensurePrivateDirectory(manager.globalHome); err != nil {
		t.Fatal(err)
	}
	original := []byte("unmanaged-device-credential")
	if err := writeGlobalCredentialAtomic(manager.globalCredentialPath(), original); err != nil {
		t.Fatal(err)
	}
	manager.newID = func() string { return "b60a377d-da68-4a61-86f2-f31f04c571f2" }
	manager.catalog.newID = func() string { return testAccountID }
	manager.executable = func() (string, error) { return "/ao", nil }
	manager.terminal = &fakeCodexLoginTerminal{writeCredential: true, result: shellterm.ShellTerminal{HandleID: "device-login"}}

	started, err := manager.openLoginTerminal(context.Background(), "", true)
	if err != nil {
		t.Fatal(err)
	}
	completed, err := manager.verifyLogin(context.Background(), started.Operation.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status != domain.CodexAccountLoginCompleted || completed.AccountID != testAccountID || completed.Account == nil || !completed.Account.Active {
		t.Fatalf("device login result = %#v", completed)
	}
	installed, err := readOpaqueCredential(manager.globalCredentialPath())
	if err != nil || string(installed) != "opaque-login-credential" {
		t.Fatalf("installed credential = %q, err=%v", installed, err)
	}
	if state.active.AccountID != testAccountID || state.active.Revision != 1 {
		t.Fatalf("active pointer = %#v", state.active)
	}
}

func TestDeviceReloginFailurePreservesCredentialAndCreatesNoAccount(t *testing.T) {
	email := "person@example.com"
	observation := ports.CodexAccountObservation{Authentication: domain.AgentAuthenticationAuthorized, Method: domain.CodexAuthMethodChatGPT, Email: &email}
	factory := &fakeCodexAccountFactory{capabilities: supportedCodexAccountCapabilities(), open: func(account ports.CodexAccountContext) (ports.CodexAccountClient, error) {
		if !account.Managed {
			return nil, errors.New("global verification unavailable")
		}
		return &fakeCodexAccountClient{read: observation}, nil
	}}
	manager := newTestCodexAccountManager(t, factory, &fakeCodexAccountStateStore{})
	if err := ensurePrivateDirectory(manager.globalHome); err != nil {
		t.Fatal(err)
	}
	original := []byte("unmanaged-device-credential")
	if err := writeGlobalCredentialAtomic(manager.globalCredentialPath(), original); err != nil {
		t.Fatal(err)
	}
	manager.newID = func() string { return "b60a377d-da68-4a61-86f2-f31f04c571f2" }
	manager.catalog.newID = func() string { return testAccountID }
	manager.executable = func() (string, error) { return "/ao", nil }
	manager.terminal = &fakeCodexLoginTerminal{writeCredential: true, result: shellterm.ShellTerminal{HandleID: "device-login"}}

	started, err := manager.openLoginTerminal(context.Background(), "", true)
	if err != nil {
		t.Fatal(err)
	}
	completed, err := manager.verifyLogin(context.Background(), started.Operation.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status != domain.CodexAccountLoginFailed {
		t.Fatalf("device login result = %#v", completed)
	}
	installed, err := readOpaqueCredential(manager.globalCredentialPath())
	if err != nil || !bytes.Equal(installed, original) {
		t.Fatalf("failed login changed device credential = %q, err=%v", installed, err)
	}
	if snapshots := manager.catalog.snapshots(); len(snapshots) != 0 {
		t.Fatalf("failed device login left a saved account = %#v", snapshots)
	}
}

func TestDeviceReloginCancellationPreservesCredential(t *testing.T) {
	manager := newTestCodexAccountManager(t, nil, nil)
	if err := ensurePrivateDirectory(manager.globalHome); err != nil {
		t.Fatal(err)
	}
	original := []byte("unmanaged-device-credential")
	if err := writeGlobalCredentialAtomic(manager.globalCredentialPath(), original); err != nil {
		t.Fatal(err)
	}
	manager.newID = func() string { return "b60a377d-da68-4a61-86f2-f31f04c571f2" }
	manager.executable = func() (string, error) { return "/ao", nil }
	manager.terminal = &fakeCodexLoginTerminal{result: shellterm.ShellTerminal{HandleID: "device-login"}}
	started, err := manager.openLoginTerminal(context.Background(), "", true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.cancelLogin(context.Background(), started.Operation.OperationID); err != nil {
		t.Fatal(err)
	}
	installed, err := readOpaqueCredential(manager.globalCredentialPath())
	if err != nil || !bytes.Equal(installed, original) {
		t.Fatalf("cancelled login changed device credential = %q, err=%v", installed, err)
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
	record := commitTestAccount(t, manager.catalog, manager.pendingRoot, "b60a377d-da68-4a61-86f2-f31f04c571f2", observation)
	manager.requireReauthentication(record.Snapshot.ID)
	if before := manager.cached().Accounts[0].Capacity; before.ReasonCode != domain.CodexCapacityReasonSkippedSignedOut {
		t.Fatalf("capacity before reauthentication = %#v", before)
	}
	manager.newID = func() string { return "1c5de3ab-82d0-4a68-a06b-8495cdeab909" }
	manager.executable = func() (string, error) { return "/ao", nil }
	manager.terminal = &fakeCodexLoginTerminal{
		writeCredential: true,
		result:          shellterm.ShellTerminal{HandleID: "shellterm-login-reauth", Title: "Sign in to Codex account"},
	}

	started, err := manager.openLoginTerminal(context.Background(), record.Snapshot.ID, false)
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
	if completed.Account.Authentication.State != domain.AgentAuthenticationAuthorized || completed.Account.Capacity.State != domain.CodexCapacityAvailable || completed.Account.Capacity.RemainingPercent == nil || *completed.Account.Capacity.RemainingPercent != 42 {
		t.Fatalf("completed reauthentication state = %#v", completed.Account)
	}
	if completed.Account.Capacity.ReasonCode == domain.CodexCapacityReasonSkippedSignedOut {
		t.Fatalf("completed reauthentication retained signed-out capacity: %#v", completed.Account.Capacity)
	}
	cached := manager.cached().Accounts[0]
	if cached.Authentication.State != domain.AgentAuthenticationAuthorized || cached.Capacity.State != domain.CodexCapacityAvailable || cached.Capacity.RemainingPercent == nil || *cached.Capacity.RemainingPercent != 42 {
		t.Fatalf("cached reauthentication state = %#v", cached)
	}
	if snapshots := manager.catalog.snapshots(); len(snapshots) != 1 || snapshots[0].ID != record.Snapshot.ID {
		t.Fatalf("reauthentication changed account identity: %#v", snapshots)
	}
	credential, err := readOpaqueCredential(filepath.Join(record.Home, codexCredentialFilename))
	if err != nil || string(credential) != "opaque-login-credential" {
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
	state := &fakeCodexAccountStateStore{
		active: domain.CodexActiveAccount{AccountID: testAccountID, Revision: 1}, found: true,
	}
	manager := newTestCodexAccountManager(t, factory, state)
	manager.catalog.newID = func() string { return testAccountID }
	record := commitTestAccount(t, manager.catalog, manager.pendingRoot, "b60a377d-da68-4a61-86f2-f31f04c571f2", observation)
	oldCredential, err := readOpaqueCredential(filepath.Join(record.Home, codexCredentialFilename))
	if err != nil {
		t.Fatal(err)
	}
	if err := writeGlobalCredentialAtomic(manager.globalCredentialPath(), oldCredential); err != nil {
		t.Fatal(err)
	}
	manager.mu.Lock()
	manager.active = state.active
	manager.markDeviceReconciledLocked(true, time.Now().UTC())
	manager.mu.Unlock()
	manager.newID = func() string { return "1c5de3ab-82d0-4a68-a06b-8495cdeab909" }
	manager.executable = func() (string, error) { return "/ao", nil }
	manager.terminal = &fakeCodexLoginTerminal{
		writeCredential: true,
		result:          shellterm.ShellTerminal{HandleID: "shellterm-login-reauth", Title: "Sign in to Codex account"},
	}

	started, err := manager.openLoginTerminal(context.Background(), record.Snapshot.ID, false)
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
	if err != nil || string(globalCredential) != "opaque-login-credential" {
		t.Fatalf("device credential = %q, err=%v", globalCredential, err)
	}
	if state.active.AccountID != testAccountID || state.active.Revision != 2 {
		t.Fatalf("durable active account = %#v", state.active)
	}
}

func TestActiveReauthenticationReportsInconclusiveDeviceVerification(t *testing.T) {
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
	state := &fakeCodexAccountStateStore{
		active: domain.CodexActiveAccount{AccountID: testAccountID, Revision: 1}, found: true,
	}
	manager := newTestCodexAccountManager(t, factory, state)
	manager.catalog.newID = func() string { return testAccountID }
	record := commitTestAccount(t, manager.catalog, manager.pendingRoot, "b60a377d-da68-4a61-86f2-f31f04c571f2", observation)
	oldCredential, err := readOpaqueCredential(filepath.Join(record.Home, codexCredentialFilename))
	if err != nil {
		t.Fatal(err)
	}
	if err := writeGlobalCredentialAtomic(manager.globalCredentialPath(), oldCredential); err != nil {
		t.Fatal(err)
	}
	manager.mu.Lock()
	manager.active = state.active
	manager.markDeviceReconciledLocked(true, time.Now().UTC())
	manager.mu.Unlock()
	manager.newID = func() string { return "1c5de3ab-82d0-4a68-a06b-8495cdeab909" }
	manager.executable = func() (string, error) { return "/ao", nil }
	manager.terminal = &fakeCodexLoginTerminal{
		writeCredential: true,
		result:          shellterm.ShellTerminal{HandleID: "shellterm-login-reauth", Title: "Sign in to Codex account"},
	}

	started, err := manager.openLoginTerminal(context.Background(), record.Snapshot.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	completed, err := manager.verifyLogin(context.Background(), started.Operation.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status != domain.CodexAccountLoginFailed {
		t.Fatalf("completed reauthentication = %#v", completed)
	}
	if completed.Reason != "The device Codex account could not be confirmed. Try again." {
		t.Fatalf("failure reason = %q", completed.Reason)
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
	started, err := manager.openLoginTerminal(context.Background(), "", false)
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
	state := &fakeCodexAccountStateStore{}
	manager := newCodexAccountManager(context.Background(), filepath.Join(root, "accounts"), filepath.Join(root, "pending"), filepath.Join(root, "staging"), device, nil, state, nil)
	manager.catalog.newID = func() string { return testAccountID }
	if err := manager.waitAccountStore(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := manager.reconcileGlobal(context.Background()); err != nil {
		t.Fatal(err)
	}
	view := manager.cached()
	if view.ActiveAccountID != testAccountID || len(view.Accounts) != 1 || !view.Accounts[0].Active || view.UnmanagedGlobalAccount != nil {
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

	if err := manager.initializeAccountStore(); err != nil {
		t.Fatal(err)
	}
	if err := manager.reconcileGlobal(context.Background()); err != nil {
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
	state := &fakeCodexAccountStateStore{
		active: domain.CodexActiveAccount{AccountID: testAccountID, Revision: 7},
		found:  true,
	}
	manager := newCodexAccountManager(context.Background(), filepath.Join(root, "accounts"), filepath.Join(root, "pending"), filepath.Join(root, "staging"), globalHome, nil, state, nil)
	manager.catalog.newID = func() string { return testAccountID }
	record := commitTestAccountWithCredential(t, manager.catalog, manager.pendingRoot, "b60a377d-da68-4a61-86f2-f31f04c571f2", testOAuthCredential("provider-account-a", "old-access"), ports.CodexAccountObservation{
		Authentication: domain.AgentAuthenticationAuthorized,
		Method:         domain.CodexAuthMethodChatGPT,
		Email:          &email,
	})
	manager.active = state.active
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
	if state.active.AccountID != testAccountID || state.active.Revision != 7 {
		t.Fatalf("durable active account changed = %#v", state.active)
	}
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
	state := &fakeCodexAccountStateStore{active: domain.CodexActiveAccount{AccountID: testAccountID, Revision: 7}, found: true}
	manager := newCodexAccountManager(context.Background(), filepath.Join(root, "accounts"), filepath.Join(root, "pending"), filepath.Join(root, "staging"), globalHome, nil, state, nil)
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
	manager.active = state.active
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
	state := &fakeCodexAccountStateStore{active: domain.CodexActiveAccount{AccountID: testAccountID, Revision: 3}, found: true}
	manager := newCodexAccountManager(context.Background(), filepath.Join(root, "accounts"), filepath.Join(root, "pending"), filepath.Join(root, "staging"), globalHome, nil, state, nil)
	accountBID := "bb1e9a5d-37ad-43f8-83bd-13de8168f8af"
	ids := []string{testAccountID, accountBID}
	manager.catalog.newID = func() string { id := ids[0]; ids = ids[1:]; return id }
	aEmail := "account-a@example.com"
	credentialA := testOAuthCredential("provider-a", "access-a")
	recordA := commitTestAccountWithCredential(t, manager.catalog, manager.pendingRoot, "b60a377d-da68-4a61-86f2-f31f04c571f2", credentialA, ports.CodexAccountObservation{
		Authentication: domain.AgentAuthenticationAuthorized, Method: domain.CodexAuthMethodChatGPT, Email: &aEmail,
	})
	manager.active = state.active
	manager.factory = &fakeCodexAccountFactory{open: func(ports.CodexAccountContext) (ports.CodexAccountClient, error) {
		t.Fatal("local reconciliation opened Codex")
		return nil, nil
	}}

	if err := manager.reconcileGlobal(context.Background()); err != nil {
		t.Fatal(err)
	}
	view := manager.cached()
	if view.ActiveAccountID != accountBID || len(view.Accounts) != 2 || view.UnmanagedGlobalAccount != nil {
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

func TestGlobalReconciliationMissingGlobalClearsActivePointerWithoutRestoringSavedAccount(t *testing.T) {
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
	manager.catalog.newID = func() string { return testAccountID }
	record := commitTestAccount(t, manager.catalog, manager.pendingRoot, "b60a377d-da68-4a61-86f2-f31f04c571f2", ports.CodexAccountObservation{
		Authentication: domain.AgentAuthenticationAuthorized,
		Method:         domain.CodexAuthMethodChatGPT,
	})
	savedCredential, err := readOpaqueCredential(filepath.Join(record.Home, codexCredentialFilename))
	if err != nil {
		t.Fatal(err)
	}
	manager.active = state.active
	manager.factory = &fakeCodexAccountFactory{open: func(ports.CodexAccountContext) (ports.CodexAccountClient, error) {
		t.Fatal("missing-global reconciliation opened Codex")
		return nil, nil
	}}

	if err := manager.reconcileGlobal(context.Background()); err != nil {
		t.Fatal(err)
	}
	if state.active.AccountID != "" || state.active.Revision != 5 {
		t.Fatalf("missing device credential did not clear active pointer = %#v", state.active)
	}
	view := manager.cached()
	if view.ActiveAccountID != "" || len(view.Accounts) != 1 || view.Accounts[0].Active {
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
	state := &fakeCodexAccountStateStore{
		active: domain.CodexActiveAccount{AccountID: testAccountID, Revision: 1},
		found:  true,
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
	manager := newCodexAccountManager(context.Background(), filepath.Join(root, "accounts"), filepath.Join(root, "pending"), filepath.Join(root, "staging"), globalHome, factory, state, nil)
	manager.catalog.newID = func() string { return testAccountID }
	record := commitTestAccountWithCredential(t, manager.catalog, manager.pendingRoot, "b60a377d-da68-4a61-86f2-f31f04c571f2", credential, ports.CodexAccountObservation{
		Authentication: domain.AgentAuthenticationAuthorized,
		Method:         domain.CodexAuthMethodChatGPT,
		Email:          &email,
	})
	if err := writeGlobalCredentialAtomic(manager.globalCredentialPath(), credential); err != nil {
		t.Fatal(err)
	}
	manager.active = state.active
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

	view, err := service.EnsureCodexAccounts(context.Background(), nil, false, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if view.ActiveAccountID != "" || len(view.Accounts) != 1 || view.Accounts[0].Active {
		t.Fatalf("removed global credential remained active: %#v", view)
	}
	if view.Accounts[0].Authentication.State != domain.AgentAuthenticationAuthorized {
		t.Fatalf("saved credential was not checked independently: %#v", view.Accounts[0].Authentication)
	}
	if state.active.AccountID != "" || state.active.Revision != 2 {
		t.Fatalf("durable active pointer was not cleared: %#v", state.active)
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

func TestEnsureCodexAccountsRetriesSavedAccountWhenGlobalCredentialDisappearsDuringCheck(t *testing.T) {
	root := t.TempDir()
	globalHome := filepath.Join(root, "global-codex")
	if err := ensurePrivateDirectory(globalHome); err != nil {
		t.Fatal(err)
	}
	credential := testOAuthCredential("provider-account", "access-token")
	state := &fakeCodexAccountStateStore{
		active: domain.CodexActiveAccount{AccountID: testAccountID, Revision: 1},
		found:  true,
	}
	email := "saved@example.com"
	removedGlobal := false
	var opened []ports.CodexAccountContext
	manager := newCodexAccountManager(context.Background(), filepath.Join(root, "accounts"), filepath.Join(root, "pending"), filepath.Join(root, "staging"), globalHome, nil, state, nil)
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
	manager.active = state.active
	manager.accountStoreReady = true
	readiness := newReadinessCoordinator(readinessCoordinatorConfig{
		Agents: []agentregistry.HarnessAgent{harnessAgent(string(domain.HarnessCodex), "Codex", nil)},
	})
	service := &Service{codexAccounts: manager, readiness: readiness}

	view, err := service.EnsureCodexAccounts(context.Background(), nil, false, false, false)
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
	var failure *codexDeviceReconciliationFailure
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
	state := &fakeCodexAccountStateStore{}
	manager := newCodexAccountManager(context.Background(), filepath.Join(root, "accounts"), filepath.Join(root, "pending"), filepath.Join(root, "staging"), globalHome, nil, state, nil)
	manager.catalog.newID = func() string { return testAccountID }

	if err := manager.initializeAccountStore(); err != nil {
		t.Fatal(err)
	}
	if err := manager.reconcileGlobal(context.Background()); err != nil {
		t.Fatal(err)
	}
	first := manager.cached()
	if first.ActiveAccountID != testAccountID || len(first.Accounts) != 1 || !first.Accounts[0].Active {
		t.Fatalf("first imported account = %#v, state=%#v", first, state.active)
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
		t.Fatalf("rotated account was not reused: %#v state=%#v", second, state.active)
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
	manager := newCodexAccountManager(context.Background(), filepath.Join(root, "accounts"), filepath.Join(root, "pending"), filepath.Join(root, "staging"), globalHome, nil, &fakeCodexAccountStateStore{}, nil)
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
	state := &fakeCodexAccountStateStore{}
	manager := newCodexAccountManager(context.Background(), filepath.Join(root, "accounts"), filepath.Join(root, "pending"), filepath.Join(root, "staging"), globalHome, factory, state, nil)
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
	if view.ActiveAccountID != testAccountID || len(view.Accounts) != 1 || view.Accounts[0].Status != domain.CodexAccountStatusValid || !view.Accounts[0].Active || view.UnmanagedGlobalAccount != nil {
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
	if view.ActiveAccountID != testAccountID || len(view.Accounts) != 1 || !view.Accounts[0].Active || view.UnmanagedGlobalAccount != nil {
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

	if err := fixture.service.VerifyCodexAccountForSwitch(context.Background(), fixture.target.Snapshot.ID); err != nil {
		t.Fatalf("VerifyCodexAccountForSwitch: %v", err)
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
	if _, err := fixture.service.CheckpointAndActivateCodexAccount(context.Background(), domain.CodexAccountSwitchSourceManaged, "6f8dfc76-8db4-4621-8974-c480093e0d55", fixture.target.Snapshot.ID, 1); err == nil {
		t.Fatal("checkpoint accepted an externally replaced source API-key credential")
	}
	saved, err := readOpaqueCredential(filepath.Join(fixture.source.Home, codexCredentialFilename))
	if err != nil || string(saved) != "source-api-key" {
		t.Fatalf("source slot was overwritten = %q, %v", saved, err)
	}
}

func TestSwitchFromDeviceOnlySourceRestoresPrivateCheckpoint(t *testing.T) {
	fixture := newAPIKeySwitchFixture(t)
	switchID := "6f8dfc76-8db4-4621-8974-c480093e0d55"
	deviceCredential := []byte("external-device-credential")
	if err := writeGlobalCredentialAtomic(fixture.manager.globalCredentialPath(), deviceCredential); err != nil {
		t.Fatal(err)
	}
	fixture.manager.factory = &fakeCodexAccountFactory{open: func(account ports.CodexAccountContext) (ports.CodexAccountClient, error) {
		return &fakeCodexAccountClient{read: ports.CodexAccountObservation{Authentication: domain.AgentAuthenticationAuthorized, Method: domain.CodexAuthMethodAPIKey}}, nil
	}}

	active, err := fixture.service.CheckpointAndActivateCodexAccount(context.Background(), domain.CodexAccountSwitchSourceDevice, switchID, fixture.target.Snapshot.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if active.AccountID != fixture.target.Snapshot.ID || active.Revision != 2 {
		t.Fatalf("activated account = %#v", active)
	}
	checkpoint, err := readOpaqueCredential(filepath.Join(fixture.manager.switchStagingRoot, switchID, "source-auth.json"))
	if err != nil || !bytes.Equal(checkpoint, deviceCredential) {
		t.Fatalf("device checkpoint = %q, err=%v", checkpoint, err)
	}
	if err := fixture.service.RestoreCodexAccountCredential(context.Background(), switchID, domain.CodexAccountSwitchSourceDevice, "", fixture.target.Snapshot.ID); err != nil {
		t.Fatal(err)
	}
	restored, err := readOpaqueCredential(fixture.manager.globalCredentialPath())
	if err != nil || !bytes.Equal(restored, deviceCredential) {
		t.Fatalf("restored device credential = %q, err=%v", restored, err)
	}
	if fixture.manager.active.AccountID != "" || fixture.manager.active.Revision != 3 || fixture.manager.unmanaged == nil {
		t.Fatalf("restored device-only state = active %#v unmanaged %#v", fixture.manager.active, fixture.manager.unmanaged)
	}
	if err := fixture.service.CleanupCodexAccountSwitch(context.Background(), switchID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(fixture.manager.switchStagingRoot, switchID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("terminal switch checkpoint remains: %v", err)
	}
}

func TestSwitchFromNoDeviceCredentialCanRollbackToNoActiveAccount(t *testing.T) {
	root := t.TempDir()
	globalHome := filepath.Join(root, "global-codex")
	if err := ensurePrivateDirectory(globalHome); err != nil {
		t.Fatal(err)
	}
	state := &fakeCodexAccountStateStore{}
	manager := newCodexAccountManager(context.Background(), filepath.Join(root, "accounts"), filepath.Join(root, "pending"), filepath.Join(root, "staging"), globalHome, nil, state, nil)
	manager.catalog.newID = func() string { return testAccountID }
	target := commitTestAccount(t, manager.catalog, manager.pendingRoot, "b60a377d-da68-4a61-86f2-f31f04c571f2", ports.CodexAccountObservation{Authentication: domain.AgentAuthenticationAuthorized, Method: domain.CodexAuthMethodAPIKey})
	if err := writePrivateFileAtomic(filepath.Join(target.Home, codexCredentialFilename), []byte("target-credential")); err != nil {
		t.Fatal(err)
	}
	manager.factory = &fakeCodexAccountFactory{open: func(ports.CodexAccountContext) (ports.CodexAccountClient, error) {
		return &fakeCodexAccountClient{read: ports.CodexAccountObservation{Authentication: domain.AgentAuthenticationAuthorized, Method: domain.CodexAuthMethodAPIKey}}, nil
	}}
	service := &Service{codexAccounts: manager, readiness: newReadinessCoordinator(readinessCoordinatorConfig{})}
	switchID := "6f8dfc76-8db4-4621-8974-c480093e0d55"

	active, err := service.CheckpointAndActivateCodexAccount(context.Background(), domain.CodexAccountSwitchSourceNone, switchID, target.Snapshot.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if active.AccountID != target.Snapshot.ID || active.Revision != 1 {
		t.Fatalf("activated account = %#v", active)
	}
	if err := service.RestoreCodexAccountCredential(context.Background(), switchID, domain.CodexAccountSwitchSourceNone, "", target.Snapshot.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(manager.globalCredentialPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rollback did not restore missing credential: %v", err)
	}
	if manager.active.AccountID != "" || manager.active.Revision != 2 || manager.deviceCredentialPresent {
		t.Fatalf("rollback did not restore no-active state: %#v", manager.active)
	}
}

func TestDeviceOnlyRollbackNeverOverwritesExternalCredential(t *testing.T) {
	fixture := newAPIKeySwitchFixture(t)
	switchID := "6f8dfc76-8db4-4621-8974-c480093e0d55"
	if err := writeGlobalCredentialAtomic(fixture.manager.globalCredentialPath(), []byte("device-source")); err != nil {
		t.Fatal(err)
	}
	fixture.manager.factory = &fakeCodexAccountFactory{open: func(ports.CodexAccountContext) (ports.CodexAccountClient, error) {
		return &fakeCodexAccountClient{read: ports.CodexAccountObservation{Authentication: domain.AgentAuthenticationAuthorized, Method: domain.CodexAuthMethodAPIKey}}, nil
	}}
	if _, err := fixture.service.CheckpointAndActivateCodexAccount(context.Background(), domain.CodexAccountSwitchSourceDevice, switchID, fixture.target.Snapshot.ID, 1); err != nil {
		t.Fatal(err)
	}
	if err := writeGlobalCredentialAtomic(fixture.manager.globalCredentialPath(), []byte("third-party-external")); err != nil {
		t.Fatal(err)
	}
	if err := fixture.service.RestoreCodexAccountCredential(context.Background(), switchID, domain.CodexAccountSwitchSourceDevice, "", fixture.target.Snapshot.ID); !errors.Is(err, ports.ErrCodexGlobalAccountChanged) {
		t.Fatalf("rollback error = %v, want external-change protection", err)
	}
	current, err := readOpaqueCredential(fixture.manager.globalCredentialPath())
	if err != nil || string(current) != "third-party-external" {
		t.Fatalf("external credential was overwritten: %q, %v", current, err)
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

func TestVerifyCurrentCodexAccountAdoptsVerifiedDeviceTargetAfterPointerLag(t *testing.T) {
	fixture := newAPIKeySwitchFixture(t)
	if err := writeGlobalCredentialAtomic(fixture.manager.globalCredentialPath(), []byte("target-api-key")); err != nil {
		t.Fatal(err)
	}
	fixture.manager.factory = &fakeCodexAccountFactory{open: func(account ports.CodexAccountContext) (ports.CodexAccountClient, error) {
		if account.Home != fixture.manager.globalHome || account.Managed {
			t.Fatalf("verification context = %#v", account)
		}
		return &fakeCodexAccountClient{read: ports.CodexAccountObservation{
			Authentication: domain.AgentAuthenticationAuthorized,
			Method:         domain.CodexAuthMethodAPIKey,
		}}, nil
	}}

	if err := fixture.service.VerifyCurrentCodexAccount(context.Background(), fixture.target.Snapshot.ID); err != nil {
		t.Fatal(err)
	}
	if fixture.manager.active.AccountID != fixture.target.Snapshot.ID || fixture.manager.active.Revision != 2 {
		t.Fatalf("active pointer = %#v, want verified target revision 2", fixture.manager.active)
	}
	if fixture.manager.deviceAccountID != fixture.target.Snapshot.ID || !fixture.manager.reconciliation.ActiveAccountVerified {
		t.Fatalf("device association was not adopted: account=%q reconciliation=%#v", fixture.manager.deviceAccountID, fixture.manager.reconciliation)
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

	err := fixture.service.RestoreCodexAccountCredential(context.Background(), "6f8dfc76-8db4-4621-8974-c480093e0d55", domain.CodexAccountSwitchSourceManaged, fixture.source.Snapshot.ID, fixture.target.Snapshot.ID)
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
		_, err := manager.ensureAuthentication(waitCtx, record, domain.AgentReadinessPurposeDisplay)
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

func TestLoginDeduplicatesExistingAccountByEmail(t *testing.T) {
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
	state := &fakeCodexAccountStateStore{}
	manager := newTestCodexAccountManager(t, factory, state)
	manager.globalAuth = accountAuthenticationObservation(time.Now().UTC(), domain.AgentAuthenticationUnauthorized)

	existingID := "a1111111-1111-4111-8111-111111111111"
	manager.catalog.newID = func() string { return existingID }
	existing := commitTestAccount(t, manager.catalog, manager.pendingRoot, "c1111111-1111-4111-8111-111111111111", observation)
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
		result:          shellterm.ShellTerminal{HandleID: "shellterm-dedup", Title: "Add Codex account"},
	}

	started, err := manager.openLoginTerminal(context.Background(), "", false)
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
	if string(credential) != "opaque-login-credential" {
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
	state := &fakeCodexAccountStateStore{
		active: domain.CodexActiveAccount{AccountID: sourceID, Revision: 1},
		found:  true,
	}
	manager := newCodexAccountManager(context.Background(),
		filepath.Join(root, "accounts"), filepath.Join(root, "pending"),
		filepath.Join(root, "staging"), globalHome, factory, state, nil)
	ids := []string{sourceID, testAccountID}
	idx := 0
	manager.catalog.newID = func() string { id := ids[idx]; idx++; return id }
	sourceObs := ports.CodexAccountObservation{Authentication: domain.AgentAuthenticationAuthorized, Method: domain.CodexAuthMethodChatGPT}
	commitTestAccount(t, manager.catalog, manager.pendingRoot, "b60a377d-da68-4a61-86f2-f31f04c571f2", sourceObs)
	record := commitTestAccount(t, manager.catalog, manager.pendingRoot, "1c5de3ab-82d0-4a68-a06b-8495cdeab909", observation)
	if err := writeGlobalCredentialAtomic(manager.globalCredentialPath(), []byte("source-credential")); err != nil {
		t.Fatal(err)
	}
	manager.active = state.active
	manager.accountStoreReady = true
	manager.reconciliation = domain.CodexDeviceReconciliation{
		Status: domain.CodexDeviceReconciliationVerified, ActiveAccountVerified: true,
	}
	svc := &Service{codexAccounts: manager, readiness: newReadinessCoordinator(readinessCoordinatorConfig{})}
	return manager, svc, record
}

func TestSwitchAdmissionTransientErrorDoesNotRequireReauthentication(t *testing.T) {
	email := "switch-test@example.com"
	observation := ports.CodexAccountObservation{
		Authentication: domain.AgentAuthenticationAuthorized,
		Method:         domain.CodexAuthMethodChatGPT,
		Email:          &email,
	}
	callCount := 0
	factory := &fakeCodexAccountFactory{
		capabilities: supportedCodexAccountCapabilities(),
		open: func(ports.CodexAccountContext) (ports.CodexAccountClient, error) {
			callCount++
			if callCount == 1 {
				return nil, errors.New("transient process spawn failure")
			}
			return &fakeCodexAccountClient{read: observation}, nil
		},
	}
	manager, svc, record := newSwitchAdmissionFixture(t, factory, observation)

	err := svc.VerifyCodexAccountForSwitch(context.Background(), record.Snapshot.ID)
	if err == nil {
		t.Fatal("expected transient error, got nil")
	}
	latest, _ := manager.catalog.record(record.Snapshot.ID)
	if latest.Snapshot.Authentication.State == domain.AgentAuthenticationUnauthorized {
		t.Fatal("transient factory.Open failure incorrectly triggered requireReauthentication")
	}

	auth, authErr := manager.ensureAuthentication(context.Background(), record, domain.AgentReadinessPurposeDisplay)
	if authErr != nil {
		t.Fatal(authErr)
	}
	if auth.State == domain.AgentAuthenticationUnauthorized {
		t.Fatal("account is permanently locked out after transient error")
	}
}

func TestSwitchAdmissionReadErrorDoesNotRequireReauthentication(t *testing.T) {
	email := "read-err@example.com"
	observation := ports.CodexAccountObservation{
		Authentication: domain.AgentAuthenticationAuthorized,
		Method:         domain.CodexAuthMethodChatGPT,
		Email:          &email,
	}
	callCount := 0
	factory := &fakeCodexAccountFactory{
		capabilities: supportedCodexAccountCapabilities(),
		open: func(ports.CodexAccountContext) (ports.CodexAccountClient, error) {
			callCount++
			if callCount == 1 {
				return &fakeCodexAccountClient{readErr: errors.New("timeout reading account")}, nil
			}
			return &fakeCodexAccountClient{read: observation}, nil
		},
	}
	manager, svc, record := newSwitchAdmissionFixture(t, factory, observation)

	err := svc.VerifyCodexAccountForSwitch(context.Background(), record.Snapshot.ID)
	if err == nil {
		t.Fatal("expected transient error, got nil")
	}
	latest, _ := manager.catalog.record(record.Snapshot.ID)
	if latest.Snapshot.Authentication.State == domain.AgentAuthenticationUnauthorized {
		t.Fatal("transient client.Read failure incorrectly triggered requireReauthentication")
	}
}

func TestSwitchAdmissionUnauthorizedRequiresReauthentication(t *testing.T) {
	email := "unauth@example.com"
	factory := &fakeCodexAccountFactory{
		capabilities: supportedCodexAccountCapabilities(),
		open: func(ports.CodexAccountContext) (ports.CodexAccountClient, error) {
			return &fakeCodexAccountClient{read: ports.CodexAccountObservation{
				Authentication: domain.AgentAuthenticationUnauthorized,
				Method:         domain.CodexAuthMethodChatGPT,
				Email:          &email,
			}}, nil
		},
	}
	_, svc, record := newSwitchAdmissionFixture(t, factory, ports.CodexAccountObservation{
		Authentication: domain.AgentAuthenticationAuthorized,
		Method:         domain.CodexAuthMethodChatGPT,
		Email:          &email,
	})

	err := svc.VerifyCodexAccountForSwitch(context.Background(), record.Snapshot.ID)
	if err == nil {
		t.Fatal("expected reauth error for unauthorized account")
	}
	var apiErr *apierr.Error
	if !errors.As(err, &apiErr) || apiErr.Code != "CODEX_ACCOUNT_REAUTHENTICATION_REQUIRED" {
		t.Fatalf("expected CODEX_ACCOUNT_REAUTHENTICATION_REQUIRED, got %v", err)
	}
}
