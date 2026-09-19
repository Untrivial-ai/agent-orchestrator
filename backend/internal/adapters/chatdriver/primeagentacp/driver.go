// Package primeagentacp binds the user's own Prime Agent installation to AO's
// reusable ACP Chat transport.
//
// Prime Agent exposes ACP natively via `prime-agent --mode acp`; AO launches the
// exact binary resolved by the existing Prime Agent plugin, so providers,
// models, skills, and credentials remain the user's own.
package primeagentacp

import (
	"context"
	"log/slog"

	acpdriver "github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/acp"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/nativeacp"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// New launches `prime-agent --mode acp` from the exact binary resolved by the
// existing Prime Agent plugin.
//
// Prime Agent's ACP profile deliberately exposes one session per connection and
// no session/load or session/resume, so AO does not claim native history
// recovery. Its ACP mode is a trusted-code boundary with no permission
// requests, so approvals are reported unsupported and AO admits Prime Agent
// Chat only through the explicit per-session bypass fallback, matching Pi.
//
// Verified against prime-agent 0.7.2: initialize advertises
// loadSession:false, session/new advertises no config options, and both
// session/set_config_option and session/set_model answer -32601 method not
// found. AO therefore sends no session selectors at all.
func New(plugin nativeacp.Plugin, log *slog.Logger) ports.ChatDriver {
	return nativeacp.New(plugin, nativeacp.Config{
		Harness: domain.HarnessPrimeAgent,
		// Only the two that differ from nativeacp's defaults.
		Capabilities: ports.ChatCapabilities{
			ports.ChatCapabilityApprovals: false,
			ports.ChatCapabilityResume:    false,
		},
		Configure: configure,
	}, log)
}

// configure builds the `prime-agent --mode acp` argv. Prime Agent reads standing
// instructions from its own AGENTS.md/config surfaces and accepts no system
// prompt or model flag in ACP mode, so configure forwards neither.
func configure(_ context.Context, _ acpdriver.LaunchConfig) ([]string, map[string]string, error) {
	return []string{"--mode", "acp"}, nil, nil
}
