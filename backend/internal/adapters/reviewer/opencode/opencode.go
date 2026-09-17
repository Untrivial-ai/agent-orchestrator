// Package opencode adapts the opencode worker agent for code-review sessions.
package opencode

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	workeragent "github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/opencode"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/reviewer/agentrestore"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// Reviewer is the opencode code-review adapter.
type Reviewer struct {
	agent ports.Agent
}

// New builds the opencode reviewer adapter.
func New() *Reviewer {
	return &Reviewer{agent: workeragent.New()}
}

// Harness identifies this reviewer in the reviewer registry.
func (r *Reviewer) Harness() domain.ReviewerHarness {
	return domain.ReviewerOpenCode
}

var _ ports.Reviewer = (*Reviewer)(nil)
var _ ports.ReviewerCanceller = (*Reviewer)(nil)
var _ ports.ReviewerRestorer = (*Reviewer)(nil)

// ReviewCommand launches the reviewer with an inline permission policy that
// permits inspection and the two reporting commands while denying edits and
// every other tool. Production launches provide the system role through an
// AO-owned prompt file; direct callers without one retain the inline fallback.
func (r *Reviewer) ReviewCommand(ctx context.Context, inv ports.ReviewInvocation) (ports.ReviewCommandSpec, error) {
	prompt := inv.Prompt
	if inv.SystemPromptFile == "" {
		prompt = strings.TrimSpace(inv.SystemPrompt + "\n\n" + inv.Prompt)
	}
	argv, err := r.agent.GetLaunchCommand(ctx, ports.LaunchConfig{
		Config:           inv.Config,
		SessionID:        inv.ReviewerID,
		WorkspacePath:    inv.WorkspacePath,
		Prompt:           prompt,
		SystemPromptFile: inv.SystemPromptFile,
		Permissions:      ports.PermissionModeAuto,
	})
	if err != nil {
		return ports.ReviewCommandSpec{}, err
	}
	config, err := buildReviewerConfig(inv.TaskPromptRoot)
	if err != nil {
		return ports.ReviewCommandSpec{}, err
	}
	return ports.ReviewCommandSpec{
		Argv: argv,
		Env:  map[string]string{"OPENCODE_CONFIG_CONTENT": config},
	}, nil
}

// buildReviewerConfig keeps OpenCode read-only while allowing it to read the
// AO-owned task prompts outside the worker checkout. The exception is scoped
// to the stable reviewer prompt root so a long-lived process can read future
// request-scoped tasks; every other external path remains denied.
func buildReviewerConfig(taskPromptRoot string) (string, error) {
	permission := map[string]any{
		"*":    "deny",
		"read": "allow",
		"glob": "allow",
		"grep": "allow",
		"bash": map[string]string{
			"*":                             "deny",
			"gh api *":                      "allow",
			"git diff*":                     "allow",
			"git log*":                      "allow",
			"git show*":                     "allow",
			"git status*":                   "allow",
			"ao review submit *":            "allow",
			"printf * | gh api *":           "allow",
			"printf * | ao review submit *": "allow",
		},
	}
	if taskPromptRoot != "" {
		promptPattern := filepath.ToSlash(filepath.Join(taskPromptRoot, "**"))
		permission["external_directory"] = map[string]string{promptPattern: "allow"}
	}
	data, err := json.Marshal(map[string]any{"permission": permission})
	if err != nil {
		return "", fmt.Errorf("encode opencode reviewer config: %w", err)
	}
	return string(data), nil
}

// ReviewMessage returns the centrally-authored task for an existing pane.
func (r *Reviewer) ReviewMessage(_ context.Context, inv ports.ReviewInvocation) (string, error) {
	return inv.Prompt, nil
}

// ReviewRestoreCommand resumes the reviewer OpenCode conversation captured
// from hooks, reapplying the same read-only reviewer config as a fresh launch.
func (r *Reviewer) ReviewRestoreCommand(ctx context.Context, inv ports.ReviewInvocation) (ports.ReviewCommandSpec, bool, error) {
	cmd, ok, err := agentrestore.Command(ctx, r.agent, inv, agentrestore.Options{Permissions: ports.PermissionModeAuto})
	if err != nil || !ok {
		return cmd, ok, err
	}
	config, err := buildReviewerConfig(inv.TaskPromptRoot)
	if err != nil {
		return ports.ReviewCommandSpec{}, false, err
	}
	cmd.Env = map[string]string{"OPENCODE_CONFIG_CONTENT": config}
	return cmd, true, nil
}

// ReviewCancel stops the active OpenCode reviewer turn while preserving the
// terminal pane for inspection. OpenCode uses Escape to stop active execution;
// unlike a queued message this reaches the busy TUI immediately and does not
// append Enter.
func (r *Reviewer) ReviewCancel(context.Context) (ports.ReviewCancelSpec, error) {
	return ports.ReviewCancelSpec{
		Mode:       ports.ReviewCancelInput,
		Inputs:     []string{"\x1b", "\x1b"},
		InputDelay: 150 * time.Millisecond,
	}, nil
}
