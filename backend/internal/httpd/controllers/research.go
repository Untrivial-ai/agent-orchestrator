package controllers

import (
	"context"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apispec"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/envelope"
)

type researchService interface {
	StartResearch(context.Context, domain.SessionID, string) (domain.ResearchRun, error)
	GetResearch(context.Context, domain.SessionID, string) (domain.ResearchRun, error)
	ListResearch(context.Context, domain.SessionID) ([]domain.ResearchRun, error)
	CancelResearch(context.Context, domain.SessionID, string) (domain.ResearchRun, error)
	ResolveResearchApproval(context.Context, domain.SessionID, string, string, string) (domain.ResearchRun, error)
}

func (c *SessionsController) researchService(w http.ResponseWriter, r *http.Request) (researchService, bool) {
	service, ok := c.Svc.(researchService)
	if !ok {
		apispec.NotImplemented(w, r, r.Method, "/api/v1/sessions/{sessionId}/research")
		return nil, false
	}
	return service, true
}

func (c *SessionsController) startResearch(w http.ResponseWriter, r *http.Request) {
	service, ok := c.researchService(w, r)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 20<<10)
	var input StartResearchRequest
	if err := decodeJSONStrict(r, &input); err != nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_JSON", "Invalid research request", nil)
		return
	}
	rec, err := service.StartResearch(r.Context(), domain.SessionID(chi.URLParam(r, "sessionId")), input.Prompt)
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusCreated, ResearchRunResponse{Research: rec})
}

func (c *SessionsController) listResearch(w http.ResponseWriter, r *http.Request) {
	service, ok := c.researchService(w, r)
	if !ok {
		return
	}
	runs, err := service.ListResearch(r.Context(), domain.SessionID(chi.URLParam(r, "sessionId")))
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, ListResearchRunsResponse{Research: runs})
}

func (c *SessionsController) getResearch(w http.ResponseWriter, r *http.Request) {
	service, ok := c.researchService(w, r)
	if !ok {
		return
	}
	rec, err := service.GetResearch(r.Context(), domain.SessionID(chi.URLParam(r, "sessionId")), chi.URLParam(r, "researchId"))
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, ResearchRunResponse{Research: rec})
}

func (c *SessionsController) cancelResearch(w http.ResponseWriter, r *http.Request) {
	service, ok := c.researchService(w, r)
	if !ok {
		return
	}
	rec, err := service.CancelResearch(r.Context(), domain.SessionID(chi.URLParam(r, "sessionId")), chi.URLParam(r, "researchId"))
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, ResearchRunResponse{Research: rec})
}

func (c *SessionsController) resolveResearchApproval(w http.ResponseWriter, r *http.Request) {
	service, ok := c.researchService(w, r)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	var input ResolveResearchApprovalRequest
	if err := decodeJSONStrict(r, &input); err != nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_JSON", "Invalid research approval", nil)
		return
	}
	rec, err := service.ResolveResearchApproval(r.Context(), domain.SessionID(chi.URLParam(r, "sessionId")), chi.URLParam(r, "researchId"), input.RequestID, input.OptionID)
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, ResearchRunResponse{Research: rec})
}
