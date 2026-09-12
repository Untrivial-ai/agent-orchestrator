package httpapi

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/pkg/contract"
	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/go-chi/chi/v5"
)

type createProjectRequest struct {
	DisplayName   string         `json:"displayName"`
	RepositoryURL string         `json:"repositoryUrl"`
	DefaultBranch string         `json:"defaultBranch"`
	Config        map[string]any `json:"config,omitempty"`
}

type updateProjectRequest struct {
	DisplayName   string `json:"displayName"`
	DefaultBranch string `json:"defaultBranch"`
}

type projectResponse struct {
	ID                 string         `json:"id"`
	OrgID              string         `json:"orgId"`
	DisplayName        string         `json:"displayName"`
	RepositoryURL      string         `json:"repositoryUrl"`
	DefaultBranch      string         `json:"defaultBranch"`
	GitHubRepositoryID string         `json:"githubRepositoryId,omitempty"`
	Config             map[string]any `json:"config"`
	CreatedAt          time.Time      `json:"createdAt"`
	UpdatedAt          time.Time      `json:"updatedAt"`
}

type createSessionRequest struct {
	ProjectID                   string   `json:"projectId"`
	Kind                        string   `json:"kind"`
	Harness                     string   `json:"harness"`
	DisplayName                 string   `json:"displayName"`
	Prompt                      string   `json:"prompt"`
	Mode                        string   `json:"mode,omitempty"`
	DeniedCommands              []string `json:"deniedCommands,omitempty"`
	SandboxProviderConnectionID string   `json:"sandboxProviderConnectionId,omitempty"`
}

type sessionResponse struct {
	ID               string    `json:"id"`
	OrgID            string    `json:"orgId"`
	ProjectID        string    `json:"projectId"`
	Kind             string    `json:"kind"`
	Harness          string    `json:"harness"`
	DisplayName      string    `json:"displayName"`
	Branch           string    `json:"branch"`
	Mode             string    `json:"mode"`
	DeniedCommands   []string  `json:"deniedCommands"`
	ActivityState    string    `json:"activityState"`
	Status           string    `json:"status"`
	RuntimeConnected bool      `json:"runtimeConnected"`
	RuntimeState     string    `json:"runtimeState,omitempty"`
	RuntimeError     string    `json:"runtimeError,omitempty"`
	IsTerminated     bool      `json:"isTerminated"`
	CreatedAt        time.Time `json:"createdAt"`
	UpdatedAt        time.Time `json:"updatedAt"`
}

type pageInfo struct {
	HasMore    bool   `json:"hasMore"`
	NextCursor string `json:"nextCursor,omitempty"`
}

func (s *Server) createProject(w http.ResponseWriter, r *http.Request) {
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
	var request createProjectRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "The request body is invalid.")
		return
	}
	request.DisplayName = strings.TrimSpace(request.DisplayName)
	request.RepositoryURL = strings.TrimSpace(request.RepositoryURL)
	request.DefaultBranch = strings.TrimSpace(request.DefaultBranch)
	if !validProjectInput(request) {
		writeError(w, r, http.StatusUnprocessableEntity, "validation_error", "Project name, repository URL, or default branch is invalid.")
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
	project, err := s.store.CreateProject(
		r.Context(),
		principalFrom(r),
		orgID,
		key,
		domain.CreateProject{
			DisplayName:   request.DisplayName,
			RepositoryURL: request.RepositoryURL,
			DefaultBranch: request.DefaultBranch,
			Config:        config,
		},
	)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"project": toProjectResponse(project)})
}

func (s *Server) listProjects(w http.ResponseWriter, r *http.Request) {
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
	projects, hasMore, err := s.store.ListProjects(
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
	items := make([]projectResponse, 0, len(projects))
	for _, project := range projects {
		items = append(items, toProjectResponse(project))
	}
	page := pageInfo{HasMore: hasMore}
	if hasMore && len(projects) > 0 {
		last := projects[len(projects)-1]
		page.NextCursor = encodeCursor(last.CreatedAt, last.ID)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "page": page})
}

func (s *Server) updateProject(w http.ResponseWriter, r *http.Request) {
	orgID := chi.URLParam(r, "orgId")
	projectID := chi.URLParam(r, "projectId")
	if requireUUID(orgID, "orgId") != nil ||
		requireUUID(projectID, "projectId") != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "orgId and projectId must be UUIDs.")
		return
	}
	var request updateProjectRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "The request body is invalid.")
		return
	}
	request.DisplayName = strings.TrimSpace(request.DisplayName)
	request.DefaultBranch = strings.TrimSpace(request.DefaultBranch)
	if !validProjectUpdate(request) {
		writeError(w, r, http.StatusUnprocessableEntity, "validation_error", "Project name or default branch is invalid.")
		return
	}
	project, err := s.store.UpdateProject(
		r.Context(),
		principalFrom(r),
		orgID,
		projectID,
		domain.UpdateProject{
			DisplayName:   request.DisplayName,
			DefaultBranch: request.DefaultBranch,
		},
	)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"project": toProjectResponse(project)})
}

// deleteProject archives the project immediately and queues every associated
// sandbox for reconciler-owned teardown. Durable session and audit history are
// retained instead of being cascaded out of PostgreSQL.
func (s *Server) deleteProject(w http.ResponseWriter, r *http.Request) {
	orgID := chi.URLParam(r, "orgId")
	projectID := chi.URLParam(r, "projectId")
	if requireUUID(orgID, "orgId") != nil ||
		requireUUID(projectID, "projectId") != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "orgId and projectId must be UUIDs.")
		return
	}
	if err := s.store.ArchiveProject(
		r.Context(),
		principalFrom(r),
		orgID,
		projectID,
	); err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"project": map[string]any{"id": projectID, "deleted": true},
	})
}

func (s *Server) createSession(w http.ResponseWriter, r *http.Request) {
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
	var request createSessionRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "The request body is invalid.")
		return
	}
	request.ProjectID = strings.TrimSpace(request.ProjectID)
	request.Harness = strings.TrimSpace(request.Harness)
	request.DisplayName = strings.TrimSpace(request.DisplayName)
	request.Mode = strings.TrimSpace(request.Mode)
	request.SandboxProviderConnectionID = strings.TrimSpace(request.SandboxProviderConnectionID)
	if request.Mode == "" {
		request.Mode = "trusted"
	}
	if !validSessionInput(request) {
		writeError(w, r, http.StatusUnprocessableEntity, "validation_error", "Session project, kind, harness, name, or prompt is invalid.")
		return
	}
	if !supportedInteractivePolicy(request) {
		writeError(
			w, r, http.StatusUnprocessableEntity, "unsupported_policy",
			"Interactive Cloud agents currently support standard or trusted mode without command deny rules.",
		)
		return
	}
	if store, ok := s.store.(providerConnectionStore); ok {
		connections, err := store.ListProviderConnections(
			r.Context(), principalFrom(r), orgID,
		)
		if err != nil {
			s.writeStoreError(w, r, err)
			return
		}
		available := agentConnectionAvailable(connections, request.Harness)
		if !available {
			if userStore, ok := s.store.(userProviderCredentialStore); ok {
				available, err = userStore.UserAgentCredentialAvailable(
					r.Context(), principalFrom(r).UserID, request.Harness,
				)
				if err != nil {
					s.writeStoreError(w, r, err)
					return
				}
			}
		}
		if !available {
			writeError(
				w, r, http.StatusUnprocessableEntity,
				"agent_provider_required",
				"Connect and validate the selected coding-agent provider before creating a session.",
			)
			return
		}
	}
	// The plan is resolved once, here, and stamped onto the sandbox row. The
	// reconciler reads it back from the row rather than from configuration, so
	// a later config change cannot disturb a session already in flight.
	plan, err := s.provisioning.SessionPlan(request.Harness)
	if err != nil {
		s.logger.Error("resolve sandbox provisioning plan", "error", err, "request_id", requestID(r))
		writeError(
			w, r, http.StatusInternalServerError, "internal_error",
			"Sandbox provisioning is misconfigured on this deployment.",
		)
		return
	}
	session, err := s.store.CreateSession(
		r.Context(),
		principalFrom(r),
		orgID,
		key,
		s.maxSandboxes,
		domain.CreateSession{
			ProjectID:           request.ProjectID,
			Kind:                request.Kind,
			Harness:             request.Harness,
			DisplayName:         request.DisplayName,
			Prompt:              request.Prompt,
			Mode:                request.Mode,
			DeniedCommands:      request.DeniedCommands,
			Provider:            plan.Provider,
			SandboxConnectionID: request.SandboxProviderConnectionID,
			ResourceProfile:     plan.ResourceProfile,
			BootstrapContext:    plan.BootstrapContext,
			Release:             s.release,
		},
	)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"session": toSessionResponse(session, nil)})
}

func (s *Server) listSessions(w http.ResponseWriter, r *http.Request) {
	orgID := chi.URLParam(r, "orgId")
	if requireUUID(orgID, "orgId") != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "orgId must be a UUID.")
		return
	}
	projectID := strings.TrimSpace(r.URL.Query().Get("projectId"))
	if projectID != "" && requireUUID(projectID, "projectId") != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "projectId must be a UUID.")
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
	sessions, hasMore, err := s.store.ListSessions(
		r.Context(),
		principalFrom(r),
		orgID,
		projectID,
		cursor,
		limit,
	)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	sessionIDs := make([]string, len(sessions))
	for i, session := range sessions {
		sessionIDs[i] = session.ID
	}
	prFacts, err := s.store.PRFactsBySession(r.Context(), orgID, sessionIDs)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	items := make([]sessionResponse, 0, len(sessions))
	for _, session := range sessions {
		items = append(items, toSessionResponse(session, prFacts[session.ID]))
	}
	page := pageInfo{HasMore: hasMore}
	if hasMore && len(sessions) > 0 {
		last := sessions[len(sessions)-1]
		page.NextCursor = encodeCursor(last.UpdatedAt, last.ID)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "page": page})
}

// wakePausedSessions asks the reconciler to resume this user's idle-paused
// sandboxes. It intentionally does not wait for NodeOps or a worker heartbeat;
// callers continue to use the regular session projection for readiness.
func (s *Server) wakePausedSessions(w http.ResponseWriter, r *http.Request) {
	orgID := chi.URLParam(r, "orgId")
	if requireUUID(orgID, "orgId") != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "orgId must be a UUID.")
		return
	}
	woken, err := s.store.WakePausedSessions(r.Context(), principalFrom(r), orgID)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"woken": woken})
}

func (s *Server) getSession(w http.ResponseWriter, r *http.Request) {
	orgID := chi.URLParam(r, "orgId")
	sessionID := chi.URLParam(r, "sessionId")
	if requireUUID(orgID, "orgId") != nil || requireUUID(sessionID, "sessionId") != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "orgId and sessionId must be UUIDs.")
		return
	}
	session, err := s.store.GetSession(
		r.Context(),
		principalFrom(r),
		orgID,
		sessionID,
	)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	prFacts, err := s.store.PRFactsBySession(r.Context(), orgID, []string{sessionID})
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"session": toSessionResponse(session, prFacts[sessionID])})
}

// deleteSession records the intent to tear a session's sandbox down. It does
// not call the provider: the reconciler owns every slow provider call, so a
// degraded provider cannot stall this request. The reconciler releases quota
// only after it confirms the compute is gone, then marks the retained session
// terminated so its event history remains available.
func (s *Server) deleteSession(w http.ResponseWriter, r *http.Request) {
	orgID := chi.URLParam(r, "orgId")
	sessionID := chi.URLParam(r, "sessionId")
	if requireUUID(orgID, "orgId") != nil || requireUUID(sessionID, "sessionId") != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "orgId and sessionId must be UUIDs.")
		return
	}
	if err := s.store.SetSandboxDesiredState(
		r.Context(),
		principalFrom(r),
		orgID,
		sessionID,
		domain.SandboxDesiredDeleted,
	); err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"session": map[string]any{"id": sessionID, "desiredState": domain.SandboxDesiredDeleted},
	})
}

func validProjectInput(request createProjectRequest) bool {
	if len(request.DisplayName) < 1 || len(request.DisplayName) > 120 ||
		len(request.DefaultBranch) < 1 || len(request.DefaultBranch) > 255 {
		return false
	}
	parsed, err := url.ParseRequestURI(request.RepositoryURL)
	return err == nil && parsed.Scheme == "https" && parsed.Host != ""
}

func validProjectUpdate(request updateProjectRequest) bool {
	return len(request.DisplayName) >= 1 &&
		len(request.DisplayName) <= 120 &&
		len(request.DefaultBranch) >= 1 &&
		len(request.DefaultBranch) <= 255
}

func validSessionInput(request createSessionRequest) bool {
	if requireUUID(request.ProjectID, "projectId") != nil ||
		(request.Kind != "worker" && request.Kind != "orchestrator") ||
		(request.Mode != "read-only" && request.Mode != "standard" && request.Mode != "trusted") ||
		len(request.Harness) < 1 || len(request.Harness) > 120 ||
		len(request.DisplayName) < 1 || len(request.DisplayName) > 80 ||
		len(request.Prompt) > 65536 ||
		len(request.DeniedCommands) > 128 {
		return false
	}
	for _, command := range request.DeniedCommands {
		if strings.TrimSpace(command) == "" || len(command) > 512 {
			return false
		}
	}
	return request.SandboxProviderConnectionID == "" ||
		requireUUID(request.SandboxProviderConnectionID, "sandboxProviderConnectionId") == nil
}

func supportedInteractivePolicy(request createSessionRequest) bool {
	return request.Mode != "read-only" && len(request.DeniedCommands) == 0
}

func toProjectResponse(project domain.Project) projectResponse {
	config := map[string]any{}
	_ = json.Unmarshal(project.Config, &config)
	return projectResponse{
		ID:                 project.ID,
		OrgID:              project.OrgID,
		DisplayName:        project.DisplayName,
		RepositoryURL:      project.RepositoryURL,
		DefaultBranch:      project.DefaultBranch,
		GitHubRepositoryID: decimalID(project.GitHubRepositoryID),
		Config:             config,
		CreatedAt:          project.CreatedAt,
		UpdatedAt:          project.UpdatedAt,
	}
}

// toSessionResponse renders one session. prs is that session's pull-request
// facts, used to derive PR-lifecycle status (pr_open, ci_failed, ...) —
// pass nil only for a session that provably has none yet (just created).
func toSessionResponse(session domain.Session, prs []contract.PRFacts) sessionResponse {
	return sessionResponse{
		ID:               session.ID,
		OrgID:            session.OrgID,
		ProjectID:        session.ProjectID,
		Kind:             session.Kind,
		Harness:          session.Harness,
		DisplayName:      session.DisplayName,
		Branch:           session.Branch,
		Mode:             session.Mode,
		DeniedCommands:   nonNilStrings(session.DeniedCommands),
		ActivityState:    string(session.ActivityState),
		Status:           string(session.Status(time.Now().UTC(), prs)),
		RuntimeConnected: session.RuntimeConnected,
		RuntimeState:     session.RuntimeState,
		RuntimeError:     session.RuntimeError,
		IsTerminated:     session.IsTerminated,
		CreatedAt:        session.CreatedAt,
		UpdatedAt:        session.UpdatedAt,
	}
}

func decimalID(id *int64) string {
	if id == nil {
		return ""
	}
	return strconv.FormatInt(*id, 10)
}

func nonNilStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}
