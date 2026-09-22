package githubpat

import (
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
	dataDir    string
	httpClient *http.Client
	apiURL     string
	mu         sync.Mutex
}

// New returns a Service rooted at dataDir.
func New(dataDir string) *Service {
	return newWithClient(dataDir, http.DefaultClient, "https://api.github.com")
}

func newWithClient(dataDir string, client *http.Client, apiURL string) *Service {
	return &Service{dataDir: dataDir, httpClient: client, apiURL: apiURL}
}

func (s *Service) patPath() string {
	return filepath.Join(s.dataDir, "github-pat.json")
}

type storedPAT struct {
	Token     string    `json:"token"`
	CreatedAt time.Time `json:"created_at"`
}

// StorePAT persists the GitHub personal access token (or OAuth token).
func (s *Service) StorePAT(_ context.Context, token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(s.dataDir, 0o700); err != nil {
		return fmt.Errorf("creating data dir: %w", err)
	}
	data, err := json.Marshal(storedPAT{Token: token, CreatedAt: time.Now().UTC()})
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

func (s *Service) loadToken() (string, error) {
	data, err := os.ReadFile(s.patPath())
	if err != nil {
		return "", err
	}
	var stored storedPAT
	if err := json.Unmarshal(data, &stored); err != nil {
		return "", err
	}
	if stored.Token == "" {
		return "", fmt.Errorf("no GitHub token stored")
	}
	return stored.Token, nil
}

// ListRepos calls the GitHub API to list repositories accessible with the
// stored token, sorted by most recently updated.
func (s *Service) ListRepos(ctx context.Context) ([]Repo, error) {
	s.mu.Lock()
	token, err := s.loadToken()
	s.mu.Unlock()
	if err != nil {
		return nil, fmt.Errorf("no GitHub token stored: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.apiURL+"/user/repos?per_page=100&sort=updated&direction=desc", http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("github api: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusUnauthorized {
		// Do NOT delete the stored token on a 401. A transient GitHub 401 would
		// otherwise permanently wipe a still-valid credential and force a full
		// reconnect (observed: connect succeeds, then a re-list on a fresh mount
		// hits one 401 and the token is gone). The token stays on disk so a retry
		// can recover; a genuinely invalid token is inert at rest (0600, local)
		// and is overwritten on the next connect or cleared by DeletePAT.
		return nil, ErrInvalidCredentials
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("github api %d: %s", resp.StatusCode, string(body))
	}

	var raw []struct {
		Name          string `json:"name"`
		FullName      string `json:"full_name"`
		Private       bool   `json:"private"`
		DefaultBranch string `json:"default_branch"`
		CloneURL      string `json:"clone_url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
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
	return repos, nil
}

// GetToken returns the stored token. Exported for the GitHub OAuth IPC flow
// that needs to put the token on the control plane after local storage.
func (s *Service) GetToken(_ context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadToken()
}
