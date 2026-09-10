package notification

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
)

const (
	// DefaultListLimit keeps the first dashboard history page bounded.
	DefaultListLimit = 100
	// MaxListLimit keeps every notification history page bounded.
	MaxListLimit = 100
)

// Manager reads stored notifications for REST controllers.
type Manager struct {
	store   Store
	barrier sync.Locker
}

// Deps configures a Manager.
type Deps struct {
	Store Store
	// Barrier serializes clear-all with notification persistence and publication.
	Barrier sync.Locker
}

// New constructs a read-only notification Manager.
func New(d Deps) *Manager {
	m := &Manager{store: d.Store, barrier: d.Barrier}
	if m.barrier == nil {
		m.barrier = &sync.Mutex{}
	}
	return m
}

// List returns one stable newest-first page of notification history.
func (m *Manager) List(ctx context.Context, filter ListFilter) (ListPage, error) {
	if m == nil || m.store == nil {
		return ListPage{}, errors.New("notification: store is required")
	}
	if filter.Status == "" {
		filter.Status = ListUnread
	}
	if !filter.Status.Valid() {
		return ListPage{}, apierr.Invalid(
			"INVALID_NOTIFICATION_STATUS",
			"Notification status must be unread, unresolved, or all",
			nil,
		)
	}
	limit := normalizeLimit(filter.Limit)
	beforeCreatedAt, beforeID, err := decodeCursor(filter.Cursor)
	if err != nil {
		return ListPage{}, err
	}
	rows, err := m.store.ListNotifications(ctx, filter.Status, beforeCreatedAt, beforeID, limit+1)
	if err != nil {
		return ListPage{}, err
	}
	unreadCount, err := m.store.CountUnreadNotifications(ctx)
	if err != nil {
		return ListPage{}, err
	}
	unresolvedCount, err := m.store.CountUnresolvedNotifications(ctx)
	if err != nil {
		return ListPage{}, err
	}
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	out := make([]Notification, 0, len(rows))
	for _, row := range rows {
		out = append(out, notificationFromRecord(row))
	}
	page := ListPage{Notifications: out, UnreadCount: int(unreadCount), UnresolvedCount: int(unresolvedCount)}
	if hasMore {
		page.NextCursor = encodeCursor(rows[len(rows)-1])
	}
	return page, nil
}

// MarkRead marks one unread notification read.
func (m *Manager) MarkRead(ctx context.Context, id string) (Notification, bool, error) {
	if m == nil || m.store == nil {
		return Notification{}, false, errors.New("notification: store is required")
	}
	if id == "" {
		return Notification{}, false, apierr.Invalid("INVALID_NOTIFICATION_ID", "Notification id is required", nil)
	}
	row, ok, err := m.store.MarkNotificationRead(ctx, id)
	if err != nil {
		return Notification{}, false, err
	}
	if !ok {
		return Notification{}, false, apierr.NotFound("NOTIFICATION_NOT_FOUND", "Unknown unread notification")
	}
	return notificationFromRecord(row), true, nil
}

// MarkAllRead acknowledges notifications as seen. With ids it acknowledges
// exactly those rows — the ones a client actually rendered — which keeps
// anything past the client's last loaded page unread and therefore still
// reachable. With no ids it falls back to acknowledging every unread row, for
// clients that do not paginate.
func (m *Manager) MarkAllRead(ctx context.Context, ids []string) (int64, error) {
	if m == nil || m.store == nil {
		return 0, errors.New("notification: store is required")
	}
	if len(ids) == 0 {
		return m.store.MarkAllNotificationsRead(ctx)
	}
	return m.store.MarkNotificationsRead(ctx, ids)
}

// ClearAll deletes every notification.
func (m *Manager) ClearAll(ctx context.Context) (int64, error) {
	if m == nil || m.store == nil {
		return 0, errors.New("notification: store is required")
	}
	m.barrier.Lock()
	defer m.barrier.Unlock()
	return m.store.ClearAllNotifications(ctx)
}

func normalizeLimit(limit int) int {
	if limit <= 0 {
		return DefaultListLimit
	}
	if limit > MaxListLimit {
		return MaxListLimit
	}
	return limit
}

func encodeCursor(rec domain.NotificationRecord) string {
	value := rec.CreatedAt.UTC().Format(time.RFC3339Nano) + "\n" + rec.ID
	return base64.RawURLEncoding.EncodeToString([]byte(value))
}

func decodeCursor(raw string) (time.Time, string, error) {
	if raw == "" {
		return time.Time{}, "", nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return time.Time{}, "", invalidCursor()
	}
	createdAtRaw, id, ok := strings.Cut(string(decoded), "\n")
	if !ok || id == "" {
		return time.Time{}, "", invalidCursor()
	}
	createdAt, err := time.Parse(time.RFC3339Nano, createdAtRaw)
	if err != nil {
		return time.Time{}, "", invalidCursor()
	}
	return createdAt.UTC(), id, nil
}

func invalidCursor() error {
	return apierr.Invalid("INVALID_NOTIFICATION_CURSOR", "Notification cursor is invalid", nil)
}

func notificationFromRecord(rec domain.NotificationRecord) Notification {
	return Notification{NotificationRecord: rec, Target: targetForRecord(rec)}
}

func targetForRecord(rec domain.NotificationRecord) Target {
	if rec.PRURL != "" {
		return Target{Kind: TargetPR, SessionID: rec.SessionID, PRURL: rec.PRURL}
	}
	return Target{Kind: TargetSession, SessionID: rec.SessionID}
}
