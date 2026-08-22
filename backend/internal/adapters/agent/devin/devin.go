// Package devin implements the Devin ("Devin for Terminal", Cognition) agent
// adapter.
//
// Devin for Terminal (binary "devin") is Cognition's terminal coding agent. It
// has a documented Claude Code compatibility layer: it imports `.claude/`
// configuration (commands, subagents, and Claude Code lifecycle hooks), storing
// the converted hooks in `.devin/hooks.v1.json`. Because of this, AO reuses the
// Claude Code hook installer (which writes .claude/settings.local.json with AO
// hook commands) and Devin picks them up via its compat layer. This makes Devin
// a Tier B (Claude-compat) adapter, mirroring the grok adapter.
//
// Launch starts interactive Devin. Prompted worker tasks are passed after `--`
// so Devin starts in interactive implementation mode with the task already
// loaded. AO intentionally avoids `-p/--print`, which is non-interactive.
// Permission handling uses `--permission-mode`; Default emits no flag (defer to
// Devin's config), AcceptEdits maps to `accept-edits`, Auto maps to `auto`, and
// BypassPermissions maps to `dangerous`.
//
// Restore prefers a native session id from AO session metadata via `-r <id>`
// when one is available.
package devin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/agentbase"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/binaryutil"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

var devinBinarySpec = binaryutil.BinarySpec{
	Label:         "devin",
	Names:         []string{"devin"},
	WinNames:      []string{"devin.cmd", "devin.exe", "devin"},
	UnixPaths:     []string{"/usr/local/bin/devin", "/opt/homebrew/bin/devin"},
	UnixHomePaths: binaryutil.NodeManagedUnixHomePaths("devin", []string{".devin", "bin", "devin"}),
	NodeManaged:   true,
	WinPaths: []binaryutil.WinPath{
		{Base: binaryutil.WinHome, Parts: []string{".devin", "bin", "devin.exe"}},
	},
}

// Plugin is the Devin for Terminal agent adapter.
type Plugin struct {
	agentbase.Base
	binaryMu       sync.Mutex
	resolvedBinary string
}

// New returns a ready-to-register Devin adapter.
func New() *Plugin {
	return &Plugin{}
}

var _ adapters.Adapter = (*Plugin)(nil)
var _ ports.Agent = (*Plugin)(nil)

// Manifest returns the adapter's static self-description.
func (p *Plugin) Manifest() adapters.Manifest {
	return adapters.Manifest{
		ID:          "devin",
		Name:        "Devin",
		Description: "Run Cognition Devin for Terminal worker sessions.",
		Version:     "0.0.1",
		Capabilities: []adapters.Capability{
			adapters.CapabilityAgent,
		},
	}
}

// GetConfigSpec reports Devin's optional model override.
func (p *Plugin) GetConfigSpec(ctx context.Context) (ports.ConfigSpec, error) {
	return agentbase.ModelConfigSpec(ctx, "Model override passed to `devin --model`.")
}

// GetLaunchCommand builds `devin [--permission-mode <mode>] [-- <prompt>]`.
//
// The `-- <prompt>` form starts an interactive session. Do not use `-p`, which
// is Devin's non-interactive print mode.
func (p *Plugin) GetLaunchCommand(ctx context.Context, cfg ports.LaunchConfig) (cmd []string, err error) {
	binary, err := p.devinBinary(ctx)
	if err != nil {
		return nil, err
	}

	cmd = []string{binary}
	appendApprovalFlags(&cmd, cfg.Permissions)
	agentbase.AppendModelFlag(&cmd, cfg.Config, "--model")
	if prompt := strings.TrimSpace(cfg.Prompt); prompt != "" {
		cmd = append(cmd, "--", prompt)
	}

	return cmd, nil
}

// GetPromptDeliveryStrategy reports that prompted Devin sessions receive the
// initial task in argv via `-- <prompt>`.
func (p *Plugin) GetPromptDeliveryStrategy(ctx context.Context, _ ports.LaunchConfig) (ports.PromptDeliveryStrategy, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return ports.PromptDeliveryInCommand, nil
}

// PreLaunch records the AO worktree as trusted before Devin starts. Devin keeps
// its own trusted_workspaces.json for the blocking "do you trust this folder?"
// prompt; the Claude-compatible trust bit is also written because Devin imports
// some Claude Code configuration.
func (p *Plugin) PreLaunch(ctx context.Context, cfg ports.LaunchConfig) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if cfg.WorkspacePath == "" {
		return nil
	}
	nativePath, err := devinTrustedWorkspacesPath()
	if err != nil {
		return err
	}
	if err := ensureDevinNativeWorkspaceTrusted(nativePath, cfg.WorkspacePath); err != nil {
		return err
	}
	cfgPath, err := devinClaudeConfigPath()
	if err != nil {
		return err
	}
	return ensureDevinWorkspaceTrusted(cfgPath, cfg.WorkspacePath)
}

// GetAgentHooks installs Devin's AO-managed workspace hook configuration.
func (p *Plugin) GetAgentHooks(ctx context.Context, cfg ports.WorkspaceHookConfig) error {
	return devinHooks.Install(ctx, cfg.WorkspacePath)
}

// GetRestoreCommand builds `devin [--permission-mode <mode>] -r <agentSessionId>`
// when we have a hook-captured native id. ok=false otherwise (fall back to fresh
// launch in the manager).
func (p *Plugin) GetRestoreCommand(ctx context.Context, cfg ports.RestoreConfig) (cmd []string, ok bool, err error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	agentSessionID := strings.TrimSpace(cfg.Session.Metadata[ports.MetadataKeyAgentSessionID])
	if agentSessionID == "" {
		return nil, false, nil
	}

	binary, err := p.devinBinary(ctx)
	if err != nil {
		return nil, false, err
	}

	cmd = make([]string, 0, 5)
	cmd = append(cmd, binary)
	appendApprovalFlags(&cmd, cfg.Permissions)
	agentbase.AppendModelFlag(&cmd, cfg.Config, "--model")
	cmd = append(cmd, "-r", agentSessionID)
	return cmd, true, nil
}

// SessionInfo reads metadata under AO's normalized keys
// ("title", "summary", "agentSessionId").
func (p *Plugin) SessionInfo(ctx context.Context, session ports.SessionRef) (ports.SessionInfo, bool, error) {
	if err := ctx.Err(); err != nil {
		return ports.SessionInfo{}, false, err
	}
	info, ok := agentbase.StandardSessionInfo(session)
	return info, ok, nil
}

// ResolveDevinBinary finds the `devin` binary (Cognition Devin for Terminal CLI).
func ResolveDevinBinary(ctx context.Context) (string, error) {
	return binaryutil.ResolveBinary(ctx, devinBinarySpec)
}

func (p *Plugin) devinBinary(ctx context.Context) (string, error) {
	p.binaryMu.Lock()
	defer p.binaryMu.Unlock()

	if p.resolvedBinary != "" {
		return p.resolvedBinary, nil
	}

	binary, err := ResolveDevinBinary(ctx)
	if err != nil {
		return "", err
	}
	p.resolvedBinary = binary
	return binary, nil
}

// appendApprovalFlags maps AO's permission modes onto Devin's native permission
// values.
func appendApprovalFlags(cmd *[]string, permissions ports.PermissionMode) {
	switch ports.NormalizePermissionMode(permissions) {
	case ports.PermissionModeDefault:
		// No flag: defer to Devin's config.
	case ports.PermissionModeAcceptEdits:
		*cmd = append(*cmd, "--permission-mode", "accept-edits")
	case ports.PermissionModeAuto:
		*cmd = append(*cmd, "--permission-mode", "auto")
	case ports.PermissionModeBypassPermissions:
		*cmd = append(*cmd, "--permission-mode", "dangerous")
	}
}

func devinClaudeConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("devin: resolve home directory: %w", err)
	}
	return filepath.Join(home, ".claude.json"), nil
}

func devinTrustedWorkspacesPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("devin: resolve home directory: %w", err)
	}
	return filepath.Join(home, ".local", "share", "devin", "cli", "trusted_workspaces.json"), nil
}

// devinTrustMu serializes trust-file writes within the daemon process.
var devinTrustMu sync.Mutex

type devinTrustedWorkspaces struct {
	TrustedPaths []string `json:"trusted_paths"`
}

func ensureDevinNativeWorkspaceTrusted(configPath, workspacePath string) error {
	devinTrustMu.Lock()
	defer devinTrustMu.Unlock()

	root := devinTrustedWorkspaces{}
	data, err := os.ReadFile(configPath)
	switch {
	case err == nil:
		if len(bytes.TrimSpace(data)) == 0 {
			// A zero-byte read means we may have caught some other writer
			// mid-truncate. Treat it like a corrupt file, not a missing one:
			// refuse to write rather than silently replacing the user's real
			// config (oauthAccount, projects history, etc.) with one containing
			// only the trust entry.
			return fmt.Errorf("devin: %s is empty; refusing to overwrite", configPath)
		}
		if err := json.Unmarshal(data, &root); err != nil {
			return fmt.Errorf("devin: parse %s: %w", configPath, err)
		}
	case os.IsNotExist(err):
		// Treat as empty config; we'll create it.
	default:
		return fmt.Errorf("devin: read %s: %w", configPath, err)
	}

	for _, path := range root.TrustedPaths {
		if path == workspacePath {
			return nil
		}
	}
	root.TrustedPaths = append(root.TrustedPaths, workspacePath)

	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return fmt.Errorf("devin: encode %s: %w", configPath, err)
	}
	return writeDevinTrustFile(configPath, out)
}

func ensureDevinWorkspaceTrusted(configPath, workspacePath string) error {
	devinTrustMu.Lock()
	defer devinTrustMu.Unlock()

	root := map[string]any{}
	data, err := os.ReadFile(configPath)
	switch {
	case err == nil:
		if len(bytes.TrimSpace(data)) == 0 {
			// A zero-byte read means we may have caught some other writer
			// mid-truncate. Treat it like a corrupt file, not a missing one:
			// refuse to write rather than silently replacing the user's real
			// config (oauthAccount, projects history, etc.) with one containing
			// only the trust entry.
			return fmt.Errorf("devin: %s is empty; refusing to overwrite", configPath)
		}
		if err := json.Unmarshal(data, &root); err != nil {
			return fmt.Errorf("devin: parse %s: %w", configPath, err)
		}
	case os.IsNotExist(err):
		// Treat as empty config; we'll create it.
	default:
		return fmt.Errorf("devin: read %s: %w", configPath, err)
	}

	projects, _ := root["projects"].(map[string]any)
	if projects == nil {
		projects = map[string]any{}
		root["projects"] = projects
	}

	entry, _ := projects[workspacePath].(map[string]any)
	if entry == nil {
		entry = map[string]any{}
		projects[workspacePath] = entry
	}

	if trusted, ok := entry["hasTrustDialogAccepted"].(bool); ok && trusted {
		return nil
	}
	entry["hasTrustDialogAccepted"] = true

	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return fmt.Errorf("devin: encode %s: %w", configPath, err)
	}

	return writeDevinTrustFile(configPath, out)
}

func writeDevinTrustFile(configPath string, out []byte) error {
	dir := filepath.Dir(configPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("devin: create config dir: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".claude.json.tmp-*")
	if err != nil {
		return fmt.Errorf("devin: create temp config: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	if _, err := tmp.Write(out); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("devin: write temp config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("devin: close temp config: %w", err)
	}
	if err := os.Rename(tmpName, configPath); err != nil {
		return fmt.Errorf("devin: replace config: %w", err)
	}
	return nil
}
