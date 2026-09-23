// Package vibeacp binds the user's own Mistral Vibe installation to AO's
// reusable ACP Chat transport.
//
// Vibe ships its ACP server as a separate `vibe-acp` executable rather than an
// `acp` subcommand of `vibe`. AO launches that exact user-installed binary and
// keeps the existing Vibe agent plugin as the canonical auth probe; login,
// providers, models, agents, and MCP configuration remain the user's own.
package vibeacp

import (
	"context"
	"log/slog"

	acpsdk "github.com/coder/acp-go-sdk"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/binaryutil"
	acpdriver "github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/acp"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/nativeacp"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// vibePlugin is the subset of AO's existing Vibe agent plugin the Chat driver
// reuses for binary resolution and local auth probing.
type vibePlugin interface {
	ResolveBinary(context.Context) (string, error)
	AuthStatus(context.Context) (ports.AgentAuthStatus, error)
}

// acpAdapter presents the separately distributed `vibe-acp` executable as this
// harness's binary on the shared native ACP path, while auth is still probed
// through the Vibe CLI plugin itself.
type acpAdapter struct{ vibePlugin }

func (a acpAdapter) ResolveBinary(ctx context.Context) (string, error) {
	return resolveVibeACPBinary(ctx, a.vibePlugin)
}

// New constructs Vibe's Chat driver over the existing Vibe agent plugin.
//
// Vibe resolves approvals through ACP permission requests, so AO's stronger
// modes are answered per request rather than by a launch flag; a mid-session
// approval change takes effect on the next tool call. Automatic modes select
// allow_once so Vibe never caches a grant that survives a later downgrade.
//
// accept-edits is deliberately not resolved here. Verified against Vibe
// 2.25.5: its request_permission sends a ToolCallUpdate carrying only a
// tool_call_id and no tool kind (vibe/acp/agent.py), so AO cannot tell a file
// edit from a shell command. accept-edits therefore prompts like default,
// rather than auto-approving a call that might not be an edit. auto and bypass
// are unaffected: Vibe always offers allow_once, allow_always,
// allow_always_permanent, and reject_once, and AO selects allow_once so every
// call is re-evaluated against the current AO mode.
func New(plugin vibePlugin, log *slog.Logger) ports.ChatDriver {
	return nativeacp.New(acpAdapter{plugin}, nativeacp.Config{
		Harness:          domain.HarnessVibe,
		Configure:        configure,
		SessionOptions:   acpdriver.ModelOption,
		PermissionPolicy: permissionPolicy,
	}, log)
}

var standardPermissionPolicy = acpdriver.StandardPermissionPolicy()

func permissionPolicy(
	mode ports.PermissionMode,
	params acpsdk.RequestPermissionRequest,
) (acpsdk.PermissionOptionId, bool) {
	switch ports.NormalizePermissionMode(mode) {
	case ports.PermissionModeAuto, ports.PermissionModeBypassPermissions:
		return acpdriver.PermissionOption(params.Options, acpsdk.PermissionOptionKindAllowOnce)
	default:
		return standardPermissionPolicy(mode, params)
	}
}

// configure adds nothing: `vibe-acp` takes no AO-relevant flag, and standing
// instructions are not injectable over its ACP mode.
func configure(context.Context, acpdriver.LaunchConfig) ([]string, map[string]string, error) {
	return nil, nil, nil
}

// resolveVibeACPBinary finds the `vibe-acp` executable. Vibe's one-line and uv
// installers place it beside `vibe`, so the sibling of the plugin-resolved
// binary is preferred; PATH and common user install locations are the fallback.
func resolveVibeACPBinary(ctx context.Context, plugin vibePlugin) (string, error) {
	vibeBinary, err := plugin.ResolveBinary(ctx)
	if err != nil {
		return "", err
	}
	if sibling := binaryutil.SiblingBinary(vibeBinary, vibeACPSpec); sibling != "" {
		return sibling, nil
	}
	return binaryutil.ResolveBinary(ctx, vibeACPSpec)
}

var vibeACPSpec = binaryutil.BinarySpec{
	Label:         "vibe-acp",
	Names:         []string{"vibe-acp"},
	WinNames:      []string{"vibe-acp.exe", "vibe-acp.cmd", "vibe-acp"},
	UnixPaths:     []string{"/usr/local/bin/vibe-acp", "/opt/homebrew/bin/vibe-acp"},
	UnixHomePaths: [][]string{{".local", "bin", "vibe-acp"}},
}
