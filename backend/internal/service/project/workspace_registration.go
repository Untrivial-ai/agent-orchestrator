package project

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
	aoprocess "github.com/aoagents/agent-orchestrator/backend/internal/process"
)

var workspaceRootIgnoreDenylist = []string{
	"node_modules/",
	"dist/",
	"build/",
	".cache/",
	".turbo/",
	"target/",
	"coverage/",
	"tmp/",
	"temp/",
}

func prepareWorkspaceProject(ctx context.Context, parent string, projectID domain.ProjectID, registeredAt time.Time) ([]domain.WorkspaceRepoRecord, error) {
	if err := validateWorkspaceParent(ctx, parent); err != nil {
		return nil, err
	}
	children, err := detectWorkspaceChildren(ctx, parent, projectID, registeredAt)
	if err != nil {
		return nil, err
	}
	if len(children) == 0 {
		return nil, apierr.Invalid("WORKSPACE_REPOS_REQUIRED", "Workspace project must contain at least one direct child git repository", map[string]any{
			"suggestedFix": "Create or move child repositories directly under the workspace folder, then retry.",
		})
	}
	if isGitRepo(parent) {
		if err := adoptWorkspaceParent(ctx, parent, children); err != nil {
			return nil, err
		}
	} else {
		if err := initWorkspaceParent(ctx, parent, children); err != nil {
			return nil, err
		}
	}
	if err := guardNoGitlinks(ctx, parent); err != nil {
		return nil, err
	}
	return children, nil
}

// validateWorkspaceParent checks that the parent folder is not a linked
// worktree of another repository and not a bare repo. These edge cases slip
// past isGitRepo but would corrupt an external repo or fail with a confusing
// error partway through mutation.
func validateWorkspaceParent(ctx context.Context, parent string) error {
	// Linked-worktree detection: a linked worktree has a .git FILE, not a dir.
	// However, a repo created with `git init --separate-git-dir=<elsewhere>`
	// also has .git as a file (containing "gitdir: <path>"). We must distinguish
	// the two: in a linked worktree, --git-dir points into .git/worktrees/<name>
	// which differs from --git-common-dir (the main .git). In a separate-git-dir
	// repo, both resolve to the same directory.
	gitPath := filepath.Join(parent, ".git")
	info, err := os.Lstat(gitPath)
	if err == nil && !info.IsDir() {
		// Probe git to tell us whether this is a worktree or a separate-git-dir repo.
		gitDir, errGD := gitOutput(ctx, parent, "rev-parse", "--git-dir")
		gitCommonDir, errCD := gitOutput(ctx, parent, "rev-parse", "--git-common-dir")
		if errGD != nil || errCD != nil {
			// Cannot interrogate — conservatively reject; we don't know what this is.
			probeErr := errGD
			if probeErr == nil {
				probeErr = errCD
			}
			return apierr.Invalid("WORKSPACE_PARENT_IS_WORKTREE",
				"Workspace parent has a .git file that could not be inspected; it may be a linked worktree of another repository",
				map[string]any{
					"path":         parent,
					"probeError":   probeErr.Error(),
					"suggestedFix": "Use the repository's main checkout directory, not a linked worktree.",
				})
		}
		// Resolve both paths to absolute, clean forms so the comparison is reliable
		// whether git returns relative or absolute paths.
		absGitDir := filepath.Clean(gitDir)
		if !filepath.IsAbs(absGitDir) {
			absGitDir = filepath.Clean(filepath.Join(parent, strings.TrimSpace(gitDir)))
		} else {
			absGitDir = filepath.Clean(strings.TrimSpace(gitDir))
		}
		absCommonDir := filepath.Clean(gitCommonDir)
		if !filepath.IsAbs(absCommonDir) {
			absCommonDir = filepath.Clean(filepath.Join(parent, strings.TrimSpace(gitCommonDir)))
		} else {
			absCommonDir = filepath.Clean(strings.TrimSpace(gitCommonDir))
		}
		// Resolve symlinks consistent with how isGitRepo normalises paths.
		if resolved, err := filepath.EvalSymlinks(absGitDir); err == nil {
			absGitDir = resolved
		}
		if resolved, err := filepath.EvalSymlinks(absCommonDir); err == nil {
			absCommonDir = resolved
		}
		// In a linked worktree --git-dir != --git-common-dir; in a separate-git-dir
		// repo they are the same (both point to the external git directory).
		if absGitDir != absCommonDir {
			return apierr.Invalid("WORKSPACE_PARENT_IS_WORKTREE",
				"Workspace parent must be a standalone repository or plain folder, not a worktree of another repository",
				map[string]any{
					"path":         parent,
					"suggestedFix": "Use the repository's main checkout directory, not a linked worktree.",
				})
		}
		// Same dir → separate-git-dir repo; fall through and allow it.
	}

	// Bare-repo detection: git init --bare creates a repo with no .git subdir at
	// all; --show-toplevel fails, so isGitRepo returns false, then git init
	// re-initialises the bare repo and git add -A fails with an opaque error.
	// Only reject on a definite "true" — if git can't run, keep the normal path.
	if out, err := gitOutput(ctx, parent, "rev-parse", "--is-bare-repository"); err == nil {
		if strings.TrimSpace(out) == "true" {
			return apierr.Invalid("WORKSPACE_PARENT_BARE",
				"Workspace parent must not be a bare repository",
				map[string]any{
					"path":         parent,
					"suggestedFix": "Create a non-bare clone or plain folder as the workspace parent.",
				})
		}
	}
	return nil
}

func detectWorkspaceChildren(ctx context.Context, parent string, projectID domain.ProjectID, registeredAt time.Time) ([]domain.WorkspaceRepoRecord, error) {
	entries, err := os.ReadDir(parent)
	if err != nil {
		return nil, apierr.Invalid("INVALID_PATH", "Workspace path could not be read", nil)
	}
	var repos []domain.WorkspaceRepoRecord
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		if name == ".git" {
			continue
		}
		child := filepath.Join(parent, name)
		if !isGitRepo(child) {
			continue
		}
		// Reject a child directory whose name collides with the reserved root name.
		// Plain folders with this name are fine (they fall through before here);
		// only a real git repo named __root__ would create a PK collision in
		// session_worktrees.
		if name == domain.RootWorkspaceRepoName {
			return nil, apierr.Invalid("WORKSPACE_CHILD_RESERVED_NAME",
				"Child repository name is reserved for internal use",
				map[string]any{
					"path":         child,
					"suggestedFix": fmt.Sprintf("Rename the directory %q — the name %q is reserved by AO for the workspace root.", child, domain.RootWorkspaceRepoName),
				})
		}
		if err := validateWorkspaceChild(ctx, child); err != nil {
			return nil, err
		}
		repos = append(repos, domain.WorkspaceRepoRecord{
			ProjectID:     projectID,
			Name:          name,
			RelativePath:  filepath.ToSlash(name),
			RepoOriginURL: resolveGitOriginURL(child),
			DefaultBranch: resolveDefaultBranch(child),
			RegisteredAt:  registeredAt,
		})
	}
	sort.Slice(repos, func(i, j int) bool { return repos[i].Name < repos[j].Name })
	return repos, nil
}

func validateWorkspaceChild(ctx context.Context, child string) error {
	gitPath := filepath.Join(child, ".git")
	info, err := os.Lstat(gitPath)
	if err != nil {
		return apierr.Invalid("INVALID_WORKSPACE_CHILD", "Workspace child repository is missing a .git directory", map[string]any{"path": child})
	}
	if !info.IsDir() {
		return apierr.Invalid("WORKSPACE_CHILD_IS_WORKTREE", "Workspace child repositories must be standalone repos, not worktrees of an external repo", map[string]any{
			"path":         child,
			"suggestedFix": "Register a standalone child repository, or clone/init it directly under the workspace parent.",
		})
	}
	if out, err := gitOutput(ctx, child, "rev-parse", "--is-bare-repository"); err != nil {
		return apierr.Invalid("INVALID_WORKSPACE_CHILD", "Workspace child repository could not be inspected", map[string]any{"path": child, "error": err.Error()})
	} else if strings.TrimSpace(out) == "true" {
		return apierr.Invalid("WORKSPACE_CHILD_BARE", "Workspace child repositories must not be bare repositories", map[string]any{"path": child})
	}
	if _, err := gitOutput(ctx, child, "rev-parse", "--verify", "HEAD"); err != nil {
		return apierr.Invalid("WORKSPACE_CHILD_UNBORN", "Workspace child repositories must have at least one commit", map[string]any{
			"path":         child,
			"suggestedFix": "Run `git init -b main`, add the initial files, and create the first commit before registering the workspace.",
		})
	}
	branch, err := gitOutput(ctx, child, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil || strings.TrimSpace(branch) == "" {
		return apierr.Invalid("WORKSPACE_CHILD_DEFAULT_BRANCH_UNKNOWN", "Workspace child repositories must have an identifiable default branch", map[string]any{
			"path":         child,
			"suggestedFix": "Check out the repository's default branch (for example `main`) and retry.",
		})
	}
	if origin := resolveGitOriginURL(child); origin == "" {
		return apierr.Invalid("WORKSPACE_CHILD_ORIGIN_REQUIRED", "Workspace child repositories must have an origin remote configured", map[string]any{
			"path":         child,
			"suggestedFix": "Run `git remote add origin <url>` in the child repository, then retry.",
		})
	}
	return nil
}

func adoptWorkspaceParent(ctx context.Context, parent string, repos []domain.WorkspaceRepoRecord) error {
	changed, err := ensureWorkspaceGitignore(parent, repos)
	if err != nil {
		return apierr.Invalid("WORKSPACE_PARENT_GITIGNORE_FAILED", "Failed to update workspace parent .gitignore", map[string]any{"error": err.Error()})
	}
	if !changed {
		return nil
	}
	if _, err := gitOutput(ctx, parent, "add", ".gitignore"); err != nil {
		return apierr.Invalid("WORKSPACE_PARENT_GITIGNORE_FAILED", "Failed to stage workspace parent .gitignore", map[string]any{"error": err.Error()})
	}
	if err := guardNoGitlinks(ctx, parent); err != nil {
		return err
	}
	if _, err := gitOutput(ctx, parent, "commit", "-m", "chore: configure AO workspace ignores", "--", ".gitignore"); err != nil {
		return apierr.Invalid("WORKSPACE_PARENT_COMMIT_FAILED", "Failed to commit workspace parent .gitignore", map[string]any{"error": err.Error()})
	}
	return nil
}

func initWorkspaceParent(ctx context.Context, parent string, repos []domain.WorkspaceRepoRecord) (retErr error) {
	// Snapshot the original .gitignore so we can restore it on failure.
	// If the file doesn't exist, originalGitignore is nil.
	gitignorePath := filepath.Join(parent, ".gitignore")
	originalGitignore, readErr := os.ReadFile(gitignorePath)
	gitignoreExisted := readErr == nil

	if _, err := gitOutput(ctx, parent, "init", "-b", domain.DefaultBranchName); err != nil {
		return apierr.Invalid("WORKSPACE_PARENT_INIT_FAILED", "Failed to initialize workspace parent git repository", map[string]any{"error": err.Error()})
	}

	// Rollback helper: remove the .git dir we just created and restore the
	// original .gitignore state. Only runs when we return an error after init.
	defer func() {
		if retErr == nil {
			return
		}
		_ = os.RemoveAll(filepath.Join(parent, ".git"))
		if gitignoreExisted {
			_ = os.WriteFile(gitignorePath, originalGitignore, 0o600)
		} else {
			_ = os.Remove(gitignorePath)
		}
	}()

	if _, err := ensureWorkspaceGitignore(parent, repos); err != nil {
		return apierr.Invalid("WORKSPACE_PARENT_GITIGNORE_FAILED", "Failed to write workspace parent .gitignore", map[string]any{"error": err.Error()})
	}
	if _, err := gitOutput(ctx, parent, "add", "-A"); err != nil {
		return apierr.Invalid("WORKSPACE_PARENT_ADD_FAILED", "Failed to stage workspace parent files", map[string]any{"error": err.Error()})
	}
	if err := guardNoGitlinks(ctx, parent); err != nil {
		return err
	}
	if _, err := gitOutput(ctx, parent, "commit", "-m", "chore: initialize AO workspace root"); err != nil {
		return apierr.Invalid("WORKSPACE_PARENT_COMMIT_FAILED", "Failed to create workspace parent initial commit", map[string]any{"error": err.Error()})
	}
	return nil
}

func ensureWorkspaceGitignore(parent string, repos []domain.WorkspaceRepoRecord) (bool, error) {
	path := filepath.Join(parent, ".gitignore")
	seen := map[string]bool{}
	var lines []string
	if data, err := os.ReadFile(path); err == nil {
		s := bufio.NewScanner(strings.NewReader(string(data)))
		for s.Scan() {
			line := s.Text()
			lines = append(lines, line)
			seen[strings.TrimSpace(line)] = true
		}
		if err := s.Err(); err != nil {
			return false, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}

	var additions []string
	for _, repo := range repos {
		additions = append(additions, "/"+filepath.ToSlash(repo.RelativePath)+"/")
	}
	additions = append(additions, workspaceRootIgnoreDenylist...)
	changed := false
	for _, entry := range additions {
		if seen[entry] {
			continue
		}
		lines = append(lines, entry)
		seen[entry] = true
		changed = true
	}
	if !changed {
		return false, nil
	}
	content := strings.Join(lines, "\n")
	if content != "" && !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	return true, os.WriteFile(path, []byte(content), 0o600)
}

func guardNoGitlinks(ctx context.Context, repo string) error {
	out, err := gitOutput(ctx, repo, "ls-files", "-s")
	if err != nil {
		return apierr.Invalid("WORKSPACE_PARENT_INDEX_FAILED", "Failed to inspect workspace parent index", map[string]any{"error": err.Error()})
	}
	var paths []string
	s := bufio.NewScanner(strings.NewReader(out))
	for s.Scan() {
		line := s.Text()
		if strings.HasPrefix(line, "160000 ") {
			_, path, _ := strings.Cut(line, "\t")
			paths = append(paths, path)
		}
	}
	if err := s.Err(); err != nil {
		return apierr.Invalid("WORKSPACE_PARENT_INDEX_FAILED", "Failed to inspect workspace parent index", map[string]any{"error": err.Error()})
	}
	if len(paths) > 0 {
		return apierr.Invalid("WORKSPACE_PARENT_GITLINK",
			"Workspace parent index contains embedded gitlinks; child repos must be gitignored before committing",
			map[string]any{
				"paths": paths,
				"suggestedFix": fmt.Sprintf(
					"Run `git rm --cached %s` for each listed path and add them to .gitignore, or remove nested repositories not directly under the workspace root.",
					strings.Join(paths, " "),
				),
			})
	}
	return nil
}

func workspaceReposFromRecords(records []domain.WorkspaceRepoRecord) []WorkspaceRepo {
	out := make([]WorkspaceRepo, 0, len(records))
	for _, rec := range records {
		out = append(out, WorkspaceRepo{
			Name:          rec.Name,
			RelativePath:  rec.RelativePath,
			Repo:          rec.RepoOriginURL,
			DefaultBranch: rec.DefaultBranch,
		})
	}
	return out
}

func gitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := aoprocess.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git -C %s %s: %w: %s", dir, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}
