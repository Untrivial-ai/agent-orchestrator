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

	"github.com/aoagents/agent-orchestrator/backend/pkg/contract"
	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/aoagents/agent-orchestrator/cloud/internal/githubapp"
	"github.com/aoagents/agent-orchestrator/cloud/internal/secrets"
	"github.com/go-chi/chi/v5"
)

type mergePRStore struct {
	Store
	pr            domain.PullRequest
	secret, nonce []byte
	lookupCalls   int
}

func (s *mergePRStore) PullRequestForMerge(_ context.Context, _ domain.Principal, _, _ string, number int, prURL string) (domain.PullRequest, error) {
	s.lookupCalls++
	if number != s.pr.Number || prURL != s.pr.URL {
		return domain.PullRequest{}, nil
	}
	return s.pr, nil
}
func (s *mergePRStore) PullRequestSnapshot(context.Context, string, string) (domain.PullRequestSnapshot, error) {
	return domain.PullRequestSnapshot{}, nil
}
func (s *mergePRStore) UserProviderConnectionSecret(context.Context, domain.Principal, string, string) ([]byte, []byte, error) {
	return s.secret, s.nonce, nil
}
func (s *mergePRStore) ListUserProviderConnections(context.Context, domain.Principal) ([]domain.UserProviderConnection, error) {
	return nil, nil
}
func (s *mergePRStore) UpsertUserProviderConnection(context.Context, domain.Principal, string, string, []byte, []byte, json.RawMessage) (domain.UserProviderConnection, error) {
	return domain.UserProviderConnection{}, nil
}
func (s *mergePRStore) DeleteUserProviderConnection(context.Context, domain.Principal, string, string) error {
	return nil
}

func mergePRRequest(body string) *http.Request {
	request := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(body))
	ctx := chi.NewRouteContext()
	ctx.URLParams.Add("orgId", "11111111-1111-1111-1111-111111111111")
	ctx.URLParams.Add("sessionId", "22222222-2222-2222-2222-222222222222")
	ctx.URLParams.Add("number", "7")
	return request.WithContext(context.WithValue(context.WithValue(request.Context(), chi.RouteCtxKey, ctx), principalKey, domain.Principal{UserID: "33333333-3333-3333-3333-333333333333"}))
}

// No GitHub App is configured here (Options.GitHub is nil), so the handler uses
// the caller's PAT — the App-less-deployment path. When an App IS configured it
// is preferred over the PAT; that precedence is verified live on staging (the
// App path needs a full githubapp.Service harness to unit-test).
func TestMergeSessionPullRequestUsesPATAndLeavesStatusForWebhook(t *testing.T) {
	var gotMethod, gotPath, gotAuth string
	var gotBody map[string]string
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath, gotAuth = r.Method, r.URL.Path, r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		if r.URL.Path == "/graphql" {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"repository": map[string]any{"pullRequest": map[string]any{
				"number": 7, "url": "https://github.com/acme/repo/pull/7", "headRefOid": "abc123", "mergeable": "MERGEABLE", "mergeStateStatus": "CLEAN", "reviewDecision": "APPROVED",
			}}}})
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer gh.Close()
	cipher, err := secrets.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	secret, nonce, err := cipher.Encrypt([]byte("test-pat"), providerSecretAssociatedData("user:33333333-3333-3333-3333-333333333333", githubPATProvider))
	if err != nil {
		t.Fatal(err)
	}
	store := &mergePRStore{secret: secret, nonce: nonce, pr: domain.PullRequest{
		ID: "pr", URL: "https://github.com/acme/repo/pull/7", Provider: "github", Repository: "acme/repo", Number: 7,
		State: contract.PRStateOpen, HeadSHA: "abc123", CIState: contract.CIPassing,
		ReviewState: contract.ReviewNone, Mergeability: contract.MergeMergeable,
	}}
	srv := New(Options{Store: store, SecretCipher: cipher, PATWrites: githubapp.NewPATWriteService(githubapp.NewRESTClient(gh.URL, gh.Client()), &patRecordStore{}), Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	w := httptest.NewRecorder()
	srv.mergeSessionPullRequest(w, mergePRRequest(`{"prUrl":"https://github.com/acme/repo/pull/7","expectedHeadSha":"abc123"}`))
	if w.Code != http.StatusAccepted {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	if gotMethod != "PUT" || gotPath != "/repos/acme/repo/pulls/7/merge" || gotAuth != "Bearer test-pat" || gotBody["sha"] != "abc123" || gotBody["merge_method"] != "squash" {
		t.Fatalf("GitHub request = %s %s %s %#v", gotMethod, gotPath, gotAuth, gotBody)
	}
	if store.pr.State != contract.PRStateOpen {
		t.Fatal("merge handler updated status before webhook")
	}
}

func TestMergeSessionPullRequestRejectsStaleHead(t *testing.T) {
	store := &mergePRStore{pr: domain.PullRequest{URL: "https://github.com/acme/repo/pull/7", Number: 7, Provider: "github", State: contract.PRStateOpen, HeadSHA: "new"}}
	srv := New(Options{Store: store, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	w := httptest.NewRecorder()
	srv.mergeSessionPullRequest(w, mergePRRequest(`{"prUrl":"https://github.com/acme/repo/pull/7","expectedHeadSha":"old"}`))
	if w.Code != http.StatusConflict {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
}

func TestPullRequestSnapshotReadyForMergeRejectsFreshBlockers(t *testing.T) {
	ready := domain.PullRequestSnapshot{Observation: domain.PullRequestObservation{
		State: contract.PRStateOpen, HeadSHA: "abc123", CIState: contract.CIPassing,
		ReviewState: contract.ReviewApproved, Mergeability: contract.MergeMergeable,
	}}
	if !pullRequestSnapshotReadyForMerge(ready, "abc123") {
		t.Fatal("ready snapshot rejected")
	}
	tests := []struct {
		name   string
		change func(*domain.PullRequestSnapshot)
	}{
		{"moved head", func(s *domain.PullRequestSnapshot) { s.Observation.HeadSHA = "def456" }},
		{"failing CI", func(s *domain.PullRequestSnapshot) { s.Observation.CIState = contract.CIFailing }},
		{"requested changes", func(s *domain.PullRequestSnapshot) { s.Observation.ReviewState = contract.ReviewChangesRequest }},
		{"partial review window", func(s *domain.PullRequestSnapshot) { s.ReviewsPartial = true }},
		{"unresolved comment", func(s *domain.PullRequestSnapshot) {
			s.Comments = []domain.PullRequestReviewComment{{Author: "reviewer"}}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			candidate := ready
			tt.change(&candidate)
			if pullRequestSnapshotReadyForMerge(candidate, "abc123") {
				t.Fatal("blocked snapshot accepted")
			}
		})
	}
}
