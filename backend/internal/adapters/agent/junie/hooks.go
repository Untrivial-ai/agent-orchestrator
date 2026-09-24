package junie

import (
	"context"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// GetAgentHooks prepares the same isolated config and optional exclusive
// guidelines overlay consumed by launch and restore. The config deliberately
// omits PermissionRequest because a zero-exit synchronous hook can approve and
// suppress Junie's native dialog.
func (p *Plugin) GetAgentHooks(ctx context.Context, cfg ports.WorkspaceHookConfig) error {
	_, err := p.fileBuilder().Prepare(ctx, RuntimeFileRequest{
		DataDir:          cfg.DataDir,
		SessionID:        cfg.SessionID,
		SystemPrompt:     cfg.SystemPrompt,
		SystemPromptFile: cfg.SystemPromptFile,
	})
	return err
}
