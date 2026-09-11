package notification

import (
	"context"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// Store is the notification service's read persistence surface.
type Store interface {
	ListNotifications(
		ctx context.Context,
		status ListStatus,
		beforeCreatedAt time.Time,
		beforeID string,
		limit int,
	) ([]domain.NotificationRecord, error)
	CountUnreadNotifications(ctx context.Context) (int64, error)
	CountUnresolvedNotifications(ctx context.Context) (int64, error)
	MarkNotificationRead(ctx context.Context, id string) (domain.NotificationRecord, bool, error)
	MarkAllNotificationsRead(ctx context.Context) (int64, error)
	MarkNotificationsRead(ctx context.Context, ids []string) (int64, error)
	ClearAllNotifications(ctx context.Context) (int64, error)
}
