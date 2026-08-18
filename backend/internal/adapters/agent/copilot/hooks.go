package copilot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/hookutil"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const (
	// copilotHooksDir is the repository-scope hooks directory Copilot CLI reads
	// (.github/hooks/*.json). AO writes a single dedicated file there so it never
	// disturbs other hook files the user or repo may ship.
	copilotHooksDir      = ".github/hooks"
	copilotHooksFileName = "ao.json"

	copilotAgentsDir     = ".github/agents"
	copilotAgentSentinel = "<!-- managed by agent-orchestrator: copilot agent profile -->"

	// copilotHooksVersion is the schema version of the hooks file (Copilot uses 1).
	copilotHooksVersion = 1

	// copilotHookCommandPrefix identifies the hook commands AO owns, so install
	// skips duplicates and uninstall recognizes AO entries by prefix without an
	// embedded template to diff against. The CLI dispatcher routes
	// `ao hooks copilot <event>` to DeriveActivityState.
	copilotHookCommandPrefix = "ao hooks copilot "
	copilotHookTimeoutSec    = 30
)

// copilotHookFile is the on-disk shape of .github/hooks/ao.json. AO owns this
// dedicated file outright, so it only models the keys it manages (version,
// disableAllHooks, hooks); user-defined hooks live in their own .github/hooks/*
// files and are never touched.
type copilotHookFile struct {
	Version         int                           `json:"version"`
	DisableAllHooks *bool                         `json:"disableAllHooks,omitempty"`
	Hooks           map[string][]copilotHookEntry `json:"hooks"`
}

// copilotHookEntry is one hook command. Copilot entries carry separate bash and
// powershell command strings (both required for cross-platform), a type, an
// optional working dir, and a timeout in seconds.
type copilotHookEntry struct {
	Type       string `json:"type"`
	Bash       string `json:"bash,omitempty"`
	Powershell string `json:"powershell,omitempty"`
	Cwd        string `json:"cwd,omitempty"`
	TimeoutSec int    `json:"timeoutSec,omitempty"`
}

// copilotHookSpec describes one hook AO installs, defined in code rather than
// read from an embedded settings file.
type copilotHookSpec struct {
	// Event is the native Copilot camelCase event name (sessionStart, ...).
	Event string
	// Command is the AO sub-command suffix (session-start, ...). It is appended
	// to copilotHookCommandPrefix to form both the bash and powershell command,
	// and is the value DeriveActivityState switches on.
	Command string
}

// copilotManagedHooks is the source of truth for the hooks AO installs. The AO
// sub-command names (session-start, user-prompt-submit, tool-use, stop) are
// exactly what StandardDeriveActivityState switches on.
//
// Native event names use Copilot's camelCase form, taken verbatim from
// https://docs.github.com/en/copilot/how-tos/copilot-cli/customize-copilot/use-hooks
// (sessionStart, sessionEnd, userPromptSubmitted, preToolUse, postToolUse,
// errorOccurred, agentStop). Copilot does not document a "permissionRequest"
// event. preToolUse fires before EVERY tool invocation — not just ones that
// prompt the user for approval, and never at all when Copilot runs with
// --allow-all-tools / --allow-all (see appendApprovalFlags), where it auto-runs
// every tool. Routing it to permission-request therefore pinned an
// actively-working session to the sticky waiting_input state, masking the
// session's PR-derived status and firing spurious "needs input" notifications.
// preToolUse means the agent is about to work, so it maps to tool-use (active).
// agentStop is the per-turn completion signal and maps to "stop" (turn → idle).
var copilotManagedHooks = []copilotHookSpec{
	{Event: "sessionStart", Command: "session-start"},
	{Event: "userPromptSubmitted", Command: "user-prompt-submit"},
	{Event: "preToolUse", Command: "tool-use"},
	{Event: "agentStop", Command: "stop"},
}

// InstallAgentProfile installs only AO's per-session Copilot custom-agent
// profile. Reviewers use this directly because their lifecycle is owned by the
// review launcher, not the worker activity hooks.
func (p *Plugin) InstallAgentProfile(ctx context.Context, cfg ports.WorkspaceHookConfig) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(cfg.WorkspacePath) == "" {
		return errors.New("copilot.InstallAgentProfile: WorkspacePath is required")
	}
	if err := installCopilotAgent(cfg.WorkspacePath, cfg.SessionID, cfg.SystemPrompt, cfg.SystemPromptFile); err != nil {
		return fmt.Errorf("copilot.InstallAgentProfile: %w", err)
	}
	return nil
}

// GetAgentHooks installs AO's Copilot workspace integration:
//   - .github/agents/ao-<session>.agent.md for an explicit per-session role.
//   - .github/hooks/ao.json for normalized activity-state signals.
//
// The launch command selects that profile with --agent=ao-<session>. Avoid
// writing a repository-root AGENTS.md here so AO does not compete with
// project-owned instructions.
func (p *Plugin) GetAgentHooks(ctx context.Context, cfg ports.WorkspaceHookConfig) error {
	if err := p.InstallAgentProfile(ctx, cfg); err != nil {
		return fmt.Errorf("copilot.GetAgentHooks: %w", err)
	}
	if err := installCopilotHooks(cfg.WorkspacePath); err != nil {
		return fmt.Errorf("copilot.GetAgentHooks: %w", err)
	}
	return nil
}

func installCopilotHooks(workspacePath string) error {
	hooksPath := copilotHooksPath(workspacePath)
	file, err := readCopilotHooks(hooksPath)
	if err != nil {
		return err
	}

	if file.Hooks == nil {
		file.Hooks = map[string][]copilotHookEntry{}
	}
	for _, spec := range copilotManagedHooks {
		command := copilotHookCommandPrefix + spec.Command
		if copilotHookCommandExists(file.Hooks[spec.Event], command) {
			continue
		}
		file.Hooks[spec.Event] = append(file.Hooks[spec.Event], copilotHookEntry{
			Type:       "command",
			Bash:       command,
			Powershell: command,
			TimeoutSec: copilotHookTimeoutSec,
		})
	}

	if err := writeCopilotHooks(hooksPath, file); err != nil {
		return err
	}
	if err := hookutil.EnsureWorkspaceGitignore(filepath.Dir(hooksPath), copilotHooksFileName); err != nil {
		return fmt.Errorf("gitignore: %w", err)
	}
	return nil
}

func installCopilotAgent(workspacePath, sessionID, inlinePrompt, promptFile string) error {
	systemPrompt, err := copilotSystemPromptText(inlinePrompt, promptFile)
	if err != nil {
		return err
	}
	agentName := copilotAgentName(sessionID, inlinePrompt, promptFile)
	if systemPrompt == "" || agentName == "" {
		return nil
	}
	agentPath := filepath.Join(workspacePath, copilotAgentsDir, agentName+".agent.md")
	existing, err := os.ReadFile(agentPath) //nolint:gosec // path built from caller-owned workspace dir
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read %s: %w", agentPath, err)
	}
	if err == nil && !strings.Contains(string(existing), copilotAgentSentinel) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(agentPath), 0o750); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(agentPath), err)
	}
	body := copilotAgentProfile(agentName, sessionID, systemPrompt)
	if err := hookutil.AtomicWriteFile(agentPath, []byte(body), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", agentPath, err)
	}
	if err := ignoreCopilotPath(workspacePath, "/"+filepath.ToSlash(filepath.Join(copilotAgentsDir, agentName+".agent.md"))); err != nil {
		return fmt.Errorf("git exclude: %w", err)
	}
	return nil
}

func copilotAgentProfile(agentName, sessionID, systemPrompt string) string {
	return "---\n" +
		"name: " + agentName + "\n" +
		"description: Agent Orchestrator role profile for AO session " + strings.TrimSpace(sessionID) + ". Use for all work in this session.\n" +
		"target: github-copilot\n" +
		"---\n\n" +
		copilotAgentSentinel + "\n\n" +
		strings.TrimRight(systemPrompt, "\n") + "\n"
}

func ignoreCopilotPath(workspacePath, pattern string) error {
	gitDir, err := workspaceGitCommonDir(workspacePath)
	if err != nil {
		return err
	}
	if strings.TrimSpace(gitDir) == "" {
		return nil
	}
	excludePath := filepath.Join(gitDir, "info", "exclude")
	data, err := os.ReadFile(excludePath) //nolint:gosec // path derived from the workspace .git metadata
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read %s: %w", excludePath, err)
	}
	pattern = strings.TrimSpace(pattern)
	if pattern == "" || strings.Contains(string(data), pattern) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(excludePath), 0o750); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(excludePath), err)
	}
	body := strings.TrimRight(string(data), "\n")
	if body != "" {
		body += "\n"
	}
	body += "# agent-orchestrator Copilot session files\n" + pattern + "\n"
	if err := hookutil.AtomicWriteFile(excludePath, []byte(body), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", excludePath, err)
	}
	return nil
}

func workspaceGitCommonDir(workspacePath string) (string, error) {
	gitPath := filepath.Join(workspacePath, ".git")
	info, err := os.Stat(gitPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", fmt.Errorf("stat %s: %w", gitPath, err)
	}
	if info.IsDir() {
		return gitCommonDir(gitPath)
	}
	data, err := os.ReadFile(gitPath) //nolint:gosec // path built from caller-owned workspace dir
	if err != nil {
		return "", fmt.Errorf("read %s: %w", gitPath, err)
	}
	text := strings.TrimSpace(string(data))
	const prefix = "gitdir:"
	if !strings.HasPrefix(text, prefix) {
		return "", nil
	}
	dir := strings.TrimSpace(strings.TrimPrefix(text, prefix))
	if dir == "" {
		return "", nil
	}
	if filepath.IsAbs(dir) {
		return gitCommonDir(dir)
	}
	return gitCommonDir(filepath.Clean(filepath.Join(workspacePath, dir)))
}

func gitCommonDir(gitDir string) (string, error) {
	commonPath := filepath.Join(gitDir, "commondir")
	data, err := os.ReadFile(commonPath) //nolint:gosec // path derived from the workspace .git metadata
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return gitDir, nil
		}
		return "", fmt.Errorf("read %s: %w", commonPath, err)
	}
	dir := strings.TrimSpace(string(data))
	if dir == "" {
		return gitDir, nil
	}
	if filepath.IsAbs(dir) {
		return filepath.Clean(dir), nil
	}
	return filepath.Clean(filepath.Join(gitDir, dir)), nil
}

// UninstallHooks removes AO's Copilot hooks from the workspace-local
// .github/hooks/ao.json file, leaving user-defined hooks and unrelated keys
// untouched. A missing file is a no-op.
func (p *Plugin) UninstallHooks(ctx context.Context, workspacePath string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(workspacePath) == "" {
		return errors.New("copilot.UninstallHooks: workspacePath is required")
	}

	hooksPath := copilotHooksPath(workspacePath)
	if _, err := os.Stat(hooksPath); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	file, err := readCopilotHooks(hooksPath)
	if err != nil {
		return fmt.Errorf("copilot.UninstallHooks: %w", err)
	}

	for event, entries := range file.Hooks {
		kept := removeCopilotManagedHooks(entries)
		if len(kept) == 0 {
			delete(file.Hooks, event)
			continue
		}
		file.Hooks[event] = kept
	}

	if err := writeCopilotHooks(hooksPath, file); err != nil {
		return fmt.Errorf("copilot.UninstallHooks: %w", err)
	}
	return nil
}

// AreHooksInstalled reports whether any AO Copilot hook is present in the
// workspace-local hooks file. A missing file means none are installed.
func (p *Plugin) AreHooksInstalled(ctx context.Context, workspacePath string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if strings.TrimSpace(workspacePath) == "" {
		return false, errors.New("copilot.AreHooksInstalled: workspacePath is required")
	}

	hooksPath := copilotHooksPath(workspacePath)
	if _, err := os.Stat(hooksPath); errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	file, err := readCopilotHooks(hooksPath)
	if err != nil {
		return false, fmt.Errorf("copilot.AreHooksInstalled: %w", err)
	}

	for _, entries := range file.Hooks {
		for _, entry := range entries {
			if isCopilotManagedHook(entry) {
				return true, nil
			}
		}
	}
	return false, nil
}

func copilotHooksPath(workspacePath string) string {
	return filepath.Join(workspacePath, filepath.FromSlash(copilotHooksDir), copilotHooksFileName)
}

// readCopilotHooks loads the hooks file. A missing or empty file yields an empty
// file struct with a nil hooks map (and the AO schema version, used on write).
func readCopilotHooks(hooksPath string) (copilotHookFile, error) {
	file := copilotHookFile{Version: copilotHooksVersion}

	data, err := os.ReadFile(hooksPath) //nolint:gosec // path built from caller-owned workspace dir
	if errors.Is(err, os.ErrNotExist) {
		return file, nil
	}
	if err != nil {
		return copilotHookFile{}, fmt.Errorf("read %s: %w", hooksPath, err)
	}
	if strings.TrimSpace(string(data)) == "" {
		return file, nil
	}
	if err := json.Unmarshal(data, &file); err != nil {
		return copilotHookFile{}, fmt.Errorf("parse %s: %w", hooksPath, err)
	}
	if file.Version == 0 {
		file.Version = copilotHooksVersion
	}
	return file, nil
}

// writeCopilotHooks writes the file. An empty hooks map still writes a valid
// (versioned) file so AreHooksInstalled and re-install see a consistent shape.
func writeCopilotHooks(hooksPath string, file copilotHookFile) error {
	if file.Version == 0 {
		file.Version = copilotHooksVersion
	}
	if file.Hooks == nil {
		file.Hooks = map[string][]copilotHookEntry{}
	}

	if err := os.MkdirAll(filepath.Dir(hooksPath), 0o750); err != nil {
		return fmt.Errorf("create hooks dir: %w", err)
	}
	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return fmt.Errorf("encode %s: %w", hooksPath, err)
	}
	data = append(data, '\n')
	if err := hookutil.AtomicWriteFile(hooksPath, data, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", hooksPath, err)
	}
	return nil
}

// isCopilotManagedHook reports whether an entry is one AO owns, recognized by the
// command prefix on either the bash or powershell command.
func isCopilotManagedHook(entry copilotHookEntry) bool {
	return strings.HasPrefix(entry.Bash, copilotHookCommandPrefix) ||
		strings.HasPrefix(entry.Powershell, copilotHookCommandPrefix)
}

func copilotHookCommandExists(entries []copilotHookEntry, command string) bool {
	for _, entry := range entries {
		if entry.Bash == command || entry.Powershell == command {
			return true
		}
	}
	return false
}

// removeCopilotManagedHooks strips AO hook entries from a slice, preserving
// user-defined entries in order.
func removeCopilotManagedHooks(entries []copilotHookEntry) []copilotHookEntry {
	kept := make([]copilotHookEntry, 0, len(entries))
	for _, entry := range entries {
		if !isCopilotManagedHook(entry) {
			kept = append(kept, entry)
		}
	}
	return kept
}
