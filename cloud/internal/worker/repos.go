package worker

import (
	"path/filepath"
	"strings"
)

// ExtraRepoDirName derives a safe local directory name from a repo URL, e.g.
// "https://github.com/owner/ChartSnip" -> "ChartSnip".
func ExtraRepoDirName(repoURL string) string {
	trimmed := strings.TrimSuffix(strings.Trim(repoURL, "/"), ".git")
	name := trimmed
	if idx := strings.LastIndex(trimmed, "/"); idx >= 0 {
		name = trimmed[idx+1:]
	}
	name = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			return r
		default:
			return '-'
		}
	}, name)
	if name == "" || name == "." || name == ".." {
		return "repo"
	}
	return name
}

// ExtraRepoPath returns where an additional dev-kit repo is checked out: a
// sibling of the primary workspace checkout. Cloning beside the primary repo
// (rather than in a worker-internal data dir) lets the agent, whose working
// directory is the primary repo, reach it at ../<name>. The launcher and the
// worker both compute the path here so the on-disk location and the path shown
// to the agent always agree.
func ExtraRepoPath(workspace, repoURL string) string {
	return filepath.Join(filepath.Dir(workspace), ExtraRepoDirName(repoURL))
}
