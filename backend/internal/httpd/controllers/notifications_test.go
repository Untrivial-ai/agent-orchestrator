package controllers_test

import (
	"bufio"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/config"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/controllers"
	notificationsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/notification"
)

type fakeNotificationService struct {
	gotFilter     notificationsvc.ListFilter
	gotMarkID     string
	gotMarkAllIDs []string
	items         []notificationsvc.Notification
	markItem      notificationsvc.Notification
	markAllCount  int64
	err           error
}

type fakeNotificationStream struct {
	gotProject domain.ProjectID
	ch         chan domain.NotificationEvent
}

func (f *fakeNotificationService) List(_ context.Context, filter notificationsvc.ListFilter) (notificationsvc.ListPage, error) {
	f.gotFilter = filter
	return notificationsvc.ListPage{Notifications: f.items, NextCursor: "next-page", UnreadCount: 7}, f.err
}

func (f *fakeNotificationService) MarkRead(_ context.Context, id string) (notificationsvc.Notification, bool, error) {
	f.gotMarkID = id
	return f.markItem, f.err == nil, f.err
}

func (f *fakeNotificationService) MarkAllRead(_ context.Context, ids []string) (int64, error) {
	f.gotMarkAllIDs = ids
	return f.markAllCount, f.err
}

func (f *fakeNotificationStream) Subscribe(projectID domain.ProjectID) (<-chan domain.NotificationEvent, func()) {
	f.gotProject = projectID
	if f.ch == nil {
		f.ch = make(chan domain.NotificationEvent, 1)
	}
	return f.ch, func() {}
}

func newNotificationTestServer(t *testing.T, svc controllers.NotificationService) *httptest.Server {
	t.Helper()
	return newNotificationStreamTestServer(t, svc, nil)
}

func newNotificationStreamTestServer(t *testing.T, svc controllers.NotificationService, stream controllers.NotificationStream) *httptest.Server {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := httptest.NewServer(httpd.NewRouterWithControl(config.Config{}, log, nil, httpd.APIDeps{Notifications: svc, NotificationStream: stream}, httpd.ControlDeps{}))
	t.Cleanup(srv.Close)
	return srv
}

func notificationsvcNotFound() error {
	return apierr.NotFound("NOTIFICATION_NOT_FOUND", "Unknown unread notification")
}

func TestNotificationsAPI_ListUnread(t *testing.T) {
	now := time.Date(2026, 6, 11, 10, 0, 0, 0, time.UTC)
	svc := &fakeNotificationService{items: []notificationsvc.Notification{{
		NotificationRecord: domain.NotificationRecord{ID: "ntf_1", SessionID: "mer-1", ProjectID: "mer", Type: domain.NotificationNeedsInput, Title: "checkout-flow needs input", Body: "The agent is waiting for your response.", Status: domain.NotificationUnread, CreatedAt: now},
		Target:             notificationsvc.Target{Kind: notificationsvc.TargetSession, SessionID: "mer-1"},
	}}}
	srv := newNotificationTestServer(t, svc)

	body, status, _ := doRequest(t, srv, "GET", "/api/v1/notifications?limit=10&cursor=previous-page", "")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", status, body)
	}
	if svc.gotFilter.Limit != 10 {
		t.Fatalf("filter = %+v", svc.gotFilter)
	}
	if svc.gotFilter.Status != notificationsvc.ListUnread {
		t.Fatalf("status = %q, want unread", svc.gotFilter.Status)
	}
	if svc.gotFilter.Cursor != "previous-page" {
		t.Fatalf("cursor = %q, want previous-page", svc.gotFilter.Cursor)
	}
	var resp struct {
		Notifications []struct {
			ID        string `json:"id"`
			SessionID string `json:"sessionId"`
			ProjectID string `json:"projectId"`
			Type      string `json:"type"`
			Status    string `json:"status"`
			Target    struct {
				Kind      string `json:"kind"`
				SessionID string `json:"sessionId"`
			} `json:"target"`
		} `json:"notifications"`
		NextCursor  string `json:"nextCursor"`
		UnreadCount int    `json:"unreadCount"`
	}
	mustJSON(t, body, &resp)
	if len(resp.Notifications) != 1 || resp.Notifications[0].ID != "ntf_1" ||
		resp.Notifications[0].Target.Kind != "session" || resp.NextCursor != "next-page" || resp.UnreadCount != 7 {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestNotificationsAPI_ListAllHistory(t *testing.T) {
	svc := &fakeNotificationService{}
	srv := newNotificationTestServer(t, svc)

	_, status, _ := doRequest(t, srv, "GET", "/api/v1/notifications?status=all", "")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if svc.gotFilter.Status != notificationsvc.ListAll || svc.gotFilter.Limit != notificationsvc.DefaultListLimit {
		t.Fatalf("filter = %+v", svc.gotFilter)
	}
}

func TestNotificationsAPI_DefaultsAndCapsLimit(t *testing.T) {
	svc := &fakeNotificationService{}
	srv := newNotificationTestServer(t, svc)

	_, status, _ := doRequest(t, srv, "GET", "/api/v1/notifications?limit=9999", "")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if svc.gotFilter.Limit != notificationsvc.MaxListLimit {
		t.Fatalf("limit = %d, want cap %d", svc.gotFilter.Limit, notificationsvc.MaxListLimit)
	}
}

func TestNotificationsAPI_RejectsUnsupportedStatus(t *testing.T) {
	srv := newNotificationTestServer(t, &fakeNotificationService{})

	body, status, _ := doRequest(t, srv, "GET", "/api/v1/notifications?status=read", "")
	assertErrorCode(t, body, status, http.StatusBadRequest, "INVALID_QUERY")
}

func TestNotificationsAPI_MarkRead(t *testing.T) {
	now := time.Date(2026, 6, 11, 10, 0, 0, 0, time.UTC)
	svc := &fakeNotificationService{markItem: notificationsvc.Notification{
		NotificationRecord: domain.NotificationRecord{
			ID: "ntf_1", SessionID: "mer-1", ProjectID: "mer", Type: domain.NotificationNeedsInput,
			Title: "checkout-flow needs input", Status: domain.NotificationRead, CreatedAt: now,
		},
		Target: notificationsvc.Target{Kind: notificationsvc.TargetSession, SessionID: "mer-1"},
	}}
	srv := newNotificationTestServer(t, svc)

	body, status, _ := doRequest(t, srv, "PATCH", "/api/v1/notifications/ntf_1", `{"status":"read"}`)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", status, body)
	}
	if svc.gotMarkID != "ntf_1" {
		t.Fatalf("gotMarkID = %q", svc.gotMarkID)
	}
	var resp struct {
		Notification struct {
			ID     string `json:"id"`
			Status string `json:"status"`
			Target struct {
				Kind string `json:"kind"`
			} `json:"target"`
		} `json:"notification"`
	}
	mustJSON(t, body, &resp)
	if resp.Notification.ID != "ntf_1" || resp.Notification.Status != "read" || resp.Notification.Target.Kind != "session" {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestNotificationsAPI_MarkReadRejectsUnsupportedStatus(t *testing.T) {
	srv := newNotificationTestServer(t, &fakeNotificationService{})

	body, status, _ := doRequest(t, srv, "PATCH", "/api/v1/notifications/ntf_1", `{"status":"unread"}`)
	assertErrorCode(t, body, status, http.StatusBadRequest, "INVALID_NOTIFICATION_STATUS")
}

func TestNotificationsAPI_MarkReadUnknownNotification(t *testing.T) {
	srv := newNotificationTestServer(t, &fakeNotificationService{err: notificationsvcNotFound()})

	body, status, _ := doRequest(t, srv, "PATCH", "/api/v1/notifications/missing", `{"status":"read"}`)
	assertErrorCode(t, body, status, http.StatusNotFound, "NOTIFICATION_NOT_FOUND")
}

func TestNotificationsAPI_MarkAllRead(t *testing.T) {
	svc := &fakeNotificationService{markAllCount: 123}
	srv := newNotificationTestServer(t, svc)

	body, status, _ := doRequest(t, srv, "POST", "/api/v1/notifications/read-all", "")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", status, body)
	}
	var resp struct {
		Notifications []controllers.NotificationResponse `json:"notifications"`
		UpdatedCount  int64                              `json:"updatedCount"`
	}
	mustJSON(t, body, &resp)
	if len(resp.Notifications) != 0 || resp.UpdatedCount != 123 {
		t.Fatalf("resp = %+v", resp)
	}
	if svc.gotMarkAllIDs != nil {
		t.Fatalf("empty body must mean every unread row, got ids %v", svc.gotMarkAllIDs)
	}
}

// A paginating client acknowledges exactly what it rendered, so notifications
// past its last loaded page stay unread and remain reachable.
func TestNotificationsAPI_MarkAllReadAcknowledgesOnlyGivenIDs(t *testing.T) {
	svc := &fakeNotificationService{markAllCount: 2}
	srv := newNotificationTestServer(t, svc)

	body, status, _ := doRequest(t, srv, "POST", "/api/v1/notifications/read-all", `{"ids":["ntf_1","ntf_2"]}`)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", status, body)
	}
	if len(svc.gotMarkAllIDs) != 2 || svc.gotMarkAllIDs[0] != "ntf_1" || svc.gotMarkAllIDs[1] != "ntf_2" {
		t.Fatalf("ids = %v", svc.gotMarkAllIDs)
	}
}

func TestNotificationsAPI_MarkAllReadRejectsInvalidBody(t *testing.T) {
	srv := newNotificationTestServer(t, &fakeNotificationService{})
	body, status, _ := doRequest(t, srv, "POST", "/api/v1/notifications/read-all", "{not json")
	assertErrorCode(t, body, status, http.StatusBadRequest, "INVALID_JSON")
}

func TestNotificationsAPI_WithoutServiceIs501(t *testing.T) {
	srv := newNotificationTestServer(t, nil)

	body, status, _ := doRequest(t, srv, "GET", "/api/v1/notifications", "")
	assertErrorCode(t, body, status, http.StatusNotImplemented, "NOT_IMPLEMENTED")
}

func TestNotificationsAPI_StreamCreatedNotifications(t *testing.T) {
	stream := &fakeNotificationStream{ch: make(chan domain.NotificationEvent, 1)}
	srv := newNotificationStreamTestServer(t, &fakeNotificationService{}, stream)

	resp, err := srv.Client().Get(srv.URL + "/api/v1/notifications/stream?projectId=mer")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("content-type = %q", ct)
	}
	if stream.gotProject != "mer" {
		t.Fatalf("project filter = %q", stream.gotProject)
	}

	rec := domain.NotificationRecord{ID: "ntf_1", SessionID: "mer-1", ProjectID: "mer", Type: domain.NotificationNeedsInput, Title: "needs input", Status: domain.NotificationUnread, CreatedAt: time.Now()}
	stream.ch <- domain.NotificationEvent{Kind: domain.NotificationCreated, Record: rec}
	reader := bufio.NewReader(resp.Body)
	readSSE := func() (string, string) {
		t.Helper()
		eventLine, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		dataLine, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(eventLine), dataLine
	}
	if eventLine, dataLine := readSSE(); eventLine != "event: notification_created" || !strings.Contains(dataLine, `"id":"ntf_1"`) {
		t.Fatalf("eventLine=%q dataLine=%q", eventLine, dataLine)
	}

	// A resolved event lets an open panel drop the row without a refetch.
	if _, err := reader.ReadString('\n'); err != nil { // blank separator line
		t.Fatal(err)
	}
	resolved := rec
	resolved.ResolvedAt = time.Now()
	stream.ch <- domain.NotificationEvent{Kind: domain.NotificationResolved, Record: resolved}
	if eventLine, dataLine := readSSE(); eventLine != "event: notification_resolved" || !strings.Contains(dataLine, `"resolvedAt"`) {
		t.Fatalf("eventLine=%q dataLine=%q", eventLine, dataLine)
	}
}

func TestNotificationsAPI_StreamWithoutPublisherIs501(t *testing.T) {
	srv := newNotificationStreamTestServer(t, &fakeNotificationService{}, nil)
	body, status, _ := doRequest(t, srv, "GET", "/api/v1/notifications/stream", "")
	assertErrorCode(t, body, status, http.StatusNotImplemented, "NOT_IMPLEMENTED")
}

func TestNotificationsAPI_StreamHeartbeatsWhileIdle(t *testing.T) {
	restore := controllers.NotificationsHeartbeatInterval
	controllers.NotificationsHeartbeatInterval = 50 * time.Millisecond
	defer func() { controllers.NotificationsHeartbeatInterval = restore }()

	stream := &fakeNotificationStream{ch: make(chan domain.NotificationEvent)}
	srv := newNotificationStreamTestServer(t, &fakeNotificationService{}, stream)

	resp, err := srv.Client().Get(srv.URL + "/api/v1/notifications/stream")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	reader := bufio.NewReader(resp.Body)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(line, ":") {
			return
		}
	}
	t.Fatal("idle notification stream sent no heartbeat comment frame")
}

type trackingNotificationStream struct {
	mu           sync.Mutex
	unsubscribed bool
	ch           chan domain.NotificationEvent
}

func (s *trackingNotificationStream) Subscribe(projectID domain.ProjectID) (<-chan domain.NotificationEvent, func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.unsubscribed = false
	if s.ch == nil {
		s.ch = make(chan domain.NotificationEvent, 1)
	}
	return s.ch, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.unsubscribed = true
	}
}

func (s *trackingNotificationStream) isUnsubscribed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.unsubscribed
}

type timeoutOnNotificationWriter struct {
	*httptest.ResponseRecorder
	mu       sync.Mutex
	deadline time.Time
}

func (w *timeoutOnNotificationWriter) SetWriteDeadline(t time.Time) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.deadline = t
	return nil
}

func (w *timeoutOnNotificationWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	d := w.deadline
	w.mu.Unlock()
	if strings.Contains(string(p), "event:") {
		if !d.IsZero() && time.Now().Before(d) {
			time.Sleep(time.Until(d) + 5*time.Millisecond)
		}
		return 0, context.DeadlineExceeded
	}
	return w.ResponseRecorder.Write(p)
}

func TestNotificationsAPI_StreamBlockedClientExitsAndUnsubscribes(t *testing.T) {
	restoreTimeout := controllers.NotificationsWriteTimeout
	controllers.NotificationsWriteTimeout = 50 * time.Millisecond
	defer func() { controllers.NotificationsWriteTimeout = restoreTimeout }()

	stream := &trackingNotificationStream{ch: make(chan domain.NotificationEvent, 1)}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	router := httpd.NewRouterWithControl(config.Config{}, log, nil, httpd.APIDeps{
		Notifications:      &fakeNotificationService{},
		NotificationStream: stream,
	}, httpd.ControlDeps{})

	tw := &timeoutOnNotificationWriter{ResponseRecorder: httptest.NewRecorder()}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/notifications/stream", nil)

	handlerDone := make(chan struct{})
	go func() {
		router.ServeHTTP(tw, req)
		close(handlerDone)
	}()

	rec := domain.NotificationRecord{ID: "ntf_block", Type: domain.NotificationNeedsInput, Title: "block"}
	stream.ch <- domain.NotificationEvent{Kind: domain.NotificationCreated, Record: rec}

	select {
	case <-handlerDone:
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not exit within bounded write deadline")
	}

	if !stream.isUnsubscribed() {
		t.Fatal("expected stream to unsubscribe after blocked write")
	}
}

func TestNotificationsAPI_StreamContextCancellationUnsubscribes(t *testing.T) {
	stream := &trackingNotificationStream{ch: make(chan domain.NotificationEvent, 1)}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	router := httpd.NewRouterWithControl(config.Config{}, log, nil, httpd.APIDeps{
		Notifications:      &fakeNotificationService{},
		NotificationStream: stream,
	}, httpd.ControlDeps{})

	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "/api/v1/notifications/stream", nil).WithContext(ctx)
	rec := httptest.NewRecorder()

	handlerDone := make(chan struct{})
	go func() {
		router.ServeHTTP(rec, req)
		close(handlerDone)
	}()

	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case <-handlerDone:
	case <-time.After(time.Second):
		t.Fatal("handler did not exit after context cancellation")
	}

	if !stream.isUnsubscribed() {
		t.Fatal("expected stream to unsubscribe after context cancellation")
	}
}

