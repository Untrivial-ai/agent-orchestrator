// Package claudecode is the claude-code reviewer adapter. claude-code is a
// prompt-driven agent, so this reviewer feeds AO's review prompt (authored
// centrally and passed in ReviewInvocation.Prompt) to the worker claude-code
// adapter's launch-command construction (binary resolution, flags). The reviewer
// contract stays prompt-agnostic, so a one-shot CLI reviewer (e.g. greptile) can
// ignore the prompt entirely.
package claudecode

import (
	"context"
	"time"

	workeragent "github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/claudecode"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/reviewer/agentrestore"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// Reviewer is the claude-code code-review adapter.
type Reviewer struct {
	agent ports.Agent
}

// New builds the claude-code reviewer adapter.
func New() *Reviewer {
	return &Reviewer{agent: workeragent.New()}
}

// Harness identifies this reviewer in the reviewer registry.
func (r *Reviewer) Harness() domain.ReviewerHarness {
	return domain.ReviewerClaudeCode
}

var _ ports.Reviewer = (*Reviewer)(nil)
var _ ports.ReviewerCanceller = (*Reviewer)(nil)
var _ ports.ReviewerRestorer = (*Reviewer)(nil)

// reviewerAllowedTools is the read-only tool allowlist the reviewer launches
// with. The reviewer runs headless (no human to approve prompts) but must stay
// read-only. It launches in Claude's dontAsk-equivalent policy: allow rules are
// honored, while an action that Claude cannot statically approve is rejected
// instead of parking the unattended reviewer at a permission prompt. Reporting
// uses an analyzer-safe base64 pipeline so review JSON is neither placed raw in
// a shell operand nor written to the worktree.
var reviewerAllowedTools = []string{
	"Read",
	"Grep",
	"Glob",
	"Bash(printf:*)",
	"Bash(base64 --decode:*)",
	"Bash(base64 -D:*)",
	"Bash(gh api --method POST repos/:*)",
	"Bash(git diff:*)",
	"Bash(git log:*)",
	"Bash(git show:*)",
	"Bash(git status:*)",
	"Bash(ao review submit:*)",
}

// reviewerDisallowedTools hard-denies the write paths as defense in depth, so a
// misbehaving model cannot edit files or move the branch even if a future
// allowlist entry would otherwise admit it.
var reviewerDisallowedTools = []string{
	"Edit",
	"Write",
	"NotebookEdit",
	"Bash(git push:*)",
	"Bash(git commit:*)",
	"Bash(gh pr merge:*)",
	"Bash(gh api --method DELETE:*)",
	"Bash(gh api --method PUT:*)",
	"Bash(gh api --method PATCH:*)",
	"Bash(gh gist:*)",
}

const analyzerSafeReportingRule = `

Claude reviewer reporting rule: when the review task shows a command that pipes raw JSON from printf, base64-encode the UTF-8 JSON yourself and run the same pipeline as printf '%s' '<base64>' | base64 --decode | <report command> (use base64 -D instead of --decode on systems that require it). Never place raw review JSON in a Bash operand or write it to a file.`

func reviewMessage(prompt string) string {
	return prompt + analyzerSafeReportingRule
}

// ReviewCommand builds a claude-code invocation that reviews the worker's
// checkout for the PR. Production launches provide the standing instructions
// through an AO-owned prompt file so only the short task-file reference is
// terminal-visible.
func (r *Reviewer) ReviewCommand(ctx context.Context, inv ports.ReviewInvocation) (ports.ReviewCommandSpec, error) {
	agentSessionID := workeragent.SessionUUID(inv.ReviewerID)
	argv, err := r.agent.GetLaunchCommand(ctx, ports.LaunchConfig{
		Config: inv.Config,
		// Pin the same deterministic reviewer-native id we persist. Hooks can
		// later replace it with Claude's reported id, but restore must never start
		// from an id that the process was not launched with.
		SessionID:        agentSessionID,
		WorkspacePath:    inv.WorkspacePath,
		Prompt:           reviewMessage(inv.Prompt),
		SystemPrompt:     inv.SystemPrompt,
		SystemPromptFile: inv.SystemPromptFile,
		// A headless reviewer cannot answer a prompt. Deny anything outside the
		// allowlist so upstream analyzer changes fail closed rather than stall.
		Permissions:     ports.PermissionModeDenyUnapproved,
		AllowedTools:    reviewerAllowedTools,
		DisallowedTools: reviewerDisallowedTools,
	})
	if err != nil {
		return ports.ReviewCommandSpec{}, err
	}
	return ports.ReviewCommandSpec{Argv: argv, AgentSessionID: agentSessionID}, nil
}

// PreLaunch runs reviewer-specific preflight. For Claude Code this installs the
// selected reviewer's hooks and records the worker checkout as trusted before
// the headless reviewer pane starts.
func (r *Reviewer) PreLaunch(ctx context.Context, inv ports.ReviewInvocation) error {
	if hooks, ok := r.agent.(interface {
		GetAgentHooks(context.Context, ports.WorkspaceHookConfig) error
	}); ok {
		if err := hooks.GetAgentHooks(ctx, ports.WorkspaceHookConfig{WorkspacePath: inv.WorkspacePath}); err != nil {
			return err
		}
	}
	pl, ok := r.agent.(interface {
		PreLaunch(context.Context, ports.LaunchConfig) error
	})
	if !ok {
		return nil
	}
	return pl.PreLaunch(ctx, ports.LaunchConfig{
		Config:        inv.Config,
		SessionID:     workeragent.SessionUUID(inv.ReviewerID),
		WorkspacePath: inv.WorkspacePath,
	})
}

// ReviewMessage is the text injected into an already-running reviewer pane to
// review a new commit — AO's central review prompt.
func (r *Reviewer) ReviewMessage(_ context.Context, inv ports.ReviewInvocation) (string, error) {
	return reviewMessage(inv.Prompt), nil
}

// ReviewRestoreCommand resumes the reviewer Claude Code conversation captured
// from hooks, reapplying the same read-only tool policy as a fresh review launch.
func (r *Reviewer) ReviewRestoreCommand(ctx context.Context, inv ports.ReviewInvocation) (ports.ReviewCommandSpec, bool, error) {
	cmd, ok, err := agentrestore.Command(ctx, r.agent, inv, agentrestore.Options{
		Permissions:     ports.PermissionModeDenyUnapproved,
		AllowedTools:    reviewerAllowedTools,
		DisallowedTools: reviewerDisallowedTools,
	})
	if err != nil || !ok {
		return cmd, ok, err
	}
	if cmd.AgentSessionID == "" {
		cmd.AgentSessionID = workeragent.SessionUUID(inv.ReviewerID)
	}
	return cmd, true, nil
}

// ReviewCancel stops the active Claude Code reviewer turn while preserving the
// terminal pane for inspection. Claude Code treats Ctrl-C as an exit path in
// common TUI states, so use Escape to interrupt active execution without
// tearing down the harness.
func (r *Reviewer) ReviewCancel(context.Context) (ports.ReviewCancelSpec, error) {
	return ports.ReviewCancelSpec{
		Mode:       ports.ReviewCancelInput,
		Inputs:     []string{"\x1b", "\x1b"},
		InputDelay: 150 * time.Millisecond,
	}, nil
}
