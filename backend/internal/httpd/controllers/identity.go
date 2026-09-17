package controllers

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apispec"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/envelope"
)

// MobileAPIVersion is the contract version the phone negotiates against. Bump
// it when a change to the mobile-facing surface is not backward compatible.
const MobileAPIVersion = 1

// IdentityController serves the unauthenticated host-identity probe.
//
// The phone races several endpoints, and a private address is not an identity:
// 192.168.1.42 exists on most networks. The probe lets the phone confirm which
// machine answered before it presents a credential. Everything it returns is
// therefore non-secret by construction — an opaque host id and a version.
type IdentityController struct {
	HostID string
}

// Register mounts the identity route on the supplied router.
func (c *IdentityController) Register(r chi.Router) {
	r.Get("/identity", c.identity)
}

func (c *IdentityController) identity(w http.ResponseWriter, r *http.Request) {
	if c.HostID == "" {
		apispec.NotImplemented(w, r, "GET", "/api/v1/identity")
		return
	}
	envelope.WriteJSON(w, http.StatusOK, IdentityResponse{
		HostID:     c.HostID,
		APIVersion: MobileAPIVersion,
	})
}
