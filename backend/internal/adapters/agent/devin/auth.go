package devin

import (
	"context"
	"os"
	"strings"
	"time"

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
	if status, ok, err := devinLocalAuthStatus(ctx, binary); err != nil {
		return ports.AgentAuthStatusUnknown, err
	} else if ok {
		return status, nil
	}
	return ports.AgentAuthStatusUnknown, nil
}

func devinLocalAuthStatus(ctx context.Context, binary string) (ports.AgentAuthStatus, bool, error) {
	if err := ctx.Err(); err != nil {
		return ports.AgentAuthStatusUnknown, false, err
	}
	if key := strings.TrimSpace(os.Getenv("DEVIN_API_KEY")); strings.HasPrefix(key, "cog_") && len(key) > len("cog_") {
		return ports.AgentAuthStatusAuthorized, true, nil
	}
	status, err := authprobe.CLIStatusWithTimeout(ctx, binary, [][]string{{"auth", "status"}}, 8*time.Second)
	if err != nil {
		return ports.AgentAuthStatusUnknown, false, err
	}
	if status != ports.AgentAuthStatusUnknown {
		return status, true, nil
	}
	return ports.AgentAuthStatusUnknown, false, nil
}
