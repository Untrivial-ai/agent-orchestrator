// Package agentrestore contains shared restore plumbing for reviewer adapters
// that wrap an AO agent adapter.
package agentrestore

import (
	"context"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// Options carries reviewer policy that must be reapplied when resuming a native
// agent conversation.
type Options struct {
	Permissions     ports.PermissionMode
	AllowedTools    []string
	DisallowedTools []string
}

// Command asks the wrapped agent adapter to resume the reviewer's native
// conversation. Adapters that can derive a legacy native id from Session.ID
// still get the call when no hook-captured id is present.
func Command(ctx context.Context, agent ports.Agent, inv ports.ReviewInvocation, opts Options) (ports.ReviewCommandSpec, bool, error) {
	agentSessionID := strings.TrimSpace(inv.AgentSessionID)
	prompt := ""
	if strings.TrimSpace(inv.RunID) != "" {
		// A dead reviewer relaunched for an active run must receive that run's
		// task as the resume-time user turn. Idle terminal restoration leaves
		// the existing conversation untouched until the next review trigger.
		prompt = inv.Prompt
	}
	metadata := map[string]string{}
	if agentSessionID != "" {
		metadata[ports.MetadataKeyAgentSessionID] = agentSessionID
	}
	argv, ok, err := agent.GetRestoreCommand(ctx, ports.RestoreConfig{
		Config: inv.Config,
		Session: ports.SessionRef{
			ID:            inv.ReviewerID,
			WorkspacePath: inv.WorkspacePath,
			Metadata:      metadata,
		},
		Kind:             domain.KindWorker,
		DataDir:          inv.DataDir,
		Permissions:      opts.Permissions,
		AllowedTools:     opts.AllowedTools,
		DisallowedTools:  opts.DisallowedTools,
		Prompt:           prompt,
		SystemPrompt:     inv.SystemPrompt,
		SystemPromptFile: inv.SystemPromptFile,
	})
	if err != nil || !ok {
		return ports.ReviewCommandSpec{}, ok, err
	}
	return ports.ReviewCommandSpec{
		Argv:           argv,
		AgentSessionID: strings.TrimSpace(inv.AgentSessionID),
		NativeResumed:  true,
	}, true, nil
}
