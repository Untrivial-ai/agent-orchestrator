package project

import (
	"sort"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// maxGitHubOwnerLen is GitHub's limit on a login (user or organization name).
// A longer first path segment is not an owner, so it is not reported.
const maxGitHubOwnerLen = 39

// githubOwner extracts the owner segment from a GitHub remote URL, or "" if the
// remote is empty or does not point at github.com. It returns only that
// segment, never the repo name, the host, or the full path, so telemetry can
// attribute usage without shipping the repository identity — and in particular
// without shipping the credentials that a remote URL may embed.
//
// The accepted shapes are the ones git itself writes or accepts as an origin:
//
//   - URL form, any of the https/http/ssh/git schemes, with optional userinfo
//     and an optional port: https://github.com/owner/repo,
//     https://<token>@github.com/owner/repo, ssh://git@github.com:22/owner/repo.
//   - SCP-like form, with optional user: git@github.com:owner/repo.
//
// The host may be an SSH config alias of the shape github.com-<name>, the
// convention for juggling several GitHub accounts on one machine
// (git@github.com-work:owner/repo). Scheme and host compare case-insensitively;
// the owner segment keeps the case git recorded.
//
// GitHub Enterprise hosts are deliberately NOT matched. An owner on
// github.mycompany.com is a different namespace from a github.com org, and
// merging the two into one property would make the field ambiguous.
func githubOwner(remote string) string {
	r := strings.TrimSpace(remote)
	if r == "" {
		return ""
	}
	host, path, ok := splitRemote(r)
	if !ok || !isGitHubHost(host) {
		return ""
	}
	return ownerSegment(path)
}

// splitRemote splits a git remote into its host and the path that follows,
// covering both the URL form (scheme://[userinfo@]host[:port]/path) and the
// SCP-like form ([user@]host:path). Userinfo is discarded rather than parsed:
// it routinely carries a token, which must never leave this function.
func splitRemote(remote string) (host, path string, ok bool) {
	if scheme, rest, found := strings.Cut(remote, "://"); found {
		if !isRemoteScheme(scheme) {
			return "", "", false
		}
		authority, p, _ := strings.Cut(rest, "/")
		return hostFromAuthority(authority, true), p, true
	}
	// SCP-like. The separator is the first colon, and anything before it that
	// looks like a path (or a Windows drive letter) is a local remote, not a
	// host.
	authority, p, found := strings.Cut(remote, ":")
	if !found || strings.ContainsAny(authority, "/\\") {
		return "", "", false
	}
	return hostFromAuthority(authority, false), p, true
}

// isRemoteScheme reports whether scheme is one git uses for a GitHub remote.
func isRemoteScheme(scheme string) bool {
	switch strings.ToLower(scheme) {
	case "https", "http", "ssh", "git":
		return true
	default:
		return false
	}
}

// hostFromAuthority strips any userinfo and, for URL-form remotes, any explicit
// port. The SCP-like form has no port — there, a colon already ended the
// authority.
func hostFromAuthority(authority string, stripPort bool) string {
	if i := strings.LastIndexByte(authority, '@'); i >= 0 {
		authority = authority[i+1:]
	}
	if stripPort {
		if i := strings.LastIndexByte(authority, ':'); i >= 0 {
			authority = authority[:i]
		}
	}
	return authority
}

// isGitHubHost reports whether host is github.com, written in any case, or an
// SSH config alias of it.
func isGitHubHost(host string) bool {
	h := strings.ToLower(strings.TrimSuffix(host, "."))
	return h == "github.com" || h == "www.github.com" || strings.HasPrefix(h, "github.com-")
}

// ownerSegment returns the first path segment when it is followed by a repo
// segment and looks like a GitHub login, and "" otherwise.
func ownerSegment(path string) string {
	p := strings.TrimLeft(path, "/")
	i := strings.IndexByte(p, '/')
	if i <= 0 {
		return ""
	}
	owner := p[:i]
	if !isGitHubLogin(owner) {
		return ""
	}
	return owner
}

// isGitHubLogin reports whether s has the shape of a GitHub login (a user or
// org name). Its job is to keep path noise — dot segments, percent escapes,
// stray query strings — out of the exported property, not to prove the account
// exists.
//
// GitHub's rule, per its username-normalization documentation: a login "can
// only contain alphanumeric characters and dashes", cannot begin or end with a
// dash, cannot contain two consecutive dashes, and is at most 39 characters.
// Any non-alphanumeric character is normalized to a dash, so an underscore can
// never reach a real login and is rejected here.
//
// This matches that rule on every point but one: two consecutive dashes are
// deliberately tolerated. That constraint is stated for names GitHub normalizes
// at signup, and whether it was ever applied retroactively to older accounts is
// not something this code can establish. The two errors are not symmetric —
// wrongly rejecting a real owner silently loses the attribution this parser
// exists to recover, whereas letting "a--b" through costs nothing, since the
// host is already known to be github.com and the segment is the owner by
// construction.
func isGitHubLogin(s string) bool {
	if s == "" || len(s) > maxGitHubOwnerLen {
		return false
	}
	if strings.HasPrefix(s, "-") || strings.HasSuffix(s, "-") {
		return false
	}
	for _, c := range s {
		alnum := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
		if !alnum && c != '-' {
			return false
		}
	}
	return true
}

// workspaceRepoOwners returns the distinct owners across a workspace's child
// repositories, sorted so the result never depends on enumeration order.
// Ownership comes from githubOwner, so children without a remote, or with a
// remote on a host it does not match, contribute nothing.
func workspaceRepoOwners(repos []domain.WorkspaceRepoRecord) []string {
	seen := make(map[string]struct{}, len(repos))
	owners := make([]string, 0, len(repos))
	for _, repo := range repos {
		owner := githubOwner(repo.RepoOriginURL)
		if owner == "" {
			continue
		}
		if _, dup := seen[owner]; dup {
			continue
		}
		seen[owner] = struct{}{}
		owners = append(owners, owner)
	}
	sort.Strings(owners)
	return owners
}
