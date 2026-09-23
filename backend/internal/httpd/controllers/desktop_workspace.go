package controllers

import (
	"context"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apispec"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/envelope"
)

// DesktopWorkspaceService is the narrow read boundary Electron main uses to
// resolve a session handoff without teaching the renderer about local paths.
type DesktopWorkspaceService interface {
	WorkspaceLocation(ctx context.Context, id domain.SessionID) (string, error)
}

// DesktopWorkspaceController owns the loopback-only desktop handoff route.
type DesktopWorkspaceController struct {
	Svc DesktopWorkspaceService
}

// Register mounts the desktop-only workspace-location route.
func (c *DesktopWorkspaceController) Register(r chi.Router) {
	r.Get("/desktop/sessions/{sessionId}/workspace", c.location)
}

func (c *DesktopWorkspaceController) location(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, http.MethodGet, "/api/v1/desktop/sessions/{sessionId}/workspace")
		return
	}
	id := domain.SessionID(chi.URLParam(r, "sessionId"))
	workspacePath, err := c.Svc.WorkspaceLocation(r.Context(), id)
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, DesktopWorkspaceLocationResponse{
		SessionID:     id,
		WorkspacePath: workspacePath,
	})
}
