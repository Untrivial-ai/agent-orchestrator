// Package kiroacp binds the user's own Kiro (AWS) installation to AO's reusable
// ACP Chat transport.
//
// Kiro exposes ACP natively via `kiro-cli acp`; AO launches the exact binary
// resolved by the existing Kiro agent plugin and selects the same workspace-local
// custom agent the TUI path uses, so login, models, steering, and MCP
// configuration remain the user's own.
package kiroacp

import (
	"context"
	"log/slog"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/kiro"
	acpdriver "github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/acp"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/nativeacp"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// New launches `kiro-cli acp --agent ao` from the exact binary resolved by the
// existing Kiro agent plugin. The custom agent carries AO's standing
// instructions (installed during workspace preparation) and Kiro owns models,
// steering, and auth. Kiro's ACP server advertises session/set_model and
// session/load, so models and native history are available to Chat.
//
// Kiro's mode is not fixed at launch: configure passes no trust flag and the
// permission policy runs per request against the conversation's current mode,
// so a mid-session approval change takes effect on the next tool call.
func New(plugin nativeacp.Plugin, log *slog.Logger) ports.ChatDriver {
	return nativeacp.New(plugin, nativeacp.Config{
		Harness:   domain.HarnessKiro,
		Configure: configure,
		PermissionPolicy: acpdriver.StandardPermissionPolicy(
			ports.PermissionModeAuto, ports.PermissionModeBypassPermissions),
		SessionOptions: acpdriver.ModelOption,
	}, log)
}

// configure builds the `kiro-cli acp` argv. AO selects the workspace-local
// custom agent by name; its system prompt is written by the Kiro agent plugin's
// GetAgentHooks, so configure forwards no separate system prompt. Kiro's
// tool-trust flags are documented for `chat` rather than `acp`, so AO resolves
// permissions through ACP requests instead of guessing a launch flag.
func configure(_ context.Context, _ acpdriver.LaunchConfig) ([]string, map[string]string, error) {
	return []string{"acp", "--agent", kiro.AgentName}, nil, nil
}
