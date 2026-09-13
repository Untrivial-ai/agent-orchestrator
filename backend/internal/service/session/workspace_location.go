package session

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
)

// WorkspaceLocation returns the live workspace directory for a session. It is
// deliberately narrower than the normal session read model: Electron main is
// the only consumer, and the renderer must never receive this absolute path.
func (s *Service) WorkspaceLocation(ctx context.Context, id domain.SessionID) (string, error) {
	record, ok, err := s.store.GetSession(ctx, id)
	if err != nil {
		return "", fmt.Errorf("get session %s workspace: %w", id, err)
	}
	if !ok {
		return "", apierr.NotFound("SESSION_NOT_FOUND", "Unknown session")
	}

	// Orchestrators run directly in the project checkout and are never assigned
	// a per-session worktree, so Metadata.WorkspacePath is empty by design.
	// Their workspace location is the project's own path — without this branch
	// the desktop editor-handoff probe 404s and the topbar permanently flags
	// every orchestrator session as "Session workspace is not available".
	// Workers and legacy records with no kind keep the strict Metadata-only
	// contract below: a worker whose worktree vanished must surface as
	// unavailable, not silently redirect the editor to the project checkout.
	if record.Kind == domain.KindOrchestrator {
		project, ok, err := s.store.GetProject(ctx, string(record.ProjectID))
		if err != nil {
			return "", fmt.Errorf("get project %s workspace: %w", record.ProjectID, err)
		}
		if !ok {
			return "", apierr.NotFound("SESSION_WORKSPACE_NOT_FOUND", "Session workspace is not available")
		}
		return workspaceDir(project.Path)
	}

	return workspaceDir(record.Metadata.WorkspacePath)
}

// workspaceDir validates path as an existing absolute directory and returns it
// cleaned; anything else is reported as "not available" rather than guessed at.
func workspaceDir(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" || !filepath.IsAbs(path) {
		return "", apierr.NotFound("SESSION_WORKSPACE_NOT_FOUND", "Session workspace is not available")
	}
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return "", apierr.NotFound("SESSION_WORKSPACE_NOT_FOUND", "Session workspace is not available")
	}
	return filepath.Clean(path), nil
}
