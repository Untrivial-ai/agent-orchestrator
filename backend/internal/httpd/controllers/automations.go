package controllers

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apispec"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/envelope"
	automationsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/automation"
)

// AutomationService defines the automation operations exposed over HTTP.
type AutomationService interface {
	Create(context.Context, automationsvc.CreateInput) (domain.Automation, error)
	Get(context.Context, domain.AutomationID) (domain.Automation, error)
	List(context.Context, domain.AutomationFilter) (domain.AutomationPage, error)
	Update(context.Context, domain.AutomationID, automationsvc.UpdateInput) (domain.Automation, error)
	Delete(context.Context, domain.AutomationID) error
	Runs(context.Context, domain.AutomationRunFilter) (domain.AutomationRunPage, error)
}

// AutomationsController serves the automation API routes.
type AutomationsController struct{ Svc AutomationService }

// Register mounts the automation API routes on r.
func (c *AutomationsController) Register(r chi.Router) {
	r.Get("/automations", c.list)
	r.Post("/automations", c.create)
	r.Get("/automations/{automationId}", c.get)
	r.Patch("/automations/{automationId}", c.update)
	r.Delete("/automations/{automationId}", c.delete)
	r.Get("/automations/{automationId}/runs", c.runs)
}

func (c *AutomationsController) create(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "POST", "/api/v1/automations")
		return
	}
	var req CreateAutomationRequest
	if err := decodeJSONStrict(r, &req); err != nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_JSON", "Invalid JSON body", nil)
		return
	}
	rec, err := c.Svc.Create(r.Context(), automationsvc.CreateInput{
		ProjectID: domain.ProjectID(req.ProjectID), DisplayName: req.DisplayName, Prompt: req.Prompt,
		Kind: domain.SessionKind(req.Kind), Harness: domain.AgentHarness(req.Harness), RRule: req.RRule,
		Cron: req.Cron, Timezone: req.Timezone, Enabled: req.Enabled,
	})
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusCreated, AutomationEnvelope{Automation: automationResponse(rec)})
}

func (c *AutomationsController) get(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "GET", "/api/v1/automations/{automationId}")
		return
	}
	id := domain.AutomationID(chi.URLParam(r, "automationId"))
	rec, err := c.Svc.Get(r.Context(), id)
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, AutomationEnvelope{Automation: automationResponse(rec)})
}

func (c *AutomationsController) list(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "GET", "/api/v1/automations")
		return
	}
	limit, offset, err := automationPage(r)
	if err != nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_QUERY", err.Error(), nil)
		return
	}
	filter := domain.AutomationFilter{Limit: limit, Offset: offset}
	if value := r.URL.Query().Get("projectId"); value != "" {
		id := domain.ProjectID(value)
		filter.ProjectID = &id
	}
	if value := r.URL.Query().Get("enabled"); value != "" {
		enabled, parseErr := strconv.ParseBool(value)
		if parseErr != nil {
			envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_QUERY", "enabled must be true or false", nil)
			return
		}
		filter.Enabled = &enabled
	}
	page, err := c.Svc.List(r.Context(), filter)
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	items := make([]AutomationResponse, 0, len(page.Items))
	for _, rec := range page.Items {
		items = append(items, automationResponse(rec))
	}
	next := ""
	if offset+int64(len(items)) < page.Total {
		next = encodeAutomationCursor(offset + int64(len(items)))
	}
	envelope.WriteJSON(w, http.StatusOK, ListAutomationsResponse{Automations: items, NextCursor: next})
}

func (c *AutomationsController) update(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "PATCH", "/api/v1/automations/{automationId}")
		return
	}
	var req UpdateAutomationRequest
	if err := decodeJSONStrict(r, &req); err != nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_JSON", "Invalid JSON body", nil)
		return
	}
	input := automationsvc.UpdateInput{DisplayName: req.DisplayName, Prompt: req.Prompt, RRule: req.RRule, Cron: req.Cron, Timezone: req.Timezone, Enabled: req.Enabled}
	if req.Kind != nil {
		value := domain.SessionKind(*req.Kind)
		input.Kind = &value
	}
	if req.Harness != nil {
		value := domain.AgentHarness(*req.Harness)
		input.Harness = &value
	}
	rec, err := c.Svc.Update(r.Context(), domain.AutomationID(chi.URLParam(r, "automationId")), input)
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, AutomationEnvelope{Automation: automationResponse(rec)})
}

func (c *AutomationsController) delete(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "DELETE", "/api/v1/automations/{automationId}")
		return
	}
	if err := c.Svc.Delete(r.Context(), domain.AutomationID(chi.URLParam(r, "automationId"))); err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (c *AutomationsController) runs(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "GET", "/api/v1/automations/{automationId}/runs")
		return
	}
	limit, offset, err := automationPage(r)
	if err != nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_QUERY", err.Error(), nil)
		return
	}
	page, err := c.Svc.Runs(r.Context(), domain.AutomationRunFilter{AutomationID: domain.AutomationID(chi.URLParam(r, "automationId")), Limit: limit, Offset: offset})
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	items := make([]AutomationRunResponse, 0, len(page.Items))
	for _, run := range page.Items {
		items = append(items, automationRunResponse(run))
	}
	next := ""
	if offset+int64(len(items)) < page.Total {
		next = encodeAutomationCursor(offset + int64(len(items)))
	}
	envelope.WriteJSON(w, http.StatusOK, ListAutomationRunsResponse{Runs: items, NextCursor: next})
}

func automationResponse(rec domain.Automation) AutomationResponse {
	var latest *AutomationRunSummaryResponse
	if rec.LatestRun != nil {
		response := automationRunSummaryResponse(*rec.LatestRun)
		latest = &response
	}
	return AutomationResponse{ID: string(rec.ID), ProjectID: string(rec.ProjectID), DisplayName: rec.DisplayName, Prompt: rec.Prompt, Kind: string(rec.Kind), Harness: string(rec.Harness), RRule: rec.RRuleText, Timezone: rec.Timezone, Enabled: rec.Enabled, NextRunAt: rec.NextRunAt, LastRunAt: rec.LastRunAt, CreatedAt: rec.CreatedAt, UpdatedAt: rec.UpdatedAt, LatestRun: latest}
}
func automationRunSummaryResponse(run domain.AutomationRun) AutomationRunSummaryResponse {
	response := AutomationRunSummaryResponse{ID: string(run.ID), Status: string(run.Status), ScheduledFor: run.ScheduledFor, ErrorMessage: run.ErrorMessage, StartedAt: run.StartedAt, FinishedAt: run.FinishedAt}
	if run.SessionID != nil {
		response.SessionID = string(*run.SessionID)
	}
	return response
}
func automationRunResponse(run domain.AutomationRun) AutomationRunResponse {
	response := AutomationRunResponse{ID: string(run.ID), AutomationID: string(run.AutomationID), ScheduledFor: run.ScheduledFor, Status: string(run.Status), AttemptCount: run.AttemptCount, ClaimedAt: run.ClaimedAt, LeaseExpiresAt: run.LeaseExpiresAt, StartedAt: run.StartedAt, FinishedAt: run.FinishedAt, ErrorMessage: run.ErrorMessage, CreatedAt: run.CreatedAt, UpdatedAt: run.UpdatedAt}
	if run.SessionID != nil {
		response.SessionID = string(*run.SessionID)
	}
	return response
}
func automationPage(r *http.Request) (int64, int64, error) {
	limit := int64(50)
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed < 1 || parsed > 100 {
			return 0, 0, fmt.Errorf("limit must be between 1 and 100")
		}
		limit = parsed
	}
	offset := int64(0)
	if cursor := r.URL.Query().Get("cursor"); cursor != "" {
		decoded, err := base64.RawURLEncoding.DecodeString(cursor)
		if err != nil {
			return 0, 0, fmt.Errorf("cursor is invalid")
		}
		parsed, err := strconv.ParseInt(string(decoded), 10, 64)
		if err != nil || parsed < 0 {
			return 0, 0, fmt.Errorf("cursor is invalid")
		}
		offset = parsed
	}
	return limit, offset, nil
}
func encodeAutomationCursor(offset int64) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(offset, 10)))
}
