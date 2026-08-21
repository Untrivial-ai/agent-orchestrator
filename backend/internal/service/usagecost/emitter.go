// Package usagecost prices normalized usage events from the ingestion pipeline
// and emits them to telemetry. It is the bridge between the local usage
// subsystem (which never leaves the daemon) and PostHog, where cost can be
// summed across users, days, sessions, and harnesses.
package usagecost

import (
	"context"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/internal/pricing"
)

// sessionStore resolves a session's harness/kind/project for tagging.
type sessionStore interface {
	GetSession(context.Context, domain.SessionID) (domain.SessionRecord, bool, error)
}

// Emitter turns applied usage events into priced ao.session.turn_usage
// telemetry. Each ingested event is applied exactly once by the pipeline (a
// durable cursor guards re-processing), so emitting here is exactly-once after
// commit: no dedupe marker is needed. It is best-effort, a nil sink or a
// failed session lookup degrades to emitting nothing rather than erroring.
type Emitter struct {
	sink  ports.EventSink
	store sessionStore
	now   func() time.Time
}

// NewEmitter constructs a usage-cost emitter.
func NewEmitter(sink ports.EventSink, store sessionStore) *Emitter {
	return &Emitter{sink: sink, store: store, now: time.Now}
}

// OnApplied prices and emits one telemetry event per applied usage event. It
// matches the ingestor's OnUsageApplied callback signature and runs only after
// the events have been durably committed.
func (e *Emitter) OnApplied(ctx context.Context, sessionID domain.SessionID, events []domain.ModelUsageEvent) {
	if e == nil || e.sink == nil || len(events) == 0 {
		return
	}
	var (
		harness string
		kind    string
		project domain.ProjectID
	)
	if e.store != nil {
		if rec, ok, err := e.store.GetSession(ctx, sessionID); err == nil && ok {
			harness = string(rec.Harness)
			kind = string(rec.Kind)
			project = rec.ProjectID
		}
	}
	sid := sessionID
	for _, ev := range events {
		t := ev.Tokens
		// Fresh (uncached) input is billed at the input rate; cache read/write
		// are separate line items. Pricing the uncached count avoids charging
		// cached tokens twice.
		cost, priced := pricing.Cost(ev.ModelID, t.UncachedInputTokens, t.OutputTokens, t.CacheReadTokens, t.CacheWriteTokens)
		payload := map[string]any{
			"harness":               harness,
			"kind":                  kind,
			"model":                 ev.ModelID,
			"model_priced":          priced,
			"input_tokens":          t.InputTokens,
			"uncached_input_tokens": t.UncachedInputTokens,
			"output_tokens":         t.OutputTokens,
			"cache_read_tokens":     t.CacheReadTokens,
			"cache_write_tokens":    t.CacheWriteTokens,
			"total_tokens":          t.InputTokens + t.OutputTokens + t.CacheReadTokens + t.CacheWriteTokens,
			"cost_usd":              cost,
		}
		ev := ports.TelemetryEvent{
			Name:       "ao.session.turn_usage",
			Source:     "usage_cost",
			OccurredAt: e.now(),
			Level:      ports.TelemetryLevelInfo,
			SessionID:  &sid,
			Payload:    payload,
		}
		if project != "" {
			ev.ProjectID = &project
		}
		e.sink.Emit(context.Background(), ev)
	}
}
