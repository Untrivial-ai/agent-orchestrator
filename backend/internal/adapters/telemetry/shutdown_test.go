package telemetry

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	sqlitestore "github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/store"
)

type shutdownLocalStore func(context.Context) error

func (s shutdownLocalStore) CreateTelemetryEvent(ctx context.Context, _ sqlitestore.TelemetryEventRecord) error {
	return s(ctx)
}

func (shutdownLocalStore) PruneTelemetryEventsBefore(context.Context, time.Time, int64) (int64, error) {
	return 0, nil
}

func newShutdownTestSink(t *testing.T, kind string, consume func(context.Context) error) (ports.EventSink, int) {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	var sink ports.EventSink
	bufferSize := localBufferSize
	if kind == "local" {
		sink = NewLocalSQLiteSink(shutdownLocalStore(consume), log)
	} else {
		var err error
		sink, err = NewPostHogSink(t.TempDir(), "phc_test", "https://posthog.test", "", "", roundTripClient(func(req *http.Request) (*http.Response, error) {
			defer req.Body.Close()
			if err := consume(req.Context()); err != nil {
				return nil, err
			}
			return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
		}), log)
		if err != nil {
			t.Fatalf("NewPostHogSink: %v", err)
		}
		bufferSize = postHogBufferSize
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := sink.Close(ctx); err != nil {
			t.Errorf("cleanup Close: %v", err)
		}
	})
	return sink, bufferSize
}

func TestDestinationSinkEmitAfterCloseIsDropped(t *testing.T) {
	for _, kind := range []string{"local", "posthog"} {
		t.Run(kind, func(t *testing.T) {
			var consumed atomic.Int32
			sink, _ := newShutdownTestSink(t, kind, func(context.Context) error { consumed.Add(1); return nil })
			if err := sink.Close(context.Background()); err != nil {
				t.Fatalf("Close: %v", err)
			}
			defer func() {
				if p := recover(); p != nil {
					t.Errorf("Emit after Close panicked: %v", p)
				}
			}()
			sink.Emit(context.Background(), ports.TelemetryEvent{Name: "ao.test.late"})
			if got := consumed.Load(); got != 0 {
				t.Errorf("late events consumed = %d, want 0", got)
			}
		})
	}
}

func TestDestinationSinkConcurrentEmitAndClose(t *testing.T) {
	for _, kind := range []string{"local", "posthog"} {
		t.Run(kind, func(t *testing.T) {
			sink, _ := newShutdownTestSink(t, kind, func(context.Context) error { return nil })
			start := make(chan struct{})
			const producers = 8
			panics := make(chan any, producers)
			var wg sync.WaitGroup
			for range producers {
				wg.Add(1)
				go func() {
					defer wg.Done()
					defer func() {
						if p := recover(); p != nil {
							panics <- p
						}
					}()
					<-start
					for range 2_000 {
						sink.Emit(context.Background(), ports.TelemetryEvent{Name: "ao.test.concurrent"})
					}
				}()
			}
			closed := make(chan error, 1)
			go func() { <-start; closed <- sink.Close(context.Background()) }()
			close(start)
			done := make(chan struct{})
			go func() { wg.Wait(); close(done) }()
			waitShutdownSignal(t, done)
			waitShutdownClose(t, closed)
			close(panics)
			for p := range panics {
				t.Errorf("Emit concurrent with Close panicked: %v", p)
			}
		})
	}
}

func TestDestinationSinkCloseDrainsQueueAndFullBufferDoesNotBlock(t *testing.T) {
	for _, kind := range []string{"local", "posthog"} {
		t.Run(kind, func(t *testing.T) {
			started := make(chan struct{})
			release := make(chan struct{})
			var startOnce, releaseOnce sync.Once
			var consumed atomic.Int32
			sink, capacity := newShutdownTestSink(t, kind, func(context.Context) error {
				startOnce.Do(func() { close(started) })
				<-release
				consumed.Add(1)
				return nil
			})
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			t.Cleanup(unblock) // Release the worker before the sink's cleanup, even after a failure.
			sink.Emit(context.Background(), ports.TelemetryEvent{Name: "ao.test.first"})
			waitShutdownSignal(t, started)
			filled := make(chan struct{})
			go func() {
				defer close(filled)
				for range capacity + 1 {
					sink.Emit(context.Background(), ports.TelemetryEvent{Name: "ao.test.queued"})
				}
			}()
			waitShutdownSignal(t, filled)
			const closers = 4
			results := make(chan error, closers)
			for range closers {
				go func() { results <- sink.Close(context.Background()) }()
			}
			select {
			case err := <-results:
				t.Fatalf("Close returned before worker was released: %v", err)
			case <-time.After(50 * time.Millisecond):
			}
			unblock()
			for range closers {
				waitShutdownClose(t, results)
			}
			if got, want := consumed.Load(), int32(capacity+1); got != want {
				t.Errorf("consumed = %d, want %d (in-flight event plus full queue)", got, want)
			}
			if err := sink.Close(context.Background()); err != nil {
				t.Errorf("repeated Close: %v", err)
			}
		})
	}
}

func TestLocalSQLiteSinkCancelledCloseAllowsLaterDrain(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	sink, _ := newShutdownTestSink(t, "local", func(context.Context) error {
		close(started)
		<-release
		return nil
	})
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	sink.Emit(context.Background(), ports.TelemetryEvent{Name: "ao.test.blocked"})
	waitShutdownSignal(t, started)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	closed := make(chan error, 1)
	go func() { closed <- sink.Close(ctx) }()
	select {
	case err := <-closed:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Close = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled Close did not return while the write was blocked")
	}
	go func() { closed <- sink.Close(context.Background()) }()
	unblock()
	waitShutdownClose(t, closed)
}

func TestPostHogSinkCancelledCloseCancelsInFlightRequest(t *testing.T) {
	started := make(chan struct{})
	finished := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	sink, _ := newShutdownTestSink(t, "posthog", func(ctx context.Context) error {
		close(started)
		select {
		case <-ctx.Done():
		case <-release:
		}
		close(finished)
		return ctx.Err()
	})
	// Cleanup must also release the fake if request cancellation regresses.
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	sink.Emit(context.Background(), ports.TelemetryEvent{Name: "ao.test.blocked"})
	waitShutdownSignal(t, started)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	closed := make(chan error, 1)
	go func() { closed <- sink.Close(ctx) }()
	select {
	case err := <-closed:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Close = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not cancel its in-flight request")
	}
	select {
	case <-finished:
	default:
		t.Fatal("Close returned before the request finished")
	}
}

func waitShutdownSignal(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for telemetry worker or producer")
	}
}

func waitShutdownClose(t *testing.T, ch <-chan error) {
	t.Helper()
	select {
	case err := <-ch:
		if err != nil {
			t.Fatalf("Close: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not finish")
	}
}
