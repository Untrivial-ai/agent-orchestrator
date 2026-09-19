package controllers

import (
	"context"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apispec"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/envelope"
)

// ProjectSummaryService produces and reads durable project briefings.
type ProjectSummaryService interface {
	Get(context.Context, domain.ProjectID, bool) (domain.ProjectSummary, error)
}

// ProjectSummariesController serves the project summary read and refresh routes.
type ProjectSummariesController struct{ Svc ProjectSummaryService }

// Register mounts project summary routes.
func (c *ProjectSummariesController) Register(r chi.Router) {
	r.Get("/projects/{id}/summary", c.get)
	r.Post("/projects/{id}/summary/refresh", c.refresh)
}

func (c *ProjectSummariesController) get(w http.ResponseWriter, r *http.Request) {
	c.write(w, r, false)
}
func (c *ProjectSummariesController) refresh(w http.ResponseWriter, r *http.Request) {
	c.write(w, r, true)
}
func (c *ProjectSummariesController) write(w http.ResponseWriter, r *http.Request, refresh bool) {
	if c.Svc == nil {
		method, path := "GET", "/api/v1/projects/{id}/summary"
		if refresh {
			method, path = "POST", "/api/v1/projects/{id}/summary/refresh"
		}
		apispec.NotImplemented(w, r, method, path)
		return
	}
	summary, err := c.Svc.Get(r.Context(), domain.ProjectID(chi.URLParam(r, "id")), refresh)
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, ProjectSummaryResponse{Summary: summary})
}
