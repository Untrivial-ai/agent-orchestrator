package domain

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// RepositoryIdentity identifies a provider repository, including its entire namespace.
// Git transport remotes never implicitly authorize additional repositories.
type RepositoryIdentity struct {
	Provider string
	// Host includes an explicit port, matching the SCM provider authority.
	Host      string
	Namespace string
	Name      string
}

// RepositoryProvider follows the SCM dispatcher: recognized GitHub hosts use
// GitHub; other hosts use GitLab, including self-managed installations.
func RepositoryProvider(host string) string {
	if hostname, _, err := net.SplitHostPort(host); err == nil {
		host = hostname
	}
	host = strings.ToLower(host)
	if host == "github.com" || host == "www.github.com" || host == "api.github.com" || strings.HasSuffix(host, ".github.com") || strings.HasSuffix(host, ".ghe.io") {
		return "github"
	}
	return "gitlab"
}

// ParseRepositoryIdentity accepts repository URLs and Git SSH remotes, never PR URLs.
func ParseRepositoryIdentity(raw string) (RepositoryIdentity, error) {
	invalid := errors.New("expected a repository URL with host and full namespace/repository")
	if raw == "" || strings.TrimSpace(raw) != raw {
		return RepositoryIdentity{}, invalid
	}
	if strings.HasPrefix(raw, "git@") {
		host, path, ok := strings.Cut(strings.TrimPrefix(raw, "git@"), ":")
		if !ok {
			return RepositoryIdentity{}, invalid
		}
		raw = "ssh://git@" + host + "/" + path
	}
	u, err := url.Parse(raw)
	if err != nil {
		return RepositoryIdentity{}, invalid
	}
	if (u.Scheme != "https" && u.Scheme != "http" && u.Scheme != "ssh") || u.Hostname() == "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawPath != "" || strings.ContainsAny(u.Path, `\`) {
		return RepositoryIdentity{}, invalid
	}
	if strings.HasSuffix(u.Host, ":") {
		return RepositoryIdentity{}, invalid
	}
	if u.Port() != "" {
		port, err := strconv.Atoi(u.Port())
		if err != nil || port < 1 || port > 65535 {
			return RepositoryIdentity{}, invalid
		}
	}
	parts := strings.Split(strings.TrimSuffix(strings.TrimPrefix(u.Path, "/"), "/"), "/")
	if len(parts) < 2 {
		return RepositoryIdentity{}, invalid
	}
	parts[len(parts)-1] = strings.TrimSuffix(parts[len(parts)-1], ".git")
	for _, part := range parts {
		if part == "" || part == "." || part == ".." || part == "-" || strings.ContainsAny(part, " \t\r\n?#%") {
			return RepositoryIdentity{}, invalid
		}
	}
	provider := RepositoryProvider(u.Hostname())
	if provider == "github" && len(parts) != 2 {
		return RepositoryIdentity{}, invalid
	}
	return RepositoryIdentity{Provider: provider, Host: strings.ToLower(u.Host), Namespace: strings.Join(parts[:len(parts)-1], "/"), Name: parts[len(parts)-1]}, nil
}

// ValidateCanonicalRepository keeps an explicitly selected upstream on the
// checkout's provider and host. Empty canonical identity preserves origin-only claims.
func (c ProjectConfig) ValidateCanonicalRepository(origin string) error {
	if c.CanonicalRepoURL == "" {
		return nil
	}
	upstream, err := ParseRepositoryIdentity(c.CanonicalRepoURL)
	canonicalURL, urlErr := url.Parse(c.CanonicalRepoURL)
	if err != nil || urlErr != nil || canonicalURL.Scheme != "https" || canonicalURL.User != nil {
		return fmt.Errorf("canonicalRepoURL: use an HTTPS repository URL without credentials, query, or fragment")
	}
	checkout, err := ParseRepositoryIdentity(origin)
	if err != nil {
		return fmt.Errorf("canonicalRepoURL: project must have a valid checkout origin repository")
	}
	if upstream.Provider != checkout.Provider || upstream.Host != checkout.Host {
		return fmt.Errorf("canonicalRepoURL: upstream must use the checkout origin's provider and host (%s)", checkout.Host)
	}
	return nil
}
