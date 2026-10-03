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
	"github.com/aoagents/agent-orchestrator/cloud/internal/postgres"
)

type appliedInstallationEvent struct {
	orgID          string
	installationID string
	action         string
}

// webhookFanoutStore records the per-organization installation events a single
// webhook delivery produces, so a test can assert the delivery fans out to every
// organization that connected the installation.
type webhookFanoutStore struct {
	Store
	routes  []domain.GitHubInstallationRoute
	applied []appliedInstallationEvent
}

func (s *webhookFanoutStore) GitHubInstallationRoutes(
	context.Context, int64,
) ([]domain.GitHubInstallationRoute, error) {
	if len(s.routes) == 0 {
		return nil, postgres.ErrNotFound
	}
	return s.routes, nil
}

func (s *webhookFanoutStore) ApplyGitHubInstallationEvent(
	_ context.Context, orgID, installationID, action, _ string,
) error {
	s.applied = append(s.applied, appliedInstallationEvent{orgID, installationID, action})
	return nil
}

func newSuspendedInstallationService(t *testing.T, store Store) *Service {
	t.Helper()
	privateKey, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatalf("generate test key: %v", err)
	}
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/app/installations/42" {
			t.Fatalf("unexpected GitHub request: %s", request.URL.String())
		}
		body := `{"id":42,"account":{"id":7,"login":"octo-org","type":"Organization"},` +
			`"repository_selection":"all","permissions":{},"events":[],` +
			`"suspended_at":"2026-01-01T00:00:00Z"}`
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
	service, err := NewService(
		store, client, bytes.Repeat([]byte{1}, 32), bytes.Repeat([]byte{2}, 32),
		"webhook-secret", time.Minute, slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	return service
}

// A suspend webhook for an installation that two organizations connected must
// apply the suspension to both organizations' installation records, not just
// one — the whole point of allowing multiple orgs to share an installation.
func TestInstallationWebhookFansOutToEveryConnectedOrg(t *testing.T) {
	store := &webhookFanoutStore{
		routes: []domain.GitHubInstallationRoute{
			{OrgID: "org-a", InstallationID: "install-a"},
			{OrgID: "org-b", InstallationID: "install-b"},
		},
	}
	service := newSuspendedInstallationService(t, store)
	delivery := domain.GitHubWebhookDelivery{
		DeliveryID: "delivery-suspend", Event: "installation", Action: "suspend",
		GitHubInstallationID: 42, Payload: []byte(`{"action":"suspend"}`),
	}

	if err := service.processWebhook(context.Background(), delivery); err != nil {
		t.Fatalf("process installation webhook: %v", err)
	}

	if len(store.applied) != 2 {
		t.Fatalf("applied events = %+v, want one per connected org", store.applied)
	}
	seen := map[string]string{}
	for _, event := range store.applied {
		if event.action != "suspend" {
			t.Errorf("org %s action = %q, want suspend", event.orgID, event.action)
		}
		seen[event.orgID] = event.installationID
	}
	if seen["org-a"] != "install-a" || seen["org-b"] != "install-b" {
		t.Fatalf("expected suspension applied to both orgs, got %+v", seen)
	}
}
