package modelcatalog

import (
	"context"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// repoConfigRefPatterns are the refs a launch worktree can be seeded from or
// attached to: any local branch, remote-tracking branch, or tag, plus HEAD.
var repoConfigRefPatterns = []string{"refs/heads", "refs/remotes", "refs/tags"}

// repoMayHoldConfig reports whether the repository containing workingDir may
// hold any of the named config files (slash-separated, relative to a
// directory) for a session AO launches.
//
// Sessions run in AO worktrees seeded from a commit that is chosen per session
// (the project base branch, the repository default, or an existing remote
// branch), so the copy in the checkout is not necessarily the copy the session
// sees: it may be staged, uncommitted, untracked, or from another branch.
// Repository config is therefore never read for the default; its possible
// presence only makes the default unresolved. Every directory from the git
// root down to workingDir is checked, on disk and at the tip of every ref a
// session could be seeded from, so a file that exists only on another branch
// or was deleted locally but is still committed also counts. If the refs
// cannot be inspected, the repository is assumed to hold config. Outside a
// git repository only workingDir itself is checked on disk.
func repoMayHoldConfig(workingDir string, names ...string) bool {
	if workingDir == "" || len(names) == 0 {
		return false
	}
	top, prefix, ok := gitTopLevel(workingDir)
	if !ok {
		return anyFileExists(workingDir, names)
	}
	var repoPaths []string
	for _, dir := range repoDirsDownTo(prefix) {
		if anyFileExists(filepath.Join(top, filepath.FromSlash(dir)), names) {
			return true
		}
		for _, name := range names {
			repoPaths = append(repoPaths, path.Join(dir, name))
		}
	}
	return anyRefHoldsAny(top, repoPaths)
}

// repoDirsDownTo lists the repository-relative directories from the root ("")
// down to prefix, e.g. "a/b/" yields "", "a", "a/b".
func repoDirsDownTo(prefix string) []string {
	dirs := []string{""}
	current := ""
	for _, part := range strings.Split(strings.Trim(prefix, "/"), "/") {
		if part == "" {
			continue
		}
		current = path.Join(current, part)
		dirs = append(dirs, current)
	}
	return dirs
}

func anyFileExists(dir string, names []string) bool {
	for _, name := range names {
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(name))); err == nil {
			return true
		}
	}
	return false
}

func gitTopLevel(dir string) (top, prefix string, ok bool) {
	out, err := runGit(dir, "rev-parse", "--show-toplevel", "--show-prefix")
	if err != nil {
		return "", "", false
	}
	lines := strings.Split(strings.TrimRight(string(out), "\r\n"), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) == "" {
		return "", "", false
	}
	top = filepath.FromSlash(strings.TrimSpace(lines[0]))
	if len(lines) > 1 {
		prefix = strings.TrimSpace(lines[1])
	}
	return top, prefix, true
}

// anyRefHoldsAny reports whether the tip of HEAD or any ref matching
// repoConfigRefPatterns contains any of repoPaths. It answers every ref and
// path with a single `git cat-file --batch-check`, and assumes true when git
// cannot answer.
func anyRefHoldsAny(top string, repoPaths []string) bool {
	out, err := runGit(top, append([]string{"for-each-ref", "--format=%(objectname)"}, repoConfigRefPatterns...)...)
	if err != nil {
		return true
	}
	tips := []string{"HEAD"}
	seen := map[string]bool{}
	for _, tip := range strings.Fields(string(out)) {
		if !seen[tip] {
			seen[tip] = true
			tips = append(tips, tip)
		}
	}
	var query strings.Builder
	for _, tip := range tips {
		for _, repoPath := range repoPaths {
			query.WriteString(tip + ":" + repoPath + "\n")
		}
	}
	out, err = runGitInput(top, query.String(), "cat-file", "--batch-check")
	if err != nil {
		return true
	}
	// Found objects print "<oid> <type> <size>"; unmatched queries print the
	// query followed by "missing" (or "ambiguous"), and the query itself may
	// contain spaces, so match on the type field.
	for _, line := range strings.Split(string(out), "\n") {
		if fields := strings.Fields(line); len(fields) == 3 && (fields[1] == "blob" || fields[1] == "tree") {
			return true
		}
	}
	return false
}

func runGit(dir string, args ...string) ([]byte, error) {
	return runGitInput(dir, "", args...)
}

func runGitInput(dir, input string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	if input != "" {
		cmd.Stdin = strings.NewReader(input)
	}
	return cmd.Output()
}
