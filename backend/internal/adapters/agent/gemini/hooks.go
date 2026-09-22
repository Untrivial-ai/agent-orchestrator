package gemini

import (
	"context"
	"encoding/json"
	"path/filepath"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/hooksjson"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

var hooks = hooksjson.Manager{
	Label: "gemini", CommandPrefix: "ao hooks gemini ", Timeout: 30000,
	Path: func(ws string) string { return filepath.Join(ws, ".gemini", "settings.json") },
	Managed: []hooksjson.HookSpec{
		{Event: "SessionStart", Command: "ao hooks gemini session-start"},
		{Event: "BeforeAgent", Command: "ao hooks gemini user-prompt-submit"},
		{Event: "AfterAgent", Command: "ao hooks gemini stop"},
		{Event: "Notification", Command: "ao hooks gemini notification"},
		{Event: "AfterTool", Command: "ao hooks gemini after-tool"},
	},
}

// GetAgentHooks installs Gemini hooks. BeforeAgent receives AO's instructions through the native
// additionalContext response from ao hooks, preserving Gemini's system prompt.
func (p *Plugin) GetAgentHooks(ctx context.Context, cfg ports.WorkspaceHookConfig) error {
	return hooks.Install(ctx, cfg.WorkspacePath)
}

// UninstallHooks removes only AO-managed hook entries.
func (p *Plugin) UninstallHooks(ctx context.Context, ws string) error {
	return hooks.Uninstall(ctx, ws)
}

// AreHooksInstalled reports whether any AO hook entry exists.
func (p *Plugin) AreHooksInstalled(ctx context.Context, ws string) (bool, error) {
	return hooks.AreInstalled(ctx, ws)
}

// DeriveActivityState translates native hook events into AO activity.
func DeriveActivityState(event string, payload []byte) (domain.ActivityState, bool) {
	switch event {
	case "session-start", "user-prompt-submit", "after-tool":
		return domain.ActivityActive, true
	case "stop":
		return domain.ActivityIdle, true
	case "notification":
		var input struct {
			Type string `json:"notification_type"`
		}
		if json.Unmarshal(payload, &input) == nil && input.Type == "ToolPermission" {
			return domain.ActivityWaitingInput, true
		}
	}
	return "", false
}
