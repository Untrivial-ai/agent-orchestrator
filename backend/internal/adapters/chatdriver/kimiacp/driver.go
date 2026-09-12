// Package kimiacp binds the user's own Kimi Code installation to AO's
// reusable ACP Chat transport.
package kimiacp

import (
	"context"
	"fmt"
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
		Configure:            configure,
		SessionOptions:       sessionOptions,
		ValidateTurnSettings: validateTurnSettings,
	}, log)
}

func configure(ctx context.Context, cfg acpdriver.LaunchConfig) ([]string, map[string]string, error) {
	if err := validateTurnSettings(cfg.Permissions, ports.ChatTurnSettings{Approval: cfg.Permissions}); err != nil {
		return nil, nil, err
	}
	if err := kimi.PrepareACPInstructions(ctx, cfg.WorkspacePath, cfg.SystemPrompt); err != nil {
		return nil, nil, err
	}
	return []string{"acp"}, nil, nil
}

func validateTurnSettings(_ ports.PermissionMode, settings ports.ChatTurnSettings) error {
	if mode := ports.NormalizePermissionMode(settings.Approval); mode != ports.PermissionModeDefault {
		return fmt.Errorf("%w: Kimi ACP advertises only its default session mode; requested %q",
			ports.ErrChatPermissionModeUnsupported, mode)
	}
	return nil
}

// sessionOptions maps AO's durable model choice onto Kimi's advertised legacy
// model selector. The generic ACP transport routes it through session/set_model.
func sessionOptions(settings ports.ChatTurnSettings) []acpdriver.SessionOption {
	var options []acpdriver.SessionOption
	if settings.Model != "" {
		options = append(options, acpdriver.SessionOption{ID: "model", Value: settings.Model})
	}
	return options
}
