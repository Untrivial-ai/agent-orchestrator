package autoreview

import (
	"context"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	reviewcore "github.com/aoagents/agent-orchestrator/backend/internal/review"
)

type fakeStore struct {
	session domain.SessionRecord
	project domain.ProjectRecord
	prs     []domain.PullRequest
	runs    []domain.ReviewRun
}

func (f *fakeStore) GetSession(context.Context, domain.SessionID) (domain.SessionRecord, bool, error) {
	return f.session, true, nil
}
func (f *fakeStore) ListAllSessions(context.Context) ([]domain.SessionRecord, error) {
	return []domain.SessionRecord{f.session}, nil
}
func (f *fakeStore) GetProject(context.Context, string) (domain.ProjectRecord, bool, error) {
	return f.project, true, nil
}
func (f *fakeStore) ListPRsBySession(context.Context, domain.SessionID) ([]domain.PullRequest, error) {
	return f.prs, nil
}
func (f *fakeStore) ListReviewRunsBySession(context.Context, domain.SessionID) ([]domain.ReviewRun, error) {
	return f.runs, nil
}

type fakeTrigger struct {
	calls   int
	harness domain.ReviewerHarness
	result  reviewcore.TriggerResult
}

func (f *fakeTrigger) TriggerAuto(_ context.Context, _ domain.SessionID, harness domain.ReviewerHarness) (reviewcore.TriggerResult, error) {
	f.calls++
	f.harness = harness
	if f.result.Reviews != nil || f.result.Runs != nil || f.result.SkipReason != "" {
		return f.result, nil
	}
	return reviewcore.TriggerResult{Created: true}, nil
}

func TestEvaluateSessionEligibility(t *testing.T) {
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name   string
		mutate func(*fakeStore)
		want   bool
	}{
		{name: "eligible", want: true},
		{name: "disabled", mutate: func(f *fakeStore) { f.session.AutoReviewEnabled = false }},
		{name: "non-worker", mutate: func(f *fakeStore) { f.session.Kind = domain.KindOrchestrator }},
		{name: "active", mutate: func(f *fakeStore) { f.session.Activity.State = domain.ActivityActive }},
		{name: "idle less than threshold", mutate: func(f *fakeStore) { f.session.Activity.LastActivityAt = now.Add(-59 * time.Second) }},
		{name: "waiting input", mutate: func(f *fakeStore) { f.session.Activity.State = domain.ActivityWaitingInput }},
		{name: "blocked", mutate: func(f *fakeStore) { f.session.Activity.State = domain.ActivityBlocked }},
		{name: "terminated", mutate: func(f *fakeStore) { f.session.IsTerminated = true }},
		{name: "draft", want: true, mutate: func(f *fakeStore) { f.prs[0].Draft = true }},
		{name: "closed", mutate: func(f *fakeStore) { f.prs[0].Closed = true }},
		{name: "merged", mutate: func(f *fakeStore) { f.prs[0].Merged = true }},
		{name: "missing head", mutate: func(f *fakeStore) { f.prs[0].HeadSHA = "" }},
		{name: "no PR", mutate: func(f *fakeStore) { f.prs = nil }},
		{name: "approved current head", mutate: func(f *fakeStore) {
			f.runs = []domain.ReviewRun{{PRURL: "pr1", TargetSHA: "sha1", Status: domain.ReviewRunComplete, Verdict: domain.VerdictApproved, CreatedAt: now}}
		}},
		{name: "changes requested current head", mutate: func(f *fakeStore) {
			f.runs = []domain.ReviewRun{{PRURL: "pr1", TargetSHA: "sha1", Status: domain.ReviewRunComplete, Verdict: domain.VerdictChangesRequested, TriggerSource: domain.ReviewTriggerAuto, CreatedAt: now}}
		}},
		{name: "new head after changes requested", want: true, mutate: func(f *fakeStore) {
			f.runs = []domain.ReviewRun{{PRURL: "pr1", TargetSHA: "old", Status: domain.ReviewRunComplete, Verdict: domain.VerdictChangesRequested, TriggerSource: domain.ReviewTriggerAuto, CreatedAt: now}}
		}},
		{name: "failed current head retries under limit", want: true, mutate: func(f *fakeStore) {
			f.runs = []domain.ReviewRun{
				{PRURL: "pr1", TargetSHA: "sha1", Status: domain.ReviewRunFailed, TriggerSource: domain.ReviewTriggerAuto, CreatedAt: now},
				{PRURL: "pr1", TargetSHA: "sha1", Status: domain.ReviewRunFailed, TriggerSource: domain.ReviewTriggerAuto, CreatedAt: now.Add(time.Second)},
			}
		}},
		{name: "failed current head stops after three auto retries", mutate: func(f *fakeStore) {
			f.runs = []domain.ReviewRun{
				{PRURL: "pr1", TargetSHA: "sha1", Status: domain.ReviewRunFailed, TriggerSource: domain.ReviewTriggerAuto, CreatedAt: now},
				{PRURL: "pr1", TargetSHA: "sha1", Status: domain.ReviewRunFailed, TriggerSource: domain.ReviewTriggerAuto, CreatedAt: now.Add(time.Second)},
				{PRURL: "pr1", TargetSHA: "sha1", Status: domain.ReviewRunFailed, TriggerSource: domain.ReviewTriggerAuto, CreatedAt: now.Add(2 * time.Second)},
			}
		}},
		{name: "manual failures do not spend the auto retry budget", want: true, mutate: func(f *fakeStore) {
			f.runs = []domain.ReviewRun{
				{PRURL: "pr1", TargetSHA: "sha1", Status: domain.ReviewRunFailed, TriggerSource: domain.ReviewTriggerManual, CreatedAt: now},
				{PRURL: "pr1", TargetSHA: "sha1", Status: domain.ReviewRunFailed, TriggerSource: domain.ReviewTriggerManual, CreatedAt: now.Add(time.Second)},
				{PRURL: "pr1", TargetSHA: "sha1", Status: domain.ReviewRunFailed, TriggerSource: domain.ReviewTriggerManual, CreatedAt: now.Add(2 * time.Second)},
			}
		}},
		{name: "cancelled current head waits for new commit", mutate: func(f *fakeStore) {
			f.runs = []domain.ReviewRun{{PRURL: "pr1", TargetSHA: "sha1", Status: domain.ReviewRunCancelled, CreatedAt: now}}
		}},
		{name: "new head after cancelled review", want: true, mutate: func(f *fakeStore) {
			f.runs = []domain.ReviewRun{{PRURL: "pr1", TargetSHA: "old", Status: domain.ReviewRunCancelled, CreatedAt: now}}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &fakeStore{
				session: domain.SessionRecord{ID: "s1", ProjectID: "p1", Kind: domain.KindWorker, Harness: domain.AgentHarness("codex"), AutoReviewEnabled: true, Activity: domain.Activity{State: domain.ActivityIdle, LastActivityAt: now.Add(-time.Minute)}},
				project: domain.ProjectRecord{ID: "p1"},
				prs:     []domain.PullRequest{{URL: "pr1", Number: 1, HeadSHA: "sha1"}},
			}
			if tt.mutate != nil {
				tt.mutate(store)
			}
			trigger := &fakeTrigger{}
			result, err := New(store, trigger, Config{Clock: func() time.Time { return now }}).EvaluateSession(context.Background(), "s1")
			if err != nil {
				t.Fatal(err)
			}
			if result.Triggered != tt.want || (trigger.calls == 1) != tt.want {
				t.Fatalf("triggered=%v calls=%d, want %v (reason=%s)", result.Triggered, trigger.calls, tt.want, result.Reason)
			}
			if tt.want && trigger.harness != domain.ReviewerCodex {
				t.Fatalf("harness=%q, want codex", trigger.harness)
			}
		})
	}
}

func TestExistingHeadReasonCapsFailedAutoRetriesPerHead(t *testing.T) {
	prURL := "pr1"
	tests := []struct {
		name string
		runs []domain.ReviewRun
		want string
	}{
		{
			name: "two failed auto runs do not block",
			runs: []domain.ReviewRun{
				{PRURL: prURL, TargetSHA: "sha1", Harness: domain.ReviewerCodex, TriggerSource: domain.ReviewTriggerAuto, Status: domain.ReviewRunFailed},
				{PRURL: prURL, TargetSHA: "sha1", Harness: domain.ReviewerCodex, TriggerSource: domain.ReviewTriggerAuto, Status: domain.ReviewRunFailed},
			},
			want: "",
		},
		{
			name: "three failed auto runs hit the retry limit",
			runs: []domain.ReviewRun{
				{PRURL: prURL, TargetSHA: "sha1", Harness: domain.ReviewerCodex, TriggerSource: domain.ReviewTriggerAuto, Status: domain.ReviewRunFailed},
				{PRURL: prURL, TargetSHA: "sha1", Harness: domain.ReviewerCodex, TriggerSource: domain.ReviewTriggerAuto, Status: domain.ReviewRunFailed},
				{PRURL: prURL, TargetSHA: "sha1", Harness: domain.ReviewerCodex, TriggerSource: domain.ReviewTriggerAuto, Status: domain.ReviewRunFailed},
			},
			want: "failed_same_sha_retry_limit",
		},
		{
			name: "other sha does not count",
			runs: []domain.ReviewRun{
				{PRURL: prURL, TargetSHA: "old", Harness: domain.ReviewerCodex, TriggerSource: domain.ReviewTriggerAuto, Status: domain.ReviewRunFailed},
				{PRURL: prURL, TargetSHA: "old", Harness: domain.ReviewerCodex, TriggerSource: domain.ReviewTriggerAuto, Status: domain.ReviewRunFailed},
				{PRURL: prURL, TargetSHA: "old", Harness: domain.ReviewerCodex, TriggerSource: domain.ReviewTriggerAuto, Status: domain.ReviewRunFailed},
			},
			want: "",
		},
		{
			name: "manual failures do not count",
			runs: []domain.ReviewRun{
				{PRURL: prURL, TargetSHA: "sha1", Harness: domain.ReviewerCodex, TriggerSource: domain.ReviewTriggerManual, Status: domain.ReviewRunFailed},
				{PRURL: prURL, TargetSHA: "sha1", Harness: domain.ReviewerCodex, TriggerSource: domain.ReviewTriggerManual, Status: domain.ReviewRunFailed},
				{PRURL: prURL, TargetSHA: "sha1", Harness: domain.ReviewerCodex, TriggerSource: domain.ReviewTriggerManual, Status: domain.ReviewRunFailed},
			},
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := existingHeadReason(tt.runs, prURL, "sha1"); got != tt.want {
				t.Fatalf("existingHeadReason() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestEvaluateSessionRoutesSoleRunningReviewThroughEngine(t *testing.T) {
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	running := domain.ReviewRun{PRURL: "pr1", TargetSHA: "sha1", Harness: domain.ReviewerCodex, Status: domain.ReviewRunRunning, CreatedAt: now}
	store := &fakeStore{
		session: domain.SessionRecord{ID: "s1", ProjectID: "p1", Kind: domain.KindWorker, Harness: domain.HarnessCodex, AutoReviewEnabled: true, Activity: domain.Activity{State: domain.ActivityIdle, LastActivityAt: now.Add(-time.Minute)}},
		project: domain.ProjectRecord{ID: "p1"},
		prs:     []domain.PullRequest{{URL: "pr1", Number: 1, HeadSHA: "sha1"}},
		runs:    []domain.ReviewRun{running},
	}
	trigger := &fakeTrigger{result: reviewcore.TriggerResult{
		Reviews: []reviewcore.PRReviewState{{PRURL: "pr1", TargetSHA: "sha1", Status: reviewcore.ReviewStateRunning}},
		Runs:    []domain.ReviewRun{running},
	}}

	result, err := New(store, trigger, Config{Clock: func() time.Time { return now }}).EvaluateSession(context.Background(), "s1")
	if err != nil {
		t.Fatal(err)
	}
	if trigger.calls != 1 || result.Triggered || result.Reason != "review_running" {
		t.Fatalf("result=%+v calls=%d, want reconciled running skip", result, trigger.calls)
	}
}

func TestEvaluateSessionReportsCancelledAfterStaleRunningReconciliation(t *testing.T) {
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	store := &fakeStore{
		session: domain.SessionRecord{ID: "s1", ProjectID: "p1", Kind: domain.KindWorker, Harness: domain.HarnessCodex, AutoReviewEnabled: true, Activity: domain.Activity{State: domain.ActivityIdle, LastActivityAt: now.Add(-time.Minute)}},
		project: domain.ProjectRecord{ID: "p1"},
		prs:     []domain.PullRequest{{URL: "pr1", Number: 1, HeadSHA: "sha1"}},
		runs:    []domain.ReviewRun{{PRURL: "pr1", TargetSHA: "sha1", Harness: domain.ReviewerCodex, Status: domain.ReviewRunRunning}},
	}
	cancelled := domain.ReviewRun{PRURL: "pr1", TargetSHA: "sha1", Harness: domain.ReviewerCodex, Status: domain.ReviewRunCancelled}
	trigger := &fakeTrigger{result: reviewcore.TriggerResult{
		Reviews: []reviewcore.PRReviewState{{PRURL: "pr1", TargetSHA: "sha1", Status: reviewcore.ReviewStateNeedsReview}},
		Runs:    []domain.ReviewRun{cancelled},
	}}

	result, err := New(store, trigger, Config{Clock: func() time.Time { return now }}).EvaluateSession(context.Background(), "s1")
	if err != nil {
		t.Fatal(err)
	}
	if trigger.calls != 1 || result.Triggered || result.Reason != "cancelled_same_sha" {
		t.Fatalf("result=%+v calls=%d, want cancelled_same_sha", result, trigger.calls)
	}
}

func TestEvaluateSessionReviewerHarnessPrecedence(t *testing.T) {
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name     string
		session  domain.ReviewerHarness
		project  domain.ReviewerHarness
		worker   domain.AgentHarness
		expected domain.ReviewerHarness
	}{
		{name: "session wins", session: domain.ReviewerOpenCode, project: domain.ReviewerClaudeCode, worker: domain.AgentHarness("codex"), expected: domain.ReviewerOpenCode},
		{name: "project fallback", project: domain.ReviewerOpenCode, worker: domain.AgentHarness("codex"), expected: domain.ReviewerOpenCode},
		{name: "safe worker inheritance", worker: domain.HarnessCodex, expected: domain.ReviewerCodex},
		{name: "known reviewer outside safe inheritance set", worker: domain.HarnessKimi, expected: domain.ReviewerClaudeCode},
		{name: "non-reviewer worker fallback", worker: domain.HarnessAider, expected: domain.ReviewerClaudeCode},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &fakeStore{
				session: domain.SessionRecord{ID: "s1", ProjectID: "p1", Kind: domain.KindWorker, Harness: tt.worker, ReviewerHarness: tt.session, AutoReviewEnabled: true, Activity: domain.Activity{State: domain.ActivityIdle, LastActivityAt: now.Add(-time.Minute)}},
				project: domain.ProjectRecord{ID: "p1"},
				prs:     []domain.PullRequest{{URL: "pr1", Number: 1, HeadSHA: "sha1"}},
			}
			if tt.project != "" {
				store.project.Config.Reviewers = []domain.ReviewerConfig{{Harness: tt.project}}
			}
			trigger := &fakeTrigger{}
			result, err := New(store, trigger, Config{Clock: func() time.Time { return now }}).EvaluateSession(context.Background(), "s1")
			if err != nil || !result.Triggered {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			if trigger.harness != tt.expected {
				t.Fatalf("harness=%q, want %q", trigger.harness, tt.expected)
			}
		})
	}
}

type notifyingTrigger struct {
	called chan domain.SessionID
}

func (f *notifyingTrigger) TriggerAuto(_ context.Context, id domain.SessionID, _ domain.ReviewerHarness) (reviewcore.TriggerResult, error) {
	f.called <- id
	return reviewcore.TriggerResult{Created: true}, nil
}

func TestCoordinatorPeriodicallyEvaluatesPersistedFacts(t *testing.T) {
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	store := &fakeStore{
		session: domain.SessionRecord{ID: "s1", ProjectID: "p1", Kind: domain.KindWorker, Harness: domain.HarnessCodex, AutoReviewEnabled: true, Activity: domain.Activity{State: domain.ActivityIdle, LastActivityAt: now.Add(-time.Minute)}},
		project: domain.ProjectRecord{ID: "p1"},
		prs:     []domain.PullRequest{{URL: "pr1", Number: 1, HeadSHA: "sha1"}},
	}
	trigger := &notifyingTrigger{called: make(chan domain.SessionID, 1)}
	ctx, cancel := context.WithCancel(context.Background())
	done := New(store, trigger, Config{Clock: func() time.Time { return now }, SweepInterval: time.Millisecond}).Start(ctx)

	select {
	case id := <-trigger.called:
		if id != "s1" {
			t.Fatalf("triggered session = %q, want s1", id)
		}
	case <-time.After(time.Second):
		t.Fatal("periodic coordinator sweep did not evaluate persisted facts")
	}

	cancel()
	<-done
}
