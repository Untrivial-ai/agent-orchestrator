package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
)

type flushRecorder struct {
	*httptest.ResponseRecorder
	flushed bool
}

func (r *flushRecorder) Flush() {
	r.flushed = true
	r.ResponseRecorder.Flush()
}

func TestStatusResponseWriterPreservesStreaming(t *testing.T) {
	t.Parallel()
	underlying := &flushRecorder{ResponseRecorder: httptest.NewRecorder()}
	response := &statusResponseWriter{ResponseWriter: underlying}

	flusher, ok := any(response).(http.Flusher)
	if !ok {
		t.Fatal("request logging response writer does not preserve http.Flusher")
	}
	flusher.Flush()
	if !underlying.flushed {
		t.Fatal("flush was not delegated to the underlying response writer")
	}
}

func TestNotificationEventStreamReplaysInSequenceAndStopsOnDrain(t *testing.T) {
	store := &notificationStreamStore{events: []domain.NotificationEvent{
		{Sequence: 4, Kind: domain.NotificationEventCreated, EventID: "evt-4", Notification: domain.Notification{ID: "n-4", Source: "cloud"}},
		{Sequence: 5, Kind: domain.NotificationEventUpdated, EventID: "evt-5", Notification: domain.Notification{ID: "n-5", Source: "cloud"}},
	}}
	srv := New(Options{Store: store})
	srv.SetDraining(true)
	req := notificationUserRequest(http.MethodGet, "/notification-events?after=3", "org-1", "user-1", nil)
	req.Header.Set("Accept", "text/event-stream")
	recorder := httptest.NewRecorder()

	srv.notificationEvents(recorder, req)

	body := recorder.Body.String()
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Header().Get("Content-Type"), "text/event-stream") {
		t.Fatalf("status=%d content-type=%q body=%s", recorder.Code, recorder.Header().Get("Content-Type"), body)
	}
	first := strings.Index(body, "id: 4\nevent: notification_created")
	second := strings.Index(body, "id: 5\nevent: notification_updated")
	if first < 0 || second <= first {
		t.Fatalf("events are missing or out of sequence: %s", body)
	}
	if store.after != 3 || store.orgID != "org-1" || store.principal.UserID != "user-1" {
		t.Fatalf("store args after=%d org=%q principal=%+v", store.after, store.orgID, store.principal)
	}
}

func TestNotificationEventStreamRejectsUnsafeCursor(t *testing.T) {
	t.Parallel()
	srv := New(Options{Store: &notificationStreamStore{}})
	req := notificationUserRequest(http.MethodGet, "/notification-events?after=9007199254740992", "org-1", "user-1", nil)
	req.Header.Set("Accept", "text/event-stream")
	recorder := httptest.NewRecorder()
	srv.notificationEvents(recorder, req)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestNotificationEventStreamWritesKeepalive(t *testing.T) {
	originalPoll, originalKeepalive := notificationStreamPollInterval, notificationStreamKeepaliveInterval
	notificationStreamPollInterval = time.Hour
	notificationStreamKeepaliveInterval = time.Millisecond
	t.Cleanup(func() {
		notificationStreamPollInterval = originalPoll
		notificationStreamKeepaliveInterval = originalKeepalive
	})
	srv := New(Options{Store: &notificationStreamStore{}})
	req := notificationUserRequest(http.MethodGet, "/notification-events", "org-1", "user-1", nil)
	req.Header.Set("Accept", "text/event-stream")
	ctx, cancel := context.WithCancel(req.Context())
	req = req.WithContext(ctx)
	recorder := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		srv.notificationEvents(recorder, req)
		close(done)
	}()
	time.Sleep(10 * time.Millisecond)
	cancel()
	<-done
	if !strings.Contains(recorder.Body.String(), ": keepalive\n\n") {
		t.Fatalf("stream did not write keepalive: %q", recorder.Body.String())
	}
}

func TestNotificationWaitersWakeOnlyMatchingOrganization(t *testing.T) {
	t.Parallel()
	waiters := newNotificationWaiters()
	orgA, cancelA := waiters.subscribe("org-a")
	defer cancelA()
	orgB, cancelB := waiters.subscribe("org-b")
	defer cancelB()
	waiters.notify("org-a")
	select {
	case <-orgA:
	default:
		t.Fatal("org-a subscriber was not woken")
	}
	select {
	case <-orgB:
		t.Fatal("org-b subscriber received org-a wake")
	default:
	}
}

type notificationStreamStore struct {
	Store
	events    []domain.NotificationEvent
	after     int64
	orgID     string
	principal domain.Principal
}

func (s *notificationStreamStore) ListNotificationEvents(_ context.Context, principal domain.Principal, orgID string, after int64, _ int) ([]domain.NotificationEvent, bool, error) {
	s.principal, s.orgID, s.after = principal, orgID, after
	return s.events, false, nil
}
