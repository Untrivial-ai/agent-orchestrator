package usagecost

import (
	"context"
	"math"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type captureSink struct{ events []ports.TelemetryEvent }

func (c *captureSink) Emit(_ context.Context, ev ports.TelemetryEvent) {
	c.events = append(c.events, ev)
}
func (c *captureSink) Close(context.Context) error { return nil }

type fakeStore struct{ rec domain.SessionRecord }

func (f fakeStore) GetSession(context.Context, domain.SessionID) (domain.SessionRecord, bool, error) {
	return f.rec, f.rec.ID != "", nil
}

func opusEvent() domain.ModelUsageEvent {
	return domain.ModelUsageEvent{
		ModelID: "claude-opus-4-8",
		Tokens: domain.UsageTokenMetrics{
			InputTokens: 1_000_000, UncachedInputTokens: 1_000_000, OutputTokens: 1_000_000,
		},
	}
}

func TestOnAppliedPricesAndTagsFromRecord(t *testing.T) {
	sink := &captureSink{}
	e := NewEmitter(sink, fakeStore{rec: domain.SessionRecord{ID: "ao-1", ProjectID: "proj", Kind: domain.KindWorker, Harness: "claude-code"}})
	e.OnApplied(context.Background(), "ao-1", []domain.ModelUsageEvent{opusEvent()})

	if len(sink.events) != 1 {
		t.Fatalf("events = %d, want 1", len(sink.events))
	}
	ev := sink.events[0]
	if ev.Name != "ao.session.turn_usage" {
		t.Fatalf("name = %q", ev.Name)
	}
	if ev.SessionID == nil || *ev.SessionID != "ao-1" || ev.ProjectID == nil || *ev.ProjectID != "proj" {
		t.Fatalf("ids = %v / %v", ev.SessionID, ev.ProjectID)
	}
	if ev.Payload["harness"] != "claude-code" {
		t.Fatalf("harness = %v (must come from the record, not the caller)", ev.Payload["harness"])
	}
	// Opus: 1M uncached input @ $15 + 1M output @ $75 = $90.
	if cost := ev.Payload["cost_usd"].(float64); math.Abs(cost-90.0) > 1e-9 {
		t.Fatalf("cost_usd = %v, want 90", cost)
	}
	if ev.Payload["model_priced"].(bool) != true {
		t.Fatalf("model_priced = %v", ev.Payload["model_priced"])
	}
}

func TestOnAppliedUnknownModelZeroCostStillEmits(t *testing.T) {
	sink := &captureSink{}
	e := NewEmitter(sink, fakeStore{rec: domain.SessionRecord{ID: "ao-1", Harness: "codex"}})
	e.OnApplied(context.Background(), "ao-1", []domain.ModelUsageEvent{{
		ModelID: "gpt-5", Tokens: domain.UsageTokenMetrics{UncachedInputTokens: 500, OutputTokens: 500},
	}})
	if len(sink.events) != 1 {
		t.Fatalf("events = %d, want 1", len(sink.events))
	}
	p := sink.events[0].Payload
	if p["cost_usd"].(float64) != 0 || p["model_priced"].(bool) != false {
		t.Fatalf("unknown model payload = %+v", p)
	}
}

func TestOnAppliedNilSinkAndEmptyEventsAreNoops(t *testing.T) {
	// nil sink: constructing with nil must not panic and must emit nothing.
	NewEmitter(nil, fakeStore{}).OnApplied(context.Background(), "ao-1", []domain.ModelUsageEvent{opusEvent()})

	sink := &captureSink{}
	NewEmitter(sink, fakeStore{}).OnApplied(context.Background(), "ao-1", nil)
	if len(sink.events) != 0 {
		t.Fatalf("empty events emitted %d", len(sink.events))
	}
}
