// Package kilocode adapts the Kilo Code worker agent for code-review sessions.
package kilocode

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	workeragent "github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/kilocode"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const configAssignmentPrefix = "KILO_CONFIG_CONTENT="

// Reviewer is the Kilo Code code-review adapter.
type Reviewer struct {
	agent ports.Agent
}

// New builds the Kilo Code reviewer adapter.
func New() *Reviewer {
	return &Reviewer{agent: workeragent.New()}
}

// Harness identifies this reviewer in the reviewer registry.
func (r *Reviewer) Harness() domain.ReviewerHarness {
	return domain.ReviewerKiloCode
}

var _ ports.Reviewer = (*Reviewer)(nil)
var _ ports.ReviewerCanceller = (*Reviewer)(nil)

// ReviewCommand launches Kilo Code with the same hidden system-prompt agent as
// the worker adapter, then replaces its normal permission config with the
// reviewer-only read/report policy. The persistent TUI form is intentional:
// AO reuses reviewer panes and injects later review tasks into them.
func (r *Reviewer) ReviewCommand(ctx context.Context, inv ports.ReviewInvocation) (ports.ReviewCommandSpec, error) {
	argv, err := r.agent.GetLaunchCommand(ctx, ports.LaunchConfig{
		Config:           inv.Config,
		SessionID:        inv.ReviewerID,
		WorkspacePath:    inv.WorkspacePath,
		Prompt:           inv.Prompt,
		SystemPrompt:     inv.SystemPrompt,
		SystemPromptFile: inv.SystemPromptFile,
		Permissions:      ports.PermissionModeDefault,
	})
	if err != nil {
		return ports.ReviewCommandSpec{}, err
	}
	argv, err = withReviewerConfig(argv, inv.TaskPromptRoot, inv.SystemPromptFile)
	if err != nil {
		return ports.ReviewCommandSpec{}, err
	}
	return ports.ReviewCommandSpec{Argv: argv}, nil
}

// ReviewRestoreCommand restores a recorded Kilo Code reviewer pane by
// relaunching with the reviewer configuration and current task context.
func (r *Reviewer) ReviewRestoreCommand(ctx context.Context, inv ports.ReviewInvocation) (ports.ReviewCommandSpec, bool, error) {
	cmd, err := r.ReviewCommand(ctx, inv)
	return cmd, true, err
}

func withReviewerConfig(argv []string, taskPromptRoot, systemPromptFile string) ([]string, error) {
	config := map[string]any{}
	assignmentIndex := -1
	for i, arg := range argv {
		if !strings.HasPrefix(arg, configAssignmentPrefix) {
			continue
		}
		assignmentIndex = i
		if err := json.Unmarshal([]byte(strings.TrimPrefix(arg, configAssignmentPrefix)), &config); err != nil {
			return nil, fmt.Errorf("decode Kilo Code launch config: %w", err)
		}
		break
	}

	configPath := ""
	if taskPromptRoot != "" && systemPromptFile != "" {
		agent, ok := config["agent"]
		if ok {
			hiddenConfig := map[string]any{"agent": agent}
			replaceAgentPrompts(hiddenConfig, "{file:./"+filepath.Base(systemPromptFile)+"}")
			data, err := json.Marshal(hiddenConfig)
			if err != nil {
				return nil, fmt.Errorf("encode Kilo Code hidden reviewer config: %w", err)
			}
			configPath = filepath.Join(taskPromptRoot, "kilo.json")
			if err := os.WriteFile(configPath, append(data, '\n'), 0o600); err != nil {
				return nil, fmt.Errorf("write Kilo Code hidden reviewer config: %w", err)
			}
			delete(config, "agent")
		}
	}

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
			"printf *":                      "allow",
			"printf * | gh api *":           "allow",
			"printf * | ao review submit *": "allow",
		},
	}
	if taskPromptRoot != "" {
		promptPattern := filepath.ToSlash(filepath.Join(taskPromptRoot, "**"))
		permission["external_directory"] = map[string]string{promptPattern: "allow"}
	}
	config["permission"] = permission
	data, err := json.Marshal(config)
	if err != nil {
		return nil, fmt.Errorf("encode Kilo Code reviewer config: %w", err)
	}
	assignment := configAssignmentPrefix + string(data)

	if assignmentIndex >= 0 {
		out := append([]string(nil), argv...)
		out[assignmentIndex] = assignment
		if configPath != "" {
			out = append(out[:assignmentIndex+1], append([]string{"KILO_CONFIG=" + configPath}, out[assignmentIndex+1:]...)...)
		}
		return out, nil
	}
	return append([]string{"env", assignment}, argv...), nil
}

func replaceAgentPrompts(config map[string]any, prompt string) {
	agents, ok := config["agent"].(map[string]any)
	if !ok {
		return
	}
	for _, value := range agents {
		settings, ok := value.(map[string]any)
		if !ok {
			continue
		}
		settings["prompt"] = prompt
	}
}

// ReviewMessage returns the centrally-authored task for an existing pane.
func (r *Reviewer) ReviewMessage(_ context.Context, inv ports.ReviewInvocation) (string, error) {
	return inv.Prompt, nil
}

// ReviewCancel stops the active Kilo Code reviewer turn while preserving the
// terminal pane for inspection.
func (r *Reviewer) ReviewCancel(context.Context) (ports.ReviewCancelSpec, error) {
	return ports.ReviewCancelSpec{Mode: ports.ReviewCancelInterrupt, Interrupts: 2}, nil
}
