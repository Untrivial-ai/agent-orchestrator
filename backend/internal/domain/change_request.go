package domain

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// ErrInvalidChangeRequestURL reports an unsupported or malformed PR/MR URL.
var ErrInvalidChangeRequestURL = errors.New("invalid pull request or merge request URL")

// ChangeRequestReference is a normalized, reference-only PR/MR identity.
type ChangeRequestReference struct {
	URL        string
	Provider   string
	Host       string
	Repository string
	Number     int
}

// Key returns the provider-neutral identity used to deduplicate references.
func (r ChangeRequestReference) Key() string {
	return fmt.Sprintf("%s|%s|%s|%d", r.Provider, r.Host, r.Repository, r.Number)
}

// ParseChangeRequestURL accepts complete GitHub PR and GitLab MR web URLs.
// It never fetches the URL or grants SCM tracking authority.
func ParseChangeRequestURL(raw string) (ChangeRequestReference, error) {
	if raw == "" || len(raw) > 2048 || strings.TrimSpace(raw) != raw {
		return ChangeRequestReference{}, ErrInvalidChangeRequestURL
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.RawPath != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" {
		return ChangeRequestReference{}, ErrInvalidChangeRequestURL
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	provider := RepositoryProvider(u.Host)
	hostname := strings.ToLower(u.Hostname())
	if provider == "github" && hostname != "github.com" && hostname != "www.github.com" && !strings.HasSuffix(hostname, ".ghe.io") {
		return ChangeRequestReference{}, ErrInvalidChangeRequestURL
	}
	if provider == "github" && u.Port() != "" {
		return ChangeRequestReference{}, ErrInvalidChangeRequestURL
	}
	var repoParts []string
	var numberText string
	switch {
	case provider == "github" && len(parts) == 4 && parts[2] == "pull":
		repoParts, numberText = parts[:2], parts[3]
	case provider == "gitlab" && len(parts) >= 5 && parts[len(parts)-3] == "-" && parts[len(parts)-2] == "merge_requests":
		repoParts, numberText = parts[:len(parts)-3], parts[len(parts)-1]
	default:
		return ChangeRequestReference{}, ErrInvalidChangeRequestURL
	}
	number, err := strconv.Atoi(numberText)
	if err != nil || number <= 0 {
		return ChangeRequestReference{}, ErrInvalidChangeRequestURL
	}
	repo, err := ParseRepositoryIdentity(u.Scheme + "://" + u.Host + "/" + strings.Join(repoParts, "/"))
	if err != nil {
		return ChangeRequestReference{}, ErrInvalidChangeRequestURL
	}
	name := repo.Namespace + "/" + repo.Name
	host := repo.Host
	if provider == "github" {
		name = strings.ToLower(name)
		u.Scheme = "https"
		if host == "www.github.com" {
			host = "github.com"
		}
	}
	path := "/" + name + "/pull/" + strconv.Itoa(number)
	if provider == "gitlab" {
		path = "/" + name + "/-/merge_requests/" + strconv.Itoa(number)
	}
	return ChangeRequestReference{URL: u.Scheme + "://" + host + path, Provider: provider, Host: host, Repository: name, Number: number}, nil
}
