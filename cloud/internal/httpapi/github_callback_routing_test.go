package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/aoagents/agent-orchestrator/cloud/internal/githubapp"
	"github.com/aoagents/agent-orchestrator/cloud/internal/postgres"
)

// ghRoutingStubStore satisfies githubapp.Store; only the methods the two OAuth
// callback paths reach are implemented, and each records that it was called so a
// test can assert which service method the handler routed to. Every other Store
// method is promoted from the embedded nil interface and panics if reached.
type ghRoutingStubStore struct {
	githubapp.Store
	validateInstallCalls int
	oauthAttemptCalls    int
}

// CompleteInstallationOAuth (bundled path) calls this first; returning Forbidden
// short-circuits before any GitHub call and yields a 403, distinct from the
// completion path's 404.
func (s *ghRoutingStubStore) ValidateGitHubInstallState(context.Context, []byte) error {
	s.validateInstallCalls++
	return postgres.ErrForbidden
}

// CompleteOAuth (completion path) calls this; it never runs on the bundled path.
func (s *ghRoutingStubStore) GitHubOAuthAttempt(context.Context, []byte) (domain.GitHubInstallAttempt, error) {
	s.oauthAttemptCalls++
	return domain.GitHubInstallAttempt{}, postgres.ErrNotFound
}

func newGitHubCallbackTestServer(t *testing.T) (*Server, *ghRoutingStubStore) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate RSA key: %v", err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})
	client, err := githubapp.New(githubapp.Config{
		AppID:         1,
		AppSlug:       "ao-test",
		ClientID:      "Iv1.test",
		ClientSecret:  "secret",
		PrivateKeyPEM: string(pemBytes),
		PublicURL:     "https://api.example.com",
		APIBaseURL:    "https://api.github.test",
		WebBaseURL:    "https://github.test",
	}, http.DefaultClient)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	store := &ghRoutingStubStore{}
	svc, err := githubapp.NewService(
		store, client,
		make([]byte, 32), make([]byte, 32),
		"webhook-secret", time.Hour, slog.Default(),
	)
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	return &Server{github: svc, logger: slog.Default()}, store
}

// A GitHub App that requests user authorization during installation delivers the
// installation context (installation_id) straight to the OAuth callback. The
// handler must complete it directly via CompleteInstallationOAuth (which
// validates the install state) and must NOT redirect back to authorize.
func TestGitHubOAuthCallbackBundledInstallCompletesDirectly(t *testing.T) {
	srv, store := newGitHubCallbackTestServer(t)
	req := httptest.NewRequest(http.MethodGet,
		"/api/cloud/v1/github/oauth/callback?installation_id=123&setup_action=install&state=teststate&code=abc", nil)
	rec := httptest.NewRecorder()

	srv.githubOAuthCallback(rec, req)

	if loc := rec.Header().Get("Location"); loc != "" {
		t.Fatalf("bundled callback redirected to %q; it must complete directly, not redirect", loc)
	}
	if store.validateInstallCalls != 1 {
		t.Fatalf("ValidateGitHubInstallState called %d times, want 1 (routed to CompleteInstallationOAuth)", store.validateInstallCalls)
	}
	if store.oauthAttemptCalls != 0 {
		t.Fatalf("GitHubOAuthAttempt called %d times on the bundled path, want 0", store.oauthAttemptCalls)
	}
	// The callback error handler always renders the 400 HTML page; routing is
	// asserted via the store-call tracking above, not the status.
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (callback error page)", rec.Code)
	}
}

// A non-numeric installation_id is rejected before any store call.
func TestGitHubOAuthCallbackBundledInstallRejectsBadInstallationID(t *testing.T) {
	srv, store := newGitHubCallbackTestServer(t)
	req := httptest.NewRequest(http.MethodGet,
		"/api/cloud/v1/github/oauth/callback?installation_id=notanumber&state=s&code=c", nil)
	rec := httptest.NewRecorder()

	srv.githubOAuthCallback(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for a non-numeric installation_id", rec.Code)
	}
	if store.validateInstallCalls != 0 || store.oauthAttemptCalls != 0 {
		t.Fatalf("no store method should run for a bad installation_id (validate=%d oauth=%d)", store.validateInstallCalls, store.oauthAttemptCalls)
	}
}

// A normal completion callback (code+state, no installation_id) must route to
// CompleteOAuth, never CompleteInstallationOAuth, and must not redirect.
func TestGitHubOAuthCallbackCompletionPath(t *testing.T) {
	srv, store := newGitHubCallbackTestServer(t)
	req := httptest.NewRequest(http.MethodGet,
		"/api/cloud/v1/github/oauth/callback?state=teststate&code=abc", nil)
	rec := httptest.NewRecorder()

	srv.githubOAuthCallback(rec, req)

	if loc := rec.Header().Get("Location"); loc != "" {
		t.Fatalf("completion callback unexpectedly redirected to %q", loc)
	}
	if store.oauthAttemptCalls != 1 {
		t.Fatalf("GitHubOAuthAttempt called %d times, want 1 (routed to CompleteOAuth)", store.oauthAttemptCalls)
	}
	if store.validateInstallCalls != 0 {
		t.Fatalf("ValidateGitHubInstallState called %d times on the completion path, want 0", store.validateInstallCalls)
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (stub has no oauth attempt)", rec.Code)
	}
}
