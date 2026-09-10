package controllers

import (
	"context"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apispec"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/envelope"
	settingssvc "github.com/aoagents/agent-orchestrator/backend/internal/service/settings"
)

// SettingsService is the controller-facing preferences contract.
type SettingsService interface {
	Get(ctx context.Context) (settingssvc.Snapshot, error)
	SetDefaultSessionMode(ctx context.Context, mode domain.SessionMode) (settingssvc.Snapshot, error)
	SetCloudOffering(ctx context.Context, enabled bool) (settingssvc.Snapshot, error)
	ChatHarnesses(candidates []domain.AgentHarness) []domain.AgentHarness
	Offering() settingssvc.Offering
}

// SettingsController owns the daemon-owned preference routes.
//
// These are daemon-owned rather than renderer-owned on purpose: desktop, mobile,
// and the CLI all resolve the same value, so a preference held in one client would
// disagree with the others.
type SettingsController struct {
	Svc SettingsService
}

// Register mounts the settings routes.
func (c *SettingsController) Register(r chi.Router) {
	r.Get("/settings", c.get)
	r.Patch("/settings/session-interface", c.setSessionInterface)
	r.Patch("/settings/cloud-offering", c.setCloudOffering)
}

func (c *SettingsController) get(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "GET", "/api/v1/settings")
		return
	}
	snapshot, err := c.Svc.Get(r.Context())
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, c.response(snapshot))
}

func (c *SettingsController) setSessionInterface(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "PATCH", "/api/v1/settings/session-interface")
		return
	}
	var req UpdateSessionInterfaceRequest
	if !decodeConversationBody(w, r, &req) {
		return
	}

	// Parsed strictly: an unrecognized value is rejected rather than collapsing to
	// a default the caller did not ask for.
	mode, err := domain.ParseSessionMode(req.DefaultSessionMode)
	if err != nil || mode == "" {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "validation",
			"SESSION_MODE_INVALID", `defaultSessionMode must be "chat" or "tui"`, nil)
		return
	}

	snapshot, err := c.Svc.SetDefaultSessionMode(r.Context(), mode)
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, c.response(snapshot))
}

func (c *SettingsController) setCloudOffering(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "PATCH", "/api/v1/settings/cloud-offering")
		return
	}
	var req UpdateCloudOfferingRequest
	if !decodeConversationBody(w, r, &req) {
		return
	}
	if req.Enabled == nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "validation",
			"CLOUD_OFFERING_INVALID", "enabled must be true or false", nil)
		return
	}
	snapshot, err := c.Svc.SetCloudOffering(r.Context(), *req.Enabled)
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, c.response(snapshot))
}

func (c *SettingsController) response(snapshot settingssvc.Snapshot) SettingsResponse {
	// Reported so the client can warn that choosing chat narrows which agents are
	// available, instead of letting the user discover it at spawn time.
	chatHarnesses := c.Svc.ChatHarnesses(domain.AllHarnesses)
	names := make([]string, 0, len(chatHarnesses))
	for _, harness := range chatHarnesses {
		names = append(names, string(harness))
	}
	offering := c.Svc.Offering()
	return SettingsResponse{
		DefaultSessionMode:   string(snapshot.DefaultSessionMode),
		ChatHarnesses:        names,
		Client:               offering.Client,
		LocalEnabled:         offering.LocalEnabled,
		CloudOffering:        snapshot.CloudOffering,
		CloudEnabled:         offering.CloudEnabled(snapshot),
		CloudControlPlaneURL: offering.CloudControlPlaneURL,
	}
}
