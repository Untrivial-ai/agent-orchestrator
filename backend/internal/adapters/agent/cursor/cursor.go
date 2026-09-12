// Package cursor implements the Cursor CLI agent adapter: launching new
// sessions, resuming hook-tracked sessions, installing workspace-local hooks,
// and reading hook-derived session info.
//
// AO-managed sessions derive native session identity and display
// metadata from Cursor hooks instead of transcript/cache scans. The driven
// binary is `cursor-agent` (not the `cursor` editor binary).
package cursor

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/agentbase"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/binaryutil"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/hookutil"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/internal/termtheme"
	"github.com/aoagents/agent-orchestrator/backend/pkg/agentruntime"
)

// Plugin is the Cursor agent adapter. It is safe for concurrent use; the binary
// path is resolved once and cached under binaryMu.
type Plugin struct {
	agentbase.Base
	binaryMu       sync.Mutex
	resolvedBinary string
}

// New returns a ready-to-register Cursor adapter.
func New() *Plugin {
	return &Plugin{}
}

var _ adapters.Adapter = (*Plugin)(nil)
var _ ports.Agent = (*Plugin)(nil)
var _ ports.StartupInputReadinessSignaler = (*Plugin)(nil)

// FirstSignalProvesInputReady reports that Cursor's sessionStart hook is
// emitted only after pre-session startup dialogs, including project MCP server
// approval, have cleared. Before that signal, pane input may be consumed by a
// dialog instead of the agent composer.
func (p *Plugin) FirstSignalProvesInputReady() bool { return true }

// cursorDataDir returns the isolated Cursor profile AO uses for managed Cursor
// sessions. This keeps Cursor's trust/cache state under AO_DATA_DIR instead of
// the user's normal ~/.cursor profile.
func cursorDataDir(dataDir string) string {
	return filepath.Join(dataDir, "cursor")
}

// AugmentRuntimeEnv points cursor-agent at AO's isolated Cursor profile so
// workspace trust seeded during hook installation is read by the launched
// process without modifying the user's normal Cursor state.
func (p *Plugin) AugmentRuntimeEnv(env map[string]string, dataDir string) {
	if strings.TrimSpace(dataDir) == "" {
		return
	}
	env[cursorDataDirEnv] = cursorDataDir(dataDir)
	termtheme.Apply(env, dataDir)
}

// Manifest returns the adapter's static self-description.
func (p *Plugin) Manifest() adapters.Manifest {
	return adapters.Manifest{
		ID:          "cursor",
		Name:        "Cursor",
		Description: "Run Cursor CLI agent worker sessions.",
		Version:     "0.0.1",
		Capabilities: []adapters.Capability{
			adapters.CapabilityAgent,
		},
	}
}

// GetConfigSpec reports Cursor CLI's optional model override.
func (p *Plugin) GetConfigSpec(ctx context.Context) (ports.ConfigSpec, error) {
	return agentbase.ModelConfigSpec(ctx, "Model override passed to `cursor-agent --model`.")
}

// GetLaunchCommand builds the argv to start a new interactive Cursor CLI
// session:
//
//	cursor-agent [permission flags] <prompt>
//
// The prompt is positional and must come last, so a leading "-" is not read as
// a flag.
//
// Cursor has no inline/file system-prompt flag: it reads workspace rule files
// (AGENTS.md, .cursor/rules, CLAUDE.md). SystemPrompt/SystemPromptFile are
// therefore not injected via a launch flag here.
func (p *Plugin) GetLaunchCommand(ctx context.Context, cfg ports.LaunchConfig) (cmd []string, err error) {
	binary, err := p.cursorBinary(ctx)
	if err != nil {
		return nil, err
	}

	return agentruntime.BuildLaunchCommand(agentruntime.LaunchConfig{
		Harness:    agentruntime.HarnessCursor,
		Binary:     binary,
		Model:      cfg.Config.Model,
		Prompt:     cfg.Prompt,
		Permission: agentruntime.PermissionPolicy(cfg.Permissions),
	})
}

// GetRestoreCommand rebuilds the argv that continues an existing Cursor CLI
// session:
//
//	cursor-agent [perm flags] --resume <id>
//
// ok is false when the hook-derived native session id has not landed yet, so
// callers can fall back to fresh launch behavior. ports.RestoreConfig carries no
// prompt, so none is appended.
func (p *Plugin) GetRestoreCommand(ctx context.Context, cfg ports.RestoreConfig) (cmd []string, ok bool, err error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	if _, ok := agentruntime.RestoreIdentity(
		agentruntime.HarnessCursor,
		cfg.Session.ID,
		cfg.Session.Metadata,
	); !ok {
		return nil, false, nil
	}
	binary, err := p.cursorBinary(ctx)
	if err != nil {
		return nil, false, err
	}

	return agentruntime.BuildRestoreCommand(agentruntime.RestoreConfig{
		Harness:    agentruntime.HarnessCursor,
		Binary:     binary,
		SessionID:  cfg.Session.ID,
		Metadata:   cfg.Session.Metadata,
		Model:      cfg.Config.Model,
		Permission: agentruntime.PermissionPolicy(cfg.Permissions),
	})
}

// SessionInfo surfaces Cursor hook-derived metadata. Metadata is intentionally
// nil for Cursor: callers get the normalized fields directly.
func (p *Plugin) SessionInfo(ctx context.Context, session ports.SessionRef) (ports.SessionInfo, bool, error) {
	if err := ctx.Err(); err != nil {
		return ports.SessionInfo{}, false, err
	}
	info, ok := agentbase.StandardSessionInfo(session)
	return info, ok, nil
}

// ResolveCursorBinary returns the path to the cursor-agent binary on this
// machine, searching PATH then a handful of well-known install locations.
// Returns "cursor-agent" as a last-ditch fallback so callers see a clear
// "command not found" rather than an empty argv.
func ResolveCursorBinary(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}

	if runtime.GOOS == "windows" {
		for _, name := range []string{"cursor-agent.exe", "cursor-agent.cmd", "cursor-agent"} {
			path, err := exec.LookPath(name)
			if err == nil && path != "" {
				return path, nil
			}
			if err := ctx.Err(); err != nil {
				return "", err
			}
		}
		for _, candidate := range binaryutil.WindowsPackageManagerBinCandidates("cursor-agent") {
			if hookutil.IsExecutableFile(candidate) {
				return candidate, nil
			}
			if err := ctx.Err(); err != nil {
				return "", err
			}
		}
		return "", fmt.Errorf("cursor: %w", ports.ErrAgentBinaryNotFound)
	}

	if path, err := exec.LookPath("cursor-agent"); err == nil && path != "" {
		return path, nil
	}

	candidates := []string{}
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates, filepath.Join(home, ".local", "bin", "cursor-agent"))
		candidates = append(candidates, binaryutil.UnixPackageManagerBinCandidates(home, "cursor-agent")...)
	}
	candidates = append(candidates,
		"/usr/local/bin/cursor-agent",
		"/opt/homebrew/bin/cursor-agent",
	)

	for _, candidate := range candidates {
		if hookutil.IsExecutableFile(candidate) {
			return candidate, nil
		}
		if err := ctx.Err(); err != nil {
			return "", err
		}
	}

	return "", fmt.Errorf("cursor: %w", ports.ErrAgentBinaryNotFound)
}

func (p *Plugin) cursorBinary(ctx context.Context) (string, error) {
	p.binaryMu.Lock()
	defer p.binaryMu.Unlock()

	if p.resolvedBinary != "" {
		return p.resolvedBinary, nil
	}

	binary, err := ResolveCursorBinary(ctx)
	if err != nil {
		return "", err
	}
	p.resolvedBinary = binary
	return binary, nil
}
