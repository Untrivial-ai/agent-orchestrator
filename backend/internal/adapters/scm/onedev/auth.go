package onedev

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	aoprocess "github.com/aoagents/agent-orchestrator/backend/internal/process"
)

// ErrNoToken is returned when no token source could yield a usable credential.
// internal/daemon/scm_wiring.go matches this sentinel to log the provider as
// "disabled: no credentials" rather than as a failure.
var ErrNoToken = errors.New("onedev scm: no credentials configured")

// ErrAuthFailed is returned when OneDev rejects the supplied credential
// (HTTP 401/403).
var ErrAuthFailed = errors.New("onedev scm: authentication failed")

// Credential is a resolved OneDev credential. OneDev accepts either a bearer
// access token or HTTP basic auth, and self-hosted estates commonly hold the
// latter (a service account's password in a keyring), so both are modelled
// here rather than forcing every deployment onto access tokens.
//
// Token wins when both are populated.
type Credential struct {
	// Token is a OneDev access token, sent as "Authorization: Bearer <token>".
	Token string
	// Username and Password are HTTP basic-auth credentials, used only when
	// Token is empty.
	Username string
	Password string
}

// BearerCredential builds a Credential from an access token.
func BearerCredential(token string) Credential {
	return Credential{Token: strings.TrimSpace(token)}
}

// BasicCredential builds a Credential from a username and password.
func BasicCredential(username, password string) Credential {
	return Credential{Username: strings.TrimSpace(username), Password: password}
}

// Empty reports whether the credential carries neither a token nor a username.
func (c Credential) Empty() bool {
	return strings.TrimSpace(c.Token) == "" && strings.TrimSpace(c.Username) == ""
}

// apply attaches the credential to req. An empty credential is an error
// rather than an unauthenticated request: OneDev has no anonymous API mode
// worth reaching, and a silent anonymous call would surface as a confusing
// 401 far from its cause.
func (c Credential) apply(req *http.Request) error {
	if tok := strings.TrimSpace(c.Token); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
		return nil
	}
	if user := strings.TrimSpace(c.Username); user != "" {
		req.SetBasicAuth(user, c.Password)
		return nil
	}
	return ErrNoToken
}

// TokenSource yields a OneDev credential on demand. Production wires this to
// EnvTokenSource with an optional CommandTokenSource fallback; tests inject
// StaticTokenSource or StaticBasicAuthSource.
type TokenSource interface {
	Token(ctx context.Context) (Credential, error)
}

// tokenInvalidator is the optional capability of dropping a cached credential
// so the next call re-fetches it. The Client invokes this whenever OneDev
// responds with an auth-class failure.
type tokenInvalidator interface {
	InvalidateToken()
}

// StaticTokenSource is a literal access token, typically used in tests and to
// carry per-host overrides from AO_ONEDEV_HOST_TOKENS.
type StaticTokenSource string

// Token returns the literal token value, trimmed of whitespace.
func (s StaticTokenSource) Token(context.Context) (Credential, error) {
	c := BearerCredential(string(s))
	if c.Empty() {
		return Credential{}, ErrNoToken
	}
	return c, nil
}

// StaticBasicAuthSource is a literal username/password pair for instances
// that authenticate with HTTP basic auth rather than an access token.
type StaticBasicAuthSource struct {
	Username string
	Password string
}

// Token returns the literal basic-auth credential.
func (s StaticBasicAuthSource) Token(context.Context) (Credential, error) {
	c := BasicCredential(s.Username, s.Password)
	if c.Empty() {
		return Credential{}, ErrNoToken
	}
	return c, nil
}

// EnvTokenSource reads the first non-empty access token from the listed env
// vars, falling back to ONEDEV_TOKEN. Order matters: the AO-scoped variable
// (AO_ONEDEV_TOKEN) should win over the global default.
type EnvTokenSource struct {
	EnvVars []string
}

// Token returns the first non-empty value from the configured env vars,
// falling back to ONEDEV_TOKEN.
func (s EnvTokenSource) Token(context.Context) (Credential, error) {
	for _, name := range s.EnvVars {
		if v := strings.TrimSpace(os.Getenv(name)); v != "" {
			return BearerCredential(v), nil
		}
	}
	if v := strings.TrimSpace(os.Getenv("ONEDEV_TOKEN")); v != "" {
		return BearerCredential(v), nil
	}
	return Credential{}, ErrNoToken
}

// FallbackTokenSource tries each source in order, returning the first
// credential. A source reporting ErrNoToken is skipped; any other error is
// remembered and returned only if no later source succeeds, so a broken
// credential helper does not mask a working env var.
type FallbackTokenSource []TokenSource

// Token tries each source in order, returning the first successful credential.
func (s FallbackTokenSource) Token(ctx context.Context) (Credential, error) {
	var firstErr error
	for _, src := range s {
		if src == nil {
			continue
		}
		cred, err := src.Token(ctx)
		if err == nil {
			return cred, nil
		}
		if errors.Is(err, ErrNoToken) {
			continue
		}
		if firstErr == nil {
			firstErr = err
		}
	}
	if firstErr != nil {
		return Credential{}, firstErr
	}
	return Credential{}, ErrNoToken
}

// InvalidateToken clears cached credentials in all sub-sources that support it.
func (s FallbackTokenSource) InvalidateToken() {
	for _, src := range s {
		if inv, ok := src.(tokenInvalidator); ok {
			inv.InvalidateToken()
		}
	}
}

// defaultCommandTokenCacheTTL bounds how long a credential-helper result is
// reused before the command is run again.
const defaultCommandTokenCacheTTL = 5 * time.Minute

// CommandTokenSource shells out to an operator-configured credential helper
// when no env var is set, memoizing the result for TokenTTL.
//
// GitLab's equivalent fallback shells out to `glab`, but OneDev ships no
// first-party CLI to borrow a token from, so this source is deliberately
// generic: the operator names the command (for example a keyring reader such
// as `secret-tool lookup ...`) and its trimmed stdout becomes the token.
// A source with no Command configured reports ErrNoToken, which disables the
// provider quietly instead of erroring on every poll — the same failure mode
// as an uninstalled `glab`.
type CommandTokenSource struct {
	// Command is the argv of the credential helper. Empty disables the source.
	Command []string
	// Username, when set, makes the command's output a basic-auth password
	// for this user rather than a bearer access token.
	Username string
	// TokenTTL is how long a resolved credential is cached. Zero means
	// defaultCommandTokenCacheTTL.
	TokenTTL time.Duration
	// Run overrides command execution; tests inject a stub.
	Run func(ctx context.Context, argv []string) (string, error)
	// Clock overrides time.Now; tests inject a fake to assert TTL expiry
	// without sleeping.
	Clock func() time.Time

	mu        sync.Mutex
	cred      Credential
	expiresAt time.Time
}

// Token returns the cached credential, re-running the configured command when
// the cache expires.
func (s *CommandTokenSource) Token(ctx context.Context) (Credential, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.Command) == 0 {
		return Credential{}, ErrNoToken
	}
	now := s.now()
	if !s.cred.Empty() && now.Before(s.expiresAt) {
		return s.cred, nil
	}
	run := s.Run
	if run == nil {
		run = runCredentialCommand
	}
	out, err := run(ctx, s.Command)
	if err != nil {
		return Credential{}, err
	}
	secret := strings.TrimSpace(out)
	if secret == "" {
		return Credential{}, ErrNoToken
	}
	cred := BearerCredential(secret)
	if user := strings.TrimSpace(s.Username); user != "" {
		cred = BasicCredential(user, secret)
	}
	s.cred = cred
	s.expiresAt = now.Add(s.ttl())
	return cred, nil
}

// InvalidateToken clears the cached credential so the next call re-runs the
// command.
func (s *CommandTokenSource) InvalidateToken() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cred = Credential{}
	s.expiresAt = time.Time{}
}

func (s *CommandTokenSource) now() time.Time {
	if s.Clock != nil {
		return s.Clock()
	}
	return time.Now()
}

func (s *CommandTokenSource) ttl() time.Duration {
	if s.TokenTTL > 0 {
		return s.TokenTTL
	}
	return defaultCommandTokenCacheTTL
}

// runCredentialCommand executes the helper and returns its stdout. Any
// failure (missing binary, non-zero exit, locked keyring) maps to ErrNoToken
// so the provider is disabled rather than erroring on every poll. Only stdout
// is read: a helper that prints diagnostics on stderr must not have them
// mistaken for the secret.
func runCredentialCommand(ctx context.Context, argv []string) (string, error) {
	if len(argv) == 0 {
		return "", ErrNoToken
	}
	out, err := aoprocess.CommandContext(ctx, argv[0], argv[1:]...).Output()
	if err != nil {
		return "", ErrNoToken
	}
	return string(out), nil
}
