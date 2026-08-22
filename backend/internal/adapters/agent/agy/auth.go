package agy

import (
	"context"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

var _ ports.AgentAuthChecker = (*Plugin)(nil)

// AuthStatus returns the plugin's local authentication status.
func (p *Plugin) AuthStatus(ctx context.Context) (ports.AgentAuthStatus, error) {
	if _, err := p.ResolveBinary(ctx); err != nil {
		return ports.AgentAuthStatusUnknown, err
	}
	// Antigravity authenticates through its OS keyring and browser sign-in. The
	// official CLI does not expose a documented non-interactive credential file
	// or status command, so local auth cannot safely be inferred here.
	return ports.AgentAuthStatusUnknown, nil
}
