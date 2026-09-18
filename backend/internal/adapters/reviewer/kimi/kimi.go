// Package kimi adapts Kimi Code CLI as an experimental host-trusted AO
// reviewer. It always runs Kimi's visible, long-lived interactive TUI.
package kimi

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/agentbase"
	workerkimi "github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/kimi"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const skillsDirectoryName = "kimi-reviewer-skills"

// HostTrustWarning describes the authority retained by Kimi's interactive
// terminal. Plan mode limits Kimi to read-only tools, while auto mode handles
// approvals and questions without pausing for user input. AO deliberately
// does not replace the TUI with print/headless mode or place it in a sandbox.
const HostTrustWarning = "experimental host-trusted reviewer: Kimi has no OS isolation; terminal users can invoke shell mode, change Plan mode, open an external editor, or alter configuration"

// Reviewer builds Kimi's persistent interactive reviewer command.
type Reviewer struct {
	agentbase.Base
	resolveBinary func(context.Context) (string, error)
}

// New returns the production Kimi reviewer adapter.
func New(discovery ...ports.AgentBinaryDiscovery) *Reviewer {
	r := &Reviewer{resolveBinary: workerkimi.ResolveKimiBinary}
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

// Harness returns Kimi's reviewer identity.
func (*Reviewer) Harness() domain.ReviewerHarness { return domain.ReviewerKimi }

var _ ports.Reviewer = (*Reviewer)(nil)
var _ ports.ReviewerCanceller = (*Reviewer)(nil)
var _ ports.ReviewerPromptReadinessProvider = (*Reviewer)(nil)

// ReviewPromptReadinessHints reuses Kimi's worker-TUI prompt markers.
func (*Reviewer) ReviewPromptReadinessHints(ctx context.Context) (ports.PromptReadinessHints, error) {
	return workerkimi.New().PromptReadinessHints(ctx, ports.LaunchConfig{})
}

// ReviewCommand starts only Kimi's normal interactive TUI. The initial task is
// injected after the pane starts; --prompt, --print, --quiet, --command, ACP,
// Wire, AFK, YOLO, and sandbox wrappers are never emitted. Auto mode is paired
// with Plan mode so read-only reviewer tools run unattended.
func (r *Reviewer) ReviewCommand(ctx context.Context, inv ports.ReviewInvocation) (ports.ReviewCommandSpec, error) {
	if err := ctx.Err(); err != nil {
		return ports.ReviewCommandSpec{}, err
	}
	binary, err := r.resolveBinary(ctx)
	if err != nil {
		return ports.ReviewCommandSpec{}, err
	}
	if !filepath.IsAbs(binary) {
		return ports.ReviewCommandSpec{}, errors.New("kimi reviewer: resolved binary must be absolute")
	}
	// Preflight builds the permanent interactive command before request-scoped
	// prompt paths exist.
	if strings.TrimSpace(inv.TaskPromptRoot) == "" {
		return ports.ReviewCommandSpec{Argv: []string{binary, "--plan", "--auto"}}, nil
	}
	if strings.TrimSpace(inv.SystemPromptFile) == "" {
		return ports.ReviewCommandSpec{}, errors.New("kimi reviewer: system prompt file is required")
	}
	skillsDir := filepath.Join(inv.TaskPromptRoot, skillsDirectoryName)
	if err := os.MkdirAll(skillsDir, 0o700); err != nil {
		return ports.ReviewCommandSpec{}, fmt.Errorf("kimi reviewer: create empty skills directory: %w", err)
	}
	message := fmt.Sprintf(
		"Read and follow the AO reviewer role in `%s`, then %s",
		filepath.ToSlash(inv.SystemPromptFile),
		strings.TrimSpace(inv.Prompt),
	)
	return ports.ReviewCommandSpec{
		Argv:           []string{binary, "--plan", "--auto", "--skills-dir", skillsDir},
		InitialMessage: message,
	}, nil
}

// ReviewRestoreCommand restores a recorded Kimi reviewer pane by relaunching
// the reviewer command with the current task context.
func (r *Reviewer) ReviewRestoreCommand(ctx context.Context, inv ports.ReviewInvocation) (ports.ReviewCommandSpec, bool, error) {
	cmd, err := r.ReviewCommand(ctx, inv)
	return cmd, true, err
}

// ReviewMessage reuses the same Kimi TUI for subsequent review passes.
func (*Reviewer) ReviewMessage(ctx context.Context, inv ports.ReviewInvocation) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return inv.Prompt, nil
}

// ReviewCancel sends Kimi's Ctrl-C sequence twice so an active operation is
// interrupted and the reviewer cannot continue executing after cancellation.
func (*Reviewer) ReviewCancel(ctx context.Context) (ports.ReviewCancelSpec, error) {
	if err := ctx.Err(); err != nil {
		return ports.ReviewCancelSpec{}, err
	}
	return ports.ReviewCancelSpec{Mode: ports.ReviewCancelInterrupt, Interrupts: 2}, nil
}
