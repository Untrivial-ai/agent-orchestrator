// Package copilotacp binds the user's own GitHub Copilot CLI installation to
// AO's reusable ACP Chat transport.
package copilotacp

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/copilot"
	acpdriver "github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/acp"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/nativeacp"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// New launches `copilot --acp` from the exact binary resolved by the existing
// Copilot agent plugin. Login, entitlement, model access, MCP servers, and
// settings all remain owned by that installation.
//
// Copilot's ACP server enforces approval itself: it raises
// session/request_permission for tool calls the launch flags did not
// pre-approve, and a denial fails the tool call rather than running it anyway.
// So this binding keeps the transport's default approvals capability instead of
// the bypass-only admission Pi Chat needs.
func New(plugin nativeacp.Plugin, log *slog.Logger) ports.ChatDriver {
	return nativeacp.New(plugin, nativeacp.Config{
		Harness: domain.HarnessCopilot,
		Capabilities: ports.ChatCapabilities{
			// Copilot streams usage_update and reports per-turn token counts.
			// It has no ACP diff content blocks and no plan updates: edits arrive
			// as ordinary edit tool calls with a unified diff inside rawOutput.
			ports.ChatCapabilityUsage: true,
		},
		Configure:            configure,
		LaunchSessionOptions: launchSessionOptions,
		SessionOptions:       sessionOptions,
		ValidateTurnSettings: validateTurnSettings,
	}, log)
}

// configure builds the `copilot --acp` argv. Copilot takes ACP as a flag rather
// than a subcommand, so there is no `copilot acp` to mirror kimi or omp with.
//
// --model is honored under --acp (the new session reports the requested model as
// its current one). --agent is not: it parses and is then ignored, so the
// standing instructions are installed here and selected by id in
// launchSessionOptions.
func configure(ctx context.Context, cfg acpdriver.LaunchConfig) ([]string, map[string]string, error) {
	args := []string{"--acp"}
	if model := strings.TrimSpace(cfg.Model); model != "" {
		args = append(args, "--model", model)
	}
	args = append(args, copilot.ApprovalArgs(cfg.Permissions)...)
	if err := copilot.PrepareACPAgentProfile(
		ctx, cfg.WorkspacePath, string(cfg.SessionID), cfg.SystemPrompt); err != nil {
		return nil, nil, err
	}
	return args, nil, nil
}

// launchSessionOptions selects the per-session custom-agent profile written by
// configure. Copilot enumerates .github/agents at process start and offers each
// profile as a value of its "agent" config option, so the session carries AO's
// standing instructions exactly as the TUI's --agent launch does.
func launchSessionOptions(cfg acpdriver.LaunchConfig) []acpdriver.SessionOption {
	agent := copilot.ACPAgentName(string(cfg.SessionID), cfg.SystemPrompt)
	if agent == "" {
		return nil
	}
	return []acpdriver.SessionOption{{ID: "agent", Value: agent}}
}

// sessionOptions maps AO's per-turn choices onto the config option ids Copilot
// advertises: "model", "reasoning_effort", and "allow_all". Copilot implements
// session/set_config_option, so these apply to a live session.
//
// Copilot's session modes (agent, plan, autopilot) are task modes rather than an
// approval vocabulary, so approval rides on "allow_all" instead. That option is
// an override layer on top of whatever the launch flags established: turning it
// on bypasses everything, and turning it off restores the launch baseline rather
// than dropping to default. A session launched with --allow-tool write still
// auto-approves edit tool calls after an on/off round trip and still raises
// session/request_permission for shell execution, so writing "off" for a
// non-bypass turn is safe. validateTurnSettings has already rejected any turn
// whose mode that baseline cannot express.
func sessionOptions(settings ports.ChatTurnSettings) []acpdriver.SessionOption {
	var options []acpdriver.SessionOption
	if model := strings.TrimSpace(settings.Model); model != "" {
		options = append(options, acpdriver.SessionOption{ID: "model", Value: model})
	}
	if effort := strings.TrimSpace(settings.Effort); effort != "" {
		options = append(options, acpdriver.SessionOption{ID: "reasoning_effort", Value: effort})
	}
	if settings.Approval != "" {
		options = append(options, acpdriver.SessionOption{ID: "allow_all", Value: allowAll(settings.Approval)})
	}
	return options
}

// allowAll is the "allow_all" value that represents an AO permission mode.
func allowAll(mode ports.PermissionMode) string {
	if ports.NormalizePermissionMode(mode) == ports.PermissionModeBypassPermissions {
		return "on"
	}
	return "off"
}

// validateTurnSettings admits the approval changes Copilot can actually make on a
// live session and rejects the rest instead of ignoring them.
//
// Copilot's only runtime permission control is the "allow_all" toggle, so a
// session can move between full bypass and the baseline its launch flags
// established, and nowhere else. Accept-edits and auto come from --allow-tool
// write and --allow-all-tools, which are read once at process start: a session
// launched in default cannot become auto, and picking that in the turn settings
// bar fails with the mode it can reach rather than quietly staying on default.
// Restarting Chat applies any mode.
func validateTurnSettings(initial ports.PermissionMode, settings ports.ChatTurnSettings) error {
	if settings.Approval == "" {
		return nil
	}
	requested := ports.NormalizePermissionMode(settings.Approval)
	if requested == ports.PermissionModeBypassPermissions || requested == baselineMode(initial) {
		return nil
	}
	return fmt.Errorf(
		"%w: a Copilot Chat session launched in %s can only switch to %s or %s; restart Chat to run it in %s",
		acpdriver.ErrACPSetterUnsupported,
		ports.NormalizePermissionMode(initial), baselineMode(initial),
		ports.PermissionModeBypassPermissions, requested)
}

// baselineMode is the mode a session falls back to when allow_all is turned off:
// whatever its launch flags set. A session launched in bypass-permissions carries
// no other allow flag, so its baseline is default.
func baselineMode(initial ports.PermissionMode) ports.PermissionMode {
	mode := ports.NormalizePermissionMode(initial)
	if mode == ports.PermissionModeBypassPermissions {
		return ports.PermissionModeDefault
	}
	return mode
}
