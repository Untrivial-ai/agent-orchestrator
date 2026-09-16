package daemon

import (
	"context"
	"sync"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// lazyTelemetrySink owns the daemon's one pipeline, including its tenure writer.
// Disabled events are dropped, not buffered for a later grant. Revocation gates
// subsequent emits but retains the pipeline so renewal cannot fork tenure state.
type lazyTelemetrySink struct {
	mu       sync.Mutex
	enabled  func() bool
	create   func() (ports.EventSink, error)
	next     ports.EventSink
	closed   bool
	closeErr error
}

func newLazyTelemetrySink(enabled func() bool, create func() (ports.EventSink, error)) *lazyTelemetrySink {
	return &lazyTelemetrySink{enabled: enabled, create: create}
}

func (s *lazyTelemetrySink) Emit(ctx context.Context, ev ports.TelemetryEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || !s.enabled() {
		return
	}
	if s.next == nil {
		next, err := s.create()
		// A failed factory must return no owned resources. Drop this event and
		// retry on a later enabled event; never replay the event that failed.
		if err != nil || next == nil {
			return
		}
		s.next = next
	}
	if !s.enabled() {
		return
	}
	// Serialize with Close and initialization. The configured sinks enqueue or
	// write locally; neither Emit, the factory, nor the policy getter calls back
	// into this holder. In particular, policy notifications do not take mu.
	s.next.Emit(ctx, ev)
}

func (s *lazyTelemetrySink) Close(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		s.closed = true
		if s.next != nil {
			s.closeErr = s.next.Close(ctx)
		}
	}
	return s.closeErr
}
