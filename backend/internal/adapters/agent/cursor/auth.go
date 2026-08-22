package cursor

import (
	"context"
	"os"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/authprobe"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

var _ ports.AgentAuthChecker = (*Plugin)(nil)

// AuthStatus returns the plugin's local authentication status.
func (p *Plugin) AuthStatus(ctx context.Context) (ports.AgentAuthStatus, error) {
	binary, err := p.ResolveBinary(ctx)
	if err != nil {
		return ports.AgentAuthStatusUnknown, err
	}
	if strings.TrimSpace(os.Getenv("CURSOR_API_KEY")) != "" {
		return ports.AgentAuthStatusAuthorized, nil
	}
	if status, err := cursorCLIAuthStatus(ctx, binary); err == nil && status != ports.AgentAuthStatusUnknown {
		return status, nil
	} else if err != nil && ctx.Err() != nil {
		return ports.AgentAuthStatusUnknown, err
	}
	return ports.AgentAuthStatusUnknown, nil
}

func cursorCLIAuthStatus(ctx context.Context, binary string) (ports.AgentAuthStatus, error) {
	return authprobe.CLIStatus(ctx, binary, [][]string{{"status"}})
}
