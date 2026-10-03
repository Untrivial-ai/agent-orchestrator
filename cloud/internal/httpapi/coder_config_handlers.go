package httpapi

import (
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/aoagents/agent-orchestrator/cloud/internal/sandbox"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// awsRegionPattern is a loose AWS-region shape (e.g. eu-north-1). It is only used
// to reject obviously wrong input for the optional PrivateLink Region field; blank
// is always allowed.
var awsRegionPattern = regexp.MustCompile(`^[a-z]{2}-[a-z]+-\d+$`)

// putOrgCoderConfigRequest is the body for setting an organization's
// bring-your-own Coder connection. The token is a secret, kept out of the config
// JSONB and stored only as the connection's encrypted_secret.
type putOrgCoderConfigRequest struct {
	Token       string            `json:"token"`
	BaseURL     string            `json:"baseUrl"`
	Owner       string            `json:"owner"`
	TemplateID  string            `json:"templateId"`
	AgentName   string            `json:"agentName"`
	Parameters  map[string]string `json:"parameters"`
	DurableRoot string            `json:"durableRoot"`
	// Optional PrivateLink coordinates for a Coder in a private VPC. Non-secret;
	// stored in the config JSONB and left blank for a directly reachable Coder.
	EndpointServiceName string `json:"endpointServiceName"`
	Region              string `json:"region"`
}

// orgCoderConfigResponse is the secret-dropping view of an organization's Coder
// connection. It never carries the token — only the non-secret config plus the
// connection's validation metadata.
type orgCoderConfigResponse struct {
	Configured      bool                   `json:"configured"`
	CoderConfig     *domain.OrgCoderConfig `json:"coderConfig,omitempty"`
	ValidationState string                 `json:"validationState,omitempty"`
	ValidatedAt     *time.Time             `json:"validatedAt,omitempty"`
	UpdatedAt       *time.Time             `json:"updatedAt,omitempty"`
}

// getOrgCoderConfig returns an organization's bring-your-own Coder config. It
// reuses the list store (which never selects the encrypted secret), so the token
// cannot leak through this path.
func (s *Server) getOrgCoderConfig(w http.ResponseWriter, r *http.Request) {
	orgID := chi.URLParam(r, "orgId")
	if requireUUID(orgID, "orgId") != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "orgId must be a UUID.")
		return
	}
	store, ok := s.store.(providerConnectionStore)
	if !ok {
		writeError(w, r, http.StatusNotImplemented, "not_implemented", "Provider connections are unavailable.")
		return
	}
	connections, err := store.ListProviderConnections(r.Context(), principalFrom(r), orgID)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	for _, connection := range connections {
		if connection.Provider != sandbox.ProviderCoder || connection.Label != defaultAgentConnectionLabel {
			continue
		}
		cfg, decodeErr := domain.DecodeOrgCoderConfig(connection.Config)
		if decodeErr != nil {
			s.logger.Error("decode organization coder config", "error", decodeErr, "request_id", requestID(r))
			writeError(w, r, http.StatusInternalServerError, "internal_error", "The organization's Coder configuration is invalid.")
			return
		}
		validatedAt := connection.ValidatedAt
		updatedAt := connection.UpdatedAt
		writeJSON(w, http.StatusOK, orgCoderConfigResponse{
			Configured:      true,
			CoderConfig:     &cfg,
			ValidationState: connection.ValidationState,
			ValidatedAt:     validatedAt,
			UpdatedAt:       &updatedAt,
		})
		return
	}
	writeJSON(w, http.StatusOK, orgCoderConfigResponse{Configured: false})
}

// putOrgCoderConfig stores an organization's bring-your-own Coder connection:
// the token is encrypted at rest, the non-secret fields live in the config
// JSONB. Writing requires the caller to be an org admin (enforced inside
// UpsertProviderConnection) and the organization to be entitled to the coder
// provider.
func (s *Server) putOrgCoderConfig(w http.ResponseWriter, r *http.Request) {
	orgID := chi.URLParam(r, "orgId")
	if requireUUID(orgID, "orgId") != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "orgId must be a UUID.")
		return
	}
	if s.secretCipher == nil {
		writeError(w, r, http.StatusServiceUnavailable, "provider_connections_unavailable", "Provider credential storage is not configured.")
		return
	}
	if !s.orgAllowsProvider(principalFrom(r), sandbox.ProviderCoder) {
		writeError(w, r, http.StatusForbidden, "provider_forbidden", "Your organization is not enabled for the Coder sandbox provider.")
		return
	}
	var request putOrgCoderConfigRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "The request body is invalid.")
		return
	}
	token := []byte(strings.TrimSpace(request.Token))
	defer clear(token)
	request.Token = ""
	if len(token) == 0 || len(token) > 64<<10 {
		writeError(w, r, http.StatusUnprocessableEntity, "validation_error", "A Coder API token is required.")
		return
	}
	// Normalize once through the domain codec: it trims every field and fills the
	// durable-root default, so validation and storage see the same canonical form.
	configJSON, err := domain.EncodeOrgCoderConfig(domain.OrgCoderConfig{
		BaseURL:             request.BaseURL,
		Owner:               request.Owner,
		TemplateID:          request.TemplateID,
		AgentName:           request.AgentName,
		Parameters:          request.Parameters,
		DurableRoot:         request.DurableRoot,
		EndpointServiceName: request.EndpointServiceName,
		Region:              request.Region,
	})
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "internal_error", "The Coder configuration could not be stored.")
		return
	}
	normalized, err := domain.DecodeOrgCoderConfig(configJSON)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "internal_error", "The Coder configuration could not be stored.")
		return
	}
	// Validate the non-secret contract exactly as the deployment default is
	// validated. The URL may be any absolute http(s) origin — an IP or host:port is
	// allowed on purpose (bring-your-own Coder is often reached privately); only
	// empty or malformed values are rejected, and public HTTPS is NOT forced. The
	// token TTL is deployment policy, so a placeholder satisfies the shared check.
	if err := (sandbox.CoderConfig{
		BaseURL:        normalized.BaseURL,
		Owner:          normalized.Owner,
		TemplateID:     normalized.TemplateID,
		AgentName:      normalized.AgentName,
		Parameters:     normalized.Parameters,
		DurableRoot:    normalized.DurableRoot,
		WorkerTokenTTL: time.Minute,
	}).Validate(); err != nil {
		writeError(w, r, http.StatusUnprocessableEntity, "validation_error", "The Coder configuration is invalid: "+err.Error())
		return
	}
	if _, err := uuid.Parse(normalized.TemplateID); err != nil {
		writeError(w, r, http.StatusUnprocessableEntity, "validation_error", "The Coder template ID must be a UUID.")
		return
	}
	// The PrivateLink fields are optional (blank for a directly reachable Coder).
	// When present, apply only a loose shape check — AO ops provisions the endpoint
	// off them, so the values are not consumed here and must not be over-constrained.
	if normalized.EndpointServiceName != "" &&
		(!strings.HasPrefix(normalized.EndpointServiceName, "com.amazonaws.vpce.") ||
			!strings.Contains(normalized.EndpointServiceName, "vpce-svc-")) {
		writeError(w, r, http.StatusUnprocessableEntity, "validation_error", "The VPC endpoint service name must look like com.amazonaws.vpce.<region>.vpce-svc-….")
		return
	}
	if normalized.Region != "" && !awsRegionPattern.MatchString(normalized.Region) {
		writeError(w, r, http.StatusUnprocessableEntity, "validation_error", "The AWS region must look like eu-north-1.")
		return
	}
	encrypted, nonce, err := s.secretCipher.Encrypt(token, providerSecretAssociatedData(orgID, sandbox.ProviderCoder))
	if err != nil {
		s.logger.Error("encrypt coder token", "error", err, "request_id", requestID(r))
		writeError(w, r, http.StatusInternalServerError, "internal_error", "The Coder token could not be stored.")
		return
	}
	store, ok := s.store.(providerConnectionStore)
	if !ok {
		writeError(w, r, http.StatusNotImplemented, "not_implemented", "Provider connections are unavailable.")
		return
	}
	connection, err := store.UpsertProviderConnection(
		r.Context(),
		principalFrom(r),
		orgID,
		sandbox.ProviderCoder,
		defaultAgentConnectionLabel,
		encrypted,
		nonce,
		configJSON,
	)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	validatedAt := connection.ValidatedAt
	updatedAt := connection.UpdatedAt
	writeJSON(w, http.StatusOK, orgCoderConfigResponse{
		Configured:      true,
		CoderConfig:     &normalized,
		ValidationState: connection.ValidationState,
		ValidatedAt:     validatedAt,
		UpdatedAt:       &updatedAt,
	})
}

// deleteOrgCoderConfig removes an organization's bring-your-own Coder connection.
// It mirrors deleteAgentConnection: the delete is admin-gated inside the store
// (requireOrgAdmin), and capability-gated here so an org that is not entitled to
// the coder provider can neither write nor clear a connection.
func (s *Server) deleteOrgCoderConfig(w http.ResponseWriter, r *http.Request) {
	orgID := chi.URLParam(r, "orgId")
	if requireUUID(orgID, "orgId") != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "orgId must be a UUID.")
		return
	}
	if !s.orgAllowsProvider(principalFrom(r), sandbox.ProviderCoder) {
		writeError(w, r, http.StatusForbidden, "provider_forbidden", "Your organization is not enabled for the Coder sandbox provider.")
		return
	}
	store, ok := s.store.(providerConnectionStore)
	if !ok {
		writeError(w, r, http.StatusNotImplemented, "not_implemented", "Provider connections are unavailable.")
		return
	}
	if err := store.DeleteProviderConnection(
		r.Context(),
		principalFrom(r),
		orgID,
		sandbox.ProviderCoder,
		defaultAgentConnectionLabel,
	); err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
