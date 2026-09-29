package githubapp

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"io"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
)

type installOAuthStore struct {
	Store
	attempt          domain.GitHubInstallAttempt
	installStateHash []byte
	oauthStateHash   []byte
}

func (store *installOAuthStore) ValidateGitHubInstallState(_ context.Context, _ []byte) error {
	return nil
}

func (store *installOAuthStore) BeginGitHubOAuth(
	_ context.Context,
	installStateHash []byte,
	installation domain.GitHubInstallation,
	oauthStateHash, verifierCiphertext, verifierNonce []byte,
	expiresAt time.Time,
) (domain.GitHubInstallAttempt, error) {
	store.installStateHash = append([]byte(nil), installStateHash...)
	store.oauthStateHash = append([]byte(nil), oauthStateHash...)
	store.attempt = domain.GitHubInstallAttempt{
		Phase:                       "oauth",
		PendingGitHubInstallationID: installation.GitHubInstallationID,
		OAuthVerifierCiphertext:     verifierCiphertext,
		OAuthVerifierNonce:          verifierNonce,
		ExpiresAt:                   expiresAt,
	}
	return store.attempt, nil
}

func (store *installOAuthStore) GitHubOAuthAttempt(_ context.Context, _ []byte) (domain.GitHubInstallAttempt, error) {
	return store.attempt, nil
}

func (store *installOAuthStore) CompleteGitHubInstallation(
	_ context.Context,
	_ []byte,
	installation domain.GitHubInstallation,
) (domain.GitHubInstallation, error) {
	installation.ID = "installation-record"
	return installation, nil
}

func TestCompleteInstallationOAuthUsesInstallationStateWithoutPKCE(t *testing.T) {
	t.Parallel()

	privateKey, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatalf("generate test key: %v", err)
	}
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		var body string
		switch request.URL.Path {
		case "/app/installations/42":
			body = `{"id":42,"account":{"id":7,"login":"octocat","type":"User"},"repository_selection":"selected","permissions":{},"events":[]}`
		case "/login/oauth/access_token":
			body = `{"access_token":"github-token"}`
		case "/user/installations":
			body = `{"installations":[{"id":42}]}`
		case "/user":
			body = `{"id":7}`
		default:
			t.Fatalf("unexpected GitHub request: %s", request.URL.String())
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": {"application/json"}},
			Body:       io.NopCloser(bytes.NewBufferString(body)),
		}, nil
	})
	client := &Client{
		appID: 1, clientID: "client-id", clientSecret: "client-secret",
		privateKey: privateKey, publicURL: "https://cloud.example.test",
		apiBaseURL: "https://api.github.test", webBaseURL: "https://github.test",
		httpClient: &http.Client{Transport: transport}, now: time.Now,
	}
	store := &installOAuthStore{}
	service, err := NewService(
		store, client, bytes.Repeat([]byte{1}, 32), bytes.Repeat([]byte{2}, 32),
		"webhook-secret", time.Minute, slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	if err != nil {
		t.Fatalf("new service: %v", err)
	}

	installation, err := service.CompleteInstallationOAuth(context.Background(), "install-state", "oauth-code", 42)
	if err != nil {
		t.Fatalf("complete installation OAuth: %v", err)
	}
	if installation.ID != "installation-record" {
		t.Fatalf("installation ID = %q, want installation-record", installation.ID)
	}
	if !bytes.Equal(store.installStateHash, store.oauthStateHash) {
		t.Fatal("direct installation authorization did not retain the installation state")
	}
	verifier, err := Decrypt(
		bytes.Repeat([]byte{1}, 32), store.attempt.OAuthVerifierCiphertext,
		store.attempt.OAuthVerifierNonce, []byte("42"),
	)
	if err != nil {
		t.Fatalf("decrypt verifier: %v", err)
	}
	if len(verifier) != 0 {
		t.Fatalf("verifier = %q, want empty", verifier)
	}
}
