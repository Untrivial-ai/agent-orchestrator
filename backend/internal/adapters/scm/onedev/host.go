package onedev

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

const (
	// ProviderKey is the normalized provider name stamped on every
	// ports.SCMRepo this adapter produces.
	ProviderKey = "onedev"

	// APIBasePath is OneDev's REST root. Note it is "/~api", not "/api" —
	// OneDev reserves the "~" prefix for its own non-project routes so that
	// no project path can shadow the API.
	APIBasePath = "/~api"

	// defaultScheme is applied to an allowlist entry written as a bare
	// authority ("onedev.example.com:6610"). Operators running OneDev over
	// plain HTTP on a private network write the scheme explicitly.
	defaultScheme = "https"
)

// NormalizeHost lowercases and trims a host string. This is the canonical
// normalization used for allowlist lookup and repository-identity comparison.
func NormalizeHost(host string) string {
	return strings.ToLower(strings.TrimSpace(host))
}

// allowedHost is one entry of the configured OneDev host allowlist: the
// scheme and authority (host, optionally with a port) of an instance the
// provider is permitted to talk to. Because OneDev is always self-hosted
// there is no implicitly-allowed host — every instance must appear here
// before any credential is attached to a request for it.
type allowedHost struct {
	// scheme is "http" or "https", lowercased.
	scheme string
	// authority is host[:port], lowercased — e.g. "10.0.0.30:6610".
	authority string
}

// apiBase returns the REST base URL for this host, e.g.
// "http://10.0.0.30:6610/~api".
func (h allowedHost) apiBase() string {
	return h.scheme + "://" + h.authority + APIBasePath
}

// hostname returns the authority with any port stripped, so an SSH remote
// (OneDev's git SSH port, commonly 6611) can be matched against an allowlist
// entry written with the HTTP API port (commonly 6610).
func (h allowedHost) hostname() string {
	return hostnameOf(h.authority)
}

// String renders the entry the way an operator would write it in
// AO_ONEDEV_ALLOWED_HOSTS.
func (h allowedHost) String() string {
	return h.scheme + "://" + h.authority
}

// parseAllowedHost parses one AO_ONEDEV_ALLOWED_HOSTS entry. An entry without
// a scheme is treated as https. Entries carrying a path, query, fragment, or
// userinfo are rejected: an allowlist entry names an instance, not a URL, and
// silently discarding the extra parts would make the allowlist read as more
// specific than it is.
func parseAllowedHost(raw string) (allowedHost, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return allowedHost{}, fmt.Errorf("onedev scm: empty host entry")
	}
	if !strings.Contains(s, "://") {
		s = defaultScheme + "://" + s
	}
	u, err := url.Parse(s)
	if err != nil {
		return allowedHost{}, fmt.Errorf("onedev scm: invalid host entry %q: %w", raw, err)
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return allowedHost{}, fmt.Errorf("onedev scm: host entry %q: scheme must be http or https", raw)
	}
	if u.Host == "" {
		return allowedHost{}, fmt.Errorf("onedev scm: host entry %q: missing host", raw)
	}
	if u.User != nil {
		return allowedHost{}, fmt.Errorf("onedev scm: host entry %q must not contain credentials", raw)
	}
	if strings.Trim(u.Path, "/") != "" || u.RawQuery != "" || u.Fragment != "" {
		return allowedHost{}, fmt.Errorf("onedev scm: host entry %q must be host[:port] only", raw)
	}
	return allowedHost{scheme: scheme, authority: NormalizeHost(u.Host)}, nil
}

// normalizeHostKey reduces a host string that may carry a scheme (as
// AO_ONEDEV_HOST_TOKENS entries and ports.SCMRepo.Host both may) to the bare
// lowercased authority used as the allowlist map key.
func normalizeHostKey(raw string) string {
	h, err := parseAllowedHost(raw)
	if err != nil {
		return NormalizeHost(raw)
	}
	return h.authority
}

// hostnameOf strips the port from an authority, handling bracketed IPv6
// literals. "10.0.0.30:6610" -> "10.0.0.30", "[::1]:6610" -> "::1".
func hostnameOf(authority string) string {
	authority = NormalizeHost(authority)
	if h, _, err := net.SplitHostPort(authority); err == nil {
		return h
	}
	return strings.Trim(authority, "[]")
}
