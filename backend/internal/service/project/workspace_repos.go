package project

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
)

// AddWorkspaceRepo attaches one child repository, already on disk under the
// workspace root, to the project's registry. The next spawn worktrees the
// child because the session manager reads the same registry at spawn time.
//
// The whole method body is serialised by addMu for the same reason as Add:
// .gitignore writes and parent commits between the duplicate check and the
// store write must be atomic from the perspective of concurrent callers.
func (m *Service) AddWorkspaceRepo(ctx context.Context, id domain.ProjectID, in AddWorkspaceRepoInput) (Project, error) {
	if err := validateProjectID(id); err != nil {
		return Project{}, err
	}
	childPath, err := normalizePath(in.Path)
	if err != nil {
		return Project{}, err
	}

	m.addMu.Lock()
	defer m.addMu.Unlock()

	row, ok, err := m.store.GetProject(ctx, string(id))
	if err != nil {
		return Project{}, apierr.Internal("PROJECT_LOAD_FAILED", "Failed to load project")
	}
	if !ok || !row.ArchivedAt.IsZero() {
		return Project{}, apierr.NotFound("PROJECT_NOT_FOUND", "Unknown project")
	}
	if row.Kind.WithDefault() != domain.ProjectKindWorkspace {
		return Project{}, apierr.Invalid("NOT_A_WORKSPACE_PROJECT", "Only workspace projects have child repositories", map[string]any{
			"kind":         string(row.Kind.WithDefault()),
			"suggestedFix": "Register a workspace with `ao project add --path <parent> --as-workspace`, then attach the child to it.",
		})
	}
	if err := ensureDirectoryPath(childPath); err != nil {
		return Project{}, err
	}
	rel, err := workspaceChildRelativePath(row.Path, childPath)
	if err != nil {
		return Project{}, err
	}
	name := strings.TrimSpace(firstNonEmpty(ptrValue(in.Name), filepath.Base(childPath)))
	if err := validateWorkspaceRepoName(childPath, name); err != nil {
		return Project{}, err
	}
	existing, err := m.store.ListWorkspaceRepos(ctx, row.ID)
	if err != nil {
		return Project{}, apierr.Internal("PROJECT_LOAD_FAILED", "Failed to load workspace repositories")
	}
	relSlash := filepath.ToSlash(rel)
	for _, repo := range existing {
		if repo.Name == name {
			return Project{}, apierr.Conflict("REPO_ALREADY_REGISTERED", "A child repository with this name is already registered", map[string]any{
				"existingRepo": repo.Name,
				"suggestedFix": fmt.Sprintf("Run `ao project repo rm --project %s %s` first, or attach with `--name` to register under a different name.", row.ID, name),
			})
		}
		// The store also enforces UNIQUE (project_id, relative_path): the
		// same directory under a different --name must be the same stable
		// 409 here instead of a 500 constraint failure from the upsert.
		if repo.RelativePath == relSlash {
			return Project{}, apierr.Conflict("REPO_ALREADY_REGISTERED", "This directory is already registered under a different name", map[string]any{
				"existingRepo": repo.Name,
				"path":         childPath,
				"suggestedFix": fmt.Sprintf("Run `ao project repo rm --project %s %s` first, or attach a different directory.", row.ID, repo.Name),
			})
		}
	}

	rec := domain.WorkspaceRepoRecord{
		ProjectID:    domain.ProjectID(row.ID),
		Name:         name,
		RelativePath: filepath.ToSlash(rel),
		RegisteredAt: m.clock().UTC(),
	}
	if !isGitRepo(childPath) {
		rec.GitStatus = domain.GitStatusNeedsInit
	} else if vErr := validateWorkspaceChild(ctx, childPath); vErr != nil {
		var apiErr *apierr.Error
		if errors.As(vErr, &apiErr) && (apiErr.Code == "WORKSPACE_CHILD_ORIGIN_REQUIRED" || apiErr.Code == "WORKSPACE_CHILD_UNBORN" || apiErr.Code == "WORKSPACE_CHILD_IS_WORKTREE") {
			rec.GitStatus = domain.GitStatusNeedsInit
		} else {
			return Project{}, vErr
		}
	} else {
		rec.RepoOriginURL = resolveGitOriginURL(childPath)
		rec.DefaultBranch = strings.TrimSpace(ptrValue(in.DefaultBranch))
		if rec.DefaultBranch == "" {
			rec.DefaultBranch = resolveWorkspaceChildDefaultBranch(ctx, childPath)
		}
		rec.GitStatus = domain.GitStatusReady
	}
	if err := m.store.UpsertWorkspaceRepo(ctx, rec); err != nil {
		return Project{}, apierr.Internal("WORKSPACE_REPO_ADD_FAILED", "Failed to register workspace child repository")
	}
	return m.finishWorkspaceRepoAttach(ctx, row, rec, snapshotWorkspaceAttachState(ctx, row.Path))
}

// workspaceAttachSnapshot captures the parent Git state before attach mutates
// it, so a later failure can compensate.
type workspaceAttachSnapshot struct {
	gitignore        []byte
	gitignoreExisted bool
	gitignoreMissing bool
	gitignoreMode    fs.FileMode
	// head is the parent HEAD oid before mutation, or "" when the parent has
	// no commit yet (or HEAD could not be read).
	head string
}

func snapshotWorkspaceAttachState(ctx context.Context, parent string) workspaceAttachSnapshot {
	var snap workspaceAttachSnapshot
	gitignorePath := filepath.Join(parent, ".gitignore")
	data, err := os.ReadFile(gitignorePath)
	switch {
	case err == nil:
		snap.gitignore = data
		snap.gitignoreExisted = true
		snap.gitignoreMode = 0o600
		if info, statErr := os.Stat(gitignorePath); statErr == nil {
			snap.gitignoreMode = info.Mode().Perm()
		}
	case errors.Is(err, os.ErrNotExist):
		snap.gitignoreMissing = true
		// Any other read error (a directory at the path, permissions) leaves both
		// flags false: the failed step wrote nothing, so rollback must not touch
		// the path either.
	}
	if head, err := gitOutput(ctx, parent, "rev-parse", "HEAD"); err == nil {
		snap.head = strings.TrimSpace(head)
	}
	return snap
}

// finishWorkspaceRepoAttach performs the post-registry steps of attach: the
// .gitignore write, the parent commit, and the read-model load. Any failure
// rolls the registry row back out plus restores the parent Git state, so the
// API reports the failure with nothing durable left behind and a retry does
// not trip REPO_ALREADY_REGISTERED.
func (m *Service) finishWorkspaceRepoAttach(ctx context.Context, row domain.ProjectRecord, rec domain.WorkspaceRepoRecord, snap workspaceAttachSnapshot) (proj Project, retErr error) {
	var changed, committed bool
	defer func() {
		if retErr != nil {
			m.rollbackWorkspaceRepoAttach(ctx, row, rec, snap, changed, committed)
		}
	}()
	var err error
	changed, err = ensureWorkspaceGitignore(row.Path, []domain.WorkspaceRepoRecord{rec})
	if err != nil {
		return Project{}, apierr.Invalid("WORKSPACE_PARENT_GITIGNORE_FAILED", "Failed to update workspace parent .gitignore", map[string]any{"error": err.Error()})
	}
	committed, err = commitWorkspaceGitignore(ctx, row.Path, changed)
	if err != nil {
		return Project{}, err
	}
	return m.workspaceProjectFromRow(ctx, row)
}

// rollbackWorkspaceRepoAttach compensates a failed attach: the registry row
// is deleted, the .gitignore bytes are restored (or the file removed when it
// did not exist before), and a commit this attach created is undone. Every
// step is best effort — rollback must never mask the original error — and
// anything it cannot prove it owns is left alone.
func (m *Service) rollbackWorkspaceRepoAttach(ctx context.Context, row domain.ProjectRecord, rec domain.WorkspaceRepoRecord, snap workspaceAttachSnapshot, changed, committed bool) {
	if _, err := m.store.DeleteWorkspaceRepo(ctx, row.ID, rec.Name); err != nil {
		m.logger.Warn("project: attach rollback could not remove registry row", "project", row.ID, "repo", rec.Name, "error", err)
	}
	gitignorePath := filepath.Join(row.Path, ".gitignore")
	switch {
	case snap.gitignoreExisted:
		if err := os.WriteFile(gitignorePath, snap.gitignore, snap.gitignoreMode); err != nil {
			m.logger.Warn("project: attach rollback could not restore .gitignore", "project", row.ID, "error", err)
		}
	case snap.gitignoreMissing:
		if err := os.Remove(gitignorePath); err != nil && !errors.Is(err, os.ErrNotExist) {
			m.logger.Warn("project: attach rollback could not remove .gitignore", "project", row.ID, "error", err)
		}
	}
	m.rollbackWorkspaceRepoCommit(ctx, row.Path, snap, changed, committed)
}

func (m *Service) rollbackWorkspaceRepoCommit(ctx context.Context, parent string, snap workspaceAttachSnapshot, changed, committed bool) {
	// Drop the `.gitignore` staging our `git add` left behind so `git status`
	// does not carry the failed attach forward. Only the index entry is
	// reset; the worktree bytes were already restored above, so user content
	// is never touched. Skipped when we never wrote the file or HEAD is
	// unknown (reset needs a HEAD to reset to).
	unstage := func() {
		if !changed || snap.head == "" {
			return
		}
		if _, err := gitOutput(ctx, parent, "reset", "-q", "HEAD", "--", ".gitignore"); err != nil {
			m.logger.Warn("project: attach rollback could not unstage .gitignore", "path", parent, "error", err)
		}
	}
	if !committed {
		unstage()
		return
	}
	if snap.head == "" {
		// Our commit was the parent's initial commit; delete the ref to
		// restore the unborn state. The index keeps its entries, matching
		// the pre-commit staging.
		if _, err := gitOutput(ctx, parent, "update-ref", "-d", "HEAD"); err != nil {
			m.logger.Warn("project: attach rollback could not unborn HEAD", "path", parent, "error", err)
		}
		return
	}
	// Our commit sits on top of the recorded HEAD. Verify that before
	// resetting so a concurrent external commit is never destroyed.
	if parentOfHead, err := gitOutput(ctx, parent, "rev-parse", "HEAD~1"); err != nil || strings.TrimSpace(parentOfHead) != snap.head {
		m.logger.Warn("project: attach rollback skipped commit undo; HEAD moved", "path", parent)
		unstage()
		return
	}
	if _, err := gitOutput(ctx, parent, "reset", "--soft", snap.head); err != nil {
		m.logger.Warn("project: attach rollback could not reset HEAD", "path", parent, "error", err)
		return
	}
	unstage()
}

// RemoveWorkspaceRepo drops one child repository from a workspace project's
// registry. Files on disk are left alone unless deleteFiles is true, in which
// case the child directory is removed. The root .gitignore entry is kept:
// re-attaching the same path needs no ignore rewrite, and the future repos
// sync reconciles stale entries.
func (m *Service) RemoveWorkspaceRepo(ctx context.Context, id domain.ProjectID, name string, deleteFiles bool) (Project, error) {
	if err := validateProjectID(id); err != nil {
		return Project{}, err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return Project{}, apierr.Invalid("REPO_NAME_REQUIRED", "Child repository name is required", nil)
	}

	m.addMu.Lock()
	defer m.addMu.Unlock()

	row, ok, err := m.store.GetProject(ctx, string(id))
	if err != nil {
		return Project{}, apierr.Internal("PROJECT_LOAD_FAILED", "Failed to load project")
	}
	if !ok || !row.ArchivedAt.IsZero() {
		return Project{}, apierr.NotFound("PROJECT_NOT_FOUND", "Unknown project")
	}
	if row.Kind.WithDefault() != domain.ProjectKindWorkspace {
		return Project{}, apierr.Invalid("NOT_A_WORKSPACE_PROJECT", "Only workspace projects have child repositories", map[string]any{
			"kind": string(row.Kind.WithDefault()),
		})
	}
	var target *domain.WorkspaceRepoRecord
	repos, err := m.store.ListWorkspaceRepos(ctx, row.ID)
	if err != nil {
		return Project{}, apierr.Internal("PROJECT_LOAD_FAILED", "Failed to load workspace repositories")
	}
	for i := range repos {
		if repos[i].Name == name {
			target = &repos[i]
			break
		}
	}
	if target == nil {
		return Project{}, apierr.NotFound("REPO_NOT_FOUND", fmt.Sprintf("Unknown child repository %q", name))
	}
	var childPath string
	if deleteFiles {
		childPath = filepath.Join(row.Path, filepath.FromSlash(target.RelativePath))
		if err := ensurePathInside(childPath, row.Path); err != nil {
			return Project{}, err
		}
		// The child checkout is the shared Git common directory every live
		// session child worktree was created from (`git worktree add`).
		// Deleting it while sessions still reference the repo would strand
		// those worktrees, so destructive detach refuses while live
		// references exist. The check runs before the registry delete so a
		// refusal leaves the registration untouched; registry-only detach
		// stays available.
		active, err := m.store.CountActiveSessionWorktreesForRepo(ctx, row.ID, target.Name)
		if err != nil {
			return Project{}, apierr.Internal("WORKSPACE_REPO_GUARD_FAILED", "Failed to check session worktrees before deleting child files")
		}
		if active > 0 {
			return Project{}, apierr.Conflict("REPO_IN_USE", fmt.Sprintf("Child repository %q is still referenced by %d live session worktree(s)", name, active), map[string]any{
				"repo":            name,
				"activeWorktrees": active,
				"suggestedFix":    "Stop or remove the sessions using this child repository first, or detach without --delete-files to keep the files on disk.",
			})
		}
	}
	removed, err := m.store.DeleteWorkspaceRepo(ctx, row.ID, name)
	if err != nil {
		return Project{}, apierr.Internal("WORKSPACE_REPO_REMOVE_FAILED", "Failed to remove workspace child repository")
	}
	if !removed {
		return Project{}, apierr.NotFound("REPO_NOT_FOUND", fmt.Sprintf("Unknown child repository %q", name))
	}
	if deleteFiles {
		if err := os.RemoveAll(childPath); err != nil {
			return Project{}, apierr.New(apierr.KindInternal, "REPO_FILES_DELETE_FAILED",
				fmt.Sprintf("The registry row was removed but the child directory %q could not be deleted: %s", childPath, err.Error()),
				map[string]any{"path": childPath})
		}
	}
	return m.workspaceProjectFromRow(ctx, row)
}

// workspaceProjectFromRow loads the child registry for a workspace row and
// returns the updated read-model shared by add/remove.
func (m *Service) workspaceProjectFromRow(ctx context.Context, row domain.ProjectRecord) (Project, error) {
	repos, err := m.store.ListWorkspaceRepos(ctx, row.ID)
	if err != nil {
		return Project{}, apierr.Internal("PROJECT_LOAD_FAILED", "Failed to load workspace repositories")
	}
	p := m.projectFromRow(ctx, row)
	p.WorkspaceRepos = workspaceReposFromRecords(row.Path, repos)
	return p, nil
}

// workspaceChildRelativePath resolves childPath against the workspace root,
// rejecting anything that escapes the root.
func workspaceChildRelativePath(parent, childPath string) (string, error) {
	rel, err := filepath.Rel(comparablePath(parent), comparablePath(childPath))
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", apierr.Invalid("REPO_OUTSIDE_WORKSPACE", "Child repository must be a directory directly or nested under the workspace root", map[string]any{
			"path":         childPath,
			"suggestedFix": "Move or clone the repository under the workspace folder, then retry.",
		})
	}
	return rel, nil
}

// ensurePathInside guards registry-driven filesystem removal against `..`
// traversal in stored relative paths.
func ensurePathInside(childPath, parent string) error {
	if _, err := workspaceChildRelativePath(parent, childPath); err != nil {
		return apierr.Invalid("REPO_PATH_UNSAFE", "Stored child repository path escapes the workspace root; refusing to touch files", map[string]any{
			"path": childPath,
		})
	}
	return nil
}

// validateWorkspaceRepoName rejects reserved, empty, and path-like child
// names before they reach the registry.
func validateWorkspaceRepoName(childPath, name string) error {
	if name == "" || name == "." || name == ".." || name == ".git" {
		return apierr.Invalid("INVALID_REPO_NAME", "Child repository name is not usable", map[string]any{"path": childPath})
	}
	if name == domain.RootWorkspaceRepoName {
		return apierr.Invalid("WORKSPACE_CHILD_RESERVED_NAME",
			"Child repository name is reserved for internal use",
			map[string]any{
				"path":         childPath,
				"suggestedFix": fmt.Sprintf("Attach with `--name` to register under a different name — %q is reserved by AO for the workspace root.", domain.RootWorkspaceRepoName),
			})
	}
	if strings.ContainsAny(name, `/\`) {
		return apierr.Invalid("INVALID_REPO_NAME", "Child repository name must be a single path segment", map[string]any{
			"path":         childPath,
			"suggestedFix": "Attach with `--name` to register under a single-segment name.",
		})
	}
	return nil
}

func ptrValue(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
