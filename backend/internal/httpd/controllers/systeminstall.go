package controllers

import (
	"context"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apispec"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/envelope"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/systeminstall"
)

// Installer is the controller-facing contract for real, asynchronous harness
// installs against the fixed systeminstall.Target allowlist.
type Installer interface {
	Start(ctx context.Context, target systeminstall.Target) (systeminstall.Job, error)
	Status(target systeminstall.Target) (systeminstall.Job, error)
	AgentPlans(ctx context.Context) ([]systeminstall.AgentPlan, error)
}

// SystemInstallController owns the agent harness install routes.
type SystemInstallController struct {
	Installer Installer
}

// Register mounts the agent installer catalog, start, and status routes.
func (c *SystemInstallController) Register(r chi.Router) {
	r.Get("/agents/installers", c.agentPlans)
	r.Post("/agents/{agent}/install", c.startAgent)
	r.Get("/agents/{agent}/install", c.agentStatus)
}

func (c *SystemInstallController) agentPlans(w http.ResponseWriter, r *http.Request) {
	if c.Installer == nil {
		apispec.NotImplemented(w, r, "GET", "/api/v1/agents/installers")
		return
	}
	plans, err := c.Installer.AgentPlans(r.Context())
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, AgentInstallerCatalogResponse{Agents: plans})
}

func (c *SystemInstallController) startAgent(w http.ResponseWriter, r *http.Request) {
	if c.Installer == nil {
		apispec.NotImplemented(w, r, "POST", "/api/v1/agents/{agent}/install")
		return
	}
	target, ok := parseAgentInstallTarget(w, r)
	if !ok {
		return
	}
	job, err := c.Installer.Start(r.Context(), target)
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusAccepted, job)
}

func (c *SystemInstallController) agentStatus(w http.ResponseWriter, r *http.Request) {
	if c.Installer == nil {
		apispec.NotImplemented(w, r, "GET", "/api/v1/agents/{agent}/install")
		return
	}
	target, ok := parseAgentInstallTarget(w, r)
	if !ok {
		return
	}
	job, err := c.Installer.Status(target)
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, job)
}

func parseAgentInstallTarget(w http.ResponseWriter, r *http.Request) (systeminstall.Target, bool) {
	target := systeminstall.Target(chi.URLParam(r, "agent"))
	if !systeminstall.IsAgentTarget(target) {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "UNKNOWN_AGENT_INSTALL_TARGET",
			"unknown agent install target", nil)
		return "", false
	}
	return target, true
}
