package autoreview

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	telemetryadapter "github.com/aoagents/agent-orchestrator/backend/internal/adapters/telemetry"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
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
		{name: "failed current head retries", want: true, mutate: func(f *fakeStore) {
			f.runs = []domain.ReviewRun{{PRURL: "pr1", TargetSHA: "sha1", Status: domain.ReviewRunFailed, CreatedAt: now}}
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

type recordingSink struct {
	events []ports.TelemetryEvent
}

func (s *recordingSink) Emit(_ context.Context, ev ports.TelemetryEvent) {
	s.events = append(s.events, ev)
}
func (*recordingSink) Close(context.Context) error { return nil }

func (s *recordingSink) reasons() []string {
	var out []string
	for _, ev := range s.events {
		if ev.Name != "ao.review.auto_skipped" {
			continue
		}
		reason, _ := ev.Payload["reason"].(string)
		out = append(out, reason)
	}
	return out
}

func autoReviewStore(now time.Time) *fakeStore {
	return &fakeStore{
		session: domain.SessionRecord{
			ID: "s1", ProjectID: "p1", Kind: domain.KindWorker, Harness: domain.AgentHarness("codex"),
			AutoReviewEnabled: true,
			Activity:          domain.Activity{State: domain.ActivityIdle, LastActivityAt: now.Add(-time.Minute)},
		},
		project: domain.ProjectRecord{ID: "p1"},
		prs:     []domain.PullRequest{{URL: "pr1", Number: 1, HeadSHA: "sha1"}},
	}
}

// "I turned auto-review on and nothing happened" is this feature's central
// failure mode, and the reason is only knowable inside the coordinator. The
// sweep runs once a minute, so a standing reason must report once, not forever.
func TestAutoReviewSkipReportsTheReasonOnceUntilItChanges(t *testing.T) {
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	store := autoReviewStore(now)
	store.runs = []domain.ReviewRun{{
		PRURL: "pr1", TargetSHA: "sha1", Status: domain.ReviewRunComplete,
		Verdict: domain.VerdictApproved, CreatedAt: now,
	}}
	sink := &recordingSink{}
	c := New(store, &fakeTrigger{}, Config{Clock: func() time.Time { return now }, Telemetry: sink})

	for i := 0; i < 3; i++ {
		if _, err := c.EvaluateSession(context.Background(), "s1"); err != nil {
			t.Fatalf("EvaluateSession %d: %v", i, err)
		}
	}
	if got := sink.reasons(); len(got) != 1 || got[0] != "already_approved" {
		t.Fatalf("reasons = %#v, want one already_approved", got)
	}

	// The PR closes: a different standing reason has to report itself rather
	// than being swallowed by the previous one.
	store.prs[0].Closed = true
	if _, err := c.EvaluateSession(context.Background(), "s1"); err != nil {
		t.Fatalf("EvaluateSession after change: %v", err)
	}
	if got := sink.reasons(); len(got) != 2 || got[1] != "closed_pr" {
		t.Fatalf("reasons = %#v, want a second closed_pr", got)
	}
}

// Gate reasons describe sessions auto-review is not meant to act on at all.
// They are policy working as intended, and the sweep already filters them, so
// reporting them would swamp the signal that matters.
func TestAutoReviewSkipIgnoresGateReasonsAndSuccessfulTriggers(t *testing.T) {
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name   string
		mutate func(*fakeStore)
	}{
		{"disabled", func(f *fakeStore) { f.session.AutoReviewEnabled = false }},
		{"not a worker", func(f *fakeStore) { f.session.Kind = domain.KindOrchestrator }},
		{"still working", func(f *fakeStore) { f.session.Activity.State = domain.ActivityActive }},
		{"terminated", func(f *fakeStore) { f.session.IsTerminated = true }},
		{"triggered", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			store := autoReviewStore(now)
			if c.mutate != nil {
				c.mutate(store)
			}
			sink := &recordingSink{}
			coordinator := New(store, &fakeTrigger{}, Config{Clock: func() time.Time { return now }, Telemetry: sink})
			if _, err := coordinator.EvaluateSession(context.Background(), "s1"); err != nil {
				t.Fatalf("EvaluateSession: %v", err)
			}
			if got := sink.reasons(); len(got) != 0 {
				t.Fatalf("reasons = %#v, want none", got)
			}
		})
	}
}

// A reason must never carry a repository path, branch, or PR URL.
func TestAutoReviewSkipReportsOnlyTheReason(t *testing.T) {
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	store := autoReviewStore(now)
	store.prs = []domain.PullRequest{{
		URL: "https://github.com/acme/secret-repo/pull/7", Number: 7,
		HeadSHA: "deadbeefcafe", SourceBranch: "feat/private-thing", Closed: true,
	}}
	sink := &recordingSink{}
	c := New(store, &fakeTrigger{}, Config{Clock: func() time.Time { return now }, Telemetry: sink})

	if _, err := c.EvaluateSession(context.Background(), "s1"); err != nil {
		t.Fatalf("EvaluateSession: %v", err)
	}
	got := sink.events
	if len(got) != 1 {
		t.Fatalf("events = %d, want 1", len(got))
	}
	if len(got[0].Payload) != 1 || got[0].Payload["reason"] != "closed_pr" {
		t.Fatalf("payload = %#v, want only reason=closed_pr", got[0].Payload)
	}
	if got[0].SessionID == nil || *got[0].SessionID != "s1" {
		t.Fatalf("SessionID = %#v, want s1", got[0].SessionID)
	}
}

// Every existing caller constructs the coordinator without a sink.
func TestCoordinatorWithoutTelemetryStaysSilent(t *testing.T) {
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	store := autoReviewStore(now)
	store.prs = nil
	c := New(store, &fakeTrigger{}, Config{Clock: func() time.Time { return now }})
	if _, err := c.EvaluateSession(context.Background(), "s1"); err != nil {
		t.Fatalf("EvaluateSession without a sink: %v", err)
	}
}

type wireRoundTripper func(*http.Request) (*http.Response, error)

func (f wireRoundTripper) Do(req *http.Request) (*http.Response, error) { return f(req) }

// End to end for the coordinator's own event: through the same remote chain the
// daemon wires (denylist -> aggregation -> rate limit -> PostHog) onto the wire.
// The payload allowlist that decides what survives lives in the adapter, keyed
// by event name, with nothing tying it to this emit site at compile time.
func TestAutoReviewSkipReachesTheWireWithItsReason(t *testing.T) {
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	type capture struct {
		event string
		props map[string]any
		raw   string
	}
	var mu sync.Mutex
	var captured []capture

	remote, err := telemetryadapter.NewPostHogSink(t.TempDir(), "phc_test", "https://us.i.posthog.com", "", "",
		wireRoundTripper(func(req *http.Request) (*http.Response, error) {
			defer req.Body.Close()
			raw, err := io.ReadAll(req.Body)
			if err != nil {
				return nil, err
			}
			var body struct {
				Event      string         `json:"event"`
				Properties map[string]any `json:"properties"`
			}
			if err := json.Unmarshal(raw, &body); err != nil {
				return nil, err
			}
			mu.Lock()
			captured = append(captured, capture{event: body.Event, props: body.Properties, raw: string(raw)})
			mu.Unlock()
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: http.NoBody}, nil
		}), slog.Default())
	if err != nil {
		t.Fatalf("NewPostHogSink: %v", err)
	}
	sink := telemetryadapter.NewDenylistSink(
		telemetryadapter.NewAggregatingSink(telemetryadapter.NewRateLimitedSink(remote, nil), nil, time.Hour),
		nil,
	)

	store := autoReviewStore(now)
	store.prs = []domain.PullRequest{{
		URL: "https://github.com/acme/secret-repo/pull/7", Number: 7,
		HeadSHA: "deadbeefcafe", SourceBranch: "feat/private-thing", Closed: true,
	}}
	c := New(store, &fakeTrigger{}, Config{Clock: func() time.Time { return now }, Telemetry: sink})
	if _, err := c.EvaluateSession(context.Background(), "s1"); err != nil {
		t.Fatalf("EvaluateSession: %v", err)
	}
	if err := sink.Close(context.Background()); err != nil {
		t.Fatalf("Close sink chain: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(captured) != 1 {
		t.Fatalf("captured %d events, want 1: %#v", len(captured), captured)
	}
	got := captured[0]
	if got.event != "ao.review.auto_skipped" {
		t.Fatalf("event = %q, want ao.review.auto_skipped", got.event)
	}
	if got.props["reason"] != "closed_pr" {
		t.Fatalf("reason = %#v, want closed_pr (a missing allowlist entry strips it silently)", got.props["reason"])
	}
	for _, forbidden := range []string{"secret-repo", "deadbeefcafe", "private-thing", "github.com"} {
		if strings.Contains(got.raw, forbidden) {
			t.Fatalf("auto_skipped put %q on the wire: %s", forbidden, got.raw)
		}
	}
}

// lastReason is the only map written on the Coordinator, and a write to a nil
// one panics. Every other field is nil-guarded despite New setting it, so a
// coordinator assembled any other way must not be able to take the sweep down
// from a telemetry write.
func TestAutoReviewSkipSurvivesACoordinatorBuiltWithoutNew(t *testing.T) {
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	store := autoReviewStore(now)
	store.prs = nil
	sink := &recordingSink{}
	c := &Coordinator{
		store:         store,
		reviews:       &fakeTrigger{},
		clock:         func() time.Time { return now },
		idleThreshold: DefaultIdleThreshold,
		sweepInterval: DefaultSweepInterval,
		logger:        slog.Default(),
		telemetry:     sink,
		// lastReason deliberately left nil.
	}

	if _, err := c.EvaluateSession(context.Background(), "s1"); err != nil {
		t.Fatalf("EvaluateSession: %v", err)
	}
	if got := sink.reasons(); len(got) != 1 || got[0] != "no_pr" {
		t.Fatalf("reasons = %#v, want one no_pr", got)
	}
	// The lazily created map still has to dedup the next identical sweep.
	if _, err := c.EvaluateSession(context.Background(), "s1"); err != nil {
		t.Fatalf("second EvaluateSession: %v", err)
	}
	if got := sink.reasons(); len(got) != 1 {
		t.Fatalf("reasons = %#v, want the repeat deduped", got)
	}
}
