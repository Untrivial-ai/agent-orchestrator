package daemon

import (
	"log/slog"
	"time"

	telemetryadapter "github.com/aoagents/agent-orchestrator/backend/internal/adapters/telemetry"
	"github.com/aoagents/agent-orchestrator/backend/internal/config"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
)

// aggregatedEventNames are the event names prone to bursts (crash loops,
// retry storms) that get folded into one rollup event per minute instead of
// one PostHog event per occurrence, and correspondingly get the generous
// daily rate-limit tier since their per-occurrence cost is already gone (see
// RateLimitedSink's eventsPerNamePerDayAggregated). Must match the event
// names as actually emitted (see httpd/router.go and httpd/recover.go /
// httpd/log.go) exactly, including underscores - there is no compile-time
// check tying this list to the emit sites.
//
// Deliberately does NOT include ao.cli.invoked or ao.app.active: those are
// already deduped to at most once per command-path/day (ao.cli.invoked) or
// once per day (ao.app.active) at the httpd layer before they ever reach a
// sink, including for ao hooks/ao pty-host and high-frequency polling
// commands like ao status/ao session ls - adding them here would double up
// on dedup logic that already owns that job and gains nothing.
var aggregatedEventNames = []string{
	"ao.http.5xx",
	"ao.daemon.panic",
	"ao.cli.usage_errors",
}

// meteringEventNames are legitimately high-frequency, per-occurrence-meaningful
// events that cannot be aggregated (each carries distinct numeric totals) and
// so get the generous metering rate-limit tier instead of the standard 5/min
// ceiling. ao.session.token_usage fires once per session end; a busy user or an
// orchestrator fleet can exceed the standard tier, and dropping those events
// would under-count exactly the heaviest usage the metric is meant to rank.
var meteringEventNames = []string{
	"ao.session.token_usage",
}

func newTelemetrySink(cfg config.Config, store *sqlite.Store, log *slog.Logger) ports.EventSink {
	if !cfg.Telemetry.Events {
		return telemetryadapter.NoopSink{}
	}
	local := telemetryadapter.NewLocalSQLiteSink(store, log)
	if cfg.Telemetry.Remote != config.TelemetryRemotePostHog {
		return local
	}
	remote, err := telemetryadapter.NewPostHogSink(cfg.DataDir, cfg.Telemetry.PostHogKey, cfg.Telemetry.PostHogHost,
		cfg.Telemetry.AppVersion, cfg.Agent, nil, log)
	if err != nil {
		log.Warn("telemetry remote sink disabled", "remote", cfg.Telemetry.Remote, "error", err)
		return local
	}
	// Both wrap only the billed remote sink; local storage keeps every event
	// unaggregated and unfiltered for debugging regardless of PostHog volume.
	// Aggregation sits in front: burst-prone event names get folded into one
	// rollup per minute before they ever reach the rate limiter, which then
	// applies the generous tier to those same names as a structural backstop
	// rather than the primary cost control, and still does the real limiting
	// job for every event name that isn't aggregated.
	rateLimited := telemetryadapter.NewRateLimitedSink(remote, aggregatedEventNames).
		WithMeteringNames(meteringEventNames...)
	aggregated := telemetryadapter.NewAggregatingSink(rateLimited, aggregatedEventNames, time.Minute)
	// The kill switch sits outermost on the remote chain so a silenced stream
	// costs nothing downstream: no aggregation window, no rate-limit slot, no
	// export. Local storage is unaffected, so silenced events stay debuggable.
	denied := telemetryadapter.NewDenylistSink(aggregated, cfg.Telemetry.DisabledEvents)
	return telemetryadapter.NewFanoutSink(local, denied)
}
