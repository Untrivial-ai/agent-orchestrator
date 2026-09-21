package domain

import (
	"encoding/json"
	"time"
)

type NotificationType string

const (
	NotificationTypeNeedsInput     NotificationType = "needs_input"
	NotificationTypeAgentFailed    NotificationType = "agent_failed"
	NotificationTypeAgentCompleted NotificationType = "agent_completed"
)

func (t NotificationType) Valid() bool {
	switch t {
	case NotificationTypeNeedsInput, NotificationTypeAgentFailed, NotificationTypeAgentCompleted:
		return true
	default:
		return false
	}
}

type NotificationStatus string

const (
	NotificationStatusUnread NotificationStatus = "unread"
	NotificationStatusRead   NotificationStatus = "read"
)

func (s NotificationStatus) Valid() bool {
	return s == NotificationStatusUnread || s == NotificationStatusRead
}

type NotificationEventKind string

const (
	NotificationEventCreated  NotificationEventKind = "notification_created"
	NotificationEventUpdated  NotificationEventKind = "notification_updated"
	NotificationEventResolved NotificationEventKind = "notification_resolved"
)

type AgentNotificationEvent struct {
	EventID    string           `json:"eventId"`
	Type       NotificationType `json:"type"`
	OccurredAt time.Time        `json:"occurredAt"`
	Payload    json.RawMessage  `json:"payload"`
}

type NotificationIngress struct {
	ID              string
	OrgID           string
	ProjectID       string
	SessionID       string
	RecipientUserID string
	WorkerID        string
	WorkerEpoch     int64
	Event           AgentNotificationEvent
	AttemptCount    int
	LeaseOwner      string
	LeaseUntil      *time.Time
	CreatedAt       time.Time
}

type NotificationAcceptance struct {
	IngressID string
	EventID   string
	Duplicate bool
}

type Notification struct {
	ID              string          `json:"id"`
	OrgID           string          `json:"orgId"`
	RecipientUserID string          `json:"recipientUserId"`
	ProjectID       string          `json:"projectId,omitempty"`
	SessionID       string          `json:"sessionId,omitempty"`
	Source          string          `json:"source"`
	Type            string          `json:"type"`
	Title           string          `json:"title"`
	Body            string          `json:"body"`
	DedupeKey       string          `json:"-"`
	Status          string          `json:"status"`
	EventID         string          `json:"eventId,omitempty"`
	Metadata        json.RawMessage `json:"metadata"`
	ResolvedAt      *time.Time      `json:"resolvedAt,omitempty"`
	CreatedAt       time.Time       `json:"createdAt"`
	UpdatedAt       time.Time       `json:"updatedAt"`
}

type NotificationEvent struct {
	Sequence        int64                 `json:"sequence"`
	OrgID           string                `json:"orgId"`
	RecipientUserID string                `json:"recipientUserId"`
	Kind            NotificationEventKind `json:"kind"`
	EventID         string                `json:"eventId"`
	Notification    Notification          `json:"notification"`
	CreatedAt       time.Time             `json:"createdAt"`
}

type NotificationCursor struct {
	CreatedAt time.Time
	ID        string
}

type NotificationFilter struct {
	Status NotificationStatus
	Cursor *NotificationCursor
	Limit  int
}

type NotificationPage struct {
	Items          []Notification
	HasMore        bool
	NextCursor     *NotificationCursor
	UnreadCount    int
	LatestSequence int64
}
