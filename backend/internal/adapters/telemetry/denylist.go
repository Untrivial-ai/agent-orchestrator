package telemetry

import (
	"context"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// DenylistSink drops named event streams before they reach the sink it wraps.
//
// This is the telemetry kill switch. Every other control in this package is
// compiled into the build: to silence a stream we had to ship a release and
// wait for users to install it, which took weeks the last time a stream turned
// out to be expensive. A configured denylist can silence one immediately on the
// installs that already exist, because the supervisor passes it through on
// daemon startup.
//
// Wrap only the remote (billed) sink. Local SQLite storage should keep every
// event so a stream silenced in production is still debuggable locally.
type DenylistSink struct {
	next ports.EventSink
	EventDenylist
}

// EventDenylist matches remote stream names before collection or export.
// It includes exported aliases and the same exact/prefix rules used by the sink.
type EventDenylist struct {
	denied   map[string]struct{}
	prefixes []string
}

// NewDenylistSink wraps next, dropping any event whose name matches an entry in
// names. An entry ending in "*" matches by prefix, so "ao.renderer.*" silences a
// whole family without enumerating it. Matching is case-insensitive because
// these values are typed by a human under time pressure.
//
// Returns next unchanged when names is empty, so the common case adds no
// indirection.
func NewDenylistSink(next ports.EventSink, names []string) ports.EventSink {
	if next == nil || len(names) == 0 {
		return next
	}
	matcher := NewEventDenylist(names)
	if len(matcher.denied) == 0 && len(matcher.prefixes) == 0 {
		return next
	}
	return &DenylistSink{next: next, EventDenylist: matcher}
}

// NewEventDenylist compiles the case-insensitive exact and prefix denylist.
func NewEventDenylist(names []string) EventDenylist {
	s := EventDenylist{denied: make(map[string]struct{}, len(names))}
	for _, raw := range names {
		name := strings.ToLower(strings.TrimSpace(raw))
		if name == "" {
			continue
		}
		if strings.HasSuffix(name, "*") {
			if prefix := strings.TrimSuffix(name, "*"); prefix != "" {
				s.prefixes = append(s.prefixes, prefix)
			}
			continue
		}
		s.denied[name] = struct{}{}
	}
	return s
}

// Emit forwards the event unless its name is denied.
func (s *DenylistSink) Emit(ctx context.Context, ev ports.TelemetryEvent) {
	if s.Blocks(ev.Name) {
		return
	}
	s.next.Emit(ctx, ev)
}

// Close delegates: this sink owns no resources of its own, but it must not
// swallow the wrapped sink's shutdown, which is what flushes pending exports.
func (s *DenylistSink) Close(ctx context.Context) error {
	return s.next.Close(ctx)
}

// Blocks reports whether an event name is silenced. Both the internal name and
// the exported PostHog alias are checked, so an operator can type either the
// name they see in PostHog ("ao.v2.app.active") or the one in the source
// ("ao.app.active") and get the result they expect.
func (s EventDenylist) Blocks(name string) bool {
	for _, candidate := range []string{name, remoteEventName(name)} {
		lowered := strings.ToLower(strings.TrimSpace(candidate))
		if lowered == "" {
			continue
		}
		if _, ok := s.denied[lowered]; ok {
			return true
		}
		for _, prefix := range s.prefixes {
			if strings.HasPrefix(lowered, prefix) {
				return true
			}
		}
	}
	return false
}
