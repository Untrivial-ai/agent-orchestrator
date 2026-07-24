package controllers

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apispec"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/envelope"
	userconfigsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/userconfig"
)

// UserConfigController owns the /user-config singleton routes. The controller
// depends only on userconfigsvc.Manager; nil keeps routes registered but returns
// OpenAPI-backed 501s.
type UserConfigController struct {
	Mgr userconfigsvc.Manager
}

// Register mounts the user-config routes on the supplied router.
func (c *UserConfigController) Register(r chi.Router) {
	r.Get("/user-config", c.get)
	r.Put("/user-config", c.set)
}

func (c *UserConfigController) get(w http.ResponseWriter, r *http.Request) {
	if c.Mgr == nil {
		apispec.NotImplemented(w, r, "GET", "/api/v1/user-config")
		return
	}
	cfg, err := c.Mgr.Get(r.Context())
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, UserConfigResponse{AgentConfig: cfg})
}

func (c *UserConfigController) set(w http.ResponseWriter, r *http.Request) {
	if c.Mgr == nil {
		apispec.NotImplemented(w, r, "PUT", "/api/v1/user-config")
		return
	}
	var in userconfigsvc.SetUserConfigInput
	if err := decodeJSONStrict(r, &in); err != nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_JSON", "Invalid JSON body", nil)
		return
	}
	cfg, err := c.Mgr.Set(r.Context(), in)
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, UserConfigResponse{AgentConfig: cfg})
}
