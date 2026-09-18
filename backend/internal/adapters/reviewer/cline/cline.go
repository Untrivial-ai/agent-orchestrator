// Package cline adapts Cline as an experimental user-approved AO reviewer.
package cline

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/agentbase"
	workercline "github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/cline"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// HostTrustWarning documents that Cline retains its normal approval flow. AO
// deliberately avoids --auto-approve and --yolo for reviews.
const HostTrustWarning = "experimental user-approved reviewer: Cline uses normal permission prompts; AO does not enable --auto-approve or --yolo"

// Reviewer builds Cline's persistent interactive reviewer command.
type Reviewer struct {
	agentbase.Base
	resolveBinary func(context.Context) (string, error)
}

// New returns the production Cline reviewer adapter.
func New(discovery ...ports.AgentBinaryDiscovery) *Reviewer {
	r := &Reviewer{resolveBinary: workercline.ResolveClineBinary}
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

// Harness returns Cline's reviewer identity.
func (*Reviewer) Harness() domain.ReviewerHarness { return domain.ReviewerCline }

var _ ports.Reviewer = (*Reviewer)(nil)
var _ ports.ReviewerCanceller = (*Reviewer)(nil)
var _ ports.ReviewerPromptReadinessProvider = (*Reviewer)(nil)

// ReviewPromptReadinessHints reuses Cline's worker-TUI prompt markers.
func (*Reviewer) ReviewPromptReadinessHints(ctx context.Context) (ports.PromptReadinessHints, error) {
	return workercline.New().PromptReadinessHints(ctx, ports.LaunchConfig{})
}

// ReviewCommand starts Cline's normal interactive TUI, injects AO's role using
// Cline's native system-prompt flag, and sends the task after startup.
func (r *Reviewer) ReviewCommand(ctx context.Context, inv ports.ReviewInvocation) (ports.ReviewCommandSpec, error) {
	binary, err := r.resolveBinary(ctx)
	if err != nil {
		return ports.ReviewCommandSpec{}, err
	}
	if strings.TrimSpace(inv.TaskPromptRoot) == "" {
		return ports.ReviewCommandSpec{Argv: []string{binary}}, nil
	}
	systemPrompt, err := os.ReadFile(inv.SystemPromptFile) //nolint:gosec // AO-owned prompt path
	if err != nil {
		return ports.ReviewCommandSpec{}, fmt.Errorf("cline reviewer: read system prompt: %w", err)
	}
	return ports.ReviewCommandSpec{Argv: []string{binary, "-s", string(systemPrompt)}, InitialMessage: inv.Prompt}, nil
}

// ReviewRestoreCommand restores a recorded Cline reviewer pane by relaunching
// the reviewer command with the current task context.
func (r *Reviewer) ReviewRestoreCommand(ctx context.Context, inv ports.ReviewInvocation) (ports.ReviewCommandSpec, bool, error) {
	cmd, err := r.ReviewCommand(ctx, inv)
	return cmd, true, err
}

// ReviewMessage returns the next AO-owned task reference.
func (*Reviewer) ReviewMessage(_ context.Context, inv ports.ReviewInvocation) (string, error) {
	return inv.Prompt, nil
}

// ReviewCancel interrupts Cline's current operation while preserving the pane.
func (*Reviewer) ReviewCancel(context.Context) (ports.ReviewCancelSpec, error) {
	return ports.ReviewCancelSpec{Mode: ports.ReviewCancelInterrupt, Interrupts: 2}, nil
}
