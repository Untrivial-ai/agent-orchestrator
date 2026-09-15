package projectsummary

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const generationTimeout = 90 * time.Second

// GenerationRequest contains the prior prose and newly observed durable facts.
type GenerationRequest struct {
	Harness       domain.AgentHarness
	Model         string
	WorkspacePath string
	Existing      string
	Facts         domain.ProjectSummary
}

// NarrativeGenerator updates the running project narrative.
type NarrativeGenerator interface {
	Update(context.Context, GenerationRequest) (string, error)
}

type commandRunner interface {
	Run(context.Context, string, []string, string, []byte) ([]byte, error)
}

// CLIGenerator invokes the user's installed and authenticated agent CLI.
type CLIGenerator struct {
	agents    ports.AgentResolver
	readiness ports.AgentReadinessProvider
	runner    commandRunner
}

// NewCLIGenerator constructs a CLI-backed narrative generator.
func NewCLIGenerator(agents ports.AgentResolver, readiness ports.AgentReadinessProvider) *CLIGenerator {
	return &CLIGenerator{agents: agents, readiness: readiness, runner: osCommandRunner{}}
}

// Update asks the selected orchestrator provider to minimally revise the narrative.
func (g *CLIGenerator) Update(ctx context.Context, request GenerationRequest) (string, error) {
	if request.Harness != domain.HarnessCodex && request.Harness != domain.HarnessClaudeCode {
		return "", fmt.Errorf("project summary generation is unsupported for agent %q", request.Harness)
	}
	if g.readiness != nil {
		ready, err := g.readiness.EnsureAgentReadiness(ctx, string(request.Harness), domain.AgentReadinessPurposeLaunch)
		if err != nil {
			return "", fmt.Errorf("check %s readiness: %w", request.Harness, err)
		}
		if ready.EffectiveReadiness != domain.AgentReadinessReady {
			return "", fmt.Errorf("agent %q is unavailable: readiness is %s", request.Harness, ready.EffectiveReadiness)
		}
	}
	agent, ok := g.agents.Agent(request.Harness)
	if !ok {
		return "", fmt.Errorf("agent %q is not registered", request.Harness)
	}
	resolver, ok := agent.(ports.AgentBinaryResolver)
	if !ok {
		return "", fmt.Errorf("agent %q cannot resolve its CLI", request.Harness)
	}
	binary, err := resolver.ResolveBinary(ctx)
	if err != nil {
		return "", fmt.Errorf("resolve %s CLI: %w", request.Harness, err)
	}
	facts, err := json.Marshal(request.Facts)
	if err != nil {
		return "", fmt.Errorf("encode project facts: %w", err)
	}
	prompt := summaryPrompt(request.Existing, string(facts))
	args := []string{}
	switch request.Harness {
	case domain.HarnessCodex:
		args = []string{"exec", "--ephemeral", "--sandbox", "read-only", "--skip-git-repo-check", "--color", "never"}
		if request.Model != "" {
			args = append(args, "--model", request.Model)
		}
		args = append(args, "-")
	case domain.HarnessClaudeCode:
		args = []string{"--print", "--tools", "", "--output-format", "text"}
		if request.Model != "" {
			args = append(args, "--model", request.Model)
		}
		args = append(args, prompt)
		prompt = ""
	}
	runCtx, cancel := context.WithTimeout(ctx, generationTimeout)
	defer cancel()
	out, err := g.runner.Run(runCtx, binary, args, request.WorkspacePath, []byte(prompt))
	if err != nil {
		return "", fmt.Errorf("generate project summary with %s: %w", request.Harness, err)
	}
	narrative := strings.TrimSpace(string(out))
	if narrative == "" {
		return "", fmt.Errorf("generate project summary with %s: empty response", request.Harness)
	}
	return narrative, nil
}

func summaryPrompt(existing, facts string) string {
	return "Update the running project summary from the durable facts below. Return only the revised summary as concise plain text. Preserve existing wording wherever its facts remain true. Revise only facts that changed. Do not infer from transcripts and do not mention these instructions.\n\nExisting summary:\n" + existing + "\n\nCurrent facts:\n" + facts
}

type osCommandRunner struct{}

func (osCommandRunner) Run(ctx context.Context, binary string, args []string, dir string, stdin []byte) ([]byte, error) {
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Dir = dir
	cmd.Stdin = bytes.NewReader(stdin)
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return nil, fmt.Errorf("%w: %s", err, strings.TrimSpace(string(exitErr.Stderr)))
		}
		return nil, err
	}
	return out, nil
}
