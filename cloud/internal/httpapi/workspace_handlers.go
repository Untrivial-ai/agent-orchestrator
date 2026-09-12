package httpapi

import (
	"context"
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
	maxWorkspacePath = 4096
	maxWorkspaceFile = 1 << 20
)

func (s *Server) listWorkspaceFiles(w http.ResponseWriter, r *http.Request) {
	orgID, sessionID, ok := workspaceRoute(w, r)
	if !ok {
		return
	}
	limit, err := parseLimit(r)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	path := r.URL.Query().Get("path")
	cursor := r.URL.Query().Get("cursor")
	if len(path) > maxWorkspacePath || len(cursor) > 1024 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "The workspace path or cursor is too long.")
		return
	}
	payload, _ := json.Marshal(worker.WorkspaceListRequest{
		Path: path, Cursor: cursor, Limit: limit,
	})
	result, ok := s.runWorkspaceRequest(w, r, orgID, sessionID, "workspace.list", payload)
	if !ok {
		return
	}
	var page worker.WorkspaceEntryPage
	if json.Unmarshal(result, &page) != nil {
		writeError(w, r, http.StatusBadGateway, "INVALID_WORKER_RESPONSE", "The worker returned an invalid file listing.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"path":  page.Path,
		"items": page.Items,
		"page": map[string]any{
			"hasMore":    page.HasMore,
			"nextCursor": page.NextCursor,
		},
	})
}

func (s *Server) readWorkspaceFile(w http.ResponseWriter, r *http.Request) {
	orgID, sessionID, ok := workspaceRoute(w, r)
	if !ok {
		return
	}
	path := r.URL.Query().Get("path")
	if strings.TrimSpace(path) == "" || len(path) > maxWorkspacePath {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "A valid workspace-relative path is required.")
		return
	}
	payload, _ := json.Marshal(worker.WorkspaceReadRequest{Path: path})
	result, ok := s.runWorkspaceRequest(w, r, orgID, sessionID, "workspace.read", payload)
	if !ok {
		return
	}
	var file worker.WorkspaceFile
	if json.Unmarshal(result, &file) != nil {
		writeError(w, r, http.StatusBadGateway, "INVALID_WORKER_RESPONSE", "The worker returned an invalid workspace file.")
		return
	}
	writeJSON(w, http.StatusOK, file)
}

func (s *Server) writeWorkspaceFile(w http.ResponseWriter, r *http.Request) {
	orgID, sessionID, ok := workspaceRoute(w, r)
	if !ok {
		return
	}
	var input worker.WorkspaceWriteRequest
	if err := decodeJSONLimit(w, r, &input, maxWorkspaceFile+maxWorkspacePath+1024); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if strings.TrimSpace(input.Path) == "" ||
		len(input.Path) > maxWorkspacePath ||
		len(input.Content) > maxWorkspaceFile {
		writeError(w, r, http.StatusUnprocessableEntity, "validation_error", "The workspace file is invalid or too large.")
		return
	}
	payload, _ := json.Marshal(input)
	result, ok := s.runWorkspaceRequest(w, r, orgID, sessionID, "workspace.write", payload)
	if !ok {
		return
	}
	var file worker.WorkspaceFile
	if json.Unmarshal(result, &file) != nil {
		writeError(w, r, http.StatusBadGateway, "INVALID_WORKER_RESPONSE", "The worker returned an invalid workspace file.")
		return
	}
	// A browser-originated write is already durable in the worker workspace.
	// Emit a small invalidation event so every view of this session refreshes
	// from the same event stream instead of waiting for a polling interval.
	s.appendSessionProjectionEvent(r.Context(), orgID, sessionID, "workspace.changed", map[string]string{
		"path": file.Path,
	})
	writeJSON(w, http.StatusOK, file)
}

func (s *Server) getWorkspaceDiff(w http.ResponseWriter, r *http.Request) {
	orgID, sessionID, ok := workspaceRoute(w, r)
	if !ok {
		return
	}
	result, ok := s.runWorkspaceRequest(
		w, r, orgID, sessionID, "workspace.diff", json.RawMessage(`{}`),
	)
	if !ok {
		return
	}
	var value map[string]any
	if json.Unmarshal(result, &value) != nil {
		writeError(w, r, http.StatusBadGateway, "INVALID_WORKER_RESPONSE", "The worker returned an invalid workspace diff.")
		return
	}
	writeJSON(w, http.StatusOK, value)
}

func workspaceRoute(w http.ResponseWriter, r *http.Request) (string, string, bool) {
	orgID := chi.URLParam(r, "orgId")
	sessionID := chi.URLParam(r, "sessionId")
	if requireUUID(orgID, "orgId") != nil || requireUUID(sessionID, "sessionId") != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "orgId and sessionId must be UUIDs.")
		return "", "", false
	}
	return orgID, sessionID, true
}

func (s *Server) runWorkspaceRequest(
	w http.ResponseWriter,
	r *http.Request,
	orgID, sessionID, kind string,
	payload json.RawMessage,
) (json.RawMessage, bool) {
	principal := principalFrom(r)
	request, err := s.store.CreateWorkspaceRequest(
		r.Context(), principal, orgID, sessionID, kind, payload, s.workerRequestTimeout,
	)
	if err != nil {
		s.writeWorkspaceStoreError(w, r, err)
		return nil, false
	}

	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	timeout := time.NewTimer(s.workerRequestTimeout)
	defer timeout.Stop()
	for {
		select {
		case <-r.Context().Done():
			s.cancelWorkspaceRequest(r, principal, orgID, sessionID, request.ID)
			return nil, false
		case <-timeout.C:
			s.cancelWorkspaceRequest(r, principal, orgID, sessionID, request.ID)
			writeError(w, r, http.StatusGatewayTimeout, "WORKER_TIMEOUT", "The worker did not complete the request in time.")
			return nil, false
		case <-ticker.C:
			current, err := s.store.GetWorkspaceRequest(
				r.Context(), principal, orgID, sessionID, request.ID,
			)
			if err != nil {
				s.writeWorkspaceStoreError(w, r, err)
				return nil, false
			}
			switch current.Status {
			case "succeeded":
				return current.Response, true
			case "failed":
				status := http.StatusUnprocessableEntity
				if current.ErrorCode == "TRANSPORT_TIMEOUT" {
					status = http.StatusGatewayTimeout
				}
				writeError(w, r, status, current.ErrorCode, current.ErrorMessage)
				return nil, false
			case "cancelled":
				writeError(w, r, http.StatusRequestTimeout, "REQUEST_CANCELLED", "The worker request was cancelled.")
				return nil, false
			}
		}
	}
}

func (s *Server) cancelWorkspaceRequest(
	r *http.Request,
	principal domain.Principal,
	orgID, sessionID, requestID string,
) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), time.Second)
	defer cancel()
	if err := s.store.CancelWorkspaceRequest(ctx, principal, orgID, sessionID, requestID); err != nil {
		s.logger.Warn("cancel worker transport request", "error", err, "request_id", requestID)
	}
}

func (s *Server) writeWorkspaceStoreError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, postgres.ErrWorkerUnavailable):
		writeError(w, r, http.StatusConflict, "WORKER_UNAVAILABLE", "The session worker is not connected.")
	case errors.Is(err, postgres.ErrWorkspaceReadOnly):
		writeError(w, r, http.StatusForbidden, "WORKSPACE_READ_ONLY", "This session does not allow workspace writes.")
	case errors.Is(err, postgres.ErrConflict):
		writeError(w, r, http.StatusTooManyRequests, "TOO_MANY_WORKER_REQUESTS", "Too many workspace operations are already in progress.")
	default:
		s.writeStoreError(w, r, err)
	}
}
