package httpapi

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/aoagents/agent-orchestrator/cloud/internal/postgres"
	"github.com/google/uuid"
)

const maxRequestBody = 1 << 20

type errorEnvelope struct {
	Error     string         `json:"error"`
	Code      string         `json:"code"`
	Message   string         `json:"message"`
	RequestID string         `json:"requestId"`
	Details   map[string]any `json:"details,omitempty"`
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target any) error {
	return decodeJSONLimit(w, r, target, maxRequestBody)
}

func decodeJSONLimit(w http.ResponseWriter, r *http.Request, target any, limit int64) error {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("request body must contain one JSON object")
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	writeJSON(w, status, errorEnvelope{
		Error:     http.StatusText(status),
		Code:      code,
		Message:   message,
		RequestID: requestID(r),
	})
}

func (s *Server) writeStoreError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, postgres.ErrForbidden):
		writeError(w, r, http.StatusForbidden, "forbidden", "You do not have access to this organization.")
	case errors.Is(err, postgres.ErrNotFound):
		writeError(w, r, http.StatusNotFound, "not_found", "The requested resource was not found.")
	case errors.Is(err, postgres.ErrConflict):
		writeError(w, r, http.StatusConflict, "conflict", "The resource conflicts with an existing record.")
	case errors.Is(err, postgres.ErrTurnFinished):
		writeError(w, r, http.StatusConflict, "TURN_FINISHED", "The turn is already finished.")
	case errors.Is(err, postgres.ErrStaleTurn):
		writeError(w, r, http.StatusConflict, "STALE_TURN", "The turn is owned by another worker attempt.")
	case errors.Is(err, postgres.ErrInvalid):
		writeError(w, r, http.StatusUnprocessableEntity, "validation_error", "The request violates a resource constraint.")
	case errors.Is(err, postgres.ErrIdempotencyMismatch):
		writeError(w, r, http.StatusConflict, "IDEMPOTENCY_CONFLICT", "The idempotency key was already used for another request.")
	case errors.Is(err, postgres.ErrSandboxQuotaExceeded):
		writeError(
			w, r, http.StatusConflict, "SANDBOX_QUOTA_EXCEEDED",
			"This organization has reached its limit of concurrent sessions. Delete a session and try again.",
		)
	default:
		s.logger.Error("handle API request", "error", err, "request_id", requestID(r))
		writeError(w, r, http.StatusInternalServerError, "internal_error", "The request could not be completed.")
	}
}

func parseLimit(r *http.Request) (int, error) {
	value := strings.TrimSpace(r.URL.Query().Get("limit"))
	if value == "" {
		return 50, nil
	}
	limit, err := strconv.Atoi(value)
	if err != nil || limit < 1 || limit > 100 {
		return 0, errors.New("limit must be between 1 and 100")
	}
	return limit, nil
}

func parseCursor(value string) (*domain.Cursor, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return nil, errors.New("invalid cursor")
	}
	var encoded struct {
		Time time.Time `json:"time"`
		ID   string    `json:"id"`
	}
	if err := json.Unmarshal(raw, &encoded); err != nil {
		return nil, errors.New("invalid cursor")
	}
	if encoded.Time.IsZero() || uuid.Validate(encoded.ID) != nil {
		return nil, errors.New("invalid cursor")
	}
	return &domain.Cursor{Time: encoded.Time, ID: encoded.ID}, nil
}

func encodeCursor(value time.Time, id string) string {
	raw, _ := json.Marshal(struct {
		Time time.Time `json:"time"`
		ID   string    `json:"id"`
	}{Time: value, ID: id})
	return base64.RawURLEncoding.EncodeToString(raw)
}

func requireUUID(value, field string) error {
	if uuid.Validate(value) != nil {
		return fmt.Errorf("%s must be a UUID", field)
	}
	return nil
}

func idempotencyKey(r *http.Request) (string, error) {
	value := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if value == "" || len(value) > 200 {
		return "", errors.New("Idempotency-Key must be between 1 and 200 characters")
	}
	return value, nil
}
