package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/aoagents/agent-orchestrator/cloud/internal/githubapp"
	"github.com/aoagents/agent-orchestrator/cloud/internal/postgres"
	"github.com/aoagents/agent-orchestrator/cloud/internal/secrets"
	"github.com/aoagents/agent-orchestrator/cloud/internal/worker"
)

// checkoutGrantBroker returns configurable App checkout/push grants so the
// handler's App-first preference can be exercised without a real GitHub App.
type checkoutGrantBroker struct {
	recordingCheckoutBroker
	checkout      githubapp.CheckoutGrant
	checkoutErr   error
	checkoutCalls int
}

func (b *checkoutGrantBroker) IssueCheckoutGrant(context.Context, string, string) (githubapp.CheckoutGrant, error) {
	b.checkoutCalls++
	return b.checkout, b.checkoutErr
}

const grantTestOwnerID = "11111111-1111-1111-1111-111111111111"
const grantTestPAT = "ghp_CHECKOUTtestPAT00000000000000000"

func newCheckoutGrantServer(t *testing.T, checkout githubapp.CheckoutGrant, checkoutErr error, withPAT bool) (*Server, *checkoutGrantBroker) {
	t.Helper()
	cipher, err := secrets.New(make([]byte, 32))
	if err != nil {
		t.Fatalf("cipher: %v", err)
	}
	store := &patServerStore{patErr: postgres.ErrNotFound}
	if withPAT {
		encrypted, nonce, err := cipher.Encrypt([]byte(grantTestPAT), providerSecretAssociatedData("user:"+grantTestOwnerID, githubPATProvider))
		if err != nil {
			t.Fatalf("encrypt: %v", err)
		}
		store = &patServerStore{pat: domain.WorkerGitHubPAT{
			OwnerUserID: grantTestOwnerID, CloneURL: "https://github.com/octo/widgets.git",
			EncryptedSecret: encrypted, Nonce: nonce,
		}}
	}
	broker := &checkoutGrantBroker{checkout: checkout, checkoutErr: checkoutErr}
	srv := New(Options{
		Store:          store,
		SecretCipher:   cipher,
		CheckoutBroker: broker,
		Logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	return srv, broker
}

func checkoutGrantRequest(t *testing.T) *http.Request {
	return workerRequest(t, http.MethodPost, "/worker/checkout-grant", "", "worker:git")
}

// The App installation grant must win even when a PAT exists: preferring the PAT
// is exactly what let a rotted PAT (validation_state is a cached snapshot) shadow
// a healthy App installation and fail every clone with "Invalid username or token".
func TestWorkerCheckoutGrantPrefersAppOverPAT(t *testing.T) {
	appGrant := githubapp.CheckoutGrant{
		CloneURL:  "https://github.com/octo/widgets.git",
		Token:     "APP_INSTALLATION_TOKEN",
		ExpiresAt: time.Now().Add(time.Hour),
	}
	srv, broker := newCheckoutGrantServer(t, appGrant, nil, true)
	w := httptest.NewRecorder()
	srv.workerCheckoutGrant(w, checkoutGrantRequest(t))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if broker.checkoutCalls != 1 {
		t.Fatalf("IssueCheckoutGrant called %d times, want 1 (App tried first)", broker.checkoutCalls)
	}
	var resp worker.CheckoutGrantResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Token != "APP_INSTALLATION_TOKEN" {
		t.Fatalf("token = %q, want the App token (a PAT must not shadow a healthy App grant)", resp.Token)
	}
}

// When the App path cannot serve the project (no installation / not App-connected),
// fall back to the stored PAT rather than failing.
func TestWorkerCheckoutGrantFallsBackToPAT(t *testing.T) {
	srv, broker := newCheckoutGrantServer(t, githubapp.CheckoutGrant{}, postgres.ErrForbidden, true)
	w := httptest.NewRecorder()
	srv.workerCheckoutGrant(w, checkoutGrantRequest(t))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (PAT fallback); body=%s", w.Code, w.Body.String())
	}
	if broker.checkoutCalls != 1 {
		t.Fatalf("IssueCheckoutGrant called %d times, want 1", broker.checkoutCalls)
	}
	var resp worker.CheckoutGrantResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Token != grantTestPAT {
		t.Fatalf("token = %q, want the PAT fallback", resp.Token)
	}
}

// No App grant and no PAT: surface the App error semantics (403), not a fallback.
func TestWorkerCheckoutGrantForbiddenWithoutAppOrPAT(t *testing.T) {
	srv, _ := newCheckoutGrantServer(t, githubapp.CheckoutGrant{}, postgres.ErrForbidden, false)
	w := httptest.NewRecorder()
	srv.workerCheckoutGrant(w, checkoutGrantRequest(t))

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", w.Code, w.Body.String())
	}
}
