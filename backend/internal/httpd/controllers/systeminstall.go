package controllers

import (
	"context"
	"errors"
	"io"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apispec"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/envelope"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/systeminstall"
)

// Installer is the controller-facing contract for real, async install runs
// against the fixed systeminstall.Target allowlist.
type Installer interface {
	Start(ctx context.Context, target systeminstall.Target) (systeminstall.Job, error)
	StartAgentOperation(ctx context.Context, target systeminstall.Target, method string, operation systeminstall.AgentOperation) (systeminstall.Job, error)
	Status(ctx context.Context, target systeminstall.Target) (systeminstall.Job, error)
	AgentPlans(ctx context.Context) ([]systeminstall.AgentPlan, error)
	AgentJobs(ctx context.Context) ([]systeminstall.Job, error)
	Verify(ctx context.Context, target systeminstall.Target) (systeminstall.Job, error)
}

// SystemInstallController owns the system prerequisite and agent harness install routes.
type SystemInstallController struct {
	Installer Installer
}

// Register mounts the system install routes on the supplied router.
func (c *SystemInstallController) Register(r chi.Router) {
	r.Post("/system/install/{target}", c.start)
	r.Get("/system/install/{target}", c.status)
	r.Get("/agents/installers", c.agentPlans)
	r.Get("/agents/install-jobs", c.agentJobs)
	r.Post("/agents/{agent}/install", c.startAgent)
	r.Get("/agents/{agent}/install", c.agentStatus)
	r.Post("/agents/{agent}/verify", c.verifyAgent)
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
	var request StartAgentInstallRequest
	if err := decodeJSONStrict(r, &request); err != nil && !errors.Is(err, io.EOF) {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_INSTALL_REQUEST", "invalid install request", nil)
		return
	}
	operation := request.Operation
	if operation == "" {
		operation = systeminstall.AgentOperationInstall
	}
	if operation != systeminstall.AgentOperationInstall && operation != systeminstall.AgentOperationReinstall {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_INSTALL_OPERATION", "operation must be install or reinstall", nil)
		return
	}
	job, err := c.Installer.StartAgentOperation(r.Context(), target, request.Method, operation)
	if err != nil {
		if writeAgentInstallError(w, r, err) {
			return
		}
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusAccepted, job)
}

func (c *SystemInstallController) agentJobs(w http.ResponseWriter, r *http.Request) {
	if c.Installer == nil {
		apispec.NotImplemented(w, r, "GET", "/api/v1/agents/install-jobs")
		return
	}
	jobs, err := c.Installer.AgentJobs(r.Context())
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, AgentInstallJobsResponse{Jobs: jobs})
}

func (c *SystemInstallController) verifyAgent(w http.ResponseWriter, r *http.Request) {
	if c.Installer == nil {
		apispec.NotImplemented(w, r, "POST", "/api/v1/agents/{agent}/verify")
		return
	}
	target, ok := parseAgentInstallTarget(w, r)
	if !ok {
		return
	}
	job, err := c.Installer.Verify(r.Context(), target)
	if err != nil {
		if writeAgentInstallError(w, r, err) {
			return
		}
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusAccepted, job)
}

func writeAgentInstallError(w http.ResponseWriter, r *http.Request, err error) bool {
	switch {
	case errors.Is(err, systeminstall.ErrInstallMethod):
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INSTALL_METHOD_UNAVAILABLE", "the selected install method is unavailable", nil)
		return true
	case errors.Is(err, systeminstall.ErrHarnessActive):
		envelope.WriteAPIError(w, r, http.StatusConflict, "conflict", "HARNESS_ACTIVE", "end active Droid sessions before installing or reinstalling Droid", nil)
		return true
	case errors.Is(err, systeminstall.ErrInstallActive):
		envelope.WriteAPIError(w, r, http.StatusConflict, "conflict", "INSTALL_ACTIVE", "an install or verification job is already active for this harness", nil)
		return true
	default:
		return false
	}
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
	job, err := c.Installer.Status(r.Context(), target)
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, job)
}

func (c *SystemInstallController) start(w http.ResponseWriter, r *http.Request) {
	if c.Installer == nil {
		apispec.NotImplemented(w, r, "POST", "/api/v1/system/install/{target}")
		return
	}
	target, ok := parseInstallTarget(w, r)
	if !ok {
		return
	}
	job, err := c.Installer.Start(r.Context(), target)
	if err != nil {
		if writeAgentInstallError(w, r, err) {
			return
		}
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusAccepted, job)
}

func (c *SystemInstallController) status(w http.ResponseWriter, r *http.Request) {
	if c.Installer == nil {
		apispec.NotImplemented(w, r, "GET", "/api/v1/system/install/{target}")
		return
	}
	target, ok := parseInstallTarget(w, r)
	if !ok {
		return
	}
	job, err := c.Installer.Status(r.Context(), target)
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, job)
}

// parseInstallTarget reads and validates the {target} path param against the
// fixed systeminstall allowlist before it ever reaches the service, so a path
// traversal attempt or other junk value gets a clean 400 here rather than
// being passed through.
func parseInstallTarget(w http.ResponseWriter, r *http.Request) (systeminstall.Target, bool) {
	target := systeminstall.Target(chi.URLParam(r, "target"))
	if !systeminstall.IsSystemTarget(target) {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "UNKNOWN_INSTALL_TARGET",
			"unknown install target", nil)
		return "", false
	}
	return target, true
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
