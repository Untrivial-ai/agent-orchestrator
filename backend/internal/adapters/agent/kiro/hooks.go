package kiro

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
	// Kiro reads hooks from a workspace-local agent configuration file at
	// .kiro/agents/<name>.json. AO installs its hooks into a dedicated agent
	// file so it never clobbers a user's own agents.
	// See https://kiro.dev/docs/cli/hooks/ and
	// https://kiro.dev/docs/cli/custom-agents/configuration-reference#hooks-field
	kiroHooksDirName  = ".kiro"
	kiroAgentsDirName = "agents"
	kiroAgentFileName = "ao.json"

	// kiroHookCommandPrefix identifies the hook commands AO owns, so install
	// skips duplicates and uninstall recognizes AO entries by prefix without an
	// embedded template to diff against.
	kiroHookCommandPrefix = "ao hooks kiro "

	kiroAgentName        = "ao"
	kiroAgentDescription = "Agent Orchestrator session instructions"
)

// kiroHookFile is the on-disk shape of .kiro/agents/ao.json. It is used by
// tests to decode the written file. Kiro hooks are a map of camelCase event
// name to a flat array of {matcher?, command} entries.
type kiroHookFile struct {
	Name   string                     `json:"name"`
	Prompt *string                    `json:"prompt"`
	Hooks  map[string][]kiroHookEntry `json:"hooks"`
}

type kiroHookEntry struct {
	Matcher string `json:"matcher,omitempty"`
	Command string `json:"command"`
}

// kiroHookSpec describes one hook AO installs, defined in code rather than read
// from an embedded hooks file.
type kiroHookSpec struct {
	// Event is the native Kiro hook event name (camelCase).
	Event string
	// Command is the AO hook command line.
	Command string
}

// kiroManagedHooks is the source of truth for the hooks AO installs. The native
// Kiro events are mapped onto AO hook sub-command names (the trailing word) so
// the CLI hook dispatcher routes them to StandardDeriveActivityState:
//
//	agentSpawn       -> session-start       (ActivityActive)
//	userPromptSubmit -> user-prompt-submit  (ActivityActive)
//	preToolUse       -> tool-use            (ActivityActive)
//	stop             -> stop                (ActivityIdle)
//
// preToolUse fires before EVERY tool invocation, not just ones that gate on a
// user decision, so it means "the agent is about to work" (tool-use → active),
// not "blocked on the user". Routing it to permission-request pinned an
// actively-working session to the sticky waiting_input state, hiding its
// PR-derived status and firing spurious "needs input" notifications.
var kiroManagedHooks = []kiroHookSpec{
	{Event: "agentSpawn", Command: kiroHookCommandPrefix + "session-start"},
	{Event: "userPromptSubmit", Command: kiroHookCommandPrefix + "user-prompt-submit"},
	{Event: "preToolUse", Command: kiroHookCommandPrefix + "tool-use"},
	{Event: "stop", Command: kiroHookCommandPrefix + "stop"},
}

// GetAgentHooks installs AO's Kiro hooks into the worktree-local
// .kiro/agents/ao.json file. Existing hook entries are preserved and duplicate
// AO commands are not appended.
func (p *Plugin) GetAgentHooks(ctx context.Context, cfg ports.WorkspaceHookConfig) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(cfg.WorkspacePath) == "" {
		return errors.New("kiro.GetAgentHooks: WorkspacePath is required")
	}

	hooksPath := kiroAgentPath(cfg.WorkspacePath)
	topLevel, rawHooks, err := readKiroHooks(hooksPath)
	if err != nil {
		return fmt.Errorf("kiro.GetAgentHooks: %w", err)
	}

	for event, specs := range groupKiroHooksByEvent() {
		var existing []kiroHookEntry
		if err := parseKiroHookEvent(rawHooks, event, &existing); err != nil {
			return fmt.Errorf("kiro.GetAgentHooks: %w", err)
		}
		for _, spec := range specs {
			if !kiroHookCommandExists(existing, spec.Command) {
				existing = append(existing, kiroHookEntry{Command: spec.Command})
			}
		}
		if err := marshalKiroHookEvent(rawHooks, event, existing); err != nil {
			return fmt.Errorf("kiro.GetAgentHooks: %w", err)
		}
	}

	if strings.TrimSpace(cfg.SystemPrompt) != "" && strings.TrimSpace(cfg.SystemPromptFile) == "" {
		return fmt.Errorf("kiro.GetAgentHooks: %w", errors.New("kiro: system prompt file required to build agent config"))
	}
	if err := writeKiroHooks(hooksPath, topLevel, rawHooks, "", cfg.SystemPromptFile, cfg.Config); err != nil {
		return fmt.Errorf("kiro.GetAgentHooks: %w", err)
	}
	if err := hookutil.EnsureWorkspaceGitignore(filepath.Dir(hooksPath), kiroAgentFileName); err != nil {
		return fmt.Errorf("kiro.GetAgentHooks: gitignore: %w", err)
	}
	return nil
}

// UninstallHooks removes AO's Kiro hooks from the workspace-local
// .kiro/agents/ao.json file, leaving user-defined hooks untouched. A missing
// file is a no-op.
func (p *Plugin) UninstallHooks(ctx context.Context, workspacePath string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(workspacePath) == "" {
		return errors.New("kiro.UninstallHooks: workspacePath is required")
	}

	hooksPath := kiroAgentPath(workspacePath)
	if _, err := os.Stat(hooksPath); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	topLevel, rawHooks, err := readKiroHooks(hooksPath)
	if err != nil {
		return fmt.Errorf("kiro.UninstallHooks: %w", err)
	}

	for _, event := range kiroManagedEvents() {
		var entries []kiroHookEntry
		if err := parseKiroHookEvent(rawHooks, event, &entries); err != nil {
			return fmt.Errorf("kiro.UninstallHooks: %w", err)
		}
		entries = removeKiroManagedHooks(entries)
		if err := marshalKiroHookEvent(rawHooks, event, entries); err != nil {
			return fmt.Errorf("kiro.UninstallHooks: %w", err)
		}
	}

	if err := writeKiroHooks(hooksPath, topLevel, rawHooks, "", "", ports.AgentConfig{}); err != nil {
		return fmt.Errorf("kiro.UninstallHooks: %w", err)
	}
	return nil
}

// AreHooksInstalled reports whether any AO Kiro hook is present in the
// workspace-local agent file. A missing file means none are installed.
func (p *Plugin) AreHooksInstalled(ctx context.Context, workspacePath string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if strings.TrimSpace(workspacePath) == "" {
		return false, errors.New("kiro.AreHooksInstalled: workspacePath is required")
	}

	hooksPath := kiroAgentPath(workspacePath)
	if _, err := os.Stat(hooksPath); errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	_, rawHooks, err := readKiroHooks(hooksPath)
	if err != nil {
		return false, fmt.Errorf("kiro.AreHooksInstalled: %w", err)
	}

	for _, event := range kiroManagedEvents() {
		var entries []kiroHookEntry
		if err := parseKiroHookEvent(rawHooks, event, &entries); err != nil {
			return false, fmt.Errorf("kiro.AreHooksInstalled: %w", err)
		}
		for _, entry := range entries {
			if isKiroManagedHook(entry.Command) {
				return true, nil
			}
		}
	}
	return false, nil
}

func kiroAgentPath(workspacePath string) string {
	return filepath.Join(workspacePath, kiroHooksDirName, kiroAgentsDirName, kiroAgentFileName)
}

// readKiroHooks loads the agent file into a top-level raw map plus the decoded
// "hooks" sub-map, preserving keys AO doesn't manage. A missing or empty file
// yields empty maps.
func readKiroHooks(hooksPath string) (topLevel, rawHooks map[string]json.RawMessage, err error) {
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

// writeKiroHooks folds rawHooks back into topLevel and writes the file. An
// empty hooks map drops the "hooks" key entirely.
func writeKiroHooks(hooksPath string, topLevel, rawHooks map[string]json.RawMessage, systemPrompt, systemPromptFile string, agentConfig ports.AgentConfig) error {
	if err := setKiroAgentDefaults(topLevel, systemPrompt, systemPromptFile, agentConfig); err != nil {
		return err
	}

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

func setKiroAgentDefaults(topLevel map[string]json.RawMessage, systemPrompt, systemPromptFile string, agentConfig ports.AgentConfig) error {
	defaults := map[string]any{
		"name":           kiroAgentName,
		"description":    kiroAgentDescription,
		"prompt":         nil,
		"mcpServers":     map[string]any{},
		"tools":          []string{"*"},
		"toolAliases":    map[string]any{},
		"allowedTools":   []any{},
		"resources":      []any{},
		"toolsSettings":  map[string]any{},
		"includeMcpJson": true,
	}
	if strings.TrimSpace(systemPrompt) != "" {
		defaults["prompt"] = systemPrompt
	} else if promptFile := strings.TrimSpace(systemPromptFile); promptFile != "" {
		defaults["prompt"] = "file://" + filepath.ToSlash(promptFile)
	}
	if model := strings.TrimSpace(agentConfig.Model); model != "" {
		defaults["model"] = model
	} else {
		delete(topLevel, "model")
	}

	for key, value := range defaults {
		managedKey := key == "name" || key == "prompt" || key == "model"
		if !managedKey {
			if _, ok := topLevel[key]; ok {
				continue
			}
		}
		data, err := json.Marshal(value)
		if err != nil {
			return fmt.Errorf("encode agent %s: %w", key, err)
		}
		topLevel[key] = data
	}
	return nil
}

// groupKiroHooksByEvent groups the managed hook specs by their Kiro event so
// each event's array is rewritten once.
func groupKiroHooksByEvent() map[string][]kiroHookSpec {
	byEvent := map[string][]kiroHookSpec{}
	for _, spec := range kiroManagedHooks {
		byEvent[spec.Event] = append(byEvent[spec.Event], spec)
	}
	return byEvent
}

// kiroManagedEvents returns the distinct Kiro events AO manages, in the order
// they first appear in kiroManagedHooks.
func kiroManagedEvents() []string {
	seen := map[string]bool{}
	events := make([]string, 0, len(kiroManagedHooks))
	for _, spec := range kiroManagedHooks {
		if !seen[spec.Event] {
			seen[spec.Event] = true
			events = append(events, spec.Event)
		}
	}
	return events
}

func isKiroManagedHook(command string) bool {
	return strings.HasPrefix(command, kiroHookCommandPrefix)
}

// removeKiroManagedHooks strips AO hook entries from an event's array.
func removeKiroManagedHooks(entries []kiroHookEntry) []kiroHookEntry {
	kept := make([]kiroHookEntry, 0, len(entries))
	for _, entry := range entries {
		if !isKiroManagedHook(entry.Command) {
			kept = append(kept, entry)
		}
	}
	return kept
}

func parseKiroHookEvent(rawHooks map[string]json.RawMessage, event string, target *[]kiroHookEntry) error {
	data, ok := rawHooks[event]
	if !ok {
		return nil
	}
	if err := json.Unmarshal(data, target); err != nil {
		return fmt.Errorf("parse %s hooks: %w", event, err)
	}
	return nil
}

func marshalKiroHookEvent(rawHooks map[string]json.RawMessage, event string, entries []kiroHookEntry) error {
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

func kiroHookCommandExists(entries []kiroHookEntry, command string) bool {
	for _, entry := range entries {
		if entry.Command == command {
			return true
		}
	}
	return false
}
