package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/aoagents/agent-orchestrator/cloud/internal/githubapp"
	"github.com/aoagents/agent-orchestrator/cloud/internal/postgres"
	"github.com/aoagents/agent-orchestrator/cloud/internal/worker"
)

// mockCheckoutBroker provides a mock implementation of CheckoutBroker for handler tests.
type mockCheckoutBroker struct {
	raisePR func(context.Context, string, string, domain.RaisePullRequest) (domain.PullRequest, error)
	claimPR func(context.Context, string, string, string) (domain.PullRequest, error)
	submitReview func(context.Context, string, string, string, domain.SubmitReviewResult) (domain.ReviewRun, error)
}

func (m *mockCheckoutBroker) IssueCheckoutGrant(ctx context.Context, orgID, sessionID string) (githubapp.CheckoutGrant, error) {
	return githubapp.CheckoutGrant{}, errors.New("not implemented")
}
func (m *mockCheckoutBroker) IssuePushGrant(ctx context.Context, orgID, sessionID string) (githubapp.CheckoutGrant, error) {
	return githubapp.CheckoutGrant{}, errors.New("not implemented")
}
func (m *mockCheckoutBroker) RaisePullRequest(ctx context.Context, orgID, sessionID string, input domain.RaisePullRequest) (domain.PullRequest, error) {
	if m.raisePR != nil {
		return m.raisePR(ctx, orgID, sessionID, input)
	}
	return domain.PullRequest{}, errors.New("not implemented")
}
func (m *mockCheckoutBroker) ClaimPullRequest(ctx context.Context, orgID, sessionID, reference string) (domain.PullRequest, error) {
	if m.claimPR != nil {
		return m.claimPR(ctx, orgID, sessionID, reference)
	}
	return domain.PullRequest{}, errors.New("not implemented")
}
func (m *mockCheckoutBroker) SubmitReview(ctx context.Context, orgID, sessionID, reviewRunID string, result domain.SubmitReviewResult) (domain.ReviewRun, error) {
	if m.submitReview != nil {
		return m.submitReview(ctx, orgID, sessionID, reviewRunID, result)
	}
	return domain.ReviewRun{}, errors.New("not implemented")
}

// Helper functions for test server and worker claim injection.


func newHandlerServer(broker CheckoutBroker) *Server {
	return &Server{
		checkoutBroker: broker,
		logger:         slog.Default(),
	}
}

// withWorkerClaims injects worker claims into the request context so that
// workerFrom(r) returns valid claims with the required scopes.
func withWorkerClaims(r *http.Request, orgID, sessionID string, scopes ...string) *http.Request {
	claims := worker.Claims{
		OrgID:     orgID,
		SessionID: sessionID,
		WorkerID:  sessionID + ":1",
		Epoch:     1,
		Scopes:    scopes,
	}
	ctx := context.WithValue(r.Context(), workerContextKey{}, claims)
	return r.WithContext(ctx)
}

func workerBody(v any) *bytes.Reader {
	b, _ := json.Marshal(v)
	return bytes.NewReader(b)
}

// workerRaisePullRequest maps ErrRemoteWriteNotSupported to 403 WRITE_NOT_SUPPORTED.


func TestWorkerRaisePullRequest_WriteNotSupported_Returns403(t *testing.T) {
	broker := &mockCheckoutBroker{
		raisePR: func(_ context.Context, _, _ string, _ domain.RaisePullRequest) (domain.PullRequest, error) {
			return domain.PullRequest{}, githubapp.ErrRemoteWriteNotSupported
		},
	}
	s := newHandlerServer(broker)

	body := workerBody(map[string]any{
		"title":      "My feature",
		"headBranch": "feature/x",
	})
	req := httptest.NewRequest(http.MethodPost, "/worker/raise-pr", body)
	req = withWorkerClaims(req, "org-1", "sess-1", "worker:git")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	s.workerRaisePullRequest(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("want 403, got %d", w.Code)
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp["code"] != "WRITE_NOT_SUPPORTED" {
		t.Errorf("want code=WRITE_NOT_SUPPORTED, got %v", resp["code"])
	}
}

// workerRaisePullRequest maps postgres.ErrInvalid to 400 INVALID_PULL_REQUEST.


func TestWorkerRaisePullRequest_InvalidBranches_Returns400(t *testing.T) {
	broker := &mockCheckoutBroker{
		raisePR: func(_ context.Context, _, _ string, _ domain.RaisePullRequest) (domain.PullRequest, error) {
			return domain.PullRequest{}, postgres.ErrInvalid
		},
	}
	s := newHandlerServer(broker)

	body := workerBody(map[string]any{
		"title":      "My feature",
		"headBranch": "feature/x",
	})
	req := httptest.NewRequest(http.MethodPost, "/worker/raise-pr", body)
	req = withWorkerClaims(req, "org-1", "sess-1", "worker:git")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	s.workerRaisePullRequest(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("want 400, got %d", w.Code)
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp["code"] != "INVALID_PULL_REQUEST" {
		t.Errorf("want code=INVALID_PULL_REQUEST, got %v", resp["code"])
	}
}

// workerSubmitReview maps postgres.ErrForbidden to 403 Forbidden.


func TestWorkerSubmitReview_WrongSession_Returns403(t *testing.T) {
	broker := &mockCheckoutBroker{
		submitReview: func(_ context.Context, _, _, _ string, _ domain.SubmitReviewResult) (domain.ReviewRun, error) {
			return domain.ReviewRun{}, postgres.ErrForbidden
		},
	}
	s := newHandlerServer(broker)

	body := workerBody(map[string]any{
		"verdict": "approved",
		"body":    "LGTM",
	})
	req := httptest.NewRequest(http.MethodPost, "/worker/sess-1/review/run-1/submit", body)
	req = withWorkerClaims(req, "org-1", "sess-1", "worker:git")
	req.Header.Set("Content-Type", "application/json")
	// Inject chi URL param so the handler can read the reviewRunId.
	// The handler calls requireUUID, so we must use a real UUID.
	const testRunID = "11111111-1111-1111-1111-111111111111"
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("reviewRunId", testRunID)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	// Re-inject worker claims after the chi context was added.
	claims := worker.Claims{
		OrgID: "org-1", SessionID: "sess-1", WorkerID: "sess-1:1", Epoch: 1,
		Scopes: []string{"worker:git"},
	}
	req = req.WithContext(context.WithValue(req.Context(), workerContextKey{}, claims))
	w := httptest.NewRecorder()

	s.workerSubmitReview(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("want 403, got %d", w.Code)
	}
}
