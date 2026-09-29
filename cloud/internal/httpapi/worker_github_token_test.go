package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/aoagents/agent-orchestrator/cloud/internal/githubapp"
	"github.com/aoagents/agent-orchestrator/cloud/internal/postgres"
	"github.com/aoagents/agent-orchestrator/cloud/internal/secrets"
	"github.com/aoagents/agent-orchestrator/cloud/internal/worker"
)

// pushGrantBroker returns a configurable App push grant so workerGitHubToken's
// App-first preference can be exercised without a real GitHub App.
type pushGrantBroker struct {
	recordingCheckoutBroker
	push          githubapp.CheckoutGrant
	pushErr       error
	pushCalls     int
	repoPush      githubapp.CheckoutGrant
	repoPushErr   error
	repoPushCalls int
	lastRepo      string
}

func (b *pushGrantBroker) IssuePushGrant(context.Context, string, string) (githubapp.CheckoutGrant, error) {
	b.pushCalls++
	return b.push, b.pushErr
}

func (b *pushGrantBroker) IssuePushGrantForRepo(_ context.Context, _, _, repo string) (githubapp.CheckoutGrant, error) {
	b.repoPushCalls++
	b.lastRepo = repo
	return b.repoPush, b.repoPushErr
}

func newGitHubTokenServer(t *testing.T, push githubapp.CheckoutGrant, pushErr error, withPAT bool) (*Server, *pushGrantBroker) {
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
	broker := &pushGrantBroker{push: push, pushErr: pushErr}
	srv := New(Options{
		Store:          store,
		SecretCipher:   cipher,
		CheckoutBroker: broker,
		Logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	return srv, broker
}

func gitHubTokenRequest(t *testing.T) *http.Request {
	return workerRequest(t, http.MethodPost, "/worker/github-token", "", "worker:git")
}

// The sandbox git credential helper calls /worker/github-token for every fetch
// and push. It must prefer the App push grant over a stored PAT — preferring the
// PAT is exactly what let a rotted PAT (validation_state is a cached snapshot)
// shadow a healthy App installation and fail every push with "Authentication
// failed", even though the App can push. Regression test for that bug.
func TestWorkerGitHubTokenPrefersAppOverPAT(t *testing.T) {
	appPush := githubapp.CheckoutGrant{
		CloneURL:  "https://github.com/octo/widgets.git",
		Token:     "APP_PUSH_TOKEN",
		ExpiresAt: time.Now().Add(time.Hour),
	}
	srv, broker := newGitHubTokenServer(t, appPush, nil, true)
	w := httptest.NewRecorder()
	srv.workerGitHubToken(w, gitHubTokenRequest(t))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if broker.pushCalls != 1 {
		t.Fatalf("IssuePushGrant called %d times, want 1 (App tried first)", broker.pushCalls)
	}
	var resp worker.GitHubTokenResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Token != "APP_PUSH_TOKEN" {
		t.Fatalf("token = %q, want the App token (a PAT must not shadow a healthy App grant)", resp.Token)
	}
}

// When the App path cannot serve the project, fall back to the stored PAT.
func TestWorkerGitHubTokenFallsBackToPAT(t *testing.T) {
	srv, broker := newGitHubTokenServer(t, githubapp.CheckoutGrant{}, postgres.ErrForbidden, true)
	w := httptest.NewRecorder()
	srv.workerGitHubToken(w, gitHubTokenRequest(t))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (PAT fallback); body=%s", w.Code, w.Body.String())
	}
	if broker.pushCalls != 1 {
		t.Fatalf("IssuePushGrant called %d times, want 1", broker.pushCalls)
	}
	var resp worker.GitHubTokenResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Token != grantTestPAT {
		t.Fatalf("token = %q, want the PAT fallback", resp.Token)
	}
}

// No App grant and no PAT: surface the App error (403), not a silent fallback.
func TestWorkerGitHubTokenForbiddenWithoutAppOrPAT(t *testing.T) {
	srv, _ := newGitHubTokenServer(t, githubapp.CheckoutGrant{}, postgres.ErrForbidden, false)
	w := httptest.NewRecorder()
	srv.workerGitHubToken(w, gitHubTokenRequest(t))

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", w.Code, w.Body.String())
	}
}

// When git names the exact repository (credential.useHttpPath forwards it as
// ?repo=), mint an App token scoped to that repository rather than the broad
// multi-repository grant. This is what lets a push to a declared extra dev-kit
// repository the App is installed on succeed with a correctly scoped token.
func TestWorkerGitHubTokenScopedToRepoUsesAppForRepo(t *testing.T) {
	srv, broker := newGitHubTokenServer(t, githubapp.CheckoutGrant{}, nil, true)
	broker.repoPush = githubapp.CheckoutGrant{
		CloneURL:  "https://github.com/octo/extra.git",
		Token:     "APP_EXTRA_TOKEN",
		ExpiresAt: time.Now().Add(time.Hour),
	}
	w := httptest.NewRecorder()
	srv.workerGitHubToken(w, workerRequest(t, http.MethodPost, "/worker/github-token?repo=octo/extra", "", "worker:git"))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if broker.repoPushCalls != 1 || broker.pushCalls != 0 {
		t.Fatalf("repoPushCalls=%d pushCalls=%d, want 1 and 0 (repo-scoped grant only)", broker.repoPushCalls, broker.pushCalls)
	}
	if broker.lastRepo != "octo/extra" {
		t.Fatalf("scoped repo = %q, want octo/extra", broker.lastRepo)
	}
	var resp worker.GitHubTokenResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Token != "APP_EXTRA_TOKEN" {
		t.Fatalf("token = %q, want the repo-scoped App token", resp.Token)
	}
}

// An extra repository the App is not installed on returns ErrForbidden from the
// repo-scoped App grant; the credential helper must then fall back to a stored
// PAT that may still cover it — the per-repository App-first/PAT-fallback rule.
func TestWorkerGitHubTokenScopedToRepoFallsBackToPAT(t *testing.T) {
	srv, broker := newGitHubTokenServer(t, githubapp.CheckoutGrant{}, nil, true)
	broker.repoPushErr = postgres.ErrForbidden
	w := httptest.NewRecorder()
	srv.workerGitHubToken(w, workerRequest(t, http.MethodPost, "/worker/github-token?repo=octo/extra", "", "worker:git"))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (PAT fallback); body=%s", w.Code, w.Body.String())
	}
	if broker.repoPushCalls != 1 {
		t.Fatalf("repoPushCalls = %d, want 1", broker.repoPushCalls)
	}
	var resp worker.GitHubTokenResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Token != grantTestPAT {
		t.Fatalf("token = %q, want the PAT fallback", resp.Token)
	}
}

func TestWorkerRequestedRepository(t *testing.T) {
	cases := map[string]string{
		"":               "",
		"octo/extra":     "octo/extra",
		"octo/extra.git": "octo/extra",
		"/octo/extra/":   "octo/extra",
		"octo":           "", // no repo segment
		"octo/a/b":       "", // extra path segments
		"octo /extra":    "", // whitespace
	}
	for query, want := range cases {
		r := httptest.NewRequest(http.MethodPost, "/worker/github-token?repo="+url.QueryEscape(query), nil)
		if got := workerRequestedRepository(r); got != want {
			t.Errorf("workerRequestedRepository(%q) = %q, want %q", query, got, want)
		}
	}
}
