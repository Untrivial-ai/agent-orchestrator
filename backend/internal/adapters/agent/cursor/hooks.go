package cursor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/hookutil"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const (
	cursorHooksDirName  = ".cursor"
	cursorHooksFileName = "hooks.json"
	cursorDataDirEnv    = "CURSOR_DATA_DIR"
	cursorProjectsDir   = "projects"
	cursorTrustStateDir = "cursor-trust"
	cursorTrustedFile   = ".workspace-trusted"

	// cursorHooksSchemaVersion is the version Cursor's hooks.json declares. AO
	// only sets it when creating a fresh file; an existing version is preserved.
	cursorHooksSchemaVersion = 1

	// cursorHookCommandPrefix identifies the hook commands AO owns, so
	// install skips duplicates and uninstall recognizes AO entries by
	// prefix without an embedded template to diff against.
	cursorHookCommandPrefix           = "ao hooks cursor "
	cursorLegacyPermissionHookCommand = cursorHookCommandPrefix + "permission-request"
)

// cursorHookFile is the on-disk shape of .cursor/hooks.json. It is used by tests
// to decode the written file. Cursor keys hooks by camelCase native event name.
type cursorHookFile struct {
	Version int                          `json:"version"`
	Hooks   map[string][]cursorHookEntry `json:"hooks"`
}

type cursorHookEntry struct {
	Command    string
	FailClosed bool
	raw        json.RawMessage
}

func (e *cursorHookEntry) UnmarshalJSON(data []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	e.raw = append(e.raw[:0], data...)
	e.Command = ""
	e.FailClosed = false
	_ = json.Unmarshal(fields["command"], &e.Command)
	_ = json.Unmarshal(fields["failClosed"], &e.FailClosed)
	return nil
}

func (e cursorHookEntry) MarshalJSON() ([]byte, error) {
	return e.raw, nil
}

func newCursorHookEntry(command string, failClosed bool) (cursorHookEntry, error) {
	commandJSON, err := json.Marshal(command)
	if err != nil {
		return cursorHookEntry{}, err
	}
	fields := map[string]json.RawMessage{"command": commandJSON}
	raw, err := json.Marshal(fields)
	if err != nil {
		return cursorHookEntry{}, err
	}
	entry := cursorHookEntry{Command: command, raw: raw}
	if err := entry.setFailClosed(failClosed); err != nil {
		return cursorHookEntry{}, err
	}
	return entry, nil
}

func (e *cursorHookEntry) setFailClosed(failClosed bool) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(e.raw, &fields); err != nil {
		return err
	}
	e.FailClosed = failClosed
	if failClosed {
		fields["failClosed"] = json.RawMessage("true")
	} else {
		delete(fields, "failClosed")
	}
	raw, err := json.Marshal(fields)
	if err != nil {
		return err
	}
	e.raw = raw
	return nil
}

// cursorHookSpec describes one hook AO installs, defined in code rather than
// read from an embedded hooks file. Event is Cursor's native camelCase event
// name; Command is the AO sub-command dispatched when the hook fires.
type cursorHookSpec struct {
	Event      string
	Command    string
	FailClosed bool
}

// cursorManagedHooks is the source of truth for the hooks AO installs. The
// native-event → AO-subcommand contract is FIXED: the orchestrator's CLI hook
// dispatch and activity.go agree on the sub-command names.
var cursorManagedHooks = []cursorHookSpec{
	{Event: "sessionStart", Command: cursorHookCommandPrefix + "session-start"},
	{Event: "beforeSubmitPrompt", Command: cursorHookCommandPrefix + "user-prompt-submit"},
	{Event: "stop", Command: cursorHookCommandPrefix + "stop"},
	{Event: "beforeShellExecution", Command: cursorHookCommandPrefix + "before-shell-execution", FailClosed: true},
	{Event: "beforeMCPExecution", Command: cursorHookCommandPrefix + "before-mcp-execution", FailClosed: true},
	{Event: "afterShellExecution", Command: cursorHookCommandPrefix + "after-shell-execution"},
	{Event: "afterMCPExecution", Command: cursorHookCommandPrefix + "after-mcp-execution"},
	{Event: "postToolUse", Command: cursorHookCommandPrefix + "post-tool-use"},
	{Event: "postToolUseFailure", Command: cursorHookCommandPrefix + "post-tool-use-failure"},
}

// GetAgentHooks installs AO's Cursor hooks into the worktree-local
// .cursor/hooks.json file. Existing hook entries are preserved and duplicate
// AO commands are not appended.
func (p *Plugin) GetAgentHooks(ctx context.Context, cfg ports.WorkspaceHookConfig) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(cfg.WorkspacePath) == "" {
		return errors.New("cursor.GetAgentHooks: WorkspacePath is required")
	}
	if err := ensureCursorWorkspaceTrusted(cfg); err != nil {
		slog.Default().Warn("cursor: failed to seed workspace trust; Cursor may show its trust prompt",
			"sessionID", cfg.SessionID, "workspacePath", cfg.WorkspacePath, "error", err)
	}

	hooksPath := cursorHooksPath(cfg.WorkspacePath)
	topLevel, rawHooks, err := readCursorHooks(hooksPath)
	if err != nil {
		return fmt.Errorf("cursor.GetAgentHooks: %w", err)
	}

	for event, specs := range groupCursorHooksByEvent() {
		var existing []cursorHookEntry
		if err := parseCursorHookEvent(rawHooks, event, &existing); err != nil {
			return fmt.Errorf("cursor.GetAgentHooks: %w", err)
		}
		existing = removeCursorLegacyPermissionHooks(event, existing)
		for _, spec := range specs {
			if index := cursorHookCommandIndex(existing, spec.Command); index >= 0 {
				if err := existing[index].setFailClosed(spec.FailClosed); err != nil {
					return fmt.Errorf("cursor.GetAgentHooks: update %s hook: %w", event, err)
				}
			} else {
				entry, err := newCursorHookEntry(spec.Command, spec.FailClosed)
				if err != nil {
					return fmt.Errorf("cursor.GetAgentHooks: create %s hook: %w", event, err)
				}
				existing = append(existing, entry)
			}
		}
		if err := marshalCursorHookEvent(rawHooks, event, existing); err != nil {
			return fmt.Errorf("cursor.GetAgentHooks: %w", err)
		}
	}

	if err := writeCursorHooks(hooksPath, topLevel, rawHooks); err != nil {
		return fmt.Errorf("cursor.GetAgentHooks: %w", err)
	}
	if err := hookutil.EnsureWorkspaceGitignore(filepath.Dir(hooksPath), cursorHooksFileName); err != nil {
		return fmt.Errorf("cursor.GetAgentHooks: gitignore: %w", err)
	}
	return nil
}

// InstallWorkspaceTrust seeds Cursor's workspace trust marker for callers that
// manage their own Cursor profile and do not install worker hooks.
func (p *Plugin) InstallWorkspaceTrust(ctx context.Context, cfg ports.WorkspaceHookConfig) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(cfg.WorkspacePath) == "" {
		return errors.New("cursor.InstallWorkspaceTrust: WorkspacePath is required")
	}
	if err := ensureCursorWorkspaceTrusted(cfg); err != nil {
		return fmt.Errorf("cursor.InstallWorkspaceTrust: %w", err)
	}
	return nil
}

// UninstallHooks removes AO's Cursor hooks from the workspace-local
// .cursor/hooks.json file, leaving user-defined hooks untouched. A missing file
// is a no-op.
func (p *Plugin) UninstallHooks(ctx context.Context, workspacePath string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(workspacePath) == "" {
		return errors.New("cursor.UninstallHooks: workspacePath is required")
	}

	hooksPath := cursorHooksPath(workspacePath)
	if _, err := os.Stat(hooksPath); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	topLevel, rawHooks, err := readCursorHooks(hooksPath)
	if err != nil {
		return fmt.Errorf("cursor.UninstallHooks: %w", err)
	}

	for _, event := range cursorManagedEvents() {
		var entries []cursorHookEntry
		if err := parseCursorHookEvent(rawHooks, event, &entries); err != nil {
			return fmt.Errorf("cursor.UninstallHooks: %w", err)
		}
		entries = removeCursorManagedHooks(entries)
		if err := marshalCursorHookEvent(rawHooks, event, entries); err != nil {
			return fmt.Errorf("cursor.UninstallHooks: %w", err)
		}
	}

	if err := writeCursorHooks(hooksPath, topLevel, rawHooks); err != nil {
		return fmt.Errorf("cursor.UninstallHooks: %w", err)
	}
	return nil
}

// CleanupWorkspace removes durable Cursor trust state that AO created after the
// corresponding session workspace was actually torn down. User-created trust
// decisions are preserved.
func (p *Plugin) CleanupWorkspace(ctx context.Context, cfg ports.WorkspaceHookConfig) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(cfg.WorkspacePath) == "" {
		return errors.New("cursor.CleanupWorkspace: WorkspacePath is required")
	}
	if err := removeCursorWorkspaceTrust(cfg); err != nil {
		return fmt.Errorf("cursor.CleanupWorkspace: %w", err)
	}
	return nil
}

// AreHooksInstalled reports whether any AO Cursor hook is present in the
// workspace-local hooks file. A missing file means none are installed.
func (p *Plugin) AreHooksInstalled(ctx context.Context, workspacePath string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if strings.TrimSpace(workspacePath) == "" {
		return false, errors.New("cursor.AreHooksInstalled: workspacePath is required")
	}

	hooksPath := cursorHooksPath(workspacePath)
	if _, err := os.Stat(hooksPath); errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	_, rawHooks, err := readCursorHooks(hooksPath)
	if err != nil {
		return false, fmt.Errorf("cursor.AreHooksInstalled: %w", err)
	}

	for _, event := range cursorManagedEvents() {
		var entries []cursorHookEntry
		if err := parseCursorHookEvent(rawHooks, event, &entries); err != nil {
			return false, fmt.Errorf("cursor.AreHooksInstalled: %w", err)
		}
		for _, hook := range entries {
			if isCursorManagedHook(hook.Command) {
				return true, nil
			}
		}
	}
	return false, nil
}

func cursorHooksPath(workspacePath string) string {
	return filepath.Join(workspacePath, cursorHooksDirName, cursorHooksFileName)
}

type cursorWorkspaceTrust struct {
	TrustedAt     string `json:"trustedAt"`
	WorkspacePath string `json:"workspacePath"`
	TrustMethod   string `json:"trustMethod"`
	AOManaged     bool   `json:"aoManaged,omitempty"`
}

type cursorWorkspaceTrustState struct {
	TrustPath     string `json:"trustPath"`
	WorkspacePath string `json:"workspacePath"`
}

func ensureCursorWorkspaceTrusted(cfg ports.WorkspaceHookConfig) error {
	base, err := cursorWorkspaceTrustBase(cfg)
	if err != nil {
		return err
	}
	absWorkspace, err := filepath.Abs(cfg.WorkspacePath)
	if err != nil {
		return fmt.Errorf("resolve workspace: %w", err)
	}

	trustPath := cursorWorkspaceTrustPath(base, absWorkspace)
	if _, err := os.Stat(trustPath); err == nil {
		if isCursorAOTrustMarker(trustPath, absWorkspace) {
			if err := writeCursorWorkspaceTrustState(cfg, trustPath, absWorkspace); err != nil {
				return err
			}
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("stat %s: %w", trustPath, err)
	}

	if err := writeCursorWorkspaceTrustState(cfg, trustPath, absWorkspace); err != nil {
		return err
	}

	trust := cursorWorkspaceTrust{
		TrustedAt:     time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
		WorkspacePath: absWorkspace,
		TrustMethod:   "ao-session",
		AOManaged:     true,
	}
	data, err := json.MarshalIndent(trust, "", "  ")
	if err != nil {
		return fmt.Errorf("encode trust marker: %w", err)
	}
	data = append(data, '\n')

	if err := os.MkdirAll(filepath.Dir(trustPath), 0o750); err != nil {
		return fmt.Errorf("create trust dir: %w", err)
	}
	if err := os.WriteFile(trustPath, data, 0o644); err != nil { //nolint:gosec // Cursor trust path is derived from AO-owned Cursor data dir plus workspace path.
		return fmt.Errorf("write %s: %w", trustPath, err)
	}
	return nil
}

func removeCursorWorkspaceTrust(cfg ports.WorkspaceHookConfig) error {
	trustPath, absWorkspace, statePath, err := cursorWorkspaceTrustRemovalTarget(cfg)
	if err != nil {
		return err
	}
	if err := removeCursorWorkspaceTrustAt(trustPath, absWorkspace); err != nil {
		return err
	}
	if statePath != "" {
		if err := os.Remove(statePath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove trust state %s: %w", statePath, err)
		}
	}
	return nil
}

func cursorWorkspaceTrustRemovalTarget(cfg ports.WorkspaceHookConfig) (trustPath, absWorkspace, statePath string, err error) {
	if statePath = cursorWorkspaceTrustStatePath(cfg); statePath != "" {
		data, readErr := os.ReadFile(statePath)
		if readErr == nil {
			var state cursorWorkspaceTrustState
			if unmarshalErr := json.Unmarshal(data, &state); unmarshalErr == nil && state.TrustPath != "" && state.WorkspacePath != "" {
				return state.TrustPath, state.WorkspacePath, statePath, nil
			}
		} else if !errors.Is(readErr, os.ErrNotExist) {
			return "", "", "", fmt.Errorf("read trust state %s: %w", statePath, readErr)
		}
	}
	base, err := cursorWorkspaceTrustBase(cfg)
	if err != nil {
		return "", "", "", err
	}
	absWorkspace, err = filepath.Abs(cfg.WorkspacePath)
	if err != nil {
		return "", "", "", fmt.Errorf("resolve workspace: %w", err)
	}
	return cursorWorkspaceTrustPath(base, absWorkspace), absWorkspace, statePath, nil
}

func removeCursorWorkspaceTrustAt(trustPath, absWorkspace string) error {
	data, err := os.ReadFile(trustPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read %s: %w", trustPath, err)
	}
	var trust cursorWorkspaceTrust
	if err := json.Unmarshal(data, &trust); err != nil {
		return fmt.Errorf("decode %s: %w", trustPath, err)
	}
	if !trust.AOManaged || trust.WorkspacePath != absWorkspace {
		return nil
	}
	if err := os.Remove(trustPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove %s: %w", trustPath, err)
	}
	return nil
}

func writeCursorWorkspaceTrustState(cfg ports.WorkspaceHookConfig, trustPath, absWorkspace string) error {
	statePath := cursorWorkspaceTrustStatePath(cfg)
	if statePath == "" {
		return nil
	}
	state := cursorWorkspaceTrustState{TrustPath: trustPath, WorkspacePath: absWorkspace}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode trust state: %w", err)
	}
	data = append(data, '\n')
	if err := os.MkdirAll(filepath.Dir(statePath), 0o750); err != nil {
		return fmt.Errorf("create trust state dir: %w", err)
	}
	if err := os.WriteFile(statePath, data, 0o600); err != nil {
		return fmt.Errorf("write trust state %s: %w", statePath, err)
	}
	return nil
}

func cursorWorkspaceTrustStatePath(cfg ports.WorkspaceHookConfig) string {
	if strings.TrimSpace(cfg.DataDir) == "" || strings.TrimSpace(cfg.SessionID) == "" {
		return ""
	}
	return filepath.Join(cfg.DataDir, cursorTrustStateDir, cfg.SessionID+".json")
}

func isCursorAOTrustMarker(trustPath, absWorkspace string) bool {
	data, err := os.ReadFile(trustPath)
	if err != nil {
		return false
	}
	var trust cursorWorkspaceTrust
	if err := json.Unmarshal(data, &trust); err != nil {
		return false
	}
	return trust.AOManaged && trust.WorkspacePath == absWorkspace
}

func cursorWorkspaceTrustBase(cfg ports.WorkspaceHookConfig) (string, error) {
	if override := strings.TrimSpace(cfg.Env[cursorDataDirEnv]); override != "" {
		return override, nil
	}
	if strings.TrimSpace(cfg.DataDir) != "" {
		return cursorDataDir(cfg.DataDir), nil
	}
	return "", errors.New("AO data dir is required for Cursor workspace trust")
}

func cursorWorkspaceTrustPath(base, workspacePath string) string {
	return filepath.Join(base, cursorProjectsDir, cursorWorkspaceProjectName(workspacePath), cursorTrustedFile)
}

var cursorProjectNamePattern = regexp.MustCompile(`[^A-Za-z0-9]+`)

func cursorWorkspaceProjectName(workspacePath string) string {
	clean := filepath.Clean(workspacePath)
	slashed := filepath.ToSlash(clean)
	return strings.Trim(cursorProjectNamePattern.ReplaceAllString(slashed, "-"), "-")
}

// readCursorHooks loads the hooks file into a top-level raw map plus the decoded
// "hooks" sub-map, preserving keys AO doesn't manage (e.g. "version"). A missing
// or empty file yields empty maps.
func readCursorHooks(hooksPath string) (topLevel, rawHooks map[string]json.RawMessage, err error) {
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

// writeCursorHooks folds rawHooks back into topLevel and writes the file. An
// empty hooks map drops the "hooks" key entirely. A "version" key is ensured so
// a freshly created file declares the schema version Cursor expects, while an
// existing version (preserved in topLevel) is left untouched.
func writeCursorHooks(hooksPath string, topLevel, rawHooks map[string]json.RawMessage) error {
	if len(rawHooks) == 0 {
		delete(topLevel, "hooks")
	} else {
		hooksJSON, err := json.Marshal(rawHooks)
		if err != nil {
			return fmt.Errorf("encode hooks: %w", err)
		}
		topLevel["hooks"] = hooksJSON
		if _, ok := topLevel["version"]; !ok {
			versionJSON, err := json.Marshal(cursorHooksSchemaVersion)
			if err != nil {
				return fmt.Errorf("encode version: %w", err)
			}
			topLevel["version"] = versionJSON
		}
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

// groupCursorHooksByEvent groups the managed hook specs by their Cursor event so
// each event's array is rewritten once.
func groupCursorHooksByEvent() map[string][]cursorHookSpec {
	byEvent := map[string][]cursorHookSpec{}
	for _, spec := range cursorManagedHooks {
		byEvent[spec.Event] = append(byEvent[spec.Event], spec)
	}
	return byEvent
}

// cursorManagedEvents returns the distinct Cursor events AO manages, in the
// order they first appear in cursorManagedHooks.
func cursorManagedEvents() []string {
	seen := map[string]bool{}
	events := make([]string, 0, len(cursorManagedHooks))
	for _, spec := range cursorManagedHooks {
		if !seen[spec.Event] {
			seen[spec.Event] = true
			events = append(events, spec.Event)
		}
	}
	return events
}

func isCursorManagedHook(command string) bool {
	if command == cursorLegacyPermissionHookCommand {
		return true
	}
	for _, spec := range cursorManagedHooks {
		if command == spec.Command {
			return true
		}
	}
	return false
}

// removeCursorManagedHooks strips AO hook entries from an event's array,
// preserving user-defined entries.
func removeCursorManagedHooks(entries []cursorHookEntry) []cursorHookEntry {
	kept := make([]cursorHookEntry, 0, len(entries))
	for _, hook := range entries {
		if !isCursorManagedHook(hook.Command) {
			kept = append(kept, hook)
		}
	}
	return kept
}

func removeCursorLegacyPermissionHooks(event string, entries []cursorHookEntry) []cursorHookEntry {
	switch event {
	case "beforeShellExecution", "beforeMCPExecution":
	default:
		return entries
	}
	kept := make([]cursorHookEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.Command != cursorLegacyPermissionHookCommand {
			kept = append(kept, entry)
		}
	}
	return kept
}

func parseCursorHookEvent(rawHooks map[string]json.RawMessage, event string, target *[]cursorHookEntry) error {
	data, ok := rawHooks[event]
	if !ok {
		return nil
	}
	if err := json.Unmarshal(data, target); err != nil {
		return fmt.Errorf("parse %s hooks: %w", event, err)
	}
	return nil
}

func marshalCursorHookEvent(rawHooks map[string]json.RawMessage, event string, entries []cursorHookEntry) error {
	if len(entries) == 0 {
		delete(rawHooks, event)
		return nil
	}
	data, err := json.Marshal(entries)
	if err != nil {
		return fmt.Errorf("encode %s hooks: %w", event, err)
	}
	rawHooks[event] = data
	return nil
}

func cursorHookCommandIndex(entries []cursorHookEntry, command string) int {
	for index, hook := range entries {
		if hook.Command == command {
			return index
		}
	}
	return -1
}
