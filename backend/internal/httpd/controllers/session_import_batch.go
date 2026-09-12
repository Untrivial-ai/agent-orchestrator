package controllers

import (
	"context"
	"net/http"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/envelope"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/sessionimportsvc"
)

// ImportSessionsRequest selects native conversations within one project.
type ImportSessionsRequest struct {
	ProjectID string                       `json:"projectId"`
	Sessions  []sessionimportsvc.Selection `json:"sessions"`
}

// ImportSessionsResponse reports each selected conversation's durable result.
type ImportSessionsResponse struct {
	Results []sessionimportsvc.ImportResult `json:"results"`
}

func (c *SessionsController) importSessions(w http.ResponseWriter, r *http.Request) {
	var req ImportSessionsRequest
	if err := decodeJSON(r, &req); err != nil || strings.TrimSpace(req.ProjectID) == "" || len(req.Sessions) == 0 || len(req.Sessions) > 1000 {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_BODY", "projectId and between 1 and 1000 sessions are required", nil)
		return
	}
	for _, selection := range req.Sessions {
		if strings.TrimSpace(selection.Provider) == "" || strings.TrimSpace(selection.NativeSessionID) == "" {
			envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_BODY", "every session requires provider and nativeSessionId", nil)
			return
		}
	}
	svc, ok := c.Import.(interface {
		ImportBatch(context.Context, domain.ProjectID, []sessionimportsvc.Selection) []sessionimportsvc.ImportResult
	})
	if !ok {
		envelope.WriteAPIError(w, r, http.StatusNotImplemented, "not_implemented", "NOT_IMPLEMENTED", "session import unavailable", nil)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, ImportSessionsResponse{Results: svc.ImportBatch(r.Context(), domain.ProjectID(req.ProjectID), req.Sessions)})
}
