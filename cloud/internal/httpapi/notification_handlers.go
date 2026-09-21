package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/aoagents/agent-orchestrator/cloud/internal/postgres"
	"github.com/aoagents/agent-orchestrator/cloud/internal/worker"
	"github.com/go-chi/chi/v5"
)

const (
	maxNotificationEventIDBytes = 128
	maxNotificationPayloadBytes = 8 << 10
	maxNotificationRequestBytes = maxNotificationPayloadBytes + 1024
)

type notificationAcceptanceResponse struct {
	Accepted  bool   `json:"accepted"`
	EventID   string `json:"eventId"`
	Duplicate bool   `json:"duplicate"`
}

type notificationListResponse struct {
	Items          []domain.Notification `json:"items"`
	Page           pageInfo              `json:"page"`
	UnreadCount    int                   `json:"unreadCount"`
	LatestSequence int64                 `json:"latestSequence"`
}

type notificationEventsResponse struct {
	Items   []domain.NotificationEvent `json:"items"`
	HasMore bool                       `json:"hasMore"`
}

func (s *Server) workerNotificationEvent(w http.ResponseWriter, r *http.Request) {
	claims := workerFrom(r)
	if !worker.HasScope(claims, "worker:notification") {
		writeError(w, r, http.StatusForbidden, "SCOPE_REQUIRED", "The worker:notification scope is required.")
		return
	}
	var event domain.AgentNotificationEvent
	if err := decodeJSONLimit(w, r, &event, maxNotificationRequestBytes); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "The notification event is invalid.")
		return
	}
	event.EventID = strings.TrimSpace(event.EventID)
	if event.EventID == "" || len(event.EventID) > maxNotificationEventIDBytes || !event.Type.Valid() ||
		event.OccurredAt.IsZero() || event.OccurredAt.After(time.Now().Add(5*time.Minute)) ||
		!validNotificationPayload(event.Payload) {
		writeError(w, r, http.StatusUnprocessableEntity, "validation_error", "The notification event violates a resource constraint.")
		return
	}
	accepted, err := s.store.AcceptNotificationEvent(
		r.Context(), claims.OrgID, claims.SessionID, claims.WorkerID, claims.Epoch, event,
	)
	if err != nil {
		if errors.Is(err, postgres.ErrStaleWorker) {
			writeError(w, r, http.StatusUnauthorized, "STALE_WORKER_TOKEN", "The worker credential has been replaced.")
			return
		}
		s.writeStoreError(w, r, err)
		return
	}
	if s.notificationWake != nil {
		s.notificationWake()
	}
	status := http.StatusAccepted
	if accepted.Duplicate {
		status = http.StatusOK
	}
	writeJSON(w, status, notificationAcceptanceResponse{
		Accepted: true, EventID: event.EventID, Duplicate: accepted.Duplicate,
	})
}

func validNotificationPayload(payload json.RawMessage) bool {
	if len(payload) == 0 || len(payload) > maxNotificationPayloadBytes {
		return false
	}
	var object map[string]json.RawMessage
	return json.Unmarshal(payload, &object) == nil && object != nil
}

func (s *Server) listNotifications(w http.ResponseWriter, r *http.Request) {
	orgID := chi.URLParam(r, "orgId")
	limit, err := parseLimit(r)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	statusValue := strings.TrimSpace(r.URL.Query().Get("status"))
	var status domain.NotificationStatus
	switch statusValue {
	case "", "all":
	case string(domain.NotificationStatusUnread):
		status = domain.NotificationStatusUnread
	case string(domain.NotificationStatusRead):
		status = domain.NotificationStatusRead
	default:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "status must be unread, read, or all")
		return
	}
	cursor, err := parseCursor(r.URL.Query().Get("cursor"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	filter := domain.NotificationFilter{Status: status, Limit: limit}
	if cursor != nil {
		filter.Cursor = &domain.NotificationCursor{CreatedAt: cursor.Time, ID: cursor.ID}
	}
	page, err := s.store.ListNotifications(r.Context(), principalFrom(r), orgID, filter)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	nextCursor := ""
	if page.NextCursor != nil {
		nextCursor = encodeCursor(page.NextCursor.CreatedAt, page.NextCursor.ID)
	}
	writeJSON(w, http.StatusOK, notificationListResponse{
		Items: page.Items, Page: pageInfo{HasMore: page.HasMore, NextCursor: nextCursor},
		UnreadCount: page.UnreadCount, LatestSequence: page.LatestSequence,
	})
}

func (s *Server) listNotificationEvents(w http.ResponseWriter, r *http.Request) {
	after, ok := parseNotificationSequence(w, r)
	if !ok {
		return
	}
	events, hasMore, err := s.store.ListNotificationEvents(
		r.Context(), principalFrom(r), chi.URLParam(r, "orgId"), after, 100,
	)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, notificationEventsResponse{Items: events, HasMore: hasMore})
}

func (s *Server) markNotificationRead(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Status domain.NotificationStatus `json:"status"`
	}
	if err := decodeJSON(w, r, &request); err != nil || request.Status != domain.NotificationStatusRead {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "status must be read")
		return
	}
	id := chi.URLParam(r, "notificationId")
	if requireUUID(id, "notificationId") != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "notificationId must be a UUID")
		return
	}
	changed, err := s.store.MarkNotificationsRead(r.Context(), principalFrom(r), chi.URLParam(r, "orgId"), []string{id})
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	if changed == 0 {
		writeError(w, r, http.StatusNotFound, "not_found", "The notification was not found.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"updated": changed})
}

func (s *Server) markAllNotificationsRead(w http.ResponseWriter, r *http.Request) {
	changed, err := s.store.MarkNotificationsRead(r.Context(), principalFrom(r), chi.URLParam(r, "orgId"), nil)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"updated": changed})
}
