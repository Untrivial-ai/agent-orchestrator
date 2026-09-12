package httpapi

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/aoagents/agent-orchestrator/cloud/internal/auth"
	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/aoagents/agent-orchestrator/cloud/internal/githubapp"
	"github.com/aoagents/agent-orchestrator/cloud/internal/postgres"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

type capabilityRequest struct {
	Capability           string `json:"capability"`
	GitHubInstallationID string `json:"githubInstallationId"`
	GitHubRepositoryID   string `json:"githubRepositoryId"`
	UserExternalID       string `json:"userExternalId"`
}

type createScratchCapabilityRequest struct {
	DisplayName          string `json:"displayName"`
	GitHubInstallationID string `json:"githubInstallationId"`
	Private              *bool  `json:"private,omitempty"`
	TargetEnvironment    string `json:"targetEnvironment"`
	Orchestrator         struct {
		Harness string `json:"harness"`
		Prompt  string `json:"prompt,omitempty"`
	} `json:"orchestrator"`
}

type environmentScratchProjectStore interface {
	CreateGitHubScratchProject(
		context.Context,
		domain.Principal,
		string,
		string,
		int,
		domain.CreateGitHubScratchProject,
	) (domain.Project, domain.Session, error)
}

type capabilityValidator interface {
	ValidateCapability(
		context.Context,
		string,
		int64,
		int64,
		string,
	) (domain.GitHubRepositoryCapability, error)
}

func (s *Server) createGitHubScratchCapability(
	w http.ResponseWriter,
	r *http.Request,
) {
	orgID := chi.URLParam(r, "orgId")
	key, err := idempotencyKey(r)
	if requireUUID(orgID, "orgId") != nil || err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "A valid organization and idempotency key are required.")
		return
	}
	var request createScratchCapabilityRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "The request body is invalid.")
		return
	}
	installationID, err := strconv.ParseInt(
		strings.TrimSpace(request.GitHubInstallationID),
		10,
		64,
	)
	request.DisplayName = strings.TrimSpace(request.DisplayName)
	request.TargetEnvironment = strings.TrimSpace(request.TargetEnvironment)
	request.Orchestrator.Harness = strings.TrimSpace(request.Orchestrator.Harness)
	if err != nil || installationID <= 0 ||
		request.DisplayName == "" || len(request.DisplayName) > 80 ||
		request.Orchestrator.Harness == "" ||
		len(request.Orchestrator.Harness) > 120 ||
		len(request.Orchestrator.Prompt) > 65536 ||
		(request.TargetEnvironment != "development" &&
			request.TargetEnvironment != "staging" &&
			request.TargetEnvironment != "production") {
		writeError(w, r, http.StatusUnprocessableEntity, "validation_error", "The scratch capability request is invalid.")
		return
	}
	private := true
	if request.Private != nil {
		private = *request.Private
	}
	requestBinding, err := json.Marshal(request)
	if err != nil {
		writeError(w, r, http.StatusUnprocessableEntity, "validation_error", "The scratch capability request is invalid.")
		return
	}
	grant, err := s.github.PrepareScratchCapability(
		r.Context(),
		principalFrom(r),
		orgID,
		key,
		request.TargetEnvironment,
		installationID,
		request.DisplayName,
		private,
		requestBinding,
	)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusCreated, map[string]any{
		"capability": grant.Capability,
		"githubInstallationId": strconv.FormatInt(
			grant.Authority.GitHubInstallationID,
			10,
		),
		"githubRepositoryId": strconv.FormatInt(
			grant.Authority.GitHubRepositoryID,
			10,
		),
		"userExternalId":    grant.Authority.UserExternalID,
		"targetEnvironment": grant.Authority.TargetEnvironment,
		"repository": githubapp.ToBrokerRepository(
			grant.Authority.Repository,
		),
	})
}

func (s *Server) authenticateBrokerUser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !constantTimeBearer(
			r.Header.Get("Authorization"),
			s.brokerAuthToken,
		) {
			writeError(w, r, http.StatusUnauthorized, "unauthorized", "Control-plane authentication is required.")
			return
		}
		userAuthorization := strings.TrimSpace(
			r.Header.Get("X-AO-User-Authorization"),
		)
		scheme, userToken, ok := strings.Cut(userAuthorization, " ")
		if !ok || !strings.EqualFold(scheme, "Bearer") ||
			strings.TrimSpace(userToken) == "" {
			writeError(w, r, http.StatusUnauthorized, "unauthorized", "User authentication is required.")
			return
		}
		principal, err := s.principalForBearer(
			r.Context(),
			strings.TrimSpace(userToken),
		)
		if err != nil {
			if errors.Is(err, postgres.ErrNotFound) ||
				errors.Is(err, auth.ErrInvalidToken) {
				writeError(w, r, http.StatusUnauthorized, "unauthorized", "The user credential is invalid or expired.")
				return
			}
			writeError(w, r, http.StatusServiceUnavailable, "authentication_unavailable", "Authentication is temporarily unavailable.")
			return
		}
		ctx := context.WithValue(r.Context(), principalKey, principal)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (s *Server) revokeGitHubScratchCapability(
	w http.ResponseWriter,
	r *http.Request,
) {
	orgID := chi.URLParam(r, "orgId")
	if requireUUID(orgID, "orgId") != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "orgId must be a UUID.")
		return
	}
	var request struct {
		Capability string `json:"capability"`
	}
	if err := decodeJSON(w, r, &request); err != nil ||
		strings.TrimSpace(request.Capability) == "" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "A capability is required.")
		return
	}
	if err := s.github.RevokeScratchCapability(
		r.Context(),
		principalFrom(r),
		orgID,
		request.Capability,
	); err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) validateRepositoryCapability(
	w http.ResponseWriter,
	r *http.Request,
) {
	request, installationID, repositoryID, target, ok :=
		s.authorizedCapabilityRequest(w, r)
	if !ok {
		return
	}
	authority, err := s.github.ValidateRepositoryCapability(
		r.Context(),
		request.Capability,
		target,
		installationID,
		repositoryID,
		request.UserExternalID,
	)
	if errors.Is(err, postgres.ErrForbidden) ||
		errors.Is(err, postgres.ErrNotFound) {
		writeError(w, r, http.StatusForbidden, "CAPABILITY_INVALID", "The repository capability is invalid or revoked.")
		return
	}
	if err != nil {
		s.logger.Error("validate repository capability", "error", err, "request_id", requestID(r))
		writeError(w, r, http.StatusServiceUnavailable, "CAPABILITY_UNAVAILABLE", "Repository authority is unavailable.")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{
		"githubInstallationId": strconv.FormatInt(authority.GitHubInstallationID, 10),
		"githubRepositoryId":   strconv.FormatInt(authority.GitHubRepositoryID, 10),
		"userExternalId":       authority.UserExternalID,
		"targetEnvironment":    authority.TargetEnvironment,
		"repository":           githubapp.ToBrokerRepository(authority.Repository),
	})
}

func (s *Server) redeemRepositoryCapability(
	w http.ResponseWriter,
	r *http.Request,
) {
	request, installationID, repositoryID, target, ok :=
		s.authorizedCapabilityRequest(w, r)
	if !ok {
		return
	}
	grant, err := s.github.RedeemRepositoryCapability(
		r.Context(),
		request.Capability,
		target,
		installationID,
		repositoryID,
		request.UserExternalID,
	)
	if errors.Is(err, postgres.ErrForbidden) ||
		errors.Is(err, postgres.ErrNotFound) {
		writeError(w, r, http.StatusForbidden, "CAPABILITY_INVALID", "The repository capability is invalid or revoked.")
		return
	}
	if err != nil {
		s.logger.Error("redeem repository capability", "error", err, "request_id", requestID(r))
		writeError(w, r, http.StatusBadGateway, "SCM_BROKER_FAILED", "A repository checkout grant could not be issued.")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, grant)
}

func (s *Server) authorizedCapabilityRequest(
	w http.ResponseWriter,
	r *http.Request,
) (capabilityRequest, int64, int64, string, bool) {
	if !constantTimeBearer(r.Header.Get("Authorization"), s.brokerAuthToken) {
		writeError(w, r, http.StatusUnauthorized, "unauthorized", "Control-plane authentication is required.")
		return capabilityRequest{}, 0, 0, "", false
	}
	target := strings.TrimSpace(r.Header.Get("X-AO-Target-Environment"))
	if target == "" {
		writeError(w, r, http.StatusForbidden, "CAPABILITY_INVALID", "The repository capability is invalid or revoked.")
		return capabilityRequest{}, 0, 0, "", false
	}
	var request capabilityRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "The request body is invalid.")
		return capabilityRequest{}, 0, 0, "", false
	}
	installationID, installationErr := strconv.ParseInt(
		request.GitHubInstallationID,
		10,
		64,
	)
	repositoryID, repositoryErr := strconv.ParseInt(
		request.GitHubRepositoryID,
		10,
		64,
	)
	if installationErr != nil || repositoryErr != nil ||
		installationID <= 0 || repositoryID <= 0 ||
		strings.TrimSpace(request.Capability) == "" ||
		strings.TrimSpace(request.UserExternalID) == "" {
		writeError(w, r, http.StatusForbidden, "CAPABILITY_INVALID", "The repository capability is invalid or revoked.")
		return capabilityRequest{}, 0, 0, "", false
	}
	return request, installationID, repositoryID, target, true
}

func (s *Server) createEnvironmentScratchProject(
	w http.ResponseWriter,
	r *http.Request,
) {
	if !constantTimeBearer(
		r.Header.Get("Authorization"),
		s.environmentControlToken,
	) {
		writeError(w, r, http.StatusUnauthorized, "unauthorized", "Control-plane authentication is required.")
		return
	}
	target := strings.TrimSpace(r.Header.Get("X-AO-Target-Environment"))
	if target != s.environment {
		writeError(w, r, http.StatusForbidden, "CAPABILITY_INVALID", "The repository capability is invalid or revoked.")
		return
	}
	userAuthorization := strings.TrimSpace(
		r.Header.Get("X-AO-User-Authorization"),
	)
	scheme, userToken, ok := strings.Cut(userAuthorization, " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") ||
		strings.TrimSpace(userToken) == "" {
		writeError(w, r, http.StatusUnauthorized, "unauthorized", "User authentication is required.")
		return
	}
	principal, err := s.principalForBearer(
		r.Context(),
		strings.TrimSpace(userToken),
	)
	if err != nil {
		if errors.Is(err, postgres.ErrNotFound) ||
			errors.Is(err, auth.ErrInvalidToken) {
			writeError(w, r, http.StatusUnauthorized, "unauthorized", "The user credential is invalid or expired.")
			return
		}
		writeError(w, r, http.StatusServiceUnavailable, "authentication_unavailable", "Authentication is temporarily unavailable.")
		return
	}
	key, err := idempotencyKey(r)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	var request struct {
		OrgID                string         `json:"orgId"`
		Capability           string         `json:"capability"`
		GitHubInstallationID string         `json:"githubInstallationId"`
		GitHubRepositoryID   string         `json:"githubRepositoryId"`
		UserExternalID       string         `json:"userExternalId"`
		DisplayName          string         `json:"displayName"`
		Config               map[string]any `json:"config,omitempty"`
		Orchestrator         struct {
			Harness string `json:"harness"`
			Prompt  string `json:"prompt,omitempty"`
		} `json:"orchestrator"`
	}
	if err := decodeJSON(w, r, &request); err != nil ||
		requireUUID(request.OrgID, "orgId") != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "The request body is invalid.")
		return
	}
	installationID, installationErr := strconv.ParseInt(request.GitHubInstallationID, 10, 64)
	repositoryID, repositoryErr := strconv.ParseInt(request.GitHubRepositoryID, 10, 64)
	request.DisplayName = strings.TrimSpace(request.DisplayName)
	request.Orchestrator.Harness = strings.TrimSpace(request.Orchestrator.Harness)
	if installationErr != nil || repositoryErr != nil ||
		installationID <= 0 || repositoryID <= 0 ||
		request.DisplayName == "" || len(request.DisplayName) > 80 ||
		request.Orchestrator.Harness == "" ||
		len(request.Orchestrator.Harness) > 120 ||
		len(request.Orchestrator.Prompt) > 65536 {
		writeError(w, r, http.StatusUnprocessableEntity, "validation_error", "The scratch project request is invalid.")
		return
	}
	validator, validatorOK := s.checkoutBroker.(capabilityValidator)
	if !validatorOK || s.secretCipher == nil {
		writeError(w, r, http.StatusServiceUnavailable, "CAPABILITY_UNAVAILABLE", "Repository authority is unavailable.")
		return
	}
	authority, err := validator.ValidateCapability(
		r.Context(),
		request.Capability,
		installationID,
		repositoryID,
		request.UserExternalID,
	)
	if errors.Is(err, githubapp.ErrCapabilityRejected) {
		writeError(w, r, http.StatusForbidden, "CAPABILITY_INVALID", "The repository capability is invalid or revoked.")
		return
	}
	if err != nil {
		s.logger.Error("validate remote repository capability", "error", err, "request_id", requestID(r))
		writeError(w, r, http.StatusServiceUnavailable, "CAPABILITY_UNAVAILABLE", "Repository authority is unavailable.")
		return
	}
	projectID := uuid.NewString()
	authorization := domain.RemoteGitHubCheckoutContext{
		OrgID:                request.OrgID,
		ProjectID:            projectID,
		GitHubInstallationID: authority.GitHubInstallationID,
		GitHubRepositoryID:   authority.GitHubRepositoryID,
		UserExternalID:       authority.UserExternalID,
		TargetEnvironment:    authority.TargetEnvironment,
	}
	ciphertext, nonce, err := s.secretCipher.Encrypt(
		[]byte(request.Capability),
		githubapp.RepositoryCapabilityAssociatedData(authorization),
	)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "internal_error", "Repository authority could not be persisted.")
		return
	}
	config := request.Config
	if config == nil {
		config = map[string]any{"source": "scratch"}
	}
	configJSON, err := json.Marshal(config)
	if err != nil {
		writeError(w, r, http.StatusUnprocessableEntity, "validation_error", "Project configuration is invalid.")
		return
	}
	plan, err := s.provisioning.SessionPlan(request.Orchestrator.Harness)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "internal_error", "Sandbox provisioning is misconfigured.")
		return
	}
	store, ok := s.store.(environmentScratchProjectStore)
	if !ok {
		writeError(w, r, http.StatusNotImplemented, "not_implemented", "GitHub scratch projects are unavailable.")
		return
	}
	capabilityHash := sha256.Sum256([]byte(request.Capability))
	project, session, err := store.CreateGitHubScratchProject(
		r.Context(),
		principal,
		request.OrgID,
		key,
		s.maxSandboxes,
		domain.CreateGitHubScratchProject{
			ProjectID:               projectID,
			Repository:              authority.Repository,
			GitHubInstallationID:    authority.GitHubInstallationID,
			AuthorityUserExternalID: authority.UserExternalID,
			AuthorityEnvironment:    authority.TargetEnvironment,
			CapabilityHash:          capabilityHash[:],
			CapabilityCiphertext:    ciphertext,
			CapabilityNonce:         nonce,
			DisplayName:             request.DisplayName,
			Config:                  configJSON,
			Session: domain.CreateSession{
				Kind:             "orchestrator",
				Harness:          request.Orchestrator.Harness,
				DisplayName:      "Orchestrator",
				Prompt:           request.Orchestrator.Prompt,
				Mode:             "trusted",
				DeniedCommands:   []string{},
				Provider:         plan.Provider,
				ResourceProfile:  plan.ResourceProfile,
				BootstrapContext: plan.BootstrapContext,
				Release:          s.release,
			},
		},
	)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusCreated, map[string]any{
		"project":    toProjectResponse(project),
		"repository": toGitHubRepositoryResponse(authority.Repository),
		"session":    toSessionResponse(session, nil),
	})
}

func constantTimeBearer(header, expected string) bool {
	scheme, token, ok := strings.Cut(strings.TrimSpace(header), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") ||
		expected == "" {
		return false
	}
	providedHash := sha256.Sum256([]byte(strings.TrimSpace(token)))
	expectedHash := sha256.Sum256([]byte(expected))
	return subtle.ConstantTimeCompare(providedHash[:], expectedHash[:]) == 1
}
