package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/pkg/contract"
	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/go-chi/chi/v5"
)

type reviewFeedbackStore struct {
	Store
	runs []domain.ReviewRunPullRequest
	sent string
	key  string
}

func (s *reviewFeedbackStore) ListReviewRunsBySession(
	context.Context, domain.Principal, string, string,
) ([]domain.ReviewRunPullRequest, error) {
	return s.runs, nil
}

func (s *reviewFeedbackStore) SendMessage(
	_ context.Context,
	_ domain.Principal,
	_, _ string,
	key, text string,
) (domain.ClientEvent, error) {
	s.key = key
	s.sent = text
	return domain.ClientEvent{SessionID: testChildID, Sequence: 12, Type: "chat.user_message", CreatedAt: time.Now()}, nil
}

func TestSendReviewToWorkerQueuesStoredReview(t *testing.T) {
	const runID = "dddddddd-dddd-4ddd-8ddd-dddddddddddd"
	store := &reviewFeedbackStore{runs: []domain.ReviewRunPullRequest{{
		ReviewRun: domain.ReviewRun{
			ID: runID, Status: contract.AOReviewRunDelivered,
			Verdict: contract.AOReviewVerdictChangesRequested,
			Body:    "Tighten validation and add a regression test.",
			Harness: "codex", ProviderReviewID: "98765",
		},
		PullRequestNumber: 7,
		PullRequestURL:    "https://github.com/acme/repo/pull/7",
	}}}
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.Header.Set("Idempotency-Key", "review-feedback-run-7")
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("orgId", testOrgID)
	rctx.URLParams.Add("sessionId", testChildID)
	rctx.URLParams.Add("reviewRunId", runID)
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = context.WithValue(ctx, principalKey, domain.Principal{UserID: "user-1"})
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	testServer(store).sendReviewToWorker(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body=%s", w.Code, w.Body.String())
	}
	if store.key != "review-feedback-run-7" {
		t.Fatalf("idempotency key = %q", store.key)
	}
	for _, want := range []string{
		"AO agent review from codex",
		"Review summary:\nTighten validation and add a regression test.",
		"Review URL: https://github.com/acme/repo/pull/7#pullrequestreview-98765",
	} {
		if !strings.Contains(store.sent, want) {
			t.Fatalf("queued message missing %q:\n%s", want, store.sent)
		}
	}
}
