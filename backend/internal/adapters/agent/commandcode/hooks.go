package commandcode

import (
	"context"
	"path/filepath"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/hooksjson"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const (
	commandCodeSettingsDirName   = ".commandcode"
	commandCodeSettingsFileName  = "settings.json"
	commandCodeHookCommandPrefix = "ao hooks command-code "
	commandCodeHookTimeout       = 30
)

var commandCodeManagedHooks = []hooksjson.HookSpec{
	{Event: "SessionStart", Command: commandCodeHookCommandPrefix + "session-start"},
	{Event: "PreToolUse", Command: commandCodeHookCommandPrefix + "pre-tool-use"},
	{Event: "PostToolUse", Command: commandCodeHookCommandPrefix + "post-tool-use"},
	{Event: "Stop", Command: commandCodeHookCommandPrefix + "stop"},
}

var commandCodeHooks = hooksjson.Manager{
	Label:         adapterID,
	CommandPrefix: commandCodeHookCommandPrefix,
	Timeout:       commandCodeHookTimeout,
	Path:          commandCodeHooksPath,
	Managed:       commandCodeManagedHooks,
}

func commandCodeHooksPath(workspacePath string) string {
	return filepath.Join(workspacePath, commandCodeSettingsDirName, commandCodeSettingsFileName)
}

// GetAgentHooks installs AO's Command Code hooks while preserving user settings.
func (p *Plugin) GetAgentHooks(ctx context.Context, cfg ports.WorkspaceHookConfig) error {
	return commandCodeHooks.Install(ctx, cfg.WorkspacePath)
}

// UninstallHooks removes only AO-owned Command Code hooks.
func (p *Plugin) UninstallHooks(ctx context.Context, workspacePath string) error {
	return commandCodeHooks.Uninstall(ctx, workspacePath)
}

// AreHooksInstalled reports whether AO owns a hook in this workspace.
func (p *Plugin) AreHooksInstalled(ctx context.Context, workspacePath string) (bool, error) {
	return commandCodeHooks.AreInstalled(ctx, workspacePath)
}
