package mimocode

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	_ "embed"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/hookutil"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/internal/skillassets"
)

const (
	mimoConfigDirName   = ".mimocode"
	mimoHookFileName    = "ao-activity.ts"
	mimoHookSentinel    = "agent-orchestrator: managed mimo-code activity hook"
	mimoAgentSentinel   = "agent-orchestrator: managed mimo-code agent"
	mimoSkillMarkerFile = ".using-ao.ao-managed"
	mimoSkillSentinel   = "agent-orchestrator: managed mimo-code using-ao skill"
)

//go:embed assets/ao-activity.ts
var mimoHookSource string

// GetAgentHooks installs AO-owned MiMo Code hooks and standing instructions.
func (p *Plugin) GetAgentHooks(ctx context.Context, cfg ports.WorkspaceHookConfig) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(cfg.WorkspacePath) == "" {
		return errors.New("mimocode.GetAgentHooks: WorkspacePath is required")
	}
	if err := writeManagedFile(mimoHookPath(cfg.WorkspacePath), mimoHookSentinel, []byte(mimoHookSource)); err != nil {
		return fmt.Errorf("mimocode.GetAgentHooks: hook: %w", err)
	}
	if err := hookutil.EnsureWorkspaceGitignore(filepath.Dir(mimoHookPath(cfg.WorkspacePath)), mimoHookFileName); err != nil {
		return fmt.Errorf("mimocode.GetAgentHooks: hook gitignore: %w", err)
	}
	if usesAOAgent(cfg.Config.Permissions, cfg.SystemPrompt, cfg.SystemPromptFile) {
		body, err := mimoAgentSource(cfg)
		if err != nil {
			return fmt.Errorf("mimocode.GetAgentHooks: agent: %w", err)
		}
		agentPath := mimoAgentPath(cfg.WorkspacePath, cfg.SessionID)
		if err := writeManagedFile(agentPath, mimoAgentSentinel, body); err != nil {
			return fmt.Errorf("mimocode.GetAgentHooks: agent: %w", err)
		}
		if err := hookutil.EnsureWorkspaceGitignore(filepath.Dir(agentPath), filepath.Base(agentPath)); err != nil {
			return fmt.Errorf("mimocode.GetAgentHooks: agent gitignore: %w", err)
		}
	}
	if err := installUsingAOSkill(cfg.WorkspacePath); err != nil {
		return fmt.Errorf("mimocode.GetAgentHooks: skill: %w", err)
	}
	return nil
}

// AreHooksInstalled reports whether AO's MiMo Code activity hook is installed.
func (p *Plugin) AreHooksInstalled(ctx context.Context, workspacePath string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if strings.TrimSpace(workspacePath) == "" {
		return false, errors.New("mimocode.AreHooksInstalled: workspacePath is required")
	}
	return isManagedFile(mimoHookPath(workspacePath), mimoHookSentinel)
}

// UninstallHooks removes only AO-owned MiMo Code workspace artifacts.
func (p *Plugin) UninstallHooks(ctx context.Context, workspacePath string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(workspacePath) == "" {
		return errors.New("mimocode.UninstallHooks: workspacePath is required")
	}
	if err := removeManagedFile(mimoHookPath(workspacePath), mimoHookSentinel); err != nil {
		return fmt.Errorf("mimocode.UninstallHooks: hook: %w", err)
	}
	agentsDir := filepath.Join(workspacePath, mimoConfigDirName, "agents")
	entries, err := os.ReadDir(agentsDir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("mimocode.UninstallHooks: read agents: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), "ao-") || filepath.Ext(entry.Name()) != ".md" {
			continue
		}
		if err := removeManagedFile(filepath.Join(agentsDir, entry.Name()), mimoAgentSentinel); err != nil {
			return fmt.Errorf("mimocode.UninstallHooks: agent: %w", err)
		}
	}
	if err := uninstallUsingAOSkill(workspacePath); err != nil {
		return fmt.Errorf("mimocode.UninstallHooks: skill: %w", err)
	}
	return nil
}

func mimoHookPath(workspace string) string {
	return filepath.Join(workspace, mimoConfigDirName, "hooks", mimoHookFileName)
}

func mimoAgentPath(workspace, sessionID string) string {
	return filepath.Join(workspace, mimoConfigDirName, "agents", mimoAgentName(sessionID)+".md")
}

func mimoAgentSource(cfg ports.WorkspaceHookConfig) ([]byte, error) {
	prompt := cfg.SystemPrompt
	if prompt == "" && cfg.SystemPromptFile != "" {
		data, err := os.ReadFile(cfg.SystemPromptFile) //nolint:gosec // AO-owned prompt path
		if err != nil {
			return nil, err
		}
		prompt = string(data)
	}
	var b strings.Builder
	b.WriteString("---\n# " + mimoAgentSentinel + "\ndescription: AO-managed session instructions\nmode: primary\n")
	if ports.NormalizePermissionMode(cfg.Config.Permissions) == ports.PermissionModeAcceptEdits {
		b.WriteString("permission:\n  edit: allow\n")
	}
	b.WriteString("---\n")
	b.WriteString(strings.TrimSpace(prompt))
	b.WriteByte('\n')
	return []byte(b.String()), nil
}

func writeManagedFile(path, sentinel string, body []byte) error {
	managed, err := isManagedFile(path, sentinel)
	if err != nil {
		return err
	}
	if _, err := os.Stat(path); err == nil && !managed {
		return fmt.Errorf("refusing to overwrite non-AO file at %s", path)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	return hookutil.AtomicWriteFile(path, body, 0o600)
}

func isManagedFile(path, sentinel string) (bool, error) {
	data, err := os.ReadFile(path) //nolint:gosec // workspace-owned path
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return strings.Contains(string(data), sentinel), nil
}

func removeManagedFile(path, sentinel string) error {
	managed, err := isManagedFile(path, sentinel)
	if err != nil || !managed {
		return err
	}
	return os.Remove(path)
}

func mimoSkillsDir(workspace string) string {
	return filepath.Join(workspace, mimoConfigDirName, "skills")
}

func installUsingAOSkill(workspace string) error {
	parent := mimoSkillsDir(workspace)
	skillDir := filepath.Join(parent, skillassets.SkillName)
	marker := filepath.Join(parent, mimoSkillMarkerFile)
	if info, err := os.Stat(skillDir); err == nil {
		if !info.IsDir() {
			return fmt.Errorf("refusing to overwrite non-directory at %s", skillDir)
		}
		managed, readErr := isManagedFile(marker, mimoSkillSentinel)
		if readErr != nil {
			return readErr
		}
		if !managed {
			return fmt.Errorf("refusing to overwrite non-AO skill at %s", skillDir)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(parent, 0o750); err != nil {
		return err
	}
	if err := hookutil.AtomicWriteFile(marker, []byte(mimoSkillSentinel+"\n"), 0o600); err != nil {
		return err
	}
	if err := skillassets.Materialize(skillDir); err != nil {
		return err
	}
	byDir := map[string][]string{}
	if err := filepath.WalkDir(skillDir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		dir := filepath.Dir(path)
		byDir[dir] = append(byDir[dir], entry.Name())
		return nil
	}); err != nil {
		return err
	}
	for dir, names := range byDir {
		if err := hookutil.EnsureWorkspaceGitignore(dir, names...); err != nil {
			return err
		}
	}
	return hookutil.EnsureWorkspaceGitignore(parent, mimoSkillMarkerFile)
}

func uninstallUsingAOSkill(workspace string) error {
	parent := mimoSkillsDir(workspace)
	marker := filepath.Join(parent, mimoSkillMarkerFile)
	managed, err := isManagedFile(marker, mimoSkillSentinel)
	if err != nil || !managed {
		return err
	}
	if err := os.RemoveAll(filepath.Join(parent, skillassets.SkillName)); err != nil {
		return err
	}
	return os.Remove(marker)
}
