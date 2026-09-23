package githubpat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// ErrInvalidCredentials reports that GitHub rejected the stored credential.
var ErrInvalidCredentials = errors.New("GitHub credentials are invalid")

// refreshSkew renews an expiring OAuth token a little before its deadline so a
// call never races the expiry.
const refreshSkew = 2 * time.Minute

// Repo represents a GitHub repository returned by the GitHub API.
type Repo struct {
	Name          string `json:"name"`
	FullName      string `json:"full_name"`
	Private       bool   `json:"private"`
	DefaultBranch string `json:"default_branch"`
	CloneURL      string `json:"clone_url"`
}

// Service manages the locally stored GitHub PAT and proxies GitHub API calls.
type Service struct {
	dataDir      string
	httpClient   *http.Client
	apiURL       string
	oauthURL     string
	clientID     string
	clientSecret string
	now          func() time.Time
	mu           sync.Mutex
}

// New returns a Service rooted at dataDir. GitHub App user tokens expire (8h by
// default) and arrive with a refresh token; the OAuth client credentials the
// desktop app already uses for the initial exchange are read from the
// environment so the daemon can renew an expiring token transparently rather
// than forcing the user to reconnect.
func New(dataDir string) *Service {
	s := newWithClient(dataDir, http.DefaultClient, "https://api.github.com")
	s.oauthURL = "https://github.com"
	s.clientID = os.Getenv("AO_GITHUB_OAUTH_CLIENT_ID")
	s.clientSecret = os.Getenv("AO_GITHUB_OAUTH_CLIENT_SECRET")
	return s
}

func newWithClient(dataDir string, client *http.Client, apiURL string) *Service {
	return &Service{dataDir: dataDir, httpClient: client, apiURL: apiURL, oauthURL: "https://github.com", now: time.Now}
}

func (s *Service) patPath() string {
	return filepath.Join(s.dataDir, "github-pat.json")
}

func (s *Service) clock() time.Time {
	if s.now != nil {
		return s.now().UTC()
	}
	return time.Now().UTC()
}

type storedPAT struct {
	Token     string    `json:"token"`
	CreatedAt time.Time `json:"created_at"`
	// The following are populated only for expiring GitHub App OAuth tokens. A
	// plain PAT (ghp_) or a non-expiring OAuth token leaves them zero, and the
	// refresh path is skipped.
	RefreshToken          string    `json:"refresh_token,omitempty"`
	ExpiresAt             time.Time `json:"expires_at,omitempty"`
	RefreshTokenExpiresAt time.Time `json:"refresh_token_expires_at,omitempty"`
}

// StorePAT persists a GitHub personal access token (or a non-expiring OAuth
// token) with no refresh material.
func (s *Service) StorePAT(_ context.Context, token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.writeLocked(storedPAT{Token: token, CreatedAt: s.clock()})
}

// StoreOAuthToken persists an expiring GitHub App OAuth token together with the
// refresh token and expiries needed to renew it without user interaction. A zero
// expiresAt means the token does not expire (behaves like StorePAT).
func (s *Service) StoreOAuthToken(_ context.Context, token, refreshToken string, expiresAt, refreshTokenExpiresAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.writeLocked(storedPAT{
		Token:                 token,
		CreatedAt:             s.clock(),
		RefreshToken:          refreshToken,
		ExpiresAt:             expiresAt.UTC(),
		RefreshTokenExpiresAt: refreshTokenExpiresAt.UTC(),
	})
}

// writeLocked persists a record. The caller must hold s.mu.
func (s *Service) writeLocked(rec storedPAT) error {
	if err := os.MkdirAll(s.dataDir, 0o700); err != nil {
		return fmt.Errorf("creating data dir: %w", err)
	}
	data, err := json.Marshal(rec) //nolint:gosec // G117: storedPAT intentionally persists the OAuth refresh token to a 0600 local file so the daemon can renew the GitHub App token without a user reconnect.
	if err != nil {
		return err
	}
	return os.WriteFile(s.patPath(), data, 0o600)
}

// HasPAT reports whether a GitHub PAT is stored locally.
func (s *Service) HasPAT(_ context.Context) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(s.patPath())
	if err != nil {
		return false
	}
	var stored storedPAT
	return json.Unmarshal(data, &stored) == nil && stored.Token != ""
}

// DeletePAT removes the stored GitHub PAT.
func (s *Service) DeletePAT(_ context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	err := os.Remove(s.patPath())
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func (s *Service) loadStoredLocked() (storedPAT, error) {
	data, err := os.ReadFile(s.patPath())
	if err != nil {
		return storedPAT{}, err
	}
	var stored storedPAT
	if err := json.Unmarshal(data, &stored); err != nil {
		return storedPAT{}, err
	}
	if stored.Token == "" {
		return storedPAT{}, fmt.Errorf("no GitHub token stored")
	}
	return stored, nil
}

// accessToken returns a usable token, renewing it first when it is an expiring
// OAuth token that is at or near its deadline. It never fails the caller just
// because a refresh could not be attempted: it falls back to the stored token,
// letting the live GitHub call be the source of truth on validity.
func (s *Service) accessToken(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	stored, err := s.loadStoredLocked()
	if err != nil {
		return "", err
	}
	if s.shouldRefresh(stored) {
		if refreshed, rerr := s.refreshLocked(ctx, stored); rerr == nil {
			return refreshed.Token, nil
		}
		// Refresh failed (network, refresh token expired/revoked); use the stored
		// token and let the GitHub call decide. A 401 there triggers a forced
		// refresh + retry.
	}
	return stored.Token, nil
}

// forceRefresh renews the token regardless of its deadline, used after GitHub
// unexpectedly rejects a token that had not yet reached expires_at.
func (s *Service) forceRefresh(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	stored, err := s.loadStoredLocked()
	if err != nil {
		return "", err
	}
	if !s.canRefresh(stored) {
		return "", ErrInvalidCredentials
	}
	refreshed, err := s.refreshLocked(ctx, stored)
	if err != nil {
		return "", err
	}
	return refreshed.Token, nil
}

// shouldRefresh reports whether an expiring token is close enough to its
// deadline to renew proactively.
func (s *Service) shouldRefresh(stored storedPAT) bool {
	if !s.canRefresh(stored) || stored.ExpiresAt.IsZero() {
		return false
	}
	return !s.clock().Before(stored.ExpiresAt.Add(-refreshSkew))
}

// canRefresh reports whether the material needed to renew the token is present:
// a refresh token, the OAuth client credentials, and a refresh token that has
// not itself expired.
func (s *Service) canRefresh(stored storedPAT) bool {
	if stored.RefreshToken == "" || s.clientID == "" || s.clientSecret == "" {
		return false
	}
	if !stored.RefreshTokenExpiresAt.IsZero() && !s.clock().Before(stored.RefreshTokenExpiresAt) {
		return false
	}
	return true
}

// refreshLocked exchanges the refresh token for a fresh access token (and a
// rotated refresh token) and persists the result. The caller must hold s.mu.
func (s *Service) refreshLocked(ctx context.Context, stored storedPAT) (storedPAT, error) {
	// Bound the refresh so a hung GitHub OAuth endpoint cannot hold s.mu (and
	// thus stall concurrent ListRepos/HasPAT) for the whole request lifetime.
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	body, err := json.Marshal(map[string]string{
		"client_id":     s.clientID,
		"client_secret": s.clientSecret,
		"grant_type":    "refresh_token",
		"refresh_token": stored.RefreshToken,
	})
	if err != nil {
		return storedPAT{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.oauthURL+"/login/oauth/access_token", bytes.NewReader(body))
	if err != nil {
		return storedPAT{}, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return storedPAT{}, fmt.Errorf("github token refresh: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return storedPAT{}, fmt.Errorf("github token refresh: status %d", resp.StatusCode)
	}
	var out struct {
		AccessToken           string `json:"access_token"`
		RefreshToken          string `json:"refresh_token"`
		ExpiresIn             int    `json:"expires_in"`
		RefreshTokenExpiresIn int    `json:"refresh_token_expires_in"`
		Error                 string `json:"error"`
		ErrorDescription      string `json:"error_description"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return storedPAT{}, fmt.Errorf("github token refresh: decode: %w", err)
	}
	if out.Error != "" {
		return storedPAT{}, fmt.Errorf("github token refresh: %s", out.Error)
	}
	if out.AccessToken == "" {
		return storedPAT{}, fmt.Errorf("github token refresh: no access token returned")
	}
	now := s.clock()
	refreshed := storedPAT{
		Token:                 out.AccessToken,
		CreatedAt:             now,
		RefreshToken:          firstNonEmpty(out.RefreshToken, stored.RefreshToken),
		ExpiresAt:             expiryFrom(now, out.ExpiresIn, stored.ExpiresAt),
		RefreshTokenExpiresAt: expiryFrom(now, out.RefreshTokenExpiresIn, stored.RefreshTokenExpiresAt),
	}
	if err := s.writeLocked(refreshed); err != nil {
		return storedPAT{}, err
	}
	return refreshed, nil
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// expiryFrom converts an OAuth expires_in (seconds) to an absolute deadline,
// falling back to the prior deadline when the field is absent (0).
func expiryFrom(now time.Time, seconds int, prior time.Time) time.Time {
	if seconds > 0 {
		return now.Add(time.Duration(seconds) * time.Second)
	}
	return prior
}

// ListRepos calls the GitHub API to list repositories accessible with the
// stored token, sorted by most recently updated. An expiring token is renewed
// before the call; an unexpected 401 triggers one forced refresh + retry before
// the credential is reported invalid.
func (s *Service) ListRepos(ctx context.Context) ([]Repo, error) {
	token, err := s.accessToken(ctx)
	if err != nil {
		return nil, fmt.Errorf("no GitHub token stored: %w", err)
	}
	repos, status, err := s.fetchRepos(ctx, token)
	if err != nil {
		return nil, err
	}
	if status == http.StatusUnauthorized {
		// The token was rejected before its recorded expiry (early expiry or a
		// server-side rotation). Try exactly one forced refresh + retry before
		// surfacing the reconnect prompt. Do NOT delete the stored token on 401:
		// a transient failure must not wipe a still-valid credential.
		newToken, rerr := s.forceRefresh(ctx)
		if rerr != nil {
			return nil, ErrInvalidCredentials
		}
		repos, status, err = s.fetchRepos(ctx, newToken)
		if err != nil {
			return nil, err
		}
		if status == http.StatusUnauthorized {
			return nil, ErrInvalidCredentials
		}
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("github api %d", status)
	}
	return repos, nil
}

// fetchRepos performs the GitHub /user/repos call with the given token and
// returns the decoded repos and the HTTP status.
func (s *Service) fetchRepos(ctx context.Context, token string) ([]Repo, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.apiURL+"/user/repos?per_page=100&sort=updated&direction=desc", http.NoBody)
	if err != nil {
		return nil, 0, fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("github api: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusUnauthorized {
			return nil, resp.StatusCode, nil
		}
		body, _ := io.ReadAll(resp.Body)
		return nil, resp.StatusCode, fmt.Errorf("github api %d: %s", resp.StatusCode, string(body))
	}

	var raw []struct {
		Name          string `json:"name"`
		FullName      string `json:"full_name"`
		Private       bool   `json:"private"`
		DefaultBranch string `json:"default_branch"`
		CloneURL      string `json:"clone_url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, resp.StatusCode, fmt.Errorf("decoding response: %w", err)
	}

	repos := make([]Repo, len(raw))
	for i, r := range raw {
		repos[i] = Repo{
			Name:          r.Name,
			FullName:      r.FullName,
			Private:       r.Private,
			DefaultBranch: r.DefaultBranch,
			CloneURL:      r.CloneURL,
		}
	}
	return repos, resp.StatusCode, nil
}

// GetToken returns a usable stored token, renewing an expiring OAuth token
// first. Exported for the GitHub OAuth IPC flow that needs to put the token on
// the control plane after local storage.
func (s *Service) GetToken(ctx context.Context) (string, error) {
	return s.accessToken(ctx)
}
