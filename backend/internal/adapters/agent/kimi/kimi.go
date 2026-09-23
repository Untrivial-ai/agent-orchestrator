// Package kimi implements the Kimi CLI (Moonshot AI) agent adapter: launching
// new interactive sessions and resuming sessions when a native Kimi session id
// is known.
//
// Kimi CLI (binary "kimi") is Moonshot AI's terminal-native agentic coding
// agent. AO launches Kimi sessions interactively as `kimi [--auto|-y]` and
// delivers prompted worker tasks after startup through the runtime pane. Kimi's
// `-p/--prompt` mode is intentionally avoided for AO workers because it is
// non-interactive and streams transcript output without opening the TUI.
// Sessions are resumed by id with `kimi --session <id>`.
//
// Kimi exposes no system-prompt launch flag, so AO injects standing
// instructions through Kimi's documented project instruction file
// (.kimi/AGENTS.md) in the per-session worktree. AO also installs Kimi
// lifecycle hooks into Kimi's config so native session metadata and activity can
// flow back through `ao hooks`.
package kimi

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/agentbase"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/binaryutil"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const (
	adapterID       = "kimi"
	kimiCodeHomeEnv = "KIMI_CODE_HOME"
	kimiDataDirName = "kimi"
)

// Plugin is the Kimi CLI agent adapter. It is safe for concurrent use; the
// binary path is resolved once and cached under binaryMu.
type Plugin struct {
	agentbase.Base
	binaryMu       sync.Mutex
	resolvedBinary string
}

// New returns a ready-to-register Kimi adapter.
func New() *Plugin {
	return &Plugin{}
}

var _ adapters.Adapter = (*Plugin)(nil)
var _ ports.Agent = (*Plugin)(nil)

func kimiCodeHomeDir(dataDir string) string {
	return filepath.Join(dataDir, kimiDataDirName)
}

// AugmentRuntimeEnv points Kimi at AO's isolated Kimi home so session hooks and
// other managed state stay under AO_DATA_DIR instead of the user's profile.
func (p *Plugin) AugmentRuntimeEnv(env map[string]string, dataDir string) {
	if strings.TrimSpace(dataDir) == "" {
		return
	}
	env[kimiCodeHomeEnv] = kimiCodeHomeDir(dataDir)
}

// Manifest returns the adapter's static self-description.
func (p *Plugin) Manifest() adapters.Manifest {
	return adapters.Manifest{
		ID:          adapterID,
		Name:        "Kimi",
		Description: "Run Kimi CLI (Moonshot AI) worker sessions.",
		Version:     "0.0.1",
		Capabilities: []adapters.Capability{
			adapters.CapabilityAgent,
		},
	}
}

// GetConfigSpec reports the per-project agent config keys Kimi understands.
func (p *Plugin) GetConfigSpec(ctx context.Context) (ports.ConfigSpec, error) {
	if err := ctx.Err(); err != nil {
		return ports.ConfigSpec{}, err
	}
	return ports.ConfigSpec{
		Fields: []ports.ConfigField{
			{
				Key:         "model",
				Type:        ports.ConfigFieldString,
				Description: "Model override passed to `kimi --model`.",
			},
		},
	}, nil
}

// GetLaunchCommand builds the argv to start a new Kimi session:
//
//	kimi [--auto|-y]                            (interactive)
//
// Prompted tasks are delivered after startup by the session manager rather than
// via `-p`, so the dashboard keeps the interactive Kimi TUI instead of a plain
// transcript stream. Kimi has no documented system-prompt flag, so standing
// instructions are installed by GetAgentHooks as a project instruction file
// instead of being passed in argv.
func (p *Plugin) GetLaunchCommand(ctx context.Context, cfg ports.LaunchConfig) (cmd []string, err error) {
	binary, err := p.kimiBinary(ctx)
	if err != nil {
		return nil, err
	}

	cmd = []string{binary}
	appendApprovalFlags(&cmd, cfg.Permissions)
	appendModelFlag(&cmd, cfg.Config)
	return cmd, nil
}

// GetPromptDeliveryStrategy reports that AO should inject prompted Kimi tasks
// into the interactive terminal after startup. Kimi's `-p/--prompt` mode is
// non-interactive and does not open the TUI.
func (p *Plugin) GetPromptDeliveryStrategy(ctx context.Context, _ ports.LaunchConfig) (ports.PromptDeliveryStrategy, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return ports.PromptDeliveryAfterStart, nil
}

// PromptReadinessHints waits for Kimi's interactive prompt before AO injects
// the worker's first task.
func (p *Plugin) PromptReadinessHints(ctx context.Context, _ ports.LaunchConfig) (ports.PromptReadinessHints, error) {
	if err := ctx.Err(); err != nil {
		return ports.PromptReadinessHints{}, err
	}
	return ports.PromptReadinessHints{
		InitialDelay: 750 * time.Millisecond,
		Patterns:     []string{"│ >"},
		PollInterval: 200 * time.Millisecond,
		Timeout:      90 * time.Second,
		Lines:        80,
	}, nil
}

// GetRestoreCommand rebuilds the argv that continues an existing Kimi session
// when a native Kimi session id is known:
//
//	kimi --session <agentSessionId>
//
// ok is false when no native session id has been captured, so callers fall back
// to fresh launch behavior. Per Kimi docs, `--yolo` and `--auto` cannot be
// combined with `--session` (or `--continue`) -- resumed sessions inherit the
// approval settings of the original session -- so cfg.Permissions is
// intentionally ignored here.
func (p *Plugin) GetRestoreCommand(ctx context.Context, cfg ports.RestoreConfig) (cmd []string, ok bool, err error) {
	// Deliberately does not forward cfg.Config.Model: --model + --session
	// composition is unverified against a real kimi install (see #2890 and
	// appendModelFlag). Pinned by TestGetRestoreCommandDoesNotForwardModelOverride.
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	agentSessionID := strings.TrimSpace(cfg.Session.Metadata[ports.MetadataKeyAgentSessionID])
	if agentSessionID == "" {
		return nil, false, nil
	}

	binary, err := p.kimiBinary(ctx)
	if err != nil {
		return nil, false, err
	}
	cmd = []string{binary, "--session", agentSessionID}
	return cmd, true, nil
}

// appendApprovalFlags maps AO's permission modes onto Kimi's approval flags
// for interactive launches. Per Kimi docs these flags cannot be combined with
// `--prompt`, `--session`, or `--continue`, so callers on those paths must
// skip this mapping.
//
//   - Default: no flag, deferring to the user's Kimi config/default behavior.
//   - AcceptEdits / Auto: `--auto` (auto permission mode; approvals handled
//     automatically).
//   - BypassPermissions: `-y` (yolo; auto-approve regular tool calls including
//     file writes and shell execution).
func appendApprovalFlags(cmd *[]string, permissions ports.PermissionMode) {
	switch normalizePermissionMode(permissions) {
	case ports.PermissionModeDefault:
		// No flag: defer to the user's Kimi config/default behavior.
	case ports.PermissionModeAcceptEdits, ports.PermissionModeAuto:
		*cmd = append(*cmd, "--auto")
	case ports.PermissionModeBypassPermissions:
		*cmd = append(*cmd, "-y")
	}
}

// appendModelFlag appends `--model <model>` when a role-specific model
// override is configured. A blank/whitespace-only value means no override was
// configured, so the flag is omitted and Kimi's own default model applies.
//
// Restore-path note: this is intentionally NOT called from
// GetRestoreCommand. Kimi's docs do not confirm --model composes with
// --session the way it does on the launch path (see issue #2890); wiring it
// blind risks silently breaking session resume. Left as a fast-follow pending
// verification against a real kimi install.
func appendModelFlag(cmd *[]string, cfg ports.AgentConfig) {
	if model := strings.TrimSpace(cfg.Model); model != "" {
		*cmd = append(*cmd, "--model", model)
	}
}

func normalizePermissionMode(mode ports.PermissionMode) ports.PermissionMode {
	switch mode {
	case ports.PermissionModeDefault,
		ports.PermissionModeAcceptEdits,
		ports.PermissionModeAuto,
		ports.PermissionModeBypassPermissions:
		return mode
	default:
		return ports.PermissionModeDefault
	}
}

var kimiBinarySpec = binaryutil.BinarySpec{
	Label:         "kimi",
	Names:         []string{"kimi"},
	WinNames:      []string{"kimi.cmd", "kimi.exe", "kimi"},
	UnixPaths:     []string{"/usr/local/bin/kimi", "/opt/homebrew/bin/kimi"},
	UnixHomePaths: binaryutil.NodeManagedUnixHomePaths("kimi", []string{".kimi-code", "bin", "kimi"}, []string{".cargo", "bin", "kimi"}),
	NodeManaged:   true,
	WinPaths: []binaryutil.WinPath{
		{Base: binaryutil.WinAppData, Parts: []string{"npm", "kimi.cmd"}},
		{Base: binaryutil.WinAppData, Parts: []string{"npm", "kimi.exe"}},
		{Base: binaryutil.WinHome, Parts: []string{".local", "bin", "kimi.exe"}},
	},
}

// ResolveKimiBinary finds the `kimi` binary, searching PATH then common install
// locations (npm global dirs, ~/.local/bin, Homebrew, and ~/.cargo/bin).
func ResolveKimiBinary(ctx context.Context) (string, error) {
	return binaryutil.ResolveBinary(ctx, kimiBinarySpec)
}

func (p *Plugin) kimiBinary(ctx context.Context) (string, error) {
	p.binaryMu.Lock()
	defer p.binaryMu.Unlock()

	if p.resolvedBinary != "" {
		return p.resolvedBinary, nil
	}

	binary, err := ResolveKimiBinary(ctx)
	if err != nil {
		return "", err
	}
	p.resolvedBinary = binary
	return binary, nil
}
