package gitcode

import "strings"

// NormalizeHost lowercases and trims a host string.
func NormalizeHost(host string) string {
	return strings.ToLower(strings.TrimSpace(host))
}

// IsGitCodeDotCom reports whether host refers to the public GitCode instance
// (gitcode.com). The empty string, "gitcode.com", and "www.gitcode.com" all
// map to the default instance. GitCode currently has no self-managed
// offering, so no other hosts are trusted with credentials.
func IsGitCodeDotCom(host string) bool {
	host = NormalizeHost(host)
	return host == "" || host == "gitcode.com" || host == "www.gitcode.com"
}
