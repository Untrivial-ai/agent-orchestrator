package httpapi

import (
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
)

type shareLinkResponse struct {
	ID             string     `json:"id"`
	OrgID          string     `json:"orgId"`
	ProjectID      string     `json:"projectId"`
	SessionID      string     `json:"sessionId,omitempty"`
	Role           string     `json:"role"`
	Status         string     `json:"status"`
	AccessScope    string     `json:"accessScope"`
	Recipients     []string   `json:"recipients"`
	Interaction    string     `json:"interaction"`
	ModeCap        string     `json:"modeCap,omitempty"`
	DeniedCommands []string   `json:"deniedCommands"`
	ExpiresAt      *time.Time `json:"expiresAt,omitempty"`
	CreatedAt      time.Time  `json:"createdAt"`
	UpdatedAt      time.Time  `json:"updatedAt"`
	// Token and URL are only ever populated once, by createProjectShareLink,
	// from the raw token the store never persists. Every other response
	// omits them. Token is the source of truth — the client builds the
	// actual link it displays/copies from window.location.origin plus
	// Token, since only the browser reliably knows its own public-facing
	// host; URL is a best-effort convenience for non-browser callers, built
	// from what this request's own proxy chain reported (see
	// shareLinkURL), which is not guaranteed correct in every deployment.
	Token string `json:"token,omitempty"`
	URL   string `json:"url,omitempty"`
}

func toShareLinkResponse(link domain.ShareLink) shareLinkResponse {
	return shareLinkResponse{
		ID:             link.ID,
		OrgID:          link.OrgID,
		ProjectID:      link.ProjectID,
		SessionID:      link.SessionID,
		Role:           link.Role,
		Status:         link.Status,
		AccessScope:    link.AccessScope,
		Recipients:     link.Recipients,
		Interaction:    link.Interaction,
		ModeCap:        link.ModeCap,
		DeniedCommands: link.DeniedCommands,
		ExpiresAt:      link.ExpiresAt,
		CreatedAt:      link.CreatedAt,
		UpdatedAt:      link.UpdatedAt,
	}
}

type sharedProjectResponse struct {
	Grant struct {
		ID              string    `json:"id"`
		Role            string    `json:"role"`
		Status          string    `json:"status"`
		UserEmail       string    `json:"userEmail,omitempty"`
		UserDisplayName string    `json:"userDisplayName,omitempty"`
		ModeCap         string    `json:"modeCap,omitempty"`
		DeniedCommands  []string  `json:"deniedCommands"`
		RedeemedAt      time.Time `json:"redeemedAt"`
	} `json:"grant"`
	Project       projectResponse `json:"project"`
	SessionID     string          `json:"sessionId,omitempty"`
	SessionName   string          `json:"sessionName,omitempty"`
	SharedByEmail string          `json:"sharedByEmail,omitempty"`
	SharedByName  string          `json:"sharedByName,omitempty"`
}

func toSharedProjectResponse(shared domain.SharedProject) sharedProjectResponse {
	response := sharedProjectResponse{
		Project:       toProjectResponse(shared.Project),
		SessionID:     shared.SessionID,
		SessionName:   shared.SessionName,
		SharedByEmail: shared.SharedByEmail,
		SharedByName:  shared.SharedByName,
	}
	response.Grant.ID = shared.Grant.ID
	response.Grant.Role = shared.Grant.Role
	response.Grant.Status = shared.Grant.Status
	response.Grant.UserEmail = shared.Grant.UserEmail
	response.Grant.UserDisplayName = shared.Grant.UserDisplayName
	response.Grant.ModeCap = shared.Grant.ModeCap
	response.Grant.DeniedCommands = shared.Grant.DeniedCommands
	response.Grant.RedeemedAt = shared.Grant.RedeemedAt
	return response
}

var validShareRoles = map[string]bool{"viewer": true, "editor": true}
var validShareModeCaps = map[string]bool{"": true, "read-only": true, "standard": true, "trusted": true}

type createShareLinkRequest struct {
	SessionID      string   `json:"sessionId,omitempty"`
	Role           string   `json:"role,omitempty"`
	AccessScope    string   `json:"accessScope,omitempty"`
	Recipients     []string `json:"recipients,omitempty"`
	Interaction    string   `json:"interaction,omitempty"`
	ModeCap        string   `json:"modeCap,omitempty"`
	DeniedCommands []string `json:"deniedCommands,omitempty"`
	ExpiresInHours int      `json:"expiresInHours,omitempty"`
}

func (s *Server) createProjectShareLink(w http.ResponseWriter, r *http.Request) {
	orgID := chi.URLParam(r, "orgId")
	projectID := chi.URLParam(r, "projectId")
	if requireUUID(orgID, "orgId") != nil || requireUUID(projectID, "projectId") != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "orgId and projectId must be UUIDs.")
		return
	}
	var request createShareLinkRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "The request body is invalid.")
		return
	}
	role := strings.TrimSpace(request.Role)
	if role == "" {
		role = "viewer"
	}
	if !validShareRoles[role] {
		writeError(w, r, http.StatusUnprocessableEntity, "validation_error", "Role must be viewer or editor.")
		return
	}
	accessScope := strings.TrimSpace(request.AccessScope)
	if accessScope == "" {
		accessScope = "anyone"
	}
	if accessScope != "anyone" && accessScope != "restricted" {
		writeError(w, r, http.StatusUnprocessableEntity, "validation_error", "Access scope must be anyone or restricted.")
		return
	}
	interaction := strings.TrimSpace(request.Interaction)
	if interaction == "" {
		interaction = "view"
	}
	if interaction != "view" && interaction != "interact" {
		writeError(w, r, http.StatusUnprocessableEntity, "validation_error", "Interaction must be view or interact.")
		return
	}
	modeCap := strings.TrimSpace(request.ModeCap)
	if !validShareModeCaps[modeCap] {
		writeError(w, r, http.StatusUnprocessableEntity, "validation_error", "Mode cap must be read-only, standard, or trusted.")
		return
	}
	recipients := make([]string, 0, len(request.Recipients))
	for _, recipient := range request.Recipients {
		recipient = strings.ToLower(strings.TrimSpace(recipient))
		if !validEmail(recipient) {
			writeError(w, r, http.StatusUnprocessableEntity, "validation_error", "Every recipient must be a valid email address.")
			return
		}
		recipients = append(recipients, recipient)
	}
	var expiresAt *time.Time
	if request.ExpiresInHours > 0 {
		at := time.Now().UTC().Add(time.Duration(request.ExpiresInHours) * time.Hour)
		expiresAt = &at
	}
	link, token, err := s.store.CreateProjectShareLink(r.Context(), principalFrom(r), orgID, projectID, domain.CreateShareLink{
		SessionID:      request.SessionID,
		Role:           role,
		AccessScope:    accessScope,
		Recipients:     recipients,
		Interaction:    interaction,
		ModeCap:        modeCap,
		DeniedCommands: request.DeniedCommands,
		ExpiresAt:      expiresAt,
	})
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	response := toShareLinkResponse(link)
	response.Token = token
	response.URL = shareLinkURL(r, orgID, token)
	writeJSON(w, http.StatusCreated, map[string]any{"link": response})
}

// shareLinkURL builds an absolute link back to this same deployment,
// reading the scheme and host the client actually connected with (from
// X-Forwarded-* when present, since the control plane sits behind a proxy
// in every hosted deployment) rather than a hardcoded origin — the same
// link therefore resolves correctly whether it's opened against a local
// dev server or the hosted product.
func shareLinkURL(r *http.Request, orgID, token string) string {
	scheme := "https"
	if forwarded := r.Header.Get("X-Forwarded-Proto"); forwarded != "" {
		scheme = strings.Split(forwarded, ",")[0]
	} else if r.TLS == nil {
		scheme = "http"
	}
	host := r.Header.Get("X-Forwarded-Host")
	if host == "" {
		host = r.Host
	}
	return scheme + "://" + host + "/share/" + orgID + "/" + token
}

func (s *Server) listProjectShareLinks(w http.ResponseWriter, r *http.Request) {
	orgID := chi.URLParam(r, "orgId")
	projectID := chi.URLParam(r, "projectId")
	if requireUUID(orgID, "orgId") != nil || requireUUID(projectID, "projectId") != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "orgId and projectId must be UUIDs.")
		return
	}
	links, err := s.store.ListProjectShareLinks(r.Context(), principalFrom(r), orgID, projectID)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	items := make([]shareLinkResponse, 0, len(links))
	for _, link := range links {
		items = append(items, toShareLinkResponse(link))
	}
	writeJSON(w, http.StatusOK, map[string]any{"links": items})
}

func (s *Server) listProjectShareGrants(w http.ResponseWriter, r *http.Request) {
	orgID := chi.URLParam(r, "orgId")
	projectID := chi.URLParam(r, "projectId")
	if requireUUID(orgID, "orgId") != nil || requireUUID(projectID, "projectId") != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "orgId and projectId must be UUIDs.")
		return
	}
	grants, err := s.store.ListProjectShareGrants(r.Context(), principalFrom(r), orgID, projectID)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	items := make([]sharedProjectResponse, 0, len(grants))
	for _, grant := range grants {
		items = append(items, toSharedProjectResponse(grant))
	}
	writeJSON(w, http.StatusOK, map[string]any{"grants": items})
}

type updateShareGrantRequest struct {
	Role      string `json:"role"`
	ModeCap   string `json:"modeCap"`
	SessionID string `json:"sessionId"`
}

func (s *Server) updateProjectShareGrant(w http.ResponseWriter, r *http.Request) {
	orgID := chi.URLParam(r, "orgId")
	projectID := chi.URLParam(r, "projectId")
	grantID := chi.URLParam(r, "grantId")
	if requireUUID(orgID, "orgId") != nil || requireUUID(projectID, "projectId") != nil || requireUUID(grantID, "grantId") != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "orgId, projectId, and grantId must be UUIDs.")
		return
	}
	var request updateShareGrantRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "The request body is invalid.")
		return
	}
	request.Role = strings.TrimSpace(request.Role)
	request.ModeCap = strings.TrimSpace(request.ModeCap)
	request.SessionID = strings.TrimSpace(request.SessionID)
	if !validShareRoles[request.Role] || request.Role == "" {
		writeError(w, r, http.StatusUnprocessableEntity, "validation_error", "Role must be viewer or editor.")
		return
	}
	if request.ModeCap == "" || !validShareModeCaps[request.ModeCap] {
		writeError(w, r, http.StatusUnprocessableEntity, "validation_error", "Mode cap must be read-only, standard, or trusted.")
		return
	}
	if request.SessionID != "" && requireUUID(request.SessionID, "sessionId") != nil {
		writeError(w, r, http.StatusUnprocessableEntity, "validation_error", "sessionId must be a UUID when provided.")
		return
	}
	grant, err := s.store.UpdateProjectShareGrant(r.Context(), principalFrom(r), orgID, projectID, grantID, domain.UpdateShareGrant{
		Role: request.Role, ModeCap: request.ModeCap, SessionID: request.SessionID,
	})
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"grant": toSharedProjectResponse(grant)})
}

func (s *Server) revokeProjectShareLink(w http.ResponseWriter, r *http.Request) {
	orgID := chi.URLParam(r, "orgId")
	projectID := chi.URLParam(r, "projectId")
	linkID := chi.URLParam(r, "linkId")
	if requireUUID(orgID, "orgId") != nil || requireUUID(projectID, "projectId") != nil || requireUUID(linkID, "linkId") != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "orgId, projectId, and linkId must be UUIDs.")
		return
	}
	if err := s.store.RevokeProjectShareLink(r.Context(), principalFrom(r), orgID, projectID, linkID); err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) revokeProjectShareGrant(w http.ResponseWriter, r *http.Request) {
	orgID := chi.URLParam(r, "orgId")
	projectID := chi.URLParam(r, "projectId")
	grantID := chi.URLParam(r, "grantId")
	if requireUUID(orgID, "orgId") != nil || requireUUID(projectID, "projectId") != nil || requireUUID(grantID, "grantId") != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "orgId, projectId, and grantId must be UUIDs.")
		return
	}
	if err := s.store.RevokeProjectShareGrant(r.Context(), principalFrom(r), orgID, projectID, grantID); err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type redeemShareLinkRequest struct {
	OrgID string `json:"orgId"`
	Token string `json:"token"`
}

func (s *Server) redeemProjectShareLink(w http.ResponseWriter, r *http.Request) {
	var request redeemShareLinkRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "The request body is invalid.")
		return
	}
	orgID := strings.TrimSpace(request.OrgID)
	token := strings.TrimSpace(request.Token)
	if requireUUID(orgID, "orgId") != nil || token == "" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "orgId must be a UUID and token is required.")
		return
	}
	shared, err := s.store.RedeemProjectShareLink(r.Context(), principalFrom(r), orgID, token)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"shared": toSharedProjectResponse(shared)})
}

func (s *Server) listSharedProjectSessions(w http.ResponseWriter, r *http.Request) {
	orgID := chi.URLParam(r, "orgId")
	projectID := chi.URLParam(r, "projectId")
	if requireUUID(orgID, "orgId") != nil || requireUUID(projectID, "projectId") != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "orgId and projectId must be UUIDs.")
		return
	}
	sessions, err := s.store.ListSharedProjectSessions(r.Context(), principalFrom(r), orgID, projectID)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	items := make([]sessionResponse, 0, len(sessions))
	for _, session := range sessions {
		items = append(items, toSessionResponse(session, nil))
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": items})
}

func (s *Server) listSharedProjects(w http.ResponseWriter, r *http.Request) {
	shared, err := s.store.ListSharedProjects(r.Context(), principalFrom(r))
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	items := make([]sharedProjectResponse, 0, len(shared))
	for _, item := range shared {
		items = append(items, toSharedProjectResponse(item))
	}
	writeJSON(w, http.StatusOK, map[string]any{"shared": items})
}
