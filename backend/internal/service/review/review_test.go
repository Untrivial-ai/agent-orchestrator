package review

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/lifecycle"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	reviewcore "github.com/aoagents/agent-orchestrator/backend/internal/review"
)

type fakeStore struct {
	mu                      sync.Mutex
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
	activityUpdates    int
	markCalls          int
	markedIDs          []string
	resolvedCommentIDs []string
	publishCalls       int
	publishStates      []domain.ReviewRunPublishState
}

func (f *fakeStore) GetReviewByID(_ context.Context, id string) (domain.Review, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.reviewOK && f.review.ID == id {
		return f.review, true, nil
	}
	return domain.Review{}, false, nil
}

func (f *fakeStore) UpdateReviewActivity(_ context.Context, id string, state domain.ActivityState, agentSessionID, launchID string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.reviewOK || f.review.ID != id {
		return false, nil
	}
	switch {
	case f.review.ReviewerLaunchID != "" && launchID != f.review.ReviewerLaunchID:
		return false, nil
	case f.review.ReviewerLaunchID == "" && launchID != "":
		return false, nil
	}
	f.activityUpdates++
	if agentSessionID != "" {
		f.review.AgentSessionID = agentSessionID
	}
	if state != "" {
		f.review.ReviewerActivityState = state
	}
	if launchID != "" {
		f.review.ReviewerLaunchID = launchID
	}
	return true, nil
}

func (f *fakeStore) GetReviewRun(_ context.Context, id string) (domain.ReviewRun, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
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
	f.mu.Lock()
	defer f.mu.Unlock()
	enabled := true
	if f.sessionAutoInjectReview != nil {
		enabled = *f.sessionAutoInjectReview
	}
	return domain.SessionRecord{ID: id, AutoInjectReview: enabled}, true, nil
}

func (f *fakeStore) UpdateReviewRunResult(_ context.Context, id string, status domain.ReviewRunStatus, verdict domain.ReviewVerdict, body, findingsJSON, githubReviewID string, autoInjectReview bool) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
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
			if err := json.Unmarshal([]byte(findingsJSON), &f.batchRuns[i].Findings); err != nil {
				f.batchRuns[i].Findings = nil
			}
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
	if err := json.Unmarshal([]byte(findingsJSON), &f.run.Findings); err != nil {
		f.run.Findings = nil
	}
	return true, nil
}

func (f *fakeStore) UpdateReviewRunPublication(_ context.Context, id string, state domain.ReviewRunPublishState, githubReviewID, publishError string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.publishCalls++
	f.publishStates = append(f.publishStates, state)
	apply := func(run *domain.ReviewRun) {
		if run.ID != id {
			return
		}
		run.PublishState = state
		run.PublishError = publishError
		if githubReviewID != "" {
			run.GithubReviewID = githubReviewID
		}
	}
	apply(&f.run)
	for i := range f.batchRuns {
		apply(&f.batchRuns[i])
	}
	return true, nil
}

func (f *fakeStore) MarkReviewRunDelivered(_ context.Context, id string, deliveredAt time.Time) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
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
	f.mu.Lock()
	defer f.mu.Unlock()
	out := append([]domain.ReviewRun(nil), f.batchRuns...)
	return out, nil
}

func (f *fakeStore) ListPRsBySession(context.Context, domain.SessionID) ([]domain.PullRequest, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := append([]domain.PullRequest(nil), f.prs...)
	return out, nil
}

func (f *fakeStore) ListPRReviews(_ context.Context, prURL string) ([]domain.PullRequestReview, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := append([]domain.PullRequestReview(nil), f.prReviews[prURL]...)
	return out, nil
}

func (f *fakeStore) ListPRComments(_ context.Context, prURL string) ([]domain.PullRequestComment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := append([]domain.PullRequestComment(nil), f.prComments[prURL]...)
	return out, nil
}

func (f *fakeStore) MarkPRCommentResolved(_ context.Context, prURL, commentID string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
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

// fakePublisher records daemon-side publication calls.
type fakePublisher struct {
	calls  int
	last   ports.SCMReviewPublishRequest
	result ports.SCMReviewPublishResult
	err    error
}

func (p *fakePublisher) PublishReview(_ context.Context, request ports.SCMReviewPublishRequest) (ports.SCMReviewPublishResult, error) {
	p.calls++
	p.last = request
	if p.err != nil {
		return ports.SCMReviewPublishResult{}, p.err
	}
	if p.result.ReviewID == "" && p.result.HTMLURL == "" {
		return ports.SCMReviewPublishResult{ReviewID: "gh-review-42"}, nil
	}
	return p.result, nil
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

	run, err := svc.Submit(context.Background(), "mer-1", "run-1", domain.VerdictChangesRequested, "fix it", nil)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if st.updateCalls != 1 || reducer.batchCalls != 1 || st.markCalls != 1 {
		t.Fatalf("calls update/reducer/mark = %d/%d/%d", st.updateCalls, reducer.batchCalls, st.markCalls)
	}
	if reducer.gotBatch[0].Verdict != domain.VerdictChangesRequested || reducer.gotBatch[0].Body != "fix it" {
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
	if st.activityUpdates != 1 || st.review.AgentSessionID != "opencode-native-2" {
		t.Fatalf("activity update calls=%d review=%+v", st.activityUpdates, st.review)
	}
	if st.review.SessionID != "worker-1" {
		t.Fatalf("worker session id changed: %+v", st.review)
	}
}

func TestApplyReviewActivitySignalPersistsReviewerActivityState(t *testing.T) {
	st := &fakeStore{
		reviewOK: true,
		review:   domain.Review{ID: "review-1", SessionID: "worker-1", Harness: domain.ReviewerOpenCode},
	}
	svc := New(nil, st)

	if err := svc.ApplyReviewActivitySignal(context.Background(), "review-1", ActivitySignal{
		Event: "stop",
		State: domain.ActivityIdle,
	}); err != nil {
		t.Fatalf("ApplyReviewActivitySignal: %v", err)
	}
	if st.activityUpdates != 1 || st.review.ReviewerActivityState != domain.ActivityIdle {
		t.Fatalf("activity update calls=%d review=%+v", st.activityUpdates, st.review)
	}
}

func TestApplyReviewActivitySignalIgnoresStaleLaunchGeneration(t *testing.T) {
	st := &fakeStore{
		reviewOK: true,
		review: domain.Review{
			ID:                    "review-1",
			SessionID:             "worker-1",
			Harness:               domain.ReviewerOpenCode,
			ReviewerLaunchID:      "launch-current",
			ReviewerActivityState: domain.ActivityActive,
		},
	}
	svc := New(nil, st)

	if err := svc.ApplyReviewActivitySignal(context.Background(), "review-1", ActivitySignal{
		Event:    "stop",
		State:    domain.ActivityIdle,
		LaunchID: "launch-stale",
	}); err != nil {
		t.Fatalf("ApplyReviewActivitySignal stale generation: %v", err)
	}
	if st.activityUpdates != 0 {
		t.Fatalf("stale generation performed update calls=%d review=%+v", st.activityUpdates, st.review)
	}
	if st.review.ReviewerActivityState != domain.ActivityActive || st.review.ReviewerLaunchID != "launch-current" {
		t.Fatalf("stale generation changed persisted review = %+v", st.review)
	}
}

func TestApplyReviewActivitySignalIgnoresMissingLaunchIDAfterGenerationClaimed(t *testing.T) {
	st := &fakeStore{
		reviewOK: true,
		review: domain.Review{
			ID:                    "review-1",
			SessionID:             "worker-1",
			Harness:               domain.ReviewerOpenCode,
			ReviewerLaunchID:      "launch-current",
			ReviewerActivityState: domain.ActivityActive,
			AgentSessionID:        "native-current",
		},
	}
	svc := New(nil, st)

	if err := svc.ApplyReviewActivitySignal(context.Background(), "review-1", ActivitySignal{
		Event:          "session-start",
		AgentSessionID: "legacy-native",
	}); err != nil {
		t.Fatalf("ApplyReviewActivitySignal missing launch id: %v", err)
	}
	if st.activityUpdates != 0 {
		t.Fatalf("missing launch id performed update calls=%d review=%+v", st.activityUpdates, st.review)
	}
	if st.review.ReviewerActivityState != domain.ActivityActive || st.review.ReviewerLaunchID != "launch-current" || st.review.AgentSessionID != "native-current" {
		t.Fatalf("missing launch id changed persisted review = %+v", st.review)
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

	run, err := svc.Submit(context.Background(), "mer-1", "run-1", domain.VerdictChangesRequested, "fix it", nil)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != domain.ReviewRunComplete || run.AutoInjectReview || reducer.batchCalls != 0 || st.markCalls != 0 {
		t.Fatalf("disabled review = %+v reducerCalls=%d markCalls=%d", run, reducer.batchCalls, st.markCalls)
	}

	enabled := true
	st.sessionAutoInjectReview = &enabled
	run, err = svc.Submit(context.Background(), "mer-1", "run-1", domain.VerdictChangesRequested, "fix it", nil)
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

	run, err := svc.Submit(context.Background(), "mer-1", "run-1", domain.VerdictChangesRequested, "fix pr1", nil)
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
		{RunID: "run-1", Verdict: domain.VerdictChangesRequested, Body: "fix pr1"},
		{RunID: "run-2", Verdict: domain.VerdictChangesRequested, Body: "fix pr2"},
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

func TestSubmitManySkipsSupersededRunAndDeliversSiblings(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	st := &fakeStore{
		ok: true,
		batchRuns: []domain.ReviewRun{
			{ID: "run-1", SessionID: "mer-1", BatchID: "batch-1", PRURL: "pr1", TargetSHA: "sha1", Status: domain.ReviewRunRunning},
			// A newer-commit trigger superseded run-2 while the reviewer was still
			// working on the original batch.
			{ID: "run-2", SessionID: "mer-1", BatchID: "batch-1", PRURL: "pr2", TargetSHA: "sha2", Status: domain.ReviewRunFailed},
		},
		prs: []domain.PullRequest{{URL: "pr1", HeadSHA: "sha1"}, {URL: "pr2", HeadSHA: "sha2-new"}},
	}
	reducer := &fakeReducer{outcome: lifecycle.ReviewDeliverySent}
	svc := New(nil, st, WithLifecycleReducer(reducer), WithClock(func() time.Time { return now }))

	runs, err := svc.SubmitMany(context.Background(), "mer-1", []SubmittedReview{
		{RunID: "run-1", Verdict: domain.VerdictChangesRequested, Body: "fix pr1"},
		{RunID: "run-2", Verdict: domain.VerdictChangesRequested, Body: "fix pr2"},
	})
	if err != nil {
		t.Fatalf("SubmitMany must deliver valid siblings when one run was superseded: %v", err)
	}
	if len(runs) != 1 || runs[0].ID != "run-1" || runs[0].Status != domain.ReviewRunDelivered {
		t.Fatalf("want only run-1 delivered, got %+v", runs)
	}
	if reducer.batchCalls != 1 || len(reducer.gotBatch) != 1 || reducer.gotBatch[0].RunID != "run-1" {
		t.Fatalf("want run-1 delivered independently; batchCalls=%d got=%+v", reducer.batchCalls, reducer.gotBatch)
	}
}

func TestSubmitManyRejectsOnlySupersededRuns(t *testing.T) {
	st := &fakeStore{
		ok: true,
		batchRuns: []domain.ReviewRun{{
			ID: "run-1", SessionID: "mer-1", BatchID: "batch-1", PRURL: "pr1", TargetSHA: "sha1", Status: domain.ReviewRunCancelled,
		}},
	}
	reducer := &fakeReducer{outcome: lifecycle.ReviewDeliverySent}
	svc := New(nil, st, WithLifecycleReducer(reducer))

	if _, err := svc.SubmitMany(context.Background(), "mer-1", []SubmittedReview{{
		RunID: "run-1", Verdict: domain.VerdictApproved,
	}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("err = %v, want ErrInvalid", err)
	} else if !strings.Contains(err.Error(), "superseded: run-1") {
		t.Fatalf("err = %v, want rejected run id", err)
	}
	if reducer.batchCalls != 0 {
		t.Fatalf("only superseded runs must not trigger delivery: batchCalls=%d", reducer.batchCalls)
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

	if _, err := svc.Submit(context.Background(), "mer-1", "run-2", domain.VerdictApproved, "ship it", nil); err != nil {
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

	if _, err := svc.Submit(context.Background(), "mer-1", "run-1", domain.VerdictChangesRequested, "fix it", nil); !errors.Is(err, sendErr) {
		t.Fatalf("err = %v, want sendErr", err)
	}
	if st.run.Status != domain.ReviewRunComplete || st.run.DeliveredAt != nil || st.markCalls != 0 {
		t.Fatalf("failed delivery should leave completed/undelivered without stamp: %+v markCalls=%d", st.run, st.markCalls)
	}

	reducer.err = nil
	reducer.outcome = lifecycle.ReviewDeliverySent
	if _, err := svc.Submit(context.Background(), "mer-1", "run-1", domain.VerdictChangesRequested, "fix it", nil); err != nil {
		t.Fatalf("retry Submit: %v", err)
	}
	if st.updateCalls != 1 || reducer.batchCalls != 2 || st.run.Status != domain.ReviewRunDelivered || st.run.DeliveredAt == nil {
		t.Fatalf("retry should not rewrite result and should stamp delivery: update=%d reducer=%d run=%+v", st.updateCalls, reducer.batchCalls, st.run)
	}
}

func TestSubmitCompletedRetryRejectsDifferentRecordedFields(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		findings []domain.ReviewFinding
	}{
		{name: "different body", body: "different"},
		{name: "different findings", body: "fix it", findings: []domain.ReviewFinding{{Path: "a.go", Line: 1, Body: "new finding"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := &fakeStore{ok: true, run: domain.ReviewRun{
				ID: "run-1", SessionID: "mer-1", PRURL: "pr1", TargetSHA: "sha1",
				Status: domain.ReviewRunComplete, Verdict: domain.VerdictChangesRequested,
				Body: "fix it",
			}}
			reducer := &fakeReducer{outcome: lifecycle.ReviewDeliverySent}
			svc := New(nil, st, WithLifecycleReducer(reducer))

			if _, err := svc.Submit(context.Background(), "mer-1", "run-1", domain.VerdictChangesRequested, tt.body, tt.findings); !errors.Is(err, ErrInvalid) {
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
		prs: []domain.PullRequest{{URL: "https://github.com/acme/secret-repo/pull/7", Provider: "github", Host: "github.com", Repo: "acme/secret-repo", Number: 7, HeadSHA: "deadbeefcafe"}},
	}
	sink := &recordingSink{}
	svc := New(nil, store,
		WithTelemetry(sink),
		WithClock(func() time.Time { return created.Add(90 * time.Second) }),
		WithReviewPublisher(&fakePublisher{}),
	)

	ctx := context.WithValue(context.Background(), middleware.RequestIDKey, "req-1")
	if _, err := svc.Submit(ctx, "worker-1", "run-1",
		domain.VerdictChangesRequested, "please rename this", nil); err != nil {
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
		domain.VerdictChangesRequested, body, nil); err != nil {
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
			domain.VerdictApproved, "ok", nil); err != nil {
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
	if _, err := svc.Submit(context.Background(), "worker-1", "run-1", domain.VerdictApproved, "ok", nil); err != nil {
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
		domain.VerdictChangesRequested, body, nil); err != nil {
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

// --- Publication state machine (issue #5701) ---

func publicationTestService(t *testing.T, st *fakeStore, pub *fakePublisher) *Service {
	t.Helper()
	st.prs = []domain.PullRequest{{
		URL: "https://github.com/acme/app/pull/9", Provider: "github", Host: "github.com",
		Repo: "acme/app", Number: 9, HeadSHA: "sha1",
	}}
	return New(nil, st, WithReviewPublisher(pub))
}

func TestSubmitPublishesReviewOnceAndReturnsRecordedId(t *testing.T) {
	st := &fakeStore{ok: true, run: domain.ReviewRun{
		ID: "run-1", SessionID: "worker-1", BatchID: "batch-1", PRURL: "https://github.com/acme/app/pull/9",
		TargetSHA: "sha1", Status: domain.ReviewRunRunning,
	}}
	pub := &fakePublisher{result: ports.SCMReviewPublishResult{ReviewID: "9001", HTMLURL: "https://github.com/acme/app/pull/9#review-9001"}}
	svc := publicationTestService(t, st, pub)

	findings := []domain.ReviewFinding{{Path: "src/auth.go", Line: 42, Body: "Missing authorization check."}}
	run, err := svc.Submit(context.Background(), "worker-1", "run-1", domain.VerdictChangesRequested, "needs auth", findings)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if pub.calls != 1 {
		t.Fatalf("publisher calls = %d, want 1", pub.calls)
	}
	if pub.last.CommitSHA != "sha1" || pub.last.Body != "needs auth" || len(pub.last.Comments) != 1 || pub.last.Comments[0].Path != "src/auth.go" || pub.last.Comments[0].Line != 42 {
		t.Fatalf("publish request = %+v", pub.last)
	}
	if run.PublishState != domain.ReviewPublishPublished || run.GithubReviewID != "9001" {
		t.Fatalf("run = publish %q id %q, want published/9001", run.PublishState, run.GithubReviewID)
	}

	// An identical resubmission must return the recorded result without
	// publishing again.
	run, err = svc.Submit(context.Background(), "worker-1", "run-1", domain.VerdictChangesRequested, "needs auth", findings)
	if err != nil {
		t.Fatalf("resubmit: %v", err)
	}
	if pub.calls != 1 {
		t.Fatalf("publisher calls after resubmit = %d, want 1", pub.calls)
	}
	if run.PublishState != domain.ReviewPublishPublished || run.GithubReviewID != "9001" {
		t.Fatalf("resubmitted run = %q/%q", run.PublishState, run.GithubReviewID)
	}
}

func TestSubmitZeroOneAndManyFindingsAllPublish(t *testing.T) {
	for _, tc := range []struct {
		name     string
		findings []domain.ReviewFinding
	}{
		{name: "zero findings"},
		{name: "one finding", findings: []domain.ReviewFinding{{Path: "a.go", Line: 1, Body: "boom"}}},
		{name: "many findings", findings: []domain.ReviewFinding{{Path: "a.go", Line: 1, Body: "boom"}, {Path: "b.go", Line: 2, Body: "also boom"}, {Path: "c.go", Line: 3, Body: "third"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := &fakeStore{ok: true, run: domain.ReviewRun{
				ID: "run-1", SessionID: "worker-1", PRURL: "https://github.com/acme/app/pull/9",
				TargetSHA: "sha1", Status: domain.ReviewRunRunning,
			}}
			pub := &fakePublisher{result: ports.SCMReviewPublishResult{ReviewID: "42"}}
			svc := publicationTestService(t, st, pub)

			run, err := svc.Submit(context.Background(), "worker-1", "run-1", domain.VerdictChangesRequested, "review body", tc.findings)
			if err != nil {
				t.Fatalf("Submit: %v", err)
			}
			if len(run.Findings) != len(tc.findings) || run.PublishState != domain.ReviewPublishPublished {
				t.Fatalf("run findings=%d publish=%q", len(run.Findings), run.PublishState)
			}
			if len(pub.last.Comments) != len(tc.findings) {
				t.Fatalf("comments = %d, want %d", len(pub.last.Comments), len(tc.findings))
			}
		})
	}
}

func TestSubmitProviderFailureRecordsFailedAndRetryRepublishes(t *testing.T) {
	st := &fakeStore{ok: true, run: domain.ReviewRun{
		ID: "run-1", SessionID: "worker-1", PRURL: "https://github.com/acme/app/pull/9",
		TargetSHA: "sha1", Status: domain.ReviewRunRunning,
	}}
	pub := &fakePublisher{err: errors.New("422: pull request head changed")}
	svc := publicationTestService(t, st, pub)

	if _, err := svc.Submit(context.Background(), "worker-1", "run-1", domain.VerdictApproved, "ship it", nil); err != nil {
		t.Fatalf("provider failure must not fail the submission: %v", err)
	}
	if st.run.PublishState != domain.ReviewPublishFailed || st.run.PublishError == "" {
		t.Fatalf("run = %+v, want failed with an error", st.run)
	}

	// The provider rejecting the request is definitive: the review was not
	// created, so a repeated submission may publish again.
	pub.err = nil
	pub.result = ports.SCMReviewPublishResult{ReviewID: "77"}
	run, err := svc.Submit(context.Background(), "worker-1", "run-1", domain.VerdictApproved, "ship it", nil)
	if err != nil {
		t.Fatalf("retry Submit: %v", err)
	}
	if pub.calls != 2 || run.PublishState != domain.ReviewPublishPublished || run.GithubReviewID != "77" {
		t.Fatalf("retry: calls=%d publish=%q id=%q", pub.calls, run.PublishState, run.GithubReviewID)
	}
}

func TestSubmitInterruptedPublicationStaysUncertainUntilConfirmed(t *testing.T) {
	// Attempt interrupted mid-flight: the publishing state was persisted before
	// the provider call, then the daemon died. A resubmission must report the
	// uncertainty instead of blindly reposting.
	st := &fakeStore{ok: true, run: domain.ReviewRun{
		ID: "run-1", SessionID: "worker-1", PRURL: "https://github.com/acme/app/pull/9",
		TargetSHA: "sha1", Status: domain.ReviewRunComplete, Verdict: domain.VerdictChangesRequested,
		Body: "fix it", PublishState: domain.ReviewPublishPublishing,
	}}
	pub := &fakePublisher{}
	svc := publicationTestService(t, st, pub)

	run, err := svc.Submit(context.Background(), "worker-1", "run-1", domain.VerdictChangesRequested, "fix it", nil)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if pub.calls != 0 {
		t.Fatalf("publisher calls = %d, want 0 for an uncertain prior attempt", pub.calls)
	}
	if run.PublishState != domain.ReviewPublishUncertain || run.PublishError == "" {
		t.Fatalf("run = %q/%q, want uncertain with a reason", run.PublishState, run.PublishError)
	}
	if st.run.PublishState != domain.ReviewPublishUncertain {
		t.Fatalf("persisted state = %q, want uncertain", st.run.PublishState)
	}
}

func TestSubmitManyConcurrentIdenticalSubmissionsPublishOnce(t *testing.T) {
	st := &fakeStore{ok: true, run: domain.ReviewRun{
		ID: "run-1", SessionID: "worker-1", PRURL: "https://github.com/acme/app/pull/9",
		TargetSHA: "sha1", Status: domain.ReviewRunRunning,
	}}
	pub := &fakePublisher{result: ports.SCMReviewPublishResult{ReviewID: "5"}}
	svc := publicationTestService(t, st, pub)

	const racers = 8
	start := make(chan struct{})
	results := make(chan error, racers)
	for i := 0; i < racers; i++ {
		go func() {
			<-start
			_, err := svc.Submit(context.Background(), "worker-1", "run-1", domain.VerdictApproved, "ship it", nil)
			results <- err
		}()
	}
	close(start)
	for i := 0; i < racers; i++ {
		if err := <-results; err != nil {
			t.Fatalf("concurrent submit: %v", err)
		}
	}
	if pub.calls != 1 {
		t.Fatalf("publisher calls = %d, want exactly 1", pub.calls)
	}
}

func TestSubmitFindingsSurviveInWorkerFeedbackBody(t *testing.T) {
	st := &fakeStore{ok: true, run: domain.ReviewRun{
		ID: "run-1", SessionID: "worker-1", BatchID: "batch-1", PRURL: "https://github.com/acme/app/pull/9",
		TargetSHA: "sha1", Status: domain.ReviewRunRunning,
	}}
	pub := &fakePublisher{result: ports.SCMReviewPublishResult{ReviewID: "8"}}
	svc := publicationTestService(t, st, pub)
	reducer := &fakeReducer{outcome: lifecycle.ReviewDeliverySent}
	svc.lifecycle = reducer

	findings := []domain.ReviewFinding{
		{Path: "src/auth.go", Line: 42, Body: "Missing authorization check."},
		{Path: "src/db.go", Line: 7, Body: "Unclosed transaction."},
	}
	if _, err := svc.Submit(context.Background(), "worker-1", "run-1", domain.VerdictChangesRequested, "two problems", findings); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if len(reducer.gotBatch) != 1 {
		t.Fatalf("delivered batch = %+v", reducer.gotBatch)
	}
	body := reducer.gotBatch[0].Body
	if !strings.HasPrefix(body, "two problems") || !strings.Contains(body, "`src/auth.go:42` — Missing authorization check.") || !strings.Contains(body, "`src/db.go:7` — Unclosed transaction.") {
		t.Fatalf("worker feedback body lost findings: %q", body)
	}
	if reducer.gotBatch[0].GithubReviewID != "8" {
		t.Fatalf("delivered run githubReviewId = %q, want the published id", reducer.gotBatch[0].GithubReviewID)
	}
}
