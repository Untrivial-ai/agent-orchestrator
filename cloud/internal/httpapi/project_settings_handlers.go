package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/aoagents/agent-orchestrator/cloud/internal/postgres"
	"github.com/go-chi/chi/v5"
)

type projectSettingsStore interface {
	UpdateProjectSettings(context.Context, domain.Principal, string, string, domain.ProjectSettingsPatch) (domain.Project, error)
}

func (s *Server) getProject(w http.ResponseWriter, r *http.Request) {
	orgID, projectID := chi.URLParam(r, "orgId"), chi.URLParam(r, "projectId")
	if requireUUID(orgID, "orgId") != nil || requireUUID(projectID, "projectId") != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "orgId and projectId must be UUIDs.")
		return
	}
	project, err := s.store.GetProject(r.Context(), principalFrom(r), orgID, projectID)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"project": toProjectResponse(project)})
}

func (s *Server) updateProjectSettings(w http.ResponseWriter, r *http.Request) {
	orgID, projectID := chi.URLParam(r, "orgId"), chi.URLParam(r, "projectId")
	if requireUUID(orgID, "orgId") != nil || requireUUID(projectID, "projectId") != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "orgId and projectId must be UUIDs.")
		return
	}
	var raw json.RawMessage
	if err := decodeJSON(w, r, &raw); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "The request body is invalid.")
		return
	}
	patch, err := domain.ParseProjectSettingsPatch(raw)
	if err != nil {
		writeError(w, r, http.StatusUnprocessableEntity, "validation_error", err.Error())
		return
	}
	store, ok := s.store.(projectSettingsStore)
	if !ok {
		writeError(w, r, http.StatusNotImplemented, "not_implemented", "Cloud project settings are unavailable.")
		return
	}
	project, err := store.UpdateProjectSettings(r.Context(), principalFrom(r), orgID, projectID, patch)
	if errors.Is(err, postgres.ErrInvalid) {
		writeError(w, r, http.StatusUnprocessableEntity, "validation_error", err.Error())
		return
	}
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"project": toProjectResponse(project)})
}
