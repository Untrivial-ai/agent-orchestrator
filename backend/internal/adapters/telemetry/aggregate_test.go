package telemetry

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type syncRecordingSink struct {
	mu     sync.Mutex
	events []ports.TelemetryEvent
	closed bool
}

func (s *syncRecordingSink) Emit(_ context.Context, ev ports.TelemetryEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, ev)
}

func (s *syncRecordingSink) Close(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}

func (s *syncRecordingSink) snapshot() []ports.TelemetryEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]ports.TelemetryEvent, len(s.events))
	copy(out, s.events)
	return out
}

func TestAggregatingSinkPassesThroughUnaggregatedNamesImmediately(t *testing.T) {
	rec := &syncRecordingSink{}
	s := NewAggregatingSink(rec, []string{"ao.http.5xx"}, time.Hour)
	defer s.Close(context.Background())

	s.Emit(context.Background(), ports.TelemetryEvent{Name: "ao.cli.invoked"})

	events := rec.snapshot()
	if len(events) != 1 || events[0].Name != "ao.cli.invoked" {
		t.Fatalf("events = %#v, want one immediate ao.cli.invoked passthrough", events)
	}
}

func TestAggregatingSinkFoldsBurstIntoOneRollupOnFlush(t *testing.T) {
	rec := &syncRecordingSink{}
	s := NewAggregatingSink(rec, []string{"ao.http.5xx"}, time.Hour)

	for i := 0; i < 812; i++ {
		s.Emit(context.Background(), ports.TelemetryEvent{
			Name:    "ao.http.5xx",
			Payload: map[string]any{"path": "/api/v1/whatever"},
		})
	}
	if got := len(rec.snapshot()); got != 0 {
		t.Fatalf("events before flush = %d, want 0 (buffered, not yet flushed)", got)
	}

	s.flush(context.Background())

	events := rec.snapshot()
	if len(events) != 1 {
		t.Fatalf("events after flush = %d, want 1 rollup", len(events))
	}
	if events[0].Name != "ao.http.5xx" {
		t.Fatalf("rollup name = %q, want ao.http.5xx", events[0].Name)
	}
	if count, _ := events[0].Payload["count"].(int); count != 812 {
		t.Fatalf("rollup count = %#v, want 812", events[0].Payload["count"])
	}
	if events[0].Payload["path"] != "/api/v1/whatever" {
		t.Fatalf("rollup payload lost sample dims: %#v", events[0].Payload)
	}
	windowStart, ok := events[0].Payload["window_start"].(string)
	if !ok || windowStart == "" {
		t.Fatalf("rollup payload window_start = %#v, want non-empty RFC3339 string", events[0].Payload["window_start"])
	}
	if _, err := time.Parse(time.RFC3339Nano, windowStart); err != nil {
		t.Fatalf("window_start not RFC3339: %v", err)
	}
	windowEnd, ok := events[0].Payload["window_end"].(string)
	if !ok || windowEnd == "" {
		t.Fatalf("rollup payload window_end = %#v, want non-empty RFC3339 string", events[0].Payload["window_end"])
	}
	if _, err := time.Parse(time.RFC3339Nano, windowEnd); err != nil {
		t.Fatalf("window_end not RFC3339: %v", err)
	}
}

func TestAggregatingSinkTracksEventNamesIndependently(t *testing.T) {
	rec := &syncRecordingSink{}
	s := NewAggregatingSink(rec, []string{"ao.http.5xx", "ao.daemon.panic"}, time.Hour)

	for i := 0; i < 3; i++ {
		s.Emit(context.Background(), ports.TelemetryEvent{Name: "ao.http.5xx"})
	}
	s.Emit(context.Background(), ports.TelemetryEvent{Name: "ao.daemon.panic"})
	s.flush(context.Background())

	events := rec.snapshot()
	counts := map[string]int{}
	for _, ev := range events {
		count, _ := ev.Payload["count"].(int)
		counts[ev.Name] = count
	}
	if counts["ao.http.5xx"] != 3 {
		t.Fatalf("ao.http.5xx rollup count = %d, want 3", counts["ao.http.5xx"])
	}
	if counts["ao.daemon.panic"] != 1 {
		t.Fatalf("ao.daemon.panic rollup count = %d, want 1", counts["ao.daemon.panic"])
	}
}

func TestAggregatingSinkClosesFlushesBufferedEvents(t *testing.T) {
	rec := &syncRecordingSink{}
	s := NewAggregatingSink(rec, []string{"ao.http.5xx"}, time.Hour)

	s.Emit(context.Background(), ports.TelemetryEvent{Name: "ao.http.5xx"})
	if err := s.Close(context.Background()); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	events := rec.snapshot()
	if len(events) != 1 || events[0].Payload["count"] != 1 {
		t.Fatalf("events after Close = %#v, want one rollup with count 1", events)
	}
	if !rec.closed {
		t.Fatal("wrapped sink was not closed")
	}
}

func TestAggregatingSinkClosedTwiceDoesNotFlushAgain(t *testing.T) {
	rec := &syncRecordingSink{}
	s := NewAggregatingSink(rec, []string{"ao.http.5xx"}, time.Hour)

	s.Emit(context.Background(), ports.TelemetryEvent{Name: "ao.http.5xx"})
	if err := s.Close(context.Background()); err != nil {
		t.Fatalf("first Close() error = %v", err)
	}
	if err := s.Close(context.Background()); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}

	if got := len(rec.snapshot()); got != 1 {
		t.Fatalf("events after double Close = %d, want 1 (no duplicate rollup)", got)
	}
}

type blockingFlushSink struct {
	emitStarted  chan struct{}
	releaseEmit  chan struct{}
	closeStarted chan struct{}
	closeErr     error

	emitOnce        sync.Once
	closeOnce       sync.Once
	releaseOnce     sync.Once
	mu              sync.Mutex
	closes          int
	counts          []int
	emitsAfterClose int
}

func (s *blockingFlushSink) Emit(_ context.Context, ev ports.TelemetryEvent) {
	s.emitOnce.Do(func() { close(s.emitStarted) })
	<-s.releaseEmit
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closes > 0 {
		s.emitsAfterClose++
	}
	count, _ := ev.Payload["count"].(int)
	s.counts = append(s.counts, count)
}

func (s *blockingFlushSink) unblock() {
	s.releaseOnce.Do(func() { close(s.releaseEmit) })
}

func (s *blockingFlushSink) Close(context.Context) error {
	s.mu.Lock()
	s.closes++
	s.mu.Unlock()
	s.closeOnce.Do(func() { close(s.closeStarted) })
	return s.closeErr
}

func (s *blockingFlushSink) closeCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closes
}

func newBlockedAggregatingSink(t *testing.T, closeErr error) (*AggregatingSink, *blockingFlushSink) {
	t.Helper()
	next := &blockingFlushSink{
		emitStarted:  make(chan struct{}),
		releaseEmit:  make(chan struct{}),
		closeStarted: make(chan struct{}),
		closeErr:     closeErr,
	}
	sink := NewAggregatingSink(next, []string{"ao.http.5xx"}, time.Millisecond)
	t.Cleanup(func() {
		next.unblock()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = sink.Close(ctx)
	})
	sink.Emit(context.Background(), ports.TelemetryEvent{Name: "ao.http.5xx"})
	select {
	case <-next.emitStarted:
	case <-time.After(time.Second):
		t.Fatal("ticker flush did not reach downstream sink")
	}
	return sink, next
}

func TestAggregatingSinkCloseJoinsTickerFlushAndSharesResult(t *testing.T) {
	downstreamErr := errors.New("downstream close failed")
	sink, next := newBlockedAggregatingSink(t, downstreamErr)
	// The ticker already owns the first window. These belong to the final one.
	sink.Emit(context.Background(), ports.TelemetryEvent{Name: "ao.http.5xx"})
	sink.Emit(context.Background(), ports.TelemetryEvent{Name: "ao.http.5xx"})

	beginClose := make(chan struct{})
	callersReady := make(chan struct{}, 2)
	results := make(chan error, 2)
	for range 2 {
		go func() {
			callersReady <- struct{}{}
			<-beginClose
			results <- sink.Close(context.Background())
		}()
	}
	<-callersReady
	<-callersReady
	close(beginClose)

	closedBeforeFlushFinished := false
	select {
	case <-next.closeStarted:
		closedBeforeFlushFinished = true
	case <-time.After(50 * time.Millisecond):
	}
	next.unblock()

	for range 2 {
		select {
		case err := <-results:
			if !errors.Is(err, downstreamErr) {
				t.Errorf("Close() error = %v, want %v", err, downstreamErr)
			}
		case <-time.After(time.Second):
			t.Fatal("Close() did not finish after ticker flush was released")
		}
	}
	if closedBeforeFlushFinished {
		t.Error("downstream Close started before the ticker flush finished")
	}
	if got := next.closeCount(); got != 1 {
		t.Errorf("downstream Close calls = %d, want 1", got)
	}
	next.mu.Lock()
	defer next.mu.Unlock()
	if next.emitsAfterClose != 0 {
		t.Errorf("rollups emitted after downstream close = %d, want 0", next.emitsAfterClose)
	}
	if len(next.counts) != 2 || next.counts[0] != 1 || next.counts[1] != 2 {
		t.Errorf("rollup counts = %v, want [1 2]", next.counts)
	}
}

func TestAggregatingSinkCloseJoinsTickerWithoutFinalBucket(t *testing.T) {
	sink, next := newBlockedAggregatingSink(t, nil)
	// The ticker already owns the only bucket. A final flush has no Emit
	// to block on, so only joining the ticker can hold downstream Close.
	result := make(chan error, 1)
	go func() { result <- sink.Close(context.Background()) }()

	select {
	case <-next.closeStarted:
		t.Error("downstream Close started while the ticker's only rollup was blocked")
	case <-time.After(50 * time.Millisecond):
	}
	next.unblock()
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close() did not finish after ticker flush was released")
	}
	next.mu.Lock()
	defer next.mu.Unlock()
	if next.closes != 1 || next.emitsAfterClose != 0 {
		t.Errorf("downstream closes = %d, emits after close = %d; want 1 and 0", next.closes, next.emitsAfterClose)
	}
	if len(next.counts) != 1 || next.counts[0] != 1 {
		t.Errorf("rollup counts = %v, want [1]", next.counts)
	}
}

func TestAggregatingSinkCanceledCloseLeavesShutdownForLaterCaller(t *testing.T) {
	downstreamErr := errors.New("downstream close failed")
	sink, next := newBlockedAggregatingSink(t, downstreamErr)

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := sink.Close(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("Close(canceled) error = %v, want context canceled", err)
	}
	select {
	case <-next.closeStarted:
		t.Fatal("downstream Close started while the ticker flush was blocked")
	default:
	}

	laterResult := make(chan error, 1)
	go func() { laterResult <- sink.Close(context.Background()) }()
	select {
	case err := <-laterResult:
		t.Fatalf("later Close returned before the ticker flush finished: %v", err)
	default:
	}

	next.unblock()
	select {
	case err := <-laterResult:
		if !errors.Is(err, downstreamErr) {
			t.Fatalf("later Close error = %v, want %v", err, downstreamErr)
		}
	case <-time.After(time.Second):
		t.Fatal("later Close did not observe shutdown completion")
	}
	if got := next.closeCount(); got != 1 {
		t.Errorf("downstream Close calls = %d, want 1", got)
	}
}

func TestAggregatingSinkDiscardsAggregatedEventsAfterClose(t *testing.T) {
	rec := &syncRecordingSink{}
	sink := NewAggregatingSink(rec, []string{"ao.http.5xx"}, time.Hour)
	if err := sink.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	for range 100 {
		sink.Emit(context.Background(), ports.TelemetryEvent{Name: "ao.http.5xx"})
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if len(sink.buckets) != 0 {
		t.Fatalf("closed aggregator retained %d buckets without a flush loop", len(sink.buckets))
	}
}
