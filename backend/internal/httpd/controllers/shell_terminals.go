package controllers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"

	"github.com/go-chi/chi/v5"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apispec"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/envelope"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/requestscope"
	shelltermsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/shellterm"
)

// ShellTerminalService is the controller-facing standalone shell terminal
// contract.
type ShellTerminalService interface {
	OpenShellTerminal(ctx context.Context, in shelltermsvc.OpenShellTerminalInput) (shelltermsvc.ShellTerminal, error)
	ListShellTerminalsForCurrentAppRun(ctx context.Context) ([]shelltermsvc.ShellTerminal, error)
	RenameShellTerminal(ctx context.Context, handleID, title string) (shelltermsvc.ShellTerminal, error)
	CloseShellTerminal(ctx context.Context, handleID string) error
	CueCommandTerminalStatus(ctx context.Context, handleID string) (shelltermsvc.CueCommandTerminalStatus, error)
	StopCueCommandTerminal(ctx context.Context, handleID string) (shelltermsvc.CueCommandTerminalStatus, error)
}

// ShellTerminalsController owns the /shell-terminals routes: standalone shells
// the user opens by hand, independent of any agent session.
type ShellTerminalsController struct {
	Svc ShellTerminalService
}

// Register mounts the bounded shell terminal REST routes.
func (c *ShellTerminalsController) Register(r chi.Router) {
	r.Get("/shell-terminals", c.list)
	r.Post("/shell-terminals", c.open)
	r.Patch("/shell-terminals/{handleId}", c.rename)
	r.Delete("/shell-terminals/{handleId}", c.close)
	r.Get("/shell-terminals/{handleId}/command-status", c.commandStatus)
	r.Post("/shell-terminals/{handleId}/stop-command", c.stopCommand)
}

func (c *ShellTerminalsController) commandStatus(w http.ResponseWriter, r *http.Request) {
	c.commandAction(w, r, false)
}

func (c *ShellTerminalsController) stopCommand(w http.ResponseWriter, r *http.Request) {
	if requestscope.IsLAN(r.Context()) {
		envelope.WriteAPIError(w, r, http.StatusForbidden, "forbidden", "CUE_COMMAND_LOOPBACK_REQUIRED", "Command Cues can only be controlled through the local daemon", nil)
		return
	}
	c.commandAction(w, r, true)
}

func (c *ShellTerminalsController) commandAction(w http.ResponseWriter, r *http.Request, stop bool) {
	if c.Svc == nil {
		method := http.MethodGet
		path := "/api/v1/shell-terminals/{handleId}/command-status"
		if stop {
			method, path = http.MethodPost, "/api/v1/shell-terminals/{handleId}/stop-command"
		}
		apispec.NotImplemented(w, r, method, path)
		return
	}
	handleID, err := url.PathUnescape(chi.URLParam(r, "handleId"))
	if err != nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "SHELL_TERMINAL_ID_INVALID", "Invalid shell terminal id", nil)
		return
	}
	var status shelltermsvc.CueCommandTerminalStatus
	if stop {
		status, err = c.Svc.StopCueCommandTerminal(r.Context(), handleID)
	} else {
		status, err = c.Svc.CueCommandTerminalStatus(r.Context(), handleID)
	}
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, CueCommandTerminalStatusResponse{HandleID: status.HandleID, State: status.State})
}

func (c *ShellTerminalsController) list(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "GET", "/api/v1/shell-terminals")
		return
	}
	terminals, err := c.Svc.ListShellTerminalsForCurrentAppRun(r.Context())
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, ListShellTerminalsResponse{
		ShellTerminals: shellTerminalResponses(terminals),
	})
}

func (c *ShellTerminalsController) open(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "POST", "/api/v1/shell-terminals")
		return
	}
	// An empty body is a valid request: it means "open a shell with no project
	// context", which the service resolves to the daemon data dir.
	var req OpenShellTerminalRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_JSON", "Invalid JSON body", nil)
		return
	}
	terminal, err := c.Svc.OpenShellTerminal(r.Context(), shelltermsvc.OpenShellTerminalInput{
		ProjectID: domain.ProjectID(req.ProjectID),
		SessionID: domain.SessionID(req.SessionID),
		Shell:     req.Shell,
	})
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusCreated, ShellTerminalEnvelope{
		ShellTerminal: shellTerminalResponse(terminal),
	})
}

func (c *ShellTerminalsController) rename(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "PATCH", "/api/v1/shell-terminals/{handleId}")
		return
	}
	var req UpdateShellTerminalRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_JSON", "Invalid JSON body", nil)
		return
	}
	handleID, err := url.PathUnescape(chi.URLParam(r, "handleId"))
	if err != nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "SHELL_TERMINAL_ID_INVALID", "Invalid shell terminal id", nil)
		return
	}
	terminal, err := c.Svc.RenameShellTerminal(r.Context(), handleID, req.Title)
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, ShellTerminalEnvelope{
		ShellTerminal: shellTerminalResponse(terminal),
	})
}

func (c *ShellTerminalsController) close(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "DELETE", "/api/v1/shell-terminals/{handleId}")
		return
	}
	handleID, err := url.PathUnescape(chi.URLParam(r, "handleId"))
	if err != nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "SHELL_TERMINAL_ID_INVALID", "Invalid shell terminal id", nil)
		return
	}
	if err := c.Svc.CloseShellTerminal(r.Context(), handleID); err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func shellTerminalResponses(in []shelltermsvc.ShellTerminal) []ShellTerminalResponse {
	out := make([]ShellTerminalResponse, 0, len(in))
	for _, t := range in {
		out = append(out, shellTerminalResponse(t))
	}
	return out
}

func shellTerminalResponse(t shelltermsvc.ShellTerminal) ShellTerminalResponse {
	return ShellTerminalResponse{
		HandleID:   t.HandleID,
		ProjectID:  string(t.ProjectID),
		SessionID:  string(t.SessionID),
		WorkingDir: t.WorkingDir,
		Title:      t.Title,
		CreatedAt:  t.CreatedAt,
	}
}
