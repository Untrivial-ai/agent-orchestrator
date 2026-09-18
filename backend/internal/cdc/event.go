// Package cdc is the change-data-capture delivery layer. Change events are
// captured durably by SQLite triggers into the change_log table (see the storage
// migrations); this package POLLS that log and fans new events out, in order, to
// in-process subscribers such as terminal session-state fan-out. Future SSE/event
// endpoints can subscribe here too.
//
// The change_log table is the durable, ordered source of truth while retained;
// a bounded janitor removes history outside the replay window. Clients catch up
// by reading it from their own offset (SSE Last-Event-ID), and the poller +
// broadcaster here are only the LIVE push on top of that.
package cdc

import (
	"encoding/json"
	"time"
)

// EventType mirrors the event_type values the DB triggers write.
type EventType string

// Event types, one per row-change the DB triggers emit into change_log.
const (
	EventSessionCreated         EventType = "session_created"
	EventSessionUpdated         EventType = "session_updated"
	EventPRCreated              EventType = "pr_created"
	EventPRUpdated              EventType = "pr_updated"
	EventPRCheckRecorded        EventType = "pr_check_recorded"
	EventPRSessionChanged       EventType = "pr_session_changed"
	EventPRReviewThreadAdded    EventType = "pr_review_thread_added"
	EventPRReviewThreadResolved EventType = "pr_review_thread_resolved"
	EventReviewRunCreated       EventType = "review_run_created"
	EventReviewRunUpdated       EventType = "review_run_updated"
)

// Event is one CDC change read from change_log. Seq is the monotonic ordering +
// idempotency key (consumers dedup by it). SessionID is empty for project-level
// events. Payload is the trigger-built JSON, kept raw so a typed transport can
// narrow it by Type (the discriminated-union decode lives at the transport edge,
// not here).
type Event struct {
	Seq       int64           `json:"seq"`
	ProjectID string          `json:"projectId"`
	SessionID string          `json:"sessionId,omitempty"`
	Type      EventType       `json:"type"`
	Payload   json.RawMessage `json:"payload"`
	CreatedAt time.Time       `json:"createdAt"`
}
