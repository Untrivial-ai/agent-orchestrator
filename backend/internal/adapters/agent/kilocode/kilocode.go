// Package kilocode implements the Kilo Code CLI agent adapter: launching new
// TUI sessions, resuming sessions by native id, installing a workspace-local
// activity plugin, and reading plugin-derived session info.
//
// The Kilo Code CLI (binary "kilocode", also aliased "kilo"; npm package
// @kilocode/cli) is a fork of sst/opencode and shares its CLI surface and
// plugin runtime, so AO bridges it the same two ways it bridges opencode:
//   - It has no native command-hook config (no settings.local.json / hooks.json
//     equivalent). Its only lifecycle-extensibility surface is the @opencode-ai
//     plugin SDK loaded from a config dir's `{plugin,plugins}/*.{ts,js}` glob,
//     so GetAgentHooks installs an AO-owned plugin file (see hooks.go) into
//     .kilocode/plugins/ instead of merging JSON.
//   - Its interactive TUI exposes no permission flag (the --auto flag lives only
//     on `kilo run`, not the default TUI command AO launches) and no
//     system-prompt flag. AO's graduated permission modes and standing
//     instructions are delivered via the KILO_CONFIG_CONTENT env var, which Kilo
//     deep-merges as the highest-precedence inline config.
//
// AO-managed sessions derive native session identity and display metadata from
// the Kilo plugin's reported events, mirroring the opencode and Codex adapters.
package kilocode

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/agentbase"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/binaryutil"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const (
	// adapterID is the registry id and the value users pass to
	// `ao spawn --agent`. It matches domain.HarnessKilocode.
	adapterID = "kilocode"
)

// Plugin is the Kilo Code agent adapter. It is safe for concurrent use; the
// binary path is resolved once and cached under binaryMu.
type Plugin struct {
	agentbase.Base
	binaryMu       sync.Mutex
	resolvedBinary string
}

// New returns a ready-to-register Kilo Code adapter.
func New() *Plugin {
	return &Plugin{}
}

var _ adapters.Adapter = (*Plugin)(nil)
var _ ports.Agent = (*Plugin)(nil)

// Manifest returns the adapter's static self-description.
func (p *Plugin) Manifest() adapters.Manifest {
	return adapters.Manifest{
		ID:          adapterID,
		Name:        "Kilo Code",
		Description: "Run Kilo Code worker sessions.",
		Version:     "0.0.1",
		Capabilities: []adapters.Capability{
			adapters.CapabilityAgent,
		},
	}
}

// GetConfigSpec reports the per-project agent config keys Kilo Code understands.
func (p *Plugin) GetConfigSpec(ctx context.Context) (ports.ConfigSpec, error) {
	if err := ctx.Err(); err != nil {
		return ports.ConfigSpec{}, err
	}
	return ports.ConfigSpec{
		Fields: []ports.ConfigField{
			{
				Key:         "model",
				Type:        ports.ConfigFieldString,
				Description: "Model override written to the AO-generated Kilo agent (agent.<name>.model); format provider/model-id (e.g. anthropic/claude-haiku-4-20250514).",
			},
		},
	}, nil
}

// GetLaunchCommand builds the argv to start a new interactive Kilo Code session.
// Shape:
//
//	[env KILO_CONFIG_CONTENT=<json>] kilocode [--agent <ao-agent>] [--prompt <prompt>]
//
// The session runs in the worktree (cwd is set by the runtime, as for opencode
// and Codex). Kilo Code has no CLI flag to set a system prompt, so AO injects a
// per-session agent prompt through KILO_CONFIG_CONTENT and selects it with
// --agent. The initial task prompt is delivered via --prompt (its argument, so a
// leading "-" is not read as a flag). Non-default permission modes use the same
// KILO_CONFIG_CONTENT env assignment rather than a flag.
func (p *Plugin) GetLaunchCommand(ctx context.Context, cfg ports.LaunchConfig) (cmd []string, err error) {
	binary, err := p.kilocodeBinary(ctx)
	if err != nil {
		return nil, err
	}

	envPrefix, agentName, err := kilocodeConfigEnvPrefix(cfg.Permissions, cfg.SystemPrompt, cfg.SystemPromptFile, cfg.SessionID, cfg.Config.Model)
	if err != nil {
		return nil, err
	}
	cmd = envPrefix
	cmd = append(cmd, binary)
	if agentName != "" {
		cmd = append(cmd, "--agent", agentName)
	}
	if cfg.Prompt != "" {
		cmd = append(cmd, "--prompt", cfg.Prompt)
	}
	return cmd, nil
}

// GetRestoreCommand rebuilds the argv that continues an existing Kilo Code
// session: `[env KILO_CONFIG_CONTENT=<json>] kilocode [--agent <ao-agent>] --session <agentSessionId>`.
// It re-applies the permission env and per-session AO agent prompt (resume
// otherwise reverts to configured defaults). ok is false when the plugin-derived
// native session id has not landed yet, so callers fall back to fresh launch
// behavior — mirroring the opencode adapter.
func (p *Plugin) GetRestoreCommand(ctx context.Context, cfg ports.RestoreConfig) (cmd []string, ok bool, err error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	agentSessionID := strings.TrimSpace(cfg.Session.Metadata[ports.MetadataKeyAgentSessionID])
	if agentSessionID == "" {
		return nil, false, nil
	}

	binary, err := p.kilocodeBinary(ctx)
	if err != nil {
		return nil, false, err
	}

	envPrefix, agentName, err := kilocodeConfigEnvPrefix(cfg.Permissions, cfg.SystemPrompt, cfg.SystemPromptFile, cfg.Session.ID, cfg.Config.Model)
	if err != nil {
		return nil, false, err
	}
	cmd = envPrefix
	cmd = append(cmd, binary)
	if agentName != "" {
		cmd = append(cmd, "--agent", agentName)
	}
	cmd = append(cmd, "--session", agentSessionID)
	return cmd, true, nil
}

// SessionInfo surfaces Kilo plugin-derived metadata. Metadata is intentionally
// nil for Kilo Code: callers get the normalized fields directly, matching the
// opencode and Codex adapters.
func (p *Plugin) SessionInfo(ctx context.Context, session ports.SessionRef) (ports.SessionInfo, bool, error) {
	if err := ctx.Err(); err != nil {
		return ports.SessionInfo{}, false, err
	}
	info, ok := agentbase.StandardSessionInfo(session)
	return info, ok, nil
}

// kilocodePermissionEnvVar is the env var Kilo deep-merges as the
// highest-precedence inline config (`KILO_CONFIG_CONTENT`, see the CLI's config
// precedence: global -> KILO_CONFIG -> ./kilo.json -> .kilo/kilo.json ->
// KILO_CONFIG_CONTENT -> managed; later wins). It is the permission-control
// surface the interactive TUI honors: the --auto flag exists solely on
// `kilo run`, not on the default TUI command AO launches, so passing any
// permission flag would make Kilo reject the argv and the session fail to launch.
const kilocodePermissionEnvVar = "KILO_CONFIG_CONTENT"

// kilocodePermissionConfig maps an AO permission mode onto Kilo's permission
// config (tool -> action, values "ask"/"allow"/"deny", verified via
// `kilocode config check`). Tools left unset fall back to Kilo's own default
// action ("ask"), so each mode only names the tools it relaxes:
//   - default            -> nil: no env; Kilo's config decides every prompt.
//   - accept-edits       -> edits ("write"/"edit"/"patch" gate on the "edit"
//     key) auto-approved; bash and everything else still prompt.
//   - auto               -> edits + bash auto-approved; network/other still prompt.
//     Kilo has no classifier/reviewer gate (unlike Claude Code's "auto"), so
//     this is the closest analog its flat allow/ask/deny config can express.
//   - bypass-permissions -> "*" wildcard-allows every tool: nothing prompts.
func kilocodePermissionConfig(mode ports.PermissionMode) map[string]string {
	switch ports.NormalizePermissionMode(mode) {
	case ports.PermissionModeAcceptEdits:
		return map[string]string{"edit": "allow"}
	case ports.PermissionModeAuto:
		return map[string]string{"edit": "allow", "bash": "allow"}
	case ports.PermissionModeBypassPermissions:
		return map[string]string{"*": "allow"}
	default:
		return nil
	}
}

type kilocodeInlineConfig struct {
	Permission map[string]string                `json:"permission,omitempty"`
	Agent      map[string]kilocodeAgentSettings `json:"agent,omitempty"`
}

type kilocodeAgentSettings struct {
	Prompt string `json:"prompt,omitempty"`
	Model  string `json:"model,omitempty"`
}

// kilocodeConfigEnvPrefix renders permission and system-prompt config as an
// `env KILO_CONFIG_CONTENT=<json>` argv prefix. The returned agent name is non-
// empty when the command must select AO's generated agent with --agent.
//
// The var must reach Kilo as a process env var, not an argv flag. The runtime
// runs the argv through a shell, which execs `env`, which sets the var and execs
// kilocode. A bare `KILO_CONFIG_CONTENT=...` argv element would not work: the
// runtime shell-quotes every element, and a quoted token is run as a command
// rather than read as an assignment — hence the explicit `env` wrapper.
// POSIX-only, which matches the tmux runtime.
func kilocodeConfigEnvPrefix(mode ports.PermissionMode, inlinePrompt, promptFile, sessionID, model string) ([]string, string, error) {
	config := kilocodeInlineConfig{Permission: kilocodePermissionConfig(mode)}
	agentName := ""
	systemPrompt, err := kilocodeSystemPromptText(inlinePrompt, promptFile)
	if err != nil {
		return nil, "", err
	}
	model = strings.TrimSpace(model)
	// A non-default permission mode rides on the permission map alone, but a
	// system prompt or a role-specific model must be carried on AO's generated
	// agent (selected with --agent): Kilo Code reads the per-agent model from
	// agent.<name>.model, and its interactive TUI has no reliable --model flag.
	if systemPrompt != "" || model != "" {
		agentName = kilocodeAOAgentName(sessionID)
		config.Agent = map[string]kilocodeAgentSettings{
			agentName: {Prompt: systemPrompt, Model: model},
		}
	}
	if len(config.Permission) == 0 && len(config.Agent) == 0 {
		return nil, "", nil
	}
	blob, err := json.Marshal(config)
	if err != nil {
		// Should never happen for this static config shape, but a silent
		// empty KILO_CONFIG_CONTENT would silently launch with default Kilo
		// permissions/rules regardless of the requested mode — surface it.
		return nil, "", err
	}
	return []string{"env", kilocodePermissionEnvVar + "=" + string(blob)}, agentName, nil
}

func kilocodeSystemPromptText(inline, file string) (string, error) {
	if inline != "" {
		return inline, nil
	}
	if file == "" {
		return "", nil
	}
	data, err := os.ReadFile(file) //nolint:gosec // path is AO-owned launch config
	if err != nil {
		return "", fmt.Errorf("kilocode: read system prompt file: %w", err)
	}
	return string(data), nil
}

func kilocodeAOAgentName(sessionID string) string {
	const fallback = "ao-system-prompt"
	trimmed := strings.TrimSpace(sessionID)
	if trimmed == "" {
		return fallback
	}
	var b strings.Builder
	for _, r := range trimmed {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '-',
			r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	name := strings.Trim(b.String(), "-_")
	if name == "" {
		return fallback
	}
	return "ao-" + name
}

var kilocodeBinarySpec = binaryutil.BinarySpec{
	Label:         "kilocode",
	Names:         []string{"kilo", "kilocode"},
	WinNames:      []string{"kilo.cmd", "kilo.exe", "kilo", "kilocode.cmd", "kilocode.exe", "kilocode"},
	UnixPaths:     []string{"/usr/local/bin/kilo", "/opt/homebrew/bin/kilo", "/usr/local/bin/kilocode", "/opt/homebrew/bin/kilocode"},
	UnixHomePaths: append(binaryutil.NodeManagedUnixHomePaths("kilo"), binaryutil.NodeManagedUnixHomePaths("kilocode")...),
	NodeManaged:   true,
	WinPaths: []binaryutil.WinPath{
		{Base: binaryutil.WinAppData, Parts: []string{"npm", "kilo.cmd"}},
		{Base: binaryutil.WinAppData, Parts: []string{"npm", "kilo.exe"}},
		{Base: binaryutil.WinAppData, Parts: []string{"npm", "kilocode.cmd"}},
		{Base: binaryutil.WinAppData, Parts: []string{"npm", "kilocode.exe"}},
	},
}

// ResolveKilocodeBinary returns the path to the kilocode binary, or a wrapped
// ports.ErrAgentBinaryNotFound when it is absent.
func ResolveKilocodeBinary(ctx context.Context) (string, error) {
	return binaryutil.ResolveBinary(ctx, kilocodeBinarySpec)
}

func (p *Plugin) kilocodeBinary(ctx context.Context) (string, error) {
	p.binaryMu.Lock()
	defer p.binaryMu.Unlock()

	if p.resolvedBinary != "" {
		return p.resolvedBinary, nil
	}

	binary, err := ResolveKilocodeBinary(ctx)
	if err != nil {
		return "", err
	}
	p.resolvedBinary = binary
	return binary, nil
}
