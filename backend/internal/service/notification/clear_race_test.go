package notification

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/notify"
)

type clearRaceBarrier struct {
	mu            sync.Mutex
	lockRequested chan struct{}
}

func (b *clearRaceBarrier) Lock() {
	select {
	case b.lockRequested <- struct{}{}:
	default:
	}
	b.mu.Lock()
}

func (b *clearRaceBarrier) Unlock() {
	b.mu.Unlock()
}

type delayedNotificationPublisher struct {
	started chan struct{}
	events  chan domain.NotificationEvent
	release chan struct{}
	publish func(context.Context, domain.NotificationEvent) error
}

func (p *delayedNotificationPublisher) Publish(ctx context.Context, event domain.NotificationEvent) error {
	p.events <- event
	close(p.started)
	select {
	case <-p.release:
		if p.publish == nil {
			return nil
		}
		return p.publish(ctx, event)
	case <-ctx.Done():
		return ctx.Err()
	}
}

type clearRaceStore struct {
	fakeStore
	cleared chan struct{}
}

func (s *clearRaceStore) CreateNotification(_ context.Context, rec domain.NotificationRecord) (domain.NotificationRecord, bool, error) {
	return rec, true, nil
}

func (s *clearRaceStore) ResolveSessionNotifications(
	context.Context,
	domain.SessionID,
	domain.NotificationType,
	time.Time,
) ([]domain.NotificationRecord, error) {
	return nil, nil
}

func (s *clearRaceStore) ResolvePRNotifications(
	context.Context,
	string,
	domain.NotificationType,
	time.Time,
) ([]domain.NotificationRecord, error) {
	return nil, nil
}

func (s *clearRaceStore) ReconcileResolvedNotifications(context.Context, time.Time) ([]domain.NotificationRecord, error) {
	return nil, nil
}

func (s *clearRaceStore) ClearAllNotifications(context.Context) (int64, error) {
	close(s.cleared)
	return 1, nil
}

// A reconnect after successful clear must not observe a notification whose
// persistence began before clear.
func TestClearAllSerializesDelayedPublishBeforeReconnect(t *testing.T) {
	barrier := &clearRaceBarrier{lockRequested: make(chan struct{}, 2)}
	hub := notify.NewHub()
	oldSource, unsubscribeOld := hub.Subscribe("project-1")
	defer unsubscribeOld()
	publisher := &delayedNotificationPublisher{
		started: make(chan struct{}),
		events:  make(chan domain.NotificationEvent, 1),
		release: make(chan struct{}),
		publish: hub.Publish,
	}
	store := &clearRaceStore{cleared: make(chan struct{})}
	writer := notify.New(notify.Deps{
		Store:     store,
		Publisher: publisher,
		Barrier:   barrier,
		Clock:     func() time.Time { return time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC) },
		NewID:     func() string { return "ntf_race" },
	})
	reader := New(Deps{Store: store, Barrier: barrier})

	notifyDone := make(chan error, 1)
	go func() {
		notifyDone <- writer.Notify(context.Background(), notify.Intent{
			Type:      domain.NotificationNeedsInput,
			SessionID: "session-1",
			ProjectID: "project-1",
		})
	}()

	select {
	case <-publisher.started:
	case <-time.After(time.Second):
		t.Fatal("Notify did not reach delayed Publish")
	}
	select {
	case event := <-publisher.events:
		if event.Kind != domain.NotificationCreated || event.Record.ID != "ntf_race" {
			t.Fatalf("published event = %+v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("Notify did not publish the inserted notification")
	}
	select {
	case <-barrier.lockRequested:
	case <-time.After(time.Second):
		t.Fatal("Notify did not acquire the notification barrier")
	}

	clearDone := make(chan error, 1)
	go func() {
		_, err := reader.ClearAll(context.Background())
		clearDone <- err
	}()
	select {
	case <-barrier.lockRequested:
	case <-time.After(time.Second):
		t.Fatal("ClearAll did not try to acquire the notification barrier")
	}
	select {
	case err := <-clearDone:
		t.Fatalf("ClearAll returned before delayed Publish: %v", err)
	default:
	}

	close(publisher.release)
	if err := <-notifyDone; err != nil {
		t.Fatalf("Notify: %v", err)
	}
	select {
	case err := <-clearDone:
		if err != nil {
			t.Fatalf("ClearAll: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("ClearAll did not finish after Publish")
	}
	select {
	case event := <-oldSource:
		if event.Record.ID != "ntf_race" {
			t.Fatalf("old source event = %+v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("delayed notification was not published to the old source")
	}
	newSource, unsubscribeNew := hub.Subscribe("project-1")
	defer unsubscribeNew()
	select {
	case event := <-newSource:
		t.Fatalf("new source received pre-clear event: %+v", event)
	default:
	}
	select {
	case <-store.cleared:
	default:
		t.Fatal("ClearAll did not delete notifications")
	}
}
