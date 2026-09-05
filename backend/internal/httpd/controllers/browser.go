package controllers

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/aoagents/agent-orchestrator/backend/internal/browserruntime"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apispec"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/envelope"
)

const browserCapabilityHeader = "X-AO-Browser-Capability"

// BrowserService authorizes and executes session-scoped browser operations.
type BrowserService interface {
	Status(ctx context.Context, sessionID domain.SessionID, capability string) (browserruntime.Status, error)
	Execute(ctx context.Context, sessionID domain.SessionID, capability, action string, args map[string]interface{}) (browserruntime.Result, string, error)
}

// BrowserController exposes the loopback-only browser command API.
type BrowserController struct {
	Svc BrowserService
}

// Register adds browser status and command routes to the API router.
func (c *BrowserController) Register(r chi.Router) {
	r.Get("/browser/status", c.status)
	r.Post("/browser/commands", c.execute)
}

func (c *BrowserController) status(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, http.MethodGet, "/api/v1/browser/status")
		return
	}
	sessionID := domain.SessionID(strings.TrimSpace(r.URL.Query().Get("sessionId")))
	if sessionID == "" {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "SESSION_ID_REQUIRED", "sessionId is required", nil)
		return
	}
	status, err := c.Svc.Status(r.Context(), sessionID, r.Header.Get(browserCapabilityHeader))
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, BrowserStatusResponse{
		SessionID:   sessionID,
		Connected:   status.Connected,
		ConnectedAt: status.ConnectedAt,
		Transport:   "electron-webcontents-debugger",
	})
}

func (c *BrowserController) execute(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, http.MethodPost, "/api/v1/browser/commands")
		return
	}
	var in BrowserCommandRequest
	if err := decodeJSON(r, &in); err != nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_JSON", "Invalid JSON body", nil)
		return
	}
	if in.SessionID == "" {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "SESSION_ID_REQUIRED", "sessionId is required", nil)
		return
	}
	result, action, err := c.Svc.Execute(
		r.Context(),
		in.SessionID,
		r.Header.Get(browserCapabilityHeader),
		in.Action,
		in.Args,
	)
	if err != nil {
		writeBrowserError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, BrowserCommandResponse{
		RequestID: result.RequestID,
		SessionID: in.SessionID,
		Action:    action,
		Result:    result.Value,
	})
}

func writeBrowserError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, browserruntime.ErrUnavailable) {
		envelope.WriteAPIError(w, r, http.StatusServiceUnavailable, "unavailable", "BROWSER_RUNTIME_UNAVAILABLE", "Desktop browser runtime is not connected", nil)
		return
	}
	var commandErr browserruntime.CommandError
	if errors.As(err, &commandErr) {
		status := http.StatusUnprocessableEntity
		typeName := "unprocessable"
		switch commandErr.Code {
		case "INVALID_ARGUMENT", "URL_REQUIRED", "REFERENCE_REQUIRED", "TAB_ID_REQUIRED",
			"AGENT_BROWSER_COMMAND_BLOCKED":
			status = http.StatusBadRequest
			typeName = "bad_request"
		case "STALE_REFERENCE", "TAB_NOT_FOUND", "BROWSER_TARGET_MISMATCH":
			status = http.StatusConflict
			typeName = "conflict"
		case "BROWSER_TARGET_UNAVAILABLE", "BROWSER_AUTOMATION_UNAVAILABLE", "AGENT_BROWSER_NOT_INSTALLED",
			"AGENT_BROWSER_START_FAILED", "BROWSER_DEVTOOLS_UNAVAILABLE":
			status = http.StatusServiceUnavailable
			typeName = "unavailable"
		}
		envelope.WriteAPIError(w, r, status, typeName, commandErr.Code, commandErr.Message, nil)
		return
	}
	envelope.WriteError(w, r, err)
}
