package kiro

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
	binary, err := p.kiroBinary(ctx)
	if err != nil {
		return ports.AgentAuthStatusUnknown, err
	}
	if strings.TrimSpace(os.Getenv("KIRO_API_KEY")) != "" {
		return ports.AgentAuthStatusAuthorized, nil
	}
	return kiroWhoamiAuthStatus(ctx, binary)
}

func kiroWhoamiAuthStatus(ctx context.Context, binary string) (ports.AgentAuthStatus, error) {
	if binary == "" {
		return ports.AgentAuthStatusUnknown, nil
	}
	// Kiro documents `whoami` as its authentication-status command. Keep the
	// probe bounded so catalog refresh cannot hang on a broken CLI install.
	return authprobe.CLIStatus(ctx, binary, [][]string{{"whoami", "--format", "json"}})
}
