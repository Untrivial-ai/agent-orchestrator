package gitcode

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// ProviderOptions configures a GitCode SCM provider.
type ProviderOptions struct {
	// Token yields the GitCode personal access token. When nil and
	// AllowAnonymous is false, NewProvider fails with ErrNoToken.
	Token TokenSource
	// AllowAnonymous lets the provider read public repositories without a
	// token. GitCode's v5 API serves public repo data to unauthenticated
	// requests, so observation works out of the box; mutations and private
	// repositories still require a token.
	AllowAnonymous bool
	// HTTPClient overrides the default HTTP client (tests).
	HTTPClient *http.Client
	// RESTBase overrides https://gitcode.com/api/v5 (tests).
	RESTBase string
	// UserAgent overrides the default User-Agent header.
	UserAgent string
	// Logger receives provider diagnostics. Defaults to slog.Default().
	Logger *slog.Logger
}

// Provider implements the observer's SCM Provider contract for GitCode.
type Provider struct {
	client *Client
	logger *slog.Logger
}

// NewProvider builds a GitCode Provider. It fails only when no token source
// is configured and anonymous access is not allowed.
func NewProvider(opts ProviderOptions) (*Provider, error) {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	if opts.Token == nil && !opts.AllowAnonymous {
		return nil, ErrNoToken
	}
	c := NewClient(ClientOptions{
		HTTPClient: opts.HTTPClient,
		Token:      opts.Token,
		RESTBase:   opts.RESTBase,
		UserAgent:  opts.UserAgent,
	})
	return &Provider{client: c, logger: logger}, nil
}

// SCMCredentialsAvailable reports whether the provider can serve requests.
// A working token qualifies; ErrNoToken falls back to anonymous mode, which
// still reads public repositories; any other token error (e.g. auth
// misconfiguration) propagates so the observer retries instead of
// permanently disabling the provider.
func (p *Provider) SCMCredentialsAvailable(ctx context.Context) (bool, error) {
	if p.client == nil {
		return false, ErrNoToken
	}
	if p.client.tokens != nil {
		tok, err := p.client.tokens.Token(ctx)
		if err == nil && tok != "" {
			return true, nil
		}
		if err != nil && !errors.Is(err, ErrNoToken) {
			return false, err
		}
	}
	// No token configured: anonymous mode still serves public repos.
	return true, nil
}

// AuthenticatedIdentity resolves the token's account via GET /user. It
// returns ErrNoToken in anonymous mode so callers can distinguish "no
// account" from a real identity.
func (p *Provider) AuthenticatedIdentity(ctx context.Context) (ports.SCMIdentity, error) {
	if p.client == nil || p.client.tokens == nil {
		return ports.SCMIdentity{}, ErrNoToken
	}
	tok, err := p.client.tokens.Token(ctx)
	if err != nil {
		return ports.SCMIdentity{}, err
	}
	if tok == "" {
		return ports.SCMIdentity{}, ErrNoToken
	}
	resp, err := p.client.doGET(ctx, "/user", nil)
	if err != nil {
		return ports.SCMIdentity{}, err
	}
	var u struct {
		Login string `json:"login"`
		Name  string `json:"name"`
	}
	if err := unmarshalBody(resp.Body, &u); err != nil {
		return ports.SCMIdentity{}, err
	}
	login := u.Login
	if login == "" {
		login = u.Name
	}
	return ports.SCMIdentity{Login: login, Human: true}, nil
}
