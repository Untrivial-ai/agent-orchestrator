// Package qodercli adapts Qoder CLI as an AO reviewer. Qoder CLI is a
// prompt-driven agent, so the reviewer feeds AO's centrally authored review
// prompt to a Qoder CLI session launched over the worker's checkout.
//
// Unlike the claude-code reviewer, this one composes its own argv instead of
// borrowing the worker adapter's launch command. A reviewer pane needs two
// things the worker contract cannot express: Qoder CLI's `dont_ask` permission
// mode, which is outside AO's four-mode vocabulary, and `--tools`, which limits
// which built-in tools exist at all rather than merely which are pre-approved.
package qodercli

import (
	"context"
	"fmt"
	"os"
	"strings"

	workeragent "github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/qodercli"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// Reviewer is the Qoder CLI code-review adapter.
type Reviewer struct {
	agent *workeragent.Plugin
}

// New builds the Qoder CLI reviewer adapter.
func New() *Reviewer { return &Reviewer{agent: workeragent.New()} }

// Harness returns Qoder CLI's reviewer identity.
func (*Reviewer) Harness() domain.ReviewerHarness { return domain.ReviewerQodercli }

var _ ports.Reviewer = (*Reviewer)(nil)
var _ ports.ReviewerCanceller = (*Reviewer)(nil)
var _ ports.ReviewerRestorer = (*Reviewer)(nil)

// reviewerCoreTools is the built-in tool set the reviewer process is given.
// `--tools` is the real containment: every built-in outside this list is denied
// outright, so no allow rule and no misread prompt can reach Edit, Write, or a
// subagent. The allow list below then decides which of these are pre-approved.
var reviewerCoreTools = []string{"Read", "Grep", "Glob", "Bash"}

// reviewerAllowedTools is the read-only allowlist the reviewer launches with:
// enough to read the checkout, inspect the PR, and submit the verdict. It
// mirrors the claude-code reviewer's proven set — the tool names are identical
// between the two CLIs.
var reviewerAllowedTools = []string{
	"Read",
	"Grep",
	"Glob",
	"Bash(printf:*)",
	"Bash(gh:*)",
	"Bash(git diff:*)",
	"Bash(git log:*)",
	"Bash(git show:*)",
	"Bash(git status:*)",
	"Bash(ao review submit:*)",
}

// reviewerDisallowedTools hard-denies the write paths as defense in depth. Deny
// rules are evaluated before everything else, so they hold even if a future
// allow rule would otherwise admit the call.
var reviewerDisallowedTools = []string{
	"Edit",
	"Write",
	"NotebookEdit",
	"Bash(git push:*)",
	"Bash(git commit:*)",
}

// ReviewCommand builds a Qoder CLI invocation that reviews the worker's
// checkout for the PR, pinning the same native session id AO persists so a
// recreated pane can resume the conversation.
func (r *Reviewer) ReviewCommand(ctx context.Context, inv ports.ReviewInvocation) (ports.ReviewCommandSpec, error) {
	agentSessionID := workeragent.SessionUUID(inv.ReviewerID)
	argv, err := r.argv(ctx, inv, sessionIdentity{launchID: agentSessionID}, inv.Prompt)
	if err != nil {
		return ports.ReviewCommandSpec{}, err
	}
	return ports.ReviewCommandSpec{Argv: argv, AgentSessionID: agentSessionID}, nil
}

// ReviewMessage is the text injected into an already-running reviewer pane to
// review a new commit — AO's central review prompt.
func (r *Reviewer) ReviewMessage(_ context.Context, inv ports.ReviewInvocation) (string, error) {
	return inv.Prompt, nil
}

// ReviewRestoreCommand resumes the reviewer's Qoder CLI conversation,
// reapplying the same tool policy as a fresh launch: on resume Qoder CLI
// rebuilds its permission rules from flags, so a restore that forgot them would
// come back in dont_ask with nothing allowed, and every git/gh call the pass
// depends on would be denied in silence.
//
// ok is false when no transcript exists for the id, which happens when the
// reviewer died before its first turn. The launcher then starts a fresh pane
// instead of running a `--resume` that Qoder CLI would reject.
func (r *Reviewer) ReviewRestoreCommand(ctx context.Context, inv ports.ReviewInvocation) (ports.ReviewCommandSpec, bool, error) {
	agentSessionID := strings.TrimSpace(inv.AgentSessionID)
	if agentSessionID == "" {
		agentSessionID = workeragent.SessionUUID(inv.ReviewerID)
	}

	exists, err := r.agent.NativeConversationExists(ctx, ports.SessionRef{
		ID:            inv.ReviewerID,
		WorkspacePath: inv.WorkspacePath,
	}, agentSessionID, nil)
	if err != nil {
		return ports.ReviewCommandSpec{}, false, err
	}
	if !exists {
		return ports.ReviewCommandSpec{}, false, nil
	}

	// A dead reviewer relaunched for an active run must receive that run's task
	// as the resume-time turn. Idle restoration leaves the conversation alone.
	prompt := ""
	if strings.TrimSpace(inv.RunID) != "" {
		prompt = inv.Prompt
	}

	argv, err := r.argv(ctx, inv, sessionIdentity{resumeID: agentSessionID}, "")
	if err != nil {
		return ports.ReviewCommandSpec{}, false, err
	}
	// The task is injected after startup rather than placed on the resume
	// command line: Qoder CLI resolves --resume asynchronously inside the TUI
	// while -i auto-submits from an unrelated effect, so a command-line turn can
	// be sent into a session that has not finished loading.
	return ports.ReviewCommandSpec{
		Argv:           argv,
		AgentSessionID: agentSessionID,
		NativeResumed:  true,
		InitialMessage: prompt,
	}, true, nil
}

// ReviewCancel stops the active reviewer turn while preserving the pane. One
// Escape cancels a response in Qoder CLI's TUI; a second would not be harmless,
// because an Escape arriving at a permission prompt is routed to that dialog.
func (*Reviewer) ReviewCancel(context.Context) (ports.ReviewCancelSpec, error) {
	return ports.ReviewCancelSpec{
		Mode:  ports.ReviewCancelInput,
		Input: "\x1b",
	}, nil
}

// sessionIdentity carries either the id a fresh launch pins or the id a restore
// resumes; exactly one is set.
type sessionIdentity struct {
	launchID string
	resumeID string
}

func (r *Reviewer) argv(ctx context.Context, inv ports.ReviewInvocation, identity sessionIdentity, prompt string) ([]string, error) {
	binary, err := workeragent.ResolveQodercliBinary(ctx)
	if err != nil {
		return nil, err
	}

	argv := []string{binary}
	if identity.launchID != "" {
		argv = append(argv, "--session-id", identity.launchID)
	}

	// dont_ask never prompts: an allowed call runs, anything else is refused.
	// `auto` would be wrong here — it hands approvals to a classifier and strips
	// the very shell allow rules this reviewer depends on — and the interactive
	// modes would park an unattended pane on a dialog nobody answers.
	argv = append(argv, "--permission-mode", "dont_ask")

	argv = append(argv, "--tools")
	argv = append(argv, reviewerCoreTools...)
	for _, rule := range reviewerAllowedTools {
		argv = append(argv, "--allowed-tools", rule)
	}
	for _, rule := range reviewerDisallowedTools {
		argv = append(argv, "--disallowed-tools", rule)
	}

	if model := strings.TrimSpace(inv.Config.Model); model != "" {
		argv = append(argv, "--model", model)
	}
	argv, err = appendSystemPrompt(argv, inv.SystemPromptFile, inv.SystemPrompt)
	if err != nil {
		return nil, err
	}

	settings, err := workeragent.SettingsArg(inv.DataDir, inv.ReviewerID, inv.WorkspacePath)
	if err != nil {
		return nil, err
	}
	argv = append(argv, "--settings", settings)

	if identity.resumeID != "" {
		argv = append(argv, "--resume", identity.resumeID)
	}
	if prompt != "" {
		argv = append(argv, "-i", prompt)
	}
	return argv, nil
}

// appendSystemPrompt prefers AO's prompt file so the reviewer's standing
// instructions stay out of the shared terminal stream.
func appendSystemPrompt(argv []string, promptFile, prompt string) ([]string, error) {
	if promptFile != "" {
		info, err := os.Stat(promptFile)
		if err != nil {
			return nil, fmt.Errorf("qodercli reviewer: inspect system prompt file: %w", err)
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("qodercli reviewer: system prompt file %q is not a regular file", promptFile)
		}
		return append(argv, "--append-system-prompt-file", promptFile), nil
	}
	if prompt != "" {
		argv = append(argv, "--append-system-prompt", prompt)
	}
	return argv, nil
}
