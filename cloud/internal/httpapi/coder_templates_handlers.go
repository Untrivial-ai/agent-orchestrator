package httpapi

import (
	"context"
	"net/http"
	"slices"

	"github.com/aoagents/agent-orchestrator/cloud/internal/sandbox"
	"github.com/aoagents/agent-orchestrator/cloud/internal/sandbox/coder"
	"github.com/go-chi/chi/v5"
)

// CoderTemplateLister lists the Coder templates a client may pick from. It is
// implemented by the coder provider client and is nil when the deployment does
// not offer the coder provider.
type CoderTemplateLister interface {
	ListTemplates(ctx context.Context) ([]coder.Template, error)
}

type coderTemplateResponse struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	DisplayName string   `json:"displayName"`
	Description string   `json:"description"`
	Icon        string   `json:"icon"`
	Parameters  []string `json:"parameters"`
}

// listCoderTemplates returns the Coder templates the picker offers for a new
// session, alongside the implicit "Default" (the deployment's configured
// template, which the client selects by sending no templateId). The list is
// empty when the deployment does not offer coder or the caller's organization
// is not entitled to it, so the picker simply shows "Default".
func (s *Server) listCoderTemplates(w http.ResponseWriter, r *http.Request) {
	orgID := chi.URLParam(r, "orgId")
	if requireUUID(orgID, "orgId") != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "orgId must be a UUID.")
		return
	}
	templates := []coderTemplateResponse{}
	entitled := slices.Contains(s.availableSandboxProviders, sandbox.ProviderCoder) &&
		s.orgAllowsProvider(principalFrom(r), sandbox.ProviderCoder)
	if entitled && s.coderTemplates != nil {
		raw, err := s.coderTemplates.ListTemplates(r.Context())
		if err != nil {
			s.logger.Error("list coder templates", "error", err, "request_id", requestID(r))
			writeError(w, r, http.StatusBadGateway, "coder_unavailable", "Could not load Coder templates.")
			return
		}
		for _, t := range raw {
			params := t.Parameters
			if params == nil {
				params = []string{}
			}
			templates = append(templates, coderTemplateResponse{
				ID:          t.ID,
				Name:        t.Name,
				DisplayName: t.DisplayName,
				Description: t.Description,
				Icon:        t.Icon,
				Parameters:  params,
			})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"templates": templates})
}
