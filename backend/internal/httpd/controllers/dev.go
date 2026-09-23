package controllers

import (
	"context"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/aoagents/agent-orchestrator/backend/internal/devimport"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apispec"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/envelope"
	devimportsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/devimport"
)

// DevImportService is the controller-facing developer import service contract.
type DevImportService interface {
	RunProjects(ctx context.Context, in devimportsvc.RunInput) (devimport.Report, error)
}

// DevController owns developer-only API routes.
type DevController struct {
	Import DevImportService
}

// Register mounts developer REST routes on the supplied router.
func (c *DevController) Register(r chi.Router) {
	r.Post("/dev/import-projects", c.importProjects)
}

func (c *DevController) importProjects(w http.ResponseWriter, r *http.Request) {
	if c.Import == nil {
		apispec.NotImplemented(w, r, "POST", "/api/v1/dev/import-projects")
		return
	}
	var req DevImportProjectsRequest
	if err := decodeJSONStrictBounded(w, r, &req); err != nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_JSON", "request body must be valid JSON", nil)
		return
	}
	if req.SourceDataDir == "" {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "SOURCE_DATA_DIR_REQUIRED", "sourceDataDir is required", nil)
		return
	}
	rep, err := c.Import.RunProjects(r.Context(), devimportsvc.RunInput{
		SourceDataDir: req.SourceDataDir,
		DryRun:        req.DryRun,
	})
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, DevImportProjectsResponse{Report: rep})
}
