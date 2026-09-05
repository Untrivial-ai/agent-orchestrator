// Package goose implements the Goose (Block) agent adapter: launching new
// interactive sessions, resuming hook-tracked sessions, installing
// workspace-local lifecycle hooks, and reading hook-derived session info.
//
// Goose (binary "goose") is launched as `goose run -t "" --interactive`, and
// AO injects prompted tasks after startup. Its non-interactive
// `goose run -t "<text>"` mode exits after the prompt completes, which is not a
// usable lifecycle for AO worker terminals. Goose has a native
// Claude-Code-style lifecycle hook system (released 2026-05): a plugin directory
// under <workspace>/.agents/plugins/<name>/hooks/hooks.json is auto-discovered
// at startup and its commands run on SessionStart / UserPromptSubmit / Stop /
// etc. AO installs its hooks there, so AO derives native session identity and
// activity from Goose hooks (Tier A), the same way the Codex adapter does.
//
// Permission/approval is controlled by the GOOSE_MODE environment variable
// (auto / approve / chat / smart_approve), not a CLI flag, so non-default modes
// are delivered as an `env GOOSE_MODE=<mode>` argv prefix (the same technique
// the opencode adapter uses for OPENCODE_PERMISSION). The default mode emits no
// prefix so Goose defers to the user's own config.
//
// Note: the AO repo also vendors pressly/goose as its SQLite migration tool,
// but that is a different Go import path; this package's name `goose` only
// collides at the import-alias level, which central wiring resolves.
package goose

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/agentbase"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/binaryutil"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	aoprocess "github.com/aoagents/agent-orchestrator/backend/internal/process"
)

const (
	adapterID = "goose"

	// gooseModeEnvVar is the only permission-control surface Goose honors: the
	// approval mode is read from this process env var, not from any CLI flag.
	gooseModeEnvVar = "GOOSE_MODE"
)

// Plugin is the Goose agent adapter. It is safe for concurrent use; the binary
// path is resolved once and cached under binaryMu.
type Plugin struct {
	agentbase.Base
	binaryMu       sync.Mutex
	resolvedBinary string
}

// New returns a ready-to-register Goose adapter.
func New() *Plugin {
	return &Plugin{}
}

var _ adapters.Adapter = (*Plugin)(nil)
var _ ports.Agent = (*Plugin)(nil)
var _ ports.AgentBinaryPresenceResolver = (*Plugin)(nil)

// Manifest returns the adapter's static self-description.
func (p *Plugin) Manifest() adapters.Manifest {
	return adapters.Manifest{
		ID:          adapterID,
		Name:        "Goose",
		Description: "Run Goose worker sessions.",
		Version:     "0.0.1",
		Capabilities: []adapters.Capability{
			adapters.CapabilityAgent,
		},
	}
}

// GetConfigSpec reports the per-project agent config keys Goose understands.
func (p *Plugin) GetConfigSpec(ctx context.Context) (ports.ConfigSpec, error) {
	if err := ctx.Err(); err != nil {
		return ports.ConfigSpec{}, err
	}
	return ports.ConfigSpec{
		Fields: []ports.ConfigField{
			{
				Key:         "model",
				Type:        ports.ConfigFieldString,
				Description: "Model override passed to `goose run --model`.",
			},
		},
	}, nil
}

// GetLaunchCommand builds the argv to start a new interactive Goose session:
//
//	[env GOOSE_MODE=<mode>] goose run [--system <text>] -t "" --interactive
//
// Prompted tasks are delivered after startup by the session manager rather than
// via `-t <prompt>`, because that mode exits when the prompt completes. A
// non-default permission mode is rendered as an `env GOOSE_MODE=<mode>` prefix
// because Goose reads its approval mode from the environment, not from a flag.
// System instructions, when present, are passed via `--system`. Goose requires
// one of --instructions, --text, or --recipe, so AO supplies empty text plus
// --interactive to land in an input-ready terminal without inventing an initial
// task.
func (p *Plugin) GetLaunchCommand(ctx context.Context, cfg ports.LaunchConfig) (cmd []string, err error) {
	binary, err := p.gooseBinary(ctx)
	if err != nil {
		return nil, err
	}

	cmd = append(gooseModeEnvPrefix(cfg.Permissions), binary, "run")

	systemPrompt, err := systemPromptText(cfg)
	if err != nil {
		return nil, err
	}
	if systemPrompt != "" {
		cmd = append(cmd, "--system", systemPrompt)
	}
	appendModelFlag(&cmd, cfg.Config)

	cmd = append(cmd, "-t", "", "--interactive")

	return cmd, nil
}

// GetPromptDeliveryStrategy reports that AO should inject prompted Goose tasks
// into the interactive terminal after startup. Goose's `-t <prompt>` mode exits
// after the single prompt completes.
func (p *Plugin) GetPromptDeliveryStrategy(ctx context.Context, _ ports.LaunchConfig) (ports.PromptDeliveryStrategy, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return ports.PromptDeliveryAfterStart, nil
}

// GetRestoreCommand rebuilds the argv that continues an existing Goose session:
//
//	[env GOOSE_MODE=<mode>] goose run --system <text> --resume --session-id <agentSessionId>
//
// ok is false when the hook-derived native session id has not landed yet, so
// callers can fall back to fresh launch behavior. AO deliberately uses run
// rather than session here: run supports --system alongside --resume, so the
// current derived system instructions are reapplied without replaying the
// original task or relying on Goose to persist invocation-level instructions.
func (p *Plugin) GetRestoreCommand(ctx context.Context, cfg ports.RestoreConfig) (cmd []string, ok bool, err error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	agentSessionID := strings.TrimSpace(cfg.Session.Metadata[ports.MetadataKeyAgentSessionID])
	if agentSessionID == "" {
		return nil, false, nil
	}

	binary, err := p.gooseBinary(ctx)
	if err != nil {
		return nil, false, err
	}

	cmd = append(gooseModeEnvPrefix(cfg.Permissions), binary, "run")
	systemPrompt, err := restoreSystemPromptText(cfg)
	if err != nil {
		return nil, false, err
	}
	if systemPrompt != "" {
		cmd = append(cmd, "--system", systemPrompt)
	}
	appendModelFlag(&cmd, cfg.Config)
	cmd = append(cmd, "--resume", "--session-id", agentSessionID)
	return cmd, true, nil
}

// SessionInfo surfaces Goose hook-derived metadata. Metadata is intentionally
// nil for Goose: callers get the normalized fields directly.
func (p *Plugin) SessionInfo(ctx context.Context, session ports.SessionRef) (ports.SessionInfo, bool, error) {
	if err := ctx.Err(); err != nil {
		return ports.SessionInfo{}, false, err
	}
	info, ok := agentbase.StandardSessionInfo(session)
	return info, ok, nil
}

// systemPromptText returns the system instructions to inject. Goose's `--system`
// flag takes inline text only (no file variant), so a system-prompt file is read
// from disk only when inline instructions are unavailable.
func systemPromptText(cfg ports.LaunchConfig) (string, error) {
	return systemPromptTextFrom(cfg.SystemPrompt, cfg.SystemPromptFile)
}

func restoreSystemPromptText(cfg ports.RestoreConfig) (string, error) {
	return systemPromptTextFrom(cfg.SystemPrompt, cfg.SystemPromptFile)
}

func systemPromptTextFrom(inline, file string) (string, error) {
	if inline != "" {
		return inline, nil
	}
	if file == "" {
		return "", nil
	}
	data, err := os.ReadFile(file) //nolint:gosec // path is AO-owned launch config
	if err != nil {
		return "", fmt.Errorf("read %s: %w", file, err)
	}
	if text := strings.TrimSpace(string(data)); text != "" {
		return text, nil
	}
	return "", nil
}

// appendModelFlag appends a trimmed --model flag when a model override is
// configured. Goose pairs a model with a --provider; a bare --model overrides
// the model within the configured provider, which matches AO's single-string
// agentConfig.model contract.
func appendModelFlag(cmd *[]string, cfg ports.AgentConfig) {
	if model := strings.TrimSpace(cfg.Model); model != "" {
		*cmd = append(*cmd, "--model", model)
	}
}

// gooseModeEnvPrefix renders mode as an `env GOOSE_MODE=<mode>` argv prefix, or
// nil for the default mode.
//
// The var must reach Goose as a process env var, not an argv flag. The runtime
// runs the argv through a shell, which execs `env`, which sets the var and execs
// goose. A bare `GOOSE_MODE=...` argv element would not work: the runtime
// shell-quotes every element, and a quoted token is run as a command rather than
// read as an assignment — hence the explicit `env` wrapper. POSIX-only, which
// matches the runtime.
func gooseModeEnvPrefix(mode ports.PermissionMode) []string {
	value := gooseMode(mode)
	if value == "" {
		return nil
	}
	return []string{"env", gooseModeEnvVar + "=" + value}
}

// gooseMode maps an AO permission mode onto Goose's GOOSE_MODE value.
//
//   - default            → "": no env; Goose's own config decides approvals.
//   - accept-edits       → smart_approve: auto-approves safe edits, asks on risk.
//   - auto               → auto: fully autonomous, no approval prompts.
//   - bypass-permissions → auto: Goose's fully-autonomous mode is the nearest
//     equivalent to bypass.
func gooseMode(mode ports.PermissionMode) string {
	switch ports.NormalizePermissionMode(mode) {
	case ports.PermissionModeAcceptEdits:
		return "smart_approve"
	case ports.PermissionModeAuto:
		return "auto"
	case ports.PermissionModeBypassPermissions:
		return "auto"
	default:
		return ""
	}
}

// gooseBinarySpec locates the goose binary: PATH first, then the install
// script's ~/.local/bin, Homebrew, Cargo, and npm global locations.
var gooseBinarySpec = binaryutil.BinarySpec{
	Label:         "goose",
	Names:         []string{"goose"},
	WinNames:      []string{"goose.cmd", "goose.exe", "goose"},
	UnixPaths:     []string{"/usr/local/bin/goose", "/opt/homebrew/bin/goose"},
	UnixHomePaths: binaryutil.NodeManagedUnixHomePaths("goose", []string{".cargo", "bin", "goose"}),
	NodeManaged:   true,
	WinPaths: []binaryutil.WinPath{
		{Base: binaryutil.WinAppData, Parts: []string{"npm", "goose.cmd"}},
		{Base: binaryutil.WinAppData, Parts: []string{"npm", "goose.exe"}},
		{Base: binaryutil.WinLocalAppData, Parts: []string{"Programs", "goose", "goose.exe"}},
		{Base: binaryutil.WinHome, Parts: []string{".cargo", "bin", "goose.exe"}},
	},
}

// ResolveGooseBinary returns the Block/AI Goose path, or a wrapped
// ports.ErrAgentBinaryNotFound when it is absent or a different goose CLI.
func ResolveGooseBinary(ctx context.Context) (string, error) {
	return resolveGooseBinary(ctx, gooseBinarySpec)
}

func resolveGooseBinary(ctx context.Context, spec binaryutil.BinarySpec) (string, error) {
	candidates, err := binaryutil.ResolveBinaryCandidates(ctx, spec)
	if err != nil {
		return "", err
	}

	for _, candidate := range candidates {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if isOfficialGooseBinary(ctx, candidate) {
			return candidate, nil
		}
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return "", fmt.Errorf("%s: %w", spec.Label, ports.ErrAgentBinaryNotFound)
}

func isOfficialGooseBinary(ctx context.Context, binary string) bool {
	probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	out, err := aoprocess.CommandContextForPath(probeCtx, binary, "--version").CombinedOutput()
	if err != nil || probeCtx.Err() != nil {
		return false
	}
	version := strings.TrimSpace(string(out))
	return strings.HasPrefix(version, "goose ") && !strings.HasPrefix(version, "goose version:")
}

func (p *Plugin) gooseBinary(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}

	p.binaryMu.Lock()
	binary := p.resolvedBinary
	p.binaryMu.Unlock()
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if binary != "" {
		return binary, nil
	}

	// Binary identity probing can execute an untrusted PATH collision and may
	// take several seconds. Keep it outside the cache mutex so other callers
	// can observe cancellation and do not queue behind the probe.
	binary, err := ResolveGooseBinary(ctx)
	if err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}

	p.binaryMu.Lock()
	if p.resolvedBinary != "" {
		binary = p.resolvedBinary
	} else {
		p.resolvedBinary = binary
	}
	p.binaryMu.Unlock()
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return binary, nil
}
