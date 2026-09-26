package notificationoutbox

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestOutboxPersistsStableEventsAcrossReopen(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "notification-outbox.db")
	outbox, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	event := Event{EventID: "evt-stable", EventType: "needs_input", Payload: []byte(`{"activityId":"tool-1"}`), OccurredAt: time.Unix(10, 0).UTC(), WorkerEpoch: 4}
	if err := outbox.Enqueue(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if err := outbox.Enqueue(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if err := outbox.Close(); err != nil {
		t.Fatal(err)
	}

	outbox, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer outbox.Close()
	ready, err := outbox.Ready(context.Background(), time.Now().Add(time.Hour), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(ready) != 1 || ready[0].EventID != event.EventID || ready[0].AttemptCount != 0 || ready[0].WorkerEpoch != 4 {
		t.Fatalf("Ready() = %+v", ready)
	}
}

func TestOutboxSupportsConcurrentHookWriterAndWorkerReader(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "notification-outbox.db")
	writer, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	reader, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()

	var wait sync.WaitGroup
	wait.Add(2)
	go func() {
		defer wait.Done()
		for index := 0; index < 50; index++ {
			if err := writer.Enqueue(context.Background(), Event{EventID: fmt.Sprintf("evt-%03d", index), EventType: "needs_input", Payload: []byte(`{}`), OccurredAt: time.Now(), WorkerEpoch: 1}); err != nil {
				t.Errorf("enqueue: %v", err)
				return
			}
		}
	}()
	go func() {
		defer wait.Done()
		for index := 0; index < 50; index++ {
			if _, err := reader.Ready(context.Background(), time.Now().Add(time.Hour), 10); err != nil {
				t.Errorf("ready: %v", err)
				return
			}
		}
	}()
	wait.Wait()
	count := outboxRowCount(t, reader)
	if count != 50 {
		t.Fatalf("row count = %d, want 50", count)
	}
}

func outboxRowCount(t *testing.T, outbox *Outbox) int {
	t.Helper()
	var count int
	if err := outbox.db.QueryRowContext(context.Background(), `SELECT count(*) FROM notification_outbox`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestOutboxRetryAndDeleteRequireMatchingEventAndEpoch(t *testing.T) {
	t.Parallel()
	outbox := openTestOutbox(t)
	event := Event{EventID: "evt-1", EventType: "agent_failed", Payload: []byte(`{}`), OccurredAt: time.Now(), WorkerEpoch: 7}
	if err := outbox.Enqueue(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	next := time.Now().Add(time.Minute)
	if err := outbox.MarkRetry(context.Background(), event.EventID, event.WorkerEpoch, 2, next); err != nil {
		t.Fatal(err)
	}
	ready, err := outbox.Ready(context.Background(), next.Add(-time.Second), 1)
	if err != nil || len(ready) != 0 {
		t.Fatalf("early Ready() = %+v, %v", ready, err)
	}
	if err := outbox.Delete(context.Background(), event.EventID, 8); !errors.Is(err, ErrNotFound) {
		t.Fatalf("wrong epoch Delete() error = %v", err)
	}
	if err := outbox.Delete(context.Background(), event.EventID, 7); err != nil {
		t.Fatal(err)
	}
}

func TestOutboxEnforcesRowAndByteBounds(t *testing.T) {
	t.Parallel()
	outbox := openTestOutbox(t)
	for index := 0; index < MaxRows; index++ {
		if err := outbox.Enqueue(context.Background(), Event{EventID: fmt.Sprintf("evt-%04d", index), EventType: "needs_input", Payload: []byte(`{}`), OccurredAt: time.Now(), WorkerEpoch: 1}); err != nil {
			t.Fatalf("enqueue %d: %v", index, err)
		}
	}
	if err := outbox.Enqueue(context.Background(), Event{EventID: "overflow", EventType: "needs_input", Payload: []byte(`{}`), OccurredAt: time.Now(), WorkerEpoch: 1}); !errors.Is(err, ErrFull) {
		t.Fatalf("row overflow error = %v, want ErrFull", err)
	}

	bytesOutbox := openTestOutbox(t)
	if err := bytesOutbox.Enqueue(context.Background(), Event{EventID: "too-large", EventType: "needs_input", Payload: make([]byte, MaxBytes+1), OccurredAt: time.Now(), WorkerEpoch: 1}); !errors.Is(err, ErrFull) {
		t.Fatalf("byte overflow error = %v, want ErrFull", err)
	}
}

func openTestOutbox(t *testing.T) *Outbox {
	t.Helper()
	outbox, err := Open(filepath.Join(t.TempDir(), "notification-outbox.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = outbox.Close() })
	return outbox
}
