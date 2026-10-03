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

type installationListStore struct {
	Store
	installation domain.GitHubInstallation
	deletions    int
}

func (s *installationListStore) ListGitHubInstallations(context.Context, domain.Principal, string) ([]domain.GitHubInstallation, error) {
	return []domain.GitHubInstallation{s.installation}, nil
}

func (s *installationListStore) ApplyGitHubInstallationEvent(_ context.Context, _, _, action, _ string) error {
	if action == "deleted" {
		s.deletions++
		s.installation.Status = "deleted"
	}
	return nil
}

func TestListInstallationsReconcilesUninstalledGitHubApp(t *testing.T) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	client := &Client{
		appID: 1, privateKey: privateKey, apiBaseURL: "https://api.github.test",
		httpClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			if request.Method != http.MethodGet || request.URL.Path != "/app/installations/42" {
				t.Fatalf("unexpected GitHub request: %s %s", request.Method, request.URL.Path)
			}
			return &http.Response{StatusCode: http.StatusNotFound, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(nil))}, nil
		})}, now: time.Now,
	}
	store := &installationListStore{installation: domain.GitHubInstallation{ID: "installation-row", GitHubInstallationID: 42, Status: "active"}}
	service := &Service{store: store, client: client, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	installations, err := service.ListInstallations(context.Background(), domain.Principal{}, "org-id")
	if err != nil {
		t.Fatal(err)
	}
	if len(installations) != 1 || installations[0].Status != "deleted" || store.deletions != 1 {
		t.Fatalf("installations = %+v; deletion events = %d, want deleted/1", installations, store.deletions)
	}
}

func TestListInstallationsKeepsStoredStateWhenGitHubIsUnavailable(t *testing.T) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	client := &Client{
		appID: 1, privateKey: privateKey, apiBaseURL: "https://api.github.test",
		httpClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusServiceUnavailable, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(nil))}, nil
		})}, now: time.Now,
	}
	store := &installationListStore{installation: domain.GitHubInstallation{ID: "installation-row", GitHubInstallationID: 42, Status: "active"}}
	service := &Service{store: store, client: client, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	installations, err := service.ListInstallations(context.Background(), domain.Principal{}, "org-id")
	if err != nil {
		t.Fatal(err)
	}
	if len(installations) != 1 || installations[0].Status != "active" || store.deletions != 0 {
		t.Fatalf("installations = %+v; deletion events = %d, want active/0", installations, store.deletions)
	}
}
