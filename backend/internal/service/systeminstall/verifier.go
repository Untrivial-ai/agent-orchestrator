package systeminstall

import (
	"context"
	"fmt"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const defaultVerifyTimeout = 5 * time.Second

// VerifyResult is the non-authenticating evidence collected after an install.
type VerifyResult struct {
	ResolvedPath string
	Output       string
}

// Verifier resolves the executable through the same adapter sessions use and
// runs a bounded version probe against that exact path.
type Verifier struct {
	agents   ports.AgentResolver
	commands ports.CommandRunner
	timeout  time.Duration
}

// NewVerifier creates a non-authenticating harness installation verifier.
func NewVerifier(agents ports.AgentResolver, commands ports.CommandRunner) *Verifier {
	return &Verifier{agents: agents, commands: commands, timeout: defaultVerifyTimeout}
}

// Resolve returns the executable selected by the harness adapter without
// probing authentication or running the binary.
func (v *Verifier) Resolve(ctx context.Context, target Target) (string, error) {
	if !IsAgentTarget(target) {
		return "", fmt.Errorf("systeminstall: %s is not a harness", target)
	}
	if v.agents == nil {
		return "", fmt.Errorf("systeminstall: harness verifier is not configured")
	}
	agent, ok := v.agents.Agent(domain.AgentHarness(target))
	if !ok {
		return "", fmt.Errorf("systeminstall: no adapter registered for %s", target)
	}
	resolver, ok := agent.(ports.AgentBinaryResolver)
	if !ok {
		return "", fmt.Errorf("systeminstall: adapter for %s cannot resolve its binary", target)
	}
	path, err := resolver.ResolveBinary(ctx)
	if err != nil {
		return "", fmt.Errorf("resolve installed %s binary: %w", target, err)
	}
	if path == "" {
		return "", fmt.Errorf("resolve installed %s binary: empty path", target)
	}
	return path, nil
}

// Verify resolves and version-probes the installed harness executable.
func (v *Verifier) Verify(ctx context.Context, target Target) (VerifyResult, error) {
	if v.commands == nil {
		return VerifyResult{}, fmt.Errorf("systeminstall: harness verifier is not configured")
	}

	probeCtx, cancel := context.WithTimeout(ctx, v.timeout)
	defer cancel()
	path, err := v.Resolve(probeCtx, target)
	if err != nil {
		return VerifyResult{}, err
	}
	out := &capturedOutput{max: maxOutputBytes}
	if err := v.commands.Run(probeCtx, []string{path, "--version"}, out, out); err != nil {
		return VerifyResult{ResolvedPath: path, Output: out.String()}, fmt.Errorf("run %s version probe: %w", target, err)
	}
	return VerifyResult{ResolvedPath: path, Output: out.String()}, nil
}
