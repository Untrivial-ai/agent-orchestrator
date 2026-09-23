package githubpat

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func jsonResp(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func githubClient(status int) *http.Client {
	return &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: status,
			Body:       io.NopCloser(strings.NewReader(`{"message":"test response"}`)),
			Header:     make(http.Header),
		}, nil
	})}
}

func TestListReposKeepsTokenOnUnauthorized(t *testing.T) {
	service := newWithClient(t.TempDir(), githubClient(http.StatusUnauthorized), "https://github.example")
	if err := service.StorePAT(context.Background(), "rejected-token"); err != nil {
		t.Fatalf("StorePAT: %v", err)
	}

	_, err := service.ListRepos(context.Background())
	if !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("ListRepos error = %v, want ErrInvalidCredentials", err)
	}
	// A 401 must NOT delete the stored token. A transient GitHub 401 would
	// otherwise permanently wipe a still-valid credential and force a full
	// reconnect. The token is preserved until the user reconnects (overwrite)
	// or explicitly disconnects (DeletePAT).
	if !service.HasPAT(context.Background()) {
		t.Fatal("HasPAT = false: token was deleted on a 401 (it must be preserved)")
	}
}

func TestListReposDoesNotRemoveTokenForTransientFailure(t *testing.T) {
	service := newWithClient(t.TempDir(), githubClient(http.StatusInternalServerError), "https://github.example")
	if err := service.StorePAT(context.Background(), "still-valid-token"); err != nil {
		t.Fatalf("StorePAT: %v", err)
	}

	if _, err := service.ListRepos(context.Background()); err == nil {
		t.Fatal("ListRepos error = nil, want failure")
	}
	if !service.HasPAT(context.Background()) {
		t.Fatal("HasPAT = false after a transient GitHub failure")
	}
}

// oauthRoutingClient routes the refresh POST and the repos GET, minting
// "new-token" on refresh and only accepting it on /user/repos.
func oauthRoutingClient(refreshCalls *int) *http.Client {
	return &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch {
		case strings.Contains(r.URL.Path, "/login/oauth/access_token"):
			*refreshCalls++
			return jsonResp(http.StatusOK, `{"access_token":"new-token","refresh_token":"new-refresh","expires_in":28800,"refresh_token_expires_in":15552000}`), nil
		case strings.Contains(r.URL.Path, "/user/repos"):
			if r.Header.Get("Authorization") != "Bearer new-token" {
				return jsonResp(http.StatusUnauthorized, `{"message":"bad credentials"}`), nil
			}
			return jsonResp(http.StatusOK, `[{"name":"r","full_name":"o/r","private":false,"default_branch":"main","clone_url":"https://github.com/o/r.git"}]`), nil
		default:
			return jsonResp(http.StatusNotFound, `{}`), nil
		}
	})}
}

func oauthService(t *testing.T, refreshCalls *int) *Service {
	t.Helper()
	s := newWithClient(t.TempDir(), oauthRoutingClient(refreshCalls), "https://api.github.example")
	s.oauthURL = "https://github.example"
	s.clientID = "client-id"
	s.clientSecret = "client-secret"
	return s
}

func TestListReposRefreshesExpiringToken(t *testing.T) {
	var refreshCalls int
	s := oauthService(t, &refreshCalls)
	// An OAuth token already past its expiry: ListRepos must renew it proactively
	// before calling GitHub, not force the user to reconnect.
	past := time.Now().Add(-time.Hour)
	future := time.Now().Add(30 * 24 * time.Hour)
	if err := s.StoreOAuthToken(context.Background(), "old-token", "old-refresh", past, future); err != nil {
		t.Fatalf("StoreOAuthToken: %v", err)
	}
	repos, err := s.ListRepos(context.Background())
	if err != nil {
		t.Fatalf("ListRepos: %v", err)
	}
	if len(repos) != 1 || repos[0].FullName != "o/r" {
		t.Fatalf("repos = %+v, want one o/r", repos)
	}
	if refreshCalls != 1 {
		t.Fatalf("refreshCalls = %d, want 1 (proactive refresh)", refreshCalls)
	}
	if tok, _ := s.GetToken(context.Background()); tok != "new-token" {
		t.Fatalf("stored token = %q, want new-token", tok)
	}
}

func TestListReposForceRefreshesOnUnexpected401(t *testing.T) {
	var refreshCalls int
	s := oauthService(t, &refreshCalls)
	// Token is not yet expired by its recorded deadline, so no proactive refresh;
	// GitHub rejects it anyway (early expiry / rotation). One forced refresh +
	// retry must recover without a reconnect prompt.
	accessExpiry := time.Now().Add(time.Hour)
	refreshExpiry := time.Now().Add(180 * 24 * time.Hour)
	if err := s.StoreOAuthToken(context.Background(), "old-token", "old-refresh", accessExpiry, refreshExpiry); err != nil {
		t.Fatalf("StoreOAuthToken: %v", err)
	}
	repos, err := s.ListRepos(context.Background())
	if err != nil {
		t.Fatalf("ListRepos: %v", err)
	}
	if len(repos) != 1 {
		t.Fatalf("repos = %+v, want one", repos)
	}
	if refreshCalls != 1 {
		t.Fatalf("refreshCalls = %d, want 1 (forced refresh on 401)", refreshCalls)
	}
}

func TestListReposNoRefreshMaterialStaysInvalidOn401(t *testing.T) {
	var refreshCalls int
	s := oauthService(t, &refreshCalls)
	// A plain PAT (no refresh token) that GitHub rejects must surface
	// ErrInvalidCredentials without attempting a refresh.
	if err := s.StorePAT(context.Background(), "plain-rejected"); err != nil {
		t.Fatalf("StorePAT: %v", err)
	}
	_, err := s.ListRepos(context.Background())
	if !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("ListRepos error = %v, want ErrInvalidCredentials", err)
	}
	if refreshCalls != 0 {
		t.Fatalf("refreshCalls = %d, want 0 (no refresh token to use)", refreshCalls)
	}
}
