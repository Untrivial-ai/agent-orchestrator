package controllers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apispec"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/envelope"
	notificationsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/notification"
)

// NotificationService is the controller-facing notification service contract.
type NotificationService interface {
	List(ctx context.Context, filter notificationsvc.ListFilter) (notificationsvc.ListPage, error)
	MarkRead(ctx context.Context, id string) (notificationsvc.Notification, bool, error)
	MarkAllRead(ctx context.Context, ids []string) (int64, error)
	Delete(ctx context.Context, id string) (notificationsvc.Notification, error)
	ClearAll(ctx context.Context) (notificationsvc.ClearResult, error)
}

// NotificationStream is the live notification stream used by SSE clients.
type NotificationStream interface {
	Subscribe(projectID domain.ProjectID) (<-chan domain.NotificationEvent, func())
}

// NotificationsController owns the /notifications routes.
type NotificationsController struct {
	Svc    NotificationService
	Stream NotificationStream
}

// Register mounts bounded notification REST routes on the supplied router.
func (c *NotificationsController) Register(r chi.Router) {
	r.Get("/notifications", c.list)
	r.Post("/notifications/read-all", c.markAllRead)
	r.Patch("/notifications/{id}", c.markRead)
	r.Delete("/notifications/{id}", c.delete)
	r.Delete("/notifications", c.clearAll)
}

// RegisterStream mounts long-lived notification stream routes on the supplied router.
func (c *NotificationsController) RegisterStream(r chi.Router) {
	r.Get("/notifications/stream", c.stream)
}

func (c *NotificationsController) list(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "GET", "/api/v1/notifications")
		return
	}
	filter, err := parseNotificationListFilter(r)
	if err != nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_QUERY", err.Error(), nil)
		return
	}
	page, err := c.Svc.List(r.Context(), filter)
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, ListNotificationsResponse{
		Notifications:   notificationResponses(page.Notifications),
		NextCursor:      page.NextCursor,
		UnreadCount:     page.UnreadCount,
		UnresolvedCount: page.UnresolvedCount,
	})
}

func (c *NotificationsController) markRead(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "PATCH", "/api/v1/notifications/{id}")
		return
	}
	var req MarkNotificationReadRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_JSON", "Invalid JSON body", nil)
		return
	}
	if req.Status != string(domain.NotificationRead) {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_NOTIFICATION_STATUS", "Notification status must be read", nil)
		return
	}
	notification, _, err := c.Svc.MarkRead(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, NotificationEnvelope{Notification: notificationResponse(notification)})
}

func (c *NotificationsController) markAllRead(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "POST", "/api/v1/notifications/read-all")
		return
	}
	// The body is optional: older clients POST nothing and mean "everything".
	var req MarkAllNotificationsReadRequest
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
			envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_JSON", "Invalid JSON body", nil)
			return
		}
	}
	updatedCount, err := c.Svc.MarkAllRead(r.Context(), req.IDs)
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, MarkAllNotificationsReadResponse{
		Notifications: []NotificationResponse{},
		UpdatedCount:  updatedCount,
	})
}

func (c *NotificationsController) delete(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "DELETE", "/api/v1/notifications/{id}")
		return
	}
	notification, err := c.Svc.Delete(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, NotificationEnvelope{Notification: notificationResponse(notification)})
}

func (c *NotificationsController) clearAll(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "DELETE", "/api/v1/notifications")
		return
	}
	result, err := c.Svc.ClearAll(r.Context())
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, ClearNotificationsResponse{
		ClearedCount:  result.ClearedCount,
		ClearID:       result.ClearID,
		ClearEpoch:    result.ClearEpoch,
		ClearSequence: result.ClearSequence,
	})
}

func (c *NotificationsController) stream(w http.ResponseWriter, r *http.Request) {
	if c.Stream == nil {
		apispec.NotImplemented(w, r, "GET", "/api/v1/notifications/stream")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		envelope.WriteAPIError(w, r, http.StatusInternalServerError, "internal", "SSE_UNSUPPORTED", "Streaming is not supported by this server", nil)
		return
	}
	ch, unsubscribe := c.Stream.Subscribe(domain.ProjectID(r.URL.Query().Get("projectId")))
	defer unsubscribe()

	h := w.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	for {
		select {
		case <-r.Context().Done():
			return
		case event, ok := <-ch:
			if !ok {
				return
			}
			if err := writeNotificationSSE(w, flusher, event); err != nil {
				return
			}
		}
	}
}

func writeNotificationSSE(w http.ResponseWriter, flusher http.Flusher, event domain.NotificationEvent) error {
	name := "notification_created"
	var payload any = notificationResponseFromRecord(event.Record)
	switch event.Kind {
	case domain.NotificationCleared:
		name = "notification_cleared"
		payload = struct {
			ClearID       string `json:"clearId"`
			ClearEpoch    string `json:"clearEpoch"`
			ClearSequence int64  `json:"clearSequence"`
		}{ClearID: event.ClearID, ClearEpoch: event.ClearEpoch, ClearSequence: event.ClearSequence}
	case domain.NotificationResolved:
		name = "notification_resolved"
	case domain.NotificationDeleted:
		name = "notification_deleted"
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, data); err != nil {
		return err
	}
	flusher.Flush()
	return nil
}

func parseNotificationListFilter(r *http.Request) (notificationsvc.ListFilter, error) {
	q := r.URL.Query()
	status := notificationsvc.ListStatus(q.Get("status"))
	if status == "" {
		status = notificationsvc.ListUnread
	}
	if !status.Valid() {
		return notificationsvc.ListFilter{}, errNotificationStatusUnsupported
	}
	limit := notificationsvc.DefaultListLimit
	if raw := q.Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			return notificationsvc.ListFilter{}, errNotificationLimitInvalid
		}
		limit = parsed
	}
	if limit > notificationsvc.MaxListLimit {
		limit = notificationsvc.MaxListLimit
	}
	return notificationsvc.ListFilter{Status: status, Limit: limit, Cursor: q.Get("cursor")}, nil
}

var (
	errNotificationStatusUnsupported = notificationQueryError("status must be unread, unresolved, or all")
	errNotificationLimitInvalid      = notificationQueryError("limit must be a positive integer")
)

type notificationQueryError string

func (e notificationQueryError) Error() string { return string(e) }

func notificationResponses(in []notificationsvc.Notification) []NotificationResponse {
	out := make([]NotificationResponse, 0, len(in))
	for _, n := range in {
		out = append(out, notificationResponse(n))
	}
	return out
}

func notificationResponse(n notificationsvc.Notification) NotificationResponse {
	return NotificationResponse{
		ID:         n.ID,
		SessionID:  string(n.SessionID),
		ProjectID:  string(n.ProjectID),
		PRURL:      n.PRURL,
		Type:       string(n.Type),
		Title:      n.Title,
		Body:       n.Body,
		Status:     string(n.Status),
		CreatedAt:  n.CreatedAt,
		ResolvedAt: optionalTime(n.ResolvedAt),
		Target: NotificationTarget{
			Kind:      string(n.Target.Kind),
			SessionID: string(n.Target.SessionID),
			PRURL:     n.Target.PRURL,
		},
	}
}

func notificationResponseFromRecord(rec domain.NotificationRecord) NotificationResponse {
	return NotificationResponse{
		ID:         rec.ID,
		SessionID:  string(rec.SessionID),
		ProjectID:  string(rec.ProjectID),
		PRURL:      rec.PRURL,
		Type:       string(rec.Type),
		Title:      rec.Title,
		Body:       rec.Body,
		Status:     string(rec.Status),
		CreatedAt:  rec.CreatedAt,
		ResolvedAt: optionalTime(rec.ResolvedAt),
		Target:     notificationTargetFromRecord(rec),
	}
}

func notificationTargetFromRecord(rec domain.NotificationRecord) NotificationTarget {
	if rec.PRURL != "" {
		return NotificationTarget{Kind: "pr", SessionID: string(rec.SessionID), PRURL: rec.PRURL}
	}
	return NotificationTarget{Kind: "session", SessionID: string(rec.SessionID)}
}
