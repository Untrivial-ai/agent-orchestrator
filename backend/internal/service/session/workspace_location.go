package session

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

	workspacePath := strings.TrimSpace(record.Metadata.WorkspacePath)
	if workspacePath == "" || !filepath.IsAbs(workspacePath) {
		return "", apierr.NotFound("SESSION_WORKSPACE_NOT_FOUND", "Session workspace is not available")
	}
	info, err := os.Stat(workspacePath)
	if errors.Is(err, os.ErrNotExist) || (err == nil && !info.IsDir()) {
		return "", apierr.NotFound("SESSION_WORKSPACE_NOT_FOUND", "Session workspace is not available")
	}
	if err != nil {
		return "", fmt.Errorf("stat session %s workspace: %w", id, err)
	}
	return filepath.Clean(workspacePath), nil
}
