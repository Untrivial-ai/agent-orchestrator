package opencode

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
	// opencode scans both `.opencode/plugin/` and `.opencode/plugins/` for
	// `*.js`/`*.ts` files (see opencode's ConfigPlugin glob
	// "{plugin,plugins}/*.{ts,js}"). AO writes the plural `plugins/`, matching
	// the directory the upstream opencode tooling (and the entire-cli reference
	// integration) uses.
	opencodePluginDirName = ".opencode"
	opencodePluginSubDir  = "plugins"

	// opencodePluginFileName is the AO-owned plugin file. AO fully owns this
	// filename: install overwrites it and uninstall deletes it (guarded by the
	// sentinel), so user-authored plugins in other files are never touched.
	// It is TypeScript (opencode runs on Bun); the file's only import is a
	// type-only import, which Bun erases at runtime.
	opencodePluginFileName = "ao-activity.ts"

	// opencodePluginSentinel marks the file as AO-managed. AreHooksInstalled and
	// UninstallHooks key off it so AO never deletes a user file that happens to
	// share the name. It must appear verbatim in the embedded plugin source.
	opencodePluginSentinel = "agent-orchestrator: managed opencode activity plugin"

	// opencodeHookCommandPrefix identifies the hook commands AO owns. The
	// embedded plugin shells `ao hooks opencode <event>`; this prefix is the
	// shared contract with the (forthcoming) `ao hooks` CLI and is asserted by
	// tests so the plugin can't silently drift away from it.
	opencodeHookCommandPrefix = "ao hooks opencode "

	// opencodeSkillSubDir is where opencode discovers project skills
	// (`.opencode/skills/<name>/SKILL.md`). AO materializes the using-ao skill
	// here so opencode's native `skill` tool can see it — the data-dir install
	// alone is invisible to that discovery path.
	opencodeSkillSubDir = "skills"

	// opencodeSkillMarkerFile lives beside the skill directory (not inside it) so
	// Materialize's RemoveAll of using-ao/ cannot erase ownership mid-install.
	// Install overwrites and uninstall deletes only when this marker is present.
	opencodeSkillMarkerFile = ".using-ao.ao-managed"

	// opencodeSkillSentinel is written into the marker file. Keep it distinct
	// from the plugin sentinel so ownership checks stay file-specific.
	opencodeSkillSentinel = "agent-orchestrator: managed opencode using-ao skill"
)

// opencodePluginSource is the AO-managed opencode plugin, embedded so it ships
// inside the binary and is written verbatim into a session's worktree on hook
// install. It is a real, lintable source file under assets/ rather than a Go
// string literal because it is opencode plugin source code, not a data
// structure AO assembles (the way it builds Codex/Claude hook JSON).
//
//go:embed assets/ao-activity.ts
var opencodePluginSource string

// opencodeManagedEvents are the three normalized activity events the embedded
// plugin reports. They are defined here (not parsed from the file) so tests can
// assert the plugin wires every one via the `ao hooks opencode <event>` command.
var opencodeManagedEvents = []string{"session-start", "user-prompt-submit", "active", "stop", "permission-blocked"}

// GetAgentHooks installs AO's opencode activity plugin into the worktree-local
// .opencode/plugins/ directory, and materializes the using-ao skill into
// .opencode/skills/using-ao/ so opencode's native `skill` tool can discover it.
// Unlike Claude Code and Codex, opencode has no native command-hook config to
// merge into; its only lifecycle-extensibility surface is a JS/TS plugin. AO
// therefore writes a dedicated, AO-owned plugin file. The write is atomic and
// idempotent: re-installing overwrites AO's own file with identical content. It
// refuses to overwrite a file that is NOT AO-managed (no sentinel), so a user
// plugin that happens to occupy our path is never silently destroyed — install
// fails loudly instead. The skill install uses the same ownership guard via a
// marker file beside the skill directory (written before Materialize runs).
func (p *Plugin) GetAgentHooks(ctx context.Context, cfg ports.WorkspaceHookConfig) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(cfg.WorkspacePath) == "" {
		return errors.New("opencode.GetAgentHooks: WorkspacePath is required")
	}

	pluginPath := opencodePluginPath(cfg.WorkspacePath)
	// Guard against clobbering a user file at our path: overwrite only when the
	// target is absent or already AO-managed. A foreign file is a loud error,
	// not silent data loss (uninstall is sentinel-guarded the same way).
	if _, err := os.Stat(pluginPath); err == nil {
		managed, err := isAOManagedPlugin(pluginPath)
		if err != nil {
			return fmt.Errorf("opencode.GetAgentHooks: %w", err)
		}
		if !managed {
			return fmt.Errorf("opencode.GetAgentHooks: refusing to overwrite non-AO file at %s — move it so AO can install its plugin", pluginPath)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("opencode.GetAgentHooks: stat plugin: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(pluginPath), 0o750); err != nil {
		return fmt.Errorf("opencode.GetAgentHooks: create plugin dir: %w", err)
	}
	if err := hookutil.AtomicWriteFile(pluginPath, []byte(opencodePluginSource), 0o600); err != nil {
		return fmt.Errorf("opencode.GetAgentHooks: write plugin: %w", err)
	}
	if err := hookutil.EnsureWorkspaceGitignore(filepath.Dir(pluginPath), opencodePluginFileName); err != nil {
		return fmt.Errorf("opencode.GetAgentHooks: gitignore: %w", err)
	}
	if err := installUsingAOSkill(cfg.WorkspacePath); err != nil {
		return fmt.Errorf("opencode.GetAgentHooks: %w", err)
	}
	return nil
}

// UninstallHooks removes AO's opencode plugin and the AO-managed using-ao skill
// from the workspace-local .opencode/ tree. It deletes the plugin only when it
// carries the AO sentinel, and the skill directory only when the AO marker is
// present, so user files that happen to share those paths are left in place. A
// missing file is a no-op.
func (p *Plugin) UninstallHooks(ctx context.Context, workspacePath string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(workspacePath) == "" {
		return errors.New("opencode.UninstallHooks: workspacePath is required")
	}

	pluginPath := opencodePluginPath(workspacePath)
	managed, err := isAOManagedPlugin(pluginPath)
	if err != nil {
		return fmt.Errorf("opencode.UninstallHooks: %w", err)
	}
	if managed {
		if err := os.Remove(pluginPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("opencode.UninstallHooks: remove plugin: %w", err)
		}
	}
	if err := uninstallUsingAOSkill(workspacePath); err != nil {
		return fmt.Errorf("opencode.UninstallHooks: %w", err)
	}
	return nil
}

// AreHooksInstalled reports whether AO's opencode plugin is present in the
// workspace-local plugin dir. A missing file, or a same-named file without the
// AO sentinel, means none are installed.
func (p *Plugin) AreHooksInstalled(ctx context.Context, workspacePath string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if strings.TrimSpace(workspacePath) == "" {
		return false, errors.New("opencode.AreHooksInstalled: workspacePath is required")
	}
	managed, err := isAOManagedPlugin(opencodePluginPath(workspacePath))
	if err != nil {
		return false, fmt.Errorf("opencode.AreHooksInstalled: %w", err)
	}
	return managed, nil
}

func opencodePluginPath(workspacePath string) string {
	return filepath.Join(workspacePath, opencodePluginDirName, opencodePluginSubDir, opencodePluginFileName)
}

func opencodeSkillDir(workspacePath string) string {
	return filepath.Join(workspacePath, opencodePluginDirName, opencodeSkillSubDir, skillassets.SkillName)
}

func opencodeSkillsDir(workspacePath string) string {
	return filepath.Join(workspacePath, opencodePluginDirName, opencodeSkillSubDir)
}

func opencodeSkillMarkerPath(workspacePath string) string {
	return filepath.Join(opencodeSkillsDir(workspacePath), opencodeSkillMarkerFile)
}

// installUsingAOSkill materializes the embedded using-ao skill into
// .opencode/skills/using-ao/ so opencode's skill tool can discover it. It
// refuses to overwrite a same-named directory that is not AO-managed.
func installUsingAOSkill(workspacePath string) error {
	skillDir := opencodeSkillDir(workspacePath)
	if info, err := os.Stat(skillDir); err == nil {
		if !info.IsDir() {
			return fmt.Errorf("refusing to overwrite non-directory at %s — move it so AO can install using-ao", skillDir)
		}
		managed, err := isAOManagedSkill(workspacePath)
		if err != nil {
			return err
		}
		if !managed {
			return fmt.Errorf("refusing to overwrite non-AO skill at %s — move it so AO can install using-ao", skillDir)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("stat skill dir: %w", err)
	}

	skillsParent := opencodeSkillsDir(workspacePath)
	if err := os.MkdirAll(skillsParent, 0o750); err != nil {
		return fmt.Errorf("create skills dir: %w", err)
	}
	// Write ownership before Materialize clobbers using-ao/, so a crash mid-tree
	// write leaves a marker that allows the next install attempt to recover.
	if err := hookutil.AtomicWriteFile(opencodeSkillMarkerPath(workspacePath), []byte(opencodeSkillSentinel+"\n"), 0o600); err != nil {
		return fmt.Errorf("write skill marker: %w", err)
	}
	if err := skillassets.Materialize(skillDir); err != nil {
		return fmt.Errorf("materialize using-ao skill: %w", err)
	}
	if err := ensureSkillTreeGitignored(skillDir); err != nil {
		return fmt.Errorf("skill gitignore: %w", err)
	}
	if err := hookutil.EnsureWorkspaceGitignore(skillsParent, opencodeSkillMarkerFile); err != nil {
		return fmt.Errorf("skill marker gitignore: %w", err)
	}
	return nil
}

// ensureSkillTreeGitignored writes AO-managed .gitignore files beside every
// file in the skill tree so registry's hook-footprint contract holds: each
// installed path must be ignorable for git worktree teardown.
func ensureSkillTreeGitignored(skillRoot string) error {
	byDir := map[string][]string{}
	err := filepath.WalkDir(skillRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		dir := filepath.Dir(path)
		byDir[dir] = append(byDir[dir], filepath.Base(path))
		return nil
	})
	if err != nil {
		return fmt.Errorf("walk skill tree: %w", err)
	}
	for dir, names := range byDir {
		if err := hookutil.EnsureWorkspaceGitignore(dir, names...); err != nil {
			return err
		}
	}
	return nil
}

// uninstallUsingAOSkill removes the AO-managed using-ao skill directory. A
// missing directory, or a same-named directory without the AO marker, is a no-op.
func uninstallUsingAOSkill(workspacePath string) error {
	managed, err := isAOManagedSkill(workspacePath)
	if err != nil {
		return err
	}
	if !managed {
		return nil
	}
	if err := os.RemoveAll(opencodeSkillDir(workspacePath)); err != nil {
		return fmt.Errorf("remove skill dir: %w", err)
	}
	markerPath := opencodeSkillMarkerPath(workspacePath)
	if err := os.Remove(markerPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove skill marker: %w", err)
	}
	return nil
}

// isAOManagedPlugin reports whether the file at path exists and carries the AO
// sentinel. A missing file yields (false, nil).
func isAOManagedPlugin(path string) (bool, error) {
	data, err := os.ReadFile(path) //nolint:gosec // path built from caller-owned workspace dir
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read %s: %w", path, err)
	}
	return strings.Contains(string(data), opencodePluginSentinel), nil
}

// isAOManagedSkill reports whether the AO ownership marker beside the skill
// directory exists. A missing marker yields (false, nil).
func isAOManagedSkill(workspacePath string) (bool, error) {
	data, err := os.ReadFile(opencodeSkillMarkerPath(workspacePath)) //nolint:gosec // path built from caller-owned workspace dir
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read skill marker: %w", err)
	}
	return strings.Contains(string(data), opencodeSkillSentinel), nil
}
