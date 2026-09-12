// Package copilot adapts the GitHub Copilot CLI worker for persistent,
// restricted AO code-review sessions.
package copilot

import (
	"context"

	workeragent "github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/copilot"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const availableTools = "bash,view,grep,glob"

var allowedTools = []string{
	"read",
	"shell(git diff:*)",
	"shell(git log:*)",
	"shell(git show:*)",
	"shell(git status:*)",
	"shell(printf:*)",
	"shell(gh api:*)",
	"shell(ao review submit:*)",
}

var deniedTools = []string{
	"write",
	"shell(git push:*)",
	"shell(git commit:*)",
	"shell(git checkout:*)",
	"shell(git reset:*)",
	"shell(git clean:*)",
	"shell(git apply:*)",
	"shell(git merge:*)",
	"shell(git rebase:*)",
	"shell(git cherry-pick:*)",
}

type agent interface {
	ports.Agent
	InstallAgentProfile(context.Context, ports.WorkspaceHookConfig) error
}

// Reviewer is the GitHub Copilot CLI code-review adapter.
type Reviewer struct {
	agent agent
}

// New builds the Copilot reviewer adapter.
func New() *Reviewer {
	return &Reviewer{agent: workeragent.New()}
}

// Harness identifies this reviewer in the reviewer registry.
func (r *Reviewer) Harness() domain.ReviewerHarness {
	return domain.ReviewerCopilot
}

var _ ports.Reviewer = (*Reviewer)(nil)
var _ ports.ReviewerCanceller = (*Reviewer)(nil)

// PreLaunch installs only the AO-owned custom-agent profile. Reviewer panes do
// not use worker lifecycle hooks, so this must not create .github/hooks/ao.json.
func (r *Reviewer) PreLaunch(ctx context.Context, inv ports.ReviewInvocation) error {
	return r.agent.InstallAgentProfile(ctx, ports.WorkspaceHookConfig{
		DataDir:          inv.DataDir,
		SessionID:        inv.ReviewerID,
		WorkspacePath:    inv.WorkspacePath,
		SystemPromptFile: inv.SystemPromptFile,
	})
}

// ReviewCommand launches one persistent interactive pane with a narrow,
// read-only Copilot policy. PermissionModeDefault is intentional: the worker
// adapter maps auto to --allow-all-tools, which would defeat this policy.
func (r *Reviewer) ReviewCommand(ctx context.Context, inv ports.ReviewInvocation) (ports.ReviewCommandSpec, error) {
	argv, err := r.agent.GetLaunchCommand(ctx, ports.LaunchConfig{
		Config:           inv.Config,
		DataDir:          inv.DataDir,
		SessionID:        inv.ReviewerID,
		WorkspacePath:    inv.WorkspacePath,
		Prompt:           inv.Prompt,
		SystemPromptFile: inv.SystemPromptFile,
		Permissions:      ports.PermissionModeDefault,
	})
	if err != nil {
		return ports.ReviewCommandSpec{}, err
	}

	policy := []string{"--available-tools", availableTools}
	for _, tool := range allowedTools {
		policy = append(policy, "--allow-tool", tool)
	}
	for _, tool := range deniedTools {
		policy = append(policy, "--deny-tool", tool)
	}
	if inv.TaskPromptRoot != "" {
		policy = append(policy, "--add-dir", inv.TaskPromptRoot)
	}
	policy = append(policy,
		"--allow-url", "github.com",
		"--allow-url", "api.github.com",
		"--disable-builtin-mcps",
		"--no-ask-user",
	)
	return ports.ReviewCommandSpec{Argv: insertBeforeInteractive(argv, policy...)}, nil
}

// ReviewRestoreCommand restores a recorded Copilot reviewer pane by relaunching
// with the reviewer profile and current task context.
func (r *Reviewer) ReviewRestoreCommand(ctx context.Context, inv ports.ReviewInvocation) (ports.ReviewCommandSpec, bool, error) {
	cmd, err := r.ReviewCommand(ctx, inv)
	return cmd, true, err
}

// ReviewMessage returns the centrally-authored task for the existing pane.
func (r *Reviewer) ReviewMessage(_ context.Context, inv ports.ReviewInvocation) (string, error) {
	return inv.Prompt, nil
}

// ReviewCancel interrupts twice, matching Copilot's interactive cancellation.
func (r *Reviewer) ReviewCancel(context.Context) (ports.ReviewCancelSpec, error) {
	return ports.ReviewCancelSpec{Mode: ports.ReviewCancelInterrupt, Interrupts: 2}, nil
}

func insertBeforeInteractive(argv []string, extra ...string) []string {
	for i, arg := range argv {
		if arg == "--interactive" || arg == "-i" {
			out := make([]string, 0, len(argv)+len(extra))
			out = append(out, argv[:i]...)
			out = append(out, extra...)
			return append(out, argv[i:]...)
		}
	}
	return append(argv, extra...)
}
