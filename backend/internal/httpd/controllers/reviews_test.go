package controllers_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/config"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	reviewcore "github.com/aoagents/agent-orchestrator/backend/internal/review"
	reviewsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/review"
)

type fakeReviewService struct {
	// triggeredHarness records the override the controller forwarded.
	triggeredHarness  domain.ReviewerHarness
	triggerErr        error
	cancelErr         error
	trigger           reviewcore.TriggerResult
	cancel            reviewcore.CancelResult
	list              reviewcore.SessionReviews
	submitted         []reviewsvc.SubmittedReview
	activityID        string
	activitySignal    reviewsvc.ActivitySignal
	activityErr       error
	killed            bool
	teardown          bool
	restored          bool
	switchedHarness   domain.ReviewerHarness
	rereviewSession   domain.SessionID
	rereviewPRURL     string
	rereviewReviewer  string
	rereviewErr       error
	resolveSession    domain.SessionID
	resolvePRURL      string
	resolveCommentURL string
	resolveErr        error
}

func (f *fakeReviewService) Trigger(
	_ context.Context,
	_ domain.SessionID,
	harness domain.ReviewerHarness,
) (reviewcore.TriggerResult, error) {
	f.triggeredHarness = harness
	if f.triggerErr != nil {
		return reviewcore.TriggerResult{}, f.triggerErr
	}
	if f.trigger.ReviewerHandleID != "" || f.trigger.Run.ID != "" || f.trigger.Reviews != nil || f.trigger.CreatedRuns != nil {
		return f.trigger, nil
	}
	return reviewcore.TriggerResult{Run: domain.ReviewRun{ID: "run-1"}, Created: true}, nil
}

func (f *fakeReviewService) RequestRereview(_ context.Context, workerID domain.SessionID, prURL, reviewer string) error {
	f.rereviewSession = workerID
	f.rereviewPRURL = prURL
	f.rereviewReviewer = reviewer
	return f.rereviewErr
}

func (f *fakeReviewService) ResolveReviewComment(_ context.Context, workerID domain.SessionID, prURL, commentURL string) error {
	f.resolveSession = workerID
	f.resolvePRURL = prURL
	f.resolveCommentURL = commentURL
	return f.resolveErr
}

func (f *fakeReviewService) TriggerAuto(context.Context, domain.SessionID, domain.ReviewerHarness) (reviewcore.TriggerResult, error) {
	return reviewcore.TriggerResult{}, nil
}

func (f *fakeReviewService) Submit(context.Context, domain.SessionID, string, domain.ReviewVerdict, string, string) (domain.ReviewRun, error) {
	return domain.ReviewRun{}, nil
}

func (f *fakeReviewService) ApplyReviewActivitySignal(_ context.Context, reviewSessionID string, signal reviewsvc.ActivitySignal) error {
	f.activityID = reviewSessionID
	f.activitySignal = signal
	return f.activityErr
}

func (f *fakeReviewService) Cancel(context.Context, domain.SessionID) (reviewcore.CancelResult, error) {
	if f.cancelErr != nil {
		return reviewcore.CancelResult{}, f.cancelErr
	}
	return f.cancel, nil
}

func (f *fakeReviewService) TerminateReviewer(context.Context, domain.SessionID, string) error {
	f.killed = true
	f.list.ReviewerHandleID = ""
	return nil
}

func (f *fakeReviewService) TeardownReviewerTerminal(context.Context, domain.SessionID) error {
	f.teardown = true
	f.list.ReviewerHandleID = ""
	return nil
}

func (f *fakeReviewService) RestoreReviewer(context.Context, domain.SessionID) error {
	f.restored = true
	if f.list.ReviewerHandleID == "" {
		f.list.ReviewerHandleID = "review-mer-1"
	}
	return nil
}

func (f *fakeReviewService) SwitchReviewer(_ context.Context, _ domain.SessionID, harness domain.ReviewerHarness) (reviewcore.SessionReviews, error) {
	f.switchedHarness = harness
	f.list.ReviewerHarness = harness
	if f.list.Runs == nil {
		f.list.Runs = []domain.ReviewRun{}
	}
	if f.list.Reviews == nil {
		f.list.Reviews = []reviewcore.PRReviewState{}
	}
	return f.list, nil
}

func (f *fakeReviewService) SubmitMany(_ context.Context, _ domain.SessionID, reviews []reviewsvc.SubmittedReview) ([]domain.ReviewRun, error) {
	f.submitted = append([]reviewsvc.SubmittedReview(nil), reviews...)
	runs := make([]domain.ReviewRun, 0, len(reviews))
	for _, review := range reviews {
		runs = append(runs, domain.ReviewRun{ID: review.RunID, Verdict: review.Verdict, Body: review.Body, GithubReviewID: review.GithubReviewID})
	}
	return runs, nil
}

func (f *fakeReviewService) List(context.Context, domain.SessionID) (reviewcore.SessionReviews, error) {
	return f.list, nil
}

func newReviewTestServer(t *testing.T, svc reviewsvc.Manager) *httptest.Server {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := httptest.NewServer(httpd.NewRouterWithControl(config.Config{}, log, nil, httpd.APIDeps{Reviews: svc}, httpd.ControlDeps{}))
	t.Cleanup(srv.Close)
	return srv
}

func TestReviewsTrigger_MissingReviewerBinaryReturns422WithCause(t *testing.T) {
	err := fmt.Errorf("launch reviewer: reviewer command: claude: %w", ports.ErrAgentBinaryNotFound)
	srv := newReviewTestServer(t, &fakeReviewService{triggerErr: err})

	body, status, headers := doRequest(t, srv, "POST", "/api/v1/sessions/mer-1/reviews/trigger", "")
	assertJSON(t, headers)
	assertErrorCode(t, body, status, http.StatusUnprocessableEntity, "REVIEWER_BINARY_NOT_FOUND")

	var got errorBody
	mustJSON(t, body, &got)
	if !strings.Contains(got.Message, "claude") || !strings.Contains(got.Message, ports.ErrAgentBinaryNotFound.Error()) {
		t.Fatalf("message = %q, want reviewer binary cause", got.Message)
	}
}

func TestReviewActivityPersistsReviewerNativeSessionID(t *testing.T) {
	svc := &fakeReviewService{}
	srv := newReviewTestServer(t, svc)

	body, status, headers := doRequest(t, srv, "POST", "/api/v1/reviews/review-1/activity", `{"event":"session-start","agentSessionId":"native-review-1"}`)
	assertJSON(t, headers)
	if status != http.StatusOK {
		t.Fatalf("status=%d body=%s", status, body)
	}
	if svc.activityID != "review-1" {
		t.Fatalf("activity id = %q, want review-1", svc.activityID)
	}
	if svc.activitySignal.Event != "session-start" || svc.activitySignal.AgentSessionID != "native-review-1" {
		t.Fatalf("activity signal = %+v", svc.activitySignal)
	}
}

func TestReviewsListIncludesReviewStates(t *testing.T) {
	srv := newReviewTestServer(t, &fakeReviewService{list: reviewcore.SessionReviews{
		ReviewerHandleID: "review-mer-1",
		ReviewerHarness:  domain.ReviewerCodex,
		Runs:             []domain.ReviewRun{{ID: "run-1", PRURL: "https://github.com/o/r/pull/1", TargetSHA: "sha1", AutoInjectReview: false}},
		Reviews:          []reviewcore.PRReviewState{{PRURL: "https://github.com/o/r/pull/1", PRNumber: 1, TargetSHA: "sha1", Status: reviewcore.ReviewStateUpToDate}},
	}})

	body, status, headers := doRequest(t, srv, "GET", "/api/v1/sessions/mer-1/reviews", "")
	assertJSON(t, headers)
	if status != http.StatusOK {
		t.Fatalf("status = %d body=%s", status, body)
	}
	if !strings.Contains(string(body), `"reviews"`) || !strings.Contains(string(body), `"up_to_date"`) || !strings.Contains(string(body), `"reviewerHandleId":"review-mer-1"`) || !strings.Contains(string(body), `"reviewerHarness":"codex"`) {
		t.Fatalf("body missing review states/handle: %s", body)
	}
	if !strings.Contains(string(body), `"autoInjectReview":false`) {
		t.Fatalf("body missing stored AO injection decision: %s", body)
	}
	if strings.Contains(string(body), `"items"`) || strings.Contains(string(body), `"reviewItems"`) || strings.Contains(string(body), `"reviewRuns"`) {
		t.Fatalf("body contains deprecated review item aliases: %s", body)
	}
}

func TestReviewsTriggerIncludesBatchFields(t *testing.T) {
	run1 := domain.ReviewRun{ID: "run-1", PRURL: "https://github.com/o/r/pull/1", TargetSHA: "sha1"}
	run2 := domain.ReviewRun{ID: "run-2", PRURL: "https://github.com/o/r/pull/2", TargetSHA: "sha2"}
	srv := newReviewTestServer(t, &fakeReviewService{trigger: reviewcore.TriggerResult{
		Run:              run1,
		ReviewerHandleID: "review-mer-1",
		Created:          true,
		CreatedRuns:      []domain.ReviewRun{run1, run2},
		Runs:             []domain.ReviewRun{run1, run2},
		Reviews: []reviewcore.PRReviewState{
			{PRURL: run1.PRURL, PRNumber: 1, TargetSHA: run1.TargetSHA, Status: reviewcore.ReviewStateRunning, LatestRun: &run1},
			{PRURL: run2.PRURL, PRNumber: 2, TargetSHA: run2.TargetSHA, Status: reviewcore.ReviewStateRunning, LatestRun: &run2},
		},
	}})

	body, status, headers := doRequest(t, srv, "POST", "/api/v1/sessions/mer-1/reviews/trigger", "")
	assertJSON(t, headers)
	if status != http.StatusCreated {
		t.Fatalf("status = %d body=%s", status, body)
	}
	for _, want := range []string{`"reviews"`, `"runs"`, `"running"`, `"run-1"`, `"run-2"`, `"reviewerHandleId":"review-mer-1"`} {
		if !strings.Contains(string(body), want) {
			t.Fatalf("body missing %s: %s", want, body)
		}
	}
	for _, unwanted := range []string{`"reviewItems"`, `"items"`, `"createdReviews"`, `"createdRuns"`, `"reviewRuns"`, `"review":`} {
		if strings.Contains(string(body), unwanted) {
			t.Fatalf("body contains deprecated field %s: %s", unwanted, body)
		}
	}
}

func TestReviewsResolveCommentForwardsPRAndComment(t *testing.T) {
	svc := &fakeReviewService{}
	srv := newReviewTestServer(t, svc)

	body, status, headers := doRequest(t, srv, "POST", "/api/v1/sessions/mer-1/reviews/comments/resolve", `{"pullRequestUrl":"https://github.com/o/r/pull/1","commentUrl":"https://github.com/o/r/pull/1#discussion_r1"}`)
	assertJSON(t, headers)
	if status != http.StatusOK {
		t.Fatalf("status = %d body=%s", status, body)
	}
	if svc.resolveSession != "mer-1" || svc.resolvePRURL != "https://github.com/o/r/pull/1" || svc.resolveCommentURL != "https://github.com/o/r/pull/1#discussion_r1" {
		t.Fatalf("request = session %q pr %q comment %q", svc.resolveSession, svc.resolvePRURL, svc.resolveCommentURL)
	}
	if !strings.Contains(string(body), `"ok":true`) {
		t.Fatalf("body missing ok: %s", body)
	}
}

func TestReviewsRerequestForwardsReviewerAndPR(t *testing.T) {
	svc := &fakeReviewService{}
	srv := newReviewTestServer(t, svc)

	body, status, headers := doRequest(t, srv, "POST", "/api/v1/sessions/mer-1/reviews/rerequest", `{"reviewerId":"prateek","pullRequestUrl":"https://github.com/o/r/pull/1"}`)
	assertJSON(t, headers)
	if status != http.StatusOK {
		t.Fatalf("status = %d body=%s", status, body)
	}
	if svc.rereviewSession != "mer-1" || svc.rereviewReviewer != "prateek" || svc.rereviewPRURL != "https://github.com/o/r/pull/1" {
		t.Fatalf("request = session %q reviewer %q pr %q", svc.rereviewSession, svc.rereviewReviewer, svc.rereviewPRURL)
	}
	if !strings.Contains(string(body), `"ok":true`) {
		t.Fatalf("body missing ok: %s", body)
	}
}

func TestReviewsRerequestInvalidJSON(t *testing.T) {
	srv := newReviewTestServer(t, &fakeReviewService{})

	body, status, headers := doRequest(t, srv, "POST", "/api/v1/sessions/mer-1/reviews/rerequest", `{`)
	assertJSON(t, headers)
	assertErrorCode(t, body, status, http.StatusBadRequest, "INVALID_JSON")
}

func TestReviewsCancelIncludesReviewStates(t *testing.T) {
	srv := newReviewTestServer(t, &fakeReviewService{cancel: reviewcore.CancelResult{
		ReviewerHandleID: "review-mer-1",
		Reviews: []reviewcore.PRReviewState{
			{PRURL: "https://github.com/o/r/pull/1", PRNumber: 1, TargetSHA: "sha1", Status: reviewcore.ReviewStateNeedsReview},
		},
	}})

	body, status, headers := doRequest(t, srv, "POST", "/api/v1/sessions/mer-1/reviews/cancel", "")
	assertJSON(t, headers)
	if status != http.StatusOK {
		t.Fatalf("status = %d body=%s", status, body)
	}
	for _, want := range []string{`"reviews"`, `"needs_review"`, `"reviewerHandleId":"review-mer-1"`} {
		if !strings.Contains(string(body), want) {
			t.Fatalf("body missing %s: %s", want, body)
		}
	}
}

func TestReviewsKillClearsReviewerHandle(t *testing.T) {
	svc := &fakeReviewService{list: reviewcore.SessionReviews{
		ReviewerHandleID: "review-mer-1",
		ReviewerHarness:  domain.ReviewerCodex,
		Reviews:          []reviewcore.PRReviewState{{PRURL: "https://github.com/o/r/pull/1", PRNumber: 1, TargetSHA: "sha1", Status: reviewcore.ReviewStateNeedsReview}},
		Runs:             []domain.ReviewRun{{ID: "run-1", SessionID: "mer-1", Harness: domain.ReviewerCodex}},
	}}
	srv := newReviewTestServer(t, svc)

	body, status, headers := doRequest(t, srv, "POST", "/api/v1/sessions/mer-1/reviews/kill", "")
	assertJSON(t, headers)
	if status != http.StatusOK {
		t.Fatalf("status = %d body=%s", status, body)
	}
	if !svc.killed {
		t.Fatal("TerminateReviewer was not called")
	}
	for _, want := range []string{`"reviewerHandleId":""`, `"reviews"`, `"runs"`, `"run-1"`} {
		if !strings.Contains(string(body), want) {
			t.Fatalf("body missing %s: %s", want, body)
		}
	}
}

func TestReviewsRestoreReturnsReviewerHandle(t *testing.T) {
	svc := &fakeReviewService{list: reviewcore.SessionReviews{
		ReviewerHarness: domain.ReviewerCodex,
		Reviews:         []reviewcore.PRReviewState{{PRURL: "https://github.com/o/r/pull/1", PRNumber: 1, TargetSHA: "sha1", Status: reviewcore.ReviewStateNeedsReview}},
		Runs:            []domain.ReviewRun{{ID: "run-1", SessionID: "mer-1", Harness: domain.ReviewerCodex}},
	}}
	srv := newReviewTestServer(t, svc)

	body, status, headers := doRequest(t, srv, "POST", "/api/v1/sessions/mer-1/reviews/restore", "")
	assertJSON(t, headers)
	if status != http.StatusOK {
		t.Fatalf("status = %d body=%s", status, body)
	}
	if !svc.restored {
		t.Fatal("RestoreReviewer was not called")
	}
	for _, want := range []string{`"reviewerHandleId":"review-mer-1"`, `"reviews"`, `"runs"`, `"run-1"`} {
		if !strings.Contains(string(body), want) {
			t.Fatalf("body missing %s: %s", want, body)
		}
	}
}

func TestReviewsSwitchReturnsAuthoritativeReviewState(t *testing.T) {
	svc := &fakeReviewService{list: reviewcore.SessionReviews{
		ReviewerHandleID: "review-mer-2",
		Reviews:          []reviewcore.PRReviewState{{PRURL: "https://github.com/o/r/pull/1", PRNumber: 1, TargetSHA: "sha1", Status: reviewcore.ReviewStateNeedsReview}},
		Runs:             []domain.ReviewRun{{ID: "run-1", SessionID: "mer-1", Harness: domain.ReviewerClaudeCode}},
	}}
	srv := newReviewTestServer(t, svc)

	body, status, headers := doRequest(t, srv, "POST", "/api/v1/sessions/mer-1/reviews/switch", `{"harness":"claude-code"}`)
	assertJSON(t, headers)
	if status != http.StatusOK {
		t.Fatalf("status = %d body=%s", status, body)
	}
	if svc.switchedHarness != domain.ReviewerClaudeCode {
		t.Fatalf("switched harness = %q, want claude-code", svc.switchedHarness)
	}
	for _, want := range []string{`"reviewerHandleId":"review-mer-2"`, `"reviewerHarness":"claude-code"`, `"reviews"`, `"runs"`, `"run-1"`} {
		if !strings.Contains(string(body), want) {
			t.Fatalf("body missing %s: %s", want, body)
		}
	}
}

func TestReviewsSubmitAcceptsBatchedReviews(t *testing.T) {
	svc := &fakeReviewService{}
	srv := newReviewTestServer(t, svc)

	body, status, headers := doRequest(t, srv, "POST", "/api/v1/sessions/mer-1/reviews/submit", `{"reviews":[{"runId":"run-1","verdict":"changes_requested","body":"fix auth","githubReviewId":"101"},{"runId":"run-2","verdict":"approved"}]}`)
	assertJSON(t, headers)
	if status != http.StatusOK {
		t.Fatalf("status = %d body=%s", status, body)
	}
	if len(svc.submitted) != 2 || svc.submitted[0].RunID != "run-1" || svc.submitted[1].Verdict != domain.VerdictApproved {
		t.Fatalf("submitted = %+v", svc.submitted)
	}
	for _, want := range []string{`"reviews"`, `"run-1"`, `"run-2"`} {
		if !strings.Contains(string(body), want) {
			t.Fatalf("body missing %s: %s", want, body)
		}
	}
}
