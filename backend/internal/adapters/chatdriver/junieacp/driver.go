package junieacp

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/junie"
	acpdriver "github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/acp"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/nativeacp"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// New returns an unregistered native ACP binding. Registration remains gated
// on an authenticated conformance run against a stable Junie release.
func New(plugin nativeacp.Plugin, runtimeFiles junie.RuntimeFileBuilder, log *slog.Logger) ports.ChatDriver {
	return nativeacp.New(plugin, nativeacp.Config{Harness: domain.AgentHarness("junie"), Configure: configure(runtimeFiles), ValidateTurnSettings: validateTurnSettings}, log)
}

func configure(builder junie.RuntimeFileBuilder) nativeacp.Configure {
	return func(ctx context.Context, cfg acpdriver.LaunchConfig) ([]string, map[string]string, error) {
		files, err := builder.Prepare(ctx, junie.RuntimeFileRequest{DataDir: cfg.DataDir, SessionID: string(cfg.SessionID), SystemPrompt: cfg.SystemPrompt})
		if err != nil {
			return nil, nil, err
		}
		return []string{"--acp", "true", "--skip-update-check", "--config-location", files.ConfigPath, "--guidelines-filename", files.GuidelinesPath}, nil, nil
	}
}

func validateTurnSettings(_ ports.PermissionMode, settings ports.ChatTurnSettings) error {
	if strings.TrimSpace(settings.Model) != "" || strings.TrimSpace(settings.Effort) != "" {
		return fmt.Errorf("%w: Junie ACP option IDs are not enabled until live conformance records them", acpdriver.ErrACPSetterUnsupported)
	}
	if mode := ports.NormalizePermissionMode(settings.Approval); mode != ports.PermissionModeDefault {
		return fmt.Errorf("%w: Junie ACP supports only its default approval mode; requested %q",
			ports.ErrChatPermissionModeUnsupported, mode)
	}
	return nil
}
