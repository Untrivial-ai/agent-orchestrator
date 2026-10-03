package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/aoagents/agent-orchestrator/cloud/internal/postgres"
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

// coderConnectionForServiceStore reads an organization's encrypted Coder
// connection without a request principal, so the template-list edge can build a
// per-organization lister from the org's own deployment. It is the same store
// method the sandbox resolver uses to provision per-org sessions.
type coderConnectionForServiceStore interface {
	CoderConnectionForService(
		ctx context.Context,
		orgID string,
	) (encrypted, nonce, config []byte, connectionID string, err error)
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
// template, which the client selects by sending no templateId). When the caller's
// organization brings its own Coder connection, the templates come from that
// deployment; otherwise they come from the shared, env-configured deployment. The
// list is empty when the deployment does not offer coder or the caller's
// organization is not entitled to it, so the picker simply shows "Default".
func (s *Server) listCoderTemplates(w http.ResponseWriter, r *http.Request) {
	orgID := chi.URLParam(r, "orgId")
	if requireUUID(orgID, "orgId") != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "orgId must be a UUID.")
		return
	}
	templates := []coderTemplateResponse{}
	entitled := slices.Contains(s.availableSandboxProviders, sandbox.ProviderCoder) &&
		s.orgAllowsProvider(principalFrom(r), sandbox.ProviderCoder)
	if entitled {
		lister, err := s.coderTemplateListerForOrg(r.Context(), orgID)
		if err != nil {
			s.logger.Error("resolve coder template lister", "error", err, "request_id", requestID(r))
			writeError(w, r, http.StatusBadGateway, "coder_unavailable", "Could not load Coder templates.")
			return
		}
		if lister != nil {
			raw, err := lister.ListTemplates(r.Context())
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
	}
	writeJSON(w, http.StatusOK, map[string]any{"templates": templates})
}

// coderTemplateListerForOrg picks the Coder template lister for one organization.
// It prefers the organization's own bring-your-own Coder connection — so the
// picker lists the templates on *their* deployment — and falls back to the shared
// deployment lister (which may be nil → an empty list, the pre-existing behavior)
// when the organization has no connection of its own. It reuses
// coder.NewForOrg, the same decrypt-and-build helper the sandbox resolver uses, so
// a per-org lister is constructed exactly as a per-org session is.
func (s *Server) coderTemplateListerForOrg(ctx context.Context, orgID string) (CoderTemplateLister, error) {
	if store, ok := s.store.(coderConnectionForServiceStore); ok && s.secretCipher != nil {
		encrypted, nonce, config, _, err := store.CoderConnectionForService(ctx, orgID)
		switch {
		case err == nil:
			cfg, decodeErr := domain.DecodeOrgCoderConfig(config)
			if decodeErr != nil {
				return nil, fmt.Errorf("decode organization coder config: %w", decodeErr)
			}
			client, buildErr := coder.NewForOrg(s.secretCipher, orgID, encrypted, nonce, coder.Config{
				BaseURL:    cfg.BaseURL,
				Owner:      cfg.Owner,
				TemplateID: cfg.TemplateID,
				AgentName:  cfg.AgentName,
				Parameters: cfg.Parameters,
			})
			if buildErr != nil {
				return nil, fmt.Errorf("build organization coder client: %w", buildErr)
			}
			return client, nil
		case errors.Is(err, postgres.ErrNotFound):
			// No per-organization connection: fall through to the deployment lister.
		default:
			return nil, fmt.Errorf("load organization coder connection: %w", err)
		}
	}
	// Deployment default. Nil when the deployment does not offer coder, which the
	// caller renders as an empty list.
	if s.coderTemplates == nil {
		return nil, nil
	}
	return s.coderTemplates, nil
}
