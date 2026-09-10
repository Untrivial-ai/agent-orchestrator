package gitlab

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"time"

	aoprocess "github.com/aoagents/agent-orchestrator/backend/internal/process"
)

// TokenSource yields a GitLab private token on demand. Production wires this
// to EnvTokenSource or GLabTokenSource; tests inject StaticTokenSource.
type TokenSource interface {
	Token(ctx context.Context) (string, error)
}

// tokenInvalidator is the optional capability of dropping a cached token so
// the next call re-fetches it. The Client invokes this whenever GitLab
// responds with an auth-class failure.
type tokenInvalidator interface {
	InvalidateToken()
}

// ErrNoToken is returned when no token source could yield a non-empty token.
var ErrNoToken = errors.New("gitlab scm: no token configured")

// ErrAuthFailed is returned when GitLab rejects the supplied token (401/403).
var ErrAuthFailed = errors.New("gitlab scm: authentication failed")

// StaticTokenSource is a literal token, typically used in tests.
type StaticTokenSource string

// Token returns the literal token value, trimmed of whitespace.
func (s StaticTokenSource) Token(context.Context) (string, error) {
	t := strings.TrimSpace(string(s))
	if t == "" {
		return "", ErrNoToken
	}
	return t, nil
}

// EnvTokenSource reads the first non-empty value from the listed env vars,
// falling back to GITLAB_TOKEN. Order matters: a project-scoped variable
// (AO_GITLAB_TOKEN) should win over the global default.
type EnvTokenSource struct {
	EnvVars []string
}

// Token returns the first non-empty value from the configured env vars,
// falling back to GITLAB_TOKEN.
func (s EnvTokenSource) Token(context.Context) (string, error) {
	for _, name := range s.EnvVars {
		if v := strings.TrimSpace(os.Getenv(name)); v != "" {
			return v, nil
		}
	}
	if v := strings.TrimSpace(os.Getenv("GITLAB_TOKEN")); v != "" {
		return v, nil
	}
	return "", ErrNoToken
}

// FallbackTokenSource tries each source in order, returning the first token.
type FallbackTokenSource []TokenSource

// Token tries each source in order, returning the first successful token.
func (s FallbackTokenSource) Token(ctx context.Context) (string, error) {
	var firstErr error
	for _, src := range s {
		if src == nil {
			continue
		}
		tok, err := src.Token(ctx)
		if err == nil {
			return tok, nil
		}
		if errors.Is(err, ErrNoToken) {
			continue
		}
		if firstErr == nil {
			firstErr = err
		}
	}
	if firstErr != nil {
		return "", firstErr
	}
	return "", ErrNoToken
}

// InvalidateToken clears cached tokens in all sub-sources that support it.
func (s FallbackTokenSource) InvalidateToken() {
	for _, src := range s {
		if inv, ok := src.(tokenInvalidator); ok {
			inv.InvalidateToken()
		}
	}
}

const defaultGLabTokenCacheTTL = 5 * time.Minute

// GLabTokenSource shells out to `glab auth status --show-token` when env vars
// are not configured. It memoizes the result for TokenTTL.
type GLabTokenSource struct {
	GLab     func(ctx context.Context) (string, error)
	TokenTTL time.Duration
	Clock    func() time.Time

	mu        sync.Mutex
	token     string
	expiresAt time.Time
}

// Token returns the cached glab token, re-fetching via `glab auth status` when
// the cache expires.
func (s *GLabTokenSource) Token(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if s.token != "" && now.Before(s.expiresAt) {
		return s.token, nil
	}
	run := s.GLab
	if run == nil {
		run = glabAuthToken
	}
	out, err := run(ctx)
	if err != nil {
		return "", err
	}
	token := strings.TrimSpace(out)
	if token == "" {
		return "", ErrNoToken
	}
	s.token = token
	s.expiresAt = now.Add(s.ttl())
	return token, nil
}

// InvalidateToken clears the cached glab token so the next call re-fetches.
func (s *GLabTokenSource) InvalidateToken() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.token = ""
	s.expiresAt = time.Time{}
}

func (s *GLabTokenSource) now() time.Time {
	if s.Clock != nil {
		return s.Clock()
	}
	return time.Now()
}

func (s *GLabTokenSource) ttl() time.Duration {
	if s.TokenTTL > 0 {
		return s.TokenTTL
	}
	return defaultGLabTokenCacheTTL
}

// glabAuthToken runs `glab auth status --show-token` and parses the token from
// the output. Unlike `gh auth token` (which prints just the token), glab does not
// have a token-only subcommand — `glab auth status --show-token` prints a
// multi-line status block that includes a line like:
//
//	✓ Token found: glpat-xxxxxxxxxxxxxxxx
//
// If glab is not installed, not authenticated, or exits non-zero for any other
// reason, ErrNoToken is returned so the GitLab provider is silently disabled
// rather than erroring on every poll.
func glabAuthToken(ctx context.Context) (string, error) {
	// glab writes auth status output to stderr, not stdout — use CombinedOutput
	// to capture both streams so the token is not lost.
	out, err := aoprocess.CommandContext(ctx, "glab", "auth", "status", "--show-token").CombinedOutput()
	if err != nil {
		return "", ErrNoToken
	}
	token := parseGLabTokenLine(string(out))
	if token == "" {
		return "", ErrNoToken
	}
	return token, nil
}

// parseGLabTokenLine extracts the token value from `glab auth status --show-token`
// output. The token appears on a line containing "Token" followed by a colon
// and the token value (e.g. "✓ Token found: glpat-xxx"). The function scans
// all lines so it is robust against reordering of fields or checkmark prefixes
// in future glab versions.
func parseGLabTokenLine(output string) string {
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		// Match any line containing "Token" — handles both "Token: xxx"
		// and "✓ Token found: xxx" formats across glab versions.
		tokenIdx := strings.Index(line, "Token")
		if tokenIdx < 0 {
			continue
		}
		// Find the colon after "Token" and take everything after it.
		colonIdx := strings.Index(line[tokenIdx:], ":")
		if colonIdx < 0 {
			continue
		}
		val := strings.TrimSpace(line[tokenIdx+colonIdx+1:])
		if val != "" {
			return val
		}
	}
	return ""
}
