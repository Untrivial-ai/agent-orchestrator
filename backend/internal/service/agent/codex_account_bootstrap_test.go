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

func TestCodexAccountStoreAndDeviceReconciliationNeverOpenProvider(t *testing.T) {
	root := t.TempDir()
	globalHome := filepath.Join(root, "global")
	if err := ensurePrivateDirectory(globalHome); err != nil {
		t.Fatal(err)
	}
	credential := testAPIKeyCredential("device-api-key")
	if err := writeGlobalCredentialAtomic(filepath.Join(globalHome, codexCredentialFilename), credential); err != nil {
		t.Fatal(err)
	}
	var attempts atomic.Int32
	factory := &fakeCodexAccountFactory{open: func(ports.CodexAccountContext) (ports.CodexAccountClient, error) {
		attempts.Add(1)
		return nil, errors.New("provider must not be opened during reconciliation")
	}}
	manager := newCodexAccountManager(context.Background(), filepath.Join(root, "accounts"), filepath.Join(root, "pending"), filepath.Join(root, "staging"), globalHome, factory, nil, nil)
	manager.catalog.newID = func() string { return testAccountID }
	service := &Service{codexAccounts: manager}
	if err := service.WaitCodexAccountStoreReady(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := service.EnsureCodexDeviceAccountReconciled(context.Background()); err != nil {
		t.Fatal(err)
	}
	if attempts.Load() != 0 {
		t.Fatalf("local reconciliation opened %d provider clients", attempts.Load())
	}
	view := manager.cached()
	if view.ActiveAccountID != testAccountID || len(view.Accounts) != 1 || !view.Accounts[0].Active {
		t.Fatalf("imported device account = %#v", view)
	}
}

func TestCodexDeviceReconciliationReportsMalformedCredentialSafely(t *testing.T) {
	root := t.TempDir()
	globalHome := filepath.Join(root, "global")
	if err := ensurePrivateDirectory(globalHome); err != nil {
		t.Fatal(err)
	}
	if err := writeGlobalCredentialAtomic(filepath.Join(globalHome, codexCredentialFilename), []byte(`{"tokens":{"access_token":"secret"}`)); err != nil {
		t.Fatal(err)
	}
	manager := newCodexAccountManager(context.Background(), filepath.Join(root, "accounts"), filepath.Join(root, "pending"), filepath.Join(root, "staging"), globalHome, nil, nil, nil)
	var logs bytes.Buffer
	manager.logger = slog.New(slog.NewTextHandler(&logs, nil))
	service := &Service{codexAccounts: manager}
	if err := service.WaitCodexAccountStoreReady(context.Background()); err != nil {
		t.Fatal(err)
	}
	err := service.EnsureCodexDeviceAccountReconciled(context.Background())
	var apiError *apierr.Error
	if !errors.As(err, &apiError) || apiError.Kind != apierr.KindUnavailable || apiError.Code != "CODEX_DEVICE_ACCOUNT_UNVERIFIED" || apiError.Message != "The device Codex account could not be verified" || apiError.Details["reasonCode"] != "global_credential_invalid" || apiError.Details["retryable"] != false {
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
	if rec.Code != http.StatusServiceUnavailable || body.Error != "unavailable" || body.Code != apiError.Code || body.RequestID != "bootstrap-request" || body.Details["reasonCode"] != "global_credential_invalid" {
		t.Fatalf("HTTP envelope = %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "secret") {
		t.Fatal("credential content leaked")
	}
	if !strings.Contains(logs.String(), "reasonCode=global_credential_invalid") || strings.Contains(logs.String(), "secret") {
		t.Fatalf("unsafe or missing diagnostic: %s", logs.String())
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
			var failure *codexAccountLocalFailure
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

func TestCodexDeviceReconciliationFailureKeepsSavedAccountsReadable(t *testing.T) {
	root := t.TempDir()
	globalHome := filepath.Join(root, "global")
	if err := ensurePrivateDirectory(globalHome); err != nil {
		t.Fatal(err)
	}
	if err := writeGlobalCredentialAtomic(filepath.Join(globalHome, codexCredentialFilename), []byte(`{"tokens":`)); err != nil {
		t.Fatal(err)
	}
	manager := newCodexAccountManager(context.Background(), filepath.Join(root, "accounts"), filepath.Join(root, "pending"), filepath.Join(root, "staging"), globalHome, nil, nil)
	manager.catalog.newID = func() string { return testAccountID }
	email := "saved@example.com"
	commitTestAccount(t, manager.catalog, manager.pendingRoot, "b60a377d-da68-4a61-86f2-f31f04c571f2", ports.CodexAccountObservation{
		Authentication: domain.AgentAuthenticationAuthorized, Method: domain.CodexAuthMethodChatGPT, Email: &email,
	})
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
	if view.ActiveAccountID != "" || len(view.Accounts) != 1 || view.Accounts[0].ID != testAccountID {
		t.Fatalf("saved account disappeared: %#v", view)
	}
	if view.Accounts[0].Active {
		t.Fatal("unverified durable pointer was presented as in use")
	}
	if view.DeviceReconciliation.Status != domain.CodexDeviceReconciliationBlocked || view.DeviceReconciliation.ReasonCode != "global_credential_invalid" {
		t.Fatalf("device reconciliation = %#v", view.DeviceReconciliation)
	}
}
