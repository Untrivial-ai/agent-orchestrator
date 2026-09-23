// Package kimchiacp binds the user's own Kimchi installation to AO's
// reusable ACP Chat transport.
package kimchiacp

import (
	"context"
	"log/slog"
	"strings"

	acpdriver "github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/acp"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/nativeacp"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// New launches `kimchi --mode acp` from the exact binary resolved by the
// existing Kimchi agent plugin. Login, models, settings, and updates remain
// owned by the user's Kimchi installation.
func New(plugin nativeacp.Plugin, log *slog.Logger) ports.ChatDriver {
	return nativeacp.New(plugin, nativeacp.Config{
		Harness:        domain.HarnessKimchi,
		Configure:      configure,
		SessionOptions: sessionOptions,
		VersionProbe:   versionProbe,
	}, log)
}

func configure(_ context.Context, cfg acpdriver.LaunchConfig) ([]string, map[string]string, error) {
	args := []string{"--mode", "acp"}
	if model := strings.TrimSpace(cfg.Model); model != "" {
		args = append(args, "--model", model)
	}
	appendPermissionFlags(&args, cfg.Permissions)
	if strings.TrimSpace(cfg.SystemPrompt) != "" {
		args = append(args, "--append-system-prompt", cfg.SystemPrompt)
	}
	return args, nil, nil
}

// appendPermissionFlags mirrors the TUI adapter's launch-time permission
// mapping: --auto for accept-edits/auto, --yolo for bypass-permissions,
// and no flag for default. This ensures the initial permission mode reaches
// Kimchi even when the ACP runtime setters (session/set_mode,
// session/set_config_option) are unsupported (-32601).
func appendPermissionFlags(cmd *[]string, permissions ports.PermissionMode) {
	switch ports.NormalizePermissionMode(permissions) {
	case ports.PermissionModeAcceptEdits, ports.PermissionModeAuto:
		*cmd = append(*cmd, "--auto")
	case ports.PermissionModeBypassPermissions:
		*cmd = append(*cmd, "--yolo")
	}
}

// sessionMode returns "" because Kimchi does not implement the legacy
// session/set_mode ACP method (returns -32601). Permission mode changes
// are applied through the "permissions-mode" config option instead — see
// sessionOptions.

func sessionOptions(settings ports.ChatTurnSettings) []acpdriver.SessionOption {
	var options []acpdriver.SessionOption
	if settings.Model != "" {
		options = append(options, acpdriver.SessionOption{ID: "model", Value: settings.Model})
	}
	if mode := kimchiPermissionMode(settings.Approval); mode != "" {
		options = append(options, acpdriver.SessionOption{ID: "permissions-mode", Value: mode})
	}
	return options
}

// kimchiPermissionMode maps AO's permission vocabulary onto Kimchi's native
// mode ids ("default", "plan", "auto", "yolo"). These are sent through
// session/set_config_option with configId "permissions-mode", which Kimchi
// implements. The legacy session/set_mode path is not used because Kimchi
// does not implement it.
func kimchiPermissionMode(permission ports.PermissionMode) string {
	switch ports.NormalizePermissionMode(permission) {
	case ports.PermissionModeAcceptEdits, ports.PermissionModeAuto:
		return "auto"
	case ports.PermissionModeBypassPermissions:
		return "yolo"
	default:
		return ""
	}
}
