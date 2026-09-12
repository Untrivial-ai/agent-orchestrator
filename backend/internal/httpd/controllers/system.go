package controllers

import (
	"context"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apispec"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/envelope"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/shellterm"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/systemcheck"
)

// SystemChecker is the controller-facing contract for the lightweight startup
// preflight the desktop loading screen runs before showing the board.
type SystemChecker interface {
	CheckStartup(ctx context.Context) (systemcheck.Report, error)
	CheckGitHubAuth(ctx context.Context) (systemcheck.Requirement, error)
	OpenGitHubAuthTerminal(ctx context.Context) (shellterm.ShellTerminal, error)
}

// SystemController owns the /system routes.
type SystemController struct {
	Checks SystemChecker
}

// Register mounts the system requirements route on the supplied router.
func (c *SystemController) Register(r chi.Router) {
	r.Get("/system/requirements", c.requirements)
	r.Get("/system/github-auth", c.githubAuth)
	r.Post("/system/github-auth/terminal", c.openGitHubAuthTerminal)
}

func (c *SystemController) openGitHubAuthTerminal(w http.ResponseWriter, r *http.Request) {
	if c.Checks == nil {
		apispec.NotImplemented(w, r, http.MethodPost, "/api/v1/system/github-auth/terminal")
		return
	}
	terminal, err := c.Checks.OpenGitHubAuthTerminal(r.Context())
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusCreated, ShellTerminalEnvelope{ShellTerminal: shellTerminalResponse(terminal)})
}

func (c *SystemController) githubAuth(w http.ResponseWriter, r *http.Request) {
	if c.Checks == nil {
		apispec.NotImplemented(w, r, "GET", "/api/v1/system/github-auth")
		return
	}
	requirement, err := c.Checks.CheckGitHubAuth(r.Context())
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, requirement)
}

func (c *SystemController) requirements(w http.ResponseWriter, r *http.Request) {
	if c.Checks == nil {
		apispec.NotImplemented(w, r, "GET", "/api/v1/system/requirements")
		return
	}
	report, err := c.Checks.CheckStartup(r.Context())
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, report)
}
