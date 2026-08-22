package httpapi

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/cloud/auth"
	"github.com/aoagents/agent-orchestrator/backend/internal/cloud/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/cloud/postgres"
)

type googleExchangeRequest struct {
	IDToken string `json:"idToken"`
}

type refreshRequest struct {
	RefreshToken string `json:"refreshToken"`
}

type currentUser struct {
	ID           string `json:"id"`
	Email        string `json:"email"`
	DisplayName  string `json:"displayName"`
	AuthProvider string `json:"authProvider"`
}

type organizationMembership struct {
	ID          string `json:"id"`
	Slug        string `json:"slug"`
	DisplayName string `json:"displayName"`
	Role        string `json:"role"`
}

type currentAccount struct {
	User          currentUser              `json:"user"`
	Organizations []organizationMembership `json:"organizations"`
}

type sessionResponse struct {
	AccessToken   string                   `json:"accessToken"`
	RefreshToken  string                   `json:"refreshToken"`
	ExpiresAt     time.Time                `json:"expiresAt"`
	User          currentUser              `json:"user"`
	Organizations []organizationMembership `json:"organizations"`
}

func (s *Server) exchangeGoogle(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	var input googleExchangeRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	if err := requireValue(input.IDToken, "idToken"); err != nil {
		writeError(w, r, http.StatusBadRequest, "bad_request", "ID_TOKEN_REQUIRED", err.Error())
		return
	}
	if len(input.IDToken) > 64<<10 {
		writeError(w, r, http.StatusBadRequest, "bad_request", "ID_TOKEN_INVALID", "idToken is too large")
		return
	}
	principal, err := s.google.Verify(r.Context(), input.IDToken)
	if err != nil {
		writeError(w, r, http.StatusUnauthorized, "unauthorized", "INVALID_GOOGLE_ID_TOKEN", "Google identity could not be verified")
		return
	}
	principal, err = s.store.UpsertGoogleUser(r.Context(), principal)
	if err != nil {
		if errors.Is(err, postgres.ErrConflict) {
			writeError(w, r, http.StatusConflict, "conflict", "ACCOUNT_CONFLICT", "account conflicts with an existing record")
			return
		}
		s.internalError(w, r, "upsert Google user", err)
		return
	}
	s.issueInitialSession(w, r, principal)
}

func (s *Server) issueInitialSession(w http.ResponseWriter, r *http.Request, principal domain.Principal) {
	accessToken, expiresAt, err := s.accessTokens.Issue(principal.UserID)
	if err != nil {
		s.internalError(w, r, "issue access token", err)
		return
	}
	refreshToken, refreshHash, err := auth.NewRefreshToken()
	if err != nil {
		s.internalError(w, r, "issue refresh token", err)
		return
	}
	if err := s.store.CreateRefreshSession(
		r.Context(),
		principal.UserID,
		refreshHash,
		time.Now().UTC().Add(s.refreshTokenTTL),
	); err != nil {
		s.internalError(w, r, "persist refresh token", err)
		return
	}
	s.writeSession(w, r, principal, accessToken, refreshToken, expiresAt)
}

func (s *Server) refresh(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	var input refreshRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	if err := requireValue(input.RefreshToken, "refreshToken"); err != nil {
		writeError(w, r, http.StatusBadRequest, "bad_request", "REFRESH_TOKEN_REQUIRED", err.Error())
		return
	}
	if !strings.HasPrefix(input.RefreshToken, "ao_refresh_") {
		writeError(w, r, http.StatusUnauthorized, "unauthorized", "INVALID_REFRESH_TOKEN", "refresh token is invalid or expired")
		return
	}
	newToken, newHash, err := auth.NewRefreshToken()
	if err != nil {
		s.internalError(w, r, "issue replacement refresh token", err)
		return
	}
	principal, err := s.store.RotateRefreshSession(
		r.Context(),
		auth.HashToken(input.RefreshToken),
		newHash,
	)
	if err != nil {
		if errors.Is(err, postgres.ErrNotFound) {
			writeError(w, r, http.StatusUnauthorized, "unauthorized", "INVALID_REFRESH_TOKEN", "refresh token is invalid or expired")
			return
		}
		s.internalError(w, r, "rotate refresh token", err)
		return
	}
	accessToken, expiresAt, err := s.accessTokens.Issue(principal.UserID)
	if err != nil {
		s.internalError(w, r, "issue refreshed access token", err)
		return
	}
	s.writeSession(w, r, principal, accessToken, newToken, expiresAt)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	var input refreshRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	if err := requireValue(input.RefreshToken, "refreshToken"); err != nil {
		writeError(w, r, http.StatusBadRequest, "bad_request", "REFRESH_TOKEN_REQUIRED", err.Error())
		return
	}
	if !strings.HasPrefix(input.RefreshToken, "ao_refresh_") {
		writeError(w, r, http.StatusUnauthorized, "unauthorized", "INVALID_REFRESH_TOKEN", "refresh token is invalid or expired")
		return
	}
	if err := s.store.RevokeRefreshSession(r.Context(), auth.HashToken(input.RefreshToken)); err != nil {
		s.internalError(w, r, "revoke refresh token", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	principal, ok := principalFromContext(r.Context())
	if !ok {
		writeError(w, r, http.StatusUnauthorized, "unauthorized", "AUTH_REQUIRED", "valid AO access token required")
		return
	}
	account, err := s.account(r, principal)
	if err != nil {
		s.internalError(w, r, "load account", err)
		return
	}
	writeJSON(w, http.StatusOK, account)
}

func (s *Server) writeSession(
	w http.ResponseWriter,
	r *http.Request,
	principal domain.Principal,
	accessToken, refreshToken string,
	expiresAt time.Time,
) {
	account, err := s.account(r, principal)
	if err != nil {
		s.internalError(w, r, "load account", err)
		return
	}
	writeJSON(w, http.StatusOK, sessionResponse{
		AccessToken:   accessToken,
		RefreshToken:  refreshToken,
		ExpiresAt:     expiresAt,
		User:          account.User,
		Organizations: account.Organizations,
	})
}

func (s *Server) account(r *http.Request, principal domain.Principal) (currentAccount, error) {
	memberships, err := s.store.ListMemberships(r.Context(), principal)
	if err != nil {
		return currentAccount{}, err
	}
	organizations := make([]organizationMembership, 0, len(memberships))
	for _, membership := range memberships {
		organizations = append(organizations, organizationMembership{
			ID:          membership.OrgID,
			Slug:        membership.OrgSlug,
			DisplayName: membership.DisplayName,
			Role:        membership.Role,
		})
	}
	return currentAccount{
		User: currentUser{
			ID:           principal.UserID,
			Email:        principal.Email,
			DisplayName:  principal.DisplayName,
			AuthProvider: principal.Provider,
		},
		Organizations: organizations,
	}, nil
}
