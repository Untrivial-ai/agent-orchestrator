package gitworktree

import "strings"

func checkRefFormatBranchArgs(repo, branch string) []string {
	return []string{"-C", repo, "check-ref-format", "--branch", branch}
}

func revParseVerifyArgs(repo, ref string) []string {
	return []string{"-C", repo, "rev-parse", "--verify", "--quiet", ref}
}

func remoteListArgs(repo string) []string {
	return []string{"-C", repo, "remote"}
}

func fetchBranchArgs(repo, remote, branch string) []string {
	return []string{"-C", repo, "fetch", remote, "+refs/heads/" + branch + ":refs/remotes/" + remote + "/" + branch}
}

// worktreeAddForce is git's documented override for "<path> is a missing but
// already registered worktree" (git's own hint for that failure is "use
// 'add -f' to override"). It re-registers a path whose registration outlived
// its directory in one step, so recovering a stale registration never needs a
// destructive `worktree remove --force` or a repo-wide `worktree prune` first.
//
// Verified against git 2.54: `worktree add --force` still refuses a path that
// exists and is non-empty ("fatal: '<path>' already exists"), so it can never
// clobber a live worktree or an agent's uncommitted work, and a single --force
// still refuses a missing-but-LOCKED registration (that needs `-f -f`), which
// is what keeps a lock an effective "do not touch" signal. Never pass it twice.
const worktreeAddForce = "--force"

func worktreeAddBranchArgs(repo, path, branch string, force bool) []string {
	args := []string{"-C", repo, "worktree", "add"}
	if force {
		args = append(args, worktreeAddForce)
	}
	return append(args, path, branch)
}

func worktreeAddNewBranchArgs(repo, branch, path, baseRef string, force bool) []string {
	args := []string{"-C", repo, "worktree", "add"}
	if force {
		args = append(args, worktreeAddForce)
	}
	return append(args, "-b", branch, path, baseRef)
}

// worktreeRemoveArgs intentionally omits --force: a dirty worktree (uncommitted
// agent work) MUST cause `git worktree remove` to fail, so the post-prune
// "still registered" check in Destroy surfaces the refusal to the Session
// Manager's Cleanup, which routes the session to Skipped rather than deleting
// the agent's in-progress changes.
func worktreeRemoveArgs(repo, path string) []string {
	return []string{"-C", repo, "worktree", "remove", path}
}

// worktreeForceRemoveArgs passes --force to bypass git's dirty-worktree check.
// Only ForceDestroy may call this. It is safe only AFTER the session's
// uncommitted work has been captured (Task 2's StashUncommitted). Callers that
// have not yet captured work must use worktreeRemoveArgs / Destroy instead.
func worktreeForceRemoveArgs(repo, path string) []string {
	return []string{"-C", repo, "worktree", "remove", "--force", path}
}

func worktreePruneArgs(repo string) []string {
	return []string{"-C", repo, "worktree", "prune"}
}

// statusPorcelainArgs probes the worktree at path for uncommitted changes or
// untracked files — the condition `git worktree remove` (without --force)
// refuses on — so Destroy can classify a refusal as ports.ErrWorkspaceDirty.
func statusPorcelainArgs(path string) []string {
	return []string{"-C", path, "status", "--porcelain"}
}

func worktreeListPorcelainArgs(repo string) []string {
	return []string{"-C", repo, "worktree", "list", "--porcelain"}
}

// addAllTempIndexArgs stages all tracked and non-ignored untracked files into a
// temp index file without touching the real index or the working tree.
// GIT_INDEX_FILE must be set in the command's environment before calling.
func addAllTempIndexArgs(worktree string) []string {
	return []string{"-C", worktree, "add", "-A"}
}

// writeTreeArgs flushes the temp index into a tree object and prints the SHA.
// GIT_INDEX_FILE must be set in the command's environment.
func writeTreeArgs(worktree string) []string {
	return []string{"-C", worktree, "write-tree"}
}

// commitTreeArgs creates a commit object from a tree SHA. parent is the HEAD
// SHA to set as parent; message is the commit message. When parent is empty
// (unborn HEAD), the -p flag is omitted.
func commitTreeArgs(worktree, treeSHA, parent, message string) []string {
	args := []string{"-C", worktree, "commit-tree", treeSHA}
	if parent != "" {
		args = append(args, "-p", parent)
	}
	args = append(args, "-m", message)
	return args
}

// updateRefArgs creates or moves a ref to point at a commit SHA.
func updateRefArgs(worktree, ref, commitSHA string) []string {
	return []string{"-C", worktree, "update-ref", ref, commitSHA}
}

// deleteRefArgs deletes a ref unconditionally.
func deleteRefArgs(worktree, ref string) []string {
	return []string{"-C", worktree, "update-ref", "-d", ref}
}

// revParseHeadArgs returns the HEAD commit SHA in the worktree.
// Exit code 128 means the repo has no commits (unborn HEAD).
func revParseHeadArgs(worktree string) []string {
	return []string{"-C", worktree, "rev-parse", "--verify", "HEAD"}
}

// cherryPickNoCommitArgs applies a single commit's diff onto the current
// working tree via a true three-way merge without committing or moving HEAD.
// git cherry-pick --no-commit computes the diff between <sha> and its parent
// and 3-way-merges it onto the current working tree. On conflict it leaves
// textual conflict markers in the affected files and exits non-zero. New files
// added in the preserve commit come through as additions. Because -n is used,
// no sequencer state is left that would require a cherry-pick --quit afterward.
func cherryPickNoCommitArgs(worktree, commitSHA string) []string {
	return []string{"-C", worktree, "cherry-pick", "--no-commit", commitSHA}
}

// ignoredCountArgs lists files skipped because of .gitignore (dry-run, no mutation).
func ignoredCountArgs(worktree string) []string {
	return []string{"-C", worktree, "status", "--ignored", "--porcelain"}
}

func configuredBaseRefCandidates(defaultBranch string) []string {
	if strings.Contains(defaultBranch, "/") {
		// A qualified default ("upstream/main") is used verbatim; git's refname
		// disambiguation already falls back to refs/heads/<defaultBranch>.
		return []string{defaultBranch}
	}
	// The local head comes after origin/<defaultBranch> so remote-tracking
	// still wins when present, but a remoteless repo can base new branches
	// on its local default branch instead of failing BRANCH_NOT_FETCHED.
	return []string{"origin/" + defaultBranch, "refs/heads/" + defaultBranch}
}
