package controllers

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apispec"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/envelope"
	reportsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/report"
)

// ReportService is the controller-facing report creation contract.
type ReportService interface {
	Create(context.Context, reportsvc.CreateInput) (domain.ReportRecord, error)
}

// ReportsController owns the /reports routes.
type ReportsController struct{ Svc ReportService }

// Register mounts report routes on r.
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
	outputs := make([]domain.ReportOutput, len(req.Outputs))
	for i, output := range req.Outputs {
		outputs[i] = domain.ReportOutput{Kind: domain.ReportOutputKind(output.Kind), Reference: output.Reference, Label: output.Label}
	}
	rec, err := c.Svc.Create(r.Context(), reportsvc.CreateInput{
		SessionID: domain.SessionID(req.SessionID), State: domain.ReportState(req.State),
		Note: req.Note, Message: req.Message, Outputs: outputs,
	})
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusCreated, CreateReportResponse{ID: rec.ID})
}
