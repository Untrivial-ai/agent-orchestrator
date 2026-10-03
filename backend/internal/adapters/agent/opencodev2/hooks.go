package opencodev2

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/hookutil"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/opencode"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const (
	v2PluginFileName = "ao-activity-v2.ts"
	v2PluginSentinel = "agent-orchestrator: managed opencode-v2 activity plugin"
	v1PluginFileName = "ao-activity.ts"
	v1PluginSentinel = "agent-orchestrator: managed opencode activity plugin"
)

//go:embed assets/ao-activity.ts
var v2PluginSource string

// GetAgentHooks installs the v2 activity plugin and shared using-ao skill. The
// v1 and v2 plugins cannot safely load together, so a marker-verified v1 plugin
// is removed before the v2 artifact is written. Foreign files are never removed.
func (p *Plugin) GetAgentHooks(ctx context.Context, cfg ports.WorkspaceHookConfig) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(cfg.WorkspacePath) == "" {
		return errors.New("opencodev2.GetAgentHooks: WorkspacePath is required")
	}
	path := v2PluginPath(cfg.WorkspacePath)
	managed, exists, err := managedV2Plugin(path, v2PluginSentinel)
	if err != nil {
		return fmt.Errorf("opencodev2.GetAgentHooks: inspect plugin: %w", err)
	}
	if exists && !managed {
		return fmt.Errorf("opencodev2.GetAgentHooks: refusing to overwrite non-AO file at %s", path)
	}
	if err := removeV2ManagedPlugin(filepath.Join(filepath.Dir(path), v1PluginFileName), v1PluginSentinel); err != nil {
		return fmt.Errorf("opencodev2.GetAgentHooks: remove managed OpenCode 1 plugin: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("opencodev2.GetAgentHooks: create plugin dir: %w", err)
	}
	if err := hookutil.AtomicWriteFile(path, []byte(v2PluginSource), 0o600); err != nil {
		return fmt.Errorf("opencodev2.GetAgentHooks: write plugin: %w", err)
	}
	if err := hookutil.EnsureWorkspaceGitignore(filepath.Dir(path), v2PluginFileName); err != nil {
		return fmt.Errorf("opencodev2.GetAgentHooks: gitignore: %w", err)
	}
	if err := opencode.InstallUsingAOSkill(cfg.WorkspacePath); err != nil {
		return fmt.Errorf("opencodev2.GetAgentHooks: %w", err)
	}
	return nil
}

// UninstallHooks removes only marker-verified v2 artifacts and the shared
// marker-verified using-ao skill.
func (p *Plugin) UninstallHooks(ctx context.Context, workspacePath string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(workspacePath) == "" {
		return errors.New("opencodev2.UninstallHooks: workspacePath is required")
	}
	if err := removeV2ManagedPlugin(v2PluginPath(workspacePath), v2PluginSentinel); err != nil {
		return fmt.Errorf("opencodev2.UninstallHooks: remove plugin: %w", err)
	}
	if err := opencode.UninstallUsingAOSkill(workspacePath); err != nil {
		return fmt.Errorf("opencodev2.UninstallHooks: %w", err)
	}
	return nil
}

// AreHooksInstalled reports whether the v2 path contains AO's v2 marker.
func (p *Plugin) AreHooksInstalled(ctx context.Context, workspacePath string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if strings.TrimSpace(workspacePath) == "" {
		return false, errors.New("opencodev2.AreHooksInstalled: workspacePath is required")
	}
	managed, _, err := managedV2Plugin(v2PluginPath(workspacePath), v2PluginSentinel)
	if err != nil {
		return false, fmt.Errorf("opencodev2.AreHooksInstalled: %w", err)
	}
	return managed, nil
}

func v2PluginPath(workspacePath string) string {
	return filepath.Join(workspacePath, ".opencode", "plugins", v2PluginFileName)
}

func managedV2Plugin(path, sentinel string) (managed, exists bool, err error) {
	data, err := os.ReadFile(path) //nolint:gosec // path is rooted in the caller-owned workspace
	if errors.Is(err, os.ErrNotExist) {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	return strings.Contains(string(data), sentinel), true, nil
}

func removeV2ManagedPlugin(path, sentinel string) error {
	managed, _, err := managedV2Plugin(path, sentinel)
	if err != nil || !managed {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
