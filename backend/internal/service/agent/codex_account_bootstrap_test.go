package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/envelope"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestCodexAccountStoreReadyDoesNotDependOnDeviceReconciliation(t *testing.T) {
	for name, firstError := range map[string]error{
		"provider unavailable":           errors.New("secret credential /private/path"),
		"executable installed later":     &os.PathError{Op: "fork/exec", Path: "/private/codex", Err: os.ErrNotExist},
		"executable permission repaired": &os.PathError{Op: "fork/exec", Path: "/private/codex", Err: os.ErrPermission},
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			attempts := 0
			factory := &fakeCodexAccountFactory{open: func(ports.CodexAccountContext) (ports.CodexAccountClient, error) {
				attempts++
				if attempts == 1 {
					return nil, firstError
				}
				return &fakeCodexAccountClient{read: ports.CodexAccountObservation{Authentication: domain.AgentAuthenticationUnauthorized}}, nil
			}}
			manager := newCodexAccountManager(context.Background(), filepath.Join(root, "accounts"), filepath.Join(root, "pending"), filepath.Join(root, "staging"), filepath.Join(root, "global"), factory, nil, nil)
			now := time.Now()
			manager.now = func() time.Time { return now }
			service := &Service{codexAccounts: manager}
			if err := service.WaitCodexAccountStoreReady(context.Background()); err != nil {
				t.Fatalf("local account store was blocked by provider: %v", err)
			}
			if attempts != 0 {
				t.Fatalf("store initialization opened %d provider clients", attempts)
			}
			if err := service.EnsureCodexDeviceAccountReconciled(context.Background()); err == nil {
				t.Fatal("device reconciliation unexpectedly succeeded")
			}
			now = now.Add(time.Minute)
			if err := service.EnsureCodexDeviceAccountReconciled(context.Background()); err != nil {
				t.Fatalf("device reconciliation retry remained blocked: %v", err)
			}
			if attempts != 2 {
				t.Fatalf("attempts = %d", attempts)
			}
		})
	}
}

func TestCodexDeviceReconciliationCooldownAndSafeFailure(t *testing.T) {
	root := t.TempDir()
	factory := &fakeCodexAccountFactory{open: func(ports.CodexAccountContext) (ports.CodexAccountClient, error) {
		return nil, errors.New("secret credential /private/path")
	}}
	manager := newCodexAccountManager(context.Background(), filepath.Join(root, "accounts"), filepath.Join(root, "pending"), filepath.Join(root, "staging"), filepath.Join(root, "global"), factory, nil, nil)
	var logs bytes.Buffer
	manager.logger = slog.New(slog.NewTextHandler(&logs, nil))
	service := &Service{codexAccounts: manager}
	if err := service.WaitCodexAccountStoreReady(context.Background()); err != nil {
		t.Fatal(err)
	}
	for range 20 {
		err := service.EnsureCodexDeviceAccountReconciled(context.Background())
		var apiError *apierr.Error
		if !errors.As(err, &apiError) || apiError.Kind != apierr.KindUnavailable || apiError.Code != "CODEX_DEVICE_ACCOUNT_UNVERIFIED" || apiError.Message != "The device Codex account could not be verified" || apiError.Details["reasonCode"] != "account_client_unavailable" || apiError.Details["retryable"] != true {
			t.Fatalf("unsafe or incorrect envelope: %#v", err)
		}
		req := httptest.NewRequest(http.MethodPost, "/api/v1/orchestrators/delegate", nil)
		req = req.WithContext(context.WithValue(req.Context(), middleware.RequestIDKey, "bootstrap-request"))
		rec := httptest.NewRecorder()
		envelope.WriteError(rec, req, err)
		var body envelope.APIError
		if decodeErr := json.Unmarshal(rec.Body.Bytes(), &body); decodeErr != nil {
			t.Fatal(decodeErr)
		}
		if rec.Code != http.StatusServiceUnavailable || body.Error != "unavailable" || body.Code != apiError.Code || body.RequestID != "bootstrap-request" || body.Details["reasonCode"] != "account_client_unavailable" {
			t.Fatalf("HTTP envelope = %d %s", rec.Code, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), "secret") || strings.Contains(rec.Body.String(), "/private/path") {
			t.Fatal("provider error leaked")
		}
	}
	if !strings.Contains(logs.String(), "reasonCode=account_client_unavailable") || strings.Contains(logs.String(), "secret") || strings.Contains(logs.String(), "/private/path") {
		t.Fatalf("unsafe or missing diagnostic: %s", logs.String())
	}
	factory.mu.Lock()
	opens := factory.opens
	factory.mu.Unlock()
	if opens != 1 {
		t.Fatalf("cooldown opened %d clients", opens)
	}
}

// The credential-file helpers summarize CreateTemp, Write, Sync, Close, and
// rename failures behind an opaque message but must preserve the underlying os
// error so bootstrap can still tell a transient I/O fault from an unsafe-storage
// rejection. Before the cause was preserved, every one of these was classified
// account_storage_unsafe with retryable=false and blocked Codex until restart.
func TestBootstrapStorageFailureClassifiesPreservedIOCause(t *testing.T) {
	const summary = "codex replacement staging could not be written"
	for name, tc := range map[string]struct {
		err       error
		reason    string
		retryable bool
	}{
		"transient path write": {
			err:       codexStorageIOFailure(summary, &os.PathError{Op: "write", Path: "/private/staging/auth.json", Err: syscall.ENOSPC}),
			reason:    "account_storage_unavailable",
			retryable: true,
		},
		"transient rename link": {
			err:       codexStorageIOFailure("codex replacement could not be committed", &os.LinkError{Op: "rename", Old: "/private/staging/tmp", New: "/private/home/auth.json", Err: syscall.EIO}),
			reason:    "account_storage_unavailable",
			retryable: true,
		},
		"permission stays unsafe": {
			err:       codexStorageIOFailure(summary, &os.PathError{Op: "open", Path: "/private/staging/auth.json", Err: os.ErrPermission}),
			reason:    "account_storage_unsafe",
			retryable: false,
		},
		"validation stays unsafe": {
			err:       errors.New("codex file has an unsafe ancestor"),
			reason:    "account_storage_unsafe",
			retryable: false,
		},
	} {
		t.Run(name, func(t *testing.T) {
			var failure *codexAccountStoreFailure
			if !errors.As(accountStoreStorageFailure(tc.err), &failure) {
				t.Fatalf("not a bootstrap failure: %#v", tc.err)
			}
			if failure.reason != tc.reason || failure.retryable != tc.retryable {
				t.Fatalf("classified %s retryable=%t, want %s retryable=%t", failure.reason, failure.retryable, tc.reason, tc.retryable)
			}
			// The preserved cause must never surface a path through the message.
			if strings.Contains(tc.err.Error(), "/private/") {
				t.Fatalf("opaque summary leaked a path: %q", tc.err.Error())
			}
		})
	}
}

func TestCodexBootstrapPermanentSafetyFailure(t *testing.T) {
	root := t.TempDir()
	pending := filepath.Join(root, "pending")
	if err := os.Symlink(t.TempDir(), pending); err != nil {
		t.Fatal(err)
	}
	factory := &fakeCodexAccountFactory{}
	manager := newCodexAccountManager(context.Background(), filepath.Join(root, "accounts"), pending, filepath.Join(root, "staging"), filepath.Join(root, "global"), factory, nil, nil)
	service := &Service{codexAccounts: manager}
	for i := range 2 {
		err := service.WaitCodexAccountStoreReady(context.Background())
		var apiError *apierr.Error
		if !errors.As(err, &apiError) || apiError.Details["reasonCode"] != "account_storage_unsafe" || apiError.Details["retryable"] != false {
			t.Fatalf("failure %d = %#v", i, err)
		}
		// Even correcting the layout cannot silently reopen a safety latch.
		if i == 0 {
			if err := os.Remove(pending); err != nil {
				t.Fatal(err)
			}
		}
	}
	factory.mu.Lock()
	opens := factory.opens
	factory.mu.Unlock()
	if opens != 0 {
		t.Fatalf("unsafe layout reached provider: %d", opens)
	}
}

type blockingAccountStateStore struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *blockingAccountStateStore) GetCodexActiveAccount(ctx context.Context) (domain.CodexActiveAccount, bool, error) {
	s.once.Do(func() { close(s.started) })
	select {
	case <-s.release:
		return domain.CodexActiveAccount{}, false, nil
	case <-ctx.Done():
		return domain.CodexActiveAccount{}, false, ctx.Err()
	}
}

func (*blockingAccountStateStore) SetCodexActiveAccount(context.Context, string, int64, time.Time) (domain.CodexActiveAccount, error) {
	return domain.CodexActiveAccount{}, nil
}

func TestCodexAccountStoreConcurrentWaitersAndCancellation(t *testing.T) {
	root := t.TempDir()
	started, release := make(chan struct{}), make(chan struct{})
	state := &blockingAccountStateStore{started: started, release: release}
	manager := newCodexAccountManager(context.Background(), filepath.Join(root, "accounts"), filepath.Join(root, "pending"), filepath.Join(root, "staging"), filepath.Join(root, "global"), &fakeCodexAccountFactory{}, state, nil)
	service := &Service{codexAccounts: manager}
	ctx, cancel := context.WithCancel(context.Background())
	first := make(chan error, 1)
	go func() { first <- service.WaitCodexAccountStoreReady(ctx) }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("account-state read did not start")
	}
	results := make(chan error, 16)
	for range 16 {
		go func() { results <- service.WaitCodexAccountStoreReady(context.Background()) }()
	}
	cancel()
	if err := <-first; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel = %v", err)
	}
	select {
	case err := <-results:
		t.Fatalf("admitted before provider result: %v", err)
	default:
	}
	close(release)
	for range 16 {
		select {
		case err := <-results:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(time.Second):
			t.Fatal("waiter stuck")
		}
	}
}

type bootstrapFailingStateStore struct {
	fakeCodexAccountStateStore
	reads int
}

func (s *bootstrapFailingStateStore) GetCodexActiveAccount(ctx context.Context) (domain.CodexActiveAccount, bool, error) {
	s.reads++
	if s.reads == 1 {
		return domain.CodexActiveAccount{}, false, errors.New("database is temporarily busy")
	}
	return s.fakeCodexAccountStateStore.GetCodexActiveAccount(ctx)
}

func TestCodexBootstrapRetriesStateReadFailure(t *testing.T) {
	root := t.TempDir()
	state := &bootstrapFailingStateStore{}
	factory := &fakeCodexAccountFactory{open: func(ports.CodexAccountContext) (ports.CodexAccountClient, error) {
		return &fakeCodexAccountClient{read: ports.CodexAccountObservation{Authentication: domain.AgentAuthenticationUnauthorized}}, nil
	}}
	manager := newCodexAccountManager(context.Background(), filepath.Join(root, "accounts"), filepath.Join(root, "pending"), filepath.Join(root, "staging"), filepath.Join(root, "global"), factory, state, nil)
	now := time.Now()
	manager.now = func() time.Time { return now }
	service := &Service{codexAccounts: manager}
	var apiError *apierr.Error
	if err := service.WaitCodexAccountStoreReady(context.Background()); !errors.As(err, &apiError) || apiError.Details["reasonCode"] != "account_state_unavailable" || apiError.Details["retryable"] != true {
		t.Fatalf("state failure = %#v", err)
	}
	now = now.Add(time.Minute)
	if err := service.WaitCodexAccountStoreReady(context.Background()); err != nil {
		t.Fatal(err)
	}
	if state.reads != 2 {
		t.Fatalf("state reads = %d", state.reads)
	}
}

func TestCodexDeviceReconciliationRetriesInconclusiveRead(t *testing.T) {
	root := t.TempDir()
	var attempts atomic.Int32
	factory := &fakeCodexAccountFactory{open: func(ports.CodexAccountContext) (ports.CodexAccountClient, error) {
		if attempts.Add(1) == 1 {
			return &fakeCodexAccountClient{readErr: context.DeadlineExceeded}, nil
		}
		return &fakeCodexAccountClient{read: ports.CodexAccountObservation{Authentication: domain.AgentAuthenticationUnauthorized}}, nil
	}}
	manager := newCodexAccountManager(context.Background(), filepath.Join(root, "accounts"), filepath.Join(root, "pending"), filepath.Join(root, "staging"), filepath.Join(root, "global"), factory, nil, nil)
	now := time.Now()
	manager.now = func() time.Time { return now }
	service := &Service{codexAccounts: manager}
	if err := service.WaitCodexAccountStoreReady(context.Background()); err != nil {
		t.Fatal(err)
	}
	var apiError *apierr.Error
	if err := service.EnsureCodexDeviceAccountReconciled(context.Background()); !errors.As(err, &apiError) || apiError.Code != "CODEX_DEVICE_ACCOUNT_UNVERIFIED" || apiError.Details["reasonCode"] != "account_read_inconclusive" || apiError.Details["retryable"] != true {
		t.Fatalf("read failure = %#v", err)
	}
	now = now.Add(time.Minute)
	if err := service.EnsureCodexDeviceAccountReconciled(context.Background()); err != nil {
		t.Fatal(err)
	}
	if attempts.Load() != 2 {
		t.Fatalf("attempts = %d", attempts.Load())
	}
}

func TestCodexDeviceReconciliationRetriesInBackground(t *testing.T) {
	root := t.TempDir()
	var attempts atomic.Int32
	factory := &fakeCodexAccountFactory{open: func(ports.CodexAccountContext) (ports.CodexAccountClient, error) {
		if attempts.Add(1) == 1 {
			return nil, errors.New("temporary provider failure")
		}
		return &fakeCodexAccountClient{read: ports.CodexAccountObservation{Authentication: domain.AgentAuthenticationUnauthorized}}, nil
	}}
	manager := newCodexAccountManager(context.Background(), filepath.Join(root, "accounts"), filepath.Join(root, "pending"), filepath.Join(root, "staging"), filepath.Join(root, "global"), factory, nil, nil)
	now := time.Now().UTC()
	manager.now = func() time.Time { return now }
	retry := make(chan time.Time, 1)
	manager.after = func(delay time.Duration) <-chan time.Time {
		if delay != time.Second {
			t.Fatalf("first retry delay = %s", delay)
		}
		return retry
	}
	service := &Service{codexAccounts: manager}
	if err := service.EnsureCodexDeviceAccountReconciled(context.Background()); err == nil {
		t.Fatal("first reconciliation unexpectedly succeeded")
	}
	now = now.Add(time.Second)
	retry <- now
	deadline := time.Now().Add(5 * time.Second)
	for {
		manager.mu.Lock()
		state := manager.reconciliation
		manager.mu.Unlock()
		if state.Status == domain.CodexDeviceReconciliationVerified {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("background retry did not recover: %#v", state)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if attempts.Load() != 2 {
		t.Fatalf("attempts = %d", attempts.Load())
	}
}

func TestCodexDeviceReconciliationFailureKeepsSavedAccountsReadable(t *testing.T) {
	root := t.TempDir()
	state := &fakeCodexAccountStateStore{active: domain.CodexActiveAccount{AccountID: testAccountID, Revision: 4}, found: true}
	manager := newCodexAccountManager(context.Background(), filepath.Join(root, "accounts"), filepath.Join(root, "pending"), filepath.Join(root, "staging"), filepath.Join(root, "global"), nil, state, nil)
	manager.catalog.newID = func() string { return testAccountID }
	email := "saved@example.com"
	commitTestAccount(t, manager.catalog, manager.pendingRoot, "b60a377d-da68-4a61-86f2-f31f04c571f2", ports.CodexAccountObservation{
		Authentication: domain.AgentAuthenticationAuthorized, Method: domain.CodexAuthMethodChatGPT, Email: &email,
	})
	manager.factory = &fakeCodexAccountFactory{open: func(ports.CodexAccountContext) (ports.CodexAccountClient, error) {
		return nil, errors.New("temporary device read failure")
	}}
	manager.after = func(time.Duration) <-chan time.Time { return make(chan time.Time) }
	service := &Service{codexAccounts: manager}
	if err := service.WaitCodexAccountStoreReady(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := service.EnsureCodexDeviceAccountReconciled(context.Background()); err == nil {
		t.Fatal("device reconciliation unexpectedly succeeded")
	}
	view, err := service.CachedCodexAccounts(context.Background())
	if err != nil {
		t.Fatalf("cached accounts failed with device reconciliation: %v", err)
	}
	if view.ActiveAccountID != testAccountID || view.AccountRevision != 4 || len(view.Accounts) != 1 || view.Accounts[0].ID != testAccountID {
		t.Fatalf("saved account disappeared: %#v", view)
	}
	if view.Accounts[0].Active {
		t.Fatal("unverified durable pointer was presented as in use")
	}
	if view.DeviceReconciliation.Status != domain.CodexDeviceReconciliationTemporarilyUnavailable || view.DeviceReconciliation.ReasonCode != "account_client_unavailable" {
		t.Fatalf("device reconciliation = %#v", view.DeviceReconciliation)
	}
	if view.UnmanagedGlobalAccount != nil {
		t.Fatalf("temporary failure was presented as unmanaged: %#v", view.UnmanagedGlobalAccount)
	}
}
