package session

import (
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
	"github.com/aoagents/agent-orchestrator/backend/internal/service/editor"
)

// maxEditorFiles bounds how many files a single open request may focus. Only
// an explicit path is ever passed today, so this is a guard, not a policy.
const maxEditorFiles = 1

// EditorInfo is one editor AO can launch on this machine.
type EditorInfo struct {
	ID   string
	Name string
}

// OpenEditorRequest asks for a session's workspace to be opened in an editor.
type OpenEditorRequest struct {
	// EditorID names a detected editor. Empty takes the first one found.
	EditorID string
	// Path is a workspace-relative file to focus. Empty auto-picks the file the
	// session most recently changed; "." forces folder-only.
	Path string
}

// OpenEditorResult reports what was opened, for the UI's confirmation copy. It
// deliberately carries no absolute path.
type OpenEditorResult struct {
	EditorID   string
	EditorName string
	// File is the workspace-relative file that was focused, empty when only the
	// folder was opened.
	File string
	// Scope is "workspace" for a session worktree, "project" when the session
	// has no live worktree and the project checkout was opened instead.
	Scope string
}

// ListEditors returns the editors installed on this machine, in AO's
// preference order.
func (s *Service) ListEditors(_ context.Context) ([]EditorInfo, error) {
	found := editor.Detect()
	out := make([]EditorInfo, 0, len(found))
	for _, ed := range found {
		out = append(out, EditorInfo{ID: ed.ID, Name: ed.Name})
	}
	return out, nil
}

// OpenInEditor launches an external editor on a session's workspace.
//
// Target resolution, in order:
//  1. The session's live worktree, focusing the requested file, or — when none
//     was requested — the changed file with the newest mtime, so clicking the
//     button on a session that was "fixing the download button" lands on the
//     file that work touched.
//  2. The session's project checkout with no file focused, when the session has
//     no worktree on disk (never spawned, or already cleaned up).
func (s *Service) OpenInEditor(ctx context.Context, id domain.SessionID, req OpenEditorRequest) (OpenEditorResult, error) {
	root, scope, err := s.editorRoot(ctx, id)
	if err != nil {
		return OpenEditorResult{}, err
	}

	ed, err := editor.Resolve(strings.TrimSpace(req.EditorID))
	if err != nil {
		return OpenEditorResult{}, editorResolveError(err, req.EditorID)
	}

	rel, abs, err := s.editorTargetFile(ctx, root, scope, req.Path)
	if err != nil {
		return OpenEditorResult{}, err
	}

	files := make([]string, 0, maxEditorFiles)
	if abs != "" {
		files = append(files, abs)
	}
	if err := editor.Open(ed, root, files...); err != nil {
		return OpenEditorResult{}, apierr.Internal("EDITOR_LAUNCH_FAILED", fmt.Sprintf("Could not launch %s", ed.Name))
	}
	return OpenEditorResult{EditorID: ed.ID, EditorName: ed.Name, File: rel, Scope: scope}, nil
}

// editorRoot picks the directory to open: the session worktree when it exists
// on disk, otherwise the project checkout.
func (s *Service) editorRoot(ctx context.Context, id domain.SessionID) (string, string, error) {
	rec, ok, err := s.store.GetSession(ctx, id)
	if err != nil {
		return "", "", fmt.Errorf("get %s: %w", id, err)
	}
	if !ok {
		return "", "", apierr.NotFound("SESSION_NOT_FOUND", "Unknown session")
	}
	if dir := strings.TrimSpace(rec.Metadata.WorkspacePath); dir != "" && isDir(dir) {
		return dir, "workspace", nil
	}
	if rec.ProjectID == "" {
		return "", "", apierr.NotFound("SESSION_WORKSPACE_NOT_FOUND", "Session workspace not found")
	}
	project, ok, err := s.store.GetProject(ctx, string(rec.ProjectID))
	if err != nil {
		return "", "", fmt.Errorf("get project %s: %w", rec.ProjectID, err)
	}
	if !ok || !isDir(strings.TrimSpace(project.Path)) {
		return "", "", apierr.NotFound("SESSION_WORKSPACE_NOT_FOUND", "Session workspace not found")
	}
	return strings.TrimSpace(project.Path), "project", nil
}

// editorTargetFile resolves the file to focus as (workspace-relative path,
// absolute path). Both are "" when only the folder should open.
func (s *Service) editorTargetFile(ctx context.Context, root, scope, rawPath string) (string, string, error) {
	trimmed := strings.TrimSpace(rawPath)
	// "." is the explicit "just open the folder" request from the dropdown.
	if trimmed == "." {
		return "", "", nil
	}
	if trimmed != "" {
		// confinedWorkspaceFile rejects traversal and symlink escapes, and
		// returns the resolved absolute path.
		rel, err := cleanWorkspaceRelativePath(trimmed)
		if err != nil {
			return "", "", err
		}
		abs, _, err := confinedWorkspaceFile(root, rel)
		if err != nil {
			return "", "", err
		}
		return rel, abs, nil
	}
	// Falling back to the project checkout means there is no session work to
	// point at, so the editor opens with no files (as requested).
	if scope != "workspace" {
		return "", "", nil
	}
	rel := mostRecentlyChangedFile(ctx, root)
	if rel == "" {
		return "", "", nil
	}
	// Route the auto-picked file through the same confinement explicit paths
	// get: an untracked symlink can be the newest "change" the worktree scan
	// sees, and following it unchecked would hand the editor a path outside
	// the session workspace.
	abs, _, confineErr := confinedWorkspaceFile(root, rel)
	if confineErr != nil {
		return "", "", nil //nolint:nilerr // an escaping auto-pick degrades to "nothing changed", not a failed open
	}
	return rel, abs, nil
}

// mostRecentlyChangedFile returns the worktree file with a git change against
// HEAD and the newest mtime, or "" when there is nothing to focus. Deleted
// paths are skipped (there is nothing to open), and a clean worktree or a
// non-git one (scratch session) yields "" so the folder opens alone — neither
// is an error worth failing the open on.
func mostRecentlyChangedFile(ctx context.Context, root string) string {
	changes, err := workspaceChangeMaps(ctx, root, "HEAD")
	if err != nil {
		return ""
	}
	type entry struct {
		rel   string
		mtime time.Time
	}
	changed := make([]entry, 0, len(changes.statuses))
	for rel, status := range changes.statuses {
		if status == WorkspaceFileDeleted || status == WorkspaceFileUnmodified {
			continue
		}
		info, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil || info.IsDir() {
			continue
		}
		changed = append(changed, entry{rel: rel, mtime: info.ModTime()})
	}
	if len(changed) == 0 {
		return ""
	}
	// Path breaks mtime ties so the pick is stable across calls (bulk edits and
	// coarse filesystem timestamps make ties common).
	sort.Slice(changed, func(i, j int) bool {
		if changed[i].mtime.Equal(changed[j].mtime) {
			return changed[i].rel < changed[j].rel
		}
		return changed[i].mtime.After(changed[j].mtime)
	})
	return changed[0].rel
}

func editorResolveError(err error, requested string) error {
	if errors.Is(err, editor.ErrUnknownEditor) && strings.TrimSpace(requested) != "" {
		return apierr.Invalid("EDITOR_NOT_AVAILABLE", "That editor is not installed", nil)
	}
	return apierr.Invalid("NO_EDITOR_FOUND", "No supported editor found. Install VS Code, Cursor, Windsurf, or Zed and make sure its command-line launcher is on PATH.", nil)
}

func isDir(path string) bool {
	if path == "" {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
