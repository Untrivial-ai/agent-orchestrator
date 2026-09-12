package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/go-chi/chi/v5"
)

const (
	maxEventSequence = int64(9007199254740991)
	eventPageLimit   = 100
	eventKeepalive   = 15 * time.Second
)

type sendMessageRequest struct {
	Text string `json:"text"`
}

type clientEventResponse struct {
	SessionID string          `json:"sessionId"`
	Sequence  int64           `json:"sequence"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
	CreatedAt time.Time       `json:"createdAt"`
}

func (s *Server) sendMessage(w http.ResponseWriter, r *http.Request) {
	orgID := chi.URLParam(r, "orgId")
	sessionID := chi.URLParam(r, "sessionId")
	if requireUUID(orgID, "orgId") != nil || requireUUID(sessionID, "sessionId") != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "orgId and sessionId must be UUIDs.")
		return
	}
	key, err := idempotencyKey(r)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	var request sendMessageRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "The request body is invalid.")
		return
	}
	if strings.TrimSpace(request.Text) == "" || len(request.Text) > 65536 {
		writeError(w, r, http.StatusUnprocessableEntity, "validation_error", "Message text must be between 1 and 65536 bytes.")
		return
	}
	event, err := s.store.SendMessage(
		r.Context(),
		principalFrom(r),
		orgID,
		sessionID,
		key,
		request.Text,
	)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"event": toClientEventResponse(event),
	})
}

func (s *Server) cancelTurn(w http.ResponseWriter, r *http.Request) {
	orgID := chi.URLParam(r, "orgId")
	sessionID := chi.URLParam(r, "sessionId")
	turnID := chi.URLParam(r, "turnId")
	if requireUUID(orgID, "orgId") != nil ||
		requireUUID(sessionID, "sessionId") != nil ||
		requireUUID(turnID, "turnId") != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "orgId, sessionId, and turnId must be UUIDs.")
		return
	}
	if err := s.store.RequestTurnCancellation(
		r.Context(),
		principalFrom(r),
		orgID,
		sessionID,
		turnID,
	); err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]bool{"ok": true})
}

func (s *Server) replayClientEvents(w http.ResponseWriter, r *http.Request) {
	orgID := chi.URLParam(r, "orgId")
	sessionID := chi.URLParam(r, "sessionId")
	if requireUUID(orgID, "orgId") != nil || requireUUID(sessionID, "sessionId") != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "orgId and sessionId must be UUIDs.")
		return
	}
	after, err := parseEventSequence(r.URL.Query().Get("after"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	limit, err := parseEventLimit(r)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	events, hasMore, err := s.store.ListClientEvents(
		r.Context(),
		principalFrom(r),
		orgID,
		sessionID,
		after,
		limit,
	)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	responses := make([]clientEventResponse, 0, len(events))
	nextAfter := after
	for _, event := range events {
		responses = append(responses, toClientEventResponse(event))
		nextAfter = event.Sequence
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"events":    responses,
		"hasMore":   hasMore,
		"nextAfter": nextAfter,
	})
}

func (s *Server) streamClientEvents(w http.ResponseWriter, r *http.Request) {
	orgID := chi.URLParam(r, "orgId")
	sessionID := chi.URLParam(r, "sessionId")
	if requireUUID(orgID, "orgId") != nil || requireUUID(sessionID, "sessionId") != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "orgId and sessionId must be UUIDs.")
		return
	}
	afterValue := r.URL.Query().Get("after")
	if afterValue == "" {
		afterValue = r.Header.Get("Last-Event-ID")
	}
	after, err := parseEventSequence(afterValue)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	events, hasMore, err := s.store.ListClientEvents(
		r.Context(),
		principalFrom(r),
		orgID,
		sessionID,
		after,
		eventPageLimit,
	)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprint(w, "retry: 2000\n\n")
	flusher := http.NewResponseController(w)
	if err := flusher.Flush(); err != nil {
		return
	}

	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	lastWrite := time.Now()
	for {
		for _, event := range events {
			if err := writeSSEEvent(w, event); err != nil {
				return
			}
			after = event.Sequence
			lastWrite = time.Now()
		}
		if len(events) > 0 {
			if err := flusher.Flush(); err != nil {
				return
			}
		}
		if hasMore {
			events, hasMore, err = s.store.ListClientEvents(
				r.Context(),
				principalFrom(r),
				orgID,
				sessionID,
				after,
				eventPageLimit,
			)
			if err != nil {
				s.logger.Warn("replay event stream", "error", err, "request_id", requestID(r))
				return
			}
			continue
		}

		select {
		case <-r.Context().Done():
			return
		case <-s.drain:
			return
		case now := <-ticker.C:
			events, hasMore, err = s.store.ListClientEvents(
				r.Context(),
				principalFrom(r),
				orgID,
				sessionID,
				after,
				eventPageLimit,
			)
			if err != nil {
				s.logger.Warn("poll event stream", "error", err, "request_id", requestID(r))
				return
			}
			if len(events) == 0 && now.Sub(lastWrite) >= eventKeepalive {
				if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
					return
				}
				if err := flusher.Flush(); err != nil {
					return
				}
				lastWrite = now
			}
		}
	}
}

func parseEventSequence(value string) (int64, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, nil
	}
	sequence, err := strconv.ParseInt(value, 10, 64)
	if err != nil || sequence < 0 || sequence > maxEventSequence {
		return 0, errors.New("after must be between 0 and 9007199254740991")
	}
	return sequence, nil
}

func parseEventLimit(r *http.Request) (int, error) {
	value := strings.TrimSpace(r.URL.Query().Get("limit"))
	if value == "" {
		return eventPageLimit, nil
	}
	limit, err := strconv.Atoi(value)
	if err != nil || limit < 1 || limit > 500 {
		return 0, errors.New("limit must be between 1 and 500")
	}
	return limit, nil
}

func toClientEventResponse(event domain.ClientEvent) clientEventResponse {
	return clientEventResponse{
		SessionID: event.SessionID,
		Sequence:  event.Sequence,
		Type:      event.Type,
		Payload:   event.Payload,
		CreatedAt: event.CreatedAt,
	}
}

func writeSSEEvent(w http.ResponseWriter, event domain.ClientEvent) error {
	payload, err := json.Marshal(toClientEventResponse(event))
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(
		w,
		"id: %d\nevent: %s\ndata: %s\n\n",
		event.Sequence,
		event.Type,
		payload,
	)
	return err
}
