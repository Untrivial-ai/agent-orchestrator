package controllers

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/envelope"
	testingsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/testing"
)

type testingLegService interface {
	StartLeg(context.Context, domain.SessionID, string) (testingsvc.StartLegResult, error)
	ExecuteCurrent(context.Context, domain.SessionID, string, string, string, json.RawMessage) (testingsvc.ToolResult, error)
}

// TestingLegStartResponse carries the new attempt and its checked target paths.
type TestingLegStartResponse testingsvc.StartLegResult

// TestingWorkerIDParam selects the worker whose current target is resolved.
type TestingWorkerIDParam struct {
	SessionID string `path:"sessionId"`
}

// TestingLegParam selects a pinned revision from the worker's comparison.
type TestingLegParam struct {
	Leg string `path:"leg" enum:"base,head"`
}

// TestingLegStartRequest accepts no worker-supplied launch identity or paths.
type TestingLegStartRequest struct{}

// RegisterLegs adds exact full routes without changing the intake controller.
func (c *TestingController) RegisterLegs(r chi.Router) {
	r = r.With(testingContentTypeMiddleware)
	r.Post("/api/v1/testing/comparisons", c.startComparison)
	r.Post("/api/v1/testing/sessions/{sessionId}/legs/{leg}/start", c.startLeg)
	for _, name := range testingsvc.ToolNames {
		r.Post("/api/v1/testing/sessions/{sessionId}/tools/"+name, func(w http.ResponseWriter, r *http.Request) { c.sessionTool(w, r, name) })
	}
}

func (c *TestingController) legService(w http.ResponseWriter, r *http.Request) (testingLegService, bool) {
	svc, ok := c.Svc.(testingLegService)
	if !ok {
		envelope.WriteError(w, r, testingsvc.ProviderNotConfigured())
	}
	return svc, ok
}

func (c *TestingController) startLeg(w http.ResponseWriter, r *http.Request) {
	svc, ok := c.legService(w, r)
	if !ok {
		return
	}
	var in TestingLegStartRequest
	if r.Body != nil && r.Body != http.NoBody && !testingJSON(w, r, &in) {
		return
	}
	result, err := svc.StartLeg(r.Context(), domain.SessionID(chi.URLParam(r, "sessionId")), chi.URLParam(r, "leg"))
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusCreated, TestingLegStartResponse(result))
}

func (c *TestingController) sessionTool(w http.ResponseWriter, r *http.Request, name string) {
	svc, ok := c.legService(w, r)
	if !ok {
		return
	}
	var in TestingToolRequest
	if !testingJSON(w, r, &in) {
		return
	}
	session := chi.URLParam(r, "sessionId")
	if in.SessionID != session {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_TESTING_REQUEST", "Tool session must match the route", nil)
		return
	}
	result, err := svc.ExecuteCurrent(r.Context(), domain.SessionID(session), r.Header.Get(testingsvc.CapabilityHeader), in.RequestID, name, in.Input)
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, TestingToolResponse(result))
}
