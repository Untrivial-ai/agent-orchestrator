// Package aider adapts Aider's normal interactive chat TUI for AO reviews.
package aider

import (
	"context"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/agentbase"
	workeraider "github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/aider"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// Reviewer builds Aider's interactive reviewer command.
type Reviewer struct {
	agentbase.Base
	resolveBinary func(context.Context) (string, error)
}

// New returns the production Aider reviewer adapter.
func New(discovery ...ports.AgentBinaryDiscovery) *Reviewer {
	r := &Reviewer{resolveBinary: workeraider.ResolveAiderBinary}
	if len(discovery) > 0 {
		r.SetBinaryDiscovery(discovery[0])
	}
	return r
}

// SetBinaryDiscovery injects the daemon-owned executable resolver.
func (r *Reviewer) SetBinaryDiscovery(d ports.AgentBinaryDiscovery) {
	r.Base.SetBinaryDiscovery(d)
	r.resolveBinary = func(ctx context.Context) (string, error) {
		v, e := d.Resolve(ctx, domain.AgentHarness(r.Harness()), ports.BinaryResolveLaunch)
		return v.Executable, e
	}
}

// Harness returns Aider's reviewer identity.
func (*Reviewer) Harness() domain.ReviewerHarness { return domain.ReviewerAider }

var _ ports.Reviewer = (*Reviewer)(nil)
var _ ports.ReviewerCanceller = (*Reviewer)(nil)
var _ ports.ReviewerReusePolicy = (*Reviewer)(nil)

// ReviewCommand launches Aider's interactive ask-mode TUI with normal manual
// confirmations. System and task files are loaded as read-only context; no
// --message, message-file, or unattended-approval flag is emitted.
func (r *Reviewer) ReviewCommand(ctx context.Context, inv ports.ReviewInvocation) (ports.ReviewCommandSpec, error) {
	binary, err := r.resolveBinary(ctx)
	if err != nil {
		return ports.ReviewCommandSpec{}, err
	}
	argv := []string{
		binary,
		"--chat-mode", "ask",
		"--dry-run",
		"--no-auto-commits",
		"--no-dirty-commits",
		"--no-gitignore",
		"--no-check-update",
		"--no-stream",
		"--no-pretty",
	}
	if inv.SystemPromptFile != "" {
		argv = append(argv, "--read", inv.SystemPromptFile)
	}
	if inv.TaskPromptFile != "" {
		argv = append(argv, "--read", inv.TaskPromptFile)
	}
	message := ""
	if inv.TaskPromptFile != "" {
		message = "Follow the AO reviewer role and review task loaded as read-only context."
	}
	return ports.ReviewCommandSpec{Argv: argv, InitialMessage: message}, nil
}

// ReviewRestoreCommand restores a recorded Aider reviewer pane by relaunching
// the request-scoped reviewer command with the current task context.
func (r *Reviewer) ReviewRestoreCommand(ctx context.Context, inv ports.ReviewInvocation) (ports.ReviewCommandSpec, bool, error) {
	cmd, err := r.ReviewCommand(ctx, inv)
	return cmd, true, err
}

// ReviewMessage returns the next AO-owned task reference.
func (*Reviewer) ReviewMessage(_ context.Context, inv ports.ReviewInvocation) (string, error) {
	return inv.Prompt, nil
}

// ReviewCancel interrupts Aider's current operation while preserving the pane.
func (*Reviewer) ReviewCancel(context.Context) (ports.ReviewCancelSpec, error) {
	return ports.ReviewCancelSpec{Mode: ports.ReviewCancelInterrupt, Interrupts: 2}, nil
}

// ReviewProcessReusable reports false because Aider cannot add future AO-owned
// task files to an existing --read context,
// so each pass opens a fresh interactive TUI rather than a one-shot process.
func (*Reviewer) ReviewProcessReusable() bool { return false }
