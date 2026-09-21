package notificationoutbox

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestFlusherDeletesOnlyAfterDurableDelivery(t *testing.T) {
	t.Parallel()
	outbox := openTestOutbox(t)
	event := Event{EventID: "evt-1", EventType: "needs_input", Payload: []byte(`{}`), OccurredAt: time.Now(), WorkerEpoch: 4}
	if err := outbox.Enqueue(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	delivered := 0
	flusher := Flusher{Outbox: outbox, WorkerEpoch: 4, Deliver: func(context.Context, Event) error { delivered++; return nil }, Clock: fixedClock{now: time.Now()}}
	processed, err := flusher.FlushOne(context.Background())
	if err != nil || !processed || delivered != 1 {
		t.Fatalf("FlushOne() = %v, %v; delivered=%d", processed, err, delivered)
	}
	count, _, _ := outbox.Usage(context.Background())
	if count != 0 {
		t.Fatalf("outbox retained %d ACKed rows", count)
	}
}

func TestFlusherRetainsFailureWithBoundedBackoff(t *testing.T) {
	t.Parallel()
	outbox := openTestOutbox(t)
	if err := outbox.Enqueue(context.Background(), Event{EventID: "evt-1", EventType: "needs_input", Payload: []byte(`{}`), OccurredAt: time.Now(), WorkerEpoch: 4}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().Add(time.Second)
	flusher := Flusher{Outbox: outbox, WorkerEpoch: 4, Deliver: func(context.Context, Event) error { return errors.New("offline") }, Clock: fixedClock{now: now}, Jitter: func(time.Duration) time.Duration { return 0 }}
	processed, err := flusher.FlushOne(context.Background())
	if !processed || err == nil {
		t.Fatalf("FlushOne() = %v, %v", processed, err)
	}
	ready, err := outbox.Ready(context.Background(), now.Add(999*time.Millisecond), 1)
	if err != nil || len(ready) != 0 {
		t.Fatalf("event retried too early: %+v, %v", ready, err)
	}
	ready, err = outbox.Ready(context.Background(), now.Add(time.Second), 1)
	if err != nil || len(ready) != 1 || ready[0].AttemptCount != 1 {
		t.Fatalf("retry row = %+v, %v", ready, err)
	}
}

func TestFlusherDiscardsRowsFromSupersededEpochWithoutDelivery(t *testing.T) {
	t.Parallel()
	outbox := openTestOutbox(t)
	if err := outbox.Enqueue(context.Background(), Event{EventID: "evt-old", EventType: "needs_input", Payload: []byte(`{}`), OccurredAt: time.Now(), WorkerEpoch: 4}); err != nil {
		t.Fatal(err)
	}
	delivered := 0
	flusher := Flusher{Outbox: outbox, WorkerEpoch: 5, Deliver: func(context.Context, Event) error { delivered++; return nil }, Clock: fixedClock{now: time.Now()}}
	processed, err := flusher.FlushOne(context.Background())
	if err != nil || !processed || delivered != 0 {
		t.Fatalf("FlushOne() = %v, %v; delivered=%d", processed, err, delivered)
	}
	count, _, _ := outbox.Usage(context.Background())
	if count != 0 {
		t.Fatalf("superseded row count = %d", count)
	}
}

func TestFlusherRunStopsOnCancellation(t *testing.T) {
	t.Parallel()
	outbox := openTestOutbox(t)
	clock := newBlockingClock()
	flusher := Flusher{Outbox: outbox, WorkerEpoch: 1, Deliver: func(context.Context, Event) error { return nil }, Clock: clock}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- flusher.Run(ctx) }()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not stop after cancellation")
	}
}

type fixedClock struct{ now time.Time }

func (c fixedClock) Now() time.Time                       { return c.now }
func (c fixedClock) After(time.Duration) <-chan time.Time { return make(chan time.Time) }

type blockingClock struct {
	mu  sync.Mutex
	now time.Time
}

func newBlockingClock() *blockingClock                        { return &blockingClock{now: time.Now()} }
func (c *blockingClock) Now() time.Time                       { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *blockingClock) After(time.Duration) <-chan time.Time { return make(chan time.Time) }
