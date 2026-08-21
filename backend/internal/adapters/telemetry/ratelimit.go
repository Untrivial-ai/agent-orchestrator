package telemetry

import (
	"context"
	"sync"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// A per-minute cap alone doesn't bound the daily total: a loop that paces
// itself just under the minute ceiling (e.g. one call every 3-4 seconds)
// would sit under it forever and still rack up a large daily count. Two caps
// close that: a burst ceiling for real signal in a short window, and a hard
// daily ceiling that bounds worst case regardless of pacing. Both bound the
// billed (remote) sink only; local storage keeps every occurrence.
const (
	// eventsPerNamePerMinute caps a short burst per event name. 5 is enough
	// for genuine failure reporting (several real 5xx/panics while something
	// is actually broken) without leaving room for a tight retry loop.
	eventsPerNamePerMinute = 5
	// eventsPerNamePerDay is the hard ceiling per event name regardless of
	// how a runaway is paced. At anonymous PostHog rates this bounds a single
	// looping event name to a few dollars a month at worst, not hundreds.
	eventsPerNamePerDay = 200
	// eventsPerNamePerDayAggregated applies to event names an upstream
	// AggregatingSink has already folded into at most one rollup event per
	// flush window (see aggregate.go) before they ever reach this limiter.
	// The true occurrence count for the window is compressed into that one
	// event's `count` field, so per-occurrence cost is already gone; this
	// tier exists as a structural backstop (in case the aggregator itself
	// misbehaves), not as the real limiting mechanism, so it can be much
	// higher. 1500/day comfortably covers a name flushing every minute for a
	// full day (1440) with headroom, at a cost of a few cents a month even
	// in the worst case.
	eventsPerNamePerDayAggregated = 1500
	// eventsPerNamePerMinuteMetering / eventsPerNamePerDayMetering apply to
	// legitimately high-frequency, per-occurrence-meaningful metering events
	// (e.g. ao.session.token_usage, one per session end). These cannot be
	// aggregated because each event carries distinct numeric totals that must
	// survive individually, and the standard 5/min ceiling would silently drop
	// exactly the heaviest users — an orchestrator fleet can end far more than
	// five sessions in a minute. The caps are still finite so a client looping
	// the reporting path stays cost-bounded (worst case a few dollars a month
	// per install at anonymous rates), just generous enough that real usage is
	// never truncated.
	eventsPerNamePerMinuteMetering = 30
	eventsPerNamePerDayMetering    = 1000
)

// RateLimitedSink wraps a sink and drops events past a per-event-name rate
// ceiling. Intended to wrap only the remote (billed) sink; local storage
// should see every event unfiltered.
type RateLimitedSink struct {
	next ports.EventSink

	// aggregated marks event names that get the generous daily tier because
	// an upstream AggregatingSink already compresses their occurrence count
	// into one rollup per flush window.
	aggregated map[string]struct{}

	// metering marks legitimately high-frequency, unaggregatable events that
	// get the metering tier (see eventsPerNamePer*Metering).
	metering map[string]struct{}

	mu      sync.Mutex
	minutes map[string]*rateWindow
	days    map[string]*rateWindow
}

type rateWindow struct {
	start time.Time
	count int
}

// NewRateLimitedSink wraps next with the per-event-name rate ceiling.
// aggregatedNames identifies event names that are pre-aggregated upstream
// (see NewAggregatingSink) and should get the generous daily tier instead of
// the standard one; pass nil if next has no aggregation in front of it.
func NewRateLimitedSink(next ports.EventSink, aggregatedNames []string) *RateLimitedSink {
	aggregated := make(map[string]struct{}, len(aggregatedNames))
	for _, n := range aggregatedNames {
		aggregated[n] = struct{}{}
	}
	return &RateLimitedSink{
		next:       next,
		aggregated: aggregated,
		metering:   make(map[string]struct{}),
		minutes:    make(map[string]*rateWindow),
		days:       make(map[string]*rateWindow),
	}
}

// WithMeteringNames marks event names that get the high-frequency metering tier
// (see eventsPerNamePer*Metering) rather than the standard ceiling. It returns
// the same sink for chaining and is meant to be called once at construction,
// before the sink starts receiving events.
func (s *RateLimitedSink) WithMeteringNames(names ...string) *RateLimitedSink {
	for _, n := range names {
		s.metering[n] = struct{}{}
	}
	return s
}

// Emit forwards ev to the wrapped sink unless its event name has exceeded
// either ceiling, in which case it is silently dropped.
func (s *RateLimitedSink) Emit(ctx context.Context, ev ports.TelemetryEvent) {
	if !s.reserve(ev.Name, time.Now()) {
		return
	}
	s.next.Emit(ctx, ev)
}

func (s *RateLimitedSink) reserve(name string, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	minuteLimit, dayLimit := s.limitsFor(name)
	if !reserveWindow(s.minutes, name, now, time.Minute, minuteLimit) {
		return false
	}
	return reserveWindow(s.days, name, now, 24*time.Hour, dayLimit)
}

// limitsFor returns the per-minute and per-day ceilings for an event name. The
// metering tier takes precedence over the aggregated tier so a name marked as
// both is never throttled below its metering allowance.
func (s *RateLimitedSink) limitsFor(name string) (minute, day int) {
	if _, ok := s.metering[name]; ok {
		return eventsPerNamePerMinuteMetering, eventsPerNamePerDayMetering
	}
	if _, ok := s.aggregated[name]; ok {
		return eventsPerNamePerMinute, eventsPerNamePerDayAggregated
	}
	return eventsPerNamePerMinute, eventsPerNamePerDay
}

func reserveWindow(windows map[string]*rateWindow, name string, now time.Time, size time.Duration, limit int) bool {
	w, ok := windows[name]
	if !ok || now.Sub(w.start) >= size {
		w = &rateWindow{start: now}
		windows[name] = w
	}
	if w.count >= limit {
		return false
	}
	w.count++
	return true
}

// Close closes the wrapped sink.
func (s *RateLimitedSink) Close(ctx context.Context) error {
	return s.next.Close(ctx)
}
