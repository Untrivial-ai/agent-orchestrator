// Package auggieacp binds the user's own Auggie (Augment Code) installation to
// AO's reusable ACP Chat transport.
//
// Auggie exposes ACP natively via `auggie --acp`; AO launches the exact binary
// resolved by the existing Auggie agent plugin, so login, subscription, rules,
// MCP configuration, and settings remain the user's own. No path scrapes
// terminal output or packages a second provider CLI.
package auggieacp

import (
	"context"
	"log/slog"
	"strings"

	acpdriver "github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/acp"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/nativeacp"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// New launches `auggie --acp` from the exact binary resolved by the existing
// Auggie agent plugin. Models and slash commands come from the live ACP session
// advertisements. Auggie owns tools, rules, and auth; AO adds a session-scoped
// rules file and resolves permissions through ACP.
func New(plugin nativeacp.Plugin, log *slog.Logger) ports.ChatDriver {
	return nativeacp.New(plugin, nativeacp.Config{
		Harness:          domain.HarnessAuggie,
		Configure:        configure,
		PermissionPolicy: acpdriver.StandardPermissionPolicy(ports.PermissionModeBypassPermissions),
		SessionOptions:   acpdriver.ModelOption,
	}, log)
}

// bindingName names this binding's session-private prompt directory.
const bindingName = "auggie-acp"

// configure builds the ACP launch. Auggie has no CLI flag that injects standing
// instructions as text, so AO writes the session's system prompt to a
// session-private rules file and passes it through Auggie's repeatable
// `--rules` flag.
func configure(ctx context.Context, cfg acpdriver.LaunchConfig) ([]string, map[string]string, error) {
	args := []string{"--acp"}
	if model := strings.TrimSpace(cfg.Model); model != "" {
		args = append(args, "--model", model)
	}
	if prompt := strings.TrimSpace(cfg.SystemPrompt); prompt != "" {
		path, err := acpdriver.WriteSessionPrompt(ctx, cfg, bindingName, "ao-standing-rules.md", prompt)
		if err != nil {
			return nil, nil, err
		}
		args = append(args, "--rules", path)
	}
	return args, nil, nil
}
