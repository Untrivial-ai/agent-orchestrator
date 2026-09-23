package controllers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/envelope"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/githubpat"
)

type invalidGitHubPATService struct{}

func (invalidGitHubPATService) StorePAT(context.Context, string) error { return nil }
func (invalidGitHubPATService) StoreOAuthToken(context.Context, string, string, time.Time, time.Time) error {
	return nil
}
func (invalidGitHubPATService) HasPAT(context.Context) bool { return true }
func (invalidGitHubPATService) DeletePAT(context.Context) error {
	return nil
}
func (invalidGitHubPATService) ListRepos(context.Context) ([]githubpat.Repo, error) {
	return nil, githubpat.ErrInvalidCredentials
}

func TestGitHubListReposReportsInvalidCredentials(t *testing.T) {
	router := chi.NewRouter()
	controller := GitHubController{Svc: invalidGitHubPATService{}}
	controller.Register(router)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/github/repos", nil)
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusUnauthorized, recorder.Body.String())
	}
	var response envelope.APIError
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Code != "GITHUB_AUTH_INVALID" {
		t.Fatalf("code = %q, want GITHUB_AUTH_INVALID", response.Code)
	}
}
