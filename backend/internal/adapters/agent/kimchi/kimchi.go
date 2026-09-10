// Package kimchi implements the Kimchi agent adapter: launching headless Kimchi
// sessions and resuming sessions when a native session id is known.
//
// Kimchi (binary "kimchi") is a coding-agent CLI built on
// @earendil-works/pi-coding-agent. AO drives it non-interactively with
// `--print` ("process prompt and exit"). The initial prompt is delivered
// in-command as a trailing positional argument.
//
// System prompts are appended to Kimchi's default prompt via
// `--append-system-prompt <text>`. The flag takes inline text only; a
// system-prompt file is read from disk and inlined.
//
// Permissions: Kimchi accepts `--plan`, `--auto`, and `--yolo` flags for
// permission modes. AO maps its own modes onto these flags.
//
// Restore: Kimchi persists sessions and resumes by id with `--session <id>`.
// The native session id is captured from hook metadata and stored in AO's
// session metadata; GetRestoreCommand reads it back.
//
// Hooks: Kimchi has a native hook adapter that reads .kimchi/hooks.local.json.
// AO installs hooks in that file using `ao hooks kimchi <event>` commands.
// The adapter is always-on; no user configuration is needed for hooks to fire.
//
// PreLaunch: Kimchi does NOT have a first-run trust/onboarding dialog (unlike
// Claude Code's "Do you trust this folder?" prompt). This was verified by
// running `kimchi -p "hi" --yolo` in a fresh empty temporary directory — the
// command returned immediately with a response and no blocking prompt.
// `kimchi --help` output contains no trust, onboarding, or workspace-trust
// flags or subcommands. The `kimchi setup` subcommand is an interactive API
// key wizard, not a per-directory trust gate. Therefore no PreLaunch
// implementation is needed; AO worktree spawns will not hang on a trust dialog.
//
// ActiveTurnSteer: Kimchi has a programmatic steering mechanism
// (pi.sendMessage with deliverAs:"steer") used by internal extensions to inject
// guidance into the active turn. However, AO writes to the tmux pane as
// terminal keystrokes, which go through the TUI editor's default submit path —
// those messages queue for the next turn, they do not steer the current one.
// Only the programmatic API can steer; terminal input cannot. Therefore
// SteersActiveTurn is not implemented, matching Claude Code and Pi. Enabling it
// would require AO to use pi.sendMessage directly instead of terminal input.
package kimchi

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/agentbase"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/binaryutil"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const adapterID = "kimchi"

// systemPromptMaxBytes caps how many bytes readBoundedSystemPrompt will read
// from a system-prompt file. This prevents unbounded reads that could cause
// "argument list too long" errors or hang on FIFOs/slow mounts.
const systemPromptMaxBytes = 128 * 1024

// readBoundedSystemPrompt reads a system-prompt file capped at
// systemPromptMaxBytes. The file contents are returned raw (no trimming) so
// a resumed session's system prompt is byte-identical to a fresh launch.
//
// A regular-file check (os.Stat + Mode().IsRegular) is performed before
// opening: FIFOs, sockets, and device files can cause io.ReadAll to block
// indefinitely, and a system-prompt file should always be a regular file.
func readBoundedSystemPrompt(ctx context.Context, path string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("kimchi: read system prompt file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("kimchi: system prompt file %s is not a regular file (mode %s)", path, info.Mode())
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}

	f, err := os.Open(path) //nolint:gosec // path is AO-owned config
	if err != nil {
		return "", fmt.Errorf("kimchi: read system prompt file: %w", err)
	}
	defer func() { _ = f.Close() }()
	openedInfo, err := f.Stat()
	if err != nil {
		return "", fmt.Errorf("kimchi: inspect system prompt file: %w", err)
	}
	if !openedInfo.Mode().IsRegular() {
		return "", fmt.Errorf("kimchi: system prompt file %s is not a regular file (mode %s)", path, openedInfo.Mode())
	}

	data, err := io.ReadAll(io.LimitReader(contextReader{ctx: ctx, reader: f}, systemPromptMaxBytes+1))
	if err != nil {
		return "", fmt.Errorf("kimchi: read system prompt file: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if len(data) > systemPromptMaxBytes {
		return "", fmt.Errorf("kimchi: system prompt file %s exceeds %d byte limit", path, systemPromptMaxBytes)
	}
	return string(data), nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

// Plugin implements the Kimchi agent adapter.
type Plugin struct {
	agentbase.Base
	binaryMu       sync.Mutex
	resolvedBinary string
}

// New creates a new Kimchi adapter plugin.
func New() *Plugin {
	return &Plugin{}
}

var _ adapters.Adapter = (*Plugin)(nil)
var _ ports.Agent = (*Plugin)(nil)
var _ ports.SubmitActivitySignaler = (*Plugin)(nil)
var _ ports.BlockedActivitySignaler = (*Plugin)(nil)

// EmitsSubmitActivity signals that Kimchi fires a user-prompt-submit hook
// under AO's launch, so Activity.State can flip to active after a prompt is
// accepted. This engages the session manager's confirm loop for Kimchi
// sessions. See ports.SubmitActivitySignaler.
func (p *Plugin) EmitsSubmitActivity() bool { return true }

// EmitsBlockedActivity signals that Kimchi installs the full pre/post-tool-use
// hook trio (PreToolUse + PostToolUse + PostToolUseFail) and maps
// Notification(permission_prompt) to ActivityBlocked. The permission_prompt
// notification payload carries tool_use_id, which the lifecycle correlator
// matches against the inflight map populated by PreToolUse — the same
// mechanism Claude Code uses, just with the blocked signal arriving via a
// Notification event rather than a separate permission-request hook. The
// correlator keys on the signal's State and ToolUseID, not the event name,
// so a blocked permission dialog is cleared when the approved tool's
// PostToolUse fires. See ports.BlockedActivitySignaler.
func (p *Plugin) EmitsBlockedActivity() bool { return true }

// permissionConfigEnum lists the permission modes the "permissions" config key
// accepts. It mirrors the ports.PermissionMode constants so a project's stored
// config validates against the same vocabulary the launch command maps.
var permissionConfigEnum = []string{
	string(ports.PermissionModeDefault),
	string(ports.PermissionModeAcceptEdits),
	string(ports.PermissionModeAuto),
	string(ports.PermissionModeBypassPermissions),
}

// Manifest returns the adapter's static metadata.
func (p *Plugin) Manifest() adapters.Manifest {
	return adapters.Manifest{
		ID:          adapterID,
		Name:        "Kimchi",
		Description: "Run Kimchi worker sessions.",
		Version:     "0.0.1",
		Capabilities: []adapters.Capability{
			adapters.CapabilityAgent,
		},
	}
}

// GetConfigSpec returns the adapter's configuration spec.
func (p *Plugin) GetConfigSpec(ctx context.Context) (ports.ConfigSpec, error) {
	if err := ctx.Err(); err != nil {
		return ports.ConfigSpec{}, err
	}
	return ports.ConfigSpec{
		Fields: []ports.ConfigField{
			{
				Key:         "model",
				Type:        ports.ConfigFieldString,
				Description: "Model override passed to `kimchi --model`.",
			},
			{
				Key:         "permissions",
				Type:        ports.ConfigFieldEnum,
				Description: "Starting permission mode.",
				Enum:        permissionConfigEnum,
			},
		},
	}, nil
}

// GetLaunchCommand builds the argv to start an interactive Kimchi session
// inside a tmux pane:
//
//	kimchi [--model <id>] [--auto|--yolo] [--append-system-prompt <text>] [<prompt>]
//
// Kimchi runs interactively (no --print): its TUI requires a TTY, which
// tmux provides. The prompt is a trailing positional argument.
func (p *Plugin) GetLaunchCommand(ctx context.Context, cfg ports.LaunchConfig) (cmd []string, err error) {
	binary, err := p.kimchiBinary(ctx)
	if err != nil {
		return nil, err
	}

	cmd = []string{binary}

	if model := strings.TrimSpace(cfg.Config.Model); model != "" {
		cmd = append(cmd, "--model", model)
	}

	permissions := cfg.Permissions
	if permissions == "" {
		permissions = cfg.Config.Permissions
	}
	appendPermissionFlags(&cmd, permissions)
	if err := appendToolFlags(&cmd, cfg.AllowedTools, cfg.DisallowedTools); err != nil {
		return nil, err
	}

	if cfg.SystemPromptFile != "" {
		data, err := readBoundedSystemPrompt(ctx, cfg.SystemPromptFile)
		if err != nil {
			return nil, err
		}
		cmd = append(cmd, "--append-system-prompt", data)
	} else if cfg.SystemPrompt != "" {
		cmd = append(cmd, "--append-system-prompt", cfg.SystemPrompt)
	}
	if cfg.Prompt != "" {
		cmd = append(cmd, sanitizePrompt(cfg.Prompt))
	}
	return cmd, nil
}

// GetRestoreCommand rebuilds the argv to resume an existing Kimchi session
// when a native session id is available in metadata.
//
// Final argv shape: kimchi [--auto|--yolo] [--allow-tool <rules>] [--deny-tool <rules>] [--append-system-prompt <text>] --session <id>.
// Re-applying the permission mode is a behavioral fix, not just a contract gap:
// Kimchi's default mode fails closed (cannot auto-approve anything) in headless
// contexts, so dropping the mode would regress a resumed orchestrator. The
// system prompt is re-applied because Kimchi rebuilds the prompt from the
// current flags on resume (it is not stored in the transcript), so standing
// instructions must be re-appended or a restored orchestrator loses its role.
// Tool allow/deny rules are re-applied because RestoreConfig carries them and
// a worker session launched with tool restrictions would otherwise lose those
// restrictions on resume, matching Claude Code's restore behavior.
// --session <id> is appended last.
func (p *Plugin) GetRestoreCommand(ctx context.Context, cfg ports.RestoreConfig) (cmd []string, ok bool, err error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	agentSessionID := strings.TrimSpace(cfg.Session.Metadata[ports.MetadataKeyAgentSessionID])
	if agentSessionID == "" {
		return nil, false, nil
	}

	binary, err := p.kimchiBinary(ctx)
	if err != nil {
		return nil, false, err
	}
	cmd = []string{binary}
	appendPermissionFlags(&cmd, cfg.Permissions)
	if err := appendToolFlags(&cmd, cfg.AllowedTools, cfg.DisallowedTools); err != nil {
		return nil, false, err
	}

	systemPrompt, err := resolveRestoreSystemPrompt(ctx, cfg)
	if err != nil {
		return nil, false, err
	}
	if systemPrompt != "" {
		cmd = append(cmd, "--append-system-prompt", systemPrompt)
	}

	cmd = append(cmd, "--session", agentSessionID)
	return cmd, true, nil
}

// SessionInfo surfaces the normalized session metadata that the hooks
// persisted: native session id, title, and summary.
func (p *Plugin) SessionInfo(ctx context.Context, session ports.SessionRef) (ports.SessionInfo, bool, error) {
	if err := ctx.Err(); err != nil {
		return ports.SessionInfo{}, false, err
	}
	info, ok := agentbase.StandardSessionInfo(session)
	return info, ok, nil
}

// appendToolFlags emits at most one --allow-tool and at most one --deny-tool
// flag for a tool-scoped launch. All allowed rules are comma-joined into a
// single --allow-tool value, and all disallowed rules into a single --deny-tool
// value. Empty lists emit nothing, so an unrestricted launch is unchanged.
//
// Kimchi's permission loader splits the flag value by comma (splitFlag), and
// repeated flags overwrite (last-wins), so emitting one pair per rule would
// collapse the deny list to a single entry — silently weakening the reviewer's
// best-effort tool policy. Comma-joining into a single value ensures every rule
// survives the split. Kimchi's --allow-tool/--deny-tool flags accept the same
// rule syntax as Claude Code (bash(git diff:*), edit, mcp__server__tool), and
// the rule parser is case-insensitive on tool names, so lowercase tool names
// are used to match Kimchi's internal names. These rules are honored under
// --auto and --plan; only --dangerously-skip-permissions (not mapped by AO)
// would skip the denylist.
func appendToolFlags(cmd *[]string, allowed, disallowed []string) error {
	for _, rule := range allowed {
		if strings.Contains(rule, ",") {
			return fmt.Errorf("kimchi: allowed tool rule %q contains a comma; tool rules are comma-joined into a single flag value so a literal comma would silently split the rule", rule)
		}
	}
	for _, rule := range disallowed {
		if strings.Contains(rule, ",") {
			return fmt.Errorf("kimchi: disallowed tool rule %q contains a comma; tool rules are comma-joined into a single flag value so a literal comma would silently split the rule", rule)
		}
	}
	if len(allowed) > 0 {
		*cmd = append(*cmd, "--allow-tool", strings.Join(allowed, ","))
	}
	if len(disallowed) > 0 {
		*cmd = append(*cmd, "--deny-tool", strings.Join(disallowed, ","))
	}
	return nil
}

func appendPermissionFlags(cmd *[]string, permissions ports.PermissionMode) {
	switch permissions {
	case ports.PermissionModeDefault, "":
		// No flag: use Kimchi's default behavior.
	case ports.PermissionModeAcceptEdits:
		*cmd = append(*cmd, "--auto")
	case ports.PermissionModeAuto:
		*cmd = append(*cmd, "--auto")
	case ports.PermissionModeBypassPermissions:
		*cmd = append(*cmd, "--yolo")
	}
}

// sanitizePrompt prevents a worker prompt from being parsed as a Kimchi flag
// (leading '-') or file reference (leading '@'). When either condition holds,
// a leading '\n' is prepended. The newline is invisible to the model and
// ensures the argument is treated as positional task text, not a flag or file
// reference. Normal prompts are passed through unchanged, preserving the
// existing PromptDeliveryInCommand strategy.
func sanitizePrompt(prompt string) string {
	if prompt == "" {
		return prompt
	}
	if strings.HasPrefix(prompt, "-") || strings.HasPrefix(prompt, "@") {
		return "\n" + prompt
	}
	return prompt
}

// resolveRestoreSystemPrompt returns the system prompt text to re-append on
// resume. It mirrors the launch-side precedence of GetLaunchCommand: a
// cfg.SystemPromptFile is read from disk and inlined when set, else
// cfg.SystemPrompt is used inline. A file-read error is returned rather than
// silently dropping the prompt, so a resumed orchestrator cannot lose its
// standing instructions without the caller knowing.
func resolveRestoreSystemPrompt(ctx context.Context, cfg ports.RestoreConfig) (string, error) {
	if cfg.SystemPromptFile != "" {
		return readBoundedSystemPrompt(ctx, cfg.SystemPromptFile)
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return cfg.SystemPrompt, nil
}

var kimchiBinarySpec = binaryutil.BinarySpec{
	Label:         "kimchi",
	Names:         []string{"kimchi"},
	WinNames:      []string{"kimchi.cmd", "kimchi.exe", "kimchi"},
	UnixPaths:     []string{"/usr/local/bin/kimchi", "/opt/homebrew/bin/kimchi"},
	UnixHomePaths: binaryutil.NodeManagedUnixHomePaths("kimchi"),
	NodeManaged:   true,
	WinPaths: []binaryutil.WinPath{
		{Base: binaryutil.WinAppData, Parts: []string{"npm", "kimchi.cmd"}},
		{Base: binaryutil.WinAppData, Parts: []string{"npm", "kimchi.exe"}},
	},
}

// ResolveKimchiBinary locates the kimchi executable on the system.
func ResolveKimchiBinary(ctx context.Context) (string, error) {
	return binaryutil.ResolveBinary(ctx, kimchiBinarySpec)
}

func (p *Plugin) kimchiBinary(ctx context.Context) (string, error) {
	p.binaryMu.Lock()
	defer p.binaryMu.Unlock()

	if p.resolvedBinary != "" {
		return p.resolvedBinary, nil
	}

	binary, err := ResolveKimchiBinary(ctx)
	if err != nil {
		return "", err
	}
	p.resolvedBinary = binary
	return binary, nil
}
