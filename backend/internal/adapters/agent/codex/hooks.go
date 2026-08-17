package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/hookutil"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/pkg/agentruntime"
)

// Codex (0.136+) never loads hook config from AO's per-session worktrees, so
// AO's hooks ride the launch command as `-c` session-flag config instead of
// workspace files:
//
//   - Project-local `.codex/` layers only load when the directory is trusted,
//     and for linked git worktrees Codex sources hook declarations from the
//     matching `.codex/` folder in the ROOT checkout, not the worktree. A
//     hooks.json written into an AO worktree is therefore dead config.
//   - Hooks passed as `-c 'hooks.<Event>=[...]'` land in Codex's session-flags
//     config layer, which is not trust-gated and aggregates with (never
//     replaces) the user's own hooks from `~/.codex`. They carry no persisted
//     trust hash, so the launch command also passes
//     `--dangerously-bypass-hook-trust` to let them run.
const (
	codexHooksDirName  = ".codex"
	codexHooksFileName = "hooks.json"

	// codexHookCommandPrefix identifies the hook commands AO owns, so the
	// legacy-file cleanup and uninstall recognize AO entries by prefix
	// without an embedded template to diff against.
	codexHookCommandPrefix = "ao hooks codex "
	// codexHookTimeout caps how long Codex waits on one AO hook callback. The
	// callback is a loopback POST that normally returns in milliseconds; a
	// tight cap keeps a hung daemon from stalling the agent's turn.
	codexHookTimeout = 5
)

// codexHookFile is the on-disk shape of .codex/hooks.json. It is used by tests
// to decode the written file.
type codexHookFile struct {
	Hooks map[string][]codexMatcherGroup `json:"hooks"`
}

type codexMatcherGroup struct {
	Matcher *string          `json:"matcher,omitempty"`
	Hooks   []codexHookEntry `json:"hooks"`
}

type codexHookEntry struct {
	Type    string `json:"type"`
	Command string `json:"command"`
	Timeout int    `json:"timeout,omitempty"`
}

// codexHookSpec describes one hook AO delivers via launch-command config.
type codexHookSpec struct {
	Event   string
	Command string
}

// codexManagedHooks is the source of truth for the hooks AO delivers. Event
// names must not contain dots: they are spliced into a dotted `-c` key path,
// and Codex splits that path on every dot without honoring quoting.
var codexManagedHooks = []codexHookSpec{
	{Event: "SessionStart", Command: codexHookCommandPrefix + "session-start"},
	{Event: "UserPromptSubmit", Command: codexHookCommandPrefix + "user-prompt-submit"},
	{Event: "PermissionRequest", Command: codexHookCommandPrefix + "permission-request"},
	{Event: "Stop", Command: codexHookCommandPrefix + "stop"},
}

// appendSessionHookFlags adds AO's activity hooks to the argv as `-c`
// session-flag config, one flag per managed event. Codex executes command
// hooks through a login shell, which may replace PATH, so every hook invokes
// this exact AO executable instead of relying on a bare `ao` lookup.
func appendSessionHookFlags(cmd *[]string) error {
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve AO hook executable: %w", err)
	}
	if !filepath.IsAbs(executable) {
		executable, err = filepath.Abs(executable)
		if err != nil {
			return fmt.Errorf("make AO hook executable absolute: %w", err)
		}
	}
	appendSessionHookFlagsForExecutable(cmd, executable)
	return nil
}

func appendSessionHookFlagsForExecutable(cmd *[]string, executable string) {
	prefix := shellQuoteHookExecutable(executable) + " hooks codex "
	for _, spec := range codexManagedHooks {
		action := strings.TrimPrefix(spec.Command, codexHookCommandPrefix)
		flag := fmt.Sprintf(`hooks.%s=[{hooks=[{type="command",command=%s,timeout=%d}]}]`,
			spec.Event, codexTOMLBasicString(prefix+action), codexHookTimeout)
		*cmd = append(*cmd, "-c", flag)
	}
}

func shellQuoteHookExecutable(executable string) string {
	if runtime.GOOS == "windows" {
		return `"` + executable + `"`
	}
	return `'` + strings.ReplaceAll(executable, `'`, `'"'"'`) + `'`
}

// appendWorkspaceTrustFlag marks the session's worktree as a trusted Codex
// project for this invocation only, so spawns into never-before-trusted repos
// don't hang on the interactive "Do you trust this directory?" prompt.
//
// The override is shaped as a single `projects={...}` value (not a dotted
// `projects."<path>".trust_level` key) because Codex splits `-c` key paths on
// every dot without honoring quoted segments, which corrupts path keys. The
// inline table deep-merges with the user's persisted projects map. Both the
// literal and symlink-resolved paths are trusted because Codex looks trust up
// by the canonicalized cwd first and the literal path second (on macOS the two
// commonly differ, e.g. /tmp vs /private/tmp).
func appendWorkspaceTrustFlag(cmd *[]string, workspacePath string) {
	*cmd = append(*cmd, agentruntime.CodexWorkspaceTrustArgs(workspacePath)...)
}

func codexTOMLConfigString(s string) string {
	if !containsTOMLControl(s) && !strings.Contains(s, "'") {
		return codexTOMLLiteralString(s)
	}
	return codexTOMLBasicString(s)
}

func codexTOMLLiteralString(s string) string {
	return "'" + s + "'"
}

// codexTOMLBasicString renders s as a TOML basic string, escaping backslashes
// and quotes (Windows paths) plus control characters so the value survives
// Codex's TOML parse of the `-c` override.
func codexTOMLBasicString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '\\':
			b.WriteString(`\\`)
		case r == '"':
			b.WriteString(`\"`)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&b, `\u%04X`, r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

func containsTOMLControl(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}

// GetAgentHooks installs no active workspace files — Codex never loads them
// from AO's worktrees (see the package comment above); activity hooks ride the
// launch command instead. It strips hook entries that older AO versions wrote
// into the worktree-local .codex/hooks.json so reused or restored worktrees
// don't keep dead AO config, preserving user-defined hooks.
func (p *Plugin) GetAgentHooks(ctx context.Context, cfg ports.WorkspaceHookConfig) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(cfg.WorkspacePath) == "" {
		return errors.New("codex.GetAgentHooks: WorkspacePath is required")
	}
	if err := removeLegacyWorkspaceHooks(cfg.WorkspacePath); err != nil {
		return fmt.Errorf("codex.GetAgentHooks: %w", err)
	}
	return nil
}

// UninstallHooks removes AO's legacy Codex hooks from the workspace-local
// .codex/hooks.json file, leaving user-defined hooks untouched. A missing file
// is a no-op.
func (p *Plugin) UninstallHooks(ctx context.Context, workspacePath string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(workspacePath) == "" {
		return errors.New("codex.UninstallHooks: workspacePath is required")
	}
	if err := removeLegacyWorkspaceHooks(workspacePath); err != nil {
		return fmt.Errorf("codex.UninstallHooks: %w", err)
	}
	return nil
}

// removeLegacyWorkspaceHooks strips AO-owned entries from a workspace-local
// hooks.json left behind by older AO versions. Files without one are untouched.
func removeLegacyWorkspaceHooks(workspacePath string) error {
	hooksPath := codexHooksPath(workspacePath)
	if _, err := os.Stat(hooksPath); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	topLevel, rawHooks, err := readCodexHooks(hooksPath)
	if err != nil {
		return err
	}

	changed := false
	for event, raw := range rawHooks {
		var groups []codexMatcherGroup
		if err := json.Unmarshal(raw, &groups); err != nil {
			return fmt.Errorf("parse %s hooks: %w", event, err)
		}
		kept := removeCodexManagedHooks(groups)
		if countCodexHooks(kept) == countCodexHooks(groups) {
			continue
		}
		changed = true
		if len(kept) == 0 {
			delete(rawHooks, event)
			continue
		}
		data, err := json.Marshal(kept)
		if err != nil {
			return fmt.Errorf("encode %s hooks: %w", event, err)
		}
		rawHooks[event] = data
	}
	if !changed {
		return nil
	}
	return writeCodexHooks(hooksPath, topLevel, rawHooks)
}

// AreHooksInstalled reports whether any legacy AO Codex hook is still present
// in the workspace-local hooks file. A missing file means none are installed.
func (p *Plugin) AreHooksInstalled(ctx context.Context, workspacePath string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if strings.TrimSpace(workspacePath) == "" {
		return false, errors.New("codex.AreHooksInstalled: workspacePath is required")
	}

	hooksPath := codexHooksPath(workspacePath)
	if _, err := os.Stat(hooksPath); errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	_, rawHooks, err := readCodexHooks(hooksPath)
	if err != nil {
		return false, fmt.Errorf("codex.AreHooksInstalled: %w", err)
	}

	for event, raw := range rawHooks {
		var groups []codexMatcherGroup
		if err := json.Unmarshal(raw, &groups); err != nil {
			return false, fmt.Errorf("codex.AreHooksInstalled: parse %s hooks: %w", event, err)
		}
		for _, group := range groups {
			for _, hook := range group.Hooks {
				if isCodexManagedHook(hook.Command) {
					return true, nil
				}
			}
		}
	}
	return false, nil
}

func codexHooksPath(workspacePath string) string {
	return filepath.Join(workspacePath, codexHooksDirName, codexHooksFileName)
}

// readCodexHooks loads the hooks file into a top-level raw map plus the decoded
// "hooks" sub-map, preserving keys AO doesn't manage. A missing or empty
// file yields empty maps.
func readCodexHooks(hooksPath string) (topLevel, rawHooks map[string]json.RawMessage, err error) {
	topLevel = map[string]json.RawMessage{}
	rawHooks = map[string]json.RawMessage{}

	data, err := os.ReadFile(hooksPath) //nolint:gosec // path built from caller-owned workspace dir
	if errors.Is(err, os.ErrNotExist) {
		return topLevel, rawHooks, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("read %s: %w", hooksPath, err)
	}
	if strings.TrimSpace(string(data)) == "" {
		return topLevel, rawHooks, nil
	}
	if err := json.Unmarshal(data, &topLevel); err != nil {
		return nil, nil, fmt.Errorf("parse %s: %w", hooksPath, err)
	}
	if hooksRaw, ok := topLevel["hooks"]; ok {
		if err := json.Unmarshal(hooksRaw, &rawHooks); err != nil {
			return nil, nil, fmt.Errorf("parse hooks in %s: %w", hooksPath, err)
		}
	}
	return topLevel, rawHooks, nil
}

// writeCodexHooks folds rawHooks back into topLevel and writes the file. An
// empty hooks map drops the "hooks" key entirely.
func writeCodexHooks(hooksPath string, topLevel, rawHooks map[string]json.RawMessage) error {
	if len(rawHooks) == 0 {
		delete(topLevel, "hooks")
	} else {
		hooksJSON, err := json.Marshal(rawHooks)
		if err != nil {
			return fmt.Errorf("encode hooks: %w", err)
		}
		topLevel["hooks"] = hooksJSON
	}

	if err := os.MkdirAll(filepath.Dir(hooksPath), 0o750); err != nil {
		return fmt.Errorf("create hook dir: %w", err)
	}
	data, err := json.MarshalIndent(topLevel, "", "  ")
	if err != nil {
		return fmt.Errorf("encode %s: %w", hooksPath, err)
	}
	data = append(data, '\n')
	if err := hookutil.AtomicWriteFile(hooksPath, data, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", hooksPath, err)
	}
	return nil
}

func isCodexManagedHook(command string) bool {
	return strings.HasPrefix(command, codexHookCommandPrefix)
}

// countCodexHooks totals the hook entries across groups so the legacy cleanup
// can tell whether stripping AO entries changed anything, including removals
// inside a group that survives.
func countCodexHooks(groups []codexMatcherGroup) int {
	total := 0
	for _, group := range groups {
		total += len(group.Hooks)
	}
	return total
}

// removeCodexManagedHooks strips AO hook entries from every group,
// dropping any group left without hooks.
func removeCodexManagedHooks(groups []codexMatcherGroup) []codexMatcherGroup {
	result := make([]codexMatcherGroup, 0, len(groups))
	for _, group := range groups {
		kept := make([]codexHookEntry, 0, len(group.Hooks))
		for _, hook := range group.Hooks {
			if !isCodexManagedHook(hook.Command) {
				kept = append(kept, hook)
			}
		}
		if len(kept) > 0 {
			group.Hooks = kept
			result = append(result, group)
		}
	}
	return result
}
