package systeminstall

import (
	"context"
	"fmt"
	"regexp"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const defaultVerifyTimeout = 5 * time.Second

// genericVersionPattern extracts the first dotted version-like token from
// `--version` output, e.g. "codex-cli 1.2.3" -> "1.2.3" or "cursor-agent
// 2026.08.11" -> "2026.08.11". Adapters whose output does not fit this shape
// implement ports.AgentVersionParser to override it.
var genericVersionPattern = regexp.MustCompile(`\d+(?:\.\d+){1,3}`)

// VerifyResult is the non-authenticating evidence collected after an install.
type VerifyResult struct {
	ResolvedPath string
	Output       string
	// Version is the parsed result of the version probe. Empty when the probe
	// output did not contain a recognizable version.
	Version string
}

// parseVersion extracts a display version from raw `--version` output,
// preferring an adapter's own parsing when it implements
// ports.AgentVersionParser over the generic pattern.
func parseVersion(agent ports.Agent, output string) string {
	if parser, ok := agent.(ports.AgentVersionParser); ok {
		if version, ok := parser.ParseVersionOutput(output); ok {
			return version
		}
	}
	return genericVersionPattern.FindString(output)
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
	if v.agents == nil {
		return VerifyResult{}, fmt.Errorf("systeminstall: harness verifier is not configured")
	}
	agent, ok := v.agents.Agent(domain.AgentHarness(target))
	if !ok {
		return VerifyResult{}, fmt.Errorf("systeminstall: no adapter registered for %s", target)
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
	return VerifyResult{ResolvedPath: path, Output: out.String(), Version: parseVersion(agent, out.String())}, nil
}
