package claudecode

import (
	"context"
	"path/filepath"
	"runtime"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/hooksjson"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const (
	claudeSettingsDirName  = ".claude"
	claudeSettingsFileName = "settings.local.json"
	// Claude shell-form hooks use sh on POSIX and Git Bash or PowerShell on
	// Windows. Select PowerShell explicitly on Windows rather than depending on
	// Git Bash availability: https://code.claude.com/docs/en/hooks#command-hook-fields
	claudePOSIXHookPrefix   = `"${AO_CLI:?AO_CLI is not set}" hooks claude-code `
	claudeWindowsHookPrefix = `if (-not $env:AO_CLI) { throw 'AO_CLI is not set' }; & "$env:AO_CLI" hooks claude-code `
	claudeHookTimeout       = 30
)

var claudeHookCommandPrefix = func() string {
	if runtime.GOOS == "windows" {
		return claudeWindowsHookPrefix
	}
	return claudePOSIXHookPrefix
}()

var claudeHookShell = func() string {
	if runtime.GOOS == "windows" {
		return "powershell"
	}
	return "bash"
}()

// claudeSessionStartMatcher is referenced by pointer so SessionStart serializes
// with Claude's documented source matcher. "startup" alone misses --resume
// relaunches (and /clear, compact, fork), so the native session id is not
// confirmed under the new RuntimeLaunchID until the next user prompt (#4122).
var claudeSessionStartMatcher = "startup|resume|clear|compact|fork"

// claudeManagedHooks is the source of truth for the hooks AO installs:
// SessionStart (startup/resume/clear/compact/fork), UserPromptSubmit, the tool-use
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
	{Shell: claudeHookShell, Event: "SessionStart", Matcher: &claudeSessionStartMatcher, Command: claudeHookCommandPrefix + "session-start"},
	{Shell: claudeHookShell, Event: "UserPromptSubmit", Command: claudeHookCommandPrefix + "user-prompt-submit"},
	{Shell: claudeHookShell, Event: "PreToolUse", Command: claudeHookCommandPrefix + "pre-tool-use"},
	{Shell: claudeHookShell, Event: "PostToolUse", Command: claudeHookCommandPrefix + "post-tool-use"},
	{Shell: claudeHookShell, Event: "PostToolUseFailure", Command: claudeHookCommandPrefix + "post-tool-use-failure"},
	{Shell: claudeHookShell, Event: "PermissionRequest", Command: claudeHookCommandPrefix + "permission-request"},
	{Shell: claudeHookShell, Event: "Stop", Command: claudeHookCommandPrefix + "stop"},
	{Shell: claudeHookShell, Event: "Notification", Command: claudeHookCommandPrefix + "notification"},
	{Shell: claudeHookShell, Event: "SubagentStop", Command: claudeHookCommandPrefix + "subagent-stop"},
	{Shell: claudeHookShell, Event: "SessionEnd", Command: claudeHookCommandPrefix + "session-end"},
}

// claudeHooks manages AO's hooks in the workspace-local
// .claude/settings.local.json file.
var claudeHooks = hooksjson.Manager{
	Label:         "claude-code",
	CommandPrefix: claudeHookCommandPrefix,
	LegacyCommandPrefixes: func() []string {
		prefixes := []string{"ao hooks continue ", "ao hooks claude-code "}
		if runtime.GOOS == "windows" {
			return append(prefixes, claudePOSIXHookPrefix)
		}
		return append(prefixes, claudeWindowsHookPrefix)
	}(),
	Timeout: claudeHookTimeout,
	Path:    claudeSettingsPath,
	Managed: claudeManagedHooks,
}

func claudeSettingsPath(workspacePath string) string {
	return filepath.Join(workspacePath, claudeSettingsDirName, claudeSettingsFileName)
}

// GetAgentHooks installs AO's Claude Code hooks, preserving user-defined hooks and unrelated settings.
func (p *Plugin) GetAgentHooks(ctx context.Context, cfg ports.WorkspaceHookConfig) error {
	return claudeHooks.Install(ctx, cfg.WorkspacePath)
}

// UninstallHooks removes AO's Claude Code hooks, leaving user-defined hooks untouched.
func (p *Plugin) UninstallHooks(ctx context.Context, workspacePath string) error {
	return claudeHooks.Uninstall(ctx, workspacePath)
}

// AreHooksInstalled reports whether any AO Claude Code hook is present.
func (p *Plugin) AreHooksInstalled(ctx context.Context, workspacePath string) (bool, error) {
	return claudeHooks.AreInstalled(ctx, workspacePath)
}
