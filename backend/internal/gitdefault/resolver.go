// Package gitdefault resolves the branch a repository advertises as its
// default without consulting the currently checked-out branch.
package gitdefault

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	aoprocess "github.com/aoagents/agent-orchestrator/backend/internal/process"
)

const (
	defaultGitBinary             = "git"
	legacyDefaultBranch          = "main"
	legacyInitialCommitSubject   = "initial commit"
	legacyWorkspaceCommitSubject = "chore: initialize AO workspace root"
	// ManagedDefaultConfigKey records the branch AO selected when it initialized
	// a repository itself. It is intentionally repo-local and is only consulted
	// when the repository has no remotes.
	ManagedDefaultConfigKey = "ao.defaultBranch"
)

// ErrUnresolved means Git exposes no authoritative default branch. Callers
// should ask the user to configure one rather than guessing from HEAD.
var ErrUnresolved = errors.New("git default branch is unresolved")

// Source describes the repository-level signal used for a resolution.
type Source string

const (
	// SourceLiveRemoteHead means the branch came from a live symbolic remote HEAD.
	SourceLiveRemoteHead Source = "live_remote_head"
	// SourceCachedRemoteHead means the branch came from the selected remote's cached HEAD.
	SourceCachedRemoteHead Source = "cached_remote_head"
	// SourceAOInitialized means AO recorded the branch when it initialized the repository.
	SourceAOInitialized Source = "ao_initialized"
)

// Resolution is an authoritative branch plus the ref that can safely seed a
// new worktree. Remote is empty only for repositories initialized by AO.
type Resolution struct {
	Branch string
	Remote string
	Ref    string
	Source Source
}

// Runner executes Git. Supplying one keeps the resolver testable at the
// workspace boundary; nil uses a non-interactive OS command runner.
type Runner func(ctx context.Context, binary string, args ...string) ([]byte, error)

// Resolver owns remote selection, live remote-HEAD lookup, exact fetching, and
// cached fallback. Callers only choose the repository and time budget.
type Resolver struct {
	binary string
	run    Runner
}

// New returns a default-branch resolver. An empty binary uses git from PATH.
func New(binary string, run Runner) *Resolver {
	if strings.TrimSpace(binary) == "" {
		binary = defaultGitBinary
	}
	if run == nil {
		run = runCommand
	}
	return &Resolver{binary: binary, run: run}
}

// Inspect resolves from local, durable repository metadata only. It never
// contacts a remote, so it is suitable for project read models and registration.
func (r *Resolver) Inspect(ctx context.Context, repo string) (Resolution, error) {
	remote, remotes, err := r.selectRemote(ctx, repo)
	if err != nil {
		return Resolution{}, err
	}
	if len(remotes) == 0 {
		return r.resolveAOInitialized(ctx, repo)
	}
	if cached, ok := r.cachedRemoteHead(ctx, repo, remote); ok {
		return cached, nil
	}
	return Resolution{}, unresolvedf(
		"remote %q in repository %q has no cached HEAD", remote, repo,
	)
}

// FindCachedRemoteBranch returns a fetched branch from the repository's
// selected primary remote. It does not require that remote to advertise a
// symbolic HEAD, so callers can resume an explicitly requested remote branch
// even when the repository default itself is unavailable.
func (r *Resolver) FindCachedRemoteBranch(ctx context.Context, repo, branch string) (remote, ref string, ok bool, err error) {
	if err := r.validateBranch(ctx, repo, branch); err != nil {
		return "", "", false, err
	}
	remote, remotes, err := r.selectRemote(ctx, repo)
	if err != nil {
		return "", "", false, err
	}
	if len(remotes) == 0 {
		return "", "", false, nil
	}
	ref = remoteBranchRef(remote, branch)
	if !r.refExists(ctx, repo, ref) {
		return remote, ref, false, nil
	}
	return remote, ref, true, nil
}

// Resolve asks the selected remote for its symbolic HEAD, fetches that exact
// branch, and records the symbolic remote HEAD locally. If the live lookup or
// fetch is unavailable, it falls back to the same remote's cached HEAD. The
// remoteCtx bounds network work independently of local Git operations.
func (r *Resolver) Resolve(ctx, remoteCtx context.Context, repo string) (Resolution, error) {
	remote, remotes, err := r.selectRemote(ctx, repo)
	if err != nil {
		return Resolution{}, err
	}
	if len(remotes) == 0 {
		return r.resolveAOInitialized(ctx, repo)
	}
	if remoteCtx == nil {
		remoteCtx = ctx
	}

	branch, liveErr := r.liveRemoteHead(remoteCtx, repo, remote)
	if liveErr == nil {
		if err := r.validateBranch(ctx, repo, branch); err != nil {
			liveErr = err
		} else {
			ref := remoteBranchRef(remote, branch)
			_, fetchErr := r.run(remoteCtx, r.binary, fetchBranchArgs(repo, remote, branch)...)
			if fetchErr == nil {
				if err := r.setCachedRemoteHead(ctx, repo, remote, branch); err != nil {
					return Resolution{}, err
				}
				return Resolution{Branch: branch, Remote: remote, Ref: ref, Source: SourceLiveRemoteHead}, nil
			}
			liveErr = fmt.Errorf("fetch remote default %q from %q: %w", branch, remote, fetchErr)
			// The live lookup still authoritatively named the branch. If that
			// remote-tracking ref already exists, it is the best offline cache.
			if r.refExists(ctx, repo, ref) {
				if err := r.setCachedRemoteHead(ctx, repo, remote, branch); err != nil {
					return Resolution{}, err
				}
				return Resolution{Branch: branch, Remote: remote, Ref: ref, Source: SourceCachedRemoteHead}, nil
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return Resolution{}, err
	}
	if cached, ok := r.cachedRemoteHead(ctx, repo, remote); ok {
		return cached, nil
	}
	if liveErr == nil {
		liveErr = errors.New("remote did not advertise a symbolic HEAD")
	}
	return Resolution{}, unresolvedf(
		"could not resolve remote %q HEAD for repository %q (%v)", remote, repo, liveErr,
	)
}

func (r *Resolver) selectRemote(ctx context.Context, repo string) (string, []string, error) {
	out, err := r.run(ctx, r.binary, "-C", repo, "remote")
	if err != nil {
		return "", nil, fmt.Errorf("list remotes for repository %q: %w", repo, err)
	}
	remotes := nonEmptyLines(string(out))
	sort.Strings(remotes)
	if len(remotes) == 0 {
		return "", remotes, nil
	}

	if out, err := r.run(ctx, r.binary, "-C", repo, "config", "--get", "checkout.defaultRemote"); err == nil {
		configured := strings.TrimSpace(string(out))
		if configured != "" {
			if contains(remotes, configured) {
				return configured, remotes, nil
			}
			return "", remotes, unresolvedf(
				"repository %q configures checkout.defaultRemote=%q, but available remotes are %s",
				repo, configured, strings.Join(remotes, ", "),
			)
		}
	}
	if contains(remotes, "origin") {
		return "origin", remotes, nil
	}
	if len(remotes) == 1 {
		return remotes[0], remotes, nil
	}
	return "", remotes, unresolvedf(
		"repository %q has multiple remotes (%s) and no primary remote; set checkout.defaultRemote or configure defaultBranch explicitly",
		repo, strings.Join(remotes, ", "),
	)
}

func (r *Resolver) liveRemoteHead(ctx context.Context, repo, remote string) (string, error) {
	out, err := r.run(ctx, r.binary, "-C", repo, "ls-remote", "--symref", "--", remote, "HEAD")
	if err != nil {
		return "", err
	}
	branch, err := parseRemoteHEAD(string(out))
	if err != nil {
		return "", err
	}
	return branch, nil
}

func (r *Resolver) cachedRemoteHead(ctx context.Context, repo, remote string) (Resolution, bool) {
	headRef := remoteHeadRef(remote)
	out, err := r.run(ctx, r.binary, "-C", repo, "symbolic-ref", "--quiet", headRef)
	if err != nil {
		return Resolution{}, false
	}
	target := strings.TrimSpace(string(out))
	prefix := "refs/remotes/" + remote + "/"
	branch := strings.TrimPrefix(target, prefix)
	if branch == target || strings.TrimSpace(branch) == "" {
		return Resolution{}, false
	}
	if err := r.validateBranch(ctx, repo, branch); err != nil || !r.refExists(ctx, repo, target) {
		return Resolution{}, false
	}
	return Resolution{Branch: branch, Remote: remote, Ref: target, Source: SourceCachedRemoteHead}, true
}

func (r *Resolver) resolveAOInitialized(ctx context.Context, repo string) (Resolution, error) {
	out, err := r.run(ctx, r.binary, "-C", repo, "config", "--local", "--get", ManagedDefaultConfigKey)
	branch := strings.TrimSpace(string(out))
	if err != nil || branch == "" {
		var ok bool
		branch, ok = r.legacyAOInitializedBranch(ctx, repo)
		if !ok {
			return Resolution{}, unresolvedf(
				"repository %q has no remote or AO-recorded default", repo,
			)
		}
		if _, err := r.run(ctx, r.binary, "-C", repo, "config", "--local", ManagedDefaultConfigKey, branch); err != nil {
			return Resolution{}, fmt.Errorf("backfill %s for repository %q: %w", ManagedDefaultConfigKey, repo, err)
		}
	}
	if err := r.validateBranch(ctx, repo, branch); err != nil {
		return Resolution{}, unresolvedf("repository %q has invalid %s=%q", repo, ManagedDefaultConfigKey, branch)
	}
	ref := "refs/heads/" + branch
	if !r.refExists(ctx, repo, ref) {
		return Resolution{}, unresolvedf(
			"repository %q records %s=%q, but that local branch does not exist", repo, ManagedDefaultConfigKey, branch,
		)
	}
	return Resolution{Branch: branch, Ref: ref, Source: SourceAOInitialized}, nil
}

// legacyAOInitializedBranch recognizes the two initial commits written by AO
// before ManagedDefaultConfigKey existed. Both legacy creation paths selected
// main explicitly, so this is a compatibility backfill rather than a branch
// guess. User-created remoteless repositories remain unresolved.
func (r *Resolver) legacyAOInitializedBranch(ctx context.Context, repo string) (string, bool) {
	ref := "refs/heads/" + legacyDefaultBranch
	if !r.refExists(ctx, repo, ref) {
		return "", false
	}
	out, err := r.run(
		ctx,
		r.binary,
		"-C", repo,
		"log", "--max-parents=0", "--format=%an%x00%ae%x00%s", ref,
	)
	if err != nil {
		return "", false
	}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Split(line, "\x00")
		if len(fields) != 3 {
			continue
		}
		name := strings.TrimSpace(fields[0])
		email := strings.TrimSpace(fields[1])
		subject := strings.TrimSpace(fields[2])
		if subject == legacyWorkspaceCommitSubject ||
			(subject == legacyInitialCommitSubject && name == "Agent Orchestrator" && email == "ao@example.com") {
			return legacyDefaultBranch, true
		}
	}
	return "", false
}

func (r *Resolver) validateBranch(ctx context.Context, repo, branch string) error {
	if _, err := r.run(ctx, r.binary, "-C", repo, "check-ref-format", "--branch", branch); err != nil {
		return fmt.Errorf("invalid default branch %q: %w", branch, err)
	}
	return nil
}

func (r *Resolver) refExists(ctx context.Context, repo, ref string) bool {
	_, err := r.run(ctx, r.binary, "-C", repo, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
	return err == nil
}

func (r *Resolver) setCachedRemoteHead(ctx context.Context, repo, remote, branch string) error {
	if _, err := r.run(ctx, r.binary, "-C", repo, "symbolic-ref", remoteHeadRef(remote), remoteBranchRef(remote, branch)); err != nil {
		return fmt.Errorf("cache remote %q HEAD for repository %q: %w", remote, repo, err)
	}
	return nil
}

func parseRemoteHEAD(output string) (string, error) {
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 3 && fields[0] == "ref:" && fields[2] == "HEAD" {
			branch := strings.TrimPrefix(fields[1], "refs/heads/")
			if branch != fields[1] && branch != "" {
				return branch, nil
			}
		}
	}
	return "", errors.New("remote did not advertise a symbolic HEAD")
}

func fetchBranchArgs(repo, remote, branch string) []string {
	refspec := "+refs/heads/" + branch + ":" + remoteBranchRef(remote, branch)
	return []string{"-C", repo, "fetch", "--no-tags", "--no-recurse-submodules", "--", remote, refspec}
}

func remoteHeadRef(remote string) string {
	return "refs/remotes/" + remote + "/HEAD"
}

func remoteBranchRef(remote, branch string) string {
	return "refs/remotes/" + remote + "/" + branch
}

func nonEmptyLines(value string) []string {
	var out []string
	for _, line := range strings.Split(value, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	return out
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func unresolvedf(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrUnresolved, fmt.Sprintf(format, args...))
}

func runCommand(ctx context.Context, binary string, args ...string) ([]byte, error) {
	cmd := aoprocess.CommandContext(ctx, binary, args...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GCM_INTERACTIVE=Never")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("%s %s: %w: %s", binary, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return out, nil
}
