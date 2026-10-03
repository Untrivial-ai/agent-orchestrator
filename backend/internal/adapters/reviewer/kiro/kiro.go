// Package kiro adapts Kiro's interactive terminal UI for AO code reviews.
package kiro

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/hookutil"
	workeragent "github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/kiro"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const reviewerAgentName = "ao-reviewer"

// Reviewer is Kiro's AO code-review adapter.
type Reviewer struct {
	resolveBinary func(context.Context) (string, error)
}

// New builds the Kiro reviewer adapter.
func New() *Reviewer {
	return &Reviewer{resolveBinary: workeragent.ResolveKiroBinary}
}

// Harness identifies Kiro in the reviewer registry.
func (r *Reviewer) Harness() domain.ReviewerHarness { return domain.ReviewerKiro }

var _ ports.Reviewer = (*Reviewer)(nil)
var _ ports.ReviewerCanceller = (*Reviewer)(nil)
var _ ports.ReviewerPromptReadinessProvider = (*Reviewer)(nil)

// ReviewPromptReadinessHints reuses Kiro's worker-TUI prompt markers.
func (*Reviewer) ReviewPromptReadinessHints(ctx context.Context) (ports.PromptReadinessHints, error) {
	return workeragent.New().PromptReadinessHints(ctx, ports.LaunchConfig{})
}

// ReviewCommand launches Kiro as a persistent interactive TUI. It deliberately
// avoids positional input and --no-interactive; AO injects InitialMessage only
// after the pane exists. The process cwd is an AO-owned empty directory, so
// project .kiro agents, MCP config, hooks, steering, skills, and AGENTS.md are
// not inherited. The generated custom agent admits read tools plus a
// deny-by-default shell policy for review inspection/reporting only.
func (r *Reviewer) ReviewCommand(ctx context.Context, inv ports.ReviewInvocation) (ports.ReviewCommandSpec, error) {
	binary, err := r.resolveBinary(ctx)
	if err != nil {
		return ports.ReviewCommandSpec{}, err
	}
	if inv.TaskPromptRoot == "" {
		return ports.ReviewCommandSpec{Argv: []string{binary, "chat", "--agent", reviewerAgentName}}, nil
	}
	reviewerDir := filepath.Join(inv.TaskPromptRoot, "kiro")
	if err := writeReviewerAgent(reviewerDir, inv); err != nil {
		return ports.ReviewCommandSpec{}, err
	}
	return ports.ReviewCommandSpec{
		Argv:             []string{binary, "chat", "--agent", reviewerAgentName},
		InitialMessage:   inv.Prompt,
		WorkingDirectory: reviewerDir,
	}, nil
}

// ReviewRestoreCommand restores a recorded Kiro reviewer pane by relaunching
// with the reviewer agent/profile and current task context.
func (r *Reviewer) ReviewRestoreCommand(ctx context.Context, inv ports.ReviewInvocation) (ports.ReviewCommandSpec, bool, error) {
	cmd, err := r.ReviewCommand(ctx, inv)
	return cmd, true, err
}

// ReviewMessage keeps later passes in the same long-lived Kiro TUI.
func (r *Reviewer) ReviewMessage(_ context.Context, inv ports.ReviewInvocation) (string, error) {
	return inv.Prompt, nil
}

// ReviewCancel uses Kiro TUI's cancel-stream key. Ctrl-C is the quit binding,
// so the shared interrupt cancellation mode is intentionally not used here.
func (r *Reviewer) ReviewCancel(context.Context) (ports.ReviewCancelSpec, error) {
	return ports.ReviewCancelSpec{Mode: ports.ReviewCancelInput, Input: "\x1b"}, nil
}

func writeReviewerAgent(reviewerDir string, inv ports.ReviewInvocation) error {
	if inv.SystemPromptFile == "" || inv.WorkspacePath == "" {
		return fmt.Errorf("kiro reviewer requires system prompt and workspace paths")
	}
	system, err := os.ReadFile(inv.SystemPromptFile) //nolint:gosec // AO-owned prompt path
	if err != nil {
		return fmt.Errorf("read Kiro reviewer system prompt: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(reviewerDir, ".kiro", "agents"), 0o700); err != nil {
		return fmt.Errorf("create Kiro reviewer directory: %w", err)
	}
	kiroSystemPath := filepath.Join(reviewerDir, "system.md")
	extra := `

Kiro security and reporting rules:
- Your process runs outside the checkout. Use the read, glob, and grep tools on the absolute worker checkout path from the task.
- Shell is deny-by-default. Only the narrow git inspection and review-reporting command shapes configured by AO can run.
- The supported git shell shapes are exactly: status with optional --short; diff with --no-ext-diff --no-textconv; log; and show. Additional arguments must be single whitespace-free refs, options, or paths. Filenames with spaces, extra shell quoting, compound commands, redirections, and complex ref expressions are intentionally unsupported; use read, glob, or grep instead.
- For each reporting command, base64-encode the UTF-8 command tail you cannot type literally and use the task's command with the affected text replaced by the base64 text: printf '%s' '<base64>' | base64 --decode | ao review submit .... Use base64 -D instead of --decode on systems that require it.
- Never request or attempt any write, unrestricted shell, commit, push, extension, MCP server, skill, steering, or project agent resource.
`
	if err := hookutil.AtomicWriteFile(kiroSystemPath, append(system, extra...), 0o600); err != nil {
		return fmt.Errorf("write Kiro reviewer system prompt: %w", err)
	}

	config := map[string]any{
		"name":           reviewerAgentName,
		"description":    "AO read-only code reviewer",
		"prompt":         "file://" + filepath.ToSlash(kiroSystemPath),
		"mcpServers":     map[string]any{},
		"includeMcpJson": false,
		"resources":      []any{},
		"tools":          []string{"read", "glob", "grep", "shell"},
		"allowedTools":   []string{"read", "glob", "grep"},
		"toolsSettings": map[string]any{
			"shell": map[string]any{
				"allowedCommands":   shellAllowedCommands(inv),
				"deniedCommands":    []string{".*(?:git[[:space:]]+(?:commit|push)|--output(?:=|[[:space:]])|>|>>|tee[[:space:]]).*"},
				"autoAllowReadonly": false,
				"denyByDefault":     true,
			},
		},
	}
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("encode Kiro reviewer agent: %w", err)
	}
	agentPath := filepath.Join(reviewerDir, ".kiro", "agents", reviewerAgentName+".json")
	if err := hookutil.AtomicWriteFile(agentPath, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("write Kiro reviewer agent: %w", err)
	}
	return nil
}

func shellAllowedCommands(inv ports.ReviewInvocation) []string {
	workspace := regexp.QuoteMeta(filepath.ToSlash(inv.WorkspacePath))
	session := regexp.QuoteMeta(string(inv.WorkerSessionID))
	gitPrefix := `git --no-pager -C ['"]?` + workspace + `['"]? `
	safeArg := `[A-Za-z0-9_./:=,@{}^~+\-]+`
	base64Pipe := `printf '%s' '[A-Za-z0-9+/=]+' \| base64 (?:--decode|-D) \| `
	// The submit command carries the review body on stdin and its inline
	// findings as the repeatable --comment-* trio; the trio may repeat any
	// number of times, in order.
	submitFlags := `--run [A-Za-z0-9_-]+ --verdict (?:approved|changes_requested) --body -(?: --comment-path ` + safeArg + ` --comment-line [0-9]+ --comment-body '[^']*')*$`
	// Kiro accepts regex policies rather than structured argv. Anchor every
	// pattern to the complete command and intentionally support only the exact
	// whitespace-free argument shapes described in the reviewer prompt above.
	return []string{
		`^` + gitPrefix + `status(?: --short)?$`,
		`^` + gitPrefix + `diff --no-ext-diff --no-textconv(?: ` + safeArg + `)*(?: --(?: ` + safeArg + `)*)?$`,
		`^` + gitPrefix + `log(?: ` + safeArg + `)*$`,
		`^` + gitPrefix + `show(?: ` + safeArg + `)*(?: --(?: ` + safeArg + `)*)?$`,
		`^` + base64Pipe + `ao review submit --session ` + session + ` ` + submitFlags,
	}
}
