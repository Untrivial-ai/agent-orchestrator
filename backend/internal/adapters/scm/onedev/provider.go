package onedev

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// ErrNoAllowedHosts is returned by NewProvider when the host allowlist is
// empty. OneDev is always self-hosted — there is no onedev.com to fall back
// on — so an empty allowlist means the provider has nowhere to talk to and
// construction fails loudly rather than yielding a provider that rejects
// every remote it is later handed.
var ErrNoAllowedHosts = errors.New("onedev scm: no allowed hosts configured (set AO_ONEDEV_ALLOWED_HOSTS)")

// ErrHostNotAllowed is returned when a remote's host is not in the configured
// allowlist. The provider rejects such hosts before attaching any credential.
var ErrHostNotAllowed = errors.New("onedev scm: host not in allowlist")

// ProviderOptions configures the OneDev SCM provider.
type ProviderOptions struct {
	HTTPClient *http.Client
	// Token is the default credential source, applied to any allowed host
	// without an entry in HostTokens.
	Token TokenSource
	// SkipTokenPreflight disables the local credential check in NewProvider.
	// It does not affect Provider.Preflight, which is the network check.
	SkipTokenPreflight bool
	UserAgent          string
	Logger             *slog.Logger

	// AllowedHosts lists the OneDev instances the provider may talk to. This
	// is required configuration. An entry is "host", "host:port", or a
	// scheme-qualified "http://host:port" / "https://host" — plain HTTP must
	// be written explicitly because self-hosted OneDev is frequently reached
	// over HTTP on a private network, and silently upgrading to HTTPS would
	// make every request fail with a TLS error instead of working.
	AllowedHosts []string

	// HostTokens maps an allowed host to a credential override. Hosts without
	// an entry fall back to Token. The per-host selection keeps one
	// instance's credential from being attached to another instance.
	HostTokens map[string]TokenSource
}

// Provider is the OneDev SCM adapter. It satisfies the observer's
// scm.Provider contract; see observer_provider.go for those methods and
// doc.go for the API-shape decisions behind them.
//
// Every host the provider talks to must appear in the configured allowlist. A
// host that is not in the allowlist is rejected before any credential is
// attached, so no request is ever made to an unconfigured instance.
type Provider struct {
	logger *slog.Logger

	// allowed maps an authority ("10.0.0.30:6610") to its allowlist entry.
	allowed map[string]allowedHost
	// order is the allowlist in stable sorted order, for AllowedHosts and
	// Preflight so their output does not depend on map iteration.
	order []string

	// byHostname indexes allowlist entries by port-less hostname, so a remote
	// cloned over SSH (OneDev's git SSH port, commonly 6611) resolves to the
	// same instance as one cloned over HTTP (commonly 6610). A hostname
	// present more than once is ambiguous and is recorded as such rather than
	// resolved arbitrarily.
	byHostname map[string][]string

	defaultToken TokenSource
	hostTokens   map[string]TokenSource

	httpClient *http.Client
	userAgent  string

	mu      sync.Mutex
	clients map[string]*Client

	// identityMu guards identities, which memoizes the authenticated account
	// per instance. OneDev identity is per-host — two allowlisted instances
	// are two unrelated user databases — so the cache is keyed by authority
	// rather than held as a single value. See identity.go.
	identityMu sync.Mutex
	identities map[string]ports.SCMIdentity

	// cache memoizes OneDev's id lookups (PR number to request id, user id to
	// login, project id to path) that the observer methods would otherwise
	// repeat on every poll.
	cache *cache
}

// NewProvider creates a OneDev SCM provider.
//
// It fails when no allowed hosts are configured, when an allowlist entry is
// malformed, or — unless SkipTokenPreflight is set — when no usable
// credential is configured. All three are local checks; no network call is
// made here. Use Preflight for the authenticated round-trip.
func NewProvider(opts ProviderOptions) (*Provider, error) {
	if len(opts.AllowedHosts) == 0 {
		return nil, ErrNoAllowedHosts
	}

	allowed := make(map[string]allowedHost, len(opts.AllowedHosts))
	byHostname := map[string][]string{}
	for _, raw := range opts.AllowedHosts {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		h, err := parseAllowedHost(raw)
		if err != nil {
			return nil, err
		}
		if prev, dup := allowed[h.authority]; dup {
			// The same instance listed twice is harmless, but listed twice
			// under different schemes it is not: whichever entry came first
			// would decide the transport, so config order would silently
			// decide whether the connection is encrypted. Refuse rather than
			// pick.
			if prev.scheme != h.scheme {
				return nil, fmt.Errorf(
					"onedev scm: host %q is listed with conflicting schemes %q and %q; list it once",
					h.authority, prev.scheme, h.scheme)
			}
			continue
		}
		allowed[h.authority] = h
		name := h.hostname()
		byHostname[name] = append(byHostname[name], h.authority)
	}
	if len(allowed) == 0 {
		return nil, ErrNoAllowedHosts
	}

	order := make([]string, 0, len(allowed))
	for authority := range allowed {
		order = append(order, authority)
	}
	sort.Strings(order)
	for name := range byHostname {
		sort.Strings(byHostname[name])
	}

	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}

	// Per-host token keys go through the same resolution as a git remote, so
	// an entry written as "10.0.0.30" still selects the allowlisted
	// "10.0.0.30:6610". Matching exactly here while tolerating a port
	// mismatch everywhere else would turn a near-miss into a silently
	// dropped override, surfacing much later as ErrNoToken with nothing
	// pointing at the typo.
	hostTokens := make(map[string]TokenSource, len(opts.HostTokens))
	claimedBy := make(map[string]string, len(opts.HostTokens))
	for _, raw := range sortedKeys(opts.HostTokens) {
		src := opts.HostTokens[raw]
		if src == nil {
			continue
		}
		h, ok := resolveAllowedHost(allowed, byHostname, raw)
		if !ok {
			// Not fatal — a surplus entry for a host this daemon does not
			// serve is a normal way to share one environment across
			// deployments — but it is never what a typo'd key looks like from
			// the outside, so say so once at construction.
			logger.Warn("onedev scm: per-host token names no configured host; the override will not be used",
				"host", raw, "allowed_hosts", allowedHostStrings(allowed, order))
			continue
		}
		if prev, dup := claimedBy[h.authority]; dup {
			return nil, fmt.Errorf(
				"onedev scm: per-host tokens %q and %q both resolve to host %q; keep one",
				prev, raw, h.authority)
		}
		claimedBy[h.authority] = raw
		hostTokens[h.authority] = src
	}

	if !opts.SkipTokenPreflight {
		if err := anyCredential(context.Background(), opts.Token, hostTokens); err != nil {
			return nil, err
		}
	}

	return &Provider{
		logger:       logger,
		allowed:      allowed,
		order:        order,
		byHostname:   byHostname,
		defaultToken: opts.Token,
		hostTokens:   hostTokens,
		httpClient:   opts.HTTPClient,
		userAgent:    opts.UserAgent,
		clients:      map[string]*Client{},
		identities:   map[string]ports.SCMIdentity{},
		cache:        newCache(),
	}, nil
}

// anyCredential reports whether the default source or any per-host source can
// yield a credential. It gates NewProvider, so its verdict must not vary
// between runs on identical configuration.
//
// The default source is tried first, then the per-host sources in sorted key
// order. FallbackTokenSource.Token then does the rest: it returns the first
// success wherever it appears, and remembers a hard (non-ErrNoToken) failure
// but returns it only if nothing later succeeds — so one broken credential
// helper cannot mask a working token elsewhere, whatever the order.
//
// Sorting is therefore not what makes a working source win; that holds
// regardless. What it decides is *which* hard failure is reported when several
// sources fail and none succeeds. Without it that error is chosen by Go's map
// iteration order, and a daemon that refuses to start with a different reason
// each time is far harder to diagnose than one that always names the same
// host. TestCredentialResolutionIsOrderIndependent pins this; it fails if
// sortedKeys stops sorting.
func anyCredential(ctx context.Context, def TokenSource, hostTokens map[string]TokenSource) error {
	chain := make(FallbackTokenSource, 0, len(hostTokens)+1)
	if def != nil {
		chain = append(chain, def)
	}
	for _, key := range sortedKeys(hostTokens) {
		chain = append(chain, hostTokens[key])
	}
	_, err := chain.Token(ctx)
	return err
}

// sortedKeys returns a map's keys in a deterministic order. Load-bearing, not
// cosmetic: see anyCredential for what varies without it.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// allowedHostStrings renders the allowlist for a log line.
func allowedHostStrings(allowed map[string]allowedHost, order []string) []string {
	out := make([]string, 0, len(order))
	for _, authority := range order {
		out = append(out, allowed[authority].String())
	}
	return out
}

// AllowedHosts returns the configured allowlist in stable order, rendered as
// scheme-qualified entries.
func (p *Provider) AllowedHosts() []string {
	return allowedHostStrings(p.allowed, p.order)
}

// SCMCredentialsAvailable reports whether usable OneDev credentials exist,
// checking the default source and then every per-host source. A deployment
// that configures only per-host tokens is still usable.
func (p *Provider) SCMCredentialsAvailable(ctx context.Context) (bool, error) {
	err := anyCredential(ctx, p.defaultToken, p.hostTokens)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, ErrNoToken) {
		return false, nil
	}
	return false, err
}

// Preflight performs an authenticated GET /~api/projects against every
// configured host, so a bad credential or an unreachable instance is reported
// at startup rather than on the first poll. Errors from all hosts are joined
// so one broken instance's message is not hidden by another's.
func (p *Provider) Preflight(ctx context.Context) error {
	var errs []error
	for _, authority := range p.order {
		client, err := p.clientForAuthority(authority)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if err := client.Preflight(ctx); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// resolveHost maps a git remote's authority onto a configured allowlist
// entry. An exact authority match wins. Failing that, the port-less hostname
// is matched: OneDev serves git over SSH and HTTP on different ports (6611
// and 6610 in a default install), so the authority of an SSH remote never
// equals the authority of the HTTP API. When a hostname appears in the
// allowlist more than once the match is ambiguous and is rejected rather than
// guessed.
//
// Rewriting the port this way does not widen the trust boundary: the returned
// entry is an allowlisted instance, and it — not the remote's authority — is
// what the credential is ever sent to.
func (p *Provider) resolveHost(authority string) (allowedHost, bool) {
	return resolveAllowedHost(p.allowed, p.byHostname, authority)
}

// resolveAllowedHost is resolveHost's logic as a free function, so NewProvider
// can resolve per-host token keys the same way before the Provider exists.
func resolveAllowedHost(allowed map[string]allowedHost, byHostname map[string][]string, authority string) (allowedHost, bool) {
	key := normalizeHostKey(authority)
	if key == "" {
		return allowedHost{}, false
	}
	if h, ok := allowed[key]; ok {
		return h, true
	}
	matches := byHostname[hostnameOf(key)]
	if len(matches) != 1 {
		return allowedHost{}, false
	}
	return allowed[matches[0]], true
}

// clientForRepo returns the client for a repository's host, or an error when
// the host is not allowlisted.
func (p *Provider) clientForRepo(repo ports.SCMRepo) (*Client, error) {
	h, ok := p.resolveHost(repo.Host)
	if !ok {
		return nil, fmt.Errorf("onedev scm: host %q: %w", repo.Host, ErrHostNotAllowed)
	}
	return p.clientForAuthority(h.authority)
}

// clientForAuthority lazily builds (and memoizes) the client for one
// allowlisted instance, selecting that host's credential override if one is
// configured.
func (p *Provider) clientForAuthority(authority string) (*Client, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if c, ok := p.clients[authority]; ok {
		return c, nil
	}
	h, ok := p.allowed[authority]
	if !ok {
		return nil, fmt.Errorf("onedev scm: host %q: %w", authority, ErrHostNotAllowed)
	}
	token := p.defaultToken
	if src, ok := p.hostTokens[authority]; ok {
		token = src
	}
	c, err := NewClient(ClientOptions{
		HTTPClient: p.httpClient,
		Token:      token,
		APIBase:    h.apiBase(),
		UserAgent:  p.userAgent,
	})
	if err != nil {
		return nil, err
	}
	p.clients[authority] = c
	return c, nil
}

// scpRemoteRe matches the scp-style SSH remote form, e.g.
// "git@onedev.example.com:Homelab/curatarr.git". OneDev installs normally
// hand out an ssh:// URL because their SSH port is not 22, but a hand-written
// scp-style remote is still valid git and is accepted here.
var scpRemoteRe = regexp.MustCompile(`^[^@/]+@([^:/]+):(.+)$`)

// ParseRepository normalizes a git remote into a OneDev repository identity,
// reporting false for anything that is not a remote of an allowlisted OneDev
// instance. Recognised forms:
//
//	ssh://git@host:6611/Homelab/curatarr.git
//	http://host:6610/Homelab/curatarr.git
//	https://host/curatarr.git
//	git@host:Homelab/curatarr.git
//
// A OneDev project path may be a single segment ("curatarr") or nested to any
// depth ("Homelab/curatarr", "Homelab/tools/curatarr"), because OneDev
// projects form a tree rather than GitLab's owner/repo pair. Repo therefore
// carries the full project path, Name the final segment, and Owner the parent
// path — empty for a root project.
//
// Host is normalized to the allowlisted API authority, not the authority
// written in the remote. That way the same project cloned over SSH (port
// 6611) and over HTTP (port 6610) yields one identity, so claim validation
// and PR tracking do not fork by protocol.
func (p *Provider) ParseRepository(remote string) (ports.SCMRepo, bool) {
	remote = strings.TrimSpace(remote)
	if remote == "" {
		return ports.SCMRepo{}, false
	}

	authority, rawPath, ok := splitRemote(remote)
	if !ok {
		return ports.SCMRepo{}, false
	}
	host, ok := p.resolveHost(authority)
	if !ok {
		return ports.SCMRepo{}, false
	}
	owner, name, ok := splitProjectPath(rawPath)
	if !ok {
		return ports.SCMRepo{}, false
	}
	return makeRepo(host.authority, owner, name), true
}

// splitRemote extracts the authority and project path from a git remote in
// any of the supported forms.
func splitRemote(remote string) (authority, path string, ok bool) {
	// ssh://git@host[:port]/project.git
	if strings.HasPrefix(remote, "ssh://") {
		u, err := url.Parse(remote)
		if err != nil || u.Host == "" {
			return "", "", false
		}
		return u.Host, u.Path, true
	}

	// scp-style: git@host:project.git
	if m := scpRemoteRe.FindStringSubmatch(remote); m != nil {
		return m[1], m[2], true
	}

	// http(s)://host[:port]/project.git
	u, err := url.Parse(remote)
	if err != nil || u.Host == "" {
		return "", "", false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", "", false
	}
	return u.Host, u.Path, true
}

// splitProjectPath splits a OneDev project path into its parent path and
// final segment, stripping the optional ".git" suffix. Empty, "." and ".."
// segments are rejected so a traversal-shaped path cannot become a repository
// identity that is later interpolated into an API URL.
func splitProjectPath(raw string) (owner, name string, ok bool) {
	p := strings.Trim(strings.TrimSpace(raw), "/")
	p = strings.TrimSuffix(p, ".git")
	p = strings.Trim(p, "/")
	if p == "" {
		return "", "", false
	}
	parts := strings.Split(p, "/")
	for _, seg := range parts {
		if seg == "" || seg == "." || seg == ".." {
			return "", "", false
		}
	}
	name = parts[len(parts)-1]
	owner = strings.Join(parts[:len(parts)-1], "/")
	return owner, name, true
}

// makeRepo builds the normalized repository identity. Repo is the full
// project path; for a root project (no parent) it equals Name.
func makeRepo(host, owner, name string) ports.SCMRepo {
	full := name
	if owner != "" {
		full = owner + "/" + name
	}
	return ports.SCMRepo{
		Provider: ProviderKey,
		Host:     host,
		Owner:    owner,
		Name:     name,
		Repo:     full,
	}
}
