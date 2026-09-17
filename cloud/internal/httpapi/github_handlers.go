package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/aoagents/agent-orchestrator/cloud/internal/postgres"
	"github.com/go-chi/chi/v5"
)

type githubInstallationResponse struct {
	ID                   string     `json:"id"`
	GitHubInstallationID string     `json:"githubInstallationId"`
	AccountLogin         string     `json:"accountLogin"`
	AccountType          string     `json:"accountType"`
	Status               string     `json:"status"`
	RepositorySelection  string     `json:"repositorySelection"`
	SyncStatus           string     `json:"syncStatus"`
	LastSyncedAt         *time.Time `json:"lastSyncedAt,omitempty"`
	LastError            string     `json:"lastError,omitempty"`
	CreatedAt            time.Time  `json:"createdAt"`
	UpdatedAt            time.Time  `json:"updatedAt"`
}

type githubRepositoryResponse struct {
	GitHubRepositoryID string     `json:"githubRepositoryId"`
	Name               string     `json:"name"`
	FullName           string     `json:"fullName"`
	HTMLURL            string     `json:"htmlUrl"`
	DefaultBranch      string     `json:"defaultBranch"`
	Visibility         string     `json:"visibility"`
	IsPrivate          bool       `json:"isPrivate"`
	IsArchived         bool       `json:"isArchived"`
	Access             string     `json:"access"`
	GrantedAt          time.Time  `json:"grantedAt"`
	RevokedAt          *time.Time `json:"revokedAt,omitempty"`
}

type createGitHubProjectRequest struct {
	GitHubRepositoryID string         `json:"githubRepositoryId"`
	DisplayName        string         `json:"displayName,omitempty"`
	Config             map[string]any `json:"config,omitempty"`
}

type githubUserConnectionResponse struct {
	Connected     bool                             `json:"connected"`
	Login         string                           `json:"login,omitempty"`
	AvatarURL     string                           `json:"avatarUrl,omitempty"`
	Installations []githubUserInstallationResponse `json:"installations"`
	LastSyncedAt  *time.Time                       `json:"lastSyncedAt,omitempty"`
}

type githubUserInstallationResponse struct {
	GitHubInstallationID string `json:"githubInstallationId"`
	AccountLogin         string `json:"accountLogin"`
	AccountType          string `json:"accountType"`
	RepositorySelection  string `json:"repositorySelection"`
	CanCreateRepository  bool   `json:"canCreateRepository"`
	UnavailableReason    string `json:"unavailableReason,omitempty"`
}

type claimGitHubInstallationRequest struct {
	GitHubInstallationID string `json:"githubInstallationId"`
}

type createGitHubScratchProjectRequest struct {
	DisplayName          string `json:"displayName"`
	GitHubInstallationID string `json:"githubInstallationId"`
	Private              *bool  `json:"private,omitempty"`
	Orchestrator         struct {
		Harness string `json:"harness"`
		Prompt  string `json:"prompt,omitempty"`
	} `json:"orchestrator"`
}

type githubProjectStore interface {
	CreateGitHubProject(
		context.Context,
		domain.Principal,
		string,
		string,
		domain.CreateGitHubProject,
	) (domain.Project, error)
}

type githubScratchProjectStore interface {
	CreateGitHubScratchProject(
		context.Context,
		domain.Principal,
		string,
		string,
		int,
		domain.CreateGitHubScratchProject,
	) (domain.Project, domain.Session, error)
}

func (s *Server) githubHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	response := map[string]string{
		"environment": s.environment,
		"release":     s.release,
	}
	if s.github == nil {
		response["status"] = "disabled"
		writeJSON(w, http.StatusServiceUnavailable, response)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if err := s.github.Check(ctx); err != nil {
		s.logger.Warn(
			"check GitHub App connectivity",
			"error",
			err,
			"request_id",
			requestID(r),
		)
		response["status"] = "unavailable"
		writeJSON(w, http.StatusServiceUnavailable, response)
		return
	}
	response["status"] = "ok"
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) startGitHubInstallation(w http.ResponseWriter, r *http.Request) {
	orgID := chi.URLParam(r, "orgId")
	if requireUUID(orgID, "orgId") != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "orgId must be a UUID.")
		return
	}
	installationURL, expiresAt, err := s.github.StartInstallation(
		r.Context(),
		principalFrom(r),
		orgID,
	)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusCreated, map[string]any{
		"installationUrl": installationURL,
		"expiresAt":       expiresAt,
	})
}

func (s *Server) claimGitHubInstallation(w http.ResponseWriter, r *http.Request) {
	orgID := chi.URLParam(r, "orgId")
	if requireUUID(orgID, "orgId") != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "orgId must be a UUID.")
		return
	}
	var request claimGitHubInstallationRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "The request body is invalid.")
		return
	}
	githubInstallationID, err := strconv.ParseInt(
		strings.TrimSpace(request.GitHubInstallationID),
		10,
		64,
	)
	if err != nil || githubInstallationID <= 0 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "githubInstallationId must be a positive integer string.")
		return
	}
	installation, err := s.github.ClaimUserInstallation(
		r.Context(),
		principalFrom(r),
		orgID,
		githubInstallationID,
	)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"installation": toGitHubInstallationResponse(installation),
	})
}

func (s *Server) githubSetupCallback(w http.ResponseWriter, r *http.Request) {
	setGitHubCallbackHeaders(w)
	installationID, err := strconv.ParseInt(
		strings.TrimSpace(r.URL.Query().Get("installation_id")),
		10,
		64,
	)
	if err != nil || installationID <= 0 {
		s.githubCallbackError(w, r, postgres.ErrInvalid)
		return
	}
	oauthURL, err := s.github.BeginOAuth(
		r.Context(),
		strings.TrimSpace(r.URL.Query().Get("state")),
		installationID,
	)
	if err != nil {
		s.githubCallbackError(w, r, err)
		return
	}
	http.Redirect(w, r, oauthURL, http.StatusSeeOther)
}

func (s *Server) githubOAuthCallback(w http.ResponseWriter, r *http.Request) {
	setGitHubCallbackHeaders(w)
	_, err := s.github.CompleteOAuth(
		r.Context(),
		strings.TrimSpace(r.URL.Query().Get("state")),
		strings.TrimSpace(r.URL.Query().Get("code")),
	)
	if err != nil {
		s.githubCallbackError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(s.github.InstallationCompletionHTML(true))
}

func (s *Server) startGitHubUserAuthorization(
	w http.ResponseWriter,
	r *http.Request,
) {
	authorizeURL, expiresAt, err := s.github.StartUserAuthorization(
		r.Context(),
		principalFrom(r),
	)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusCreated, map[string]any{
		"authorizeUrl": authorizeURL,
		"expiresAt":    expiresAt,
	})
}

func (s *Server) githubUserCallback(w http.ResponseWriter, r *http.Request) {
	setGitHubCallbackHeaders(w)
	if strings.TrimSpace(r.URL.Query().Get("error")) != "" {
		s.githubCallbackError(w, r, postgres.ErrForbidden)
		return
	}
	if _, err := s.github.CompleteUserAuthorization(
		r.Context(),
		strings.TrimSpace(r.URL.Query().Get("state")),
		strings.TrimSpace(r.URL.Query().Get("code")),
	); err != nil {
		s.githubCallbackError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(s.github.CompletionHTML(true))
}

func (s *Server) getGitHubUser(w http.ResponseWriter, r *http.Request) {
	connection, installations, err := s.github.UserConnection(
		r.Context(),
		principalFrom(r),
	)
	if errors.Is(err, postgres.ErrNotFound) {
		writeJSON(w, http.StatusOK, githubUserConnectionResponse{
			Installations: []githubUserInstallationResponse{},
		})
		return
	}
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	items := make([]githubUserInstallationResponse, 0, len(installations))
	for _, installation := range installations {
		items = append(items, githubUserInstallationResponse{
			GitHubInstallationID: strconv.FormatInt(
				installation.GitHubInstallationID,
				10,
			),
			AccountLogin:        installation.AccountLogin,
			AccountType:         installation.AccountType,
			RepositorySelection: installation.RepositorySelection,
			CanCreateRepository: installation.CanCreateRepository,
			UnavailableReason:   installation.UnavailableReason,
		})
	}
	lastSyncedAt := connection.LastSyncedAt
	writeJSON(w, http.StatusOK, githubUserConnectionResponse{
		Connected:     true,
		Login:         connection.GitHubLogin,
		AvatarURL:     connection.GitHubAvatarURL,
		Installations: items,
		LastSyncedAt:  &lastSyncedAt,
	})
}

func (s *Server) disconnectGitHubUser(w http.ResponseWriter, r *http.Request) {
	if err := s.github.DisconnectUser(r.Context(), principalFrom(r)); err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) githubCallbackError(w http.ResponseWriter, r *http.Request, err error) {
	if !errors.Is(err, postgres.ErrInvalid) &&
		!errors.Is(err, postgres.ErrForbidden) &&
		!errors.Is(err, postgres.ErrNotFound) {
		s.logger.Error("GitHub callback", "error", err, "request_id", requestID(r))
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusBadRequest)
	_, _ = w.Write(s.github.CompletionHTML(false))
}

func (s *Server) listGitHubInstallations(w http.ResponseWriter, r *http.Request) {
	orgID := chi.URLParam(r, "orgId")
	if requireUUID(orgID, "orgId") != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "orgId must be a UUID.")
		return
	}
	installations, err := s.github.ListInstallations(
		r.Context(),
		principalFrom(r),
		orgID,
	)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	items := make([]githubInstallationResponse, 0, len(installations))
	for _, installation := range installations {
		items = append(items, toGitHubInstallationResponse(installation))
	}
	writeJSON(w, http.StatusOK, map[string]any{"installations": items})
}

func (s *Server) syncGitHubInstallation(w http.ResponseWriter, r *http.Request) {
	orgID, installationID, ok := githubInstallationParams(w, r)
	if !ok {
		return
	}
	installation, err := s.github.SyncInstallation(
		r.Context(),
		principalFrom(r),
		orgID,
		installationID,
	)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"installation": toGitHubInstallationResponse(installation),
	})
}

func (s *Server) disconnectGitHubInstallation(w http.ResponseWriter, r *http.Request) {
	orgID, installationID, ok := githubInstallationParams(w, r)
	if !ok {
		return
	}
	installation, err := s.github.DisconnectInstallation(
		r.Context(),
		principalFrom(r),
		orgID,
		installationID,
	)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"installation": toGitHubInstallationResponse(installation),
	})
}

func (s *Server) listGitHubRepositories(w http.ResponseWriter, r *http.Request) {
	orgID := chi.URLParam(r, "orgId")
	if requireUUID(orgID, "orgId") != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "orgId must be a UUID.")
		return
	}
	limit, err := parseLimit(r)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	cursor, err := parseCursor(r.URL.Query().Get("cursor"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_cursor", "The pagination cursor is invalid.")
		return
	}
	repositories, hasMore, err := s.github.ListRepositories(
		r.Context(),
		principalFrom(r),
		orgID,
		cursor,
		limit,
	)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	items := make([]githubRepositoryResponse, 0, len(repositories))
	for _, repository := range repositories {
		items = append(items, toGitHubRepositoryResponse(repository))
	}
	page := pageInfo{HasMore: hasMore}
	if hasMore && len(repositories) > 0 {
		last := repositories[len(repositories)-1]
		page.NextCursor = encodeCursor(last.GrantedAt, last.GrantID)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "page": page})
}

func (s *Server) createGitHubProject(w http.ResponseWriter, r *http.Request) {
	orgID := chi.URLParam(r, "orgId")
	if requireUUID(orgID, "orgId") != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "orgId must be a UUID.")
		return
	}
	key, err := idempotencyKey(r)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	var request createGitHubProjectRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "The request body is invalid.")
		return
	}
	request.DisplayName = strings.TrimSpace(request.DisplayName)
	githubRepositoryID, err := strconv.ParseInt(
		strings.TrimSpace(request.GitHubRepositoryID),
		10,
		64,
	)
	if err != nil || githubRepositoryID <= 0 || len(request.DisplayName) > 120 {
		writeError(w, r, http.StatusUnprocessableEntity, "validation_error", "The GitHub repository or display name is invalid.")
		return
	}
	if request.Config == nil {
		request.Config = map[string]any{}
	}
	config, err := json.Marshal(request.Config)
	if err != nil {
		writeError(w, r, http.StatusUnprocessableEntity, "validation_error", "Project configuration is invalid.")
		return
	}
	store, ok := s.store.(githubProjectStore)
	if !ok {
		writeError(w, r, http.StatusNotImplemented, "not_implemented", "GitHub project creation is unavailable.")
		return
	}
	project, err := store.CreateGitHubProject(
		r.Context(),
		principalFrom(r),
		orgID,
		key,
		domain.CreateGitHubProject{
			GitHubRepositoryID: githubRepositoryID,
			DisplayName:        request.DisplayName,
			Config:             config,
		},
	)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"project": toProjectResponse(project)})
}

func (s *Server) createGitHubScratchProject(
	w http.ResponseWriter,
	r *http.Request,
) {
	orgID := chi.URLParam(r, "orgId")
	if requireUUID(orgID, "orgId") != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "orgId must be a UUID.")
		return
	}
	key, err := idempotencyKey(r)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	var request createGitHubScratchProjectRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "The request body is invalid.")
		return
	}
	request.DisplayName = strings.TrimSpace(request.DisplayName)
	request.Orchestrator.Harness = strings.TrimSpace(request.Orchestrator.Harness)
	installationID, err := strconv.ParseInt(
		strings.TrimSpace(request.GitHubInstallationID),
		10,
		64,
	)
	if err != nil || installationID <= 0 ||
		request.DisplayName == "" || len(request.DisplayName) > 80 ||
		request.Orchestrator.Harness == "" ||
		len(request.Orchestrator.Harness) > 120 ||
		len(request.Orchestrator.Prompt) > 65536 {
		writeError(w, r, http.StatusUnprocessableEntity, "validation_error", "The scratch project request is invalid.")
		return
	}
	private := true
	if request.Private != nil {
		private = *request.Private
	}
	requestBinding, err := json.Marshal(request)
	if err != nil {
		writeError(w, r, http.StatusUnprocessableEntity, "validation_error", "The scratch project request is invalid.")
		return
	}
	plan, err := s.provisioning.SessionPlan(request.Orchestrator.Harness)
	if err != nil {
		s.logger.Error("resolve scratch provisioning plan", "error", err, "request_id", requestID(r))
		writeError(w, r, http.StatusInternalServerError, "internal_error", "Sandbox provisioning is misconfigured.")
		return
	}
	grant, err := s.github.PrepareScratchCapability(
		r.Context(),
		principalFrom(r),
		orgID,
		key,
		"production",
		installationID,
		request.DisplayName,
		private,
		requestBinding,
	)
	if errors.Is(err, postgres.ErrNotFound) {
		writeError(w, r, http.StatusConflict, "github_connection_required", "Connect GitHub before creating a repository.")
		return
	}
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	store, ok := s.store.(githubScratchProjectStore)
	if !ok {
		_ = s.github.RevokeScratchCapability(
			context.WithoutCancel(r.Context()),
			principalFrom(r),
			orgID,
			grant.Capability,
		)
		writeError(w, r, http.StatusNotImplemented, "not_implemented", "GitHub scratch projects are unavailable.")
		return
	}
	project, session, err := store.CreateGitHubScratchProject(
		r.Context(),
		principalFrom(r),
		orgID,
		key,
		s.maxSandboxes,
		domain.CreateGitHubScratchProject{
			Repository:              grant.Authority.Repository,
			GitHubInstallationID:    grant.Authority.GitHubInstallationID,
			AuthorityUserExternalID: grant.Authority.UserExternalID,
			AuthorityEnvironment:    "production",
			CapabilityHash:          grant.Authority.CapabilityHash,
			DisplayName:             request.DisplayName,
			Config:                  json.RawMessage(`{"source":"scratch"}`),
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
		_ = s.github.RevokeScratchCapability(
			context.WithoutCancel(r.Context()),
			principalFrom(r),
			orgID,
			grant.Capability,
		)
		s.writeStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"project":    toProjectResponse(project),
		"repository": toGitHubRepositoryResponse(grant.Authority.Repository),
		"session":    toSessionResponse(session, nil),
	})
}

func (s *Server) githubWebhook(w http.ResponseWriter, r *http.Request) {
	deliveryID := strings.TrimSpace(r.Header.Get("X-GitHub-Delivery"))
	event := strings.TrimSpace(r.Header.Get("X-GitHub-Event"))
	signature := strings.TrimSpace(r.Header.Get("X-Hub-Signature-256"))
	if deliveryID == "" || len(deliveryID) > 128 ||
		event == "" || len(event) > 64 || signature == "" {
		writeError(w, r, http.StatusBadRequest, "invalid_webhook", "Required GitHub webhook headers are missing.")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, s.webhookMaxBody)
	payload, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, r, http.StatusRequestEntityTooLarge, "webhook_too_large", "The GitHub webhook payload is too large.")
		return
	}
	if !s.github.VerifyWebhook(payload, signature) {
		writeError(w, r, http.StatusUnauthorized, "invalid_signature", "The GitHub webhook signature is invalid.")
		return
	}
	if event == "ping" {
		w.WriteHeader(http.StatusOK)
		return
	}
	if event != "installation" && event != "installation_repositories" &&
		event != "github_app_authorization" {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	var envelope struct {
		Action       string `json:"action"`
		Installation *struct {
			ID int64 `json:"id"`
		} `json:"installation"`
		Repository *struct {
			ID int64 `json:"id"`
		} `json:"repository"`
		Sender *struct {
			ID int64 `json:"id"`
		} `json:"sender"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_webhook", "The GitHub webhook payload is invalid.")
		return
	}
	var installationID, repositoryID int64
	if envelope.Installation != nil {
		installationID = envelope.Installation.ID
	}
	if envelope.Repository != nil {
		repositoryID = envelope.Repository.ID
	}
	if event == "github_app_authorization" {
		if envelope.Action == "revoked" && envelope.Sender != nil &&
			envelope.Sender.ID > 0 {
			if err := s.github.RevokeUserByGitHubID(
				r.Context(),
				envelope.Sender.ID,
			); err != nil {
				s.writeStoreError(w, r, err)
				return
			}
		}
		w.WriteHeader(http.StatusAccepted)
		return
	}
	inserted, err := s.github.EnqueueVerifiedWebhook(r.Context(), domain.GitHubWebhookDelivery{
		DeliveryID:           deliveryID,
		Event:                event,
		Action:               envelope.Action,
		GitHubInstallationID: installationID,
		GitHubRepositoryID:   repositoryID,
		Payload:              payload,
	})
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	if !inserted {
		w.WriteHeader(http.StatusOK)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func githubInstallationParams(
	w http.ResponseWriter,
	r *http.Request,
) (string, string, bool) {
	orgID := chi.URLParam(r, "orgId")
	installationID := chi.URLParam(r, "installationId")
	if requireUUID(orgID, "orgId") != nil ||
		requireUUID(installationID, "installationId") != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "Organization and installation IDs must be UUIDs.")
		return "", "", false
	}
	return orgID, installationID, true
}

func toGitHubInstallationResponse(
	installation domain.GitHubInstallation,
) githubInstallationResponse {
	status := installation.Status
	if status == "disconnected" || status == "deleted" {
		status = "removed"
	}
	return githubInstallationResponse{
		ID:                   installation.ID,
		GitHubInstallationID: strconv.FormatInt(installation.GitHubInstallationID, 10),
		AccountLogin:         installation.AccountLogin,
		AccountType:          installation.AccountType,
		Status:               status,
		RepositorySelection:  installation.RepositorySelection,
		SyncStatus:           installation.SyncStatus,
		LastSyncedAt:         installation.LastSyncedAt,
		LastError:            installation.LastError,
		CreatedAt:            installation.CreatedAt,
		UpdatedAt:            installation.UpdatedAt,
	}
}

func toGitHubRepositoryResponse(
	repository domain.GitHubRepository,
) githubRepositoryResponse {
	access := "active"
	if repository.RevokedAt != nil {
		access = "revoked"
	}
	return githubRepositoryResponse{
		GitHubRepositoryID: strconv.FormatInt(repository.GitHubRepositoryID, 10),
		Name:               repository.Name,
		FullName:           repository.FullName,
		HTMLURL:            repository.HTMLURL,
		DefaultBranch:      repository.DefaultBranch,
		Visibility:         repository.Visibility,
		IsPrivate:          repository.IsPrivate,
		IsArchived:         repository.IsArchived,
		Access:             access,
		GrantedAt:          repository.GrantedAt,
		RevokedAt:          repository.RevokedAt,
	}
}

func setGitHubCallbackHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; base-uri 'none'; frame-ancestors 'none'")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
}
