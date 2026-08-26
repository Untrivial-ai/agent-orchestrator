package controllers

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apispec"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/envelope"
)

type ReportService interface {
	Create(context.Context, domain.SessionID, domain.ReportType, string) (domain.ReportRecord, error)
}

type ReportsController struct{ Svc ReportService }

func (c *ReportsController) Register(r chi.Router) { r.Post("/reports", c.create) }

func (c *ReportsController) create(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "POST", "/api/v1/reports")
		return
	}
	var req CreateReportRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_JSON", "Invalid JSON body", nil)
		return
	}
	rec, err := c.Svc.Create(r.Context(), domain.SessionID(req.SessionID), domain.ReportType(req.Type), req.Note)
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusCreated, CreateReportResponse{Report: reportResponse(rec)})
}

func reportResponse(rec domain.ReportRecord) ReportResponse {
	return ReportResponse{ID: rec.ID, SessionID: string(rec.SessionID), ProjectID: string(rec.ProjectID), Type: string(rec.Type), Note: rec.Note, CreatedAt: rec.CreatedAt, DeliveryState: string(rec.DeliveryState)}
}
