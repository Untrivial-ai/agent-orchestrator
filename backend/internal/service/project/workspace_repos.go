package project

import (
	"context"
	"errors"
	"fmt"
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
	for _, repo := range existing {
		if repo.Name == name {
			return Project{}, apierr.Conflict("REPO_ALREADY_REGISTERED", "A child repository with this name is already registered", map[string]any{
				"existingRepo": repo.Name,
				"suggestedFix": fmt.Sprintf("Run `ao project repo rm --project %s %s` first, or attach with `--name` to register under a different name.", row.ID, name),
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
	changed, err := ensureWorkspaceGitignore(row.Path, []domain.WorkspaceRepoRecord{rec})
	if err != nil {
		return Project{}, apierr.Invalid("WORKSPACE_PARENT_GITIGNORE_FAILED", "Failed to update workspace parent .gitignore", map[string]any{"error": err.Error()})
	}
	if err := commitWorkspaceGitignore(ctx, row.Path, changed); err != nil {
		return Project{}, err
	}
	return m.workspaceProjectFromRow(ctx, row)
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
	removed, err := m.store.DeleteWorkspaceRepo(ctx, row.ID, name)
	if err != nil {
		return Project{}, apierr.Internal("WORKSPACE_REPO_REMOVE_FAILED", "Failed to remove workspace child repository")
	}
	if !removed {
		return Project{}, apierr.NotFound("REPO_NOT_FOUND", fmt.Sprintf("Unknown child repository %q", name))
	}
	if deleteFiles {
		childPath := filepath.Join(row.Path, filepath.FromSlash(target.RelativePath))
		if err := ensurePathInside(childPath, row.Path); err != nil {
			return Project{}, err
		}
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
