// Package continueagent implements the Continue CLI agent adapter.
//
// Continue (https://docs.continue.dev/guides/cli) is Continue's terminal coding
// agent. Its binary is "cn" (npm package @continuedev/cli) and the AO harness /
// manifest id is the string "continue". The Go package and directory are named
// "continueagent" because "continue" is a reserved keyword.
//
// Tier B (Claude Code-compatible hooks): the Continue CLI natively reads Claude
// Code hook settings (.claude/settings.json and .claude/settings.local.json) and
// dispatches Claude-format hook events (SessionStart, UserPromptSubmit,
// PreToolUse, PostToolUse, Stop, Notification) with the standard hook payload
// (session_id, hook_event_name, hookSpecificOutput, permissionDecision,
// additionalContext). So we install Claude-shaped hook specs, but route them
// through the Continue hook token ("ao hooks continue <evt>") so activity and
// session metadata stay attributed to the Continue harness.
//
// Launch is interactive via `cn [--auto|--readonly] [--rule <rule>] [-- <prompt>]`.
// Restore continues a specific native session by id with `cn --fork <sessionId>`
// (Continue's `--resume` only continues the *last* session, so it cannot target
// a particular AO session).
package continueagent

import (
	"context"
	"path/filepath"
	"strings"
	"sync"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/agentbase"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/binaryutil"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/claudecode"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/hooksjson"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// adapterID is the AO harness / manifest id. It is the string "continue"
// (NOT the Go package name "continueagent").
const adapterID = "continue"

const (
	continueClaudeSettingsDirName  = ".claude"
	continueClaudeSettingsFileName = "settings.local.json"
	continueHookCommandPrefix      = "ao hooks continue "
	continueHookTimeout            = 30
)

var continueBinarySpec = binaryutil.BinarySpec{
	Label:         "cn",
	Names:         []string{"cn"},
	WinNames:      []string{"cn.cmd", "cn.exe", "cn"},
	UnixPaths:     []string{"/usr/local/bin/cn", "/opt/homebrew/bin/cn"},
	UnixHomePaths: binaryutil.NodeManagedUnixHomePaths("cn"),
	NodeManaged:   true,
	WinPaths: []binaryutil.WinPath{
		{Base: binaryutil.WinAppData, Parts: []string{"npm", "cn.cmd"}},
		{Base: binaryutil.WinAppData, Parts: []string{"npm", "cn.exe"}},
	},
}

var continueSessionStartMatcher = "startup|resume|clear|compact"

// continueManagedHooks is Claude Code's hook event shape with Continue-specific
// AO hook commands. Continue reads this file through its Claude compatibility
// layer, while `ao hooks continue` keeps activity and session metadata
// attributed to the Continue harness.
var continueManagedHooks = []hooksjson.HookSpec{
	{Event: "SessionStart", Matcher: &continueSessionStartMatcher, Command: continueHookCommandPrefix + "session-start"},
	{Event: "UserPromptSubmit", Command: continueHookCommandPrefix + "user-prompt-submit"},
	{Event: "PreToolUse", Command: continueHookCommandPrefix + "pre-tool-use"},
	{Event: "PostToolUse", Command: continueHookCommandPrefix + "post-tool-use"},
	{Event: "PostToolUseFailure", Command: continueHookCommandPrefix + "post-tool-use-failure"},
	{Event: "PermissionRequest", Command: continueHookCommandPrefix + "permission-request"},
	{Event: "Stop", Command: continueHookCommandPrefix + "stop"},
	{Event: "Notification", Command: continueHookCommandPrefix + "notification"},
	{Event: "SubagentStop", Command: continueHookCommandPrefix + "subagent-stop"},
	{Event: "SessionEnd", Command: continueHookCommandPrefix + "session-end"},
}

var continueHooks = hooksjson.Manager{
	Label:                 adapterID,
	CommandPrefix:         continueHookCommandPrefix,
	LegacyCommandPrefixes: []string{"ao hooks claude-code "},
	Timeout:               continueHookTimeout,
	Path:                  continueClaudeSettingsPath,
	Managed:               continueManagedHooks,
}

// Plugin is the Continue CLI agent adapter. It is safe for concurrent use; the
// binary path is resolved once and cached under binaryMu.
type Plugin struct {
	agentbase.Base
	binaryMu       sync.Mutex
	resolvedBinary string
}

// New returns a ready-to-register Continue adapter.
func New() *Plugin {
	return &Plugin{}
}

var _ adapters.Adapter = (*Plugin)(nil)
var _ ports.Agent = (*Plugin)(nil)

// Manifest returns the adapter's static self-description. ID is "continue".
func (p *Plugin) Manifest() adapters.Manifest {
	return adapters.Manifest{
		ID:          adapterID,
		Name:        "Continue",
		Description: "Run Continue CLI worker sessions.",
		Version:     "0.0.1",
		Capabilities: []adapters.Capability{
			adapters.CapabilityAgent,
		},
	}
}

// GetConfigSpec reports Continue's optional model slug override.
func (p *Plugin) GetConfigSpec(ctx context.Context) (ports.ConfigSpec, error) {
	return agentbase.ModelConfigSpec(ctx, "Model slug passed to `cn --model`.")
}

// GetLaunchCommand builds the Continue CLI argv for a fresh launch.
//
// AO sessions are long-lived terminal sessions, so prompted and promptless
// launches both stay interactive as `cn ...`. Permission flags map AO's 4 modes
// onto Continue's two booleans (--auto / --readonly); Default and AcceptEdits
// emit no flag so Continue resolves behavior from the user's config.
func (p *Plugin) GetLaunchCommand(ctx context.Context, cfg ports.LaunchConfig) (cmd []string, err error) {
	binary, err := p.continueBinary(ctx)
	if err != nil {
		return nil, err
	}

	cmd = []string{binary}
	appendApprovalFlags(&cmd, cfg.Permissions)
	agentbase.AppendModelFlag(&cmd, cfg.Config, "--model")
	appendSystemPromptRule(&cmd, cfg.SystemPrompt, cfg.SystemPromptFile)

	if cfg.Prompt != "" {
		cmd = append(cmd, "--", cfg.Prompt)
	}

	return cmd, nil
}

// GetPromptDeliveryStrategy reports how Continue receives the initial prompt.
// Prompted launches carry the prompt in `cn ... -- <prompt>`; promptless
// launches start interactively and have no command prompt to deliver.
func (p *Plugin) GetPromptDeliveryStrategy(ctx context.Context, cfg ports.LaunchConfig) (ports.PromptDeliveryStrategy, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if cfg.Prompt != "" {
		return ports.PromptDeliveryInCommand, nil
	}
	return ports.PromptDeliveryAfterStart, nil
}

// GetAgentHooks installs Claude Code-shaped hooks because the Continue CLI
// natively reads Claude Code hook settings.
//
// The installed commands are "ao hooks continue <evt>". Continue still fires
// Claude-format events, but the Continue token makes AO store activity under the
// actual harness instead of under claude-code.
func (p *Plugin) GetAgentHooks(ctx context.Context, cfg ports.WorkspaceHookConfig) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return continueHooks.Install(ctx, cfg.WorkspacePath)
}

// GetRestoreCommand builds `cn [--auto|--readonly] --fork <agentSessionId>` when
// a hook-captured native session id is available. ok=false otherwise (the manager
// falls back to a fresh launch). `--fork <id>` continues a specific session by
// id; Continue's `--resume` only continues the last session and so cannot target
// a particular AO session.
func (p *Plugin) GetRestoreCommand(ctx context.Context, cfg ports.RestoreConfig) (cmd []string, ok bool, err error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	agentSessionID := strings.TrimSpace(cfg.Session.Metadata[ports.MetadataKeyAgentSessionID])
	if agentSessionID == "" {
		return nil, false, nil
	}

	binary, err := p.continueBinary(ctx)
	if err != nil {
		return nil, false, err
	}

	cmd = make([]string, 0, 5)
	cmd = append(cmd, binary)
	appendApprovalFlags(&cmd, cfg.Permissions)
	agentbase.AppendModelFlag(&cmd, cfg.Config, "--model")
	appendSystemPromptRule(&cmd, cfg.SystemPrompt, cfg.SystemPromptFile)
	cmd = append(cmd, "--fork", agentSessionID)
	return cmd, true, nil
}

// SessionInfo reads hook-derived metadata. Since hook install is delegated to
// the claude hooks (via Continue's compat layer), the metadata keys are the
// claude ones ("title", "summary", "agentSessionId").
func (p *Plugin) SessionInfo(ctx context.Context, session ports.SessionRef) (ports.SessionInfo, bool, error) {
	if err := ctx.Err(); err != nil {
		return ports.SessionInfo{}, false, err
	}
	info, ok := agentbase.StandardSessionInfo(session)
	return info, ok, nil
}

// DeriveActivityState interprets Continue's Claude-compatible hook payloads.
func DeriveActivityState(event string, payload []byte) (domain.ActivityState, bool) {
	return claudecode.DeriveActivityState(event, payload)
}

// ResolveContinueBinary finds the `cn` binary (Continue CLI), searching PATH then
// common npm/global install locations. It returns a wrapped
// ports.ErrAgentBinaryNotFound when Continue is absent.
func ResolveContinueBinary(ctx context.Context) (string, error) {
	return binaryutil.ResolveBinary(ctx, continueBinarySpec)
}

func (p *Plugin) continueBinary(ctx context.Context) (string, error) {
	p.binaryMu.Lock()
	defer p.binaryMu.Unlock()

	if p.resolvedBinary != "" {
		return p.resolvedBinary, nil
	}

	binary, err := ResolveContinueBinary(ctx)
	if err != nil {
		return "", err
	}
	p.resolvedBinary = binary
	return binary, nil
}

func continueClaudeSettingsPath(workspacePath string) string {
	return filepath.Join(workspacePath, continueClaudeSettingsDirName, continueClaudeSettingsFileName)
}

// appendApprovalFlags maps AO's 4 permission modes onto Continue's two boolean
// flags. Continue exposes only `--readonly` (plan mode, read-only tools) and
// `--auto` (all tools allowed); there is no separate yolo/bypass beyond --auto,
// and the two flags are mutually exclusive. Default and AcceptEdits emit no flag
// so Continue defers to the user's own config / default behavior.
func appendApprovalFlags(cmd *[]string, permissions ports.PermissionMode) {
	switch ports.NormalizePermissionMode(permissions) {
	case ports.PermissionModeDefault:
		// No flag: defer to the user's Continue config / default behavior.
	case ports.PermissionModeAcceptEdits:
		// Continue has no granular "accept edits only" mode; defer to config.
	case ports.PermissionModeAuto:
		*cmd = append(*cmd, "--auto")
	case ports.PermissionModeBypassPermissions:
		*cmd = append(*cmd, "--auto")
	}
}

func appendSystemPromptRule(cmd *[]string, inline, file string) {
	if inline != "" {
		*cmd = append(*cmd, "--rule", inline)
		return
	}
	if file != "" {
		*cmd = append(*cmd, "--rule", file)
	}
}
