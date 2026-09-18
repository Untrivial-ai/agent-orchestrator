package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/go-chi/chi/v5"
)

const defaultAgentConnectionLabel = "default"

const githubPATProvider = "github"

var validAgentsList = []string{"claude-code", "codex", "cursor"}

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

type putAgentConnectionRequest struct {
	CredentialType string `json:"credentialType"`
	Secret         string `json:"secret"`
}

type putGitHubPATRequest struct {
	Secret string `json:"secret"`
}

type validateSavedRepositoryRequest struct {
	RepositoryURL string `json:"repositoryUrl"`
}

type validateSavedRepositoryResponse struct {
	WriteAccess bool `json:"writeAccess"`
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

func (s *Server) listAvailableAgents(w http.ResponseWriter, r *http.Request) {
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
	type agent struct {
		ID              string `json:"id"`
		Provider        string `json:"provider"`
		HasValidCred    bool   `json:"hasValidCred"`
		ValidationState string `json:"validationState"`
	}
	availableAgents := []agent{}
	for _, provider := range validAgentsList {
		hasValid := agentConnectionAvailable(connections, provider)
		state := "not_configured"
		for _, conn := range connections {
			if conn.Provider == provider && conn.Label == defaultAgentConnectionLabel {
				state = conn.ValidationState
				break
			}
		}
		availableAgents = append(availableAgents, agent{
			ID:              provider,
			Provider:        provider,
			HasValidCred:    hasValid,
			ValidationState: state,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"agents": availableAgents})
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
	// Codex owns its refreshable auth document. Preserve its opaque bytes; the
	// normal token normalization would corrupt JSON string values.
	secret := normalizeAgentCredentialSecret(request.Secret)
	if agent == "codex" && request.CredentialType == "auth_json" {
		secret = []byte(request.Secret)
	}
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

// putGitHubPAT stores the caller's GitHub personal access token for private
// repository checkout. The token is checked against GitHub before it is
// stored — a bad token is rejected here, not surfaced later as an opaque
// checkout failure inside a sandbox — and it is never echoed or logged.
func (s *Server) putGitHubPAT(w http.ResponseWriter, r *http.Request) {
	if s.secretCipher == nil || s.credentialValidator == nil {
		writeError(w, r, http.StatusServiceUnavailable, "provider_connections_unavailable", "GitHub credential storage is not configured.")
		return
	}
	var request putGitHubPATRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "The request body is invalid.")
		return
	}
	secret := []byte(strings.TrimSpace(request.Secret))
	defer clear(secret)
	request.Secret = ""
	if len(secret) < 8 || len(secret) > 64<<10 {
		writeError(w, r, http.StatusUnprocessableEntity, "validation_error", "The GitHub personal access token is invalid.")
		return
	}
	if err := s.credentialValidator.Validate(r.Context(), githubPATProvider, "personal_access_token", secret); err != nil {
		if errors.Is(err, errInvalidAgentCredential) {
			writeError(w, r, http.StatusUnprocessableEntity, "invalid_credential", "This GitHub token doesn't work — check it hasn't expired or been revoked.")
			return
		}
		s.logger.Warn("validate GitHub personal access token", "error", err, "request_id", requestID(r))
		writeError(w, r, http.StatusBadGateway, "provider_unavailable", "GitHub could not be reached to check this token.")
		return
	}
	principal := principalFrom(r)
	encrypted, nonce, err := s.secretCipher.Encrypt(secret, providerSecretAssociatedData("user:"+principal.UserID, githubPATProvider))
	if err != nil {
		s.logger.Error("encrypt GitHub personal access token", "error", err, "request_id", requestID(r))
		writeError(w, r, http.StatusInternalServerError, "internal_error", "The GitHub token could not be stored.")
		return
	}
	store, ok := s.store.(userProviderConnectionStore)
	if !ok {
		writeError(w, r, http.StatusNotImplemented, "not_implemented", "Provider connections are unavailable.")
		return
	}
	config, err := json.Marshal(map[string]string{"credentialType": "personal_access_token"})
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "internal_error", "The GitHub token could not be stored.")
		return
	}
	connection, err := store.UpsertUserProviderConnection(r.Context(), principal, githubPATProvider, defaultAgentConnectionLabel, encrypted, nonce, config)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"providerConnection": toUserProviderConnectionResponse(connection)})
}

func (s *Server) deleteGitHubPAT(w http.ResponseWriter, r *http.Request) {
	store, ok := s.store.(userProviderConnectionStore)
	if !ok {
		writeError(w, r, http.StatusNotImplemented, "not_implemented", "Provider connections are unavailable.")
		return
	}
	if err := store.DeleteUserProviderConnection(r.Context(), principalFrom(r), githubPATProvider, defaultAgentConnectionLabel); err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) validateSavedRepository(w http.ResponseWriter, r *http.Request) {
	if s.secretCipher == nil {
		writeError(w, r, http.StatusServiceUnavailable, "provider_connections_unavailable", "GitHub credential storage is not configured.")
		return
	}
	var request validateSavedRepositoryRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "The request body is invalid.")
		return
	}
	request.RepositoryURL = strings.TrimSpace(request.RepositoryURL)
	if request.RepositoryURL == "" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "Repository URL is required.")
		return
	}

	principal := principalFrom(r)
	store, ok := s.store.(userProviderConnectionStore)
	if !ok {
		writeError(w, r, http.StatusNotImplemented, "not_implemented", "Provider connections are unavailable.")
		return
	}

	encrypted, nonce, err := store.UserProviderConnectionSecret(r.Context(), principal, githubPATProvider, defaultAgentConnectionLabel)
	if err != nil {
		s.logger.Error("fetch GitHub personal access token", "error", err, "request_id", requestID(r))
		writeError(w, r, http.StatusUnprocessableEntity, "token_missing", "No GitHub personal access token found. Please add one first.")
		return
	}

	secret, err := s.secretCipher.Decrypt(encrypted, nonce, providerSecretAssociatedData("user:"+principal.UserID, githubPATProvider))
	if err != nil {
		s.logger.Error("decrypt GitHub personal access token", "error", err, "request_id", requestID(r))
		writeError(w, r, http.StatusInternalServerError, "internal_error", "Failed to decrypt the GitHub token.")
		return
	}
	defer clear(secret)

	reachable, writeAccess, err := s.probeRepositoryAccess(r.Context(), request.RepositoryURL, string(secret))
	if err != nil {
		s.logger.Error("probe repository access", "error", err, "request_id", requestID(r))
		writeError(w, r, http.StatusBadGateway, "provider_unavailable", "GitHub is temporarily unavailable. Please try again later.")
		return
	}
	if !reachable {
		writeError(w, r, http.StatusUnprocessableEntity, "repository_unreachable", "Can't reach this repository - it may be private, or the URL may be wrong.")
		return
	}

	writeJSON(w, http.StatusOK, validateSavedRepositoryResponse{
		WriteAccess: writeAccess,
	})
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
	UserProviderConnectionSecret(context.Context, domain.Principal, string, string) ([]byte, []byte, error)
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
	if agent == "codex" && request.CredentialType == "auth_json" {
		secret = []byte(request.Secret)
	}
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
	if !validAgentProvider(agent) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "The coding agent is invalid.")
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
	for _, valid := range validAgentsList {
		if agent == valid {
			return true
		}
	}
	return false
}

func validAgentCredentialType(agent, credentialType string) bool {
	switch agent {
	case "claude-code":
		return credentialType == "api_key" || credentialType == "oauth_token"
	case "codex":
		return credentialType == "api_key" || credentialType == "access_token" || credentialType == "auth_json"
	case "cursor":
		return credentialType == "api_key"
	default:
		return false
	}
}

func providerSecretAssociatedData(orgID, provider string) string {
	return orgID + "|" + provider + "|" + defaultAgentConnectionLabel
}
