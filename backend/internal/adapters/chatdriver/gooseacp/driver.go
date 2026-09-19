// Package gooseacp binds the user's own Goose (Block) installation to AO's
// reusable ACP Chat transport.
//
// Goose exposes ACP natively via the `goose acp` subcommand; AO launches the
// exact binary resolved by the existing Goose agent plugin, so providers,
// extensions, recipes, and credentials remain the user's own.
package gooseacp

import (
	"context"
	"log/slog"

	acpdriver "github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/acp"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/nativeacp"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// gooseModeEnvVar is the only permission-control surface Goose honors: the
// approval mode is read from this process env var, not from any CLI flag. The
// name and value vocabulary mirror the existing Goose agent plugin so Chat and
// TUI sessions do not acquire different answers for the same AO mode.
const gooseModeEnvVar = "GOOSE_MODE"

// New launches `goose acp` from the exact binary resolved by the existing Goose
// agent plugin. Models and extensions come from the live ACP session
// advertisements; Goose owns tools, recipes, and auth.
func New(plugin nativeacp.Plugin, log *slog.Logger) ports.ChatDriver {
	return nativeacp.New(plugin, nativeacp.Config{
		Harness:              domain.HarnessGoose,
		Configure:            configure,
		ValidateTurnSettings: acpdriver.ApprovalFixedAtLaunch("Goose ACP approval mode", gooseMode),
	}, log)
}

// configure builds the `goose acp` argv and applies AO's permission mode through
// GOOSE_MODE. Goose's `acp` subcommand accepts no model or system-prompt flag,
// so AO forwards neither: model selection comes from Goose's own config and the
// session's advertised options, and standing instructions are not injectable
// over this surface. This is documented in docs/harnesses/acp-bindings.md.
func configure(_ context.Context, cfg acpdriver.LaunchConfig) ([]string, map[string]string, error) {
	args := []string{"acp"}
	if mode := gooseMode(cfg.Permissions); mode != "" {
		return args, map[string]string{gooseModeEnvVar: mode}, nil
	}
	return args, nil, nil
}

// gooseMode maps an AO permission mode onto Goose's GOOSE_MODE value, matching
// the TUI adapter: default leaves Goose's own config in charge, accept-edits
// auto-approves safe edits, and auto/bypass select Goose's fully-autonomous
// mode.
func gooseMode(mode ports.PermissionMode) string {
	switch ports.NormalizePermissionMode(mode) {
	case ports.PermissionModeAcceptEdits:
		return "smart_approve"
	case ports.PermissionModeAuto, ports.PermissionModeBypassPermissions:
		return "auto"
	default:
		return ""
	}
}
