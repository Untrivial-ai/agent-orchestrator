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
)

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
func PrepareCheckout(
	ctx context.Context,
	runner GitRunner,
	workspace string,
	grant CheckoutGrantResponse,
	defaultBranch string,
) error {
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
	defaultBranch = strings.TrimSpace(defaultBranch)
	if defaultBranch == "" {
		return errors.New("default branch is required")
	}
	info, statErr := os.Stat(workspace)
	if statErr == nil {
		if !info.IsDir() {
			return errors.New("workspace path is not a directory")
		}
		if _, gitErr := os.Stat(filepath.Join(workspace, ".git")); gitErr == nil {
			if err := validateOrigin(ctx, runner, workspace, expected); err != nil {
				return err
			}
			return withGitCredential(grant.Token, func(env map[string]string) error {
				_, err := runner.Run(ctx, workspace, env, "fetch", "--prune", "--", "origin")
				return err
			})
		}
		return cloneIntoWorkspace(ctx, runner, workspace, grant, expected, defaultBranch)
	}
	if !errors.Is(statErr, os.ErrNotExist) {
		return fmt.Errorf("inspect workspace: %w", statErr)
	}
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		return fmt.Errorf("create workspace: %w", err)
	}
	return cloneIntoWorkspace(ctx, runner, workspace, grant, expected, defaultBranch)
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

// cloneIntoWorkspace keeps the stable workspace directory in place while the
// repository is downloaded into a staging directory. Files created by the
// harness during checkout are preserved when the staged checkout is merged.
func cloneIntoWorkspace(
	ctx context.Context,
	runner GitRunner,
	workspace string,
	grant CheckoutGrantResponse,
	expected, defaultBranch string,
) error {
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
	if err := cloneSingleBranch(ctx, runner, staging, clone, grant, defaultBranch); err != nil {
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

func cloneSingleBranch(
	ctx context.Context,
	runner GitRunner,
	directory, destination string,
	grant CheckoutGrantResponse,
	defaultBranch string,
) error {
	clone := func(env map[string]string, branch string) error {
		_, err := runner.Run(ctx, directory, env,
			"clone", "--origin", "origin", "--no-tags", "--single-branch",
			"--filter=blob:none", "--branch", branch, "--", grant.CloneURL, destination)
		return err
	}
	return withGitCredential(grant.Token, func(env map[string]string) error {
		initialErr := clone(env, defaultBranch)
		if initialErr == nil {
			return nil
		}
		output, err := runner.Run(
			ctx, directory, env,
			"ls-remote", "--symref", "--exit-code", grant.CloneURL, "HEAD",
		)
		if err != nil {
			return initialErr
		}
		remoteBranch := parseRemoteDefaultBranch(output)
		if remoteBranch == "" || remoteBranch == defaultBranch {
			return initialErr
		}
		if err := os.RemoveAll(destination); err != nil {
			return fmt.Errorf("clear incomplete checkout: %w", err)
		}
		return clone(env, remoteBranch)
	})
}

func parseRemoteDefaultBranch(output string) string {
	for line := range strings.SplitSeq(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 3 || fields[0] != "ref:" || fields[2] != "HEAD" {
			continue
		}
		const prefix = "refs/heads/"
		if strings.HasPrefix(fields[1], prefix) {
			return strings.TrimPrefix(fields[1], prefix)
		}
	}
	return ""
}

// ConfigureWorkerGit prepares the assigned branch and a repo-local credential
// helper that brokers a fresh scoped token for each GitHub network operation.
// The helper stores only the rotating worker-token path, never a GitHub token.
func ConfigureWorkerGit(
	ctx context.Context,
	runner GitRunner,
	workspace, dataDir, publicURL, sessionID, branch, defaultBranch string,
) error {
	if runner == nil {
		return errors.New("git runner is required")
	}
	for label, value := range map[string]string{
		"workspace": workspace, "data directory": dataDir, "public URL": publicURL,
		"session ID": sessionID, "branch": branch, "default branch": defaultBranch,
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
worker_token="$(tr -d '\r\n' < %s)"
response="$(curl -fsS --connect-timeout 10 --max-time 30 -X POST \
  -H "Authorization: Worker ${worker_token}" \
  -H "X-AO-Session-ID: %s" \
  %s)"
github_token="$(printf '%%s' "$response" | jq -er '.token | select(type == "string" and length > 0)')"
printf 'username=x-access-token\npassword=%%s\n' "$github_token"
`, shellQuote(filepath.Join(dataDir, "worker-token")), sessionID,
		shellQuote(strings.TrimRight(publicURL, "/")+"/api/cloud/v1/worker/github-token"))
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
	checkout := []string{"checkout", "-B", branch}
	if _, err := runner.Run(ctx, workspace, nil, "rev-parse", "--verify", "--quiet", "HEAD^{commit}"); err != nil {
		checkout = append(checkout, "refs/remotes/origin/"+defaultBranch)
	}
	commands := [][]string{
		{"config", "--local", "--replace-all", "credential.helper", ""},
		{"config", "--local", "--add", "credential.helper", helperPath},
		{"config", "--local", "--replace-all", "credential.useHttpPath", "true"},
		{"config", "--local", "--replace-all", "user.name", cloudGitAuthorName},
		{"config", "--local", "--replace-all", "user.email", cloudGitAuthorEmail},
		checkout,
	}
	for _, command := range commands {
		if _, err := runner.Run(ctx, workspace, nil, command...); err != nil {
			return fmt.Errorf("configure worker Git repository: %w", err)
		}
	}
	return nil
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
