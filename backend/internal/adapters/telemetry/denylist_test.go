package telemetry

import (
	"context"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// emitAll pushes bare named events through sink; recordingSink is shared with
// ratelimit_test.go.
func emitAll(sink ports.EventSink, names ...string) {
	for _, name := range names {
		sink.Emit(context.Background(), ports.TelemetryEvent{Name: name})
	}
}

func forwardedNames(rec *recordingSink) []string {
	names := make([]string, 0, len(rec.events))
	for _, ev := range rec.events {
		names = append(names, ev.Name)
	}
	return names
}

func TestDenylistSinkPassesEverythingWhenUnconfigured(t *testing.T) {
	next := &recordingSink{}
	sink := NewDenylistSink(next, nil)
	if sink != ports.EventSink(next) {
		t.Fatal("empty denylist should return the wrapped sink unchanged")
	}
	emitAll(sink, "ao.app.active", "ao.session.spawned")
	if len(next.events) != 2 {
		t.Fatalf("names = %v, want both events forwarded", forwardedNames(next))
	}
}

func TestDenylistSinkDropsNamedEvents(t *testing.T) {
	next := &recordingSink{}
	sink := NewDenylistSink(next, []string{"ao.session.spawned"})
	emitAll(sink, "ao.app.active", "ao.session.spawned", "ao.http.5xx")
	if len(next.events) != 2 || forwardedNames(next)[0] != "ao.app.active" || forwardedNames(next)[1] != "ao.http.5xx" {
		t.Fatalf("names = %v, want the denied event dropped", forwardedNames(next))
	}
}

// An operator reading PostHog sees the exported alias, not the internal name.
// Typing either one must silence the stream, or the switch is a trap.
func TestDenylistSinkMatchesEitherInternalOrExportedName(t *testing.T) {
	for _, denied := range []string{"ao.app.active", "ao.v2.app.active"} {
		next := &recordingSink{}
		sink := NewDenylistSink(next, []string{denied})
		emitAll(sink, "ao.app.active")
		if len(next.events) != 0 {
			t.Fatalf("denylist %q forwarded %v, want dropped", denied, forwardedNames(next))
		}
	}
}

func TestDenylistSinkSupportsPrefixWildcardAndIsCaseInsensitive(t *testing.T) {
	next := &recordingSink{}
	sink := NewDenylistSink(next, []string{"  AO.RENDERER.*  "})
	emitAll(sink, "ao.renderer.route_viewed", "ao.renderer.api_error", "ao.session.spawned")
	if len(next.events) != 1 || forwardedNames(next)[0] != "ao.session.spawned" {
		t.Fatalf("names = %v, want only the non-renderer event forwarded", forwardedNames(next))
	}
}

// A denylist of only blank or bare "*" entries must not silence everything by
// accident, and must not add a pointless wrapper either.
func TestDenylistSinkIgnoresEmptyEntries(t *testing.T) {
	next := &recordingSink{}
	sink := NewDenylistSink(next, []string{"", "   ", "*"})
	if sink != ports.EventSink(next) {
		t.Fatal("blank entries should leave the sink unwrapped")
	}
	emitAll(sink, "ao.app.active")
	if len(next.events) != 1 {
		t.Fatalf("names = %v, want the event forwarded", forwardedNames(next))
	}
}

func TestEventDenylistMatchesSink(t *testing.T) {
	for _, names := range [][]string{nil, {"", "*"}, {" AO.GITHUB.* "}, {"ao.v2.app.active"}, {"ao.github.account_observed"}} {
		matcher := NewEventDenylist(names)
		for _, name := range []string{"ao.github.account_observed", "ao.app.active", "ao.session.spawned"} {
			next := &recordingSink{}
			NewDenylistSink(next, names).Emit(t.Context(), ports.TelemetryEvent{Name: name})
			if got := matcher.Blocks(name); got != (len(next.events) == 0) {
				t.Fatalf("names=%v event=%s matcher=%v forwarded=%d", names, name, got, len(next.events))
			}
		}
	}
}
