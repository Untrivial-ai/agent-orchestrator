package lifecycle

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
)

type wireRoundTripper func(*http.Request) (*http.Response, error)

func (f wireRoundTripper) Do(req *http.Request) (*http.Response, error) { return f(req) }

type capturedEvent struct {
	event string
	props map[string]any
	raw   string
}

type wireRecorder struct {
	mu     sync.Mutex
	events []capturedEvent
}

func (r *wireRecorder) find(name string) (capturedEvent, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, ev := range r.events {
		if ev.event == name {
			return ev, true
		}
	}
	return capturedEvent{}, false
}

func (r *wireRecorder) names() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.events))
	for _, ev := range r.events {
		out = append(out, ev.event)
	}
	return out
}

// newWireSink builds the same remote chain newTelemetrySink wires in the daemon
// in front of a fake transport, so these tests assert what a reducer actually
// puts on the wire rather than what it handed to a sink. The payload allowlist
// that decides this lives in the adapter, keyed by event name, with nothing
// tying it to the emit site at compile time.
func newWireSink(t *testing.T) (ports.EventSink, *wireRecorder, func()) {
	t.Helper()
	rec := &wireRecorder{}
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
			rec.mu.Lock()
			rec.events = append(rec.events, capturedEvent{event: body.Event, props: body.Properties, raw: string(raw)})
			rec.mu.Unlock()
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: http.NoBody}, nil
		}), slog.Default())
	if err != nil {
		t.Fatalf("NewPostHogSink: %v", err)
	}
	rateLimited := telemetryadapter.NewRateLimitedSink(remote, nil)
	aggregated := telemetryadapter.NewAggregatingSink(rateLimited, nil, time.Hour)
	sink := telemetryadapter.NewDenylistSink(aggregated, nil)
	return sink, rec, func() {
		if err := sink.Close(context.Background()); err != nil {
			t.Fatalf("Close sink chain: %v", err)
		}
	}
}

// End to end for the two lifecycle-owned review events: feedback delivery (did
// the reviewer's changes-requested actually land in the worker's pane) and the
// PR outcome (was it merged over a standing changes-requested verdict).
func TestReviewLifecycleEventsReachTheWireWithTheirProperties(t *testing.T) {
	const prURL = "https://github.com/acme/secret-repo/pull/7"
	sink, rec, closeSink := newWireSink(t)

	st := newFakeStore()
	rec2 := working("mer-1")
	rec2.TerminateOnPRMerge = false
	st.sessions["mer-1"] = rec2
	st.prs["mer-1"] = []domain.PullRequest{{URL: prURL, Number: 7, HeadSHA: "deadbeefcafe", Merged: true}}
	st.reviewRuns["mer-1"] = []domain.ReviewRun{{
		PRURL: prURL, TargetSHA: "deadbeefcafe", Verdict: domain.VerdictChangesRequested,
		Harness: "claude-code", CreatedAt: time.Unix(20, 0),
	}}
	m := New(st, &fakeMessenger{}, WithTelemetry(sink))

	reviewBody := "rename the credential loader in src/config/prod.ts"
	if _, err := m.ApplyReviewBatch(ctx, "mer-1", "batch-1", []ReviewResult{{
		RunID: "run-1", BatchID: "batch-1", WorkerID: "mer-1", PRURL: prURL,
		TargetSHA: "deadbeefcafe", Verdict: domain.VerdictChangesRequested,
		Body: reviewBody, GithubReviewID: "gh-review-42",
	}}); err != nil {
		t.Fatalf("ApplyReviewBatch: %v", err)
	}
	if err := m.ApplyPRObservation(ctx, "mer-1", ports.PRObservation{
		Fetched: true, URL: prURL, Number: 7, Merged: true,
		Title: "Rewrite the credential loader", SourceBranch: "feat/private-thing",
	}); err != nil {
		t.Fatalf("ApplyPRObservation: %v", err)
	}
	closeSink()

	delivered, ok := rec.find("ao.review.feedback_delivered")
	if !ok {
		t.Fatalf("ao.review.feedback_delivered never reached the wire; got %v", rec.names())
	}
	if delivered.props["outcome"] != "sent" {
		t.Errorf("feedback_delivered.outcome = %#v, want sent", delivered.props["outcome"])
	}
	if delivered.props["results"] != float64(1) { // JSON numbers decode as float64
		t.Errorf("feedback_delivered.results = %#v, want 1", delivered.props["results"])
	}

	closed, ok := rec.find("ao.review.pr_closed")
	if !ok {
		t.Fatalf("ao.review.pr_closed never reached the wire; got %v", rec.names())
	}
	for key, want := range map[string]any{
		"merged":                        true,
		"was_reviewed":                  true,
		"review_rounds":                 float64(1),
		"last_verdict":                  "changes_requested",
		"changes_requested_outstanding": true,
		"harness":                       "claude-code",
	} {
		if got := closed.props[key]; got != want {
			t.Errorf("pr_closed.%s = %#v, want %#v", key, got, want)
		}
	}

	// The review prose, the repository, the branch, the PR title, and the commit
	// must not appear anywhere in what left the process.
	for _, ev := range rec.events {
		for _, forbidden := range []string{
			reviewBody, "secret-repo", "deadbeefcafe", "prod.ts", "github.com",
			"gh-review-42", "private-thing", "credential loader",
		} {
			if strings.Contains(ev.raw, forbidden) {
				t.Errorf("%s put %q on the wire: %s", ev.event, forbidden, ev.raw)
			}
		}
	}
}
