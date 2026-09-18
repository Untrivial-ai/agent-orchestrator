// Package crush adapts Crush as an experimental user-approved AO reviewer.
package crush

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/agentbase"
	workercrush "github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/crush"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// HostTrustWarning documents that Crush relies on its normal interactive
// approval flow rather than its broad --yolo mode.
const HostTrustWarning = "experimental user-approved reviewer: Crush uses normal permission prompts; AO deliberately does not enable --yolo"

// Reviewer builds Crush's persistent interactive reviewer command.
type Reviewer struct {
	agentbase.Base
	resolveBinary func(context.Context) (string, error)
}

// New returns the production Crush reviewer adapter.
func New(discovery ...ports.AgentBinaryDiscovery) *Reviewer {
	r := &Reviewer{resolveBinary: workercrush.ResolveCrushBinary}
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

// Harness returns Crush's reviewer identity.
func (*Reviewer) Harness() domain.ReviewerHarness { return domain.ReviewerCrush }

var _ ports.Reviewer = (*Reviewer)(nil)
var _ ports.ReviewerCanceller = (*Reviewer)(nil)
var _ ports.ReviewerPromptReadinessProvider = (*Reviewer)(nil)

// ReviewPromptReadinessHints reuses Crush's worker-TUI prompt markers.
func (*Reviewer) ReviewPromptReadinessHints(ctx context.Context) (ports.PromptReadinessHints, error) {
	return workercrush.New().PromptReadinessHints(ctx, ports.LaunchConfig{})
}

// ReviewCommand starts Crush's normal interactive TUI and injects the first
// task after startup. It writes no reviewer configuration into the checkout.
func (r *Reviewer) ReviewCommand(ctx context.Context, inv ports.ReviewInvocation) (ports.ReviewCommandSpec, error) {
	binary, err := r.resolveBinary(ctx)
	if err != nil {
		return ports.ReviewCommandSpec{}, err
	}
	if strings.TrimSpace(inv.TaskPromptRoot) == "" {
		return ports.ReviewCommandSpec{Argv: []string{binary}}, nil
	}
	message := fmt.Sprintf("Read and follow the AO reviewer role in `%s`, then %s", filepath.ToSlash(inv.SystemPromptFile), strings.TrimSpace(inv.Prompt))
	return ports.ReviewCommandSpec{Argv: []string{binary, "--cwd", inv.WorkspacePath}, InitialMessage: message}, nil
}

// ReviewRestoreCommand restores a recorded Crush reviewer pane by relaunching
// the reviewer command with the current task context.
func (r *Reviewer) ReviewRestoreCommand(ctx context.Context, inv ports.ReviewInvocation) (ports.ReviewCommandSpec, bool, error) {
	cmd, err := r.ReviewCommand(ctx, inv)
	return cmd, true, err
}

// ReviewMessage returns the next AO-owned task reference.
func (*Reviewer) ReviewMessage(_ context.Context, inv ports.ReviewInvocation) (string, error) {
	return inv.Prompt, nil
}

// ReviewCancel interrupts Crush's current operation while preserving the pane.
func (*Reviewer) ReviewCancel(context.Context) (ports.ReviewCancelSpec, error) {
	return ports.ReviewCancelSpec{Mode: ports.ReviewCancelInterrupt, Interrupts: 2}, nil
}
