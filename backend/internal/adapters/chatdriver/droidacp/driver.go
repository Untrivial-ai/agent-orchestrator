// Package droidacp binds the user's own Factory Droid installation to AO's
// reusable ACP Chat transport.
package droidacp

import (
	"context"
	"log/slog"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/droid"
	acpdriver "github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/acp"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/nativeacp"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// New launches Droid's native ACP daemon from the exact binary resolved by its
// existing agent plugin. Login, models, settings, and updates remain owned by
// the user's Droid installation.
func New(plugin nativeacp.Plugin, log *slog.Logger) ports.ChatDriver {
	return nativeacp.New(plugin, nativeacp.Config{
		Harness:   domain.HarnessDroid,
		Configure: configure,
	}, log)
}

func configure(_ context.Context, cfg acpdriver.LaunchConfig) ([]string, map[string]string, error) {
	settingsMode := cfg.Permissions
	bypass := ports.NormalizePermissionMode(cfg.Permissions) == ports.PermissionModeBypassPermissions
	if bypass {
		// Droid's ACP-daemon form is under `exec`, where full bypass exists.
		// The TUI adapter maps this to high because interactive Droid has no
		// equivalent flag; Chat can preserve AO's stronger requested meaning.
		settingsMode = ports.PermissionModeDefault
	}
	args, err := droid.PrepareRuntimeSettingsArgs(
		cfg.DataDir, string(cfg.SessionID), settingsMode, cfg.Model)
	if err != nil {
		return nil, nil, err
	}
	args = append(args, "exec", "--output-format", "acp-daemon")
	if bypass {
		args = append(args, "--skip-permissions-unsafe")
	}
	if strings.TrimSpace(cfg.SystemPrompt) != "" {
		args = append(args, "--append-system-prompt", cfg.SystemPrompt)
	}
	return args, nil, nil
}
