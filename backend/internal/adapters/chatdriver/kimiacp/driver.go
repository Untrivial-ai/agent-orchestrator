// Package kimiacp binds the user's own Kimi Code installation to AO's
// reusable ACP Chat transport.
package kimiacp

import (
	"log/slog"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/kimi"
	acpdriver "github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/acp"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/nativeacp"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// New launches `kimi acp` from the exact binary resolved by the existing Kimi
// agent plugin. Authentication, model discovery, sessions, and configuration
// remain owned by that installation.
func New(plugin nativeacp.Plugin, log *slog.Logger) ports.ChatDriver {
	return nativeacp.New(plugin, nativeacp.Config{
		Harness: domain.HarnessKimi,
		Capabilities: ports.ChatCapabilities{
			ports.ChatCapabilityHistory: true,
			ports.ChatCapabilityPlans:   true,
		},
		Configure:      configure,
		SessionOptions: sessionOptions,
	}, log)
}

func configure(cfg acpdriver.LaunchConfig) ([]string, map[string]string, error) {
	if err := kimi.PrepareACPInstructions(cfg.WorkspacePath, cfg.SystemPrompt); err != nil {
		return nil, nil, err
	}
	return []string{"acp"}, nil, nil
}

// sessionOptions maps AO's durable turn settings onto Kimi's native ACP
// config-option ids. Kimi advertises the complete model/thinking/mode catalog
// during session setup; AO keeps that provider-owned catalog intact for direct
// plan-mode and thinking selection in Chat.
func sessionOptions(settings ports.ChatTurnSettings) []acpdriver.SessionOption {
	var options []acpdriver.SessionOption
	if settings.Model != "" {
		options = append(options, acpdriver.SessionOption{ID: "model", Value: settings.Model})
	}
	if mode := permissionMode(settings.Approval); mode != "" {
		options = append(options, acpdriver.SessionOption{ID: "mode", Value: mode})
	}
	return options
}

func permissionMode(permission ports.PermissionMode) string {
	switch ports.NormalizePermissionMode(permission) {
	case ports.PermissionModeAcceptEdits, ports.PermissionModeAuto:
		return "auto"
	case ports.PermissionModeBypassPermissions:
		return "yolo"
	default:
		// Leave Kimi's configured/default mode unchanged. The provider-owned
		// config option remains available when the user explicitly wants to
		// switch back to default or enter plan mode.
		return ""
	}
}
