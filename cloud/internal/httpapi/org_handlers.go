package httpapi

import (
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
)

type orgMemberResponse struct {
	UserID      string    `json:"userId"`
	Email       string    `json:"email"`
	DisplayName string    `json:"displayName"`
	Role        string    `json:"role"`
	CreatedAt   time.Time `json:"createdAt"`
}

func toOrgMemberResponse(member domain.OrgMember) orgMemberResponse {
	return orgMemberResponse{
		UserID:      member.UserID,
		Email:       member.Email,
		DisplayName: member.DisplayName,
		Role:        member.Role,
		CreatedAt:   member.CreatedAt,
	}
}

type invitationResponse struct {
	ID             string     `json:"id"`
	OrgID          string     `json:"orgId"`
	Email          string     `json:"email"`
	InvitedByEmail string     `json:"invitedByEmail,omitempty"`
	InvitedByName  string     `json:"invitedByName,omitempty"`
	Role           string     `json:"role"`
	Status         string     `json:"status"`
	ExpiresAt      time.Time  `json:"expiresAt"`
	AcceptedAt     *time.Time `json:"acceptedAt,omitempty"`
	DeclinedAt     *time.Time `json:"declinedAt,omitempty"`
	RevokedAt      *time.Time `json:"revokedAt,omitempty"`
	CreatedAt      time.Time  `json:"createdAt"`
	UpdatedAt      time.Time  `json:"updatedAt"`
}

func toInvitationResponse(invite domain.Invitation) invitationResponse {
	return invitationResponse{
		ID:             invite.ID,
		OrgID:          invite.OrgID,
		Email:          invite.Email,
		InvitedByEmail: invite.InvitedByEmail,
		InvitedByName:  invite.InvitedByName,
		Role:           invite.Role,
		Status:         invite.Status,
		ExpiresAt:      invite.ExpiresAt,
		AcceptedAt:     invite.AcceptedAt,
		DeclinedAt:     invite.DeclinedAt,
		RevokedAt:      invite.RevokedAt,
		CreatedAt:      invite.CreatedAt,
		UpdatedAt:      invite.UpdatedAt,
	}
}

func invitableOrgRole(role string) bool {
	return role == "admin" || role == "member"
}

type createOrganizationRequest struct {
	DisplayName string `json:"displayName"`
}

func (s *Server) createOrganization(w http.ResponseWriter, r *http.Request) {
	var request createOrganizationRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "The request body is invalid.")
		return
	}
	request.DisplayName = strings.TrimSpace(request.DisplayName)
	if request.DisplayName == "" || len(request.DisplayName) > 80 {
		writeError(w, r, http.StatusUnprocessableEntity, "validation_error", "Workspace name must be between 1 and 80 characters.")
		return
	}
	membership, err := s.store.CreateOrganization(r.Context(), principalFrom(r), request.DisplayName)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"organization": organizationResponse{
		ID: membership.OrgID, Slug: membership.OrgSlug,
		DisplayName: membership.DisplayName, Role: membership.Role,
	}})
}

func (s *Server) listOrgMembers(w http.ResponseWriter, r *http.Request) {
	orgID := chi.URLParam(r, "orgId")
	if requireUUID(orgID, "orgId") != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "orgId must be a UUID.")
		return
	}
	members, err := s.store.ListOrgMembers(r.Context(), principalFrom(r), orgID)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	items := make([]orgMemberResponse, 0, len(members))
	for _, member := range members {
		items = append(items, toOrgMemberResponse(member))
	}
	writeJSON(w, http.StatusOK, map[string]any{"members": items})
}

type updateOrgMemberRoleRequest struct {
	Role string `json:"role"`
}

func (s *Server) updateOrgMemberRole(w http.ResponseWriter, r *http.Request) {
	orgID := chi.URLParam(r, "orgId")
	userID := chi.URLParam(r, "userId")
	if requireUUID(orgID, "orgId") != nil || requireUUID(userID, "userId") != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "orgId and userId must be UUIDs.")
		return
	}
	var request updateOrgMemberRoleRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "The request body is invalid.")
		return
	}
	request.Role = strings.TrimSpace(request.Role)
	if request.Role != "owner" && request.Role != "admin" && request.Role != "member" {
		writeError(w, r, http.StatusUnprocessableEntity, "validation_error", "Role must be owner, admin, or member.")
		return
	}
	member, err := s.store.UpdateOrgMemberRole(r.Context(), principalFrom(r), orgID, userID, request.Role)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"member": toOrgMemberResponse(member)})
}

func (s *Server) listOrgInvitations(w http.ResponseWriter, r *http.Request) {
	orgID := chi.URLParam(r, "orgId")
	if requireUUID(orgID, "orgId") != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "orgId must be a UUID.")
		return
	}
	invitations, err := s.store.ListOrgInvitations(r.Context(), principalFrom(r), orgID)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	items := make([]invitationResponse, 0, len(invitations))
	for _, invite := range invitations {
		items = append(items, toInvitationResponse(invite))
	}
	writeJSON(w, http.StatusOK, map[string]any{"invitations": items})
}

func (s *Server) listMyInvitations(w http.ResponseWriter, r *http.Request) {
	invitations, err := s.store.ListMyInvitations(r.Context(), principalFrom(r))
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	items := make([]invitationResponse, 0, len(invitations))
	for _, invite := range invitations {
		items = append(items, toInvitationResponse(invite))
	}
	writeJSON(w, http.StatusOK, map[string]any{"invitations": items})
}

type createInvitationRequest struct {
	Email string `json:"email"`
	Role  string `json:"role"`
}

func (s *Server) createOrgInvitation(w http.ResponseWriter, r *http.Request) {
	orgID := chi.URLParam(r, "orgId")
	if requireUUID(orgID, "orgId") != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "orgId must be a UUID.")
		return
	}
	var request createInvitationRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "The request body is invalid.")
		return
	}
	email := strings.ToLower(strings.TrimSpace(request.Email))
	if !validEmail(email) {
		writeError(w, r, http.StatusUnprocessableEntity, "validation_error", "A valid invite email is required.")
		return
	}
	role := strings.TrimSpace(request.Role)
	if role == "" {
		role = "member"
	}
	if !invitableOrgRole(role) {
		writeError(w, r, http.StatusUnprocessableEntity, "validation_error", "Invite role must be admin or member.")
		return
	}
	invitation, err := s.store.CreateOrgInvitation(r.Context(), principalFrom(r), orgID, domain.CreateInvitation{
		Email: email,
		Role:  role,
	})
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"invitation": toInvitationResponse(invitation)})
}

func (s *Server) revokeOrgInvitation(w http.ResponseWriter, r *http.Request) {
	orgID := chi.URLParam(r, "orgId")
	invitationID := chi.URLParam(r, "invitationId")
	if requireUUID(orgID, "orgId") != nil || requireUUID(invitationID, "invitationId") != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "orgId and invitationId must be UUIDs.")
		return
	}
	if err := s.store.RevokeOrgInvitation(r.Context(), principalFrom(r), orgID, invitationID); err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) getOrgInvitation(w http.ResponseWriter, r *http.Request) {
	orgID := chi.URLParam(r, "orgId")
	invitationID := chi.URLParam(r, "invitationId")
	if requireUUID(orgID, "orgId") != nil || requireUUID(invitationID, "invitationId") != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "orgId and invitationId must be UUIDs.")
		return
	}
	invitation, err := s.store.GetOrgInvitation(r.Context(), principalFrom(r), orgID, invitationID)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"invitation": toInvitationResponse(invitation)})
}

func (s *Server) acceptOrgInvitation(w http.ResponseWriter, r *http.Request) {
	orgID := chi.URLParam(r, "orgId")
	invitationID := chi.URLParam(r, "invitationId")
	if requireUUID(orgID, "orgId") != nil || requireUUID(invitationID, "invitationId") != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "orgId and invitationId must be UUIDs.")
		return
	}
	membership, err := s.store.AcceptOrgInvitation(r.Context(), principalFrom(r), orgID, invitationID)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"organization": organizationResponse{
		ID:          membership.OrgID,
		Slug:        membership.OrgSlug,
		DisplayName: membership.DisplayName,
		Role:        membership.Role,
	}})
}

func (s *Server) declineOrgInvitation(w http.ResponseWriter, r *http.Request) {
	orgID := chi.URLParam(r, "orgId")
	invitationID := chi.URLParam(r, "invitationId")
	if requireUUID(orgID, "orgId") != nil || requireUUID(invitationID, "invitationId") != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "orgId and invitationId must be UUIDs.")
		return
	}
	if err := s.store.DeclineOrgInvitation(r.Context(), principalFrom(r), orgID, invitationID); err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
