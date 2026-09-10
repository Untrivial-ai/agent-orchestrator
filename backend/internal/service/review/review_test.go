package review

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/lifecycle"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	reviewcore "github.com/aoagents/agent-orchestrator/backend/internal/review"
)

type fakeStore struct {
	run                     domain.ReviewRun
	ok                      bool
	review                  domain.Review
	reviewOK                bool
	batchRuns               []domain.ReviewRun
	prs                     []domain.PullRequest
	prReviews               map[string][]domain.PullRequestReview
	prComments              map[string][]domain.PullRequestComment
	sessionAutoInjectReview *bool

	updateCalls        int
	agentSessionUpdate int
	markCalls          int
	markedIDs          []string
	resolvedCommentIDs []string
}

func (f *fakeStore) GetReviewByID(_ context.Context, id string) (domain.Review, bool, error) {
	if f.reviewOK && f.review.ID == id {
		return f.review, true, nil
	}
	return domain.Review{}, false, nil
}

func (f *fakeStore) UpdateReviewAgentSessionID(_ context.Context, id, agentSessionID string) (bool, error) {
	if !f.reviewOK || f.review.ID != id {
		return false, nil
	}
	f.agentSessionUpdate++
	f.review.AgentSessionID = agentSessionID
	return true, nil
}

func (f *fakeStore) GetReviewRun(_ context.Context, id string) (domain.ReviewRun, bool, error) {
	for _, run := range f.batchRuns {
		if run.ID == id {
			return run, true, nil
		}
	}
	if f.ok && f.run.ID == id {
		return f.run, true, nil
	}
	return domain.ReviewRun{}, false, nil
}

func (f *fakeStore) GetSession(_ context.Context, id domain.SessionID) (domain.SessionRecord, bool, error) {
	enabled := true
	if f.sessionAutoInjectReview != nil {
		enabled = *f.sessionAutoInjectReview
	}
	return domain.SessionRecord{ID: id, AutoInjectReview: enabled}, true, nil
}

func (f *fakeStore) UpdateReviewRunResult(_ context.Context, id string, status domain.ReviewRunStatus, verdict domain.ReviewVerdict, body, githubReviewID string, autoInjectReview bool) (bool, error) {
	for i := range f.batchRuns {
		if f.batchRuns[i].ID == id {
			if f.batchRuns[i].Status != domain.ReviewRunRunning {
				return false, nil
			}
			f.updateCalls++
			f.batchRuns[i].Status = status
			f.batchRuns[i].Verdict = verdict
			f.batchRuns[i].Body = body
			f.batchRuns[i].GithubReviewID = githubReviewID
			f.batchRuns[i].AutoInjectReview = autoInjectReview
			if f.run.ID == id {
				f.run = f.batchRuns[i]
			}
			return true, nil
		}
	}
	if f.run.Status != domain.ReviewRunRunning {
		return false, nil
	}
	f.updateCalls++
	f.run.Status = status
	f.run.Verdict = verdict
	f.run.Body = body
	f.run.GithubReviewID = githubReviewID
	f.run.AutoInjectReview = autoInjectReview
	return true, nil
}

func (f *fakeStore) MarkReviewRunDelivered(_ context.Context, id string, deliveredAt time.Time) (bool, error) {
	f.markCalls++
	f.markedIDs = append(f.markedIDs, id)
	if f.run.ID == id && f.run.Status == domain.ReviewRunComplete && f.run.DeliveredAt == nil {
		f.run.Status = domain.ReviewRunDelivered
		f.run.DeliveredAt = &deliveredAt
	}
	for i := range f.batchRuns {
		if f.batchRuns[i].ID == id && f.batchRuns[i].Status == domain.ReviewRunComplete && f.batchRuns[i].DeliveredAt == nil {
			f.batchRuns[i].Status = domain.ReviewRunDelivered
			f.batchRuns[i].DeliveredAt = &deliveredAt
			return true, nil
		}
	}
	if f.run.ID != id || f.run.Status != domain.ReviewRunDelivered {
		return false, nil
	}
	return true, nil
}

func (f *fakeStore) ListReviewRunsByBatch(context.Context, domain.SessionID, string) ([]domain.ReviewRun, error) {
	out := append([]domain.ReviewRun(nil), f.batchRuns...)
	return out, nil
}

func (f *fakeStore) ListPRsBySession(context.Context, domain.SessionID) ([]domain.PullRequest, error) {
	out := append([]domain.PullRequest(nil), f.prs...)
	return out, nil
}

func (f *fakeStore) ListPRReviews(_ context.Context, prURL string) ([]domain.PullRequestReview, error) {
	out := append([]domain.PullRequestReview(nil), f.prReviews[prURL]...)
	return out, nil
}

func (f *fakeStore) ListPRComments(_ context.Context, prURL string) ([]domain.PullRequestComment, error) {
	out := append([]domain.PullRequestComment(nil), f.prComments[prURL]...)
	return out, nil
}

func (f *fakeStore) MarkPRCommentResolved(_ context.Context, prURL, commentID string) (bool, error) {
	f.resolvedCommentIDs = append(f.resolvedCommentIDs, commentID)
	comments := f.prComments[prURL]
	for i := range comments {
		if comments[i].ID == commentID {
			comments[i].Resolved = true
			f.prComments[prURL] = comments
			return true, nil
		}
	}
	return false, nil
}

type fakeReviewResolver struct {
	request ports.SCMReviewResolveRequest
	err     error
}

func (f *fakeReviewResolver) ResolveReviewThread(_ context.Context, request ports.SCMReviewResolveRequest) error {
	f.request = request
	return f.err
}

type fakeReviewRequester struct {
	request ports.SCMReviewRequest
	err     error
}

func (f *fakeReviewRequester) RequestReview(_ context.Context, request ports.SCMReviewRequest) error {
	f.request = request
	return f.err
}

func TestResolveReviewCommentResolvesTrackedThread(t *testing.T) {
	prURL := "https://github.com/acme/widget/pull/7"
	commentURL := "https://github.com/acme/widget/pull/7#discussion_r1"
	store := &fakeStore{
		prs: []domain.PullRequest{{URL: prURL, Number: 7, Provider: "github", Repo: "acme/widget"}},
		prComments: map[string][]domain.PullRequestComment{
			prURL: {{ThreadID: "thread-1", ID: "comment-1", URL: commentURL}},
		},
	}
	resolver := &fakeReviewResolver{}
	svc := New(nil, store, WithReviewResolver(resolver))

	if err := svc.ResolveReviewComment(context.Background(), "mer-1", prURL, commentURL); err != nil {
		t.Fatal(err)
	}
	if resolver.request.ThreadID != "thread-1" || resolver.request.PR.Number != 7 {
		t.Fatalf("request = %#v", resolver.request)
	}
	if got := store.resolvedCommentIDs; len(got) != 1 || got[0] != "comment-1" {
		t.Fatalf("resolved comment ids = %#v", got)
	}
	if !store.prComments[prURL][0].Resolved {
		t.Fatalf("comment was not marked resolved: %#v", store.prComments[prURL][0])
	}
}

func TestResolveReviewCommentDoesNotPersistWhenProviderFails(t *testing.T) {
	prURL := "https://github.com/acme/widget/pull/7"
	commentURL := "https://github.com/acme/widget/pull/7#discussion_r1"
	store := &fakeStore{
		prs: []domain.PullRequest{{URL: prURL, Number: 7, Provider: "github", Repo: "acme/widget"}},
		prComments: map[string][]domain.PullRequestComment{
			prURL: {{ThreadID: "thread-1", ID: "comment-1", URL: commentURL}},
		},
	}
	resolver := &fakeReviewResolver{err: errors.New("provider down")}
	svc := New(nil, store, WithReviewResolver(resolver))

	if err := svc.ResolveReviewComment(context.Background(), "mer-1", prURL, commentURL); err == nil {
		t.Fatal("ResolveReviewComment error = nil, want provider failure")
	}
	if len(store.resolvedCommentIDs) != 0 {
		t.Fatalf("resolved comment ids = %#v, want none", store.resolvedCommentIDs)
	}
	if store.prComments[prURL][0].Resolved {
		t.Fatalf("comment was marked resolved after provider failure")
	}
}

func TestRequestRereviewRequestsReviewerForTrackedPR(t *testing.T) {
	prURL := "https://github.com/acme/widget/pull/7"
	store := &fakeStore{
		prs: []domain.PullRequest{{
			URL:      prURL,
			Number:   7,
			Provider: "github",
			Host:     "github.com",
			Repo:     "acme/widget",
		}},
		prReviews: map[string][]domain.PullRequestReview{
			prURL: {{Author: "prateek"}},
		},
	}
	requester := &fakeReviewRequester{}
	svc := New(nil, store, WithReviewRequester(requester))

	if err := svc.RequestRereview(context.Background(), "mer-1", prURL, "@prateek"); err != nil {
		t.Fatal(err)
	}
	if requester.request.Reviewer != "prateek" || requester.request.PR.Number != 7 || requester.request.PR.Repo.Owner != "acme" || requester.request.PR.Repo.Name != "widget" {
		t.Fatalf("request = %#v", requester.request)
	}
}

func TestRequestRereviewRejectsUnknownReviewer(t *testing.T) {
	prURL := "https://github.com/acme/widget/pull/7"
	store := &fakeStore{
		prs:       []domain.PullRequest{{URL: prURL, Number: 7, Provider: "github", Repo: "acme/widget"}},
		prReviews: map[string][]domain.PullRequestReview{prURL: {{Author: "someone-else"}}},
	}
	svc := New(nil, store, WithReviewRequester(&fakeReviewRequester{}))

	if err := svc.RequestRereview(context.Background(), "mer-1", prURL, "prateek"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("error = %v, want ErrInvalid", err)
	}
}

type fakeReducer struct {
	outcome    lifecycle.ReviewDeliveryOutcome
	err        error
	batchCalls int
	gotBatchID string
	gotBatch   []lifecycle.ReviewResult
}

func (f *fakeReducer) ApplyReviewBatch(_ context.Context, _ domain.SessionID, batchID string, results []lifecycle.ReviewResult) (lifecycle.ReviewDeliveryOutcome, error) {
	f.batchCalls++
	f.gotBatchID = batchID
	f.gotBatch = append([]lifecycle.ReviewResult(nil), results...)
	return f.outcome, f.err
}

func TestSubmitPersistsThenAppliesThenStampsDelivered(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	st := &fakeStore{
		ok:  true,
		run: domain.ReviewRun{ID: "run-1", SessionID: "mer-1", BatchID: "batch-1", PRURL: "pr1", TargetSHA: "sha1", Status: domain.ReviewRunRunning},
		prs: []domain.PullRequest{{URL: "pr1", HeadSHA: "sha1"}},
	}
	reducer := &fakeReducer{outcome: lifecycle.ReviewDeliverySent}
	svc := New(nil, st, WithLifecycleReducer(reducer), WithClock(func() time.Time { return now }))

	run, err := svc.Submit(context.Background(), "mer-1", "run-1", domain.VerdictChangesRequested, "fix it", "987")
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if st.updateCalls != 1 || reducer.batchCalls != 1 || st.markCalls != 1 {
		t.Fatalf("calls update/reducer/mark = %d/%d/%d", st.updateCalls, reducer.batchCalls, st.markCalls)
	}
	if reducer.gotBatch[0].Verdict != domain.VerdictChangesRequested || reducer.gotBatch[0].Body != "fix it" || reducer.gotBatch[0].GithubReviewID != "987" {
		t.Fatalf("reducer saw wrong result: %+v", reducer.gotBatch)
	}
	if run.Status != domain.ReviewRunDelivered || run.DeliveredAt == nil || !run.DeliveredAt.Equal(now) {
		t.Fatalf("run not stamped delivered: %+v", run)
	}
}

func TestApplyReviewActivitySignalPersistsNativeReviewerSessionID(t *testing.T) {
	st := &fakeStore{
		reviewOK: true,
		review:   domain.Review{ID: "review-1", SessionID: "worker-1", Harness: domain.ReviewerOpenCode, AgentSessionID: "old-native"},
	}
	svc := New(nil, st)

	if err := svc.ApplyReviewActivitySignal(context.Background(), "review-1", ActivitySignal{
		Event:          "session-start",
		AgentSessionID: "opencode-native-2",
	}); err != nil {
		t.Fatalf("ApplyReviewActivitySignal: %v", err)
	}
	if st.agentSessionUpdate != 1 || st.review.AgentSessionID != "opencode-native-2" {
		t.Fatalf("agent session update calls=%d review=%+v", st.agentSessionUpdate, st.review)
	}
	if st.review.SessionID != "worker-1" {
		t.Fatalf("worker session id changed: %+v", st.review)
	}
}

func TestApplyReviewActivitySignalRequiresExistingReviewSession(t *testing.T) {
	svc := New(nil, &fakeStore{})

	err := svc.ApplyReviewActivitySignal(context.Background(), "missing-review", ActivitySignal{AgentSessionID: "native-1"})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestSubmitSnapshotsDisabledPolicyAndNeverDeliversOnRetry(t *testing.T) {
	disabled := false
	st := &fakeStore{
		ok:                      true,
		sessionAutoInjectReview: &disabled,
		run: domain.ReviewRun{
			ID: "run-1", SessionID: "mer-1", BatchID: "batch-1", PRURL: "pr1", TargetSHA: "sha1", Status: domain.ReviewRunRunning,
		},
		prs: []domain.PullRequest{{URL: "pr1", HeadSHA: "sha1"}},
	}
	reducer := &fakeReducer{outcome: lifecycle.ReviewDeliverySent}
	svc := New(nil, st, WithLifecycleReducer(reducer))

	run, err := svc.Submit(context.Background(), "mer-1", "run-1", domain.VerdictChangesRequested, "fix it", "987")
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != domain.ReviewRunComplete || run.AutoInjectReview || reducer.batchCalls != 0 || st.markCalls != 0 {
		t.Fatalf("disabled review = %+v reducerCalls=%d markCalls=%d", run, reducer.batchCalls, st.markCalls)
	}

	enabled := true
	st.sessionAutoInjectReview = &enabled
	run, err = svc.Submit(context.Background(), "mer-1", "run-1", domain.VerdictChangesRequested, "fix it", "987")
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != domain.ReviewRunComplete || run.AutoInjectReview || reducer.batchCalls != 0 || st.markCalls != 0 {
		t.Fatalf("retry rewrote or delivered disabled review = %+v reducerCalls=%d markCalls=%d", run, reducer.batchCalls, st.markCalls)
	}
}

func TestSubmitBatchRunDoesNotWaitForOtherRunningRuns(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	st := &fakeStore{
		ok:  true,
		run: domain.ReviewRun{ID: "run-1", SessionID: "mer-1", BatchID: "batch-1", PRURL: "pr1", TargetSHA: "sha1", Status: domain.ReviewRunRunning},
		batchRuns: []domain.ReviewRun{
			{ID: "run-1", SessionID: "mer-1", BatchID: "batch-1", PRURL: "pr1", TargetSHA: "sha1", Status: domain.ReviewRunRunning},
			{ID: "run-2", SessionID: "mer-1", BatchID: "batch-1", PRURL: "pr2", TargetSHA: "sha2", Status: domain.ReviewRunRunning},
		},
		prs: []domain.PullRequest{{URL: "pr1", HeadSHA: "sha1"}, {URL: "pr2", HeadSHA: "sha2"}},
	}
	reducer := &fakeReducer{outcome: lifecycle.ReviewDeliverySent}
	svc := New(nil, st, WithLifecycleReducer(reducer), WithClock(func() time.Time { return now }))

	run, err := svc.Submit(context.Background(), "mer-1", "run-1", domain.VerdictChangesRequested, "fix pr1", "101")
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if run.Status != domain.ReviewRunDelivered || run.DeliveredAt == nil || !run.DeliveredAt.Equal(now) {
		t.Fatalf("first submit status = %+v, want delivered", run)
	}
	if reducer.batchCalls != 1 || len(reducer.gotBatch) != 1 || reducer.gotBatch[0].RunID != "run-1" || st.markCalls != 1 {
		t.Fatalf("submitted run should deliver independently: batchCalls=%d got=%+v markCalls=%d", reducer.batchCalls, reducer.gotBatch, st.markCalls)
	}
}

func TestSubmitManySendsCombinedChangesRequested(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	st := &fakeStore{
		ok: true,
		batchRuns: []domain.ReviewRun{
			{ID: "run-1", SessionID: "mer-1", BatchID: "batch-1", PRURL: "pr1", TargetSHA: "sha1", Status: domain.ReviewRunRunning},
			{ID: "run-2", SessionID: "mer-1", BatchID: "batch-1", PRURL: "pr2", TargetSHA: "sha2", Status: domain.ReviewRunRunning},
			{ID: "run-3", SessionID: "mer-1", BatchID: "batch-1", PRURL: "pr3", TargetSHA: "sha3", Status: domain.ReviewRunComplete, Verdict: domain.VerdictApproved},
			{ID: "run-4", SessionID: "mer-1", BatchID: "batch-1", PRURL: "pr4", TargetSHA: "old", Status: domain.ReviewRunComplete, Verdict: domain.VerdictChangesRequested, Body: "stale"},
			{ID: "run-5", SessionID: "mer-1", BatchID: "batch-1", PRURL: "pr5", TargetSHA: "sha5", Status: domain.ReviewRunFailed},
		},
		prs: []domain.PullRequest{
			{URL: "pr1", HeadSHA: "sha1"},
			{URL: "pr2", HeadSHA: "sha2"},
			{URL: "pr3", HeadSHA: "sha3"},
			{URL: "pr4", HeadSHA: "new"},
			{URL: "pr5", HeadSHA: "sha5"},
		},
	}
	reducer := &fakeReducer{outcome: lifecycle.ReviewDeliverySent}
	svc := New(nil, st, WithLifecycleReducer(reducer), WithClock(func() time.Time { return now }))

	runs, err := svc.SubmitMany(context.Background(), "mer-1", []SubmittedReview{
		{RunID: "run-1", Verdict: domain.VerdictChangesRequested, Body: "fix pr1", GithubReviewID: "101"},
		{RunID: "run-2", Verdict: domain.VerdictChangesRequested, Body: "fix pr2", GithubReviewID: "102"},
		{RunID: "run-3", Verdict: domain.VerdictApproved},
	})
	if err != nil {
		t.Fatalf("SubmitMany: %v", err)
	}
	if reducer.batchCalls != 1 || reducer.gotBatchID != "batch-1" {
		t.Fatalf("batch delivery calls/id = %d/%q", reducer.batchCalls, reducer.gotBatchID)
	}
	if len(reducer.gotBatch) != 2 || reducer.gotBatch[0].RunID != "run-1" || reducer.gotBatch[1].RunID != "run-2" {
		t.Fatalf("delivered batch = %+v, want run-1 and run-2 only", reducer.gotBatch)
	}
	if st.markCalls != 2 {
		t.Fatalf("markCalls = %d, want 2", st.markCalls)
	}
	if runs[0].Status != domain.ReviewRunDelivered || runs[0].DeliveredAt == nil || !runs[0].DeliveredAt.Equal(now) ||
		runs[1].Status != domain.ReviewRunDelivered || runs[1].DeliveredAt == nil || !runs[1].DeliveredAt.Equal(now) {
		t.Fatalf("submitted runs not stamped delivered: %+v", runs)
	}
}

func TestSubmitBatchApprovedOnlySendsNothing(t *testing.T) {
	st := &fakeStore{
		ok:  true,
		run: domain.ReviewRun{ID: "run-2", SessionID: "mer-1", BatchID: "batch-1", PRURL: "pr2", TargetSHA: "sha2", Status: domain.ReviewRunRunning},
		batchRuns: []domain.ReviewRun{
			{ID: "run-1", SessionID: "mer-1", BatchID: "batch-1", PRURL: "pr1", TargetSHA: "sha1", Status: domain.ReviewRunComplete, Verdict: domain.VerdictApproved},
			{ID: "run-2", SessionID: "mer-1", BatchID: "batch-1", PRURL: "pr2", TargetSHA: "sha2", Status: domain.ReviewRunRunning},
		},
		prs: []domain.PullRequest{{URL: "pr1", HeadSHA: "sha1"}, {URL: "pr2", HeadSHA: "sha2"}},
	}
	reducer := &fakeReducer{outcome: lifecycle.ReviewDeliverySent}
	svc := New(nil, st, WithLifecycleReducer(reducer))

	if _, err := svc.Submit(context.Background(), "mer-1", "run-2", domain.VerdictApproved, "", "102"); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if reducer.batchCalls != 0 || st.markCalls != 0 {
		t.Fatalf("approved-only batch should not deliver: batchCalls=%d markCalls=%d", reducer.batchCalls, st.markCalls)
	}
}

func TestSubmitDeliveryFailureLeavesCompletedUndeliveredForRetry(t *testing.T) {
	sendErr := errors.New("dead pane")
	st := &fakeStore{
		ok:  true,
		run: domain.ReviewRun{ID: "run-1", SessionID: "mer-1", BatchID: "batch-1", PRURL: "pr1", TargetSHA: "sha1", Status: domain.ReviewRunRunning},
		prs: []domain.PullRequest{{URL: "pr1", HeadSHA: "sha1"}},
	}
	reducer := &fakeReducer{err: sendErr}
	svc := New(nil, st, WithLifecycleReducer(reducer))

	if _, err := svc.Submit(context.Background(), "mer-1", "run-1", domain.VerdictChangesRequested, "fix it", "987"); !errors.Is(err, sendErr) {
		t.Fatalf("err = %v, want sendErr", err)
	}
	if st.run.Status != domain.ReviewRunComplete || st.run.DeliveredAt != nil || st.markCalls != 0 {
		t.Fatalf("failed delivery should leave completed/undelivered without stamp: %+v markCalls=%d", st.run, st.markCalls)
	}

	reducer.err = nil
	reducer.outcome = lifecycle.ReviewDeliverySent
	if _, err := svc.Submit(context.Background(), "mer-1", "run-1", domain.VerdictChangesRequested, "fix it", "987"); err != nil {
		t.Fatalf("retry Submit: %v", err)
	}
	if st.updateCalls != 1 || reducer.batchCalls != 2 || st.run.Status != domain.ReviewRunDelivered || st.run.DeliveredAt == nil {
		t.Fatalf("retry should not rewrite result and should stamp delivery: update=%d reducer=%d run=%+v", st.updateCalls, reducer.batchCalls, st.run)
	}
}

func TestSubmitCompletedRetryRejectsDifferentRecordedFields(t *testing.T) {
	tests := []struct {
		name           string
		body           string
		githubReviewID string
	}{
		{name: "different body", body: "different", githubReviewID: "987"},
		{name: "different review id", body: "fix it", githubReviewID: "654"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := &fakeStore{ok: true, run: domain.ReviewRun{
				ID: "run-1", SessionID: "mer-1", PRURL: "pr1", TargetSHA: "sha1",
				Status: domain.ReviewRunComplete, Verdict: domain.VerdictChangesRequested,
				Body: "fix it", GithubReviewID: "987",
			}}
			reducer := &fakeReducer{outcome: lifecycle.ReviewDeliverySent}
			svc := New(nil, st, WithLifecycleReducer(reducer))

			if _, err := svc.Submit(context.Background(), "mer-1", "run-1", domain.VerdictChangesRequested, tt.body, tt.githubReviewID); !errors.Is(err, ErrInvalid) {
				t.Fatalf("err = %v, want ErrInvalid", err)
			}
			if st.updateCalls != 0 || st.markCalls != 0 || reducer.batchCalls != 0 {
				t.Fatalf("mismatched retry should not rewrite or deliver: update=%d mark=%d reducer=%d", st.updateCalls, st.markCalls, reducer.batchCalls)
			}
		})
	}
}

// recordingSink captures what the review service reports.
type recordingSink struct{ events []ports.TelemetryEvent }

func (r *recordingSink) Emit(_ context.Context, ev ports.TelemetryEvent) {
	r.events = append(r.events, ev)
}
func (r *recordingSink) Close(context.Context) error { return nil }

func (r *recordingSink) named(name string) []ports.TelemetryEvent {
	var out []ports.TelemetryEvent
	for _, ev := range r.events {
		if ev.Name == name {
			out = append(out, ev)
		}
	}
	return out
}

// Code review shipped with no telemetry at all, so there was no way to tell
// whether reviewers approve or request changes. This pins the outcome event.
func TestSubmitReportsReviewOutcome(t *testing.T) {
	created := time.Date(2026, 8, 4, 10, 0, 0, 0, time.UTC)
	store := &fakeStore{
		ok: true,
		run: domain.ReviewRun{
			ID: "run-1", SessionID: "worker-1", Status: domain.ReviewRunRunning,
			Harness: "claude-code", CreatedAt: created,
			PRURL: "https://github.com/acme/secret-repo/pull/7", TargetSHA: "deadbeefcafe",
		},
	}
	sink := &recordingSink{}
	svc := New(nil, store,
		WithTelemetry(sink),
		WithClock(func() time.Time { return created.Add(90 * time.Second) }),
	)

	ctx := context.WithValue(context.Background(), middleware.RequestIDKey, "req-1")
	if _, err := svc.Submit(ctx, "worker-1", "run-1",
		domain.VerdictChangesRequested, "please rename this", "gh-review-42"); err != nil {
		t.Fatalf("Submit: %v", err)
	}

	got := sink.named("ao.review.submitted")
	if len(got) != 1 {
		t.Fatalf("ao.review.submitted count = %d, want 1", len(got))
	}
	p := got[0].Payload
	if p["verdict"] != string(domain.VerdictChangesRequested) {
		t.Fatalf("verdict = %#v, want changes_requested", p["verdict"])
	}
	if p["harness"] != "claude-code" {
		t.Fatalf("harness = %#v, want claude-code", p["harness"])
	}
	if p["duration_ms"] != int64(90_000) {
		t.Fatalf("duration_ms = %#v, want 90000", p["duration_ms"])
	}
	if p["posted_to_provider"] != true {
		t.Fatalf("posted_to_provider = %#v, want true", p["posted_to_provider"])
	}
	if got[0].SessionID == nil || *got[0].SessionID != "worker-1" {
		t.Fatalf("SessionID = %#v, want worker-1", got[0].SessionID)
	}
	// The emit path detaches from the request context on purpose; the request id
	// must still be carried so review rows join to the HTTP request.
	if got[0].RequestID != "req-1" {
		t.Fatalf("RequestID = %q, want req-1", got[0].RequestID)
	}
}

// The review body is reviewer prose about someone's source code, and the PR URL
// and SHA identify the repository. None may ever reach the payload, regardless of
// what the daemon's remote allowlist would strip later.
func TestSubmitNeverReportsReviewProseOrRepoIdentifiers(t *testing.T) {
	store := &fakeStore{
		ok: true,
		run: domain.ReviewRun{
			ID: "run-1", SessionID: "worker-1", Status: domain.ReviewRunRunning,
			Harness: "codex", CreatedAt: time.Now().UTC(),
			PRURL: "https://github.com/acme/secret-repo/pull/7", TargetSHA: "deadbeefcafe",
		},
	}
	sink := &recordingSink{}
	svc := New(nil, store, WithTelemetry(sink))

	body := "leaks credentials in src/config/prod.ts"
	if _, err := svc.Submit(context.Background(), "worker-1", "run-1",
		domain.VerdictChangesRequested, body, ""); err != nil {
		t.Fatalf("Submit: %v", err)
	}

	for _, ev := range sink.events {
		for key, value := range ev.Payload {
			text, ok := value.(string)
			if !ok {
				continue
			}
			for _, forbidden := range []string{body, "secret-repo", "deadbeefcafe", "prod.ts", "github.com"} {
				if strings.Contains(text, forbidden) {
					t.Fatalf("payload %q leaked %q: %q", key, forbidden, text)
				}
			}
		}
		if _, ok := ev.Payload["body"]; ok {
			t.Fatalf("payload carries a body key: %#v", ev.Payload)
		}
	}
	if p := sink.named("ao.review.submitted")[0].Payload; p["posted_to_provider"] != false {
		t.Fatalf("posted_to_provider = %#v, want false when nothing was posted", p["posted_to_provider"])
	}
}

// Re-submitting an already-complete run is idempotent in the store, so it must be
// idempotent in telemetry too, or a retrying reviewer would double-count verdicts.
func TestResubmitDoesNotDoubleReport(t *testing.T) {
	store := &fakeStore{
		ok: true,
		run: domain.ReviewRun{
			ID: "run-1", SessionID: "worker-1", Status: domain.ReviewRunRunning,
			Harness: "opencode", CreatedAt: time.Now().UTC(),
		},
	}
	sink := &recordingSink{}
	svc := New(nil, store, WithTelemetry(sink))

	for i := 0; i < 3; i++ {
		if _, err := svc.Submit(context.Background(), "worker-1", "run-1",
			domain.VerdictApproved, "", ""); err != nil {
			t.Fatalf("Submit %d: %v", i, err)
		}
	}
	if got := len(sink.named("ao.review.submitted")); got != 1 {
		t.Fatalf("ao.review.submitted count = %d, want 1 across three submits", got)
	}
}

// Every existing caller constructs the service without a sink, so an unwired
// service must stay silent rather than panic.
func TestServiceWithoutTelemetrySinkStaysSilent(t *testing.T) {
	store := &fakeStore{
		ok:  true,
		run: domain.ReviewRun{ID: "run-1", SessionID: "worker-1", Status: domain.ReviewRunRunning},
	}
	svc := New(nil, store)
	if _, err := svc.Submit(context.Background(), "worker-1", "run-1", domain.VerdictApproved, "", ""); err != nil {
		t.Fatalf("Submit without a sink: %v", err)
	}
}

// reviewErrorKind must distinguish the engine's sentinels. They are wrapped with
// %w and only become *apierr.Error at the HTTP boundary, so the generic
// classifier would report every trigger failure as "internal" and the
// trigger_failed event's error_kind could never say why.
func TestReviewErrorKindClassifiesEngineSentinels(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"invalid", fmt.Errorf("%w: no PR", reviewcore.ErrInvalid), "invalid"},
		{"not_found", fmt.Errorf("%w: worker gone", reviewcore.ErrNotFound), "not_found"},
		{"agent_unavailable", fmt.Errorf("%w", ports.ErrAgentBinaryNotFound), "agent_unavailable"},
		{"fallback_internal", errors.New("something unexpected"), "internal"},
	}
	for _, c := range cases {
		if got := reviewErrorKind(c.err); got != c.want {
			t.Errorf("%s: reviewErrorKind = %q, want %q", c.name, got, c.want)
		}
	}
}

// An automatic pass was previously invisible: only the manual Trigger emitted,
// so auto-review could not be told apart from manual review anywhere
// downstream even though the two answer different product questions.
func TestTriggerReportsWhoStartedThePass(t *testing.T) {
	cases := []struct {
		name string
		call func(*Service) error
		want string
	}{
		{"manual", func(s *Service) error {
			_, err := s.Trigger(context.Background(), "worker-1", "", domain.AgentConfig{})
			return err
		}, "manual"},
		{"auto", func(s *Service) error {
			_, err := s.TriggerAuto(context.Background(), "worker-1", "claude-code")
			return err
		}, "auto"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sink := &recordingSink{}
			svc := New(nil, &fakeStore{}, WithTelemetry(sink))
			svc.engineTrigger = func(
				_ context.Context, _ domain.SessionID, _ domain.ReviewerHarness, _ domain.AgentConfig, _ domain.ReviewTriggerSource,
			) (reviewcore.TriggerResult, error) {
				return reviewcore.TriggerResult{
					Run:         domain.ReviewRun{Harness: "claude-code"},
					CreatedRuns: []domain.ReviewRun{{ID: "run-1"}},
				}, nil
			}
			if err := c.call(svc); err != nil {
				t.Fatalf("trigger: %v", err)
			}
			got := sink.named("ao.review.triggered")
			if len(got) != 1 {
				t.Fatalf("ao.review.triggered count = %d, want 1", len(got))
			}
			if got[0].Payload["trigger"] != c.want {
				t.Fatalf("trigger = %#v, want %q", got[0].Payload["trigger"], c.want)
			}
			if got[0].Payload["reused"] != false {
				t.Fatalf("reused = %#v, want false", got[0].Payload["reused"])
			}
		})
	}
}

func TestTriggerFailureReportsWhichPassFailed(t *testing.T) {
	sink := &recordingSink{}
	svc := New(nil, &fakeStore{}, WithTelemetry(sink))
	svc.engineTrigger = func(
		_ context.Context, _ domain.SessionID, _ domain.ReviewerHarness, _ domain.AgentConfig, _ domain.ReviewTriggerSource,
	) (reviewcore.TriggerResult, error) {
		return reviewcore.TriggerResult{}, fmt.Errorf("%w: no PR", reviewcore.ErrInvalid)
	}

	if _, err := svc.TriggerAuto(context.Background(), "worker-1", "codex"); err == nil {
		t.Fatal("TriggerAuto: want error")
	}
	if got := sink.named("ao.review.triggered"); len(got) != 1 {
		t.Fatalf("ao.review.triggered count = %d, want 1 even on failure", len(got))
	} else if got[0].Payload["trigger"] != "auto" {
		t.Fatalf("triggered payload = %#v, want trigger=auto", got[0].Payload)
	}
	got := sink.named("ao.review.trigger_failed")
	if len(got) != 1 {
		t.Fatalf("ao.review.trigger_failed count = %d, want 1", len(got))
	}
	if got[0].Payload["error_kind"] != "invalid" || got[0].Payload["trigger"] != "auto" {
		t.Fatalf("payload = %#v, want error_kind=invalid trigger=auto", got[0].Payload)
	}
}

func TestTriggerRejectsInvalidReviewerConfigBeforeEngine(t *testing.T) {
	sink := &recordingSink{}
	svc := New(nil, &fakeStore{}, WithTelemetry(sink))
	called := false
	svc.engineTrigger = func(
		_ context.Context, _ domain.SessionID, _ domain.ReviewerHarness, _ domain.AgentConfig, _ domain.ReviewTriggerSource,
	) (reviewcore.TriggerResult, error) {
		called = true
		return reviewcore.TriggerResult{}, nil
	}

	if _, err := svc.Trigger(context.Background(), "worker-1", "", domain.AgentConfig{Mode: "turbo"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("err = %v, want ErrInvalid", err)
	}
	if called {
		t.Fatal("engineTrigger should not run for invalid config")
	}
	got := sink.named("ao.review.trigger_failed")
	if len(got) != 1 {
		t.Fatalf("ao.review.trigger_failed count = %d, want 1", len(got))
	}
	if got[0].Payload["error_kind"] != "invalid" || got[0].Payload["trigger"] != "manual" {
		t.Fatalf("payload = %#v, want error_kind=invalid trigger=manual", got[0].Payload)
	}
}

// The submitted event has to carry enough to tell a shallow automatic approval
// apart from a substantial manual changes-requested pass.
func TestSubmitReportsPassShapeNotItsContents(t *testing.T) {
	policyOff := false
	store := &fakeStore{
		ok: true,
		run: domain.ReviewRun{
			ID: "run-1", SessionID: "worker-1", Status: domain.ReviewRunRunning,
			Harness: "codex", TriggerSource: domain.ReviewTriggerAuto, CreatedAt: time.Now().UTC(),
		},
		sessionAutoInjectReview: &policyOff,
	}
	sink := &recordingSink{}
	svc := New(nil, store, WithTelemetry(sink))

	body := "rename this symbol"
	if _, err := svc.Submit(context.Background(), "worker-1", "run-1",
		domain.VerdictChangesRequested, body, ""); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	p := sink.named("ao.review.submitted")[0].Payload
	if p["trigger"] != string(domain.ReviewTriggerAuto) {
		t.Fatalf("trigger = %#v, want auto", p["trigger"])
	}
	if p["body_bytes"] != len(body) {
		t.Fatalf("body_bytes = %#v, want %d", p["body_bytes"], len(body))
	}
	if p["auto_inject"] != false {
		t.Fatalf("auto_inject = %#v, want false for a session with the policy off", p["auto_inject"])
	}
}
func TestRestartedManualPassIsNotReportedAsReused(t *testing.T) {
	sink := &recordingSink{}
	svc := New(nil, &fakeStore{}, WithTelemetry(sink))
	svc.engineTrigger = func(
		_ context.Context, _ domain.SessionID, _ domain.ReviewerHarness, _ domain.AgentConfig, _ domain.ReviewTriggerSource,
	) (reviewcore.TriggerResult, error) {
		return reviewcore.TriggerResult{Run: domain.ReviewRun{Harness: "codex"}, Created: true, CreatedRuns: nil}, nil
	}

	if _, err := svc.Trigger(context.Background(), "worker-1", "", domain.AgentConfig{Model: "gpt-5-mini"}); err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	got := sink.named("ao.review.triggered")
	if len(got) != 1 {
		t.Fatalf("ao.review.triggered count = %d, want 1", len(got))
	}
	if got[0].Payload["reused"] != false || got[0].Payload["created_runs"] != 0 {
		t.Fatalf("payload = %#v, want reused=false created_runs=0 for a restart", got[0].Payload)
	}
}

func TestReusedManualPassStaysATrigger(t *testing.T) {
	sink := &recordingSink{}
	svc := New(nil, &fakeStore{}, WithTelemetry(sink))
	svc.engineTrigger = func(
		_ context.Context, _ domain.SessionID, _ domain.ReviewerHarness, _ domain.AgentConfig, _ domain.ReviewTriggerSource,
	) (reviewcore.TriggerResult, error) {
		return reviewcore.TriggerResult{Run: domain.ReviewRun{Harness: "codex"}, CreatedRuns: nil}, nil
	}

	if _, err := svc.Trigger(context.Background(), "worker-1", "", domain.AgentConfig{}); err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	got := sink.named("ao.review.triggered")
	if len(got) != 1 {
		t.Fatalf("ao.review.triggered count = %d, want 1", len(got))
	}
	if got[0].Payload["reused"] != true || got[0].Payload["trigger"] != "manual" {
		t.Fatalf("payload = %#v, want reused=true trigger=manual", got[0].Payload)
	}
	if n := len(sink.events); n != 1 {
		t.Fatalf("emitted %d events, want only the trigger for a manual reuse", n)
	}
}

// ao.review.triggered now counts every attempt, including automatic no-op
// sweeps. created_runs and reused distinguish real work from an already
// running or skipped pass.
func TestReusedOrSkippedAutoPassStillCountsAsTriggered(t *testing.T) {
	cases := []struct {
		name    string
		result  reviewcore.TriggerResult
		harness any
	}{
		{"reused: a reviewer is already running", reviewcore.TriggerResult{
			Run: domain.ReviewRun{Harness: "claude-code"}, CreatedRuns: nil,
		}, "claude-code"},
		{"skipped: the session changed under the coordinator", reviewcore.TriggerResult{
			SkipReason: "worker_active",
		}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sink := &recordingSink{}
			svc := New(nil, &fakeStore{}, WithTelemetry(sink))
			svc.engineTrigger = func(
				_ context.Context, _ domain.SessionID, _ domain.ReviewerHarness, _ domain.AgentConfig, _ domain.ReviewTriggerSource,
			) (reviewcore.TriggerResult, error) {
				return c.result, nil
			}

			for i := 0; i < 6; i++ {
				if _, err := svc.TriggerAuto(context.Background(), "worker-1", "claude-code"); err != nil {
					t.Fatalf("TriggerAuto %d: %v", i, err)
				}
			}
			got := sink.named("ao.review.triggered")
			if len(got) != 6 {
				t.Fatalf("ao.review.triggered count = %d, want 6", len(got))
			}
			for i, ev := range got {
				if ev.Payload["trigger"] != "auto" || ev.Payload["created_runs"] != 0 || ev.Payload["reused"] != true {
					t.Fatalf("event %d payload = %#v, want trigger=auto created_runs=0 reused=true", i, ev.Payload)
				}
				if ev.Payload["harness"] != c.harness {
					t.Fatalf("event %d harness = %#v, want %#v", i, ev.Payload["harness"], c.harness)
				}
			}
			if failed := sink.named("ao.review.trigger_failed"); len(failed) != 0 {
				t.Fatalf("ao.review.trigger_failed count = %d, want 0", len(failed))
			}
		})
	}
}
