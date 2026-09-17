package githubapp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/pkg/contract"
	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/aoagents/agent-orchestrator/cloud/internal/postgres"
)

type stubTrackerStore struct {
	sessions     []domain.PullRequestTrackingSession
	credentials  map[string]domain.WorkerGitHubPAT
	tracked      map[int]bool
	openRefs     map[string][]domain.PullRequestRef
	claimed      []domain.PullRequest
	observations map[string]domain.PullRequestObservation
}

func (s *stubTrackerStore) PullRequestTrackingSessions(context.Context) ([]domain.PullRequestTrackingSession, error) {
	return s.sessions, nil
}

func (s *stubTrackerStore) SessionGitHubPAT(_ context.Context, _, sessionID string) (domain.WorkerGitHubPAT, error) {
	credential, ok := s.credentials[sessionID]
	if !ok {
		return domain.WorkerGitHubPAT{}, postgres.ErrNotFound
	}
	return credential, nil
}

func (s *stubTrackerStore) OpenPullRequestRefsForSession(_ context.Context, _, sessionID string) ([]domain.PullRequestRef, error) {
	return s.openRefs[sessionID], nil
}

func (s *stubTrackerStore) PullRequestTracked(_ context.Context, _, _, _ string, number int) (bool, error) {
	return s.tracked[number], nil
}

func (s *stubTrackerStore) ClaimPullRequestRecord(
	_ context.Context, orgID, sessionID string, input domain.PullRequest,
) (domain.PullRequest, error) {
	s.claimed = append(s.claimed, input)
	if s.openRefs == nil {
		s.openRefs = map[string][]domain.PullRequestRef{}
	}
	if input.State == contract.PRStateOpen {
		s.openRefs[sessionID] = append(s.openRefs[sessionID], domain.PullRequestRef{
			ID: "rec-new", OrgID: orgID, Provider: input.Provider, Repository: input.Repository, Number: input.Number,
		})
	}
	return input, nil
}

func (s *stubTrackerStore) UpdatePullRequestObservation(
	_ context.Context, _, pullRequestID string, observation domain.PullRequestObservation,
) (domain.PullRequest, error) {
	if s.observations == nil {
		s.observations = map[string]domain.PullRequestObservation{}
	}
	s.observations[pullRequestID] = observation
	return domain.PullRequest{ID: pullRequestID}, nil
}

func decryptTestPAT(domain.WorkerGitHubPAT) (string, error) { return testPAT, nil }

func trackerGitHub(t *testing.T, pulls []map[string]any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer "+testPAT {
			t.Errorf("Authorization = %q, want the PAT as bearer", got)
		}
		switch r.URL.Path {
		case "/repos/octo/widgets/pulls":
			if got := r.URL.Query().Get("head"); got != "octo:ao/session-1" {
				t.Errorf("head filter = %q, want octo:ao/session-1", got)
			}
			_ = json.NewEncoder(w).Encode(pulls)
		case "/repos/octo/widgets/pulls/7":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"number": 7, "state": "open", "mergeable_state": "clean",
				"additions": 12, "deletions": 3, "changed_files": 2,
				"head": map[string]any{"sha": "abc123"},
			})
		case "/repos/octo/widgets/commits/abc123/check-runs":
			_ = json.NewEncoder(w).Encode(map[string]any{"check_runs": []map[string]any{
				{"status": "completed", "conclusion": "failure"},
			}})
		case "/repos/octo/widgets/pulls/7/reviews":
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"id": 1, "state": "APPROVED", "user": map[string]any{"login": "reviewer"}},
			})
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.String())
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func openPull(number int, ref string) map[string]any {
	return map[string]any{
		"number": number, "html_url": "https://github.com/octo/widgets/pull/" + strconv.Itoa(number),
		"state": "open", "title": "Add logging", "user": map[string]any{"login": "octocat"},
		"head": map[string]any{"sha": "abc123", "ref": ref},
		"base": map[string]any{"ref": "main"},
	}
}

// A PR the agent opened from its branch by any means is claimed for the
// session and refreshed in the same scan, as the local daemon's observer does.
func TestPATPullRequestTrackerDiscoversAndRefreshes(t *testing.T) {
	server := trackerGitHub(t, []map[string]any{openPull(7, "ao/session-1")})
	defer server.Close()
	store := &stubTrackerStore{
		sessions: []domain.PullRequestTrackingSession{{OrgID: "org-1", SessionID: "sess-1", Branch: "ao/session-1", Active: true}},
		credentials: map[string]domain.WorkerGitHubPAT{
			"sess-1": {OwnerUserID: "user-1", CloneURL: "https://github.com/octo/widgets.git"},
		},
	}
	tracker := NewPATPullRequestTracker(NewRESTClient(server.URL, server.Client()), store, decryptTestPAT, nil)

	if err := tracker.ScanOnce(context.Background()); err != nil {
		t.Fatalf("ScanOnce: %v", err)
	}
	if len(store.claimed) != 1 {
		t.Fatalf("claimed %d pull requests, want 1", len(store.claimed))
	}
	claimed := store.claimed[0]
	if claimed.Repository != "octo/widgets" || claimed.Number != 7 || claimed.SourceBranch != "ao/session-1" ||
		claimed.TargetBranch != "main" || claimed.HeadSHA != "abc123" || claimed.State != contract.PRStateOpen {
		t.Fatalf("claimed record not plumbed: %+v", claimed)
	}
	observation, ok := store.observations["rec-new"]
	if !ok {
		t.Fatal("discovered pull request was not refreshed")
	}
	if observation.CIState != contract.CIFailing || observation.ReviewState != contract.ReviewApproved ||
		observation.Mergeability != contract.MergeMergeable || observation.Additions != 12 {
		t.Fatalf("observation = %+v", observation)
	}
}

// A PR already tracked by any session is never reclaimed, and a paused
// session only refreshes what it already tracks.
func TestPATPullRequestTrackerSkipsTrackedAndPausedDiscovery(t *testing.T) {
	server := trackerGitHub(t, []map[string]any{openPull(7, "ao/session-1")})
	defer server.Close()
	credential := domain.WorkerGitHubPAT{OwnerUserID: "user-1", CloneURL: "https://github.com/octo/widgets"}
	store := &stubTrackerStore{
		sessions: []domain.PullRequestTrackingSession{
			{OrgID: "org-1", SessionID: "sess-1", Branch: "ao/session-1", Active: true},
			{OrgID: "org-1", SessionID: "sess-2", Branch: "ao/session-2", Active: false},
		},
		credentials: map[string]domain.WorkerGitHubPAT{"sess-1": credential, "sess-2": credential},
		tracked:     map[int]bool{7: true},
		openRefs: map[string][]domain.PullRequestRef{
			"sess-2": {{ID: "rec-7", OrgID: "org-1", Provider: "github", Repository: "octo/widgets", Number: 7}},
		},
	}
	tracker := NewPATPullRequestTracker(NewRESTClient(server.URL, server.Client()), store, decryptTestPAT, nil)

	if err := tracker.ScanOnce(context.Background()); err != nil {
		t.Fatalf("ScanOnce: %v", err)
	}
	if len(store.claimed) != 0 {
		t.Fatalf("reclaimed an already-tracked pull request: %+v", store.claimed)
	}
	if _, ok := store.observations["rec-7"]; !ok {
		t.Fatal("paused session's tracked pull request was not refreshed")
	}
}

// Sessions without a PAT, or on a non-GitHub remote, cost no GitHub calls.
func TestPATPullRequestTrackerIgnoresSessionsWithoutUsablePAT(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected GitHub request %s %s", r.Method, r.URL.String())
	}))
	defer server.Close()
	store := &stubTrackerStore{
		sessions: []domain.PullRequestTrackingSession{
			{OrgID: "org-1", SessionID: "no-pat", Branch: "ao/a", Active: true},
			{OrgID: "org-1", SessionID: "gitlab", Branch: "ao/b", Active: true},
		},
		credentials: map[string]domain.WorkerGitHubPAT{
			"gitlab": {OwnerUserID: "user-1", CloneURL: "https://gitlab.com/octo/widgets"},
		},
	}
	decrypt := func(domain.WorkerGitHubPAT) (string, error) {
		return "", errors.New("must not decrypt for an unusable repository")
	}
	tracker := NewPATPullRequestTracker(NewRESTClient(server.URL, server.Client()), store, decrypt, nil)
	if err := tracker.ScanOnce(context.Background()); err != nil {
		t.Fatalf("ScanOnce: %v", err)
	}
}
