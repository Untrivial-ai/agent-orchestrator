package claudecode

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/hooksjson"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const (
	claudeSettingsDirName   = ".claude"
	claudeSettingsFileName  = "settings.local.json"
	claudeHookCommandPrefix = "ao hooks claude-code "
	claudeHookTimeout       = 30
)

// claudeStartupMatcher is referenced by pointer so SessionStart serializes with
// its required "startup" matcher.
var claudeStartupMatcher = "startup"

// claudeManagedHooks is the source of truth for the hooks AO installs:
// SessionStart (under the "startup" matcher), UserPromptSubmit, the tool-use
// trio (PreToolUse, PostToolUse, PostToolUseFailure), PermissionRequest,
// Stop, Notification, and SessionEnd. They report normalized session metadata
// and activity-state signals back into AO's store (see DeriveActivityState).
// Notification and SessionEnd carry no matcher: each installs once and fires
// for every sub-type, and the handler filters on the payload's
// notification_type / reason field. The tool-use hooks also carry no matcher
// (fire for every tool): their payloads carry tool_name/tool_use_id, which
// lifecycle uses to clear a stale sticky `blocked` only when the specific
// approved tool finishes — the daemon-side precedence rule is what makes these
// signals safe against parallel-subagent traffic (the naive mapping without it
// was reverted in PR #5's review). PermissionRequest fires when a permission
// dialog appears and carries the blocking tool_name; `ao hooks` writes nothing
// to stdout, so installing it never injects a permission decision.
var claudeManagedHooks = []hooksjson.HookSpec{
	{Event: "SessionStart", Matcher: &claudeStartupMatcher, Command: claudeHookCommandPrefix + "session-start"},
	{Event: "UserPromptSubmit", Command: claudeHookCommandPrefix + "user-prompt-submit"},
	{Event: "PreToolUse", Command: claudeHookCommandPrefix + "pre-tool-use"},
	{Event: "PostToolUse", Command: claudeHookCommandPrefix + "post-tool-use"},
	{Event: "PostToolUseFailure", Command: claudeHookCommandPrefix + "post-tool-use-failure"},
	{Event: "PermissionRequest", Command: claudeHookCommandPrefix + "permission-request"},
	{Event: "Stop", Command: claudeHookCommandPrefix + "stop"},
	{Event: "Notification", Command: claudeHookCommandPrefix + "notification"},
	{Event: "SubagentStop", Command: claudeHookCommandPrefix + "subagent-stop"},
	{Event: "SessionEnd", Command: claudeHookCommandPrefix + "session-end"},
}

// claudeHooks manages AO's hooks in the workspace-local
// .claude/settings.local.json file.
var legacyClaudeHooks = hooksjson.Manager{
	Label:         "claude-code",
	CommandPrefix: claudeHookCommandPrefix,
	Timeout:       claudeHookTimeout,
	Path:          claudeSettingsPath,
	Managed:       claudeManagedHooks,
}

func claudeHooksForExecutable(executable string) hooksjson.Manager {
	prefix := quoteHookExecutable(executable) + " hooks claude-code "
	managed := make([]hooksjson.HookSpec, len(claudeManagedHooks))
	for i, spec := range claudeManagedHooks {
		managed[i] = spec
		managed[i].Command = prefix + strings.TrimPrefix(spec.Command, claudeHookCommandPrefix)
	}
	return hooksjson.Manager{Label: "claude-code", CommandPrefix: prefix, Timeout: claudeHookTimeout, Path: claudeSettingsPath, Managed: managed}
}

func quoteHookExecutable(executable string) string {
	if runtime.GOOS == "windows" {
		return `& "` + executable + `"`
	}
	return "'" + strings.ReplaceAll(executable, "'", `'\''`) + "'"
}

func currentClaudeHooks() (hooksjson.Manager, error) {
	executable, err := os.Executable()
	if err != nil {
		return hooksjson.Manager{}, fmt.Errorf("claude-code: resolve AO hook executable: %w", err)
	}
	return claudeHooksForExecutable(executable), nil
}

func claudeSettingsPath(workspacePath string) string {
	return filepath.Join(workspacePath, claudeSettingsDirName, claudeSettingsFileName)
}

// GetAgentHooks installs AO's Claude Code hooks, preserving user-defined hooks and unrelated settings.
func (p *Plugin) GetAgentHooks(ctx context.Context, cfg ports.WorkspaceHookConfig) error {
	hooks, err := currentClaudeHooks()
	if err != nil {
		return err
	}
	if err := legacyClaudeHooks.Uninstall(ctx, cfg.WorkspacePath); err != nil {
		return err
	}
	return hooks.Install(ctx, cfg.WorkspacePath)
}

// UninstallHooks removes AO's Claude Code hooks, leaving user-defined hooks untouched.
func (p *Plugin) UninstallHooks(ctx context.Context, workspacePath string) error {
	hooks, err := currentClaudeHooks()
	if err != nil {
		return err
	}
	if err := hooks.Uninstall(ctx, workspacePath); err != nil {
		return err
	}
	return legacyClaudeHooks.Uninstall(ctx, workspacePath)
}

// AreHooksInstalled reports whether any AO Claude Code hook is present.
func (p *Plugin) AreHooksInstalled(ctx context.Context, workspacePath string) (bool, error) {
	hooks, err := currentClaudeHooks()
	if err != nil {
		return false, err
	}
	installed, err := hooks.AreInstalled(ctx, workspacePath)
	if err != nil || installed {
		return installed, err
	}
	return legacyClaudeHooks.AreInstalled(ctx, workspacePath)
}
