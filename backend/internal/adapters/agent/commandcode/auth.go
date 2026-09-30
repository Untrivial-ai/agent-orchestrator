package commandcode

import (
	"context"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/authprobe"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

var _ ports.AgentAuthChecker = (*Plugin)(nil)

// commandCodeAuthProbeTimeout bounds `cmd status`, which performs local
// startup work before printing the status line.
const commandCodeAuthProbeTimeout = 5 * time.Second

// AuthStatus returns the plugin's local authentication status by running
// `cmd status`.
func (p *Plugin) AuthStatus(ctx context.Context) (ports.AgentAuthStatus, error) {
	binary, err := p.binary(ctx)
	if err != nil {
		return ports.AgentAuthStatusUnknown, err
	}
	return commandCodeAuthStatus(ctx, binary)
}

func commandCodeAuthStatus(ctx context.Context, binary string) (ports.AgentAuthStatus, error) {
	if err := ctx.Err(); err != nil {
		return ports.AgentAuthStatusUnknown, err
	}
	if strings.TrimSpace(binary) == "" {
		return ports.AgentAuthStatusUnknown, nil
	}
	runCtx, cancel := context.WithTimeout(ctx, commandCodeAuthProbeTimeout)
	defer cancel()
	// `cmd status` may exit non-zero when signed out, so classify whatever text
	// it printed instead of treating the exit code as a probe failure.
	out, _ := authprobe.CmdRunner(runCtx, binary, "status")
	return commandCodeAuthStatusFromOutput(string(out)), nil
}

// commandCodeAuthStatusFromOutput classifies `cmd status` output. Command Code
// prints "Authentication verified" when signed in, which the shared authprobe
// needles do not recognize, so an explicit "verified" is treated as authorized.
func commandCodeAuthStatusFromOutput(out string) ports.AgentAuthStatus {
	text := strings.ToLower(out)
	if strings.Contains(text, "not verified") {
		return ports.AgentAuthStatusUnauthorized
	}
	status := authprobe.StatusFromText(out)
	if status == ports.AgentAuthStatusUnknown && strings.Contains(text, "verified") {
		return ports.AgentAuthStatusAuthorized
	}
	return status
}
