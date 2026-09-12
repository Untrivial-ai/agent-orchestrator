package httpapi

import (
	"errors"
	"net/http"
	"net/mail"
	"regexp"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/cloud/internal/auth"
	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/aoagents/agent-orchestrator/cloud/internal/postgres"
)

var orgSlugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,62}$`)

const maxLocalPasswordBytes = 72

type localRegisterRequest struct {
	Email       string `json:"email"`
	DisplayName string `json:"displayName"`
	Password    string `json:"password"`
	OrgSlug     string `json:"orgSlug"`
	OrgName     string `json:"orgName"`
}

type localLoginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type userResponse struct {
	ID          string `json:"id"`
	Email       string `json:"email"`
	DisplayName string `json:"displayName"`
	Provider    string `json:"authProvider"`
}

type organizationResponse struct {
	ID          string `json:"id"`
	Slug        string `json:"slug"`
	DisplayName string `json:"displayName"`
	Role        string `json:"role"`
}

type authResponse struct {
	Token         string                 `json:"token"`
	ExpiresAt     time.Time              `json:"expiresAt"`
	User          userResponse           `json:"user"`
	Organizations []organizationResponse `json:"organizations"`
}

func (s *Server) registerLocal(w http.ResponseWriter, r *http.Request) {
	if !s.localAuthEnabled {
		writeError(w, r, http.StatusNotFound, "not_found", "Local authentication is disabled.")
		return
	}
	if !s.allowLocalAuthAttempt(w, r) {
		return
	}
	var request localRegisterRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "The request body is invalid.")
		return
	}
	request.Email = strings.ToLower(strings.TrimSpace(request.Email))
	request.DisplayName = strings.TrimSpace(request.DisplayName)
	request.OrgSlug = strings.ToLower(strings.TrimSpace(request.OrgSlug))
	request.OrgName = strings.TrimSpace(request.OrgName)
	if !validEmail(request.Email) ||
		len(request.DisplayName) < 1 || len(request.DisplayName) > 120 ||
		len(request.Password) < 12 || len(request.Password) > maxLocalPasswordBytes ||
		!orgSlugPattern.MatchString(request.OrgSlug) ||
		len(request.OrgName) < 1 || len(request.OrgName) > 120 {
		writeError(w, r, http.StatusUnprocessableEntity, "validation_error", "Email, name, password, or organization details are invalid.")
		return
	}
	passwordHash, err := auth.HashPassword(request.Password)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	token, tokenHash, err := auth.NewOpaqueToken()
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	expiresAt := time.Now().UTC().Add(s.localSessionTTL)
	principal, orgID, err := s.store.RegisterLocal(r.Context(), domain.LocalRegistration{
		Email:        request.Email,
		DisplayName:  request.DisplayName,
		PasswordHash: passwordHash,
		OrgSlug:      request.OrgSlug,
		OrgName:      request.OrgName,
	}, tokenHash, expiresAt)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, authResponse{
		Token:     token,
		ExpiresAt: expiresAt,
		User:      toUserResponse(principal),
		Organizations: []organizationResponse{{
			ID:          orgID,
			Slug:        request.OrgSlug,
			DisplayName: request.OrgName,
			Role:        "owner",
		}},
	})
}

func (s *Server) loginLocal(w http.ResponseWriter, r *http.Request) {
	if !s.localAuthEnabled {
		writeError(w, r, http.StatusNotFound, "not_found", "Local authentication is disabled.")
		return
	}
	if !s.allowLocalAuthAttempt(w, r) {
		return
	}
	var request localLoginRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "The request body is invalid.")
		return
	}
	if len(request.Password) > maxLocalPasswordBytes {
		writeError(w, r, http.StatusUnauthorized, "invalid_credentials", "The email or password is incorrect.")
		return
	}
	principal, passwordHash, err := s.store.LocalUserByEmail(r.Context(), request.Email)
	if err != nil && !errors.Is(err, postgres.ErrNotFound) {
		s.writeStoreError(w, r, err)
		return
	}
	if !auth.VerifyPassword(passwordHash, request.Password) || errors.Is(err, postgres.ErrNotFound) {
		writeError(w, r, http.StatusUnauthorized, "invalid_credentials", "The email or password is incorrect.")
		return
	}
	memberships, err := s.store.ListMemberships(r.Context(), principal)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	token, tokenHash, err := auth.NewOpaqueToken()
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	expiresAt := time.Now().UTC().Add(s.localSessionTTL)
	if err := s.store.CreateLocalSession(r.Context(), principal.UserID, tokenHash, expiresAt); err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, authResponse{
		Token:         token,
		ExpiresAt:     expiresAt,
		User:          toUserResponse(principal),
		Organizations: toOrganizations(memberships),
	})
}

func (s *Server) logoutLocal(w http.ResponseWriter, r *http.Request) {
	token := bearerFrom(r)
	if !strings.HasPrefix(token, "ao_local_") {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "WorkOS sessions must be signed out through WorkOS.")
		return
	}
	if err := s.store.RevokeLocalSession(r.Context(), auth.HashToken(token)); err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	principal := principalFrom(r)
	memberships, err := s.store.ListMemberships(r.Context(), principal)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"user":          toUserResponse(principal),
		"organizations": toOrganizations(memberships),
	})
}

func toUserResponse(principal domain.Principal) userResponse {
	return userResponse{
		ID:          principal.UserID,
		Email:       principal.Email,
		DisplayName: principal.DisplayName,
		Provider:    principal.Provider,
	}
}

func toOrganizations(memberships []domain.Membership) []organizationResponse {
	organizations := make([]organizationResponse, 0, len(memberships))
	for _, membership := range memberships {
		organizations = append(organizations, organizationResponse{
			ID:          membership.OrgID,
			Slug:        membership.OrgSlug,
			DisplayName: membership.DisplayName,
			Role:        membership.Role,
		})
	}
	return organizations
}

func validEmail(value string) bool {
	address, err := mail.ParseAddress(value)
	return err == nil && strings.EqualFold(address.Address, value)
}

func (s *Server) allowLocalAuthAttempt(w http.ResponseWriter, r *http.Request) bool {
	if s.localAuthLimiter.allow(localAuthRateLimitKey(r), time.Now()) {
		return true
	}
	w.Header().Set("Retry-After", "60")
	writeError(w, r, http.StatusTooManyRequests, "rate_limited", "Too many authentication attempts.")
	return false
}
