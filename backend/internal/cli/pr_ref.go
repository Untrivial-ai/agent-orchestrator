package cli

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

func (c *commandContext) resolvePRRef(ctx context.Context, ref string, project projectDetails) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", usageError{errors.New("PR reference must be a PR/MR URL or a number")}
	}
	if isNumericPRRef(ref) {
		repo := strings.TrimSpace(project.Repo)
		if repo == "" {
			// The daemon must not shell out to external CLIs from its loopback API;
			// when the durable project record lacks repo_origin_url, the thin CLI
			// does the one-off gh lookup from the registered project checkout and
			// sends the daemon a normalized URL.
			out, err := c.deps.CommandOutputInDir(ctx, project.Path, "gh", "repo", "view", "--json", "url", "-q", ".url")
			if err != nil || strings.TrimSpace(string(out)) == "" {
				return "", usageError{errors.New("gh not available; pass the full PR URL")}
			}
			repo = strings.TrimSpace(string(out))
		}
		host, owner, name, err := cliRepoFromURL(repo)
		if err != nil {
			return "", usageError{errors.New("PR reference must be a PR/MR URL or a number")}
		}
		n, _ := strconv.Atoi(strings.TrimPrefix(ref, "#"))
		return cliPRURLFromParts(host, owner, name, n), nil
	}
	host, owner, name, n, err := cliParsePRURL(ref)
	if err != nil || host == "" || owner == "" || name == "" || n <= 0 {
		return "", usageError{errors.New("PR reference must be a PR/MR URL or a number")}
	}
	return cliPRURLFromParts(host, owner, name, n), nil
}

func isNumericPRRef(ref string) bool {
	ref = strings.TrimPrefix(strings.TrimSpace(ref), "#")
	n, err := strconv.Atoi(ref)
	return err == nil && n > 0
}

// cliPRURLFromParts constructs the canonical PR/MR URL for a provider.
// GitHub uses /pull/N; GitLab uses /-/merge_requests/N.
func cliPRURLFromParts(host, owner, repo string, number int) string {
	if isCLIGitHubHost(host) {
		return fmt.Sprintf("https://%s/%s/%s/pull/%d", host, owner, repo, number)
	}
	return fmt.Sprintf("https://%s/%s/%s/-/merge_requests/%d", host, owner, repo, number)
}

func cliParsePRURL(raw string) (host, owner, name string, number int, err error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", "", "", 0, err
	}
	if !strings.EqualFold(u.Scheme, "https") {
		return "", "", "", 0, errors.New("not https")
	}
	host = u.Hostname()
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")

	// GitHub: /owner/repo/pull/N → 4 parts, parts[2] == "pull"
	if len(parts) == 4 && parts[2] == "pull" {
		n, parseErr := strconv.Atoi(parts[3])
		if parseErr != nil || n <= 0 {
			return "", "", "", 0, errors.New("bad number")
		}
		return host, parts[0], strings.TrimSuffix(parts[1], ".git"), n, nil
	}

	// GitLab: /owner/repo/-/merge_requests/N
	// Supports nested groups: /group/subgroup/repo/-/merge_requests/N
	if len(parts) >= 5 && parts[len(parts)-2] == "merge_requests" && parts[len(parts)-3] == "-" {
		n, parseErr := strconv.Atoi(parts[len(parts)-1])
		if parseErr != nil || n <= 0 {
			return "", "", "", 0, errors.New("bad number")
		}
		repoParts := parts[:len(parts)-3]
		if len(repoParts) < 2 {
			return "", "", "", 0, errors.New("bad repo path")
		}
		owner = strings.Join(repoParts[:len(repoParts)-1], "/")
		name = strings.TrimSuffix(repoParts[len(repoParts)-1], ".git")
		return host, owner, name, n, nil
	}

	return "", "", "", 0, errors.New("not a PR/MR URL")
}

func cliRepoFromURL(raw string) (host, owner, name string, err error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", "", "", errors.New("empty repo")
	}
	if strings.HasPrefix(raw, "git@") {
		rest := strings.TrimPrefix(raw, "git@")
		colonIdx := strings.Index(rest, ":")
		if colonIdx < 0 {
			return "", "", "", errors.New("bad ssh remote")
		}
		host = rest[:colonIdx]
		path := rest[colonIdx+1:]
		parts := strings.Split(strings.TrimSuffix(path, ".git"), "/")
		if len(parts) < 2 {
			return "", "", "", errors.New("bad repo")
		}
		for _, seg := range parts {
			if seg == "" {
				return "", "", "", errors.New("bad repo")
			}
		}
		name = parts[len(parts)-1]
		owner = strings.Join(parts[:len(parts)-1], "/")
		return host, owner, name, nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", "", "", err
	}
	host = u.Hostname()
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 2 {
		return "", "", "", errors.New("bad repo")
	}
	for _, seg := range parts {
		if seg == "" {
			return "", "", "", errors.New("bad repo")
		}
	}
	name = strings.TrimSuffix(parts[len(parts)-1], ".git")
	owner = strings.Join(parts[:len(parts)-1], "/")
	return host, owner, name, nil
}

func isCLIGitHubHost(host string) bool {
	host = strings.ToLower(host)
	return host == "github.com" || host == "www.github.com" || host == "api.github.com" ||
		strings.HasSuffix(host, ".github.com") || strings.HasSuffix(host, ".ghe.io")
}
