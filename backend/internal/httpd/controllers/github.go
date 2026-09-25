package controllers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apispec"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/envelope"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/githubpat"
)

// GitHubPATService is the controller-facing contract for local GitHub PAT management.
type GitHubPATService interface {
	StorePAT(ctx context.Context, token string) error
	StoreOAuthToken(ctx context.Context, token, refreshToken string, expiresAt, refreshTokenExpiresAt time.Time) error
	HasPAT(ctx context.Context) bool
	DeletePAT(ctx context.Context) error
	ListRepos(ctx context.Context) ([]githubpat.Repo, error)
}

// GitHubController owns the local GitHub PAT + repos routes.
type GitHubController struct {
	Svc GitHubPATService
}

// Register mounts the GitHub routes on the supplied router.
func (c *GitHubController) Register(r chi.Router) {
	r.Put("/github/pat", c.putPAT)
	r.Delete("/github/pat", c.deletePAT)
	r.Get("/github/status", c.status)
	r.Get("/github/repos", c.listRepos)
}

// PutGitHubPATRequest is the request body for storing a GitHub PAT. For an
// expiring GitHub App OAuth token, the optional refresh fields let the daemon
// renew the token without a manual reconnect; a plain PAT sends only 'pat'.
type PutGitHubPATRequest struct {
	PAT string `json:"pat"`
	// Optional OAuth refresh material. RefreshToken empty => stored as a plain,
	// non-refreshable credential. ExpiresIn / RefreshTokenExpiresIn are seconds
	// from now, as returned by GitHub's token exchange.
	RefreshToken          string `json:"refreshToken,omitempty"`
	ExpiresIn             int    `json:"expiresIn,omitempty"`
	RefreshTokenExpiresIn int    `json:"refreshTokenExpiresIn,omitempty"`
}

func (c *GitHubController) putPAT(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, http.MethodPut, "/api/v1/github/pat")
		return
	}
	var body PutGitHubPATRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.PAT == "" {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_REQUEST", "A non-empty 'pat' field is required.", nil)
		return
	}
	var err error
	if body.RefreshToken != "" {
		now := time.Now().UTC()
		var expiresAt, refreshExpiresAt time.Time
		if body.ExpiresIn > 0 {
			expiresAt = now.Add(time.Duration(body.ExpiresIn) * time.Second)
		}
		if body.RefreshTokenExpiresIn > 0 {
			refreshExpiresAt = now.Add(time.Duration(body.RefreshTokenExpiresIn) * time.Second)
		}
		err = c.Svc.StoreOAuthToken(r.Context(), body.PAT, body.RefreshToken, expiresAt, refreshExpiresAt)
	} else {
		err = c.Svc.StorePAT(r.Context(), body.PAT)
	}
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (c *GitHubController) deletePAT(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, http.MethodDelete, "/api/v1/github/pat")
		return
	}
	if err := c.Svc.DeletePAT(r.Context()); err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (c *GitHubController) status(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, http.MethodGet, "/api/v1/github/status")
		return
	}
	envelope.WriteJSON(w, http.StatusOK, map[string]bool{"connected": c.Svc.HasPAT(r.Context())})
}

func (c *GitHubController) listRepos(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, http.MethodGet, "/api/v1/github/repos")
		return
	}
	repos, err := c.Svc.ListRepos(r.Context())
	if err != nil {
		if errors.Is(err, githubpat.ErrInvalidCredentials) {
			envelope.WriteAPIError(w, r, http.StatusUnauthorized, "unauthorized", "GITHUB_AUTH_INVALID", "GitHub authorization expired. Reconnect GitHub to continue.", nil)
			return
		}
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, map[string]any{"repos": repos})
}
