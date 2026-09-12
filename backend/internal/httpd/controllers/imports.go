package controllers

import (
	"context"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apispec"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/envelope"
	"github.com/aoagents/agent-orchestrator/backend/internal/legacyimport"
	importsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/importer"
)

// ImportService is the controller-facing import service contract.
type ImportService interface {
	Status(ctx context.Context) (importsvc.Status, error)
	Run(ctx context.Context) (legacyimport.Report, error)
	Validate(ctx context.Context, in importsvc.ImportValidationInput) (importsvc.ImportValidationResult, error)
	PrepareGit(ctx context.Context, in importsvc.GitPreparationInput) (importsvc.GitPreparationResult, error)
}

// ImportController owns the /import routes.
type ImportController struct {
	Svc ImportService
}

// Register mounts import REST routes on the supplied router.
func (c *ImportController) Register(r chi.Router) {
	r.Get("/import", c.status)
	r.Post("/import", c.run)
	r.Post("/imports/validate", c.validate)
	r.Post("/imports/prepare-git", c.prepareGit)
}

func (c *ImportController) status(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "GET", "/api/v1/import")
		return
	}
	st, err := c.Svc.Status(r.Context())
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, ImportStatusResponse{
		Available:  st.Available,
		LegacyRoot: st.LegacyRoot,
	})
}

func (c *ImportController) run(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "POST", "/api/v1/import")
		return
	}
	rep, err := c.Svc.Run(r.Context())
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, ImportRunResponse{Report: rep})
}

func (c *ImportController) validate(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "POST", "/api/v1/imports/validate")
		return
	}
	var in importsvc.ImportValidationInput
	if err := decodeJSONStrict(r, &in); err != nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_JSON", "Invalid JSON body", nil)
		return
	}
	result, err := c.Svc.Validate(r.Context(), in)
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, result)
}

func (c *ImportController) prepareGit(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "POST", "/api/v1/imports/prepare-git")
		return
	}
	var in importsvc.GitPreparationInput
	if err := decodeJSONStrict(r, &in); err != nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_JSON", "Invalid JSON body", nil)
		return
	}
	result, err := c.Svc.PrepareGit(r.Context(), in)
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, result)
}
