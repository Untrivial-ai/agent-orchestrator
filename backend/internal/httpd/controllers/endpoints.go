package controllers

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apispec"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/envelope"
	"github.com/aoagents/agent-orchestrator/backend/internal/mobilebridge"
)

// EndpointSource reports the ways this daemon can currently be reached.
type EndpointSource interface {
	AdvertisedEndpoints() []mobilebridge.Endpoint
}

// EndpointsController serves the phone's endpoint refresh.
//
// It deliberately does not live under /api/v1/mobile: lanControlBlock 404s that
// entire prefix on the LAN listener, which is the only listener a phone can
// reach. Authenticated, unlike the identity probe, because the list describes
// the machine's network position rather than just naming it.
type EndpointsController struct {
	Source EndpointSource
}

// Register mounts the endpoints route on the supplied router.
func (c *EndpointsController) Register(r chi.Router) {
	r.Get("/endpoints", c.endpoints)
}

func (c *EndpointsController) endpoints(w http.ResponseWriter, r *http.Request) {
	if c.Source == nil {
		apispec.NotImplemented(w, r, "GET", "/api/v1/endpoints")
		return
	}
	endpoints := c.Source.AdvertisedEndpoints()
	if endpoints == nil {
		// An empty list is meaningful — no network — and must serialise as [].
		endpoints = []mobilebridge.Endpoint{}
	}
	envelope.WriteJSON(w, http.StatusOK, EndpointsResponse{Endpoints: endpoints})
}
