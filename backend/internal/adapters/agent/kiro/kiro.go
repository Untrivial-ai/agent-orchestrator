// Package kiro implements the Kiro (AWS) agent adapter: launching interactive
// sessions, resuming hook-tracked sessions, installing workspace-local hooks,
// and reading hook-derived session info.
//
// Kiro is AWS's agentic coding assistant. Its terminal CLI ships as the
// `kiro-cli` binary. AO launches Kiro with a workspace-local custom agent so
// both worker and orchestrator sessions can use Kiro's normal interactive
// approval flow. See https://kiro.dev/docs/cli/headless/ and
// https://kiro.dev/docs/cli/reference/cli-commands/.
//
// Worker prompts are delivered after startup so AO keeps Kiro's interactive
// TUI. Permission/approval modes map onto Kiro's tool-trust flags
// (`--trust-all-tools`, `--trust-tools=<categories>`).
// Restore uses `kiro-cli chat --resume-id <UUID>` with the native session id
// captured from a Kiro hook payload.
//
// AO-managed sessions derive native session identity and display metadata from
// Kiro's native hooks (see hooks.go) rather than transcript scans.
package kiro

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/agentbase"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/authprobe"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/binaryutil"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// Plugin is the Kiro agent adapter. It is safe for concurrent use; the binary
// path is resolved once and cached under binaryMu.
type Plugin struct {
	agentbase.Base
	binaryMu       sync.Mutex
	resolvedBinary string
	authRunner     authprobe.ScopedCmdRunner
}

// New returns a ready-to-register Kiro adapter.
func New() *Plugin {
	return &Plugin{}
}

var _ adapters.Adapter = (*Plugin)(nil)
var _ ports.Agent = (*Plugin)(nil)

// Manifest returns the adapter's static self-description.
func (p *Plugin) Manifest() adapters.Manifest {
	return adapters.Manifest{
		ID:          "kiro",
		Name:        "Kiro",
		Description: "Run Kiro (AWS) worker sessions.",
		Version:     "0.0.1",
		Capabilities: []adapters.Capability{
			adapters.CapabilityAgent,
		},
	}
}

// GetConfigSpec reports the per-project agent config keys Kiro understands:
// a model override. Kiro already forwards agentConfig.Model into its
// workspace-local custom agent config (see setKiroAgentDefaults in
// hooks.go); this declares that support so it is discoverable/validated
// through the config-spec surface, matching claude-code and codex.
func (p *Plugin) GetConfigSpec(ctx context.Context) (ports.ConfigSpec, error) {
	if err := ctx.Err(); err != nil {
		return ports.ConfigSpec{}, err
	}
	return ports.ConfigSpec{
		Fields: []ports.ConfigField{
			{
				Key:         "model",
				Type:        ports.ConfigFieldString,
				Description: "Model override written into Kiro's workspace-local agent config.",
			},
		},
	}, nil
}

// GetLaunchCommand builds the argv to start a new Kiro session:
// `kiro-cli chat [--agent ao] --agent ao [trust flags] [-- <prompt>]`.
//
// The prompt is passed as a positional argument after `--` so a leading "-" is
// not read as a flag for non-worker launches. Worker prompts are sent after
// startup so AO keeps the interactive TUI and avoids Kiro's current positional
// input submission gap. Kiro runs interactively for both workers and
// orchestrators; standing instructions come from the generated custom agent.
// AO standing instructions are installed during workspace preparation through
// the AO-managed workspace-local agent config, then selected here with --agent.
func (p *Plugin) GetLaunchCommand(ctx context.Context, cfg ports.LaunchConfig) (cmd []string, err error) {
	binary, err := p.kiroBinary(ctx)
	if err != nil {
		return nil, err
	}

	cmd = []string{binary, "chat"}
	cmd = append(cmd, "--agent", kiroAgentName)
	appendApprovalFlags(&cmd, cfg.Permissions)

	prompt := cfg.Prompt
	if prompt != "" && cfg.Kind == domain.KindOrchestrator {
		cmd = append(cmd, "--", prompt)
	}

	return cmd, nil
}

// GetPromptDeliveryStrategy reports how Kiro receives the initial task prompt.
// Orchestrator standing instructions are delivered through the generated
// custom-agent prompt, so no command or post-start prompt injection is needed
// there.
func (p *Plugin) GetPromptDeliveryStrategy(ctx context.Context, cfg ports.LaunchConfig) (ports.PromptDeliveryStrategy, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if cfg.Prompt != "" && cfg.Kind == domain.KindOrchestrator {
		return ports.PromptDeliveryInCommand, nil
	}
	if cfg.Prompt != "" && cfg.Kind != domain.KindOrchestrator {
		return ports.PromptDeliveryAfterStart, nil
	}
	if cfg.Kind == domain.KindOrchestrator {
		return ports.PromptDeliveryCustomAgent, nil
	}
	return ports.PromptDeliveryInCommand, nil
}

// PromptReadinessHints waits for Kiro's interactive prompt before AO injects
// the worker's first task.
func (p *Plugin) PromptReadinessHints(ctx context.Context, _ ports.LaunchConfig) (ports.PromptReadinessHints, error) {
	if err := ctx.Err(); err != nil {
		return ports.PromptReadinessHints{}, err
	}
	return ports.PromptReadinessHints{
		InitialDelay: 500 * time.Millisecond,
		Patterns:     []string{"ask a question or describe a task"},
		PollInterval: 200 * time.Millisecond,
		Timeout:      8 * time.Second,
		Lines:        80,
	}, nil
}

// GetRestoreCommand rebuilds the argv that continues an existing Kiro session:
// `kiro-cli chat --no-interactive --resume-id <agentSessionId> [trust flags]`.
// ok is false when the hook-derived native session id has not landed yet, so
// callers can fall back to fresh launch behavior.
func (p *Plugin) GetRestoreCommand(ctx context.Context, cfg ports.RestoreConfig) (cmd []string, ok bool, err error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	agentSessionID := strings.TrimSpace(cfg.Session.Metadata[ports.MetadataKeyAgentSessionID])
	if agentSessionID == "" {
		return nil, false, nil
	}

	binary, err := p.kiroBinary(ctx)
	if err != nil {
		return nil, false, err
	}

	cmd = make([]string, 0, 8)
	cmd = append(cmd, binary, "chat", "--agent", kiroAgentName, "--resume-id", agentSessionID)
	appendApprovalFlags(&cmd, cfg.Permissions)
	return cmd, true, nil
}

// SessionInfo surfaces Kiro hook-derived metadata. Metadata is intentionally
// nil for Kiro: callers get the normalized fields directly.
func (p *Plugin) SessionInfo(ctx context.Context, session ports.SessionRef) (ports.SessionInfo, bool, error) {
	if err := ctx.Err(); err != nil {
		return ports.SessionInfo{}, false, err
	}
	info, ok := agentbase.StandardSessionInfo(session)
	return info, ok, nil
}

var kiroBinarySpec = binaryutil.BinarySpec{
	Label:         "kiro",
	Names:         []string{"kiro-cli"},
	WinNames:      []string{"kiro-cli.cmd", "kiro-cli.exe", "kiro-cli"},
	UnixPaths:     []string{"/usr/local/bin/kiro-cli", "/opt/homebrew/bin/kiro-cli"},
	UnixHomePaths: binaryutil.NodeManagedUnixHomePaths("kiro-cli", []string{".kiro", "bin", "kiro-cli"}),
	NodeManaged:   true,
	// The native Kiro installer location is probed before the npm shim, matching
	// the pre-refactor order so a native install still wins when both are present.
	WinPaths: []binaryutil.WinPath{
		{Base: binaryutil.WinLocalAppData, Parts: []string{"Programs", "kiro", "kiro-cli.exe"}},
		{Base: binaryutil.WinAppData, Parts: []string{"npm", "kiro-cli.cmd"}},
		{Base: binaryutil.WinAppData, Parts: []string{"npm", "kiro-cli.exe"}},
		{Base: binaryutil.WinHome, Parts: []string{".kiro", "bin", "kiro-cli.exe"}},
	},
}

// ResolveKiroBinary returns the path to the kiro-cli binary on this machine,
// searching PATH then a handful of well-known install locations. It returns a
// wrapped ports.ErrAgentBinaryNotFound when kiro-cli is absent.
func ResolveKiroBinary(ctx context.Context) (string, error) {
	return binaryutil.ResolveBinary(ctx, kiroBinarySpec)
}

func (p *Plugin) kiroBinary(ctx context.Context) (string, error) {
	p.binaryMu.Lock()
	defer p.binaryMu.Unlock()

	if p.resolvedBinary != "" {
		return p.resolvedBinary, nil
	}

	binary, err := ResolveKiroBinary(ctx)
	if err != nil {
		return "", err
	}
	p.resolvedBinary = binary
	return binary, nil
}

// appendApprovalFlags maps AO's permission modes onto Kiro's tool-trust flags.
// Default emits no flag so Kiro uses its normal interactive approval flow.
// accept-edits grants the write-capable built-in tools; auto/bypass grant all
// tools.
func appendApprovalFlags(cmd *[]string, permissions ports.PermissionMode) {
	switch ports.NormalizePermissionMode(permissions) {
	case ports.PermissionModeDefault:
		// No flag: defer to Kiro's normal interactive approval flow.
	case ports.PermissionModeAcceptEdits:
		*cmd = append(*cmd, "--trust-tools=fs_read,fs_write")
	case ports.PermissionModeAuto:
		*cmd = append(*cmd, "--trust-all-tools")
	case ports.PermissionModeBypassPermissions:
		*cmd = append(*cmd, "--trust-all-tools")
	}
}
