package qoder

import (
	"context"
	"path/filepath"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/hooksjson"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const hookCommandPrefix = "ao hooks qoder "

var managedHooks = []hooksjson.HookSpec{
	{Event: "SessionStart", Command: hookCommandPrefix + "session-start"},
	{Event: "UserPromptSubmit", Command: hookCommandPrefix + "user-prompt-submit"},
	{Event: "PermissionRequest", Command: hookCommandPrefix + "permission-request"},
	{Event: "PreToolUse", Command: hookCommandPrefix + "pre-tool-use"},
	{Event: "PostToolUse", Command: hookCommandPrefix + "post-tool-use"},
	{Event: "PostToolUseFailure", Command: hookCommandPrefix + "post-tool-use-failure"},
	{Event: "Stop", Command: hookCommandPrefix + "stop"},
	{Event: "SessionEnd", Command: hookCommandPrefix + "session-end"},
}

var qoderHooks = hooksjson.Manager{Label: "qoder", CommandPrefix: hookCommandPrefix, Timeout: 30, Path: func(workspace string) string { return filepath.Join(workspace, ".qoder", "settings.json") }, Managed: managedHooks}

func (p *Plugin) GetAgentHooks(ctx context.Context, cfg ports.WorkspaceHookConfig) error {
	return qoderHooks.Install(ctx, cfg.WorkspacePath)
}
func (p *Plugin) UninstallHooks(ctx context.Context, workspace string) error {
	return qoderHooks.Uninstall(ctx, workspace)
}
func (p *Plugin) AreHooksInstalled(ctx context.Context, workspace string) (bool, error) {
	return qoderHooks.AreInstalled(ctx, workspace)
}
