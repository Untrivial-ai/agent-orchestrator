package gitcode

import (
	"context"
	"errors"
	"os"
	"strings"
)

// TokenSource yields a GitCode personal access token on demand. Production
// wires this to EnvTokenSource; tests inject StaticTokenSource.
type TokenSource interface {
	Token(ctx context.Context) (string, error)
}

// ErrNoToken is returned when no token source could yield a non-empty token.
var ErrNoToken = errors.New("gitcode scm: no token configured")

// ErrAuthFailed is returned when GitCode rejects the supplied token (401/403).
var ErrAuthFailed = errors.New("gitcode scm: authentication failed")

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
// falling back to GITCODE_TOKEN. Order matters: a project-scoped variable
// (AO_GITCODE_TOKEN) should win over the global default.
type EnvTokenSource struct {
	EnvVars []string
}

// Token returns the first non-empty value from the configured env vars,
// falling back to GITCODE_TOKEN.
func (s EnvTokenSource) Token(context.Context) (string, error) {
	for _, name := range s.EnvVars {
		if v := strings.TrimSpace(os.Getenv(name)); v != "" {
			return v, nil
		}
	}
	if v := strings.TrimSpace(os.Getenv("GITCODE_TOKEN")); v != "" {
		return v, nil
	}
	return "", ErrNoToken
}

// FallbackTokenSource tries each source in order, returning the first token.
type FallbackTokenSource []TokenSource

// Token tries each source in order, returning the first successful token.
// ErrNoToken from a source moves on to the next; any other error is
// remembered and returned when no source yields a token.
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
