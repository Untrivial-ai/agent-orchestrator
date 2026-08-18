package cline

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/hookutil"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// Cline's hook system is git-style: each lifecycle hook is an executable script
// placed in the workspace-local `.clinerules/hooks/` directory, named exactly
// after the hook event (no extension), reading a JSON payload on stdin and
// writing a JSON result on stdout (see docs.cline.bot hooks reference).
//
// AO installs one wrapper script per managed event. Each script forwards the
// hook payload to `ao hooks cline <subcommand>` and emits the no-op
// continuation result Cline expects. Scripts carry a marker line so install is
// idempotent and uninstall recognizes AO-owned scripts without an embedded
// template to diff against; user-authored hooks (lacking the marker) are never
// touched.
const (
	clineHooksDirName = ".clinerules"
	clineHooksSubDir  = "hooks"

	// clineHookCommandPrefix identifies the hook commands AO owns. The CLI hook
	// dispatcher routes "ao hooks cline <subcommand>" to DeriveActivityState.
	clineHookCommandPrefix = "ao hooks cline "

	// clineHookMarker tags AO-generated hook scripts so install/uninstall can
	// distinguish them from user-authored Cline hooks in the same directory.
	clineHookMarker = "# ao-managed-cline-hook"
)

// clineHookSpec describes one hook AO installs: the native Cline hook event
// (used as the script's filename) and the AO sub-command its wrapper forwards
// to (used by DeriveActivityState).
type clineHookSpec struct {
	// Event is the native Cline hook name, which is also the script filename.
	Event string
	// Subcommand is the fixed AO hook sub-command name the wrapper invokes.
	Subcommand string
}

// clineManagedHooks is the source of truth for the hooks AO installs. The
// native Cline events are mapped onto AO's fixed sub-command names so activity
// derivation stays uniform across adapters:
//   - TaskStart        -> session-start       (a new task begins: active)
//   - UserPromptSubmit -> user-prompt-submit  (user message submitted: active)
//   - PreToolUse       -> tool-use            (about to run a tool: active)
//   - TaskCancel       -> stop                (task cancelled/aborted: idle)
//
// PreToolUse fires before EVERY tool invocation, not just ones that gate on a
// user decision (and never at all under --auto-approve / --yolo, see
// appendApprovalFlags). It means "the agent is about to work", so it maps to
// tool-use (active). Routing it to permission-request pinned an actively-working
// session to the sticky waiting_input state, hiding its PR-derived status and
// firing spurious "needs input" notifications.
var clineManagedHooks = []clineHookSpec{
	{Event: "TaskStart", Subcommand: "session-start"},
	{Event: "UserPromptSubmit", Subcommand: "user-prompt-submit"},
	{Event: "PreToolUse", Subcommand: "tool-use"},
	{Event: "TaskCancel", Subcommand: "stop"},
}

// GetAgentHooks installs AO's Cline hook scripts into the worktree-local
// `.clinerules/hooks/` directory. Existing user-authored hook scripts are
// preserved, and re-running install simply rewrites AO-owned scripts in place.
func (p *Plugin) GetAgentHooks(ctx context.Context, cfg ports.WorkspaceHookConfig) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(cfg.WorkspacePath) == "" {
		return errors.New("cline.GetAgentHooks: WorkspacePath is required")
	}

	hooksDir := clineHooksDir(cfg.WorkspacePath)
	if err := os.MkdirAll(hooksDir, 0o750); err != nil {
		return fmt.Errorf("cline.GetAgentHooks: create hook dir: %w", err)
	}

	// Only scripts AO actually wrote go into the workspace .gitignore: a
	// user-authored script at one of these paths must keep counting as dirt so
	// workspace teardown preserves it.
	written := make([]string, 0, len(clineManagedHooks))
	for _, spec := range clineManagedHooks {
		scriptPath := filepath.Join(hooksDir, spec.Event)
		// Never clobber a user-authored hook with the same event name.
		if hookutil.FileExists(scriptPath) && !isManagedClineHook(scriptPath) {
			continue
		}
		script := renderClineHookScript(spec.Subcommand)
		if err := hookutil.AtomicWriteFile(scriptPath, []byte(script), 0o700); err != nil {
			return fmt.Errorf("cline.GetAgentHooks: write %s: %w", spec.Event, err)
		}
		written = append(written, spec.Event)
	}
	if err := hookutil.EnsureWorkspaceGitignore(hooksDir, written...); err != nil {
		return fmt.Errorf("cline.GetAgentHooks: gitignore: %w", err)
	}
	return nil
}

// UninstallHooks removes AO's Cline hook scripts from the workspace-local
// `.clinerules/hooks/` directory, leaving user-authored hooks untouched. A
// missing directory is a no-op.
func (p *Plugin) UninstallHooks(ctx context.Context, workspacePath string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(workspacePath) == "" {
		return errors.New("cline.UninstallHooks: workspacePath is required")
	}

	hooksDir := clineHooksDir(workspacePath)
	if _, err := os.Stat(hooksDir); errors.Is(err, os.ErrNotExist) {
		return nil
	}

	for _, spec := range clineManagedHooks {
		scriptPath := filepath.Join(hooksDir, spec.Event)
		if !hookutil.FileExists(scriptPath) || !isManagedClineHook(scriptPath) {
			continue
		}
		if err := os.Remove(scriptPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("cline.UninstallHooks: remove %s: %w", spec.Event, err)
		}
	}
	return nil
}

// AreHooksInstalled reports whether any AO Cline hook script is present in the
// workspace-local hooks directory. A missing directory means none.
func (p *Plugin) AreHooksInstalled(ctx context.Context, workspacePath string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if strings.TrimSpace(workspacePath) == "" {
		return false, errors.New("cline.AreHooksInstalled: workspacePath is required")
	}

	hooksDir := clineHooksDir(workspacePath)
	if _, err := os.Stat(hooksDir); errors.Is(err, os.ErrNotExist) {
		return false, nil
	}

	for _, spec := range clineManagedHooks {
		scriptPath := filepath.Join(hooksDir, spec.Event)
		if hookutil.FileExists(scriptPath) && isManagedClineHook(scriptPath) {
			return true, nil
		}
	}
	return false, nil
}

func clineHooksDir(workspacePath string) string {
	return filepath.Join(workspacePath, clineHooksDirName, clineHooksSubDir)
}

// renderClineHookScript builds an executable wrapper that forwards the Cline
// hook payload (JSON on stdin) to the AO CLI hook dispatcher and prints the
// no-op continuation result Cline expects ({"cancel": false}). The marker line
// identifies it as AO-owned.
func renderClineHookScript(subcommand string) string {
	var b strings.Builder
	b.WriteString("#!/usr/bin/env bash\n")
	b.WriteString(clineHookMarker + "\n")
	// Forward stdin to the AO dispatcher; ignore its exit code so a missing/old
	// `ao` binary can never block Cline's own execution.
	b.WriteString(clineHookCommandPrefix + subcommand + " || true\n")
	// Cline requires a JSON result on stdout; never block the agent.
	b.WriteString(`echo '{"cancel": false}'` + "\n")
	return b.String()
}

func isManagedClineHook(scriptPath string) bool {
	data, err := os.ReadFile(scriptPath) //nolint:gosec // path built from caller-owned workspace dir
	if err != nil {
		return false
	}
	return strings.Contains(string(data), clineHookMarker)
}
