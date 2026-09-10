package gitlab

import "strings"

// NormalizeHost lowercases and trims a host string. This is the canonical
// normalization used by both the SCM provider and the tracker for host
// comparison and allowlist lookup.
func NormalizeHost(host string) string {
	return strings.ToLower(strings.TrimSpace(host))
}

// IsGitLabDotCom reports whether host refers to the default GitLab instance
// (gitlab.com). The empty string, "gitlab.com", and "www.gitlab.com" all
// map to the default instance and use the default base URL + token.
func IsGitLabDotCom(host string) bool {
	host = NormalizeHost(host)
	return host == "" || host == "gitlab.com" || host == "www.gitlab.com"
}
