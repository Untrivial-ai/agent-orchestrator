package auggie

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/hooksjson"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/hookutil"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const (
	auggieSettingsDirName  = ".augment"
	auggieSettingsFileName = "settings.local.json"
	auggieHooksDirName     = "ao-hooks"
	auggieHookSentinel     = "agent-orchestrator: managed auggie activity hook"
	auggieHookTimeoutMS    = 5000
)

type auggieHookSpec struct {
	nativeEvent string
	aoEvent     string
}

var auggieManagedHookSpecs = []auggieHookSpec{
	{nativeEvent: "SessionStart", aoEvent: "session-start"},
	{nativeEvent: "PreToolUse", aoEvent: "pre-tool-use"},
	{nativeEvent: "PostToolUse", aoEvent: "post-tool-use"},
	{nativeEvent: "Stop", aoEvent: "stop"},
	{nativeEvent: "SessionEnd", aoEvent: "session-end"},
}

func auggieSettingsPath(workspacePath string) string {
	return filepath.Join(workspacePath, auggieSettingsDirName, auggieSettingsFileName)
}

func auggieHookDir(workspacePath string) string {
	return filepath.Join(workspacePath, auggieSettingsDirName, auggieHooksDirName)
}

func auggieHookScriptName(event string) string {
	ext := ".sh"
	if runtime.GOOS == "windows" {
		ext = ".cmd"
	}
	return "ao-" + event + ext
}

func auggieHookScriptPath(workspacePath, event string) string {
	return filepath.Join(auggieHookDir(workspacePath), auggieHookScriptName(event))
}

func auggieHooksManager(workspacePath string) hooksjson.Manager {
	managed := make([]hooksjson.HookSpec, 0, len(auggieManagedHookSpecs))
	for _, spec := range auggieManagedHookSpecs {
		managed = append(managed, hooksjson.HookSpec{
			Event:   spec.nativeEvent,
			Command: auggieHookScriptPath(workspacePath, spec.aoEvent),
		})
	}
	return hooksjson.Manager{
		Label:         adapterID,
		CommandPrefix: auggieHookDir(workspacePath) + string(filepath.Separator),
		Timeout:       auggieHookTimeoutMS,
		Path:          auggieSettingsPath,
		Managed:       managed,
	}
}

// GetAgentHooks installs executable callbacks and reconciles them into
// .augment/settings.local.json. Auggie requires command hooks to reference a
// supported script file rather than an arbitrary shell command.
func (p *Plugin) GetAgentHooks(ctx context.Context, cfg ports.WorkspaceHookConfig) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	workspacePath := strings.TrimSpace(cfg.WorkspacePath)
	if workspacePath == "" {
		return errors.New("auggie.GetAgentHooks: WorkspacePath is required")
	}
	absoluteWorkspace, err := filepath.Abs(workspacePath)
	if err != nil {
		return fmt.Errorf("auggie.GetAgentHooks: resolve workspace path: %w", err)
	}
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("auggie.GetAgentHooks: resolve AO executable: %w", err)
	}
	if !filepath.IsAbs(executable) {
		executable, err = filepath.Abs(executable)
		if err != nil {
			return fmt.Errorf("auggie.GetAgentHooks: resolve AO executable path: %w", err)
		}
	}

	if err := installAuggieHookScripts(absoluteWorkspace, executable); err != nil {
		return fmt.Errorf("auggie.GetAgentHooks: %w", err)
	}
	if err := auggieHooksManager(absoluteWorkspace).Install(ctx, absoluteWorkspace); err != nil {
		return err
	}
	return nil
}

func installAuggieHookScripts(workspacePath, executable string) error {
	dir := auggieHookDir(workspacePath)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("create hook directory: %w", err)
	}
	names := make([]string, 0, len(auggieManagedHookSpecs))
	for _, spec := range auggieManagedHookSpecs {
		name := auggieHookScriptName(spec.aoEvent)
		path := filepath.Join(dir, name)
		if data, err := os.ReadFile(path); err == nil { //nolint:gosec // AO-owned workspace path
			if !strings.Contains(string(data), auggieHookSentinel) {
				return fmt.Errorf("refusing to overwrite non-AO file at %s", path)
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("read hook script: %w", err)
		}
		if err := hookutil.AtomicWriteFile(path, []byte(auggieHookScriptSource(executable, spec.aoEvent)), 0o700); err != nil {
			return fmt.Errorf("write %s hook: %w", spec.aoEvent, err)
		}
		names = append(names, name)
	}
	if err := hookutil.EnsureWorkspaceGitignore(dir, names...); err != nil {
		return fmt.Errorf("gitignore hook scripts: %w", err)
	}
	return nil
}

func auggieHookScriptSource(executable, event string) string {
	if runtime.GOOS == "windows" {
		quoted := strings.ReplaceAll(executable, `"`, `""`)
		return "@echo off\r\nrem " + auggieHookSentinel + "\r\n\"" + quoted + "\" hooks auggie " + event + "\r\nexit /b 0\r\n"
	}
	quoted := "'" + strings.ReplaceAll(executable, "'", `'"'"'`) + "'"
	return "#!/bin/sh\n# " + auggieHookSentinel + "\n" + quoted + " hooks auggie " + event + " || true\nexit 0\n"
}
