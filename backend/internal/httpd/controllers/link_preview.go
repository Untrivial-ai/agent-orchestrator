package controllers

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apispec"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/envelope"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/linkpreview"
)

// LinkPreviewService fetches external page metadata server-side. The renderer
// is CSP-locked to loopback, so unfurling a link must go through the daemon.
type LinkPreviewService interface {
	Preview(ctx context.Context, rawURL string) (linkpreview.Preview, error)
}

// LinkPreviewController serves the link unfurl endpoint.
type LinkPreviewController struct {
	Svc LinkPreviewService
}

// Register mounts the link-preview route on the supplied router.
func (c *LinkPreviewController) Register(r chi.Router) {
	r.Get("/link-preview", c.preview)
}

func (c *LinkPreviewController) preview(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, http.MethodGet, "/api/v1/link-preview")
		return
	}
	rawURL := strings.TrimSpace(r.URL.Query().Get("url"))
	if rawURL == "" {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "URL_REQUIRED", "url query parameter is required", nil)
		return
	}
	p, err := c.Svc.Preview(r.Context(), rawURL)
	if err != nil {
		// Upstream fetch failures are a bad gateway, which the apierr.Kind
		// vocabulary cannot express; render them here. Everything else is an
		// apierr and goes through the standard mapping (400 invalid URL, 404
		// non-HTML page).
		if errors.Is(err, linkpreview.ErrFetchFailed) {
			envelope.WriteAPIError(w, r, http.StatusBadGateway, "bad_gateway", "LINK_PREVIEW_FETCH_FAILED", "The page could not be fetched for a link preview", nil)
			return
		}
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, LinkPreviewResponse{
		URL:         p.URL,
		Title:       p.Title,
		Description: p.Description,
		ImageURL:    p.ImageURL,
		SiteName:    p.SiteName,
		FaviconURL:  p.FaviconURL,
	})
}
