// Package pi implements the Pi agent adapter: launching new interactive Pi
// sessions and resuming sessions when a native Pi session id is known.
//
// Pi (badlogic / "@earendil-works/pi-coding-agent", binary "pi") is a minimal
// terminal coding harness. AO runs Pi interactively in the session terminal
// pane. The initial prompt is delivered in-command as a trailing positional
// message; Pi's argument parser does not honor a `--` options terminator, so AO
// relies on prompts not beginning with a literal "-".
//
// System prompts are appended to Pi's default coding-assistant prompt via
// `--append-system-prompt <text>`. Pi's flag takes inline text only (no file
// variant), so a system-prompt file is read from disk and its contents are
// inlined into the flag; a read failure aborts the launch.
//
// Permissions: Pi has no permission/approval CLI flags ("No permission popups" --
// confirmation flows are built via TypeScript extensions), so AO emits no
// permission flag and defers to Pi's own behavior.
//
// Restore: Pi persists sessions to ~/.pi/agent/sessions/ and resumes
// interactively by id with `--session <id>` (partial UUIDs accepted). The native
// session id is emitted on the first line of `--mode json` output as
// {"type":"session","id":"<uuid>",...} and is captured into session metadata
// out-of-band; GetRestoreCommand reads it back from metadata. ok=false when no
// native id is known (manager falls back to a fresh launch).
//
// Hooks/activity: AO installs a workspace-local TypeScript extension and passes
// it explicitly with --extension. It reports Pi's session and agent lifecycle
// without depending on project-local extension auto-discovery or trust state.
package pi

import (
	"context"
	"os"
	"strings"
	"sync"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/agentbase"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/binaryutil"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const adapterID = "pi"

// Plugin is the Pi agent adapter. It is safe for concurrent use; the binary
// path is resolved once and cached under binaryMu.
type Plugin struct {
	agentbase.Base
	binaryMu       sync.Mutex
	resolvedBinary string
}

// New returns a ready-to-register Pi adapter.
func New() *Plugin {
	return &Plugin{}
}

var _ adapters.Adapter = (*Plugin)(nil)
var _ ports.Agent = (*Plugin)(nil)

// Manifest returns the adapter's static self-description.
func (p *Plugin) Manifest() adapters.Manifest {
	return adapters.Manifest{
		ID:          adapterID,
		Name:        "Pi",
		Description: "Run Pi worker sessions.",
		Version:     "0.0.1",
		Capabilities: []adapters.Capability{
			adapters.CapabilityAgent,
		},
	}
}

// GetConfigSpec reports the per-project agent config keys Pi understands.
func (p *Plugin) GetConfigSpec(ctx context.Context) (ports.ConfigSpec, error) {
	if err := ctx.Err(); err != nil {
		return ports.ConfigSpec{}, err
	}
	return ports.ConfigSpec{
		Fields: []ports.ConfigField{
			{
				Key:         "model",
				Type:        ports.ConfigFieldString,
				Description: "Model override passed to `pi --model`.",
			},
		},
	}, nil
}

// GetLaunchCommand builds the argv to start a new interactive Pi session:
//
//	pi [--append-system-prompt <system prompt>] [--model <model>] [<prompt>]
//
// The prompt is delivered in-command as a trailing positional message. Pi does
// not honor a `--` options terminator, so the prompt must not begin with "-".
// Pi has no permission flags, so none are emitted.
func (p *Plugin) GetLaunchCommand(ctx context.Context, cfg ports.LaunchConfig) (cmd []string, err error) {
	binary, err := p.piBinary(ctx)
	if err != nil {
		return nil, err
	}

	cmd = []string{binary}
	appendPiExtensionFlag(&cmd, cfg.WorkspacePath)
	if cfg.SystemPrompt != "" {
		cmd = append(cmd, "--append-system-prompt", cfg.SystemPrompt)
	} else if cfg.SystemPromptFile != "" {
		data, err := os.ReadFile(cfg.SystemPromptFile) //nolint:gosec // path is AO-owned launch config
		if err != nil {
			return nil, err
		}
		cmd = append(cmd, "--append-system-prompt", string(data))
	}
	appendModelFlag(&cmd, cfg.Config)
	if cfg.Prompt != "" {
		cmd = append(cmd, cfg.Prompt)
	}
	return cmd, nil
}

// GetRestoreCommand rebuilds the argv that continues an existing Pi session when
// a native session id is available in metadata. Pi resumes by id with
// `--session <id>` (partial UUIDs accepted). Until that id exists, ok is false
// and callers fall back to fresh launch behavior.
func (p *Plugin) GetRestoreCommand(ctx context.Context, cfg ports.RestoreConfig) (cmd []string, ok bool, err error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	agentSessionID := strings.TrimSpace(cfg.Session.Metadata[ports.MetadataKeyAgentSessionID])
	if agentSessionID == "" {
		return nil, false, nil
	}

	binary, err := p.piBinary(ctx)
	if err != nil {
		return nil, false, err
	}
	cmd = []string{binary}
	appendPiExtensionFlag(&cmd, cfg.Session.WorkspacePath)
	if cfg.SystemPrompt != "" {
		cmd = append(cmd, "--append-system-prompt", cfg.SystemPrompt)
	} else if cfg.SystemPromptFile != "" {
		data, err := os.ReadFile(cfg.SystemPromptFile) //nolint:gosec // path is AO-owned launch config
		if err != nil {
			return nil, false, err
		}
		cmd = append(cmd, "--append-system-prompt", string(data))
	}
	appendModelFlag(&cmd, cfg.Config)
	cmd = append(cmd, "--session", agentSessionID)
	return cmd, true, nil
}

// SessionInfo surfaces the native Pi session id captured by the managed
// extension's lifecycle callbacks.
func (p *Plugin) SessionInfo(ctx context.Context, session ports.SessionRef) (ports.SessionInfo, bool, error) {
	if err := ctx.Err(); err != nil {
		return ports.SessionInfo{}, false, err
	}
	info, ok := agentbase.StandardSessionInfo(session)
	return info, ok, nil
}

func appendModelFlag(cmd *[]string, cfg ports.AgentConfig) {
	if model := strings.TrimSpace(cfg.Model); model != "" {
		*cmd = append(*cmd, "--model", model)
	}
}

var piBinarySpec = binaryutil.BinarySpec{
	Label:         "pi",
	Names:         []string{"pi"},
	WinNames:      []string{"pi.cmd", "pi.exe", "pi"},
	UnixPaths:     []string{"/usr/local/bin/pi", "/opt/homebrew/bin/pi"},
	UnixHomePaths: binaryutil.NodeManagedUnixHomePaths("pi", []string{".pi", "bin", "pi"}),
	NodeManaged:   true,
	WinPaths: []binaryutil.WinPath{
		{Base: binaryutil.WinAppData, Parts: []string{"npm", "pi.cmd"}},
		{Base: binaryutil.WinAppData, Parts: []string{"npm", "pi.exe"}},
	},
}

// ResolvePiBinary finds the `pi` binary, searching PATH then common install
// locations. It returns a wrapped ports.ErrAgentBinaryNotFound when Pi is absent.
func ResolvePiBinary(ctx context.Context) (string, error) {
	return binaryutil.ResolveBinary(ctx, piBinarySpec)
}

func (p *Plugin) piBinary(ctx context.Context) (string, error) {
	p.binaryMu.Lock()
	defer p.binaryMu.Unlock()

	if p.resolvedBinary != "" {
		return p.resolvedBinary, nil
	}

	binary, err := ResolvePiBinary(ctx)
	if err != nil {
		return "", err
	}
	p.resolvedBinary = binary
	return binary, nil
}
