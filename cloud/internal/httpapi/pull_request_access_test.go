package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/pkg/contract"
	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/aoagents/agent-orchestrator/cloud/internal/githubapp"
	"github.com/aoagents/agent-orchestrator/cloud/internal/postgres"
	"github.com/go-chi/chi/v5"
)

type inaccessiblePRStore struct{ Store }

func (inaccessiblePRStore) ListPullRequestsBySession(context.Context, domain.Principal, string, string) ([]domain.PullRequest, error) {
	return []domain.PullRequest{{ID: "pr-1", Provider: "github", Repository: "owner/private", Number: 1, State: contract.PRStateOpen, Mergeability: contract.MergeMergeable}}, nil
}

func (inaccessiblePRStore) PullRequestSnapshot(context.Context, string, string) (domain.PullRequestSnapshot, error) {
	return domain.PullRequestSnapshot{}, nil
}

func (inaccessiblePRStore) GitHubInstallationForRepository(context.Context, string, string) (int64, int64, error) {
	return 0, 0, postgres.ErrNotFound
}

func TestListSessionPullRequestsMarksRevokedAppAccess(t *testing.T) {
	server := New(Options{Store: inaccessiblePRStore{}, GitHub: &githubapp.Service{}})
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	route := chi.NewRouteContext()
	route.URLParams.Add("orgId", "11111111-1111-1111-1111-111111111111")
	route.URLParams.Add("sessionId", "22222222-2222-2222-2222-222222222222")
	request = request.WithContext(context.WithValue(request.Context(), chi.RouteCtxKey, route))
	response := httptest.NewRecorder()
	server.listSessionPullRequests(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var result struct {
		PullRequests []struct {
			Mergeability struct {
				State   string   `json:"state"`
				Reasons []string `json:"reasons"`
			} `json:"mergeability"`
		} `json:"pullRequests"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.PullRequests) != 1 || result.PullRequests[0].Mergeability.State != "unknown" ||
		len(result.PullRequests[0].Mergeability.Reasons) != 1 || result.PullRequests[0].Mergeability.Reasons[0] != "github_access_lost" {
		t.Fatalf("mergeability = %+v, want unknown with github_access_lost", result.PullRequests)
	}
}
