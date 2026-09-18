// Package grok adapts Grok Build as an experimental user-approved AO reviewer.
package grok

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/agentbase"
	workergrok "github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/grok"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// HostTrustWarning documents that Grok relies on its normal interactive
// approval flow rather than an AO-enforced read-only sandbox.
const HostTrustWarning = "experimental user-approved reviewer: Grok uses its normal permission prompts because AO cannot enforce a read-only sandbox"

// Reviewer builds Grok's persistent interactive reviewer command.
type Reviewer struct {
	agentbase.Base
	resolveBinary func(context.Context) (string, error)
}

// New returns the production Grok reviewer adapter.
func New(discovery ...ports.AgentBinaryDiscovery) *Reviewer {
	r := &Reviewer{resolveBinary: workergrok.ResolveGrokBinary}
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

// Harness returns Grok's reviewer identity.
func (*Reviewer) Harness() domain.ReviewerHarness { return domain.ReviewerGrok }

var _ ports.Reviewer = (*Reviewer)(nil)
var _ ports.ReviewerCanceller = (*Reviewer)(nil)

// ReviewCommand starts Grok with AO's reviewer role as native rules. It emits
// no permission-mode flag, leaving each tool request visible to the user.
func (r *Reviewer) ReviewCommand(ctx context.Context, inv ports.ReviewInvocation) (ports.ReviewCommandSpec, error) {
	binary, err := r.resolveBinary(ctx)
	if err != nil {
		return ports.ReviewCommandSpec{}, err
	}
	if strings.TrimSpace(inv.TaskPromptRoot) == "" {
		return ports.ReviewCommandSpec{Argv: []string{binary}}, nil
	}
	rules, err := os.ReadFile(inv.SystemPromptFile) //nolint:gosec // AO-owned prompt path
	if err != nil {
		return ports.ReviewCommandSpec{}, fmt.Errorf("grok reviewer: read system prompt: %w", err)
	}
	return ports.ReviewCommandSpec{Argv: []string{binary, "--no-auto-update", "--rules", string(rules), "--", inv.Prompt}}, nil
}

// ReviewRestoreCommand restores a recorded Grok reviewer pane by relaunching
// the reviewer command with the current task context.
func (r *Reviewer) ReviewRestoreCommand(ctx context.Context, inv ports.ReviewInvocation) (ports.ReviewCommandSpec, bool, error) {
	cmd, err := r.ReviewCommand(ctx, inv)
	return cmd, true, err
}

// ReviewMessage returns the next AO-owned task reference.
func (*Reviewer) ReviewMessage(_ context.Context, inv ports.ReviewInvocation) (string, error) {
	return inv.Prompt, nil
}

// ReviewCancel interrupts Grok's current operation while preserving the pane.
func (*Reviewer) ReviewCancel(context.Context) (ports.ReviewCancelSpec, error) {
	return ports.ReviewCancelSpec{Mode: ports.ReviewCancelInterrupt, Interrupts: 2}, nil
}
