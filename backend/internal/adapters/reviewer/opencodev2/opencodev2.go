// Package opencodev2 adapts the OpenCode 2 worker for read-only code reviews.
package opencodev2

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	workeragent "github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/opencodev2"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/reviewer/agentrestore"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const configContentPrefix = "OPENCODE_CONFIG_CONTENT="

// Reviewer is the OpenCode 2 code-review adapter.
type Reviewer struct {
	agent ports.Agent
}

// New builds the OpenCode 2 reviewer adapter.
func New() *Reviewer {
	return &Reviewer{agent: workeragent.New()}
}

var _ ports.Reviewer = (*Reviewer)(nil)
var _ ports.ReviewerCanceller = (*Reviewer)(nil)
var _ ports.ReviewerRestorer = (*Reviewer)(nil)

// Harness identifies this reviewer in the reviewer registry.
func (r *Reviewer) Harness() domain.ReviewerHarness {
	return domain.ReviewerOpenCodeV2
}

// ReviewCommand launches OpenCode 2 with a final agent-level permission list.
// Accept-edits only forces the worker adapter to materialize its selected AO
// agent when a direct caller supplies no system prompt; the edit allow is
// replaced before the command can be launched.
func (r *Reviewer) ReviewCommand(ctx context.Context, inv ports.ReviewInvocation) (ports.ReviewCommandSpec, error) {
	argv, err := r.agent.GetLaunchCommand(ctx, ports.LaunchConfig{
		Config:           inv.Config,
		SessionID:        inv.ReviewerID,
		WorkspacePath:    inv.WorkspacePath,
		Prompt:           inv.Prompt,
		SystemPrompt:     inv.SystemPrompt,
		SystemPromptFile: inv.SystemPromptFile,
		Permissions:      ports.PermissionModeAcceptEdits,
	})
	if err != nil {
		return ports.ReviewCommandSpec{}, err
	}
	argv, content, err := applyReviewerPolicy(argv, inv.TaskPromptRoot)
	if err != nil {
		return ports.ReviewCommandSpec{}, err
	}
	return ports.ReviewCommandSpec{
		Argv: argv,
		Env:  map[string]string{"OPENCODE_CONFIG_CONTENT": content},
	}, nil
}

// ReviewMessage returns the centrally-authored task for an existing pane.
func (r *Reviewer) ReviewMessage(_ context.Context, inv ports.ReviewInvocation) (string, error) {
	return inv.Prompt, nil
}

// ReviewRestoreCommand resumes the native conversation and reapplies the same
// final agent-level policy used for a fresh reviewer.
func (r *Reviewer) ReviewRestoreCommand(ctx context.Context, inv ports.ReviewInvocation) (ports.ReviewCommandSpec, bool, error) {
	cmd, ok, err := agentrestore.Command(ctx, r.agent, inv, agentrestore.Options{Permissions: ports.PermissionModeAcceptEdits})
	if err != nil || !ok {
		return cmd, ok, err
	}
	var content string
	cmd.Argv, content, err = applyReviewerPolicy(cmd.Argv, inv.TaskPromptRoot)
	if err != nil {
		return ports.ReviewCommandSpec{}, false, err
	}
	cmd.Env = map[string]string{"OPENCODE_CONFIG_CONTENT": content}
	return cmd, true, nil
}

// ReviewCancel stops the active OpenCode turn while preserving its pane.
func (r *Reviewer) ReviewCancel(context.Context) (ports.ReviewCancelSpec, error) {
	return ports.ReviewCancelSpec{
		Mode:       ports.ReviewCancelInput,
		Inputs:     []string{"\x1b", "\x1b"},
		InputDelay: 150 * time.Millisecond,
	}, nil
}

type permissionRule struct {
	Action   string `json:"action"`
	Resource string `json:"resource"`
	Effect   string `json:"effect"`
}

func applyReviewerPolicy(argv []string, taskPromptRoot string) ([]string, string, error) {
	contentIndex := -1
	for i, arg := range argv {
		if strings.HasPrefix(arg, configContentPrefix) {
			contentIndex = i
			break
		}
	}
	if contentIndex < 0 {
		return nil, "", fmt.Errorf("opencode-v2 reviewer: worker command has no inline config")
	}

	config := map[string]json.RawMessage{}
	if err := json.Unmarshal([]byte(strings.TrimPrefix(argv[contentIndex], configContentPrefix)), &config); err != nil || config == nil {
		return nil, "", fmt.Errorf("opencode-v2 reviewer: worker inline config must be a JSON object")
	}
	var defaultAgent string
	if err := json.Unmarshal(config["default_agent"], &defaultAgent); err != nil || strings.TrimSpace(defaultAgent) == "" {
		return nil, "", fmt.Errorf("opencode-v2 reviewer: worker inline config has no default agent")
	}
	agents := map[string]json.RawMessage{}
	if err := json.Unmarshal(config["agents"], &agents); err != nil || agents == nil {
		return nil, "", fmt.Errorf("opencode-v2 reviewer: worker inline config agents must be an object")
	}
	agent := map[string]any{}
	if err := json.Unmarshal(agents[defaultAgent], &agent); err != nil || agent == nil {
		return nil, "", fmt.Errorf("opencode-v2 reviewer: default agent %q must be an object", defaultAgent)
	}
	agent["permissions"] = reviewerPermissions(taskPromptRoot)

	var err error
	agents[defaultAgent], err = json.Marshal(agent)
	if err != nil {
		return nil, "", fmt.Errorf("opencode-v2 reviewer: encode default agent: %w", err)
	}
	config["agents"], err = json.Marshal(agents)
	if err != nil {
		return nil, "", fmt.Errorf("opencode-v2 reviewer: encode agents: %w", err)
	}
	data, err := json.Marshal(config)
	if err != nil {
		return nil, "", fmt.Errorf("opencode-v2 reviewer: encode inline config: %w", err)
	}
	content := string(data)
	argv[contentIndex] = configContentPrefix + content
	return argv, content, nil
}

// OpenCode 2 resolves ordered rules by the last match. The catch-all therefore
// comes first, followed only by the inspection and reporting exceptions AO's
// reviewer task requires. Because these rules belong to the selected agent,
// they are evaluated after caller/project global rules and cannot inherit a
// saved broad approval from that agent's existing config.
func reviewerPermissions(taskPromptRoot string) []permissionRule {
	rules := []permissionRule{
		{Action: "*", Resource: "*", Effect: "deny"},
		{Action: "read", Resource: "*", Effect: "allow"},
		{Action: "glob", Resource: "*", Effect: "allow"},
		{Action: "grep", Resource: "*", Effect: "allow"},
	}
	if taskPromptRoot != "" {
		rules = append(rules, permissionRule{
			Action:   "external_directory",
			Resource: filepath.ToSlash(filepath.Join(taskPromptRoot, "**")),
			Effect:   "allow",
		})
	}
	// OpenCode 2's shell scanner checks each command node separately (a pipeline
	// is split, and a redirected command's resource includes its redirect), so
	// every rule below matches a single command, never a whole pipeline.
	for _, resource := range []string{
		"gh api repos/*",
		"git diff *",
		"git log *",
		"git show *",
		"git status *",
		"ao review submit *",
	} {
		rules = append(rules, permissionRule{Action: "shell", Resource: resource, Effect: "allow"})
	}
	// Later rules win, so the mutating shapes below override the broad allows
	// above. Any gh api flag is denied (a positive shape, not a flag blocklist);
	// only the review-submission endpoint is re-allowed afterwards.
	for _, resource := range []string{
		"gh api -*",
		"gh api * -*",
		"gh api *>*",
		"git *>*",
		"ao review submit *>*",
		"gh api *<*",
		"git *<*",
		"ao review submit *<*",
		"gh api *|*",
		"git *|*",
		"ao review submit *|*",
		"gh api *;*",
		"git *;*",
		"ao review submit *;*",
		"gh api *&*",
		"git *&*",
		"ao review submit *&*",
		"gh api *`*",
		"git *`*",
		"ao review submit *`*",
		"gh api *$(*",
		"git *$(*",
		"ao review submit *$(*",
		"git diff *--output*",
		"git log *--output*",
		"git show *--output*",
	} {
		rules = append(rules, permissionRule{Action: "shell", Resource: resource, Effect: "deny"})
	}
	for _, resource := range []string{
		"gh api --method POST repos/*/pulls/*/reviews --input -",
		"gh api --method POST repos/*/pulls/*/reviews --input - --jq '.id'",
		"printf '%s' '*'",
	} {
		rules = append(rules, permissionRule{Action: "shell", Resource: resource, Effect: "allow"})
	}
	for _, resource := range []string{
		"printf '%s' '*' *>*",
		"printf '%s' '*' *<*",
		"printf '%s' '*' *|*",
		"printf '%s' '*' *;*",
		"printf '%s' '*' *&*",
	} {
		rules = append(rules, permissionRule{Action: "shell", Resource: resource, Effect: "deny"})
	}
	return rules
}
