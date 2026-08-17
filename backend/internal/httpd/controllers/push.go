package controllers

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apispec"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/envelope"
	"github.com/aoagents/agent-orchestrator/backend/internal/mobilebridge"
)

// PushRegistry is the controller-facing push-device registry contract, satisfied
// by *mobilebridge.DeviceRegistry.
type PushRegistry interface {
	Upsert(dev mobilebridge.PushDevice) error
	UnregisterToken(token string) error
	Unpair(id string) error
}

// PushController owns the /push/devices routes: a paired phone registers (and
// unregisters) its Expo push token so the daemon's dispatcher can reach it. The
// routes live on the REST surface behind the shared Bearer auth, so only a phone
// holding the connection password can register.
type PushController struct {
	Registry PushRegistry
	// Now stamps CreatedAt/LastSeenAt; overridable in tests. Defaults to time.Now.
	Now func() time.Time
}

// Register mounts the push-device routes on the supplied router.
func (c *PushController) Register(r chi.Router) {
	r.Post("/push/devices", c.register)
	r.Delete("/push/devices/{token}", c.unregister)
	r.Delete("/push/pairings/{id}", c.unpair)
}

func (c *PushController) now() time.Time {
	if c.Now != nil {
		return c.Now().UTC()
	}
	return time.Now().UTC()
}

func (c *PushController) register(w http.ResponseWriter, r *http.Request) {
	if c.Registry == nil {
		apispec.NotImplemented(w, r, "POST", "/api/v1/push/devices")
		return
	}
	var req RegisterPushDeviceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_JSON", "Invalid JSON body", nil)
		return
	}
	// Token is optional: a row represents a paired phone, not a push
	// registration. An empty token means "no notifications yet" (permission not
	// granted, or a build that can't mint one); a non-empty one must still be a
	// well-formed Expo push token.
	if req.Token != "" && !mobilebridge.ValidPushToken(req.Token) {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_PUSH_TOKEN",
			"token must be a well-formed Expo push token", nil)
		return
	}
	// A registration has to identify a phone somehow. Exactly two shapes are
	// valid: a real installId (token optional), or a token from a build too old to
	// send one. With neither there is nothing to key a row on and nothing for a
	// later registration to merge with, so synthesizing an id would mint a
	// permanent row representing no device — and repeating the call would mint
	// another, growing the roster with phantoms.
	if req.InstallID == "" && req.Token == "" {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "MISSING_DEVICE_IDENTITY",
			"installId or token is required", nil)
		return
	}
	installID := req.InstallID
	if installID == "" {
		// Phone builds installed before install IDs existed don't send one. Synthesize
		// a legacy id rather than rejecting the registration; Upsert's token-adoption
		// path merges this row into the real one once that phone updates and sends a
		// genuine install ID. Mirrors LoadRegistry's migration of pre-existing rows.
		installID = mobilebridge.LegacyInstallIDPrefix + uuid.NewString()
	}
	now := c.now()
	dev := mobilebridge.PushDevice{
		InstallID:  installID,
		Token:      req.Token,
		Platform:   req.Platform,
		DeviceName: req.DeviceName,
		CreatedAt:  now,
		LastSeenAt: now,
	}
	if err := c.Registry.Upsert(dev); err != nil {
		envelope.WriteAPIError(w, r, http.StatusInternalServerError, "internal", "PUSH_REGISTER", err.Error(), nil)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, PushDeviceEnvelope{Device: PushDeviceResponse{
		Token:      dev.Token,
		Platform:   dev.Platform,
		DeviceName: dev.DeviceName,
		CreatedAt:  dev.CreatedAt,
		LastSeenAt: dev.LastSeenAt,
	}})
}

func (c *PushController) unregister(w http.ResponseWriter, r *http.Request) {
	if c.Registry == nil {
		apispec.NotImplemented(w, r, "DELETE", "/api/v1/push/devices/{token}")
		return
	}
	// chi decodes the percent-encoded token (the Expo token's [ ] brackets are
	// URL-encoded by the client). Deleting an unknown token is a clean no-op.
	token := chi.URLParam(r, "token")
	if err := c.Registry.UnregisterToken(token); err != nil {
		envelope.WriteAPIError(w, r, http.StatusInternalServerError, "internal", "PUSH_UNREGISTER", err.Error(), nil)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, UnregisterPushDeviceResponse{Token: token, Deleted: true})
}

// unpair handles DELETE /api/v1/push/pairings/{id}: the phone telling this
// daemon it is no longer paired ("Disconnect & forget server", or moving to
// another desktop). Distinct from unregister, which only detaches the push token
// and leaves the phone listed as notifications-off.
func (c *PushController) unpair(w http.ResponseWriter, r *http.Request) {
	if c.Registry == nil {
		apispec.NotImplemented(w, r, "DELETE", "/api/v1/push/pairings/{id}")
		return
	}
	// chi decodes the percent-encoded segment, so {id} arrives as either a plain
	// install ID or an Expo token with its brackets restored. Unpairing something
	// already gone is a clean no-op.
	id := chi.URLParam(r, "id")
	if err := c.Registry.Unpair(id); err != nil {
		envelope.WriteAPIError(w, r, http.StatusInternalServerError, "internal", "DEVICE_UNPAIR_FAILED",
			"Could not unpair the device", nil)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
