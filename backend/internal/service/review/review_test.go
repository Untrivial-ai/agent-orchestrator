package review

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/lifecycle"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	reviewcore "github.com/aoagents/agent-orchestrator/backend/internal/review"
)

type fakeStore struct {
	run       domain.ReviewRun
	ok        bool
	review    domain.Review
	reviewOK  bool
	batchRuns []domain.ReviewRun
	prs       []domain.PullRequest

	updateCalls        int
	agentSessionUpdate int
	markCalls          int
	markedIDs          []string
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

func (f *fakeStore) UpdateReviewRunResult(_ context.Context, id string, status domain.ReviewRunStatus, verdict domain.ReviewVerdict, body, githubReviewID string) (bool, error) {
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

func TestSubmitManySkipsSupersededRunAndDeliversSiblings(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	st := &fakeStore{
		ok: true,
		batchRuns: []domain.ReviewRun{
			{ID: "run-1", SessionID: "mer-1", BatchID: "batch-1", PRURL: "pr1", TargetSHA: "sha1", Status: domain.ReviewRunRunning},
			// run-2 was superseded to failed by a newer-commit trigger while the
			// reviewer was still working, so its result is no longer submittable.
			{ID: "run-2", SessionID: "mer-1", BatchID: "batch-1", PRURL: "pr2", TargetSHA: "sha2", Status: domain.ReviewRunFailed},
		},
		prs: []domain.PullRequest{{URL: "pr1", HeadSHA: "sha1"}, {URL: "pr2", HeadSHA: "sha2new"}},
	}
	reducer := &fakeReducer{outcome: lifecycle.ReviewDeliverySent}
	svc := New(nil, st, WithLifecycleReducer(reducer), WithClock(func() time.Time { return now }))

	// The reviewer submits both results, unaware run-2 was superseded. The stale
	// run-2 must not strand run-1's feedback.
	runs, err := svc.SubmitMany(context.Background(), "mer-1", []SubmittedReview{
		{RunID: "run-1", Verdict: domain.VerdictChangesRequested, Body: "fix pr1"},
		{RunID: "run-2", Verdict: domain.VerdictChangesRequested, Body: "fix pr2"},
	})
	if err != nil {
		t.Fatalf("SubmitMany must not fail when a sibling run was superseded: %v", err)
	}
	if len(runs) != 1 || runs[0].ID != "run-1" || runs[0].Status != domain.ReviewRunDelivered {
		t.Fatalf("want only run-1 delivered (run-2 skipped), got %+v", runs)
	}
	if reducer.batchCalls != 1 || len(reducer.gotBatch) != 1 || reducer.gotBatch[0].RunID != "run-1" {
		t.Fatalf("want run-1 delivered independently; batchCalls=%d got=%+v", reducer.batchCalls, reducer.gotBatch)
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

	if _, err := svc.Submit(context.Background(), "worker-1", "run-1",
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
