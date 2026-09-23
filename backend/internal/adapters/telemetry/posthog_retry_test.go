package telemetry

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// newRetryTestSink builds a sink whose HTTP client is driven by respond, which
// receives the 1-based attempt number and returns the response (or error) for
// that attempt. It also returns a pointer to the live attempt counter.
func newRetryTestSink(t *testing.T, respond func(attempt int) (*http.Response, error)) (*PostHogSink, *int) {
	t.Helper()
	attempts := 0
	sink, err := NewPostHogSink(t.TempDir(), "phc_test", "https://example.test", "", "", roundTripClient(func(_ *http.Request) (*http.Response, error) {
		attempts++
		return respond(attempts)
	}), nil)
	if err != nil {
		t.Fatalf("NewPostHogSink: %v", err)
	}
	return sink, &attempts
}

func okResponse() *http.Response {
	return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}
}

func statusResponse(code int) *http.Response {
	return &http.Response{StatusCode: code, Body: http.NoBody}
}

func activeEvent() ports.TelemetryEvent {
	return ports.TelemetryEvent{
		Name:       "ao.app.active",
		Source:     "cli",
		OccurredAt: time.Unix(0, 0).UTC(),
		Payload:    map[string]any{"channel": "cli", "actor_type": "user"},
	}
}

func TestPostHogSinkRetriesTransientServerErrorThenSucceeds(t *testing.T) {
	sink, attempts := newRetryTestSink(t, func(attempt int) (*http.Response, error) {
		if attempt == 1 {
			return statusResponse(http.StatusInternalServerError), nil
		}
		return okResponse(), nil
	})
	sink.send(context.Background(), activeEvent())
	if *attempts != 2 {
		t.Fatalf("attempts = %d, want 2 (one 5xx then a successful retry)", *attempts)
	}
}

func TestPostHogSinkRetriesRateLimited429ThenSucceeds(t *testing.T) {
	// 429 is a distinct transient branch from 5xx and must also retry.
	sink, attempts := newRetryTestSink(t, func(attempt int) (*http.Response, error) {
		if attempt == 1 {
			return statusResponse(http.StatusTooManyRequests), nil
		}
		return okResponse(), nil
	})
	sink.send(context.Background(), activeEvent())
	if *attempts != 2 {
		t.Fatalf("attempts = %d, want 2 (one 429 then a successful retry)", *attempts)
	}
}

func TestPostHogSinkStopsAfterMaxAttemptsOn429(t *testing.T) {
	// A sustained 429 must stay bounded, not retry forever.
	sink, attempts := newRetryTestSink(t, func(_ int) (*http.Response, error) {
		return statusResponse(http.StatusTooManyRequests), nil
	})
	sink.send(context.Background(), activeEvent())
	if *attempts != postHogSendMaxAttempts {
		t.Fatalf("attempts = %d, want %d (429 retries stay bounded)", *attempts, postHogSendMaxAttempts)
	}
}

func TestPostHogSinkRetriesNetworkErrorThenSucceeds(t *testing.T) {
	sink, attempts := newRetryTestSink(t, func(attempt int) (*http.Response, error) {
		if attempt == 1 {
			return nil, fmt.Errorf("connection reset")
		}
		return okResponse(), nil
	})
	sink.send(context.Background(), activeEvent())
	if *attempts != 2 {
		t.Fatalf("attempts = %d, want 2 (one network error then a successful retry)", *attempts)
	}
}

func TestPostHogSinkDoesNotRetryPermanentRejection(t *testing.T) {
	// A 4xx (other than 429) will fail identically on retry, so retrying only
	// wastes a call; it must send exactly once.
	sink, attempts := newRetryTestSink(t, func(_ int) (*http.Response, error) {
		return statusResponse(http.StatusBadRequest), nil
	})
	sink.send(context.Background(), activeEvent())
	if *attempts != 1 {
		t.Fatalf("attempts = %d, want 1 (permanent 4xx must not retry)", *attempts)
	}
}

func TestPostHogSinkStopsAfterMaxAttemptsOnSustainedFailure(t *testing.T) {
	sink, attempts := newRetryTestSink(t, func(_ int) (*http.Response, error) {
		return statusResponse(http.StatusInternalServerError), nil
	})
	sink.send(context.Background(), activeEvent())
	if *attempts != postHogSendMaxAttempts {
		t.Fatalf("attempts = %d, want %d (bounded by postHogSendMaxAttempts)", *attempts, postHogSendMaxAttempts)
	}
}

func TestPostHogSinkSucceedsFirstTryMakesOneRequest(t *testing.T) {
	// The healthy path must add no extra billable request.
	sink, attempts := newRetryTestSink(t, func(_ int) (*http.Response, error) {
		return okResponse(), nil
	})
	sink.send(context.Background(), activeEvent())
	if *attempts != 1 {
		t.Fatalf("attempts = %d, want 1 (a successful send must not retry)", *attempts)
	}
}

func TestPostHogSinkRetryReusesSameEventUUID(t *testing.T) {
	// Idempotency: both the first attempt and the retry must carry the same
	// non-empty uuid so PostHog dedupes a first request it may already have
	// ingested, rather than double-counting it.
	var uuids []string
	sink, err := NewPostHogSink(t.TempDir(), "phc_test", "https://example.test", "", "", roundTripClient(func(req *http.Request) (*http.Response, error) {
		raw, _ := io.ReadAll(req.Body)
		var parsed map[string]any
		_ = json.Unmarshal(raw, &parsed)
		got, _ := parsed["uuid"].(string)
		uuids = append(uuids, got)
		if len(uuids) == 1 {
			return statusResponse(http.StatusInternalServerError), nil
		}
		return okResponse(), nil
	}), nil)
	if err != nil {
		t.Fatalf("NewPostHogSink: %v", err)
	}
	sink.send(context.Background(), activeEvent())
	if len(uuids) != 2 {
		t.Fatalf("captured %d requests, want 2 (retried once)", len(uuids))
	}
	if uuids[0] == "" {
		t.Fatalf("event carried no uuid; PostHog cannot dedupe a retried send")
	}
	if uuids[0] != uuids[1] {
		t.Fatalf("retry used a different uuid: %q vs %q", uuids[0], uuids[1])
	}
}

func TestPostHogSinkCancelledContextStopsRetry(t *testing.T) {
	// An already-cancelled context must not perform a retry after the first
	// transient failure: shutdown cancellation stops pending retry work.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	sink, attempts := newRetryTestSink(t, func(_ int) (*http.Response, error) {
		return statusResponse(http.StatusInternalServerError), nil
	})
	sink.send(ctx, activeEvent())
	if *attempts != 1 {
		t.Fatalf("attempts = %d, want 1 (a cancelled context must skip the retry backoff)", *attempts)
	}
}
