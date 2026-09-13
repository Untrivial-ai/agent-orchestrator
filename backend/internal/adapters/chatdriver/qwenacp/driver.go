// Package qwenacp binds the user's own Qwen Code installation to AO's reusable
// ACP Chat transport.
package qwenacp

import (
	"context"
	"log/slog"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/qwen"
	acpdriver "github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/acp"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/nativeacp"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// New launches `qwen --acp` from the exact binary resolved by the existing Qwen
// Code agent plugin. Login, models, settings, and updates remain owned by the
// user's Qwen installation. `--experimental-acp` is the deprecated alias; AO
// launches the stable flag.
func New(plugin nativeacp.Plugin, log *slog.Logger) ports.ChatDriver {
	return newDriver(plugin, versionProbe, log)
}

func newDriver(plugin nativeacp.Plugin, probe nativeacp.VersionProbe, log *slog.Logger) ports.ChatDriver {
	return nativeacp.New(plugin, nativeacp.Config{
		Harness:        domain.HarnessQwen,
		Configure:      configure,
		SessionMode:    sessionMode,
		SessionOptions: sessionOptions,
		VersionProbe:   probe,
		Capabilities: ports.ChatCapabilities{
			// Qwen Code's ACP mode ignores --approval-mode: it auto-executes tool
			// calls and never sends session/request_permission, so a permission
			// selector would carry a value the provider silently discards. Declare
			// approvals unavailable, like piacp, so AO's production floor admits
			// Qwen Chat only under the explicit bypass-permissions fallback.
			ports.ChatCapabilityApprovals: false,
		},
	}, log)
}

func configure(_ context.Context, cfg acpdriver.LaunchConfig) ([]string, map[string]string, error) {
	args := []string{"--acp"}
	qwen.AppendSessionFlags(&args, cfg.Permissions, cfg.Model)
	if prompt := strings.TrimSpace(cfg.SystemPrompt); prompt != "" {
		args = append(args, "--append-system-prompt", prompt)
	}
	return args, nil, nil
}

// sessionMode maps AO's permission vocabulary onto Qwen Code's advertised ACP
// mode ids (plan, default, auto-edit, auto, yolo). Empty leaves Qwen's own
// default in place. Qwen advertises these both as session/set_mode ids and as
// the "mode" config option.
func sessionMode(permission ports.PermissionMode) string {
	switch ports.NormalizePermissionMode(permission) {
	case ports.PermissionModeAcceptEdits:
		return "auto-edit"
	case ports.PermissionModeAuto:
		return "auto"
	case ports.PermissionModeBypassPermissions:
		return "yolo"
	default:
		return ""
	}
}

func sessionOptions(settings ports.ChatTurnSettings) []acpdriver.SessionOption {
	if model := strings.TrimSpace(settings.Model); model != "" {
		return []acpdriver.SessionOption{{ID: "model", Value: model}}
	}
	return nil
}
