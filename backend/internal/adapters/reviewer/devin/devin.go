// Package devin adapts Devin for Terminal as an experimental host-trusted AO
// reviewer. It always runs Devin's visible, long-lived interactive TUI.
package devin

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/agentbase"
	workerdevin "github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/devin"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// HostTrustWarning describes the authority retained by Devin's interactive
// terminal. AO deliberately does not add Devin's --sandbox flag.
const HostTrustWarning = "experimental host-trusted reviewer: Devin runs its interactive TUI without OS isolation; terminal users can change modes, invoke shell features, or open an external editor"

// Reviewer builds Devin's persistent interactive reviewer command.
type Reviewer struct {
	agentbase.Base
	resolveBinary func(context.Context) (string, error)
}

// New returns the production Devin reviewer adapter.
func New(discovery ...ports.AgentBinaryDiscovery) *Reviewer {
	r := &Reviewer{resolveBinary: workerdevin.ResolveDevinBinary}
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

// Harness returns Devin's reviewer identity.
func (*Reviewer) Harness() domain.ReviewerHarness { return domain.ReviewerDevin }

var _ ports.Reviewer = (*Reviewer)(nil)
var _ ports.ReviewerCanceller = (*Reviewer)(nil)
var _ ports.ReviewerPromptReadinessProvider = (*Reviewer)(nil)

// ReviewCommand starts only Devin's normal interactive TUI. The initial task
// is injected after the pane starts; print mode, prompt argv, ACP, cloud
// handoff, and sandbox mode are never emitted.
func (r *Reviewer) ReviewCommand(ctx context.Context, inv ports.ReviewInvocation) (ports.ReviewCommandSpec, error) {
	if err := ctx.Err(); err != nil {
		return ports.ReviewCommandSpec{}, err
	}
	binary, err := r.resolveBinary(ctx)
	if err != nil {
		return ports.ReviewCommandSpec{}, err
	}
	if !filepath.IsAbs(binary) {
		return ports.ReviewCommandSpec{}, errors.New("devin reviewer: resolved binary must be absolute")
	}
	message, err := initialMessage(inv)
	if err != nil {
		return ports.ReviewCommandSpec{}, err
	}
	return ports.ReviewCommandSpec{
		Argv:           []string{binary, "--permission-mode", "auto"},
		InitialMessage: message,
	}, nil
}

// ReviewRestoreCommand restores a recorded Devin reviewer pane by relaunching
// the reviewer command with the current task context.
func (r *Reviewer) ReviewRestoreCommand(ctx context.Context, inv ports.ReviewInvocation) (ports.ReviewCommandSpec, bool, error) {
	cmd, err := r.ReviewCommand(ctx, inv)
	return cmd, true, err
}

// ReviewPromptReadinessHints gives Devin's startup screen enough time to accept
// pasted task input before AO submits the initial review reference.
func (*Reviewer) ReviewPromptReadinessHints(ctx context.Context) (ports.PromptReadinessHints, error) {
	if err := ctx.Err(); err != nil {
		return ports.PromptReadinessHints{}, err
	}
	return ports.PromptReadinessHints{InitialDelay: 2 * time.Second}, nil
}

func initialMessage(inv ports.ReviewInvocation) (string, error) {
	// Preflight builds the command without request-scoped prompt files.
	if strings.TrimSpace(inv.TaskPromptRoot) == "" {
		return "", nil
	}
	if strings.TrimSpace(inv.SystemPromptFile) == "" {
		return "", errors.New("devin reviewer: system prompt file is required")
	}
	return fmt.Sprintf(
		"Read and follow the AO reviewer role in `%s`, then %s",
		filepath.ToSlash(inv.SystemPromptFile),
		strings.TrimSpace(inv.Prompt),
	), nil
}

// ReviewMessage reuses the same Devin TUI for subsequent review passes.
func (*Reviewer) ReviewMessage(ctx context.Context, inv ports.ReviewInvocation) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return inv.Prompt, nil
}

// ReviewCancel sends Devin's active-operation Ctrl-C once, preserving the pane.
func (*Reviewer) ReviewCancel(ctx context.Context) (ports.ReviewCancelSpec, error) {
	if err := ctx.Err(); err != nil {
		return ports.ReviewCancelSpec{}, err
	}
	return ports.ReviewCancelSpec{Mode: ports.ReviewCancelInterrupt, Interrupts: 1}, nil
}
