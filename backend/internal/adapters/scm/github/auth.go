package github

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"time"

	aoprocess "github.com/aoagents/agent-orchestrator/backend/internal/process"
)

// TokenSource yields a GitHub bearer token on demand. Production wires this
// to EnvTokenSource or GHTokenSource; tests inject StaticTokenSource.
type TokenSource interface {
	Token(ctx context.Context) (string, error)
}

// tokenInvalidator is the optional capability of dropping a cached token so
// the next call re-fetches it. The Client invokes this whenever GitHub
// responds with an auth-class failure: the next request will pick up a
// rotated token without restarting the daemon.
type tokenInvalidator interface {
	InvalidateToken()
}

// ErrNoToken is returned when no token source could yield a non-empty token.
var ErrNoToken = errors.New("github scm: no token configured")

// StaticTokenSource is a literal token, typically used in tests.
type StaticTokenSource string

// Token returns the literal token, or ErrNoToken if it is blank.
func (s StaticTokenSource) Token(context.Context) (string, error) {
	t := strings.TrimSpace(string(s))
	if t == "" {
		return "", ErrNoToken
	}
	return t, nil
}

// EnvTokenSource reads the first non-empty value from the listed env vars,
// falling back to GITHUB_TOKEN. Order matters: a project-scoped variable
// (AO_GITHUB_TOKEN) should win over the global default.
type EnvTokenSource struct {
	EnvVars []string
}

// Token returns the first non-empty env-var value found, or ErrNoToken.
func (s EnvTokenSource) Token(context.Context) (string, error) {
	for _, name := range s.EnvVars {
		if v := strings.TrimSpace(os.Getenv(name)); v != "" {
			return v, nil
		}
	}
	if v := strings.TrimSpace(os.Getenv("GITHUB_TOKEN")); v != "" {
		return v, nil
	}
	return "", ErrNoToken
}

// FallbackTokenSource tries each source in order, returning the first token. A
// source that returns ErrNoToken is skipped; other errors are remembered and
// surfaced if no later source yields a token.
type FallbackTokenSource []TokenSource

// Token returns the first non-empty token from the configured sources.
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

// InvalidateToken forwards cache invalidation to sources that support it.
func (s FallbackTokenSource) InvalidateToken() {
	for _, src := range s {
		if inv, ok := src.(tokenInvalidator); ok {
			inv.InvalidateToken()
		}
	}
}

const defaultGHTokenCacheTTL = 5 * time.Minute

// GHTokenSource shells out to `gh auth token` when env vars are not
// configured. It memoizes the result for TokenTTL so we don't fork-exec on
// every request, but the Client invalidates the cache on auth failures so a
// rotated token is picked up on the next call. Tests inject GH so the gh
// binary is never required.
type GHTokenSource struct {
	// GH is the shell-out hook. Production leaves this nil and falls back
	// to `exec.CommandContext("gh", "auth", "token")`; tests inject a
	// fake to avoid touching the real binary.
	GH func(ctx context.Context) (string, error)
	// TokenTTL is how long a successful read is memoized. Zero means use
	// defaultGHTokenCacheTTL.
	TokenTTL time.Duration
	// Clock allows tests to drive expiration. Zero means time.Now.
	Clock func() time.Time

	mu        sync.Mutex
	token     string
	expiresAt time.Time
}

// Token returns the cached token if still fresh, otherwise re-runs gh.
func (s *GHTokenSource) Token(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if s.token != "" && now.Before(s.expiresAt) {
		return s.token, nil
	}
	run := s.GH
	if run == nil {
		run = ghAuthToken
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

// InvalidateToken drops the memoized token so the next Token call shells
// out again. The Client calls this on 401/403-auth responses.
func (s *GHTokenSource) InvalidateToken() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.token = ""
	s.expiresAt = time.Time{}
}

func (s *GHTokenSource) now() time.Time {
	if s.Clock != nil {
		return s.Clock()
	}
	return time.Now()
}

func (s *GHTokenSource) ttl() time.Duration {
	if s.TokenTTL > 0 {
		return s.TokenTTL
	}
	return defaultGHTokenCacheTTL
}

// ghAuthToken maps "gh missing" and "gh not logged in" to ErrNoToken so an
// operator with no credential reaches the best-effort fallback instead of a
// generic auth failure. A cancelled or timed-out probe stays a real error.
func ghAuthToken(ctx context.Context) (string, error) {
	out, err := aoprocess.CommandContext(ctx, "gh", "auth", "token").Output()
	if err != nil {
		return "", noTokenUnlessCancelled(ctx, err)
	}
	return string(out), nil
}

// noTokenUnlessCancelled reports a failed credential probe as ErrNoToken, except
// when the context ended, which says nothing about whether a credential exists.
func noTokenUnlessCancelled(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return err
	}
	return ErrNoToken
}

// defaultCredentialHost is the host queried when CredentialHelperTokenSource.Host
// is empty.
const defaultCredentialHost = "github.com"

// CredentialHelperTokenSource reads a stored HTTPS token for a GitHub host via
// `git credential fill`, covering operators who authenticated git over HTTPS
// (macOS Keychain, git-credential-manager, cache helper) but never ran gh or set
// an env token. It runs non-interactively: GIT_TERMINAL_PROMPT=0 makes git error
// instead of blocking on a username/password prompt when nothing is stored, so an
// unconfigured machine yields ErrNoToken. The credential's password field is the
// token. A successful read is memoized for TokenTTL; the Client invalidates the
// cache on auth failures so a rotated credential is picked up on the next call.
type CredentialHelperTokenSource struct {
	// Host is the GitHub host to query. Empty means github.com.
	Host string
	// Fill is the shell-out hook. Production leaves it nil and falls back to
	// gitCredentialFill; tests inject a fake so git is never required.
	Fill func(ctx context.Context, host string) (string, error)
	// TokenTTL is how long a successful read is memoized. Zero means
	// defaultGHTokenCacheTTL.
	TokenTTL time.Duration
	// Clock allows tests to drive expiration. Zero means time.Now.
	Clock func() time.Time

	mu        sync.Mutex
	token     string
	expiresAt time.Time
}

// Token returns the cached token if still fresh, otherwise re-runs the helper.
func (s *CredentialHelperTokenSource) Token(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if s.token != "" && now.Before(s.expiresAt) {
		return s.token, nil
	}
	run := s.Fill
	if run == nil {
		run = gitCredentialFill
	}
	out, err := run(ctx, s.host())
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

// InvalidateToken drops the memoized token so the next Token call re-runs the
// helper. The Client calls this on 401/403-auth responses.
func (s *CredentialHelperTokenSource) InvalidateToken() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.token = ""
	s.expiresAt = time.Time{}
}

func (s *CredentialHelperTokenSource) now() time.Time {
	if s.Clock != nil {
		return s.Clock()
	}
	return time.Now()
}

func (s *CredentialHelperTokenSource) ttl() time.Duration {
	if s.TokenTTL > 0 {
		return s.TokenTTL
	}
	return defaultGHTokenCacheTTL
}

func (s *CredentialHelperTokenSource) host() string {
	if h := strings.TrimSpace(s.Host); h != "" {
		return h
	}
	return defaultCredentialHost
}

// credentialFillTimeout bounds `git credential fill` so a slow or GUI-backed
// helper cannot stall the caller (this runs on the session-spawn path).
const credentialFillTimeout = 4 * time.Second

// gitCredentialFill runs `git credential fill` for an HTTPS host and returns the
// stored token (the password field), or an error when nothing is stored. It
// never blocks on a prompt: GIT_TERMINAL_PROMPT=0 disables git's terminal
// prompt and GCM_INTERACTIVE=never disables git-credential-manager's GUI, so a
// machine with no cached credential errors out instead of popping a dialog.
func gitCredentialFill(ctx context.Context, host string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, credentialFillTimeout)
	defer cancel()
	cmd := aoprocess.CommandContext(ctx, "git", "credential", "fill")
	cmd.Stdin = strings.NewReader("protocol=https\nhost=" + host + "\n\n")
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GCM_INTERACTIVE=never")
	out, err := cmd.Output()
	if err != nil {
		// With prompts disabled, git exits 128 when nothing is stored.
		return "", noTokenUnlessCancelled(ctx, err)
	}
	return parseCredentialPassword(string(out)), nil
}

// parseCredentialPassword extracts the password field from `git credential`
// key=value output. Returns "" when absent.
func parseCredentialPassword(out string) string {
	for _, line := range strings.Split(out, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "password="); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
