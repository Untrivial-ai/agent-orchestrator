// Package kimchi adapts Kimchi as an experimental host-trusted reviewer.
package kimchi

import (
	"context"

	workeragent "github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/kimchi"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/reviewer/agentrestore"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// HostTrustWarning documents the boundary users accept when selecting Kimchi.
// Tool rules reduce accidental writes but do not contain shell or network
// authority at the operating-system boundary.
const HostTrustWarning = "experimental host-trusted reviewer: Kimchi tool allow/deny rules are not OS isolation; shell commands and GitHub access retain host authority"

// reviewerAllowedTools is the best-effort tool policy the reviewer launches
// with. Instead of bypassPermissions — which skips Kimchi's permission system
// entirely — it launches in --auto mode where these rules are honored. The
// reviewer can read the checkout and run the few commands it needs (git
// diff/log/status to inspect the PR, gh api to post the review, printf to pipe
// JSON, and `ao review submit` to record the verdict) without stalling. This is
// hardening, not an OS sandbox; the adapter remains host-trusted.
//
// The review protocol (review/prompt.go step 1) requires the piped command:
//
//	printf '%s' '{...}' | gh api --method POST .../reviews --input -
//
// Kimchi's allow matcher is single-segment only, so piped commands can't be
// auto-approved by an allow rule — they fall to the classifier under --auto.
// Including both bash(printf:*) and bash(gh:*) ensures the classifier
// recognizes each segment. Dangerous gh verbs (merge, DELETE/PUT/PATCH, gist)
// are denied in reviewerDisallowedTools; git show is denied there too.
// Kimchi's rule parser is case-insensitive on tool names, so lowercase tool
// names are used to match Kimchi's internal names.
var reviewerAllowedTools = []string{
	"read",
	"grep",
	"glob",
	"bash(printf:*)",
	"bash(git diff:*)",
	"bash(git log:*)",
	"bash(git status:*)",
	"bash(gh:*)",
	"bash(ao review submit:*)",
}

// reviewerDisallowedTools denies common write and exfiltration paths inside
// Kimchi's own permission layer. It is defense in depth only: a shell-capable
// process in the worker checkout still has host authority. Kimchi has no
// NotebookEdit tool, so it is omitted from the deny list.
//
// Deny-before-allow ordering: Kimchi's evaluateRules (in
// src/extensions/permissions/rules.ts) checks deny rules before allow rules
// within each source level (session > cli > local > project > user > builtin),
// so the deny list below IS effective despite the allow list above — a denied
// tool is always blocked regardless of allow rules. This ordering was verified
// directly from Kimchi source.
//
// The blanket bash(gh:*) deny was removed because the review protocol requires
// gh api --method POST to post reviews. Instead, specific dangerous gh verbs
// are denied: pr merge (self-merge), api --method DELETE/PUT/PATCH (mutate
// repo state), and gist (exfiltration).
var reviewerDisallowedTools = []string{
	"edit",
	"write",
	"bash(git push:*)",
	"bash(git commit:*)",
	"bash(git show:*)",
	"bash(gh pr merge:*)",
	"bash(gh api --method DELETE:*)",
	"bash(gh api --method PUT:*)",
	"bash(gh api --method PATCH:*)",
	"bash(gh gist:*)",
}

// Reviewer is the Kimchi code-review adapter.
type Reviewer struct {
	agent ports.Agent
}

// New builds the Kimchi reviewer adapter.
func New() *Reviewer {
	return &Reviewer{agent: workeragent.New()}
}

// Harness identifies this reviewer in the reviewer registry.
func (r *Reviewer) Harness() domain.ReviewerHarness {
	return domain.ReviewerKimchi
}

var _ ports.Reviewer = (*Reviewer)(nil)
var _ ports.ReviewerCanceller = (*Reviewer)(nil)
var _ ports.ReviewerRestorer = (*Reviewer)(nil)

// ReviewCommand builds the argv to launch a fresh Kimchi reviewer over the
// worker's checkout. --auto keeps the session moving while the allow/deny tool
// lists provide best-effort hardening for the review tools (git
// diff/log/status to inspect the PR, gh api to post the review, printf to pipe
// JSON, and `ao review submit` to record the verdict). The deny list covers
// common mutation paths, but does not make the process read-only or isolated.
func (r *Reviewer) ReviewCommand(ctx context.Context, inv ports.ReviewInvocation) (ports.ReviewCommandSpec, error) {
	argv, err := r.agent.GetLaunchCommand(ctx, ports.LaunchConfig{
		Config:           inv.Config,
		SessionID:        inv.ReviewerID,
		WorkspacePath:    inv.WorkspacePath,
		Prompt:           inv.Prompt,
		SystemPrompt:     inv.SystemPrompt,
		SystemPromptFile: inv.SystemPromptFile,
		Permissions:      ports.PermissionModeAuto,
		AllowedTools:     reviewerAllowedTools,
		DisallowedTools:  reviewerDisallowedTools,
	})
	if err != nil {
		return ports.ReviewCommandSpec{}, err
	}
	return ports.ReviewCommandSpec{Argv: argv}, nil
}

// PreLaunch installs Kimchi's native hooks in the selected review workspace so
// AO can capture the native conversation id and activity events.
func (r *Reviewer) PreLaunch(ctx context.Context, inv ports.ReviewInvocation) error {
	return r.agent.GetAgentHooks(ctx, ports.WorkspaceHookConfig{WorkspacePath: inv.WorkspacePath})
}

// ReviewMessage returns the centrally-authored task for an existing pane.
func (r *Reviewer) ReviewMessage(_ context.Context, inv ports.ReviewInvocation) (string, error) {
	return inv.Prompt, nil
}

// ReviewRestoreCommand resumes the captured native Kimchi conversation and
// reapplies the same best-effort tool policy as a fresh reviewer launch.
func (r *Reviewer) ReviewRestoreCommand(ctx context.Context, inv ports.ReviewInvocation) (ports.ReviewCommandSpec, bool, error) {
	return agentrestore.Command(ctx, r.agent, inv, agentrestore.Options{
		Permissions:     ports.PermissionModeAuto,
		AllowedTools:    reviewerAllowedTools,
		DisallowedTools: reviewerDisallowedTools,
	})
}

// ReviewCancel stops the active Kimchi reviewer turn while preserving the
// terminal pane for inspection.
func (r *Reviewer) ReviewCancel(context.Context) (ports.ReviewCancelSpec, error) {
	return ports.ReviewCancelSpec{Mode: ports.ReviewCancelInterrupt, Interrupts: 2}, nil
}
