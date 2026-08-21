package telemetry

import (
	"context"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type recordingSink struct {
	events []ports.TelemetryEvent
}

func (s *recordingSink) Emit(_ context.Context, ev ports.TelemetryEvent) {
	s.events = append(s.events, ev)
}

func (s *recordingSink) Close(context.Context) error { return nil }

func TestRateLimitedSinkCapsBurstPerMinute(t *testing.T) {
	rec := &recordingSink{}
	s := NewRateLimitedSink(rec, nil)
	ctx := context.Background()
	now := time.Now()

	for i := 0; i < eventsPerNamePerMinute+10; i++ {
		s.Emit(ctx, ports.TelemetryEvent{Name: "ao.http.5xx", OccurredAt: now})
	}
	if len(rec.events) != eventsPerNamePerMinute {
		t.Fatalf("events forwarded = %d, want %d", len(rec.events), eventsPerNamePerMinute)
	}
}

func TestRateLimitedSinkCapsTotalPerDayEvenWhenPacedUnderBurstLimit(t *testing.T) {
	s := NewRateLimitedSink(&recordingSink{}, nil)
	start := time.Now()

	// One reservation per (simulated) minute never trips the burst cap, but
	// the day cap must still bound the total once it's exhausted. Driving
	// reserve() directly with synthetic timestamps, rather than Emit (which
	// rate-limits by real wall-clock arrival time, not a caller-supplied
	// event timestamp), is what actually simulates pacing here.
	var forwarded int
	for i := 0; i < eventsPerNamePerDay+10; i++ {
		if s.reserve("ao.http.5xx", start) {
			forwarded++
		}
		start = start.Add(time.Minute)
	}
	if forwarded != eventsPerNamePerDay {
		t.Fatalf("events forwarded = %d, want %d", forwarded, eventsPerNamePerDay)
	}
}

func TestRateLimitedSinkGivesAggregatedNamesTheGenerousDailyTier(t *testing.T) {
	s := NewRateLimitedSink(&recordingSink{}, []string{"ao.http.5xx"})
	start := time.Now()

	// eventsPerNamePerDayAggregated (1500) exceeds the number of minutes in a
	// day (1440), so pacing one reservation per minute would let the 24-hour
	// window itself roll over and hand out a second, fresh budget before the
	// cap is ever reached - that would test the window reset, not the cap.
	// Pacing faster (30s) keeps the whole loop inside one 24-hour window so
	// the cap is what actually gets exercised.
	const step = 30 * time.Second
	var forwarded int
	for i := 0; i < eventsPerNamePerDayAggregated+10; i++ {
		if s.reserve("ao.http.5xx", start) {
			forwarded++
		}
		start = start.Add(step)
	}
	if forwarded != eventsPerNamePerDayAggregated {
		t.Fatalf("events forwarded = %d, want %d (aggregated tier)", forwarded, eventsPerNamePerDayAggregated)
	}
}

func TestRateLimitedSinkNonAggregatedNameKeepsStandardTierEvenWhenOthersAreAggregated(t *testing.T) {
	s := NewRateLimitedSink(&recordingSink{}, []string{"ao.http.5xx"})
	start := time.Now()

	var forwarded int
	for i := 0; i < eventsPerNamePerDay+10; i++ {
		if s.reserve("ao.daemon.started", start) {
			forwarded++
		}
		start = start.Add(time.Minute)
	}
	if forwarded != eventsPerNamePerDay {
		t.Fatalf("events forwarded = %d, want %d (standard tier for a non-aggregated name)", forwarded, eventsPerNamePerDay)
	}
}

func TestRateLimitedSinkMeteringTierRaisesBurstCeiling(t *testing.T) {
	rec := &recordingSink{}
	s := NewRateLimitedSink(rec, nil).WithMeteringNames("ao.session.token_usage")
	ctx := context.Background()
	now := time.Now()

	for i := 0; i < eventsPerNamePerMinuteMetering; i++ {
		s.Emit(ctx, ports.TelemetryEvent{Name: "ao.session.token_usage", OccurredAt: now})
	}
	if len(rec.events) != eventsPerNamePerMinuteMetering {
		t.Fatalf("metering events forwarded = %d, want %d", len(rec.events), eventsPerNamePerMinuteMetering)
	}
	s.Emit(ctx, ports.TelemetryEvent{Name: "ao.session.token_usage", OccurredAt: now})
	if len(rec.events) != eventsPerNamePerMinuteMetering {
		t.Fatalf("metering burst not capped: forwarded = %d, want %d", len(rec.events), eventsPerNamePerMinuteMetering)
	}
}

func TestRateLimitedSinkMeteringTierDailyCeiling(t *testing.T) {
	s := NewRateLimitedSink(&recordingSink{}, nil).WithMeteringNames("ao.session.token_usage")
	start := time.Now()

	const step = 30 * time.Second
	var forwarded int
	for i := 0; i < eventsPerNamePerDayMetering+10; i++ {
		if s.reserve("ao.session.token_usage", start) {
			forwarded++
		}
		start = start.Add(step)
	}
	if forwarded != eventsPerNamePerDayMetering {
		t.Fatalf("metering events forwarded = %d, want %d", forwarded, eventsPerNamePerDayMetering)
	}
}

func TestRateLimitedSinkTracksEventNamesIndependently(t *testing.T) {
	rec := &recordingSink{}
	s := NewRateLimitedSink(rec, nil)
	ctx := context.Background()
	now := time.Now()

	for i := 0; i < eventsPerNamePerMinute; i++ {
		s.Emit(ctx, ports.TelemetryEvent{Name: "ao.http.5xx", OccurredAt: now})
	}
	s.Emit(ctx, ports.TelemetryEvent{Name: "ao.daemon.panic", OccurredAt: now})

	var panics int
	for _, ev := range rec.events {
		if ev.Name == "ao.daemon.panic" {
			panics++
		}
	}
	if panics != 1 {
		t.Fatalf("ao.daemon.panic forwarded = %d, want 1 (independent window from ao.http.5xx)", panics)
	}
}

func TestRateLimitedSinkBurstWindowResetsAfterOneMinute(t *testing.T) {
	rec := &recordingSink{}
	s := NewRateLimitedSink(rec, nil)
	ctx := context.Background()
	now := time.Now()

	for i := 0; i < eventsPerNamePerMinute; i++ {
		s.Emit(ctx, ports.TelemetryEvent{Name: "ao.http.5xx", OccurredAt: now})
	}
	if !s.reserve("ao.http.5xx", now.Add(61*time.Second)) {
		t.Fatal("reserve should succeed once the one-minute burst window has elapsed")
	}
}

func TestRateLimitedSinkDayWindowResetsAfter24Hours(t *testing.T) {
	s := NewRateLimitedSink(&recordingSink{}, nil)
	start := time.Now()

	for i := 0; i < eventsPerNamePerDay; i++ {
		if !s.reserve("ao.http.5xx", start) {
			t.Fatalf("reserve unexpectedly failed at i=%d, before the daily ceiling should be reached", i)
		}
		start = start.Add(time.Minute)
	}
	if s.reserve("ao.http.5xx", start) {
		t.Fatal("reserve should fail once the daily ceiling is exhausted within the same day")
	}
	if !s.reserve("ao.http.5xx", start.Add(24*time.Hour)) {
		t.Fatal("reserve should succeed once the 24-hour day window has elapsed")
	}
}
