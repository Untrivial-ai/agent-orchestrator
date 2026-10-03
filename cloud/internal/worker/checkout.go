package worker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const scratchRepositoryHost = "scratch.ao.local"

const (
	cloudGitAuthorName  = "AO Cloud Agent"
	cloudGitAuthorEmail = "noreply@aoagents.com"
	// WorkspaceReviewBaseRef is an immutable, AO-owned baseline stored with the
	// checkout so committed changes survive worker restarts and restores.
	WorkspaceReviewBaseRef = "refs/ao/diff-base"
	// PreservedBranchRefPrefix namespaces the AO-owned ref that carries a
	// session's branch tip when origin/<branch> itself is blocked
	// (non-fast-forward) or has been rewritten, so boot-time restore always has
	// an attestable fetch location for the session's own lineage. Force-pushed
	// by the checkpoint, gated by the control plane's attested tip on restore.
	PreservedBranchRefPrefix = "refs/ao/preserved-branch/"
)

// PreservedBranchRef is the full AO-owned fallback ref for a session's branch.
func PreservedBranchRef(sessionID string) string {
	return PreservedBranchRefPrefix + sessionID
}

type GitRunner interface {
	Run(context.Context, string, map[string]string, ...string) (string, error)
}

type ExecGitRunner struct{}

func (ExecGitRunner) Run(ctx context.Context, dir string, env map[string]string, args ...string) (string, error) {
	// Pin git wire protocol v0. git 2.39 defaults to protocol v2 over HTTP/2,
	// which is mangled through some sandbox egress paths: the ref-listing
	// handshake fails with "expected flush after ref listing" plus a spurious
	// "could not read Username" prompt, crash-looping checkout for public repos.
	// v0 is universally compatible; the only cost is slightly larger fetches.
	command := exec.CommandContext(ctx, "git", append([]string{"-c", "protocol.version=0"}, args...)...)
	command.Dir = dir
	command.Env = replaceEnvironment(os.Environ(), env)
	// Abort a stalled HTTP transfer instead of hanging until the whole sandbox is
	// torn down. If throughput stays under 1 KB/s for 60s, git fails the
	// clone/fetch/push. A stalled clone otherwise blocks worker startup - and thus
	// the agent terminal, which is only created after checkout - indefinitely,
	// which is exactly the multi-minute "terminal never connects" stall we saw.
	// This targets stalls, not slow-but-progressing transfers, so a large repo
	// still clones; and it is a no-op for local git ops, which do no HTTP.
	command.Env = append(command.Env,
		"GIT_HTTP_LOW_SPEED_LIMIT=1000",
		"GIT_HTTP_LOW_SPEED_TIME=60",
	)
	var output bytes.Buffer
	command.Stdout, command.Stderr = &output, &output
	if err := command.Run(); err != nil {
		message := strings.TrimSpace(output.String())
		for _, secret := range env {
			if secret != "" {
				message = strings.ReplaceAll(message, secret, "[REDACTED]")
			}
		}
		if message == "" {
			return "", fmt.Errorf("git command failed: %w", err)
		}
		return "", fmt.Errorf("git command failed: %s", message)
	}
	return output.String(), nil
}

// PrepareCheckout clones once, then validates and fetches the persistent
// workspace. The token exists only in the network command's askpass environment.
func PrepareCheckout(ctx context.Context, runner GitRunner, workspace string, grant CheckoutGrantResponse) error {
	if runner == nil {
		return errors.New("git runner is required")
	}
	workspace = filepath.Clean(strings.TrimSpace(workspace))
	if !filepath.IsAbs(workspace) || workspace == string(filepath.Separator) {
		return errors.New("workspace path must be an absolute non-root directory")
	}
	expected, err := githubRepositoryIdentity(grant.CloneURL)
	if err != nil {
		return fmt.Errorf("validate checkout grant: %w", err)
	}
	if grant.Token != "" && !grant.ExpiresAt.After(time.Now().Add(30*time.Second)) {
		return errors.New("checkout grant is expired")
	}
	info, statErr := os.Stat(workspace)
	if statErr == nil {
		if !info.IsDir() {
			return errors.New("workspace path is not a directory")
		}
		entries, err := os.ReadDir(workspace)
		if err != nil {
			return fmt.Errorf("inspect workspace contents: %w", err)
		}
		if len(entries) == 0 {
			statErr = os.ErrNotExist
		} else if _, gitErr := os.Stat(filepath.Join(workspace, ".git")); gitErr == nil {
			if err := validateOrigin(ctx, runner, workspace, expected); err != nil {
				return err
			}
			return withGitCredential(grant.Token, func(env map[string]string) error {
				_, err := runner.Run(ctx, workspace, env, "fetch", "--prune", "--", "origin")
				return err
			})
		} else {
			// The workspace is non-empty but not a Git checkout. This happens
			// when the coding agent starts first and writes files (for example
			// .claude) into the workspace before the checkout runs. Clone into a
			// staging directory and merge the result in, so a bare git clone into
			// a non-empty directory does not fail and checkout no longer depends
			// on agent-vs-checkout startup ordering.
			return cloneIntoNonEmptyWorkspace(ctx, runner, workspace, grant, expected)
		}
	}
	if !errors.Is(statErr, os.ErrNotExist) {
		return fmt.Errorf("inspect workspace: %w", statErr)
	}
	if err := os.MkdirAll(filepath.Dir(workspace), 0o700); err != nil {
		return fmt.Errorf("create workspace parent: %w", err)
	}
	if err := withGitCredential(grant.Token, func(env map[string]string) error {
		_, err := runner.Run(ctx, filepath.Dir(workspace), env,
			"clone", "--origin", "origin", "--no-tags", "--", grant.CloneURL, workspace)
		return err
	}); err != nil {
		return err
	}
	return validateOrigin(ctx, runner, workspace, expected)
}

// EnsureWorkspaceReviewBase records the checkout's comparison baseline once.
// Existing refs are deliberately left untouched even when a later fetch moves
// origin/<defaultBranch>.
func EnsureWorkspaceReviewBase(ctx context.Context, runner GitRunner, workspace, defaultBranch string) error {
	if runner == nil {
		return errors.New("git runner is required")
	}
	// A scratch / freshly-initialized workspace has an unborn HEAD (git init with
	// no commit), so there is no history to anchor a review base to. Treat it as a
	// no-op instead of failing worker startup on the later rev-list HEAD, which
	// aborts every no-repo session across all providers.
	if _, err := runner.Run(ctx, workspace, nil, "rev-parse", "--verify", "HEAD"); err != nil {
		return nil
	}
	existingOutput, existingErr := runner.Run(ctx, workspace, nil, "rev-parse", "--verify", WorkspaceReviewBaseRef)
	configuredCandidate := ""
	if branch := strings.TrimSpace(defaultBranch); branch != "" {
		if output, err := runner.Run(ctx, workspace, nil, "merge-base", "origin/"+branch, "HEAD"); err == nil {
			configuredCandidate = strings.TrimSpace(output)
		}
	}
	remoteHeadCandidate := ""
	if configuredCandidate == "" {
		if output, err := runner.Run(ctx, workspace, nil, "merge-base", "refs/remotes/origin/HEAD", "HEAD"); err == nil {
			remoteHeadCandidate = strings.TrimSpace(output)
		}
	}
	if existingErr == nil {
		// Older workers fell straight back to the repository root when a project
		// called its default branch "main" but the remote used "master" (or vice
		// versa). Repair only that recognizable legacy fallback; every non-root
		// baseline remains immutable across fetches and restores.
		if configuredCandidate != "" || remoteHeadCandidate == "" {
			return nil
		}
		rootOutput, err := runner.Run(ctx, workspace, nil, "rev-list", "--max-parents=0", "--reverse", "HEAD")
		if err != nil {
			return nil
		}
		root := strings.TrimSpace(strings.SplitN(rootOutput, "\n", 2)[0])
		if strings.TrimSpace(existingOutput) != root || remoteHeadCandidate == root {
			return nil
		}
		if _, err := runner.Run(ctx, workspace, nil, "update-ref", WorkspaceReviewBaseRef, remoteHeadCandidate); err != nil {
			return fmt.Errorf("repair workspace review base: %w", err)
		}
		return nil
	}

	candidate := configuredCandidate
	if candidate == "" {
		candidate = remoteHeadCandidate
	}
	if candidate == "" {
		output, err := runner.Run(ctx, workspace, nil, "rev-list", "--max-parents=0", "--reverse", "HEAD")
		if err != nil {
			return fmt.Errorf("resolve workspace review base: %w", err)
		}
		candidate = strings.TrimSpace(strings.SplitN(output, "\n", 2)[0])
	}
	if candidate == "" {
		return errors.New("resolve workspace review base: repository has no commits")
	}
	if _, err := runner.Run(ctx, workspace, nil, "update-ref", WorkspaceReviewBaseRef, candidate); err != nil {
		return fmt.Errorf("record workspace review base: %w", err)
	}
	return nil
}

// cloneIntoNonEmptyWorkspace clones the authorized repository into a staging
// directory and merges the result into a workspace that already contains files
// the coding agent wrote (for example .claude) before the checkout ran. Files
// the clone did not produce are preserved; the clone's .git directory and
// tracked files are moved in. This removes the ordering dependency between
// agent startup and repository checkout.
func cloneIntoNonEmptyWorkspace(ctx context.Context, runner GitRunner, workspace string, grant CheckoutGrantResponse, expected string) error {
	// Stage inside the workspace itself, not its parent. The parent is the
	// provider's durable root (e.g. Coder's /home/coder), which is owned by the
	// provider's own user and is not writable by the AO worker user, so a
	// staging dir there fails with "permission denied". The workspace, by
	// contrast, is always writable by the worker (the agent just wrote into it,
	// which is why this non-empty path runs) and is on the same filesystem, so
	// the entry moves below stay a same-filesystem rename. The hidden staging
	// dir is removed before the checkout returns.
	staging, err := os.MkdirTemp(workspace, ".ao-checkout-")
	if err != nil {
		return fmt.Errorf("create checkout staging directory: %w", err)
	}
	defer os.RemoveAll(staging)
	clone := filepath.Join(staging, "repository")
	if err := withGitCredential(grant.Token, func(env map[string]string) error {
		_, err := runner.Run(ctx, staging, env,
			"clone", "--origin", "origin", "--no-tags", "--", grant.CloneURL, clone)
		return err
	}); err != nil {
		return err
	}
	if err := validateOrigin(ctx, runner, clone, expected); err != nil {
		return err
	}
	entries, err := os.ReadDir(clone)
	if err != nil {
		return fmt.Errorf("read cloned repository: %w", err)
	}
	for _, entry := range entries {
		destination := filepath.Join(workspace, entry.Name())
		if _, statErr := os.Stat(destination); statErr == nil {
			// A pre-existing file the agent wrote (for example .claude) stays.
			continue
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return fmt.Errorf("inspect workspace entry %s: %w", entry.Name(), statErr)
		}
		if err := os.Rename(filepath.Join(clone, entry.Name()), destination); err != nil {
			return fmt.Errorf("move %s into workspace: %w", entry.Name(), err)
		}
	}
	return nil
}

// ConfigureWorkerGit prepares a repo-local credential helper that brokers a
// fresh scoped token for each GitHub network operation. The helper stores only
// the rotating worker-token path, never a GitHub token. Restoring the session
// branch is deliberately separate: it is best-effort (CheckoutSessionBranch)
// and never blocks startup.
func ConfigureWorkerGit(
	ctx context.Context,
	runner GitRunner,
	workspace, dataDir, publicURL, sessionID string,
) error {
	if runner == nil {
		return errors.New("git runner is required")
	}
	for label, value := range map[string]string{
		"workspace": workspace, "data directory": dataDir, "public URL": publicURL,
		"session ID": sessionID,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s is required", label)
		}
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return fmt.Errorf("create worker Git credential directory: %w", err)
	}
	binDir := filepath.Join(dataDir, "bin")
	if err := os.MkdirAll(binDir, 0o700); err != nil {
		return fmt.Errorf("create worker tooling directory: %w", err)
	}
	helperPath := GitCredentialHelperPath(dataDir)
	helper := fmt.Sprintf(`#!/bin/sh
set -eu
[ "${1:-}" = "get" ] || exit 0
# git supplies the request as protocol/host/path lines on stdin (path is
# included because credential.useHttpPath is true). Read them so the token can
# be scoped to the exact repository being fetched or pushed — the primary
# checkout or a declared extra dev-kit repository. Any parse miss leaves
# repo_query empty and the control plane issues the broad multi-repository
# grant, so this never narrows a request it cannot classify.
req_host=""
req_path=""
while IFS='=' read -r key value; do
  [ -n "$key" ] || break
  case "$key" in
    host) req_host="$value" ;;
    path) req_path="$value" ;;
  esac
done
# Only ever hand the GitHub installation token to github.com. This helper is
# registered unscoped and credential.useHttpPath makes git invoke it for EVERY
# host, so without this gate a sandbox git operation against a non-github remote
# (which the coding agent can add) would be handed the installation token. exit 0
# returns no credential, so git falls through rather than leaking it.
[ "$req_host" = "github.com" ] || exit 0
repo_query=""
if [ -n "$req_path" ]; then
  repo="${req_path%%.git}"
  repo="${repo#/}"
  # Only a real owner/repo (exactly two segments) is forwarded; a single-segment
  # or multi-segment path leaves repo_query empty → the broad multi-repository
  # grant, never a bogus scoped request.
  case "$repo" in
    */*)
      owner="${repo%%%%/*}"
      name="${repo#*/}"
      case "$name" in */*) name="" ;; esac
      if [ -n "$owner" ] && [ -n "$name" ]; then
        repo_query="?repo=${owner}/${name}"
      fi
      ;;
  esac
fi
worker_token="$(tr -d '\r\n' < %s)"
token_url=%s
response="$(curl -fsS --connect-timeout 10 --max-time 30 -X POST \
  -H "Authorization: Worker ${worker_token}" \
  -H "X-AO-Session-ID: %s" \
  "${token_url}${repo_query}")"
github_token="$(printf '%%s' "$response" | jq -er '.token | select(type == "string" and length > 0)')"
printf 'username=x-access-token\npassword=%%s\n' "$github_token"
`, shellQuote(filepath.Join(dataDir, "worker-token")),
		shellQuote(strings.TrimRight(publicURL, "/")+"/api/cloud/v1/worker/github-token"),
		sessionID)
	if err := os.WriteFile(helperPath, []byte(helper), 0o700); err != nil {
		return fmt.Errorf("write worker Git credential helper: %w", err)
	}
	githubWrapper := fmt.Sprintf(`#!/bin/sh
set -eu
real_gh="${AO_GH_REAL_BINARY:-}"
if [ -z "$real_gh" ]; then
  for candidate in /usr/local/bin/gh /usr/bin/gh; do
    if [ -x "$candidate" ]; then
      real_gh="$candidate"
      break
    fi
  done
fi
if [ -z "$real_gh" ] || [ ! -x "$real_gh" ]; then
  echo "AO GitHub CLI is unavailable; expected /usr/local/bin/gh or /usr/bin/gh" >&2
  exit 127
fi
if [ -n "${GH_TOKEN:-}" ]; then
  github_token="$GH_TOKEN"
elif [ -n "${GITHUB_TOKEN:-}" ]; then
  github_token="$GITHUB_TOKEN"
else
  worker_token="$(tr -d '\r\n' < %s)"
  response="$(curl -fsS --connect-timeout 10 --max-time 30 -X POST \
    -H "Authorization: Worker ${worker_token}" \
    -H "X-AO-Session-ID: %s" \
    %s)"
  github_token="$(printf '%%s' "$response" | jq -er '.token | select(type == "string" and length > 0)')"
fi
if [ "${1:-}" = "pr" ] && [ "${2:-}" = "create" ]; then
  set +e
  output="$(GH_TOKEN="$github_token" "$real_gh" "$@" 2>&1)"
  status=$?
  set -e
  printf '%%s\n' "$output"
  if [ "$status" -ne 0 ]; then
    exit "$status"
  fi
  pr_url="$(printf '%%s\n' "$output" | sed -n 's#.*\(https://github.com/[^[:space:]]*/pull/[0-9][0-9]*\).*#\1#p' | tail -n 1)"
  if [ -z "$pr_url" ]; then
    echo "AO could not observe the pull request URL; run ao claim-pr <number-or-url>." >&2
    exit 1
  fi
  exec ao claim-pr "$pr_url"
fi
GH_TOKEN="$github_token" exec "$real_gh" "$@"
`, shellQuote(filepath.Join(dataDir, "worker-token")), sessionID,
		shellQuote(strings.TrimRight(publicURL, "/")+"/api/cloud/v1/worker/github-token"))
	if err := os.WriteFile(filepath.Join(binDir, "gh"), []byte(githubWrapper), 0o700); err != nil {
		return fmt.Errorf("write worker GitHub CLI wrapper: %w", err)
	}
	commands := [][]string{
		{"config", "--local", "--replace-all", "credential.helper", ""},
		{"config", "--local", "--add", "credential.helper", helperPath},
		{"config", "--local", "--replace-all", "credential.useHttpPath", "true"},
		{"config", "--local", "--replace-all", "user.name", cloudGitAuthorName},
		{"config", "--local", "--replace-all", "user.email", cloudGitAuthorEmail},
	}
	for _, command := range commands {
		if _, err := runner.Run(ctx, workspace, nil, command...); err != nil {
			return fmt.Errorf("configure worker Git repository: %w", err)
		}
	}
	return nil
}

// CheckoutSessionBranch restores the workspace onto the session's branch as a
// best-effort, lineage-preserving operation. The restoration intent is chosen
// from durable facts only:
//
//  1. Local branch exists: switch to it, but only when HEAD cannot move the
//     tree backwards (HEAD is an ancestor of — or equal to — the branch).
//  2. Only origin/<branch> exists: recreate the branch from it ONLY when the
//     control plane attested a tip that is an ancestor of the remote ref. An
//     unattested remote tip may be attacker-controlled (force-push race), so it
//     is never adopted silently.
//  3. When a tip was attested but the visible branch is blocked or rewritten,
//     recover the session's own lineage from the AO-owned preserved-branch ref,
//     gated by the same attestation.
//  4. Otherwise (fresh branch, or every intent failed): fall back to
//     `checkout -B <branch>` at the current HEAD — a tree no-op that keeps the
//     worker on a well-named branch.
//
// Dirty worktrees are carried across tree-moving switches with a stash (popped
// after the switch; retained by git on conflict). A returned error means the
// intended lineage was NOT restored — callers must treat it as a degraded,
// non-fatal warning, never a startup failure.
func CheckoutSessionBranch(
	ctx context.Context,
	runner GitRunner,
	workspace, branch, sessionID, attestedTip string,
) error {
	if runner == nil {
		return errors.New("git runner is required")
	}
	branch = strings.TrimSpace(branch)
	if branch == "" {
		return errors.New("session branch is required")
	}
	attestedTip = strings.TrimSpace(attestedTip)
	attested := attestedTip != ""
	branchRef := "refs/heads/" + branch
	originRef := "refs/remotes/origin/" + branch
	var degraded error

	if refExists(ctx, runner, workspace, branchRef) {
		// Persistent workspace: switch only when the branch cannot move the tree
		// backwards. The local branch is authoritative here, so no fallback ref is
		// consulted when the switch is skipped or fails.
		if !isAncestor(ctx, runner, workspace, "HEAD", branchRef) {
			degraded = fmt.Errorf("workspace has commits ahead of local session branch %q; not switching backwards", branch)
		} else if err := checkoutWithDirtyCarry(ctx, runner, workspace, "checkout", branch); err != nil {
			degraded = fmt.Errorf("switch to local session branch %q: %w", branch, err)
		} else {
			return nil
		}
	} else {
		originExists := refExists(ctx, runner, workspace, originRef)
		switch {
		case originExists && attested &&
			isAncestor(ctx, runner, workspace, attestedTip, originRef):
			if err := checkoutWithDirtyCarry(ctx, runner, workspace, "checkout", "-B", branch, "origin/"+branch); err == nil {
				return nil
			} else {
				degraded = fmt.Errorf("restore session branch %q from origin: %w", branch, err)
			}
		case originExists && !attested:
			degraded = fmt.Errorf("origin/%s tip is not attested by the control plane; not adopting it", branch)
		case originExists:
			degraded = fmt.Errorf("origin/%s does not contain the attested session tip", branch)
		default:
			if attested {
				degraded = fmt.Errorf("origin/%s is unavailable for the attested session tip", branch)
			}
		}
		if attested {
			if err := checkoutFromPreservedBranch(ctx, runner, workspace, branch, sessionID, attestedTip); err == nil {
				return nil
			} else if degraded != nil {
				degraded = fmt.Errorf("%w (preserved-branch fallback: %v)", degraded, err)
			} else {
				degraded = err
			}
		}
	}

	// Fallback: put the workspace on the branch name at the current HEAD without
	// moving the tree. Safe on any state; the branch may re-anchor to HEAD, which
	// is why degraded cases report the lost lineage explicitly (recoverable via
	// reflog and, when previously pushed, origin/<branch>).
	if _, err := runner.Run(ctx, workspace, nil, "checkout", "-B", branch); err != nil {
		if degraded != nil {
			return fmt.Errorf("%w; fallback checkout failed: %v", degraded, err)
		}
		return fmt.Errorf("checkout session branch %q: %w", branch, err)
	}
	if degraded != nil {
		return degraded
	}
	return nil
}

// checkoutFromPreservedBranch fetches the AO-owned preserved-branch ref for the
// session and checks it out as the session branch, but only when the ref
// contains the control plane's attested tip — the same gate as origin/<branch>.
// An error means nothing was checked out (ref absent, unattested, or the
// checkout itself failed); callers fall through to the no-op fallback.
func checkoutFromPreservedBranch(
	ctx context.Context,
	runner GitRunner,
	workspace, branch, sessionID, attestedTip string,
) error {
	ref := PreservedBranchRef(sessionID)
	if _, err := runner.Run(ctx, workspace, nil, "fetch", "origin", ref+":"+ref); err != nil {
		return fmt.Errorf("fetch preserved branch ref: %w", err)
	}
	if !isAncestor(ctx, runner, workspace, attestedTip, ref) {
		return fmt.Errorf("preserved branch ref does not contain the attested session tip")
	}
	return checkoutWithDirtyCarry(ctx, runner, workspace, "checkout", "-B", branch, ref)
}

// checkoutWithDirtyCarry runs a tree-moving checkout while carrying uncommitted
// work across the switch: a dirty workspace is stashed first and popped after.
// A failed pop is reported even when the checkout itself succeeded (the stash
// entry is retained by git so no work is lost).
func checkoutWithDirtyCarry(ctx context.Context, runner GitRunner, workspace string, args ...string) error {
	dirty, err := workspaceDirty(ctx, runner, workspace)
	if err != nil {
		return fmt.Errorf("inspect workspace state: %w", err)
	}
	stashed := false
	if dirty {
		if _, err := runner.Run(ctx, workspace, nil,
			"stash", "push", "--include-untracked", "-m", "ao-session-branch-boot"); err != nil {
			return fmt.Errorf("stash dirty workspace: %w", err)
		}
		stashed = true
	}
	_, checkoutErr := runner.Run(ctx, workspace, nil, args...)
	if stashed {
		if _, popErr := runner.Run(ctx, workspace, nil, "stash", "pop"); popErr != nil {
			if checkoutErr != nil {
				return fmt.Errorf("checkout %v: %v (stash pop failed: %v; stash retained)", args, checkoutErr, popErr)
			}
			return fmt.Errorf("restore stashed workspace state: %w (stash retained)", popErr)
		}
	}
	if checkoutErr != nil {
		return fmt.Errorf("checkout %v: %w", args, checkoutErr)
	}
	return nil
}

func workspaceDirty(ctx context.Context, runner GitRunner, workspace string) (bool, error) {
	out, err := runner.Run(ctx, workspace, nil, "status", "--porcelain")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) != "", nil
}

// isAncestor reports whether ancestor is an ancestor of — or equals — descendant.
func isAncestor(ctx context.Context, runner GitRunner, workspace, ancestor, descendant string) bool {
	_, err := runner.Run(ctx, workspace, nil, "merge-base", "--is-ancestor", ancestor, descendant)
	return err == nil
}

func refExists(ctx context.Context, runner GitRunner, workspace, ref string) bool {
	_, err := runner.Run(ctx, workspace, nil, "rev-parse", "--verify", "--quiet", ref)
	return err == nil
}

func ToolingBinDir(dataDir string) string {
	return filepath.Join(dataDir, "bin")
}

// GitCredentialHelperPath is where ConfigureWorkerGit writes the repo-local
// credential helper that brokers fresh scoped GitHub tokens. Exported so extra
// dev-kit repositories can reuse the same session helper.
func GitCredentialHelperPath(dataDir string) string {
	return filepath.Join(dataDir, "git-credential-ao")
}

// CloneExtraRepo clones an additional dev-kit repository beside the primary
// checkout using the SAME askpass mechanism as the primary (the token lives only
// in the clone command's environment - never in the URL, the process argv, or
// the repository's .git/config, so the coding agent cannot read it back). It
// then points the repo at the session credential helper (already written by
// ConfigureWorkerGit for the primary checkout) so the agent's own git
// fetch/push in the extra repo keeps working after the short-lived clone token
// expires. cloneURL must be an uncredentialed GitHub URL.
func CloneExtraRepo(
	ctx context.Context,
	runner GitRunner,
	parentDir, dest, cloneURL, branch, token, dataDir string,
) error {
	if runner == nil {
		return errors.New("git runner is required")
	}
	args := []string{"clone", "--origin", "origin", "--no-tags"}
	if strings.TrimSpace(branch) != "" {
		args = append(args, "--branch", branch)
	}
	args = append(args, "--", cloneURL, dest)
	if err := withGitCredential(token, func(env map[string]string) error {
		_, err := runner.Run(ctx, parentDir, env, args...)
		return err
	}); err != nil {
		return err
	}
	helperPath := GitCredentialHelperPath(dataDir)
	if _, statErr := os.Stat(helperPath); statErr != nil {
		// No session helper (e.g. scratch/no-primary paths). The clone succeeded
		// with an uncredentialed origin; agent network ops will simply prompt-fail
		// rather than leak a token. Leave the repo as-is.
		return nil
	}
	for _, command := range [][]string{
		{"config", "--local", "--replace-all", "credential.helper", ""},
		{"config", "--local", "--add", "credential.helper", helperPath},
		{"config", "--local", "--replace-all", "credential.useHttpPath", "true"},
		{"config", "--local", "--replace-all", "user.name", cloudGitAuthorName},
		{"config", "--local", "--replace-all", "user.email", cloudGitAuthorEmail},
	} {
		if _, err := runner.Run(ctx, dest, nil, command...); err != nil {
			return fmt.Errorf("configure extra repo git: %w", err)
		}
	}
	return nil
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

// PushBranch pushes the current HEAD to a remote branch using a fresh
// write-scoped grant. It never force-pushes: git push refuses non-fast-
// forward updates by default, so a branch that already has commits this
// workspace doesn't have fails loudly instead of silently discarding them.
func PushBranch(
	ctx context.Context,
	runner GitRunner,
	workspace, branch string,
	grant CheckoutGrantResponse,
) error {
	if runner == nil {
		return errors.New("git runner is required")
	}
	workspace = filepath.Clean(strings.TrimSpace(workspace))
	if !filepath.IsAbs(workspace) || workspace == string(filepath.Separator) {
		return errors.New("workspace path must be an absolute non-root directory")
	}
	branch = strings.TrimSpace(branch)
	if branch == "" {
		return errors.New("branch name is required")
	}
	if grant.Token != "" && !grant.ExpiresAt.After(time.Now().Add(30*time.Second)) {
		return errors.New("push grant is expired")
	}
	return withGitCredential(grant.Token, func(env map[string]string) error {
		_, err := runner.Run(ctx, workspace, env,
			"push", "--", "origin", "HEAD:refs/heads/"+branch)
		return err
	})
}

// IsScratchRepositoryURL identifies AO's non-network repository sentinel.
func IsScratchRepositoryURL(raw string) bool {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	return err == nil &&
		parsed.Scheme == "https" &&
		strings.EqualFold(parsed.Hostname(), scratchRepositoryHost) &&
		parsed.Port() == "" &&
		parsed.User == nil &&
		parsed.RawQuery == "" &&
		parsed.Fragment == "" &&
		strings.Trim(parsed.Path, "/") != ""
}

// PrepareScratchWorkspace initializes an empty persistent workspace as a Git
// repository. Existing scratch repositories survive worker replacements.
func PrepareScratchWorkspace(
	ctx context.Context,
	runner GitRunner,
	workspace string,
) error {
	if runner == nil {
		return errors.New("git runner is required")
	}
	workspace = filepath.Clean(strings.TrimSpace(workspace))
	if !filepath.IsAbs(workspace) || workspace == string(filepath.Separator) {
		return errors.New("workspace path must be an absolute non-root directory")
	}
	info, err := os.Stat(workspace)
	if err == nil {
		if !info.IsDir() {
			return errors.New("workspace path is not a directory")
		}
		entries, readErr := os.ReadDir(workspace)
		if readErr != nil {
			return fmt.Errorf("inspect workspace contents: %w", readErr)
		}
		if len(entries) > 0 {
			if gitInfo, statErr := os.Stat(filepath.Join(workspace, ".git")); statErr != nil ||
				!gitInfo.IsDir() {
				return errors.New("existing scratch workspace is not a Git repository")
			}
			if _, runErr := runner.Run(
				ctx, workspace, nil, "rev-parse", "--is-inside-work-tree",
			); runErr != nil {
				return fmt.Errorf("validate scratch workspace: %w", runErr)
			}
			return nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect workspace: %w", err)
	}
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		return fmt.Errorf("create scratch workspace: %w", err)
	}
	if _, err := runner.Run(
		ctx, workspace, nil, "init", "--initial-branch", "main",
	); err != nil {
		return fmt.Errorf("initialize scratch workspace: %w", err)
	}
	return nil
}

func validateOrigin(ctx context.Context, runner GitRunner, workspace, expected string) error {
	if info, err := os.Stat(filepath.Join(workspace, ".git")); err != nil || !info.IsDir() {
		return errors.New("existing workspace is not a Git repository")
	}
	output, err := runner.Run(ctx, workspace, nil, "remote", "get-url", "origin")
	if err != nil {
		return fmt.Errorf("read workspace origin: %w", err)
	}
	actual, err := githubRepositoryIdentity(strings.TrimSpace(output))
	if err != nil || actual != expected {
		return errors.New("workspace origin does not match the authorized repository")
	}
	return nil
}

func withGitCredential(token string, operation func(map[string]string) error) error {
	if token == "" {
		return operation(map[string]string{"GIT_TERMINAL_PROMPT": "0"})
	}
	return withAskpass(token, operation)
}

func withAskpass(token string, operation func(map[string]string) error) error {
	dir, err := os.MkdirTemp("", "ao-git-askpass-")
	if err != nil {
		return fmt.Errorf("create askpass directory: %w", err)
	}
	defer os.RemoveAll(dir)
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("secure askpass directory: %w", err)
	}
	path := filepath.Join(dir, "askpass")
	script := "#!/bin/sh\ncase \"$1\" in\n*Username*) printf '%s\\n' x-access-token;;\n*Password*) printf '%s\\n' \"$AO_GIT_TOKEN\";;\n*) exit 1;;\nesac\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		return fmt.Errorf("write askpass helper: %w", err)
	}
	return operation(map[string]string{
		"GIT_ASKPASS": path, "GIT_ASKPASS_REQUIRE": "force",
		"GIT_TERMINAL_PROMPT": "0", "AO_GIT_TOKEN": token,
	})
}

func githubRepositoryIdentity(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	var path string
	if strings.HasPrefix(raw, "git@github.com:") {
		path = strings.TrimPrefix(raw, "git@github.com:")
	} else {
		parsed, err := url.Parse(raw)
		if err != nil || parsed.Scheme != "https" ||
			!strings.EqualFold(parsed.Hostname(), "github.com") ||
			parsed.Port() != "" || parsed.User != nil ||
			parsed.RawQuery != "" || parsed.Fragment != "" {
			return "", errors.New("repository URL is not an uncredentialed GitHub URL")
		}
		path = strings.TrimPrefix(parsed.Path, "/")
	}
	parts := strings.Split(strings.TrimSuffix(path, ".git"), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" ||
		parts[0] == "." || parts[0] == ".." || parts[1] == "." || parts[1] == ".." {
		return "", errors.New("repository URL does not identify one GitHub repository")
	}
	return strings.ToLower(parts[0] + "/" + parts[1]), nil
}

func replaceEnvironment(base []string, overrides map[string]string) []string {
	if len(overrides) == 0 {
		return base
	}
	result := make([]string, 0, len(base)+len(overrides))
	for _, entry := range base {
		key, _, _ := strings.Cut(entry, "=")
		if _, replaced := overrides[key]; !replaced {
			result = append(result, entry)
		}
	}
	for key, value := range overrides {
		result = append(result, key+"="+value)
	}
	return result
}
