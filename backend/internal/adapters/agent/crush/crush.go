// Package crush implements the Crush agent adapter: launching new sessions,
// resuming sessions by native ID, and reading session info.
//
// Crush differs from hook-driven agents in that it doesn't yet expose
// AO-compatible activity hooks. GetAgentHooks only injects AO's standing system
// prompt through project-local context configuration; activity is reconciled
// from conservative terminal UI markers.
package crush

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/agentbase"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/binaryutil"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const (
	// adapterID is the registry id and the value users pass to
	// `ao spawn --agent`. It matches domain.HarnessCrush.
	adapterID = "crush"
)

// Plugin is the Crush agent adapter. It is safe for concurrent use; the
// binary path is resolved once and cached under binaryMu.
type Plugin struct {
	agentbase.Base
	binaryMu       sync.Mutex
	resolvedBinary string
}

// New returns a ready-to-register Crush adapter.
func New() *Plugin {
	return &Plugin{}
}

var _ adapters.Adapter = (*Plugin)(nil)
var _ ports.Agent = (*Plugin)(nil)
var _ ports.ContinuousTerminalActivityDetector = (*Plugin)(nil)
var _ ports.WaitingTerminalActivityDetector = (*Plugin)(nil)

// Manifest returns the adapter's static self-description.
func (p *Plugin) Manifest() adapters.Manifest {
	return adapters.Manifest{
		ID:          adapterID,
		Name:        "Crush",
		Description: "Run Crush worker sessions.",
		Version:     "0.0.1",
		Capabilities: []adapters.Capability{
			adapters.CapabilityAgent,
		},
	}
}

// GetConfigSpec reports the per-project agent config keys Crush understands:
// a model override. Unlike Claude/Codex (a bare --model flag) or Kiro (a
// single model key), Crush's .crush.json requires both a provider id and a
// model id per selection (see mergeCrushModel in hooks.go), so AO's plain
// Model string must carry the provider too. This declares the expected
// "<provider>/<model-id>" shape so it is discoverable/validated through the
// config-spec surface, matching claude-code, codex, and kiro.
func (p *Plugin) GetConfigSpec(ctx context.Context) (ports.ConfigSpec, error) {
	if err := ctx.Err(); err != nil {
		return ports.ConfigSpec{}, err
	}
	return ports.ConfigSpec{
		Fields: []ports.ConfigField{
			{
				Key:         "model",
				Type:        ports.ConfigFieldString,
				Description: "Model override written into Crush's workspace-local config as the large-model selection. Must be \"<provider>/<model-id>\" (e.g. \"anthropic/claude-sonnet-4-5\"): Crush's config schema requires a provider id alongside the model id, which a bare model string doesn't carry.",
			},
		},
	}, nil
}

// GetLaunchCommand builds the argv to start an interactive Crush session.
// Shape:
//
//	crush [--cwd <WorkspacePath>] [--yolo]
//
// The session runs in the worktree (cwd is set by the runtime). Crush doesn't
// have a launch-time system-prompt flag, so GetAgentHooks installs AO's system
// prompt as a workspace-local context file before launch. Worker task prompts
// are delivered after startup so AO keeps the interactive TUI; Crush's
// documented `run` command is intentionally not used here because it is
// non-interactive. The --yolo flag corresponds to bypass-permissions mode.
//
// We intentionally do not pass --session on launch: cfg.SessionID is the
// AO-internal id, not a Crush-native session id. Letting Crush mint its own
// native session id (captured by hooks into session metadata) keeps launch
// consistent with GetRestoreCommand, which resumes using that native id.
func (p *Plugin) GetLaunchCommand(ctx context.Context, cfg ports.LaunchConfig) (cmd []string, err error) {
	binary, err := p.crushBinary(ctx)
	if err != nil {
		return nil, err
	}

	cmd = []string{binary}

	// Crush uses --cwd to set working directory
	if cfg.WorkspacePath != "" {
		cmd = append(cmd, "--cwd", cfg.WorkspacePath)
	}

	// Handle permission modes
	if cfg.Permissions == ports.PermissionModeBypassPermissions {
		cmd = append(cmd, "--yolo")
	}

	return cmd, nil
}

// GetPromptDeliveryStrategy reports that prompted sessions receive their task
// after the interactive Crush UI starts.
func (p *Plugin) GetPromptDeliveryStrategy(ctx context.Context, cfg ports.LaunchConfig) (ports.PromptDeliveryStrategy, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if cfg.Prompt != "" {
		return ports.PromptDeliveryAfterStart, nil
	}
	return ports.PromptDeliveryInCommand, nil
}

// PromptReadinessHints waits for Crush's ready prompt before AO injects the
// worker's first task.
func (p *Plugin) PromptReadinessHints(ctx context.Context, _ ports.LaunchConfig) (ports.PromptReadinessHints, error) {
	if err := ctx.Err(); err != nil {
		return ports.PromptReadinessHints{}, err
	}
	return ports.PromptReadinessHints{
		InitialDelay: 500 * time.Millisecond,
		Patterns:     []string{"Ready..."},
		PollInterval: 200 * time.Millisecond,
		Timeout:      8 * time.Second,
		Lines:        80,
	}, nil
}

// GetRestoreCommand rebuilds the argv that continues an existing Crush session:
// `crush [--cwd <WorkspacePath>] [--yolo] --session <agentSessionId>`.
// It re-applies the permission flag but not the prompt, which the session
// already carries. ok is false when the native session id is not available.
func (p *Plugin) GetRestoreCommand(ctx context.Context, cfg ports.RestoreConfig) (cmd []string, ok bool, err error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	agentSessionID := strings.TrimSpace(cfg.Session.Metadata[ports.MetadataKeyAgentSessionID])
	if agentSessionID == "" {
		return nil, false, nil
	}

	binary, err := p.crushBinary(ctx)
	if err != nil {
		return nil, false, err
	}

	cmd = []string{binary}

	if cfg.Session.WorkspacePath != "" {
		cmd = append(cmd, "--cwd", cfg.Session.WorkspacePath)
	}

	if cfg.Permissions == ports.PermissionModeBypassPermissions {
		cmd = append(cmd, "--yolo")
	}

	cmd = append(cmd, "--session", agentSessionID)
	return cmd, true, nil
}

var crushBinarySpec = binaryutil.BinarySpec{
	Label:         "crush",
	Names:         []string{"crush"},
	WinNames:      []string{"crush.cmd", "crush.exe", "crush"},
	UnixPaths:     []string{"/usr/local/bin/crush", "/opt/homebrew/bin/crush"},
	UnixHomePaths: binaryutil.NodeManagedUnixHomePaths("crush", []string{".cargo", "bin", "crush"}),
	NodeManaged:   true,
	WinPaths: []binaryutil.WinPath{
		{Base: binaryutil.WinAppData, Parts: []string{"npm", "crush.cmd"}},
		{Base: binaryutil.WinAppData, Parts: []string{"npm", "crush.exe"}},
		{Base: binaryutil.WinHome, Parts: []string{".cargo", "bin", "crush.exe"}},
	},
}

// ResolveCrushBinary returns the path to the crush binary on this machine,
// searching PATH then a handful of well-known install locations. It returns a
// wrapped ports.ErrAgentBinaryNotFound when crush is absent.
func ResolveCrushBinary(ctx context.Context) (string, error) {
	return binaryutil.ResolveBinary(ctx, crushBinarySpec)
}

func (p *Plugin) crushBinary(ctx context.Context) (string, error) {
	p.binaryMu.Lock()
	defer p.binaryMu.Unlock()

	if p.resolvedBinary != "" {
		return p.resolvedBinary, nil
	}

	binary, err := ResolveCrushBinary(ctx)
	if err != nil {
		return "", err
	}
	p.resolvedBinary = binary
	return binary, nil
}
