// Package repoowner classifies the owner segment of a GitHub remote as a
// personal account or an organization.
//
// AO's project telemetry already carries the owner segment of a project's git
// remote (never the repo name, never the full URL). That segment alone cannot
// say whether usage belongs to a person or to a company, which is the only
// question the property exists to answer. GitHub's public GET /users/{owner}
// endpoint answers it, so this adapter makes exactly that one call.
//
// The call is deliberately unauthenticated: the endpoint is public, the owner
// is a value AO already holds, and keeping credentials out of this path means
// installs with no GitHub token — which today are the ones most often missing
// an owner attribution entirely — are classified just as well as authenticated
// ones. The trade is GitHub's 60 requests/hour unauthenticated budget per IP;
// exceeding it degrades to an error and therefore to an omitted property,
// which is the same outcome as any other failure here.
package repoowner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

const (
	defaultBaseURL   = "https://api.github.com"
	defaultUserAgent = "ao-agent-orchestrator/repo-owner"
	// maxBodyBytes caps the response read. The two fields we decode are tiny;
	// anything larger is a proxy error page, not a GitHub user payload.
	maxBodyBytes = 64 << 10
)

// Owner classifications, spelled exactly as GitHub reports them on
// GET /users/{owner}. These are the only two values this package ever
// returns, so a new GitHub account type cannot silently widen the property's
// value space in an analytics dashboard.
const (
	TypeUser         = "User"
	TypeOrganization = "Organization"
)

// Errors callers can discriminate. Every one of them means the same thing to
// the telemetry caller — omit the property — but they keep the debug log
// honest about why.
var (
	// ErrInvalidOwner is returned for an owner that cannot be a GitHub login.
	ErrInvalidOwner = errors.New("repoowner: invalid github owner")
	// ErrUnknownOwnerType is returned when GitHub answers with an account type
	// that is neither User nor Organization (for example a future account kind).
	ErrUnknownOwnerType = errors.New("repoowner: unknown github owner type")
	// ErrNotFound is returned when GitHub does not know the owner, which is the
	// expected answer for a renamed or deleted account.
	ErrNotFound = errors.New("repoowner: github owner not found")
)

// validOwner matches GitHub's login grammar: alphanumerics and hyphens, no
// leading or trailing hyphen, 39 characters at most. The owner reaching this
// adapter is a path segment of a remote URL that AO did not author, so it is
// validated before it is interpolated into a request path rather than trusted.
var validOwner = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,37}[A-Za-z0-9])?$`)

type httpDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

// Options configures a GitHubClassifier. Production code sets nothing; tests
// inject HTTPClient and BaseURL to point at an httptest fake.
type Options struct {
	HTTPClient httpDoer
	BaseURL    string
	UserAgent  string
}

// GitHubClassifier resolves owner classifications from GitHub's public user
// endpoint. It holds no credentials and no state, so it is safe for concurrent
// use by definition.
type GitHubClassifier struct {
	http      httpDoer
	baseURL   string
	userAgent string
}

// NewGitHubClassifier returns a classifier against api.github.com.
func NewGitHubClassifier(opts Options) *GitHubClassifier {
	c := &GitHubClassifier{
		http:      opts.HTTPClient,
		baseURL:   strings.TrimRight(strings.TrimSpace(opts.BaseURL), "/"),
		userAgent: strings.TrimSpace(opts.UserAgent),
	}
	if c.http == nil {
		c.http = &http.Client{Timeout: 5 * time.Second}
	}
	if c.baseURL == "" {
		c.baseURL = defaultBaseURL
	}
	if c.userAgent == "" {
		c.userAgent = defaultUserAgent
	}
	return c
}

// ClassifyRepoOwner reports whether owner names a personal account
// (TypeUser) or an organization (TypeOrganization). Every failure mode —
// unreachable network, rate limit, unknown owner, unexpected account type —
// returns an error so the caller omits the property rather than guessing.
func (c *GitHubClassifier) ClassifyRepoOwner(ctx context.Context, owner string) (string, error) {
	owner = strings.TrimSpace(owner)
	if !validOwner.MatchString(owner) {
		return "", fmt.Errorf("%w: %q", ErrInvalidOwner, owner)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/users/"+owner, http.NoBody)
	if err != nil {
		return "", fmt.Errorf("repoowner: build request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", c.userAgent)

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("repoowner: GET /users/%s: %w", owner, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		return "", fmt.Errorf("%w: %s", ErrNotFound, owner)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("repoowner: GET /users/%s: unexpected status %d", owner, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return "", fmt.Errorf("repoowner: read response: %w", err)
	}
	// Only "type" is decoded. The /users payload also carries the name, email,
	// company, and avatar of a real person; none of it is read here, so none of
	// it can reach a telemetry payload by accident.
	var parsed struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("repoowner: decode response: %w", err)
	}
	switch parsed.Type {
	case TypeUser, TypeOrganization:
		return parsed.Type, nil
	default:
		return "", fmt.Errorf("%w: %q", ErrUnknownOwnerType, parsed.Type)
	}
}
