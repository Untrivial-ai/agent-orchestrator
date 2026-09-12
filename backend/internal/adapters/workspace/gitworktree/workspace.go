package gitworktree

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/gitdefault"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	aoprocess "github.com/aoagents/agent-orchestrator/backend/internal/process"
)

const (
	defaultGitBinary              = "git"
	defaultBranchResolutionBudget = 5 * time.Second
)

// ErrUnsafePath is returned when a resolved worktree path escapes the managed
// root (path traversal guard).
var (
	ErrUnsafePath = errors.New("gitworktree: unsafe workspace path")
)

// ErrPreservedConflict is an adapter-local alias of ports.ErrPreservedConflict.
// Tests inside this package use this name; callers outside use ports.ErrPreservedConflict
// and errors.Is works because the adapter wraps the ports sentinel.
var ErrPreservedConflict = ports.ErrPreservedConflict

// ErrBranchCheckedOutElsewhere and ErrBranchNotFetched are adapter-local aliases
// of the port-level sentinels: they preserve the gitworktree-prefixed message
// while letting the service layer match on ports.ErrWorkspaceBranchCheckedOutElsewhere
// / ports.ErrWorkspaceBranchNotFetched without importing this package. Tests
// inside the adapter use these names; callers outside use the port sentinels.
var (
	ErrBranchCheckedOutElsewhere = ports.ErrWorkspaceBranchCheckedOutElsewhere
	ErrBranchNotFetched          = ports.ErrWorkspaceBranchNotFetched
	ErrBranchInvalid             = ports.ErrWorkspaceBranchInvalid
	ErrDefaultBranchUnresolved   = ports.ErrWorkspaceDefaultBranchUnresolved
	// ErrWorktreeLocked is an adapter-local alias of ports.ErrWorkspaceLocked,
	// following the same aliasing convention as the branch sentinels above.
	ErrWorktreeLocked = ports.ErrWorkspaceLocked
)

// RepoResolver maps a project to the absolute path of its source git repo.
type RepoResolver interface {
	RepoPath(projectID domain.ProjectID) (string, error)
}

// StaticRepoResolver is a RepoResolver backed by a fixed project→repo-path map.
type StaticRepoResolver map[domain.ProjectID]string

// RepoPath returns the configured repo path for a project, or an error if none
// is configured.
func (r StaticRepoResolver) RepoPath(projectID domain.ProjectID) (string, error) {
	path := r[projectID]
	if path == "" {
		return "", fmt.Errorf("gitworktree: no repo configured for project %q", projectID)
	}
	return path, nil
}

// Options configures a gitworktree Workspace. ManagedRoot and RepoResolver are
// required; Binary falls back to git from PATH.
type Options struct {
	Binary       string
	ManagedRoot  string
	RepoResolver RepoResolver
	// Logger receives the failures background removal cannot return to a
	// caller. Optional; nil silences them.
	Logger *slog.Logger
}

// Workspace creates per-session git worktrees under a managed root. It
// implements ports.Workspace.
type Workspace struct {
	binary      string
	managedRoot string
	repos       RepoResolver
	run         commandRunner
	logger      *slog.Logger
	// discards tracks background worktree removals so tests can wait for them.
	// Production never waits: that deferral is the point (see discard.go).
	discards sync.WaitGroup
}

type commandRunner func(ctx context.Context, binary string, args ...string) ([]byte, error)

var _ ports.Workspace = (*Workspace)(nil)
var _ ports.WorkspaceDefaultBranchRefresher = (*Workspace)(nil)
var _ ports.WorkspaceProject = (*Workspace)(nil)
var _ ports.WorkspaceObserver = (*Workspace)(nil)
var _ ports.WorkspaceReclaimer = (*Workspace)(nil)

// New builds a gitworktree Workspace, validating that ManagedRoot and
// RepoResolver are set and resolving the root to an absolute, symlink-free path.
func New(opts Options) (*Workspace, error) {
	binary := opts.Binary
	if binary == "" {
		binary = defaultGitBinary
	}
	if opts.ManagedRoot == "" {
		return nil, errors.New("gitworktree: ManagedRoot is required")
	}
	if opts.RepoResolver == nil {
		return nil, errors.New("gitworktree: RepoResolver is required")
	}
	root, err := physicalAbs(opts.ManagedRoot)
	if err != nil {
		return nil, fmt.Errorf("gitworktree: managed root: %w", err)
	}
	w := &Workspace{
		binary:      binary,
		managedRoot: filepath.Clean(root),
		repos:       opts.RepoResolver,
		run:         runCommand,
		logger:      opts.Logger,
	}
	// A daemon that died mid-removal leaves directories under the discard root
	// that nothing else will ever look at again. Sweeping at construction is
	// the one moment we know the root is ours.
	w.sweepDiscarded()
	return w, nil
}

// ResolveDefaultBranch selects the authoritative default, refreshing automatic
// remote HEAD metadata when available. A slash is a remote qualifier only when its
// prefix exactly matches a configured remote; otherwise it remains part of an
// origin branch name such as release/2026.
func (w *Workspace) ResolveDefaultBranch(ctx context.Context, repoPath, configuredBranch string) (ports.WorkspaceDefaultBranch, error) {
	return w.resolveDefaultBranch(ctx, repoPath, configuredBranch, false)
}

// ResolveLocalDefaultBranch resolves cached repository metadata for imports
// without contacting a remote. Ordinary spawns retain live HEAD resolution.
func (w *Workspace) ResolveLocalDefaultBranch(ctx context.Context, repoPath, configuredBranch string) (ports.WorkspaceDefaultBranch, error) {
	return w.resolveDefaultBranch(ctx, repoPath, configuredBranch, true)
}

func (w *Workspace) resolveDefaultBranch(ctx context.Context, repoPath, configuredBranch string, localOnly bool) (ports.WorkspaceDefaultBranch, error) {
	repo, err := physicalAbs(repoPath)
	if err != nil {
		return ports.WorkspaceDefaultBranch{}, fmt.Errorf("gitworktree: repo path: %w", err)
	}
	branch := strings.TrimSpace(configuredBranch)
	if branch == "" {
		resolver := gitdefault.New(w.binary, gitdefault.Runner(w.run))
		var resolution gitdefault.Resolution
		if localOnly {
			resolution, err = resolver.Inspect(ctx, repo)
		} else {
			resolution, err = resolver.Resolve(ctx, ctx, repo)
		}
		if err != nil {
			return ports.WorkspaceDefaultBranch{}, fmt.Errorf("gitworktree: resolve repository default for %q: %w", repo, err)
		}
		return ports.WorkspaceDefaultBranch{
			Remote:  resolution.Remote,
			Branch:  resolution.Branch,
			BaseRef: resolution.Ref,
		}, nil
	}
	remote := "origin"
	qualifiedRemote := false
	if candidateRemote, candidateBranch, ok := strings.Cut(branch, "/"); ok && candidateRemote != "" && candidateBranch != "" {
		exists, err := w.remoteExists(ctx, repo, candidateRemote)
		if err != nil {
			return ports.WorkspaceDefaultBranch{}, err
		}
		if exists {
			remote = candidateRemote
			branch = candidateBranch
			qualifiedRemote = true
		}
	}
	if err := w.validateBranch(ctx, repo, branch); err != nil {
		return ports.WorkspaceDefaultBranch{}, err
	}
	remoteRef := "refs/remotes/" + remote + "/" + branch
	if exists, err := w.refExists(ctx, repo, remoteRef); err != nil {
		return ports.WorkspaceDefaultBranch{}, err
	} else if exists {
		return ports.WorkspaceDefaultBranch{
			Remote:  remote,
			Branch:  branch,
			BaseRef: remoteRef,
		}, nil
	}
	if !qualifiedRemote {
		localRef := "refs/heads/" + branch
		if exists, err := w.refExists(ctx, repo, localRef); err != nil {
			return ports.WorkspaceDefaultBranch{}, err
		} else if exists {
			return ports.WorkspaceDefaultBranch{
				Remote:  remote,
				Branch:  branch,
				BaseRef: localRef,
			}, nil
		}
	}
	return ports.WorkspaceDefaultBranch{
		Remote:  remote,
		Branch:  branch,
		BaseRef: remoteRef,
	}, nil
}

// FetchDefaultBranch refreshes the exact target returned by
// ResolveDefaultBranch using an explicit refspec.
func (w *Workspace) FetchDefaultBranch(ctx context.Context, repoPath string, target ports.WorkspaceDefaultBranch) error {
	repo, err := physicalAbs(repoPath)
	if err != nil {
		return fmt.Errorf("gitworktree: repo path: %w", err)
	}
	if strings.TrimSpace(target.Remote) == "" {
		return errors.New("gitworktree: remote is required")
	}
	if strings.TrimSpace(target.Branch) == "" {
		return errors.New("gitworktree: branch is required")
	}
	wantRef := "refs/remotes/" + target.Remote + "/" + target.Branch
	localRef := "refs/heads/" + target.Branch
	if target.BaseRef == localRef {
		return nil
	}
	if target.BaseRef != wantRef {
		return fmt.Errorf("gitworktree: invalid default branch target %q (want %q)", target.BaseRef, wantRef)
	}
	if err := w.validateBranch(ctx, repo, target.Branch); err != nil {
		return err
	}
	if _, err := w.run(ctx, w.binary, fetchBranchArgs(repo, target.Remote, target.Branch)...); err != nil {
		return fmt.Errorf("gitworktree: fetch %s %s: %w", target.Remote, target.Branch, err)
	}
	return nil
}

func (w *Workspace) remoteExists(ctx context.Context, repo, remote string) (bool, error) {
	out, err := w.run(ctx, w.binary, remoteListArgs(repo)...)
	if err != nil {
		return false, fmt.Errorf("gitworktree: list remotes: %w", err)
	}
	for _, candidate := range strings.Fields(string(out)) {
		if candidate == remote {
			return true, nil
		}
	}
	return false, nil
}

// Create adds a git worktree for the session under the managed root, checking
// out the requested branch, and returns where it landed.
func (w *Workspace) Create(ctx context.Context, cfg ports.WorkspaceConfig) (ports.WorkspaceInfo, error) {
	if err := validateConfig(cfg); err != nil {
		return ports.WorkspaceInfo{}, err
	}
	repo, err := w.repoPath(cfg.ProjectID)
	if err != nil {
		return ports.WorkspaceInfo{}, err
	}
	if err := w.validateBranch(ctx, repo, cfg.Branch); err != nil {
		return ports.WorkspaceInfo{}, err
	}
	path, err := w.managedPath(cfg)
	if err != nil {
		return ports.WorkspaceInfo{}, err
	}
	if info, ok, err := w.existingWorktree(ctx, repo, path, cfg); err != nil {
		return ports.WorkspaceInfo{}, err
	} else if ok {
		refs, err := w.resolveWorktreeRefsWithBudget(ctx, repo, cfg.Branch, cfg.BaseBranch)
		if err != nil {
			return ports.WorkspaceInfo{}, err
		}
		info.BaseRef = refs.baseRef
		return info, nil
	}
	baseRef, err := w.addWorktree(ctx, repo, path, cfg.Branch, cfg.BaseBranch, cfg.BaseRef, true)
	if err != nil {
		return ports.WorkspaceInfo{}, err
	}
	return ports.WorkspaceInfo{Path: path, Branch: cfg.Branch, BaseRef: baseRef, SessionID: cfg.SessionID, ProjectID: cfg.ProjectID}, nil
}

// CreateWorkspaceProject materialises a root-as-repo workspace session: the
// parent repo worktree is created at the session root, then each registered
// child repo is created at its relative path inside that root. All repos share
// one branch name; if the requested branch already exists in any repo, one
// suffixed branch that is free in every repo is selected and used everywhere.
func (w *Workspace) CreateWorkspaceProject(ctx context.Context, cfg ports.WorkspaceProjectConfig) (ports.WorkspaceProjectInfo, error) {
	if err := validateWorkspaceProjectConfig(cfg); err != nil {
		return ports.WorkspaceProjectInfo{}, err
	}
	rootRepo, err := physicalAbs(cfg.RootRepoPath)
	if err != nil {
		return ports.WorkspaceProjectInfo{}, fmt.Errorf("gitworktree: root repo path: %w", err)
	}
	rootPath, err := w.managedPath(ports.WorkspaceConfig{
		ProjectID:     cfg.ProjectID,
		SessionID:     cfg.SessionID,
		Kind:          cfg.Kind,
		SessionPrefix: cfg.SessionPrefix,
		Branch:        firstNonEmpty(cfg.Branch, defaultSessionBranchName(cfg.SessionID)),
	})
	if err != nil {
		return ports.WorkspaceProjectInfo{}, err
	}
	repos := make([]workspaceProjectRepo, 0, len(cfg.Repos)+1)
	repos = append(repos, workspaceProjectRepo{
		name:       domain.RootWorkspaceRepoName,
		repoPath:   rootRepo,
		outputPath: rootPath,
		baseBranch: cfg.BaseBranch,
	})
	for _, child := range cfg.Repos {
		repoPath, err := physicalAbs(child.RepoPath)
		if err != nil {
			return ports.WorkspaceProjectInfo{}, fmt.Errorf("gitworktree: child repo %q path: %w", child.Name, err)
		}
		rel, err := cleanRelativePath(child.RelativePath)
		if err != nil {
			return ports.WorkspaceProjectInfo{}, fmt.Errorf("gitworktree: child repo %q: %w", child.Name, err)
		}
		outPath, err := w.validateManagedPath(filepath.Join(rootPath, filepath.FromSlash(rel)))
		if err != nil {
			return ports.WorkspaceProjectInfo{}, fmt.Errorf("gitworktree: child repo %q path: %w", child.Name, err)
		}
		repos = append(repos, workspaceProjectRepo{
			name:         child.Name,
			relativePath: rel,
			repoPath:     repoPath,
			outputPath:   outPath,
			baseBranch:   child.BaseBranch,
			baseRef:      child.BaseRef,
		})
	}
	branch, err := w.workspaceProjectBranch(ctx, repos, firstNonEmpty(cfg.Branch, defaultSessionBranchName(cfg.SessionID)))
	if err != nil {
		return ports.WorkspaceProjectInfo{}, err
	}
	// Resolve every repository base before creating the first worktree. Besides
	// keeping remote probing within one aggregate budget, this prevents a later
	// unresolved child from leaving session branches behind in earlier repos.
	remoteCtx, cancelRemote := context.WithTimeout(ctx, defaultBranchResolutionBudget)
	defer cancelRemote()
	for i := range repos {
		if i == 0 {
			refs, err := w.resolveWorkspaceRootRefs(ctx, repos[i].repoPath, repos[i].baseBranch)
			if err != nil {
				return ports.WorkspaceProjectInfo{}, fmt.Errorf("gitworktree: resolve workspace repo %q base: %w", repos[i].name, err)
			}
			repos[i].seedRef = refs.seedRef
			repos[i].baseRef = refs.baseRef
			continue
		}
		if repos[i].baseRef != "" {
			repos[i].seedRef = repos[i].baseRef
			requestedRef := "origin/" + branch
			if exists, err := w.refExists(ctx, repos[i].repoPath, requestedRef); err != nil {
				return ports.WorkspaceProjectInfo{}, fmt.Errorf("gitworktree: inspect workspace repo %q requested branch: %w", repos[i].name, err)
			} else if exists {
				repos[i].seedRef = requestedRef
			}
			continue
		}
		refs, err := w.resolveWorktreeRefs(ctx, remoteCtx, repos[i].repoPath, branch, repos[i].baseBranch)
		if err != nil {
			return ports.WorkspaceProjectInfo{}, fmt.Errorf("gitworktree: resolve workspace repo %q base: %w", repos[i].name, err)
		}
		repos[i].seedRef = refs.seedRef
		repos[i].baseRef = refs.baseRef
	}
	created := make([]workspaceProjectRepo, 0, len(repos))
	out := ports.WorkspaceProjectInfo{Worktrees: make([]ports.WorkspaceRepoInfo, 0, len(repos))}
	for repoIndex, repo := range repos {
		baseSHA, err := w.createWorkspaceProjectRepo(ctx, repo, branch)
		if err != nil {
			for i := len(created) - 1; i >= 0; i-- {
				_ = w.forceDestroyPath(ctx, created[i].repoPath, created[i].outputPath)
			}
			return ports.WorkspaceProjectInfo{}, err
		}
		created = append(created, repo)
		if repoIndex == 0 {
			if err := copyWorkspaceAssets(rootRepo, rootPath, cfg.Assets); err != nil {
				for i := len(created) - 1; i >= 0; i-- {
					_ = w.forceDestroyPath(ctx, created[i].repoPath, created[i].outputPath)
				}
				return ports.WorkspaceProjectInfo{}, err
			}
		}
		info := ports.WorkspaceRepoInfo{
			RepoName:     repo.name,
			RepoPath:     repo.repoPath,
			Path:         repo.outputPath,
			Branch:       branch,
			BaseSHA:      baseSHA,
			BaseRef:      repo.baseRef,
			SessionID:    cfg.SessionID,
			ProjectID:    cfg.ProjectID,
			RelativePath: repo.relativePath,
		}
		out.Worktrees = append(out.Worktrees, info)
		if repo.name == domain.RootWorkspaceRepoName {
			out.Root = ports.WorkspaceInfo{Path: repo.outputPath, Branch: branch, BaseRef: repo.baseRef, SessionID: cfg.SessionID, ProjectID: cfg.ProjectID}
		}
	}
	return out, nil
}

func (w *Workspace) resolveWorkspaceRootRefs(ctx context.Context, repo, configuredBranch string) (worktreeRefs, error) {
	branch := strings.TrimSpace(configuredBranch)
	if branch == "" {
		if out, err := w.run(ctx, w.binary, "-C", repo, "config", "--local", "--get", gitdefault.ManagedDefaultConfigKey); err == nil {
			branch = strings.TrimSpace(string(out))
		}
	}
	if branch == "" {
		return w.repairWorkspaceRootDefault(ctx, repo)
	}
	return w.resolveWorktreeRefsFromDefault(ctx, repo, "", branch)
}

func (w *Workspace) repairWorkspaceRootDefault(ctx context.Context, repo string) (worktreeRefs, error) {
	out, err := w.run(ctx, w.binary, "-C", repo, "symbolic-ref", "--short", "HEAD")
	if err != nil {
		return worktreeRefs{}, err
	}
	branch := strings.TrimSpace(string(out))
	if err := w.validateBranch(ctx, repo, branch); err != nil {
		return worktreeRefs{}, err
	}
	ref := "refs/heads/" + branch
	exists, err := w.refExists(ctx, repo, ref)
	if err != nil {
		return worktreeRefs{}, err
	}
	if !exists {
		return worktreeRefs{}, fmt.Errorf("workspace root local branch %q has no commit", branch)
	}
	if _, err := w.run(ctx, w.binary, "-C", repo, "config", "--local", gitdefault.ManagedDefaultConfigKey, branch); err != nil {
		return worktreeRefs{}, err
	}
	return worktreeRefs{seedRef: ref, baseRef: ref}, nil
}

func copyWorkspaceAssets(sourceRoot, destinationRoot string, assets []ports.WorkspaceProjectAssetConfig) error {
	for _, asset := range assets {
		rel, err := cleanRelativePath(asset.RelativePath)
		if err != nil || hasWorkspaceManagedComponent(rel) {
			return fmt.Errorf("gitworktree: workspace asset %q: %w", asset.RelativePath, ErrUnsafePath)
		}
		source, err := physicalAbs(asset.SourcePath)
		if err != nil || !assetPathWithin(sourceRoot, source) {
			return fmt.Errorf("gitworktree: workspace asset %q source: %w", asset.RelativePath, ErrUnsafePath)
		}
		destination := filepath.Join(destinationRoot, filepath.FromSlash(rel))
		if !assetPathWithin(destinationRoot, destination) {
			return fmt.Errorf("gitworktree: workspace asset %q destination: %w", asset.RelativePath, ErrUnsafePath)
		}
		if err := copyWorkspaceAssetTree(sourceRoot, source, destination); err != nil {
			return fmt.Errorf("gitworktree: copy workspace asset %q: %w", asset.RelativePath, err)
		}
	}
	return nil
}

func copyWorkspaceAssetTree(sourceRoot, source, destination string) error {
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(source)
		if err != nil {
			return err
		}
		if filepath.IsAbs(target) {
			return fmt.Errorf("absolute symlink %q: %w", source, ErrUnsafePath)
		}
		resolved := filepath.Clean(filepath.Join(filepath.Dir(source), target))
		if !assetPathWithin(sourceRoot, resolved) || hasWorkspaceManagedComponent(relativeOrEmpty(sourceRoot, resolved)) {
			return fmt.Errorf("symlink %q escapes or targets managed data: %w", source, ErrUnsafePath)
		}
		physicalTarget, err := filepath.EvalSymlinks(resolved)
		if err != nil || !assetPathWithin(sourceRoot, physicalTarget) {
			return fmt.Errorf("symlink %q has an unsafe or unresolved target: %w", source, ErrUnsafePath)
		}
		if existing, err := os.Lstat(destination); err == nil {
			if existing.IsDir() {
				return fmt.Errorf("cannot overlay symlink on directory %q", destination)
			}
			if err := os.Remove(destination); err != nil {
				return err
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return os.Symlink(target, destination)
	}
	if info.IsDir() {
		if existing, err := os.Lstat(destination); err == nil {
			if !existing.IsDir() || existing.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("cannot overlay directory on non-directory %q", destination)
			}
		} else if errors.Is(err, os.ErrNotExist) {
			if err := os.Mkdir(destination, info.Mode().Perm()); err != nil {
				return err
			}
		} else {
			return err
		}
		entries, err := os.ReadDir(source)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if entry.Name() == ".git" || entry.Name() == ".ao" {
				continue
			}
			if err := copyWorkspaceAssetTree(sourceRoot, filepath.Join(source, entry.Name()), filepath.Join(destination, entry.Name())); err != nil {
				return err
			}
		}
		return nil
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("unsupported file type at %q", source)
	}
	if existing, err := os.Lstat(destination); err == nil {
		if !existing.Mode().IsRegular() {
			return fmt.Errorf("cannot overlay file on non-regular path %q", destination)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return err
	}
	if err := out.Chmod(info.Mode().Perm()); err != nil {
		_ = out.Close()
		return err
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

func assetPathWithin(root, path string) bool {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func relativeOrEmpty(root, path string) string {
	rel, _ := filepath.Rel(root, path)
	return rel
}

func hasWorkspaceManagedComponent(path string) bool {
	for _, component := range strings.Split(filepath.ToSlash(path), "/") {
		if component == ".git" || component == ".ao" {
			return true
		}
	}
	return false
}

// DestroyWorkspaceProject removes every worktree in a workspace project,
// children first and the parent/root last. It uses the same force path as spawn
// rollback because normal interactive cleanup still goes through Destroy and
// the full dirty-preserve matrix is implemented separately.
func (w *Workspace) DestroyWorkspaceProject(ctx context.Context, info ports.WorkspaceProjectInfo) error {
	var firstErr error
	for i := len(info.Worktrees) - 1; i >= 0; i-- {
		wt := info.Worktrees[i]
		if wt.Path == "" {
			continue
		}
		repoPath := wt.RepoPath
		if repoPath == "" {
			if firstErr == nil {
				firstErr = fmt.Errorf("gitworktree: missing repo path for worktree %q", wt.Path)
			}
			continue
		}
		if err := w.forceDestroyPath(ctx, repoPath, wt.Path); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// Destroy removes the session's worktree and prunes it from the repo, refusing
// (rather than force-deleting) if git still has the path registered afterwards.
func (w *Workspace) Destroy(ctx context.Context, info ports.WorkspaceInfo) error {
	_, err := w.destroy(ctx, info)
	return err
}

// DestroyReclaim is Destroy plus the reclaim outcome. A worktree directory that
// was already gone before this call leaves nothing for `git worktree remove` to
// operate on: the prune below still clears the stale registration and the final
// os.RemoveAll is a no-op on a missing path, so the whole teardown reports
// success without any disk having been released. Callers that count reclaimed
// workspaces need that told apart from a real removal.
func (w *Workspace) DestroyReclaim(ctx context.Context, info ports.WorkspaceInfo) (ports.WorkspaceReclaim, error) {
	return w.destroy(ctx, info)
}

func (w *Workspace) destroy(ctx context.Context, info ports.WorkspaceInfo) (ports.WorkspaceReclaim, error) {
	if info.Path == "" {
		return ports.WorkspaceReclaimAlreadyAbsent, fmt.Errorf("%w: empty path", ErrUnsafePath)
	}
	repo, err := w.repoPathForInfo(info)
	if err != nil {
		return ports.WorkspaceReclaimAlreadyAbsent, err
	}
	path, err := w.validateManagedPath(info.Path)
	if err != nil {
		return ports.WorkspaceReclaimAlreadyAbsent, err
	}
	// Sampled before any teardown step runs, so it reflects the state this call
	// found rather than the state it left behind. Only a definite absence counts
	// as already-absent; a stat that fails for any other reason (permissions, a
	// racing writer) stays on the removed path so an unknown is never reported
	// as "nothing to do".
	reclaim := ports.WorkspaceReclaimRemoved
	if _, statErr := os.Lstat(path); errors.Is(statErr, os.ErrNotExist) {
		reclaim = ports.WorkspaceReclaimAlreadyAbsent
	}
	if err := w.requireReachableRepo(repo); err != nil {
		return reclaim, err
	}
	// Move the directory aside rather than waiting out `git worktree remove`'s
	// walk of an ignored-file mountain; falls through to the git-driven path
	// below whenever the move is not clearly safe.
	if handled, err := w.discardWorktree(ctx, repo, path); handled {
		return reclaim, err
	}
	_, removeErr := w.run(ctx, w.binary, worktreeRemoveArgs(repo, path)...)
	if _, err := w.run(ctx, w.binary, worktreePruneArgs(repo)...); err != nil {
		return reclaim, fmt.Errorf("gitworktree: worktree prune: %w", err)
	}
	records, err := w.listRecords(ctx, repo)
	if err != nil {
		return reclaim, err
	}
	if _, ok := findWorktree(records, path); ok {
		if removeErr != nil {
			if isLockedWorktreeRemoveError(removeErr) {
				return reclaim, fmt.Errorf("gitworktree: refusing to remove %q: path is still registered after git worktree prune (worktree remove: %w)", path, removeErr)
			}
			// Distinguish the dirty-worktree refusal (uncommitted agent work)
			// from other registration leftovers (e.g. a locked worktree) so the
			// Session Manager can preserve the workspace without erroring.
			dirty, statusErr := w.isDirty(ctx, path)
			if statusErr == nil && dirty {
				return reclaim, fmt.Errorf("gitworktree: refusing to remove %q: %w (worktree remove: %w)", path, ports.ErrWorkspaceDirty, removeErr)
			}
			if statusErr != nil {
				// A failed probe must stay visible: without it the caller can't
				// tell "not dirty" from "couldn't check".
				return reclaim, fmt.Errorf("gitworktree: refusing to remove %q: path is still registered after git worktree prune (worktree remove: %w; dirty probe: %w)", path, removeErr, statusErr)
			}
			return reclaim, fmt.Errorf("gitworktree: refusing to remove %q: path is still registered after git worktree prune (worktree remove: %w)", path, removeErr)
		}
		return reclaim, fmt.Errorf("gitworktree: refusing to remove %q: path is still registered after git worktree prune", path)
	}
	if err := removeAllWithRetry(ctx, path); err != nil {
		return reclaim, fmt.Errorf("gitworktree: remove unregistered path %q: %w", path, err)
	}
	return reclaim, nil
}

// ForceDestroy removes the session's worktree unconditionally (--force), prunes
// it from git's worktree list, and falls back to os.RemoveAll if any filesystem
// residue remains.
//
// ponytail: only safe to call AFTER the session's uncommitted work has been
// captured via StashUncommitted. Calling it before capture silently
// discards agent work. For interactive teardown (ao session kill, ao cleanup)
// use Destroy, which refuses dirty worktrees via ErrWorkspaceDirty.
func (w *Workspace) ForceDestroy(ctx context.Context, info ports.WorkspaceInfo) error {
	if info.Path == "" {
		return fmt.Errorf("%w: empty path", ErrUnsafePath)
	}
	repo, err := w.repoPathForInfo(info)
	if err != nil {
		return err
	}
	path, err := w.validateManagedPath(info.Path)
	if err != nil {
		return err
	}
	if err := w.requireReachableRepo(repo); err != nil {
		return err
	}
	// Force teardown has no refusal to honour, so the move is unconditional:
	// rename the directory out of the way, drop the registration, unlink in the
	// background. This runs on daemon shutdown and orchestrator replacement,
	// which stalled on the same unlink that used to stall a kill.
	if discarded, moved := w.discard(path); moved {
		// A failed prune must leave both halves intact. Deleting anyway would
		// report failure while destroying the directory and stranding its
		// registration, and that dangling entry blocks the path from being
		// used again; restoring lets the caller retry against the state it
		// started from.
		if _, err := w.run(ctx, w.binary, worktreePruneArgs(repo)...); err != nil {
			w.undiscard(discarded, path)
			return fmt.Errorf("gitworktree: worktree prune: %w", err)
		}
		w.removeInBackground(discarded)
		return nil
	}
	// --force bypasses git's dirty check; errors here are advisory (the path may
	// already be gone). We proceed to prune regardless.
	_, _ = w.run(ctx, w.binary, worktreeForceRemoveArgs(repo, path)...)
	if _, err := w.run(ctx, w.binary, worktreePruneArgs(repo)...); err != nil {
		return fmt.Errorf("gitworktree: worktree prune: %w", err)
	}
	// os.RemoveAll as a backstop: cleans up filesystem residue left behind if
	// git worktree remove --force still left the directory (e.g. files outside
	// git tracking).
	if err := removeAllWithRetry(ctx, path); err != nil {
		return fmt.Errorf("gitworktree: force remove path %q: %w", path, err)
	}
	return nil
}

// StashUncommitted captures all uncommitted work in the session's worktree
// into a git commit object WITHOUT mutating the working tree or the global
// stash stack. The commit is stored at refs/ao/preserved/<session-id>.
//
// It builds the preserve commit through a temporary index file so tracked
// edits AND new non-ignored files are captured while .gitignore-d files are
// silently skipped (honoured because we never pass -f/--force to git-add).
//
// Returns the full ref name (e.g. "refs/ao/preserved/sess-1"). Returns an
// empty string (and no error) if the worktree is clean.
func (w *Workspace) StashUncommitted(ctx context.Context, info ports.WorkspaceInfo) (string, error) {
	if info.Path == "" {
		return "", fmt.Errorf("%w: empty path", ErrUnsafePath)
	}
	if info.SessionID == "" {
		return "", errors.New("gitworktree: session id is required for StashUncommitted")
	}
	repo, err := w.repoPathForInfo(info)
	if err != nil {
		return "", err
	}
	path, err := w.validateManagedPath(info.Path)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("gitworktree: stale worktree %q: %w", path, ports.ErrWorkspaceStale)
		}
		return "", fmt.Errorf("gitworktree: stat worktree %q: %w", path, err)
	}
	records, err := w.listRecords(ctx, repo)
	if err != nil {
		return "", err
	}
	if _, ok := findWorktree(records, path); !ok {
		return "", fmt.Errorf("gitworktree: worktree %q is not registered: %w", path, ports.ErrWorkspaceStale)
	}

	// Early exit for clean worktrees: nothing to preserve.
	dirty, err := w.isDirty(ctx, path)
	if err != nil {
		if isNotGitRepositoryError(err) {
			return "", fmt.Errorf("gitworktree: stale worktree %q: %w", path, ports.ErrWorkspaceStale)
		}
		return "", fmt.Errorf("gitworktree: StashUncommitted dirty check: %w", err)
	}
	if !dirty {
		return "", nil
	}

	// Log the count of ignored paths that will be skipped.
	if skipCount, err := w.countIgnoredPaths(ctx, path); err == nil {
		slog.InfoContext(ctx, "gitworktree: StashUncommitted skipping ignored paths",
			"session", string(info.SessionID),
			"skipped_count", skipCount,
		)
	}

	// Reserve a unique path for the temp index in the system temp dir (not ~/.ao).
	// We must NOT pre-create the file: git requires GIT_INDEX_FILE to either not
	// exist (it creates it) or be a valid git index. os.CreateTemp gives us a
	// unique name; we close and remove it immediately so git gets an absent path.
	tmpIdx, err := os.CreateTemp("", "ao-preserve-idx-*")
	if err != nil {
		return "", fmt.Errorf("gitworktree: reserve temp index path: %w", err)
	}
	tmpIdxPath := tmpIdx.Name()
	_ = tmpIdx.Close()
	// Remove now so git sees an absent path (not a 0-byte corrupt index).
	_ = os.Remove(tmpIdxPath)
	// Deferred remove is a best-effort cleanup in case git leaves the file.
	defer func() { _ = os.Remove(tmpIdxPath) }()

	// Stage all tracked and non-ignored untracked files into the temp index.
	// GIT_INDEX_FILE overrides the index so the real index is never touched.
	addCmd := aoprocess.CommandContext(ctx, w.binary, addAllTempIndexArgs(path)...)
	addCmd.Env = append(os.Environ(), "GIT_INDEX_FILE="+tmpIdxPath)
	if out, err := addCmd.CombinedOutput(); err != nil {
		return "", commandError{args: append([]string{w.binary}, addAllTempIndexArgs(path)...), output: string(out), err: err}
	}

	// Write the staged tree to get a tree SHA.
	writeTreeCmd := aoprocess.CommandContext(ctx, w.binary, writeTreeArgs(path)...)
	writeTreeCmd.Env = append(os.Environ(), "GIT_INDEX_FILE="+tmpIdxPath)
	treeOut, err := writeTreeCmd.CombinedOutput()
	if err != nil {
		return "", commandError{args: append([]string{w.binary}, writeTreeArgs(path)...), output: string(treeOut), err: err}
	}
	treeSHA := strings.TrimSpace(string(treeOut))

	// Resolve HEAD. An unborn HEAD (no commits yet) means we omit the -p flag
	// from commit-tree so the preserve commit has no parent.
	headOut, headErr := w.run(ctx, w.binary, revParseHeadArgs(path)...)
	headSHA := ""
	if headErr == nil {
		headSHA = strings.TrimSpace(string(headOut))
	}
	// headErr != nil means unborn HEAD: headSHA stays empty, commit-tree gets no -p.

	// If the preserve tree SHA equals HEAD's tree SHA the working tree is
	// effectively clean from git's perspective (only ignored files differ).
	if headSHA != "" {
		headTreeOut, err := w.run(ctx, w.binary, "-C", path, "rev-parse", headSHA+"^{tree}")
		if err == nil {
			headTreeSHA := strings.TrimSpace(string(headTreeOut))
			if headTreeSHA == treeSHA {
				// Nothing to preserve beyond ignored files.
				return "", nil
			}
		}
	}

	// Create a commit object that wraps the preserve tree.
	msg := "ao preserved " + string(info.SessionID)
	commitOut, err := w.run(ctx, w.binary, commitTreeArgs(path, treeSHA, headSHA, msg)...)
	if err != nil {
		return "", fmt.Errorf("gitworktree: commit-tree: %w", err)
	}
	commitSHA := strings.TrimSpace(string(commitOut))

	// Point the preserve ref at the commit.
	ref := "refs/ao/preserved/" + string(info.SessionID)
	if _, err := w.run(ctx, w.binary, updateRefArgs(path, ref, commitSHA)...); err != nil {
		return "", fmt.Errorf("gitworktree: update-ref %q: %w", ref, err)
	}
	return ref, nil
}

func isNotGitRepositoryError(err error) bool {
	return strings.Contains(err.Error(), "not a git repository")
}

// countIgnoredPaths returns the number of entries listed by
// "git status --ignored --porcelain" that start with "!!" (ignored).
func (w *Workspace) countIgnoredPaths(ctx context.Context, worktree string) (int, error) {
	out, err := w.run(ctx, w.binary, ignoredCountArgs(worktree)...)
	if err != nil {
		return 0, fmt.Errorf("gitworktree: count ignored: %w", err)
	}
	count := 0
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "!! ") {
			count++
		}
	}
	return count, nil
}

// ApplyPreserved replays the capture created by StashUncommitted onto the
// (freshly re-added) worktree using a true three-way merge (cherry-pick --no-commit).
// On clean success, the preserve ref is deleted.
// On conflict, the ref is kept, conflict markers are left in the affected files,
// and ErrPreservedConflict (wrapped) is returned so the caller can surface it.
//
// NEVER deletes the preserve ref on a failed or conflicted apply.
func (w *Workspace) ApplyPreserved(ctx context.Context, info ports.WorkspaceInfo, ref string) error {
	if info.Path == "" {
		return fmt.Errorf("%w: empty path", ErrUnsafePath)
	}
	if ref == "" {
		return errors.New("gitworktree: ApplyPreserved: ref must not be empty")
	}

	// Resolve the ref to its commit SHA.
	resolveOut, err := w.run(ctx, w.binary, revParseVerifyArgs(info.Path, ref)...)
	if err != nil {
		return fmt.Errorf("gitworktree: ApplyPreserved resolve ref %q: %w", ref, err)
	}
	commitSHA := strings.TrimSpace(string(resolveOut))

	// Apply the preserve commit via "git cherry-pick --no-commit <sha>".
	// cherry-pick computes the diff between the preserve commit and its parent
	// (the HEAD at save time) and 3-way-merges it onto the current working tree.
	// On conflict it leaves textual conflict markers in the affected files and
	// exits non-zero WITHOUT committing or moving HEAD. Conflict detection uses
	// the exit code only (not output text) to stay locale-independent.
	applyErr := w.runCherryPickNoCommit(ctx, info.Path, commitSHA)
	if applyErr != nil {
		// Any non-zero exit from the merge step is a conflict: keep the ref,
		// leave conflict markers in place, and surface the sentinel.
		return fmt.Errorf("%w: %w", ErrPreservedConflict, applyErr)
	}

	// Clean apply: remove the preserve ref so it is never replayed twice.
	if _, err := w.run(ctx, w.binary, deleteRefArgs(info.Path, ref)...); err != nil {
		// Log but do not fail: the work is already applied. A dangling preserve
		// ref is harmless; the next StashUncommitted will overwrite it.
		slog.WarnContext(ctx, "gitworktree: ApplyPreserved could not delete preserve ref",
			"ref", ref,
			"err", err,
		)
	}
	return nil
}

// AddExclude appends git ignore patterns to the worktree's local info/exclude so
// daemon-generated files never surface as untracked changes. The exclude file is
// resolved via `git rev-parse --git-common-dir`, not `--git-dir`: git reads
// info/exclude from $GIT_COMMON_DIR, and this adapter only ever creates linked
// worktrees, where --git-dir points into .git/worktrees/<name> (per-worktree)
// while --git-common-dir points at the shared main .git. Writing under --git-dir
// would land info/exclude in a directory git never consults, making the exclude a
// no-op. Idempotent: patterns already present are skipped.
func (w *Workspace) AddExclude(ctx context.Context, info ports.WorkspaceInfo, patterns ...string) error {
	if len(patterns) == 0 {
		return nil
	}
	path, err := w.validateManagedPath(info.Path)
	if err != nil {
		return err
	}
	out, err := w.run(ctx, w.binary, "-C", path, "rev-parse", "--git-common-dir")
	if err != nil {
		return fmt.Errorf("gitworktree: AddExclude resolve git common dir: %w", err)
	}
	gitDir := strings.TrimSpace(string(out))
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(path, gitDir)
	}
	infoDir := filepath.Join(gitDir, "info")
	if err := os.MkdirAll(infoDir, 0o750); err != nil {
		return fmt.Errorf("gitworktree: AddExclude create info dir: %w", err)
	}
	excludePath := filepath.Join(infoDir, "exclude")
	existing, _ := os.ReadFile(excludePath)
	var toAdd []string
	for _, p := range patterns {
		if !strings.Contains(string(existing), p) {
			toAdd = append(toAdd, p)
		}
	}
	if len(toAdd) == 0 {
		return nil
	}
	f, err := os.OpenFile(excludePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("gitworktree: AddExclude open exclude: %w", err)
	}
	defer func() { _ = f.Close() }()
	prefix := ""
	if len(existing) > 0 && !strings.HasSuffix(string(existing), "\n") {
		prefix = "\n"
	}
	if _, err := f.WriteString(prefix + strings.Join(toAdd, "\n") + "\n"); err != nil {
		return fmt.Errorf("gitworktree: AddExclude write exclude: %w", err)
	}
	return nil
}

const (
	maxObservedWorkspaceChanges = 500
	maxObservedWorkspaceCommits = 20
)

// ObserveWorkspace returns a bounded, read-only Git snapshot for handoff and
// recovery. It deliberately lives in the workspace adapter so the session
// manager does not grow an out-of-band dependency on the git executable.
func (w *Workspace) ObserveWorkspace(ctx context.Context, info ports.WorkspaceInfo) (ports.WorkspaceObservation, error) {
	path := strings.TrimSpace(info.Path)
	if path == "" {
		return ports.WorkspaceObservation{}, errors.New("gitworktree: observe workspace path is required")
	}
	validated, err := w.validateManagedPath(path)
	if err != nil {
		return ports.WorkspaceObservation{}, err
	}
	path = validated

	head, err := w.run(ctx, w.binary, "-C", path, "rev-parse", "--verify", "HEAD")
	if err != nil {
		return ports.WorkspaceObservation{}, fmt.Errorf("gitworktree: observe HEAD: %w", err)
	}
	branch := strings.TrimSpace(info.Branch)
	if out, branchErr := w.run(ctx, w.binary, "-C", path, "branch", "--show-current"); branchErr == nil {
		if current := strings.TrimSpace(string(out)); current != "" {
			branch = current
		}
	}
	statusOut, err := w.run(ctx, w.binary, "-C", path, "status", "--porcelain=v1", "--untracked-files=all")
	if err != nil {
		return ports.WorkspaceObservation{}, fmt.Errorf("gitworktree: observe status: %w", err)
	}
	changes, staged, untracked := parseObservedWorkspaceChanges(string(statusOut), maxObservedWorkspaceChanges)

	// Unit and record separators avoid ambiguity from spaces in subjects. Git
	// subjects cannot contain a newline, and the bounded output is local-only.
	logFormat := "%H%x1f%s%x1f%aI%x1e"
	logOut, err := w.run(ctx, w.binary, "-C", path, "log", "-n", fmt.Sprintf("%d", maxObservedWorkspaceCommits), "--pretty=format:"+logFormat)
	if err != nil {
		return ports.WorkspaceObservation{}, fmt.Errorf("gitworktree: observe log: %w", err)
	}
	return ports.WorkspaceObservation{
		Path:      path,
		Branch:    branch,
		HeadSHA:   strings.TrimSpace(string(head)),
		Dirty:     len(changes) > 0,
		Staged:    staged,
		Untracked: untracked,
		Changes:   changes,
		Commits:   parseObservedWorkspaceCommits(string(logOut)),
	}, nil
}

func parseObservedWorkspaceChanges(output string, limit int) ([]ports.WorkspaceChange, bool, bool) {
	lines := strings.Split(strings.ReplaceAll(output, "\r\n", "\n"), "\n")
	changes := make([]ports.WorkspaceChange, 0, min(len(lines), limit))
	var staged, untracked bool
	for _, line := range lines {
		if len(line) < 3 {
			continue
		}
		status := line[:2]
		path := strings.TrimSpace(line[3:])
		if path == "" {
			continue
		}
		if status == "??" {
			untracked = true
		} else if status[0] != ' ' && status[0] != '?' {
			staged = true
		}
		if len(changes) < limit {
			changes = append(changes, ports.WorkspaceChange{Path: path, Status: status})
		}
	}
	return changes, staged, untracked
}

func parseObservedWorkspaceCommits(output string) []ports.WorkspaceCommit {
	records := strings.Split(output, "\x1e")
	commits := make([]ports.WorkspaceCommit, 0, len(records))
	for _, record := range records {
		record = strings.TrimSpace(record)
		if record == "" {
			continue
		}
		fields := strings.SplitN(record, "\x1f", 3)
		if len(fields) != 3 {
			continue
		}
		commits = append(commits, ports.WorkspaceCommit{
			SHA:        strings.TrimSpace(fields[0]),
			Subject:    strings.TrimSpace(fields[1]),
			AuthoredAt: strings.TrimSpace(fields[2]),
		})
	}
	return commits
}

// runCherryPickNoCommit runs "git -C <worktree> cherry-pick --no-commit <sha>"
// and captures combined output so any conflict details are available in the
// returned commandError. Exit code detection happens in the caller.
func (w *Workspace) runCherryPickNoCommit(ctx context.Context, worktree, commitSHA string) error {
	args := cherryPickNoCommitArgs(worktree, commitSHA)
	cmd := aoprocess.CommandContext(ctx, w.binary, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return commandError{args: append([]string{w.binary}, args...), output: string(out), err: err}
	}
	return nil
}

// Restore re-attaches to an existing worktree for the session if one is still
// present, recreating the handle without disturbing its contents.
func (w *Workspace) Restore(ctx context.Context, cfg ports.WorkspaceConfig) (ports.WorkspaceInfo, error) {
	if err := validateConfig(cfg); err != nil {
		return ports.WorkspaceInfo{}, err
	}
	repo, err := w.repoPathForConfig(cfg)
	if err != nil {
		return ports.WorkspaceInfo{}, err
	}
	path, err := w.restorePath(cfg)
	if err != nil {
		return ports.WorkspaceInfo{}, err
	}
	records, err := w.listRecords(ctx, repo)
	if err != nil {
		return ports.WorkspaceInfo{}, err
	}
	// recreateBranch is the branch used if we fall through to recreate the
	// worktree below. It defaults to cfg.Branch (the "no prior registration"
	// case) but is overridden to the stale registration's own branch when one
	// is found, so a session sitting on a child branch is not silently
	// recreated on cfg.Branch (typically the root branch) instead.
	recreateBranch := cfg.Branch
	if rec, ok := findWorktree(records, path); ok {
		missing, err := registeredWorktreeDirMissing(rec)
		if err != nil {
			return ports.WorkspaceInfo{}, err
		}
		if !missing {
			branch := rec.Branch
			if branch == "" {
				branch = cfg.Branch
			}
			return ports.WorkspaceInfo{Path: path, Branch: branch, BaseRef: cfg.BaseRef, SessionID: cfg.SessionID, ProjectID: cfg.ProjectID, RepoPath: repo}, nil
		}
		// The registration outlived its directory (issue #2775: a session's git
		// worktree registration and DB row survived a deletion that removed only
		// the directory). Fall through to recreate the worktree at path below,
		// on the registration's own branch (not cfg.Branch), instead of
		// returning a handle to a directory that does not exist, which
		// previously made `cd <path> || exit` in the tmux launch command exit
		// instantly with no diagnostic. addWorktree re-registers the stale path
		// itself via `worktree add --force`; the registration is left in place
		// until then.
		if rec.Branch != "" {
			recreateBranch = rec.Branch
		}
	}
	if nonEmpty, err := pathExistsNonEmpty(path); err != nil {
		return ports.WorkspaceInfo{}, err
	} else if nonEmpty {
		if cfg.Path == "" {
			return ports.WorkspaceInfo{}, fmt.Errorf("gitworktree: refusing to restore %q: path exists and is not a registered worktree", path)
		}
		if _, err := moveStrayPathAside(path); err != nil {
			return ports.WorkspaceInfo{}, err
		}
	}
	if err := w.validateBranch(ctx, repo, recreateBranch); err != nil {
		return ports.WorkspaceInfo{}, err
	}
	baseRef, err := w.addWorktree(ctx, repo, path, recreateBranch, cfg.BaseBranch, cfg.BaseRef, false)
	if err != nil {
		return ports.WorkspaceInfo{}, err
	}
	return ports.WorkspaceInfo{Path: path, Branch: recreateBranch, BaseRef: baseRef, SessionID: cfg.SessionID, ProjectID: cfg.ProjectID, RepoPath: repo}, nil
}

func (w *Workspace) existingWorktree(ctx context.Context, repo, path string, cfg ports.WorkspaceConfig) (ports.WorkspaceInfo, bool, error) {
	records, err := w.listRecords(ctx, repo)
	if err != nil {
		return ports.WorkspaceInfo{}, false, err
	}
	if rec, ok := findWorktree(records, path); ok {
		missing, err := registeredWorktreeDirMissing(rec)
		if err != nil {
			return ports.WorkspaceInfo{}, false, err
		}
		if missing {
			// Report "no existing worktree" so Create falls through to
			// addWorktree, which re-registers this stale path in one step via
			// `worktree add --force`.
			//
			// Unlike Restore, Create deliberately recreates on cfg.Branch and
			// not on rec.Branch. Restore is re-attaching to a live session
			// whose branch may have moved on past what AO recorded, so the
			// registration is the better source of truth there; Create is
			// materializing a NEW session, where a registration at this path is
			// a leftover from a prior session of the same name and its branch
			// says nothing about what the caller asked for.
			return ports.WorkspaceInfo{}, false, nil
		}
		branch := rec.Branch
		if branch == "" {
			branch = cfg.Branch
		}
		return ports.WorkspaceInfo{Path: path, Branch: branch, SessionID: cfg.SessionID, ProjectID: cfg.ProjectID}, true, nil
	}
	return ports.WorkspaceInfo{}, false, nil
}

// registeredWorktreeDirMissing reports whether a git-registered worktree's
// directory no longer exists on disk. A worktree registration (and the
// session's DB row) can outlive its directory when something removes the path
// out of band of AO's own teardown (issue #2775: session agent-orchestrator-78
// kept its branches and worktree registration but its directory was gone, so
// handing that path straight to the runtime made the tmux launch command's
// `cd <path> || exit` guard exit instantly with no diagnostic). When it reports
// true, the caller materializes a fresh worktree at the same path with
// `git worktree add --force` (see worktreeAddForce), which re-registers the
// path itself: nothing here removes or prunes a registration first.
//
// This is deliberately a pure observation. Recovering by first clearing the
// stale registration would mean either the repo-wide `git worktree prune`,
// which also drops the registration of every OTHER worktree of the repo whose
// directory git currently cannot see (verified against real git: two worktrees
// with their directories both deleted, one prune removes BOTH registrations,
// silently reintroducing the "recreated on the wrong branch" failure
// recreateBranch exists to prevent for a sibling session that never asked to be
// touched), or the target-specific `git worktree remove --force`, which is
// check-then-delete against this stat: if anything materializes the worktree
// between the two, the force-remove deletes a live worktree and any uncommitted
// agent work in it. `worktree add --force` has neither problem: it touches only
// this path's registration, and git refuses it outright when the directory
// exists and is non-empty, so a lost race fails loudly instead of destroying
// work.
//
// If rec is locked (`git worktree lock`), this returns ErrWorktreeLocked
// instead of reporting a recoverable registration. Verified against real git: a
// single `worktree add --force` refuses a missing-but-locked registration (it
// demands `-f -f`), and `git worktree prune` leaves such a registration in
// place too, so attempting recovery here would just relay an opaque git error
// downstream. Locking is an explicit operator signal not to touch a worktree,
// so recovering it automatically would be wrong even if git allowed it.
func registeredWorktreeDirMissing(rec worktreeRecord) (bool, error) {
	info, err := os.Stat(rec.Path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return false, fmt.Errorf("gitworktree: stat registered worktree %q: %w", rec.Path, err)
		}
		if rec.Locked {
			return false, fmt.Errorf(
				"%w: %q (branch %q) is registered but its directory is missing; unlock it (`git worktree unlock %s`) and retry, or remove the registration manually",
				ErrWorktreeLocked, rec.Path, rec.Branch, rec.Path,
			)
		}
		return true, nil
	}
	if !info.IsDir() {
		return false, fmt.Errorf("gitworktree: registered worktree %q is not a directory", rec.Path)
	}
	return false, nil
}

func (w *Workspace) addWorktree(ctx context.Context, repo, path, branch, baseBranch, baseRef string, resolveExistingBase bool) (string, error) {
	// Refuse early if the branch is already checked out in another worktree:
	// `git worktree add` will fail, but its stderr leaks through as an opaque
	// 500. A typed sentinel lets the HTTP layer surface a 409.
	records, err := w.listRecords(ctx, repo)
	if err != nil {
		return "", err
	}
	if conflict, ok := findWorktreeByBranch(records, branch); ok && filepath.Clean(conflict.Path) != filepath.Clean(path) {
		return "", fmt.Errorf("%w: %q is checked out at %q", ErrBranchCheckedOutElsewhere, branch, conflict.Path)
	}
	// A registration at path whose directory is gone makes a plain add fail
	// ("is a missing but already registered worktree; use 'add -f' to
	// override"), so re-register it in the same add instead of clearing it
	// first. records is the freshly listed state, so this decision and the add
	// it feeds are as close together as git allows.
	force, err := staleRegistrationForPath(records, path)
	if err != nil {
		return "", err
	}

	localBranch, err := w.refExists(ctx, repo, "refs/heads/"+branch)
	if err != nil {
		return "", err
	}
	if localBranch {
		// Create needs a freshly resolved base for accurate initial diffs even
		// when the requested branch already exists. Restore already has durable
		// base metadata and only needs to reattach that branch; making it resolve
		// again can fail after remote metadata becomes unavailable.
		if resolveExistingBase && baseRef == "" {
			refs, err := w.resolveWorktreeRefsWithBudget(ctx, repo, branch, baseBranch)
			if err != nil {
				if errors.Is(err, errNoBaseRef) {
					return "", fmt.Errorf("%w: %q has no local head, no remote, and no tag — run `git fetch` then retry", ErrBranchNotFetched, branch)
				}
				return "", err
			}
			baseRef = refs.baseRef
		}
		if _, err := w.run(ctx, w.binary, worktreeAddBranchArgs(repo, path, branch, force)...); err != nil {
			return "", fmt.Errorf("gitworktree: worktree add existing branch %q: %w", branch, err)
		}
		return baseRef, nil
	}
	if baseRef == "" {
		refs, resolveErr := w.resolveWorktreeRefsWithBudget(ctx, repo, branch, baseBranch)
		err = resolveErr
		if err != nil {
			if errors.Is(err, errNoBaseRef) {
				return "", fmt.Errorf("%w: %q has no local head, no remote, and no tag — run `git fetch` then retry", ErrBranchNotFetched, branch)
			}
			return "", err
		}
		baseRef = refs.baseRef
		if err := w.addNewBranchWorktree(ctx, repo, branch, path, refs.seedRef, force); err != nil {
			return "", fmt.Errorf("gitworktree: worktree add branch %q from %q: %w", branch, refs.seedRef, err)
		}
		return baseRef, nil
	}
	seedRef := baseRef
	requestedRef := "origin/" + branch
	if exists, err := w.refExists(ctx, repo, requestedRef); err != nil {
		return "", err
	} else if exists {
		seedRef = requestedRef
	}

	// Restore reaches this path when its local branch is gone but its durable
	// comparison ref remains. Fresh creation returns above after resolving its
	// seed and comparison refs independently.
	if err := w.addNewBranchWorktree(ctx, repo, branch, path, seedRef, force); err != nil {
		return "", fmt.Errorf("gitworktree: worktree add branch %q from %q: %w", branch, seedRef, err)
	}
	return baseRef, nil
}

// staleRegistrationForPath reports whether records carries a registration for
// path whose directory is gone, i.e. whether an add at path needs git's
// `--force` override to re-register it. No registration at all is not stale.
func staleRegistrationForPath(records []worktreeRecord, path string) (bool, error) {
	rec, ok := findWorktree(records, path)
	if !ok {
		return false, nil
	}
	return registeredWorktreeDirMissing(rec)
}

// addNewBranchWorktree runs `git worktree add [--force] -b <branch> <path>
// <baseRef>` and recovers when git rejects path as a stale registration that
// the caller's own pre-check did not see (the directory vanished between that
// check and this add).
//
// The retry deliberately does NOT repeat the `-b` form. git creates
// refs/heads/<branch> BEFORE it validates the target path, so a `-b` attempt
// that fails on the stale registration still leaves the branch behind:
// verified against git 2.54, the failed add prints "Preparing worktree (new
// branch '<branch>')", exits 128, and `show-ref` then finds the branch. A
// `--force -b` retry therefore never recovers, it fails with "a branch named
// '<branch>' already exists" (exit 255), which is the bug this addresses.
// Retrying on the existing-branch form `worktree add --force <path> <branch>`
// checks out the branch the first attempt just created, at baseRef, which is
// exactly what the `-b` form would have produced.
//
// Both callers reach the `-b` form only after confirming refs/heads/<branch>
// did not exist (addWorktree via refExists, createWorkspaceProjectRepo via
// workspaceProjectBranchFree), so a branch present after the failed attempt is
// the one that attempt created, never a pre-existing branch this would hijack.
// The ref is re-read rather than assumed, so a future caller that skips that
// precondition still gets the correct form.
//
// A recovery that fails outright leaves the created branch ref behind. That is
// deliberate: the failure that matters here is "path exists and is non-empty",
// which means something else materialized the worktree at path first, possibly
// checked out on this very branch, and deleting the ref would then damage the
// worktree that won. The leftover ref is harmless and self-correcting: the next
// addWorktree for it takes the existing-branch path, and workspaceProjectBranch
// simply picks the next free candidate.
func (w *Workspace) addNewBranchWorktree(ctx context.Context, repo, branch, path, baseRef string, force bool) error {
	_, err := w.run(ctx, w.binary, worktreeAddNewBranchArgs(repo, branch, path, baseRef, force)...)
	if err == nil {
		return nil
	}
	// --force was already in play, so the stale registration is not what failed.
	if force || !isMissingRegisteredWorktreeError(err) {
		return err
	}
	// Report the recovery failure alongside the original: on its own, the
	// original ("is a missing but already registered worktree") names the
	// condition recovery was FOR, not the reason recovery failed, and the two
	// are routinely different. The interesting ones are "'<path>' already
	// exists" (another restore materialized the worktree first, so this one
	// lost the race and must not be read as a stale registration) and "missing
	// but locked" (a registration that acquired a lock after the caller's
	// pre-check). Joining keeps errors.Is working for both.
	created, refErr := w.refExists(ctx, repo, "refs/heads/"+branch)
	if refErr != nil {
		return errors.Join(err, refErr)
	}
	retryArgs := worktreeAddNewBranchArgs(repo, branch, path, baseRef, true)
	if created {
		retryArgs = worktreeAddBranchArgs(repo, path, branch, true)
	}
	if _, retryErr := w.run(ctx, w.binary, retryArgs...); retryErr != nil {
		return errors.Join(err, retryErr)
	}
	return nil
}

type workspaceProjectRepo struct {
	name         string
	relativePath string
	repoPath     string
	outputPath   string
	baseBranch   string
	seedRef      string
	baseRef      string
}

func (w *Workspace) workspaceProjectBranch(ctx context.Context, repos []workspaceProjectRepo, requested string) (string, error) {
	branch := strings.TrimSpace(requested)
	if branch == "" {
		return "", errors.New("gitworktree: branch is required")
	}
	for i := 0; i < 100; i++ {
		candidate := branch
		if i > 0 {
			candidate = fmt.Sprintf("%s-%d", branch, i+1)
		}
		free, err := w.workspaceProjectBranchFree(ctx, repos, candidate)
		if err != nil {
			return "", err
		}
		if free {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("gitworktree: could not find free workspace branch for %q", branch)
}

func (w *Workspace) workspaceProjectBranchFree(ctx context.Context, repos []workspaceProjectRepo, branch string) (bool, error) {
	for _, repo := range repos {
		if err := w.validateBranch(ctx, repo.repoPath, branch); err != nil {
			return false, err
		}
		exists, err := w.refExists(ctx, repo.repoPath, "refs/heads/"+branch)
		if err != nil {
			return false, err
		}
		if exists {
			return false, nil
		}
		records, err := w.listRecords(ctx, repo.repoPath)
		if err != nil {
			return false, err
		}
		if conflict, ok := findWorktreeByBranch(records, branch); ok && filepath.Clean(conflict.Path) != filepath.Clean(repo.outputPath) {
			return false, nil
		}
	}
	return true, nil
}

func (w *Workspace) createWorkspaceProjectRepo(ctx context.Context, repo workspaceProjectRepo, branch string) (string, error) {
	baseRef := strings.TrimSpace(repo.baseRef)
	if baseRef == "" {
		return "", errors.New("gitworktree: workspace repository base was not resolved before creation")
	}
	seedRef := firstNonEmpty(strings.TrimSpace(repo.seedRef), baseRef)
	baseSHA, err := w.revParse(ctx, repo.repoPath, baseRef)
	if err != nil {
		return "", err
	}
	// Same up-front stale-registration check addWorktree does, so the ordinary
	// #2775 shape (registration outlived its directory) is handled by the first
	// add and never reaches the recovery below. Without it every recovery here
	// had to go through a failed `-b` attempt, which leaves a stray branch ref
	// behind even when it succeeds.
	records, err := w.listRecords(ctx, repo.repoPath)
	if err != nil {
		return "", err
	}
	force, err := staleRegistrationForPath(records, repo.outputPath)
	if err != nil {
		return "", err
	}
	// Recovery from a registration that only goes stale after that check is
	// addNewBranchWorktree's job: git's own --force override, not the repo-wide
	// prune this used to run, which would also drop sibling sessions'
	// registrations.
	if err := w.addNewBranchWorktree(ctx, repo.repoPath, branch, repo.outputPath, seedRef, force); err != nil {
		return "", fmt.Errorf("gitworktree: workspace repo %q worktree add branch %q from %q: %w", repo.name, branch, seedRef, err)
	}
	return baseSHA, nil
}

func (w *Workspace) forceDestroyPath(ctx context.Context, repo, path string) error {
	if err := w.requireReachableRepo(repo); err != nil {
		return err
	}
	// Force teardown has no refusal to honour, so the move is unconditional:
	// rename the directory out of the way, drop the registration, unlink in the
	// background.
	if discarded, ok := w.discard(path); ok {
		if err := w.pruneWorktrees(ctx, repo); err != nil {
			w.undiscard(discarded, path)
			return err
		}
		w.removeInBackground(discarded)
		return nil
	}
	_, _ = w.run(ctx, w.binary, worktreeForceRemoveArgs(repo, path)...)
	if err := w.pruneWorktrees(ctx, repo); err != nil {
		return err
	}
	if err := removeAllWithRetry(ctx, path); err != nil {
		return fmt.Errorf("gitworktree: force remove path %q: %w", path, err)
	}
	return nil
}

func (w *Workspace) pruneWorktrees(ctx context.Context, repo string) error {
	if _, err := w.run(ctx, w.binary, worktreePruneArgs(repo)...); err != nil {
		return fmt.Errorf("gitworktree: worktree prune: %w", err)
	}
	return nil
}

func isMissingRegisteredWorktreeError(err error) bool {
	return strings.Contains(err.Error(), "is a missing but already registered worktree")
}

func isLockedWorktreeRemoveError(err error) bool {
	return strings.Contains(err.Error(), "cannot remove a locked working tree")
}

func (w *Workspace) revParse(ctx context.Context, repo, ref string) (string, error) {
	out, err := w.run(ctx, w.binary, "-C", repo, "rev-parse", "--verify", ref)
	if err != nil {
		return "", fmt.Errorf("gitworktree: rev-parse %q: %w", ref, err)
	}
	return strings.TrimSpace(string(out)), nil
}

func (w *Workspace) validateBranch(ctx context.Context, repo, branch string) error {
	if _, err := w.run(ctx, w.binary, checkRefFormatBranchArgs(repo, branch)...); err != nil {
		return fmt.Errorf("%w: %q (%w)", ErrBranchInvalid, branch, err)
	}
	return nil
}

// errNoBaseRef is an internal sentinel: every candidate base ref is missing.
// addWorktree translates it into ErrBranchNotFetched.
var errNoBaseRef = errors.New("gitworktree: no base ref found")

// worktreeRefs keeps the ref used to seed a session branch separate from the
// repository-default ref used for diffs. A fetched session branch may be ahead
// of the default; returning it as the diff base would hide all of those commits.
type worktreeRefs struct {
	seedRef string
	baseRef string
}

func (w *Workspace) resolveWorktreeRefs(ctx, remoteCtx context.Context, repo, branch, baseBranch string) (worktreeRefs, error) {
	if strings.TrimSpace(baseBranch) != "" {
		return w.resolveWorktreeRefsFromDefault(ctx, repo, branch, baseBranch)
	}
	resolver := gitdefault.New(w.binary, gitdefault.Runner(w.run))
	remote, requestedRef, requestedExists, requestedErr := resolver.FindCachedRemoteBranch(ctx, repo, branch)
	if requestedErr != nil && !errors.Is(requestedErr, gitdefault.ErrUnresolved) {
		return worktreeRefs{}, fmt.Errorf("gitworktree: inspect requested branch for %q: %w", repo, requestedErr)
	}
	resolution, err := resolver.Resolve(ctx, remoteCtx, repo)
	if err != nil {
		if errors.Is(err, gitdefault.ErrUnresolved) {
			// A fetched requested branch remains a valid seed when the remote
			// exposes no default metadata. There is no authoritative comparison
			// ref in this case, so preserve the legacy fallback to that branch.
			if requestedExists {
				return worktreeRefs{seedRef: requestedRef, baseRef: requestedRef}, nil
			}
			return worktreeRefs{}, fmt.Errorf(
				"%w: %s; configure this repository's primary remote and cached HEAD (for example, `git -C %q remote set-head <remote> <branch>`) and retry",
				ErrDefaultBranchUnresolved, strings.TrimPrefix(err.Error(), gitdefault.ErrUnresolved.Error()+": "),
				repo,
			)
		}
		return worktreeRefs{}, fmt.Errorf("gitworktree: resolve repository default for %q: %w", repo, err)
	}
	slog.Debug("gitworktree: resolved automatic default branch",
		"repo", repo,
		"remote", resolution.Remote,
		"branch", resolution.Branch,
		"source", resolution.Source,
	)

	if exists, err := w.refExists(ctx, repo, resolution.Ref); err != nil {
		return worktreeRefs{}, err
	} else if exists {
		seedRef := resolution.Ref
		if requestedExists {
			slog.Debug("gitworktree: resolved requested remote branch",
				"repo", repo,
				"remote", remote,
				"branch", branch,
			)
			seedRef = requestedRef
		}
		return worktreeRefs{seedRef: seedRef, baseRef: resolution.Ref}, nil
	}
	return worktreeRefs{}, fmt.Errorf("%w: resolved default branch %q from %q, but ref %q is unavailable", errNoBaseRef, resolution.Branch, resolution.Source, resolution.Ref)
}

func (w *Workspace) resolveWorktreeRefsWithBudget(ctx context.Context, repo, branch, baseBranch string) (worktreeRefs, error) {
	remoteCtx, cancelRemote := context.WithTimeout(ctx, defaultBranchResolutionBudget)
	defer cancelRemote()
	return w.resolveWorktreeRefs(ctx, remoteCtx, repo, branch, baseBranch)
}

func (w *Workspace) resolveWorktreeRefsFromDefault(ctx context.Context, repo, branch, defaultBranch string) (worktreeRefs, error) {
	requestedRef := "origin/" + branch
	requestedExists, err := w.refExists(ctx, repo, requestedRef)
	if err != nil {
		return worktreeRefs{}, err
	}
	baseCandidates := configuredBaseRefCandidates(defaultBranch)
	for _, ref := range baseCandidates {
		exists, err := w.refExists(ctx, repo, ref)
		if err != nil {
			return worktreeRefs{}, err
		}
		if exists {
			seedRef := ref
			if requestedExists {
				seedRef = requestedRef
			}
			return worktreeRefs{seedRef: seedRef, baseRef: ref}, nil
		}
	}

	// Preserve the adapter's fallback when the configured default is missing:
	// an already-fetched session branch or same-named tag can still be resumed,
	// but is used as the comparison ref only because no default ref is available.
	if requestedExists {
		return worktreeRefs{seedRef: requestedRef, baseRef: requestedRef}, nil
	}
	// Also probe a same-named tag so requests like `--branch v1.2.3` can
	// auto-track when the tag is fetched but no branch ref exists.
	tagRef := "refs/tags/" + branch
	fallbackCandidates := []string{branch, tagRef}
	for _, ref := range fallbackCandidates {
		exists, err := w.refExists(ctx, repo, ref)
		if err != nil {
			return worktreeRefs{}, err
		}
		if exists {
			return worktreeRefs{seedRef: ref, baseRef: ref}, nil
		}
	}
	allCandidates := append([]string{requestedRef}, baseCandidates...)
	allCandidates = append(allCandidates, fallbackCandidates...)
	return worktreeRefs{}, fmt.Errorf("%w for branch %q (tried %s)", errNoBaseRef, branch, strings.Join(allCandidates, ", "))
}

func (w *Workspace) refExists(ctx context.Context, repo, ref string) (bool, error) {
	_, err := w.run(ctx, w.binary, revParseVerifyArgs(repo, ref)...)
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false, nil
	}
	return false, fmt.Errorf("gitworktree: verify ref %q: %w", ref, err)
}

// isDirty reports whether the worktree at path has uncommitted changes or
// untracked files — the same check `git worktree remove` performs before
// refusing without --force.
func (w *Workspace) isDirty(ctx context.Context, path string) (bool, error) {
	out, err := w.run(ctx, w.binary, statusPorcelainArgs(path)...)
	if err != nil {
		return false, fmt.Errorf("gitworktree: status %q: %w", path, err)
	}
	return strings.TrimSpace(string(out)) != "", nil
}

func (w *Workspace) listRecords(ctx context.Context, repo string) ([]worktreeRecord, error) {
	out, err := w.run(ctx, w.binary, worktreeListPorcelainArgs(repo)...)
	if err != nil {
		return nil, fmt.Errorf("gitworktree: worktree list: %w", err)
	}
	records, err := parseWorktreePorcelain(string(out))
	if err != nil {
		return nil, fmt.Errorf("gitworktree: parse worktree list: %w", err)
	}
	return records, nil
}

func (w *Workspace) repoPath(project domain.ProjectID) (string, error) {
	repo, err := w.repos.RepoPath(project)
	if err != nil {
		return "", err
	}
	if repo == "" {
		return "", fmt.Errorf("gitworktree: no repo configured for project %q", project)
	}
	abs, err := physicalAbs(repo)
	if err != nil {
		return "", fmt.Errorf("gitworktree: repo path: %w", err)
	}
	return abs, nil
}

func (w *Workspace) repoPathForInfo(info ports.WorkspaceInfo) (string, error) {
	if info.RepoPath != "" {
		repo, err := physicalAbs(info.RepoPath)
		if err != nil {
			return "", fmt.Errorf("gitworktree: repo path: %w", err)
		}
		return repo, nil
	}
	if info.ProjectID == "" {
		return "", errors.New("gitworktree: project id is required")
	}
	return w.repoPath(info.ProjectID)
}

// requireReachableRepo reports the project repo as unavailable when it is no
// longer on disk. Teardown is the only caller: every git command it runs is
// `git -C <repo> ...`, so a deleted project directory turns each one into an
// opaque exit 128, and the session it belongs to can never be deleted. Create
// and Restore deliberately do not use this: there, a missing repo must fail.
func (w *Workspace) requireReachableRepo(repo string) error {
	if info, err := os.Stat(repo); err == nil && info.IsDir() {
		return nil
	}
	return fmt.Errorf("gitworktree: repository %q is no longer on disk: %w", repo, ports.ErrWorkspaceRepoUnavailable)
}

func (w *Workspace) repoPathForConfig(cfg ports.WorkspaceConfig) (string, error) {
	if cfg.RepoPath != "" {
		repo, err := physicalAbs(cfg.RepoPath)
		if err != nil {
			return "", fmt.Errorf("gitworktree: repo path: %w", err)
		}
		return repo, nil
	}
	return w.repoPath(cfg.ProjectID)
}

func physicalAbs(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	abs = filepath.Clean(abs)
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return filepath.Clean(resolved), nil
	}
	parent := filepath.Dir(abs)
	base := filepath.Base(abs)
	for parent != "." && parent != string(os.PathSeparator) {
		if resolved, err := filepath.EvalSymlinks(parent); err == nil {
			return filepath.Join(resolved, base), nil
		}
		base = filepath.Join(filepath.Base(parent), base)
		parent = filepath.Dir(parent)
	}
	if resolved, err := filepath.EvalSymlinks(parent); err == nil {
		return filepath.Join(resolved, base), nil
	}
	return abs, nil
}

func validateConfig(cfg ports.WorkspaceConfig) error {
	if cfg.ProjectID == "" {
		return errors.New("gitworktree: project id is required")
	}
	if err := validatePathComponent("project id", string(cfg.ProjectID)); err != nil {
		return err
	}
	if cfg.Kind == domain.KindOrchestrator {
		prefix := resolvedSessionPrefix(cfg)
		if err := validatePathComponent("session prefix", prefix); err != nil {
			return err
		}
	} else {
		if cfg.SessionID == "" {
			return errors.New("gitworktree: session id is required")
		}
		if err := validatePathComponent("session id", string(cfg.SessionID)); err != nil {
			return err
		}
	}
	if cfg.Branch == "" {
		return errors.New("gitworktree: branch is required")
	}
	return nil
}

func validateWorkspaceProjectConfig(cfg ports.WorkspaceProjectConfig) error {
	if err := validateConfig(ports.WorkspaceConfig{
		ProjectID:     cfg.ProjectID,
		SessionID:     cfg.SessionID,
		Kind:          cfg.Kind,
		SessionPrefix: cfg.SessionPrefix,
		Branch:        firstNonEmpty(cfg.Branch, defaultSessionBranchName(cfg.SessionID)),
		BaseBranch:    cfg.BaseBranch,
	}); err != nil {
		return err
	}
	if strings.TrimSpace(cfg.RootRepoPath) == "" {
		return errors.New("gitworktree: root repo path is required")
	}
	for _, repo := range cfg.Repos {
		if strings.TrimSpace(repo.Name) == "" {
			return errors.New("gitworktree: child repo name is required")
		}
		if err := validatePathComponent("child repo name", repo.Name); err != nil {
			return err
		}
		if strings.TrimSpace(repo.RepoPath) == "" {
			return fmt.Errorf("gitworktree: child repo %q path is required", repo.Name)
		}
		if _, err := cleanRelativePath(repo.RelativePath); err != nil {
			return fmt.Errorf("gitworktree: child repo %q: %w", repo.Name, err)
		}
	}
	return nil
}

// validatePathComponent rejects id values that could escape the managed root
// once joined into a path. filepath.Join cleans `..` before validateManagedPath
// runs, so a session id of "../other" would otherwise resolve back inside
// managedRoot while breaking per-project isolation. Reject any path separator
// or the special `.`/`..` components at the source.
func validatePathComponent(name, value string) error {
	if strings.ContainsAny(value, `/\`) {
		return fmt.Errorf("%w: %s %q must not contain path separators", ErrUnsafePath, name, value)
	}
	if value == "." || value == ".." {
		return fmt.Errorf("%w: %s %q must not be a path-traversal component", ErrUnsafePath, name, value)
	}
	return nil
}

func (w *Workspace) managedPath(cfg ports.WorkspaceConfig) (string, error) {
	var path string
	if cfg.Kind == domain.KindOrchestrator {
		prefix := resolvedSessionPrefix(cfg)
		path = filepath.Join(w.managedRoot, string(cfg.ProjectID), "orchestrator", prefix+"-orchestrator")
	} else {
		path = filepath.Join(w.managedRoot, string(cfg.ProjectID), string(cfg.SessionID))
	}
	return w.validateManagedPath(path)
}

func (w *Workspace) restorePath(cfg ports.WorkspaceConfig) (string, error) {
	if cfg.Path != "" {
		return w.validateManagedPath(cfg.Path)
	}
	return w.managedPath(cfg)
}

// resolvedSessionPrefix returns cfg.SessionPrefix when set, otherwise the first
// 12 characters of the project ID (matching the display-prefix convention).
func resolvedSessionPrefix(cfg ports.WorkspaceConfig) string {
	if p := strings.TrimSpace(cfg.SessionPrefix); p != "" {
		return p
	}
	id := string(cfg.ProjectID)
	if len(id) <= 12 {
		return id
	}
	return id[:12]
}

func defaultSessionBranchName(id domain.SessionID) string {
	return "ao/" + string(id)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func cleanRelativePath(path string) (string, error) {
	rel := filepath.ToSlash(strings.TrimSpace(path))
	if rel == "" {
		return "", errors.New("relative path is required")
	}
	if strings.HasPrefix(rel, "/") {
		return "", fmt.Errorf("%w: relative path %q must not be absolute", ErrUnsafePath, path)
	}
	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(rel)))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("%w: relative path %q escapes the workspace root", ErrUnsafePath, path)
	}
	return clean, nil
}

func (w *Workspace) validateManagedPath(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("%w: empty path", ErrUnsafePath)
	}
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("%w: %q is not absolute", ErrUnsafePath, path)
	}
	clean := filepath.Clean(path)
	if clean != path {
		return "", fmt.Errorf("%w: %q is not clean", ErrUnsafePath, path)
	}
	physical, err := physicalAbs(clean)
	if err != nil {
		return "", fmt.Errorf("gitworktree: resolve path %q: %w", path, err)
	}
	clean = physical
	inside, err := pathWithin(w.managedRoot, clean)
	if err != nil {
		return "", err
	}
	if !inside || clean == w.managedRoot {
		return "", fmt.Errorf("%w: %q is outside managed root %q", ErrUnsafePath, clean, w.managedRoot)
	}
	return clean, nil
}

func pathWithin(root, path string) (bool, error) {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false, fmt.Errorf("gitworktree: compare paths: %w", err)
	}
	return rel == "." || (rel != "" && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))), nil
}

func findWorktree(records []worktreeRecord, path string) (worktreeRecord, bool) {
	clean := filepath.Clean(path)
	for _, rec := range records {
		if filepath.Clean(rec.Path) == clean {
			return rec, true
		}
	}
	return worktreeRecord{}, false
}

func findWorktreeByBranch(records []worktreeRecord, branch string) (worktreeRecord, bool) {
	for _, rec := range records {
		if rec.Branch == branch {
			return rec, true
		}
	}
	return worktreeRecord{}, false
}

func pathExistsNonEmpty(path string) (bool, error) {
	entries, err := os.ReadDir(path)
	if err == nil {
		return len(entries) > 0, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, fmt.Errorf("gitworktree: inspect path %q: %w", path, err)
}

func moveStrayPathAside(path string) (string, error) {
	for i := 0; i < 100; i++ {
		candidate := path + ".stray"
		if i > 0 {
			candidate = fmt.Sprintf("%s.stray-%d", path, i+1)
		}
		if _, err := os.Lstat(candidate); err == nil {
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("gitworktree: inspect stray destination %q: %w", candidate, err)
		}
		if err := os.Rename(path, candidate); err != nil {
			return "", fmt.Errorf("gitworktree: move stray path %q aside to %q: %w", path, candidate, err)
		}
		return candidate, nil
	}
	return "", fmt.Errorf("gitworktree: move stray path %q aside: no available destination", path)
}

func runCommand(ctx context.Context, binary string, args ...string) ([]byte, error) {
	cmd := aoprocess.CommandContext(ctx, binary, args...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GCM_INTERACTIVE=Never")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return out, commandError{args: append([]string{binary}, args...), output: string(out), err: err}
	}
	return out, nil
}

type commandError struct {
	args   []string
	output string
	err    error
}

func (e commandError) Error() string {
	if strings.TrimSpace(e.output) == "" {
		return fmt.Sprintf("%s: %v", strings.Join(e.args, " "), e.err)
	}
	return fmt.Sprintf("%s: %v: %s", strings.Join(e.args, " "), e.err, strings.TrimSpace(e.output))
}

func (e commandError) Unwrap() error { return e.err }
