package sessionmanager

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// prepareImportedWorkspace is called only by an explicit Resume agent action.
// Registering and reading imported history never owns or modifies a checkout.
func (m *Manager) prepareImportedWorkspace(ctx context.Context, rec domain.SessionRecord, project domain.ProjectRecord) (domain.SessionRecord, error) {
	started := time.Now()
	defer func() {
		m.logger.Debug("import workspace prepared", "session", rec.ID, "duration_ms", time.Since(started).Milliseconds())
	}()
	cfg := ports.SpawnConfig{ProjectID: rec.ProjectID, Kind: rec.Kind, Harness: rec.Harness,
		ResumeNativeSession: &ports.ResumeNativeSession{NativeSessionID: rec.Metadata.ProviderConversationID},
	}
	branch := m.importSpawnBranch(cfg, project, rec.ID)
	ws, workspaceProject, err := m.createSessionWorkspace(ctx, project, cfg, rec.ID, branch, m.refreshDefaultBranchesBestEffort(ctx, project, false))
	if err != nil {
		return rec, fmt.Errorf("prepare imported workspace: %w", err)
	}
	if err = m.provisionWorkspace(ctx, project, ws.Path); err == nil {
		rec.Metadata.WorkspacePath = ws.Path
		rec.Metadata.WorkspaceRepoPath = ws.RepoPath
		rec.Metadata.Branch = ws.Branch
		rec.UpdatedAt = m.clock()
		err = m.store.UpdateSession(ctx, rec)
	}
	if err != nil {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		m.destroySpawnWorkspace(cleanup, ws, workspaceProject)
		return rec, fmt.Errorf("prepare imported workspace: %w", err)
	}
	return rec, nil
}

func importedConfigDir(path string, harness domain.AgentHarness) string {
	for dir := filepath.Dir(path); dir != "." && dir != filepath.Dir(dir); dir = filepath.Dir(dir) {
		base := filepath.Base(dir)
		if (harness == domain.HarnessCodex && (base == "sessions" || base == "archived_sessions")) ||
			(harness == domain.HarnessClaudeCode && base == "projects") {
			return filepath.Dir(dir)
		}
	}
	return ""
}
