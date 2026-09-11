package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/aoagents/agent-orchestrator/cloud/internal/sandbox"
	coderprovider "github.com/aoagents/agent-orchestrator/cloud/internal/sandbox/coder"
	"github.com/go-chi/chi/v5"
)

const defaultAgentConnectionLabel = "default"

type providerConnectionStore interface {
	ListProviderConnections(
		context.Context,
		domain.Principal,
		string,
	) ([]domain.ProviderConnection, error)
	UpsertProviderConnection(
		context.Context,
		domain.Principal,
		string,
		string,
		string,
		[]byte,
		[]byte,
		json.RawMessage,
	) (domain.ProviderConnection, error)
	DeleteProviderConnection(
		context.Context,
		domain.Principal,
		string,
		string,
		string,
	) error
}

type workerCredentialAvailabilityStore interface {
	AgentCredentialAvailable(context.Context, string, string) (bool, error)
	OrchestratorAgentCredentialAvailable(context.Context, string, string, string) (bool, error)
}

type userProviderCredentialStore interface {
	UserAgentCredentialAvailable(context.Context, string, string) (bool, error)
}

func agentConnectionAvailable(
	connections []domain.ProviderConnection,
	provider string,
) bool {
	for _, connection := range connections {
		if connection.Provider == provider &&
			connection.Label == defaultAgentConnectionLabel &&
			connection.ValidationState == "valid" {
			return true
		}
	}
	return false
}

type secretEncrypter interface {
	Encrypt([]byte, string) ([]byte, []byte, error)
}

type credentialValidator interface {
	Validate(context.Context, string, string, []byte) error
}

type coderConnectionInspector func(
	context.Context,
	string,
	string,
	string,
	*http.Client,
) (coderprovider.ConnectionIdentity, error)

type putAgentConnectionRequest struct {
	CredentialType string `json:"credentialType"`
	Secret         string `json:"secret"`
}

type putCoderConnectionRequest struct {
	BaseURL     string            `json:"baseUrl"`
	APIToken    string            `json:"apiToken"`
	TemplateID  string            `json:"templateId"`
	AgentName   string            `json:"agentName,omitempty"`
	Parameters  map[string]string `json:"parameters,omitempty"`
	DurableRoot string            `json:"durableRoot"`
}

type providerConnectionResponse struct {
	ID              string         `json:"id"`
	Provider        string         `json:"provider"`
	Label           string         `json:"label"`
	Config          map[string]any `json:"config"`
	ValidationState string         `json:"validationState"`
	ValidatedAt     *time.Time     `json:"validatedAt,omitempty"`
	CreatedAt       time.Time      `json:"createdAt"`
	UpdatedAt       time.Time      `json:"updatedAt"`
}

func (s *Server) listProviderConnections(w http.ResponseWriter, r *http.Request) {
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
	connections, err := store.ListProviderConnections(
		r.Context(),
		principalFrom(r),
		orgID,
	)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	items := make([]providerConnectionResponse, 0, len(connections))
	for _, connection := range connections {
		items = append(items, toProviderConnectionResponse(connection))
	}
	writeJSON(w, http.StatusOK, map[string]any{"providerConnections": items})
}

func (s *Server) putAgentConnection(w http.ResponseWriter, r *http.Request) {
	orgID := chi.URLParam(r, "orgId")
	agent := strings.TrimSpace(chi.URLParam(r, "agent"))
	if requireUUID(orgID, "orgId") != nil || !validAgentProvider(agent) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "The organization or coding agent is invalid.")
		return
	}
	if s.secretCipher == nil || s.credentialValidator == nil {
		writeError(w, r, http.StatusServiceUnavailable, "provider_connections_unavailable", "Provider credential storage is not configured.")
		return
	}
	var request putAgentConnectionRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "The request body is invalid.")
		return
	}
	request.CredentialType = strings.TrimSpace(request.CredentialType)
	secret := normalizeAgentCredentialSecret(request.Secret)
	defer clear(secret)
	request.Secret = ""
	if len(secret) == 0 || len(secret) > 64<<10 ||
		!validAgentCredentialType(agent, request.CredentialType) {
		writeError(w, r, http.StatusUnprocessableEntity, "validation_error", "The coding-agent credential is invalid.")
		return
	}
	if err := s.credentialValidator.Validate(
		r.Context(),
		agent,
		request.CredentialType,
		secret,
	); err != nil {
		if errors.Is(err, errInvalidAgentCredential) {
			writeError(w, r, http.StatusUnprocessableEntity, "invalid_credential", err.Error())
			return
		}
		s.logger.Warn(
			"validate coding-agent credential",
			"provider",
			agent,
			"error",
			err,
			"request_id",
			requestID(r),
		)
		writeError(w, r, http.StatusBadGateway, "provider_unavailable", "The credential provider could not be reached.")
		return
	}
	encrypted, nonce, err := s.secretCipher.Encrypt(
		secret,
		providerSecretAssociatedData(orgID, agent),
	)
	if err != nil {
		s.logger.Error("encrypt provider credential", "error", err, "request_id", requestID(r))
		writeError(w, r, http.StatusInternalServerError, "internal_error", "The credential could not be stored.")
		return
	}
	config, err := json.Marshal(map[string]string{
		"credentialType": request.CredentialType,
	})
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "internal_error", "The credential could not be stored.")
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
		agent,
		defaultAgentConnectionLabel,
		encrypted,
		nonce,
		config,
	)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"providerConnection": toProviderConnectionResponse(connection),
	})
}

func (s *Server) deleteAgentConnection(w http.ResponseWriter, r *http.Request) {
	orgID := chi.URLParam(r, "orgId")
	agent := strings.TrimSpace(chi.URLParam(r, "agent"))
	if requireUUID(orgID, "orgId") != nil || !validAgentProvider(agent) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "The organization or coding agent is invalid.")
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
		agent,
		defaultAgentConnectionLabel,
	); err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func toProviderConnectionResponse(
	connection domain.ProviderConnection,
) providerConnectionResponse {
	config := map[string]any{}
	if len(connection.Config) > 0 {
		_ = json.Unmarshal(connection.Config, &config)
	}
	return providerConnectionResponse{
		ID:              connection.ID,
		Provider:        connection.Provider,
		Label:           connection.Label,
		Config:          config,
		ValidationState: connection.ValidationState,
		ValidatedAt:     connection.ValidatedAt,
		CreatedAt:       connection.CreatedAt,
		UpdatedAt:       connection.UpdatedAt,
	}
}

type userProviderConnectionStore interface {
	ListUserProviderConnections(context.Context, domain.Principal) ([]domain.UserProviderConnection, error)
	UpsertUserProviderConnection(
		context.Context,
		domain.Principal,
		string,
		string,
		[]byte,
		[]byte,
		json.RawMessage,
	) (domain.UserProviderConnection, error)
	DeleteUserProviderConnection(context.Context, domain.Principal, string, string) error
}

type providerConnectionPromotionStore interface {
	ProviderConnectionSecretForPromotion(
		context.Context,
		domain.Principal,
		string,
		string,
		string,
	) ([]byte, []byte, json.RawMessage, error)
	UserAgentCredentialAvailable(context.Context, string, string) (bool, error)
	ListUserProviderConnections(context.Context, domain.Principal) ([]domain.UserProviderConnection, error)
	UpsertUserProviderConnection(
		context.Context,
		domain.Principal,
		string,
		string,
		[]byte,
		[]byte,
		json.RawMessage,
	) (domain.UserProviderConnection, error)
}

func toUserProviderConnectionResponse(
	connection domain.UserProviderConnection,
) providerConnectionResponse {
	config := map[string]any{}
	if len(connection.Config) > 0 {
		_ = json.Unmarshal(connection.Config, &config)
	}
	return providerConnectionResponse{
		ID:              connection.ID,
		Provider:        connection.Provider,
		Label:           connection.Label,
		Config:          config,
		ValidationState: connection.ValidationState,
		ValidatedAt:     connection.ValidatedAt,
		CreatedAt:       connection.CreatedAt,
		UpdatedAt:       connection.UpdatedAt,
	}
}

// listUserProviderConnections, putUserAgentConnection, and
// deleteUserAgentConnection are the personal counterpart to the org-scoped
// handlers above: a connection here is usable by its owner in every org
// they belong to, not just one, and there is no admin gate — it's the
// caller's own credential.
func (s *Server) listUserProviderConnections(w http.ResponseWriter, r *http.Request) {
	store, ok := s.store.(userProviderConnectionStore)
	if !ok {
		writeError(w, r, http.StatusNotImplemented, "not_implemented", "Provider connections are unavailable.")
		return
	}
	connections, err := store.ListUserProviderConnections(r.Context(), principalFrom(r))
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	items := make([]providerConnectionResponse, 0, len(connections))
	for _, connection := range connections {
		items = append(items, toUserProviderConnectionResponse(connection))
	}
	writeJSON(w, http.StatusOK, map[string]any{"providerConnections": items})
}

func (s *Server) putUserAgentConnection(w http.ResponseWriter, r *http.Request) {
	agent := strings.TrimSpace(chi.URLParam(r, "agent"))
	if agent == sandbox.ProviderCoder {
		s.putUserCoderConnection(w, r)
		return
	}
	if !validAgentProvider(agent) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "The coding agent is invalid.")
		return
	}
	if s.secretCipher == nil || s.credentialValidator == nil {
		writeError(w, r, http.StatusServiceUnavailable, "provider_connections_unavailable", "Provider credential storage is not configured.")
		return
	}
	var request putAgentConnectionRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "The request body is invalid.")
		return
	}
	request.CredentialType = strings.TrimSpace(request.CredentialType)
	secret := normalizeAgentCredentialSecret(request.Secret)
	defer clear(secret)
	request.Secret = ""
	if len(secret) == 0 || len(secret) > 64<<10 ||
		!validAgentCredentialType(agent, request.CredentialType) {
		writeError(w, r, http.StatusUnprocessableEntity, "validation_error", "The coding-agent credential is invalid.")
		return
	}
	principal := principalFrom(r)
	if err := s.credentialValidator.Validate(
		r.Context(),
		agent,
		request.CredentialType,
		secret,
	); err != nil {
		if errors.Is(err, errInvalidAgentCredential) {
			writeError(w, r, http.StatusUnprocessableEntity, "invalid_credential", err.Error())
			return
		}
		s.logger.Warn(
			"validate coding-agent credential",
			"provider", agent,
			"error", err,
			"request_id", requestID(r),
		)
		writeError(w, r, http.StatusBadGateway, "provider_unavailable", "The credential provider could not be reached.")
		return
	}
	encrypted, nonce, err := s.secretCipher.Encrypt(
		secret,
		providerSecretAssociatedData("user:"+principal.UserID, agent),
	)
	if err != nil {
		s.logger.Error("encrypt provider credential", "error", err, "request_id", requestID(r))
		writeError(w, r, http.StatusInternalServerError, "internal_error", "The credential could not be stored.")
		return
	}
	config, err := json.Marshal(map[string]string{
		"credentialType": request.CredentialType,
	})
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "internal_error", "The credential could not be stored.")
		return
	}
	store, ok := s.store.(userProviderConnectionStore)
	if !ok {
		writeError(w, r, http.StatusNotImplemented, "not_implemented", "Provider connections are unavailable.")
		return
	}
	connection, err := store.UpsertUserProviderConnection(
		r.Context(),
		principal,
		agent,
		defaultAgentConnectionLabel,
		encrypted,
		nonce,
		config,
	)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"providerConnection": toUserProviderConnectionResponse(connection),
	})
}

func (s *Server) deleteUserAgentConnection(w http.ResponseWriter, r *http.Request) {
	agent := strings.TrimSpace(chi.URLParam(r, "agent"))
	if !validAgentProvider(agent) && agent != sandbox.ProviderCoder {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "The provider is invalid.")
		return
	}
	store, ok := s.store.(userProviderConnectionStore)
	if !ok {
		writeError(w, r, http.StatusNotImplemented, "not_implemented", "Provider connections are unavailable.")
		return
	}
	if err := store.DeleteUserProviderConnection(
		r.Context(), principalFrom(r), agent, defaultAgentConnectionLabel,
	); err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) putUserCoderConnection(w http.ResponseWriter, r *http.Request) {
	if s.secretCipher == nil || s.coderInspector == nil {
		writeError(w, r, http.StatusServiceUnavailable, "provider_connections_unavailable", "Provider credential storage is not configured.")
		return
	}
	var request putCoderConnectionRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "The request body is invalid.")
		return
	}
	request.BaseURL = strings.TrimRight(strings.TrimSpace(request.BaseURL), "/")
	request.TemplateID = strings.TrimSpace(request.TemplateID)
	request.AgentName = strings.TrimSpace(request.AgentName)
	request.DurableRoot = strings.TrimSpace(request.DurableRoot)
	secret := []byte(strings.TrimSpace(request.APIToken))
	defer clear(secret)
	request.APIToken = ""
	config := sandbox.CoderConnectionConfig{
		BaseURL: request.BaseURL, TemplateID: request.TemplateID,
		AgentName: request.AgentName, Parameters: request.Parameters,
		DurableRoot: request.DurableRoot,
	}
	endpoint, endpointErr := url.Parse(config.BaseURL)
	if endpointErr != nil || endpoint.Scheme != "https" {
		writeError(w, r, http.StatusUnprocessableEntity, "validation_error", "Self-service Coder connections require an HTTPS URL.")
		return
	}
	if len(secret) == 0 || len(secret) > 64<<10 {
		writeError(w, r, http.StatusUnprocessableEntity, "validation_error", "The Coder API token is invalid.")
		return
	}
	// Reuse the provisioning validator so unsafe URLs, parameter names, and
	// durable paths are rejected before any network request or persistence.
	probeConfig := config.Config(time.Minute)
	probeConfig.Owner = "pending"
	if err := probeConfig.Validate(); err != nil {
		writeError(w, r, http.StatusUnprocessableEntity, "validation_error", err.Error())
		return
	}
	identity, err := s.coderInspector(
		r.Context(), config.BaseURL, string(secret), config.TemplateID, coderprovider.NewPublicHTTPClient(),
	)
	if err != nil {
		var apiError *coderprovider.APIError
		if errors.As(err, &apiError) && (apiError.StatusCode == http.StatusUnauthorized || apiError.StatusCode == http.StatusForbidden) {
			writeError(w, r, http.StatusUnprocessableEntity, "invalid_credential", "Coder rejected the API token or template access.")
			return
		}
		if errors.Is(err, sandbox.ErrNotFound) {
			writeError(w, r, http.StatusUnprocessableEntity, "invalid_template", "The Coder template was not found or is not accessible to this account.")
			return
		}
		s.logger.Warn("validate Coder connection", "error", err, "request_id", requestID(r))
		writeError(w, r, http.StatusBadGateway, "provider_unavailable", "The Coder deployment could not be validated.")
		return
	}
	config.Owner = identity.Owner
	encodedConfig, err := json.Marshal(config)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "internal_error", "The Coder connection could not be stored.")
		return
	}
	principal := principalFrom(r)
	encrypted, nonce, err := s.secretCipher.Encrypt(
		secret,
		providerSecretAssociatedData("user:"+principal.UserID, sandbox.ProviderCoder),
	)
	if err != nil {
		s.logger.Error("encrypt Coder credential", "error", err, "request_id", requestID(r))
		writeError(w, r, http.StatusInternalServerError, "internal_error", "The Coder connection could not be stored.")
		return
	}
	store, ok := s.store.(userProviderConnectionStore)
	if !ok {
		writeError(w, r, http.StatusNotImplemented, "not_implemented", "Provider connections are unavailable.")
		return
	}
	connection, err := store.UpsertUserProviderConnection(
		r.Context(), principal, sandbox.ProviderCoder, defaultAgentConnectionLabel,
		encrypted, nonce, encodedConfig,
	)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"providerConnection": toUserProviderConnectionResponse(connection),
	})
}

func (s *Server) promoteAgentConnection(w http.ResponseWriter, r *http.Request) {
	orgID := chi.URLParam(r, "orgId")
	agent := strings.TrimSpace(chi.URLParam(r, "agent"))
	if requireUUID(orgID, "orgId") != nil || !validAgentProvider(agent) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "The organization or coding agent is invalid.")
		return
	}
	if s.secretCipher == nil {
		writeError(w, r, http.StatusServiceUnavailable, "provider_connections_unavailable", "Provider credential storage is not configured.")
		return
	}
	store, ok := s.store.(providerConnectionPromotionStore)
	if !ok {
		writeError(w, r, http.StatusNotImplemented, "not_implemented", "Provider connections are unavailable.")
		return
	}
	principal := principalFrom(r)
	available, err := store.UserAgentCredentialAvailable(r.Context(), principal.UserID, agent)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	if available {
		connections, listErr := store.ListUserProviderConnections(r.Context(), principal)
		if listErr != nil {
			s.writeStoreError(w, r, listErr)
			return
		}
		for _, connection := range connections {
			if connection.Provider == agent && connection.Label == defaultAgentConnectionLabel {
				writeJSON(w, http.StatusOK, map[string]any{"providerConnection": toUserProviderConnectionResponse(connection)})
				return
			}
		}
		writeError(w, r, http.StatusConflict, "provider_connection_conflict", "The personal provider connection could not be resolved.")
		return
	}
	encrypted, nonce, config, err := store.ProviderConnectionSecretForPromotion(
		r.Context(), principal, orgID, agent, defaultAgentConnectionLabel,
	)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	secret, err := s.secretCipher.Decrypt(
		encrypted, nonce, providerSecretAssociatedData(orgID, agent),
	)
	if err != nil {
		s.logger.Error("decrypt provider credential for promotion", "error", err, "request_id", requestID(r))
		writeError(w, r, http.StatusInternalServerError, "internal_error", "The credential could not be promoted.")
		return
	}
	defer clear(secret)
	rewrapped, rewrappedNonce, err := s.secretCipher.Encrypt(
		secret, providerSecretAssociatedData("user:"+principal.UserID, agent),
	)
	if err != nil {
		s.logger.Error("encrypt promoted provider credential", "error", err, "request_id", requestID(r))
		writeError(w, r, http.StatusInternalServerError, "internal_error", "The credential could not be promoted.")
		return
	}
	connection, err := store.UpsertUserProviderConnection(
		r.Context(), principal, agent, defaultAgentConnectionLabel,
		rewrapped, rewrappedNonce, config,
	)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"providerConnection": toUserProviderConnectionResponse(connection)})
}

func validAgentProvider(agent string) bool {
	return agent == "claude-code" || agent == "codex" || agent == "cursor"
}

func validAgentCredentialType(agent, credentialType string) bool {
	switch agent {
	case "claude-code":
		return credentialType == "api_key" || credentialType == "oauth_token"
	case "codex":
		return credentialType == "api_key" || credentialType == "access_token"
	case "cursor":
		return credentialType == "api_key"
	default:
		return false
	}
}

func providerSecretAssociatedData(orgID, provider string) string {
	return orgID + "|" + provider + "|" + defaultAgentConnectionLabel
}
