// Package muse adapts the Muse Code worker agent for code-review sessions.
package muse

import (
	"context"

	workermuse "github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/muse"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/reviewer/agentrestore"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// Reviewer is the Muse Code reviewer adapter.
type Reviewer struct {
	agent ports.Agent
}

// New builds the Muse Code reviewer adapter.
func New() *Reviewer {
	return &Reviewer{agent: workermuse.New()}
}

// Harness identifies this reviewer in the reviewer registry.
func (r *Reviewer) Harness() domain.ReviewerHarness {
	return domain.ReviewerMuse
}

var _ ports.Reviewer = (*Reviewer)(nil)
var _ ports.ReviewerCanceller = (*Reviewer)(nil)
var _ ports.ReviewerRestorer = (*Reviewer)(nil)

// ReviewCommand launches Muse's interactive TUI with writes disabled. Muse
// supplies its own sandbox and approval machinery; AO keeps the reviewer in
// auto approval so read/report commands can run unattended while --disable-write
// prevents non-shell workspace file writes.
func (r *Reviewer) ReviewCommand(ctx context.Context, inv ports.ReviewInvocation) (ports.ReviewCommandSpec, error) {
	argv, err := r.agent.GetLaunchCommand(ctx, ports.LaunchConfig{
		Config:           inv.Config,
		SessionID:        inv.ReviewerID,
		WorkspacePath:    inv.WorkspacePath,
		Prompt:           inv.Prompt,
		SystemPrompt:     inv.SystemPrompt,
		SystemPromptFile: inv.SystemPromptFile,
		Permissions:      ports.PermissionModeAuto,
		DataDir:          inv.DataDir,
	})
	if err != nil {
		return ports.ReviewCommandSpec{}, err
	}
	return ports.ReviewCommandSpec{Argv: addNoWriteFlag(argv, inv.Prompt != "")}, nil
}

// ReviewRestoreCommand resumes the reviewer Muse conversation captured from
// hooks, reapplying the same no-write reviewer launch flag.
func (r *Reviewer) ReviewRestoreCommand(ctx context.Context, inv ports.ReviewInvocation) (ports.ReviewCommandSpec, bool, error) {
	cmd, ok, err := agentrestore.Command(ctx, r.agent, inv, agentrestore.Options{Permissions: ports.PermissionModeAuto})
	if err != nil || !ok {
		return cmd, ok, err
	}
	cmd.Argv = addNoWriteFlag(cmd.Argv, false)
	return cmd, true, nil
}

// ReviewMessage returns AO's centrally-authored review task for an existing
// Muse pane.
func (*Reviewer) ReviewMessage(_ context.Context, inv ports.ReviewInvocation) (string, error) {
	return inv.Prompt, nil
}

// ReviewCancel stops the active Muse turn while preserving the terminal pane.
func (*Reviewer) ReviewCancel(context.Context) (ports.ReviewCancelSpec, error) {
	return ports.ReviewCancelSpec{Mode: ports.ReviewCancelInput, Input: "\x1b"}, nil
}

func addNoWriteFlag(argv []string, hasPrompt bool) []string {
	return insertBeforeMuseOperand(argv, hasPrompt, "--disable-write")
}

func insertBeforeMuseOperand(argv []string, hasPrompt bool, extra ...string) []string {
	if len(argv) == 0 {
		return append([]string(nil), extra...)
	}
	insertAt := len(argv)
	for i, arg := range argv {
		if arg == "resume" || arg == "exec" || arg == "export" || arg == "trace" || arg == "skills" || arg == "sandbox" || arg == "session-message" || arg == "auth" || arg == "login" || arg == "logout" || arg == "init" {
			insertAt = i
			break
		}
	}
	if insertAt == len(argv) && hasPrompt {
		insertAt = len(argv) - 1
	}
	out := make([]string, 0, len(argv)+len(extra))
	out = append(out, argv[:insertAt]...)
	out = append(out, extra...)
	return append(out, argv[insertAt:]...)
}
