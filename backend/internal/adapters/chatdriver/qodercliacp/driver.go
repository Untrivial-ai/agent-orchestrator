// Package qodercliacp binds the user's own Qoder CLI installation to AO's
// reusable ACP Chat transport.
package qodercliacp

import (
	"context"
	"log/slog"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/qodercli"
	acpdriver "github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/acp"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/nativeacp"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// New launches `qodercli --acp` from the exact binary resolved by the existing
// Qoder CLI agent plugin. Authentication, model catalog, sessions and settings
// remain owned by that installation.
func New(plugin nativeacp.Plugin, log *slog.Logger) ports.ChatDriver {
	return newDriver(plugin, versionProbe, log)
}

func newDriver(plugin nativeacp.Plugin, probe nativeacp.VersionProbe, log *slog.Logger) ports.ChatDriver {
	return nativeacp.New(plugin, nativeacp.Config{
		Harness: domain.HarnessQodercli,
		// Streaming, tools, approvals, interrupt and resume come from the shared
		// native-ACP defaults. Plans and diffs are added because Qoder CLI emits
		// both as typed session updates.
		//
		// Usage is declared because the shared transport reads it off the prompt
		// response, which is where Qoder CLI reports it (it sends no
		// usage_update notification).
		//
		// History is deliberately NOT declared: Qoder CLI returns from
		// session/load before its history replay finishes, while AO's capture
		// window closes when that call returns, so replayed history could
		// arrive after AO stopped listening.
		Capabilities: ports.ChatCapabilities{
			ports.ChatCapabilityPlans: true,
			ports.ChatCapabilityDiffs: true,
			ports.ChatCapabilityUsage: true,
		},
		Configure:      configure,
		SessionMode:    sessionMode,
		SessionOptions: sessionOptions,
		VersionProbe:   probe,
	}, log)
}

func configure(_ context.Context, cfg acpdriver.LaunchConfig) ([]string, map[string]string, error) {
	args := []string{"--acp"}
	if model := strings.TrimSpace(cfg.Model); model != "" {
		args = append(args, "--model", model)
	}
	// ACP reads no per-request system prompt, so standing instructions have to
	// be process input.
	if prompt := strings.TrimSpace(cfg.SystemPrompt); prompt != "" {
		args = append(args, "--append-system-prompt", prompt)
	}
	settings, err := qodercli.ChatSettingsJSON()
	if err != nil {
		return nil, nil, err
	}
	return append(args, "--settings", settings), nil, nil
}

// sessionMode maps AO's approval vocabulary onto Qoder CLI's ACP mode ids.
// Empty leaves the provider's own default in place. Unlike OMP, Qoder CLI
// accepts mode changes mid-session, so no turn-settings validator is needed.
func sessionMode(mode ports.PermissionMode) string {
	switch ports.NormalizePermissionMode(mode) {
	case ports.PermissionModeAcceptEdits:
		return "acceptEdits"
	case ports.PermissionModeAuto:
		return "auto"
	case ports.PermissionModeBypassPermissions:
		return "yolo"
	default:
		return ""
	}
}

// sessionOptions maps AO's durable model choice onto Qoder CLI's advertised
// model selector. The id must match one the agent reported.
func sessionOptions(settings ports.ChatTurnSettings) []acpdriver.SessionOption {
	if model := strings.TrimSpace(settings.Model); model != "" {
		return []acpdriver.SessionOption{{ID: "model", Value: model}}
	}
	return nil
}
