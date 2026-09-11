package notification

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

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
	store      Store
	publisher  Publisher
	barrier    sync.Locker
	newClearID func() string
}

// Publisher sends notification changes to live dashboard subscribers.
type Publisher interface {
	Publish(ctx context.Context, event domain.NotificationEvent) error
}

// ClearResult describes one completed notification-history clear.
type ClearResult struct {
	ClearedCount int64
	ClearID      string
}

// Deps configures a Manager.
type Deps struct {
	Store      Store
	Publisher  Publisher
	Barrier    sync.Locker
	NewClearID func() string
}

// New constructs a notification Manager.
func New(d Deps) *Manager {
	m := &Manager{store: d.Store, publisher: d.Publisher, barrier: d.Barrier, newClearID: d.NewClearID}
	if m.barrier == nil {
		m.barrier = &sync.Mutex{}
	}
	if m.newClearID == nil {
		m.newClearID = func() string { return "ntf_clear_" + uuid.NewString() }
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

// ClearAll deletes notification history and publishes the same clear id that
// the HTTP response returns. Clients use that id to apply the reset once even
// when the stream event arrives before the mutation response.
func (m *Manager) ClearAll(ctx context.Context) (ClearResult, error) {
	if m == nil || m.store == nil {
		return ClearResult{}, errors.New("notification: store is required")
	}
	m.barrier.Lock()
	defer m.barrier.Unlock()
	cleared, err := m.store.ClearAllNotifications(ctx)
	if err != nil {
		return ClearResult{}, err
	}
	result := ClearResult{ClearedCount: cleared, ClearID: m.newClearID()}
	if m.publisher != nil {
		if err := m.publisher.Publish(ctx, domain.NotificationEvent{Kind: domain.NotificationCleared, ClearID: result.ClearID}); err != nil {
			return ClearResult{}, fmt.Errorf("notification: publish clear-all: %w", err)
		}
	}
	return result, nil
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
