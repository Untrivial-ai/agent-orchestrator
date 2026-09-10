package notification

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
)

type fakeStore struct {
	rows            []domain.NotificationRecord
	listStatus      ListStatus
	listBeforeAt    time.Time
	listBeforeID    string
	listLimit       int
	unreadCount     int64
	unresolvedCount int64

	markRow       domain.NotificationRecord
	markOK        bool
	markAllCount  int64
	markedAll     bool
	markedIDs     []string
	clearedAll    bool
	clearAllCount int64
	err           error
}

type capturePublisher struct {
	events []domain.NotificationEvent
}

func (p *capturePublisher) Publish(_ context.Context, event domain.NotificationEvent) error {
	p.events = append(p.events, event)
	return nil
}

func (f *fakeStore) CreateNotification(context.Context, domain.NotificationRecord) (domain.NotificationRecord, bool, error) {
	return domain.NotificationRecord{}, false, nil
}

func (f *fakeStore) ListNotifications(
	_ context.Context,
	status ListStatus,
	beforeCreatedAt time.Time,
	beforeID string,
	limit int,
) ([]domain.NotificationRecord, error) {
	f.listStatus = status
	f.listBeforeAt = beforeCreatedAt
	f.listBeforeID = beforeID
	f.listLimit = limit
	return f.rows, f.err
}

func (f *fakeStore) CountUnreadNotifications(context.Context) (int64, error) {
	return f.unreadCount, f.err
}

func (f *fakeStore) CountUnresolvedNotifications(context.Context) (int64, error) {
	return f.unresolvedCount, f.err
}

func (f *fakeStore) MarkNotificationRead(_ context.Context, _ string) (domain.NotificationRecord, bool, error) {
	return f.markRow, f.markOK, f.err
}

func (f *fakeStore) MarkAllNotificationsRead(context.Context) (int64, error) {
	f.markedAll = true
	return f.markAllCount, f.err
}

func (f *fakeStore) MarkNotificationsRead(_ context.Context, ids []string) (int64, error) {
	f.markedIDs = ids
	return int64(len(ids)), f.err
}

func (f *fakeStore) ClearAllNotifications(context.Context) (int64, error) {
	f.clearedAll = true
	return f.clearAllCount, f.err
}

func TestListAddsTargetsAndReturnsNextCursor(t *testing.T) {
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	st := &fakeStore{rows: []domain.NotificationRecord{
		{ID: "n3", SessionID: "mer-1", ProjectID: "mer", Type: domain.NotificationNeedsInput, Title: "needs", Status: domain.NotificationUnread, CreatedAt: now},
		{ID: "n2", SessionID: "mer-1", ProjectID: "mer", PRURL: "https://github.com/o/r/pull/1", Type: domain.NotificationReadyToMerge, Title: "ready", Status: domain.NotificationUnread, CreatedAt: now.Add(-time.Minute)},
		{ID: "n1", SessionID: "mer-1", ProjectID: "mer", Type: domain.NotificationNeedsInput, Title: "older", Status: domain.NotificationRead, CreatedAt: now.Add(-2 * time.Minute)},
	}, unreadCount: 2}
	mgr := New(Deps{Store: st})
	got, err := mgr.List(context.Background(), ListFilter{Status: ListAll, Limit: 2})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got.Notifications) != 2 || got.Notifications[0].Target.Kind != TargetSession ||
		got.Notifications[1].Target.Kind != TargetPR || got.Notifications[1].Target.PRURL == "" {
		t.Fatalf("targets = %+v", got)
	}
	if got.UnreadCount != 2 || got.NextCursor == "" {
		t.Fatalf("page = %+v", got)
	}
	cursorAt, cursorID, err := decodeCursor(got.NextCursor)
	if err != nil || !cursorAt.Equal(now.Add(-time.Minute)) || cursorID != "n2" {
		t.Fatalf("cursor at=%s id=%q err=%v", cursorAt, cursorID, err)
	}
	if st.listStatus != ListAll || st.listLimit != 3 || !st.listBeforeAt.IsZero() || st.listBeforeID != "" {
		t.Fatalf("list filter status=%q before=%s/%q limit=%d", st.listStatus, st.listBeforeAt, st.listBeforeID, st.listLimit)
	}
}

func TestListDefaultsToUnreadAndOneHundred(t *testing.T) {
	st := &fakeStore{}
	mgr := New(Deps{Store: st})
	if _, err := mgr.List(context.Background(), ListFilter{}); err != nil {
		t.Fatalf("List: %v", err)
	}
	if st.listStatus != ListUnread || st.listLimit != DefaultListLimit+1 {
		t.Fatalf("list status=%q limit=%d", st.listStatus, st.listLimit)
	}
}

func TestListRejectsInvalidCursor(t *testing.T) {
	_, err := New(Deps{Store: &fakeStore{}}).List(context.Background(), ListFilter{Cursor: "not-a-cursor"})
	var apiErr *apierr.Error
	if !errors.As(err, &apiErr) || apiErr.Code != "INVALID_NOTIFICATION_CURSOR" {
		t.Fatalf("err = %v, want invalid cursor", err)
	}
}

func TestMarkReadAddsTarget(t *testing.T) {
	st := &fakeStore{
		markRow: domain.NotificationRecord{
			ID: "n2", SessionID: "mer-1", ProjectID: "mer", PRURL: "https://github.com/o/r/pull/1",
			Type: domain.NotificationReadyToMerge, Title: "ready", Status: domain.NotificationRead, CreatedAt: time.Now(),
		},
		markOK: true,
	}
	mgr := New(Deps{Store: st})
	got, ok, err := mgr.MarkRead(context.Background(), "n2")
	if err != nil || !ok {
		t.Fatalf("MarkRead ok=%v err=%v", ok, err)
	}
	if got.Status != domain.NotificationRead || got.Target.Kind != TargetPR || got.Target.PRURL == "" {
		t.Fatalf("notification = %+v", got)
	}
}

func TestMarkReadMissingReturnsNotFound(t *testing.T) {
	mgr := New(Deps{Store: &fakeStore{}})
	_, _, err := mgr.MarkRead(context.Background(), "missing")
	var apiErr *apierr.Error
	if !errors.As(err, &apiErr) || apiErr.Kind != apierr.KindNotFound || apiErr.Code != "NOTIFICATION_NOT_FOUND" {
		t.Fatalf("err = %v, want notification not found", err)
	}
}

func TestMarkAllReadReturnsUpdatedCount(t *testing.T) {
	st := &fakeStore{markAllCount: 42}
	mgr := New(Deps{Store: st})
	got, err := mgr.MarkAllRead(context.Background(), nil)
	if err != nil {
		t.Fatalf("MarkAllRead: %v", err)
	}
	if got != 42 || !st.markedAll {
		t.Fatalf("updated count = %d markedAll=%v, want 42 true", got, st.markedAll)
	}
}

// Acknowledging every unread row would strand anything past the client's last
// loaded page, so an explicit id list must scope the write to those rows.
func TestMarkAllReadWithIDsScopesToThoseNotifications(t *testing.T) {
	st := &fakeStore{markAllCount: 99}
	mgr := New(Deps{Store: st})
	got, err := mgr.MarkAllRead(context.Background(), []string{"n1", "n2"})
	if err != nil {
		t.Fatalf("MarkAllRead: %v", err)
	}
	if got != 2 || st.markedAll {
		t.Fatalf("updated count = %d markedAll=%v, want 2 false", got, st.markedAll)
	}
	if len(st.markedIDs) != 2 || st.markedIDs[0] != "n1" {
		t.Fatalf("marked ids = %v", st.markedIDs)
	}
}

func TestListUnreadRequiresStore(t *testing.T) {
	_, err := New(Deps{}).List(context.Background(), ListFilter{})
	if err == nil {
		t.Fatal("want missing store error")
	}
}

func TestClearAllReturnsClearedCount(t *testing.T) {
	st := &fakeStore{clearAllCount: 15}
	mgr := New(Deps{Store: st})
	got, err := mgr.ClearAll(context.Background())
	if err != nil {
		t.Fatalf("ClearAll: %v", err)
	}
	if got != 15 || !st.clearedAll {
		t.Fatalf("cleared count = %d clearedAll=%v, want 15 true", got, st.clearedAll)
	}
}

func TestClearAllPublishesOrderedClearEvent(t *testing.T) {
	st := &fakeStore{clearAllCount: 15}
	publisher := &capturePublisher{}
	mgr := New(Deps{Store: st, Publisher: publisher})

	got, err := mgr.ClearAll(context.Background())
	if err != nil {
		t.Fatalf("ClearAll: %v", err)
	}
	if got != 15 || !st.clearedAll {
		t.Fatalf("cleared count = %d clearedAll=%v, want 15 true", got, st.clearedAll)
	}
	if len(publisher.events) != 1 || publisher.events[0].Kind != domain.NotificationCleared {
		t.Fatalf("published events = %+v, want one clear event", publisher.events)
	}
}

func TestClearAllRequiresStore(t *testing.T) {
	_, err := New(Deps{}).ClearAll(context.Background())
	if err == nil {
		t.Fatal("want missing store error")
	}
}
