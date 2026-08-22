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

// defaultGLabFailureCacheTTL bounds how often a *failing* lookup re-spawns
// glab. A host-scoped source whose host glab knows nothing about (or a glab
// too old for `--hostname`) fails on every call, and a failure that is not
// memoized forks a process per token resolution — once per allowlisted host,
// on every API call and every credentials probe. It is deliberately far
// shorter than the success TTL: after `glab auth login --hostname <host>` the
// new credential must become visible quickly.
const defaultGLabFailureCacheTTL = 30 * time.Second

// GLabTokenSource shells out to `glab auth status --show-token` when env vars
// are not configured. It memoizes the result for TokenTTL.
type GLabTokenSource struct {
	// Hostname scopes the lookup to one GitLab instance
	// (`glab auth status --hostname <host>`). Empty asks glab for its own
	// default host. Without it a glab configured for several instances
	// reports whichever host it lists first, so a self-managed host can end up
	// probed with another instance's token.
	Hostname string
	GLab     func(ctx context.Context) (string, error)
	TokenTTL time.Duration
	Clock    func() time.Time

	mu           sync.Mutex
	token        string
	expiresAt    time.Time
	err          error
	errExpiresAt time.Time
}

// Token returns the cached glab token, re-fetching via `glab auth status` when
// the cache expires. Failures are memoized too, for a shorter window, so a
// source glab can never satisfy does not fork a process per call — except a
// failure caused by the caller's own context, which is not evidence about the
// credential and is therefore never cached.
func (s *GLabTokenSource) Token(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if s.token != "" && now.Before(s.expiresAt) {
		return s.token, nil
	}
	if s.err != nil && now.Before(s.errExpiresAt) {
		return "", s.err
	}
	run := s.GLab
	if run == nil {
		run = func(ctx context.Context) (string, error) { return glabAuthToken(ctx, s.Hostname) }
	}
	out, err := run(ctx)
	if err != nil {
		// A lookup the caller ended is not evidence about the credential:
		// memoizing it would answer the next caller — one with a live context
		// — out of a stale failure. Daemon startup probes several hosts under
		// one deadline and cancels the losers, so this is the common case.
		if ctx.Err() != nil {
			return "", err
		}
		return "", s.cacheFailure(err, now)
	}
	token := strings.TrimSpace(out)
	if token == "" {
		if ctx.Err() != nil {
			return "", ErrNoToken
		}
		return "", s.cacheFailure(ErrNoToken, now)
	}
	s.token = token
	s.expiresAt = now.Add(s.ttl())
	s.err, s.errExpiresAt = nil, time.Time{}
	return token, nil
}

// cacheFailure memoizes err until the failure window elapses and returns it
// unchanged, so callers (FallbackTokenSource in particular) still see the
// original ErrNoToken or command error identity.
func (s *GLabTokenSource) cacheFailure(err error, now time.Time) error {
	s.token, s.expiresAt = "", time.Time{}
	s.err = err
	s.errExpiresAt = now.Add(s.failureTTL())
	return err
}

// InvalidateToken clears the cached glab token so the next call re-fetches.
// The memoized failure is cleared with it: an invalidation means the caller
// believes the credential situation changed.
func (s *GLabTokenSource) InvalidateToken() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.token = ""
	s.expiresAt = time.Time{}
	s.err = nil
	s.errExpiresAt = time.Time{}
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

// failureTTL is the negative-cache window: the success TTL when it is shorter
// (tests pin it), otherwise defaultGLabFailureCacheTTL.
func (s *GLabTokenSource) failureTTL() time.Duration {
	if ttl := s.ttl(); ttl < defaultGLabFailureCacheTTL {
		return ttl
	}
	return defaultGLabFailureCacheTTL
}

// glabRunner runs one `glab auth status` invocation and returns its combined
// output. It exists so the host-attribution logic can be tested without a glab
// binary on PATH.
type glabRunner func(ctx context.Context, args ...string) (string, error)

// runGLab is the production glabRunner.
//
// glab writes auth status output to stderr, not stdout — use CombinedOutput so
// the token is not lost. The output is returned even when glab exits non-zero,
// because an older glab rejecting `--hostname` still prints usable status.
func runGLab(ctx context.Context, args ...string) (string, error) {
	out, err := aoprocess.CommandContext(ctx, "glab", args...).CombinedOutput()
	return string(out), err
}

// glabAuthToken resolves the token glab holds for hostname.
//
// Unlike `gh auth token` (which prints just the token), glab has no token-only
// subcommand — `glab auth status --show-token` prints a multi-line status block
// that includes a line like:
//
//	✓ Token found: glpat-xxxxxxxxxxxxxxxx
//
// If glab is not installed, not authenticated, or exits non-zero for any other
// reason, ErrNoToken is returned so the GitLab provider is silently disabled
// rather than erroring on every poll.
func glabAuthToken(ctx context.Context, hostname string) (string, error) {
	return glabAuthTokenWith(ctx, hostname, runGLab)
}

// glabAuthTokenWith asks glab for hostname's token, scoping the query with
// `--hostname` first. When that yields nothing — a glab too old for the flag,
// or an instance it knows under a different name — it falls back to the
// unscoped status block, but accepts only the token that block attributes to
// hostname. A glab authenticated against several instances (or against one
// instance that is not the one being asked about) therefore never hands its
// default host's credential to another host.
//
// An empty hostname asks glab for its own default host and takes whatever it
// reports; no caller in the daemon does this, because a token that cannot be
// attributed to an instance must not be sent to one.
func glabAuthTokenWith(ctx context.Context, hostname string, run glabRunner) (string, error) {
	args := []string{"auth", "status", "--show-token"}
	host := NormalizeHost(hostname)
	// The exit code is not the gate on the output: `glab auth status` exits
	// non-zero when *any* configured instance is unauthenticated, while still
	// printing a usable block for every instance that is. Gating on it would
	// drop a valid token because some unrelated host has a stale session.
	if host == "" {
		out, _ := run(ctx, args...)
		if token := ParseGLabTokenLine(out); token != "" {
			return token, nil
		}
		return "", ErrNoToken
	}
	if out, _ := run(ctx, append(append([]string{}, args...), "--hostname", host)...); out != "" {
		if token := GLabScopedToken(out, host); token != "" {
			return token, nil
		}
	}
	out, _ := run(ctx, args...)
	if token := GLabTokenForHost(out, host); token != "" {
		return token, nil
	}
	return "", ErrNoToken
}

// ParseGLabTokenLine extracts the token value from `glab auth status --show-token`
// output. It takes the first token line it finds, so it is only safe on output
// that covers a single instance — use GLabTokenForHost on unscoped output.
//
// The token appears on a line containing "Token" followed by a colon
// and the token value (e.g. "✓ Token found: glpat-xxx"). The function scans
// all lines so it is robust against reordering of fields or checkmark prefixes
// in future glab versions.
//
// Only "Token:" and "Token found:" name the credential itself
// (glabTokenLineValue): a line such as "Token expires: 2026-01-01" carries a
// value that is not a token, and returning it would send a date to GitLab as
// a PRIVATE-TOKEN.
func ParseGLabTokenLine(output string) string {
	for _, line := range strings.Split(output, "\n") {
		if token := glabTokenLineValue(strings.TrimSpace(line)); token != "" {
			return token
		}
	}
	return ""
}

// glabHostToken pairs one host named in `glab auth status` output with the
// token glab reported for it.
type glabHostToken struct {
	host  string
	token string
}

// parseGLabHostTokens attributes every token in `glab auth status --show-token`
// output to the host it belongs to. glab prints one block per authenticated
// instance, so an unscoped invocation on a multi-instance setup contains
// several tokens; taking the first one (ParseGLabTokenLine) would answer with
// whichever instance glab happened to list first. A token printed before any
// host is named is dropped: an unattributable credential must never be sent
// anywhere, because it may belong to a different instance than the caller asks
// about.
//
// Both known glab layouts are recognized: the older
//
//	Hostname: gitlab.com
//	✓ Token found: glpat-xxx
//
// and the current per-host block
//
//	gitlab.com
//	  ✓ Logged in to gitlab.com as alice (keyring)
//	  ✓ Token: glpat-xxx
func parseGLabHostTokens(output string) []glabHostToken {
	var pairs []glabHostToken
	host := ""
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if h := glabHostLine(line); h != "" {
			host = h
			continue
		}
		if host == "" {
			continue
		}
		token := glabTokenLineValue(line)
		if token == "" || glabHasHost(pairs, host) {
			continue
		}
		pairs = append(pairs, glabHostToken{host: host, token: token})
	}
	return pairs
}

func glabHasHost(pairs []glabHostToken, host string) bool {
	for _, p := range pairs {
		if p.host == host {
			return true
		}
	}
	return false
}

// GLabScopedToken reads the token out of `glab auth status --show-token
// --hostname <host>` output. The `--hostname` flag is a request, not a
// guarantee: a glab that does not support it (or does not honor it) prints the
// full multi-instance block, and taking the first token line there would
// attribute another instance's credential to host. So the host-attributed
// token wins, and the unattributed first-token reading is only used when the
// output names no instance at all.
func GLabScopedToken(output, host string) string {
	if token := GLabTokenForHost(output, host); token != "" {
		return token
	}
	if len(parseGLabHostTokens(output)) > 0 {
		return ""
	}
	return ParseGLabTokenLine(output)
}

// GLabTokenForHost returns the token `glab auth status --show-token` output
// attributes to host, or "" when it attributes none to it. Callers outside this
// package use it to check that a credential belongs to the instance they are
// about to send it to.
func GLabTokenForHost(output, host string) string {
	h := NormalizeHost(host)
	if h == "" {
		return ""
	}
	for _, p := range parseGLabHostTokens(output) {
		if p.host == h {
			return p.token
		}
	}
	return ""
}

// glabHostLine returns the host a status line names, or "" when the line does
// not introduce one.
func glabHostLine(line string) string {
	trimmed := strings.TrimLeft(line, "✓✗•*- \t")
	if rest, ok := cutPrefixFold(trimmed, "logged in to "); ok {
		host, _, _ := strings.Cut(rest, " ")
		return NormalizeHost(host)
	}
	if rest, ok := cutPrefixFold(trimmed, "hostname:"); ok {
		return NormalizeHost(rest)
	}
	return glabBareHostLine(trimmed)
}

func cutPrefixFold(s, prefix string) (string, bool) {
	if len(s) < len(prefix) || !strings.EqualFold(s[:len(prefix)], prefix) {
		return "", false
	}
	return strings.TrimSpace(s[len(prefix):]), true
}

// glabBareHostLine recognizes the bare `<host>` section header of the current
// glab layout. It is deliberately strict — a line only counts as a host when it
// looks like one (optional numeric port, dotted name, host characters only) —
// so a stray value is never mistaken for an instance name.
func glabBareHostLine(line string) string {
	if line == "" || strings.ContainsAny(line, " \t/\\") {
		return ""
	}
	name := line
	if i := strings.LastIndex(name, ":"); i >= 0 {
		port := name[i+1:]
		if port == "" || strings.TrimLeft(port, "0123456789") != "" {
			return ""
		}
		name = name[:i]
	}
	if !strings.Contains(name, ".") {
		return ""
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-':
		default:
			return ""
		}
	}
	return NormalizeHost(line)
}

// glabTokenLineValue returns the token a status line carries, or "" when the
// line is not the token line. Lines such as "Token expires: <date>" are
// rejected: only "Token:" and "Token found:" name the credential itself.
//
// A value containing whitespace is rejected too. No GitLab token contains a
// space, so such a value is prose — "error: Token: expired or revoked" is a
// diagnostic, not a credential, and this is what keeps a failing glab's output
// from being read as one now that the exit code no longer gates parsing.
func glabTokenLineValue(line string) string {
	idx := strings.Index(line, "Token")
	if idx < 0 {
		return ""
	}
	rest := line[idx+len("Token"):]
	colon := strings.Index(rest, ":")
	if colon < 0 {
		return ""
	}
	if label := strings.ToLower(strings.TrimSpace(rest[:colon])); label != "" && label != "found" {
		return ""
	}
	token := strings.TrimSpace(rest[colon+1:])
	if strings.ContainsAny(token, " \t") {
		return ""
	}
	return token
}

// HostTokenSource is the token chain for one self-managed GitLab host: the
// credential glab holds for that instance first, then the shared env vars.
//
// The host-scoped lookup outranks AO_GITLAB_TOKEN/GITLAB_TOKEN on purpose. The
// env vars are a global default — the same value is offered to every instance
// AO talks to — so letting them win would send a gitlab.com token to a
// self-managed server (disclosing it to whoever runs it, and failing every call
// with 401) even though the user had authenticated that instance separately.
// AO_GITLAB_HOST_TOKENS still overrides both; it is applied by the wiring layer
// instead of this chain.
//
// gitlab.com is not a self-managed host: it falls through to DotComTokenSource,
// which keeps the documented env-var-first precedence for the default instance.
func HostTokenSource(host string) TokenSource {
	h := NormalizeHost(host)
	if IsGitLabDotCom(h) {
		return DotComTokenSource()
	}
	return FallbackTokenSource{
		&GLabTokenSource{Hostname: h},
		&EnvTokenSource{EnvVars: []string{"AO_GITLAB_TOKEN"}},
	}
}

// DotComTokenSource is the token chain for gitlab.com: the shared env vars
// first (the documented precedence for the default instance), then glab scoped
// to gitlab.com. There is no unattributed fallback — GLabTokenSource itself
// falls back to glab's unscoped status output, but only ever accepts the token
// that output attributes to gitlab.com.
func DotComTokenSource() TokenSource {
	return FallbackTokenSource{
		&EnvTokenSource{EnvVars: []string{"AO_GITLAB_TOKEN"}},
		&GLabTokenSource{Hostname: DotComHost},
	}
}
