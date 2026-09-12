package controllers

import (
	"context"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apispec"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/envelope"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/agentauth"
)

// AgentAuthService exposes only fixed, daemon-owned authentication plans.
type AgentAuthService interface {
	Plans(context.Context) []agentauth.Plan
	Start(ctx context.Context, agentID string) (agentauth.StartResult, error)
}

// AgentAuthController owns the safe native authentication routes.
type AgentAuthController struct {
	Svc AgentAuthService
}

// Register mounts the agent authentication routes.
func (c *AgentAuthController) Register(r chi.Router) {
	r.Get("/agents/auth-plans", c.list)
	r.Post("/agents/{agent}/auth", c.start)
}

func (c *AgentAuthController) list(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, http.MethodGet, "/api/v1/agents/auth-plans")
		return
	}
	envelope.WriteJSON(w, http.StatusOK, ListAgentAuthPlansResponse{Plans: c.Svc.Plans(r.Context())})
}

func (c *AgentAuthController) start(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, http.MethodPost, "/api/v1/agents/{agent}/auth")
		return
	}
	result, err := c.Svc.Start(r.Context(), chi.URLParam(r, "agent"))
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusCreated, StartAgentAuthResponse{
		AgentID:       result.AgentID,
		Action:        result.Action,
		Guidance:      result.Guidance,
		TerminalInput: result.TerminalInput,
		Terminal:      shellTerminalResponse(result.Terminal),
	})
}
