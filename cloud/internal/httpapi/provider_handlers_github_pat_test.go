package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/aoagents/agent-orchestrator/cloud/internal/secrets"
)

// githubPATFakeStore is the minimal userProviderConnectionStore double this
// test needs: it records whether Upsert was reached, so a rejected token can
// be proven never to have been stored.
type githubPATFakeStore struct {
	Store
	upserted int
}

func (s *githubPATFakeStore) ListUserProviderConnections(context.Context, domain.Principal) ([]domain.UserProviderConnection, error) {
	return nil, nil
}

func (s *githubPATFakeStore) UpsertUserProviderConnection(
	_ context.Context, _ domain.Principal, provider, label string, _, _ []byte, _ json.RawMessage,
) (domain.UserProviderConnection, error) {
	s.upserted++
	return domain.UserProviderConnection{Provider: provider, Label: label, ValidationState: "valid"}, nil
}

func (s *githubPATFakeStore) DeleteUserProviderConnection(context.Context, domain.Principal, string, string) error {
	return nil
}

func (s *githubPATFakeStore) UserProviderConnectionSecret(context.Context, domain.Principal, string, string) ([]byte, []byte, error) {
	return nil, nil, nil
}

// newGitHubPATTestServer wires a Server whose credential validator points at
// a fake GitHub that answers /user with the given status, so the test
// controls whether the token "works" without a real network call.
func newGitHubPATTestServer(t *testing.T, githubStatus int) (*Server, *githubPATFakeStore) {
	t.Helper()
	key := make([]byte, 32)
	cipher, err := secrets.New(key)
	if err != nil {
		t.Fatalf("cipher: %v", err)
	}
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(githubStatus)
	}))
	t.Cleanup(gh.Close)

	validator := newAgentCredentialValidator(gh.Client())
	validator.githubBaseURL = gh.URL

	store := &githubPATFakeStore{}
	srv := New(Options{
		Store:               store,
		SecretCipher:        cipher,
		CredentialValidator: validator,
		Logger:              slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	return srv, store
}

func githubPATRequest(t *testing.T, secret string) *http.Request {
	t.Helper()
	body, err := json.Marshal(putGitHubPATRequest{Secret: secret})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	req := httptest.NewRequest(http.MethodPut, "/me/github-pat", bytes.NewReader(body))
	ctx := context.WithValue(req.Context(), principalKey, domain.Principal{UserID: "00000000-0000-0000-0000-000000000001"})
	return req.WithContext(ctx)
}

// A token GitHub accepts is stored.
func TestPutGitHubPATAcceptsAWorkingToken(t *testing.T) {
	srv, store := newGitHubPATTestServer(t, http.StatusOK)
	w := httptest.NewRecorder()
	srv.putGitHubPAT(w, githubPATRequest(t, "ghp_validtoken00000000000000000000000"))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if store.upserted != 1 {
		t.Fatalf("UpsertUserProviderConnection called %d times, want 1", store.upserted)
	}
}

// A token GitHub rejects (expired/revoked) must not be stored: this is the
// fix for the token silently reading "Connected" and only failing later,
// inside a sandbox, at checkout time.
func TestPutGitHubPATRejectsAnInvalidToken(t *testing.T) {
	srv, store := newGitHubPATTestServer(t, http.StatusUnauthorized)
	w := httptest.NewRecorder()
	srv.putGitHubPAT(w, githubPATRequest(t, "ghp_expiredtoken0000000000000000000000"))

	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body=%s", w.Code, w.Body.String())
	}
	if store.upserted != 0 {
		t.Fatalf("UpsertUserProviderConnection called %d times, want 0 — a rejected token must never be stored", store.upserted)
	}
}

// GitHub being unreachable is a different failure than a bad token: it must
// not be reported as an invalid credential, and must not be stored either.
func TestPutGitHubPATReportsGitHubUnavailableSeparatelyFromInvalid(t *testing.T) {
	srv, store := newGitHubPATTestServer(t, http.StatusInternalServerError)
	w := httptest.NewRecorder()
	srv.putGitHubPAT(w, githubPATRequest(t, "ghp_sometoken000000000000000000000000"))

	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502; body=%s", w.Code, w.Body.String())
	}
	if store.upserted != 0 {
		t.Fatalf("UpsertUserProviderConnection called %d times, want 0", store.upserted)
	}
}
