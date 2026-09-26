package onedev

import (
	"fmt"
	"net"
	"net/url"
	"strings"

	scmonedev "github.com/aoagents/agent-orchestrator/backend/internal/adapters/scm/onedev"
)

const (
	// ProviderKey is the routing key this adapter answers to. It is the SCM
	// provider's key so a OneDev repository and a OneDev issue name the same
	// provider everywhere in the daemon.
	ProviderKey = scmonedev.ProviderKey

	// apiBasePath is OneDev's REST root — "/~api", not "/api". OneDev reserves
	// the "~" prefix for its own routes so no project path can shadow the API.
	apiBasePath = scmonedev.APIBasePath

	// defaultScheme is applied to an allowlist entry written as a bare
	// authority. Operators running OneDev over plain HTTP on a private network
	// write the scheme explicitly.
	defaultScheme = "https"
)

// allowedHost is one entry of the configured OneDev instance allowlist: the
// scheme and authority of an instance the tracker may talk to. Because OneDev
// is always self-hosted there is no implicitly-allowed host — every instance
// must appear here before any credential is attached to a request for it.
type allowedHost struct {
	// scheme is "http" or "https", lowercased.
	scheme string
	// authority is host[:port], lowercased.
	authority string
}

// apiBase returns the REST root for this instance, e.g.
// "http://10.0.0.30:6610/~api".
func (h allowedHost) apiBase() string { return h.webBase() + apiBasePath }

// webBase returns the browser-facing origin, e.g. "http://10.0.0.30:6610".
func (h allowedHost) webBase() string { return h.scheme + "://" + h.authority }

// hostname returns the authority with any port stripped, so a repository
// recorded against OneDev's git SSH port (commonly 6611) still resolves to the
// allowlist entry written with the HTTP API port (commonly 6610).
func (h allowedHost) hostname() string { return hostnameOf(h.authority) }

// String renders the entry the way an operator would write it in
// AO_ONEDEV_ALLOWED_HOSTS.
func (h allowedHost) String() string { return h.webBase() }

// parseAllowedHost parses one allowlist entry. An entry without a scheme is
// treated as https. Entries carrying a path, query, fragment or userinfo are
// rejected: an allowlist entry names an instance, not a URL, and silently
// discarding the extra parts would make the allowlist read as more specific
// than it is.
//
// This mirrors the SCM provider's parser entry-for-entry so one
// AO_ONEDEV_ALLOWED_HOSTS value configures both adapters identically.
func parseAllowedHost(raw string) (allowedHost, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return allowedHost{}, fmt.Errorf("onedev tracker: empty host entry")
	}
	if !strings.Contains(s, "://") {
		s = defaultScheme + "://" + s
	}
	u, err := url.Parse(s)
	if err != nil {
		return allowedHost{}, fmt.Errorf("onedev tracker: invalid host entry %q: %w", raw, err)
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return allowedHost{}, fmt.Errorf("onedev tracker: host entry %q: scheme must be http or https", raw)
	}
	if u.Host == "" {
		return allowedHost{}, fmt.Errorf("onedev tracker: host entry %q: missing host", raw)
	}
	if u.User != nil {
		return allowedHost{}, fmt.Errorf("onedev tracker: host entry %q must not contain credentials", raw)
	}
	if strings.Trim(u.Path, "/") != "" || u.RawQuery != "" || u.Fragment != "" {
		return allowedHost{}, fmt.Errorf("onedev tracker: host entry %q must be host[:port] only", raw)
	}
	return allowedHost{scheme: scheme, authority: scmonedev.NormalizeHost(u.Host)}, nil
}

// normalizeHostKey reduces a host string that may carry a scheme (as
// TrackerID.Host and per-host token keys both may) to the bare lowercased
// authority used as the allowlist map key.
func normalizeHostKey(raw string) string {
	h, err := parseAllowedHost(raw)
	if err != nil {
		return scmonedev.NormalizeHost(raw)
	}
	return h.authority
}

// hostnameOf strips the port from an authority, handling bracketed IPv6
// literals. "10.0.0.30:6610" -> "10.0.0.30", "[::1]:6610" -> "::1".
func hostnameOf(authority string) string {
	authority = scmonedev.NormalizeHost(authority)
	if h, _, err := net.SplitHostPort(authority); err == nil {
		return h
	}
	return strings.Trim(authority, "[]")
}
