package review

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

type wireRoundTripper func(*http.Request) (*http.Response, error)

func (f wireRoundTripper) Do(req *http.Request) (*http.Response, error) { return f(req) }

// captured is one PostHog capture request as it actually left the process.
type captured struct {
	event string
	props map[string]any
	raw   string
}

type wireRecorder struct {
	mu     sync.Mutex
	events []captured
}

func (r *wireRecorder) add(c captured) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, c)
}

func (r *wireRecorder) find(name string) (captured, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, ev := range r.events {
		if ev.event == name {
			return ev, true
		}
	}
	return captured{}, false
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
// (denylist -> aggregation -> rate limit -> PostHog) in front of a fake
// transport, so a test can assert what an emit site actually puts on the wire
// rather than what it handed to a sink. flushEvery is long on purpose: the
// aggregator is flushed by Close, never by the background ticker, so nothing
// here depends on timing.
func newWireSink(t *testing.T) (ports.EventSink, *wireRecorder, func()) {
	t.Helper()
	rec := &wireRecorder{}
	remote, err := telemetryadapter.NewPostHogSink(t.TempDir(), "phc_test", "https://us.i.posthog.com", "", "",
		wireRoundTripper(func(req *http.Request) (*http.Response, error) {
			defer req.Body.Close()
			var body struct {
				Event      string         `json:"event"`
				Properties map[string]any `json:"properties"`
			}
			raw, err := io.ReadAll(req.Body)
			if err != nil {
				return nil, err
			}
			if err := json.Unmarshal(raw, &body); err != nil {
				return nil, err
			}
			rec.add(captured{event: body.Event, props: body.Properties, raw: string(raw)})
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

// End to end: the review service's real emit sites, through the real export
// chain, onto the wire. Every assertion here is one a unit test cannot make,
// because the payload allowlist that decides what actually reaches PostHog
// lives in the adapter and is keyed by event name, with nothing tying it to the
// emit site at compile time.
func TestReviewFunnelReachesTheWireWithItsProperties(t *testing.T) {
	sink, rec, closeSink := newWireSink(t)

	created := time.Date(2026, 8, 4, 10, 0, 0, 0, time.UTC)
	store := &fakeStore{
		ok: true,
		run: domain.ReviewRun{
			ID: "run-1", SessionID: "worker-1", Status: domain.ReviewRunRunning,
			Harness: "claude-code", TriggerSource: domain.ReviewTriggerAuto, CreatedAt: created,
			PRURL: "https://github.com/acme/secret-repo/pull/7", TargetSHA: "deadbeefcafe",
		},
	}
	svc := New(nil, store,
		WithTelemetry(sink),
		WithClock(func() time.Time { return created.Add(90 * time.Second) }),
	)
	svc.engineTrigger = func(
		_ context.Context, _ domain.SessionID, _ domain.ReviewerHarness, _ domain.AgentConfig, _ domain.ReviewTriggerSource,
	) (reviewcore.TriggerResult, error) {
		return reviewcore.TriggerResult{
			Run:         domain.ReviewRun{Harness: "claude-code"},
			CreatedRuns: []domain.ReviewRun{{ID: "run-1"}},
		}, nil
	}

	if _, err := svc.TriggerAuto(context.Background(), "worker-1", "claude-code"); err != nil {
		t.Fatalf("TriggerAuto: %v", err)
	}
	reviewBody := "rename the credential loader in src/config/prod.ts"
	if _, err := svc.Submit(context.Background(), "worker-1", "run-1",
		domain.VerdictChangesRequested, reviewBody, "gh-review-42"); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	closeSink()

	triggered, ok := rec.find("ao.review.triggered")
	if !ok {
		t.Fatalf("ao.review.triggered never reached the wire; got %v", rec.names())
	}
	if triggered.props["trigger"] != "auto" {
		t.Fatalf("triggered.trigger = %#v, want auto (missing allowlist entry strips it silently)", triggered.props["trigger"])
	}
	if triggered.props["harness"] != "claude-code" {
		t.Fatalf("triggered.harness = %#v, want claude-code", triggered.props["harness"])
	}

	submitted, ok := rec.find("ao.review.submitted")
	if !ok {
		t.Fatalf("ao.review.submitted never reached the wire; got %v", rec.names())
	}
	for key, want := range map[string]any{
		"verdict":            "changes_requested",
		"harness":            "claude-code",
		"trigger":            "auto",
		"posted_to_provider": true,
		"duration_ms":        float64(90_000), // JSON numbers decode as float64
		"body_bytes":         float64(len(reviewBody)),
	} {
		if got := submitted.props[key]; got != want {
			t.Errorf("submitted.%s = %#v, want %#v", key, got, want)
		}
	}

	// The review prose, the repository, and the commit must not appear anywhere
	// in what left the process, in any event, under any key.
	for _, ev := range rec.events {
		for _, forbidden := range []string{
			reviewBody, "secret-repo", "deadbeefcafe", "prod.ts", "github.com", "gh-review-42",
		} {
			if strings.Contains(ev.raw, forbidden) {
				t.Errorf("%s put %q on the wire: %s", ev.event, forbidden, ev.raw)
			}
		}
	}
}
