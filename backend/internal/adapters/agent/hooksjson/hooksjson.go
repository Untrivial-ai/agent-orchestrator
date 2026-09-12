// Package hooksjson implements the matcher-group hooks file that several agents
// (claude-code, goose, qwen, agy, droid, kimchi) share byte-for-byte in shape.
// Each file is a JSON object with a "hooks" sub-map keyed by native event name,
// whose values are matcher groups ({matcher?, hooks:[{type,command,timeout}]}). The
// adapters differed only in the file path, the AO command prefix, the per-hook
// timeout, and which events they install, so they describe those with a Manager
// and share the install/uninstall/detect logic here.
//
// The read/write path preserves every top-level key and every user-defined hook
// AO does not own, and writes atomically, so installing AO's hooks never clobbers
// unrelated settings.
package hooksjson

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/hookutil"
)

// HookEntry is one command hook inside a matcher group.
type HookEntry struct {
	Type    string                     `json:"type"`
	Command string                     `json:"command"`
	Timeout int                        `json:"timeout,omitempty"`
	Extra   map[string]json.RawMessage `json:"-"`
}

// MatcherGroup is a set of hooks sharing one matcher. Matcher is a pointer so it
// round-trips exactly: events that require a matcher (e.g. claude SessionStart's
// "startup") carry one; events that omit it serialize without the key.
type MatcherGroup struct {
	Matcher *string                    `json:"matcher,omitempty"`
	Hooks   []HookEntry                `json:"hooks"`
	Extra   map[string]json.RawMessage `json:"-"`
}

// UnmarshalJSON retains fields introduced by an agent before AO learns about
// them. User hooks may depend on agent-specific keys such as async or
// commandWindows, and reconciling AO hooks must not remove them.
func (h *HookEntry) UnmarshalJSON(data []byte) error {
	type known struct {
		Type    string `json:"type"`
		Command string `json:"command"`
		Timeout int    `json:"timeout,omitempty"`
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	var parsed known
	if err := json.Unmarshal(data, &parsed); err != nil {
		return err
	}
	h.Type = parsed.Type
	h.Command = parsed.Command
	h.Timeout = parsed.Timeout
	delete(fields, "type")
	delete(fields, "command")
	delete(fields, "timeout")
	if len(fields) == 0 {
		fields = nil
	}
	h.Extra = fields
	return nil
}

// MarshalJSON writes known fields from the typed representation and folds all
// preserved agent-specific fields back into the same object.
func (h HookEntry) MarshalJSON() ([]byte, error) {
	fields := cloneRawFields(h.Extra)
	if err := setRawField(fields, "type", h.Type); err != nil {
		return nil, err
	}
	if err := setRawField(fields, "command", h.Command); err != nil {
		return nil, err
	}
	if h.Timeout != 0 {
		if err := setRawField(fields, "timeout", h.Timeout); err != nil {
			return nil, err
		}
	} else {
		delete(fields, "timeout")
	}
	return json.Marshal(fields)
}

// UnmarshalJSON preserves unknown matcher-group fields for the same reason as
// HookEntry: reconciling one AO hook must not rewrite unrelated user behavior.
func (g *MatcherGroup) UnmarshalJSON(data []byte) error {
	type known struct {
		Matcher *string     `json:"matcher,omitempty"`
		Hooks   []HookEntry `json:"hooks"`
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	var parsed known
	if err := json.Unmarshal(data, &parsed); err != nil {
		return err
	}
	g.Matcher = parsed.Matcher
	g.Hooks = parsed.Hooks
	delete(fields, "matcher")
	delete(fields, "hooks")
	if len(fields) == 0 {
		fields = nil
	}
	g.Extra = fields
	return nil
}

// MarshalJSON folds preserved matcher-group fields back into the object.
func (g MatcherGroup) MarshalJSON() ([]byte, error) {
	fields := cloneRawFields(g.Extra)
	if g.Matcher != nil {
		if err := setRawField(fields, "matcher", g.Matcher); err != nil {
			return nil, err
		}
	} else {
		delete(fields, "matcher")
	}
	if err := setRawField(fields, "hooks", g.Hooks); err != nil {
		return nil, err
	}
	return json.Marshal(fields)
}

func cloneRawFields(source map[string]json.RawMessage) map[string]json.RawMessage {
	fields := make(map[string]json.RawMessage, len(source)+3)
	for key, value := range source {
		fields[key] = value
	}
	return fields
}

func setRawField(fields map[string]json.RawMessage, key string, value any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	fields[key] = encoded
	return nil
}

// HookSpec describes one hook AO installs: the native event it attaches to, its
// optional matcher, and the command to run. Adapters define these in code rather
// than reading an embedded template.
type HookSpec struct {
	Event   string
	Matcher *string
	Command string
}

// Manager installs, removes, and detects AO's hooks in one agent's matcher-group
// hooks file. Construct one per adapter with its file path, command prefix,
// per-hook timeout, and managed hook set.
type Manager struct {
	// Label prefixes error messages, e.g. "claude-code" or "goose", so the
	// wrapped error reads "<label>.GetAgentHooks: ...".
	Label string
	// CommandPrefix identifies AO-owned hook commands, e.g. "ao hooks goose ".
	// Install skips commands already present and uninstall/detect match on it.
	CommandPrefix string
	// LegacyCommandPrefixes identifies AO-owned command prefixes from an older
	// installer that should be removed while installing the current hooks.
	LegacyCommandPrefixes []string
	// Timeout is written into each installed hook entry.
	Timeout int
	// Path returns the hooks file path for a workspace.
	Path func(workspacePath string) string
	// Managed is the set of hooks AO installs.
	Managed []HookSpec
}

// Install reconciles AO's managed hooks into the workspace's hooks file,
// preserving user-defined hooks and unrelated settings. A managed command is
// moved when its matcher changes and is never duplicated. It also writes a
// self-ignoring .gitignore covering the hooks file so it does not block
// worktree teardown.
func (m Manager) Install(ctx context.Context, workspacePath string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(workspacePath) == "" {
		return fmt.Errorf("%s.GetAgentHooks: WorkspacePath is required", m.Label)
	}

	hooksPath := m.Path(workspacePath)
	topLevel, rawHooks, err := readHooksFile(hooksPath)
	if err != nil {
		return fmt.Errorf("%s.GetAgentHooks: %w", m.Label, err)
	}

	for event, specs := range m.groupByEvent() {
		var groups []MatcherGroup
		if err := parseEvent(rawHooks, event, &groups); err != nil {
			return fmt.Errorf("%s.GetAgentHooks: %w", m.Label, err)
		}
		groups = removeManagedPrefixes(groups, m.LegacyCommandPrefixes)
		for _, spec := range specs {
			entry := HookEntry{Type: "command", Command: spec.Command, Timeout: m.Timeout}
			groups = reconcileHook(groups, entry, spec.Matcher)
		}
		if err := marshalEvent(rawHooks, event, groups); err != nil {
			return fmt.Errorf("%s.GetAgentHooks: %w", m.Label, err)
		}
	}

	if err := writeHooksFile(hooksPath, topLevel, rawHooks); err != nil {
		return fmt.Errorf("%s.GetAgentHooks: %w", m.Label, err)
	}
	if err := hookutil.EnsureWorkspaceGitignore(filepath.Dir(hooksPath), filepath.Base(hooksPath)); err != nil {
		return fmt.Errorf("%s.GetAgentHooks: gitignore: %w", m.Label, err)
	}
	return nil
}

func removeManagedPrefixes(groups []MatcherGroup, prefixes []string) []MatcherGroup {
	if len(prefixes) == 0 {
		return groups
	}
	result := make([]MatcherGroup, 0, len(groups))
	for _, group := range groups {
		kept := make([]HookEntry, 0, len(group.Hooks))
		for _, hook := range group.Hooks {
			legacy := false
			for _, prefix := range prefixes {
				if strings.HasPrefix(hook.Command, prefix) {
					legacy = true
					break
				}
			}
			if !legacy {
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

// Uninstall removes AO's hooks from the workspace's hooks file, leaving
// user-defined hooks and unrelated settings untouched. A missing file is a no-op.
func (m Manager) Uninstall(ctx context.Context, workspacePath string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(workspacePath) == "" {
		return fmt.Errorf("%s.UninstallHooks: workspacePath is required", m.Label)
	}

	hooksPath := m.Path(workspacePath)
	if _, err := os.Stat(hooksPath); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	topLevel, rawHooks, err := readHooksFile(hooksPath)
	if err != nil {
		return fmt.Errorf("%s.UninstallHooks: %w", m.Label, err)
	}

	for _, event := range m.managedEvents() {
		var groups []MatcherGroup
		if err := parseEvent(rawHooks, event, &groups); err != nil {
			return fmt.Errorf("%s.UninstallHooks: %w", m.Label, err)
		}
		groups = removeManaged(groups, m.CommandPrefix)
		if err := marshalEvent(rawHooks, event, groups); err != nil {
			return fmt.Errorf("%s.UninstallHooks: %w", m.Label, err)
		}
	}

	if err := writeHooksFile(hooksPath, topLevel, rawHooks); err != nil {
		return fmt.Errorf("%s.UninstallHooks: %w", m.Label, err)
	}
	return nil
}

// AreInstalled reports whether any AO hook is present in the workspace's hooks
// file. A missing file means none are installed.
func (m Manager) AreInstalled(ctx context.Context, workspacePath string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if strings.TrimSpace(workspacePath) == "" {
		return false, fmt.Errorf("%s.AreHooksInstalled: workspacePath is required", m.Label)
	}

	hooksPath := m.Path(workspacePath)
	if _, err := os.Stat(hooksPath); errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	_, rawHooks, err := readHooksFile(hooksPath)
	if err != nil {
		return false, fmt.Errorf("%s.AreHooksInstalled: %w", m.Label, err)
	}

	for _, event := range m.managedEvents() {
		var groups []MatcherGroup
		if err := parseEvent(rawHooks, event, &groups); err != nil {
			return false, fmt.Errorf("%s.AreHooksInstalled: %w", m.Label, err)
		}
		for _, group := range groups {
			for _, hook := range group.Hooks {
				if strings.HasPrefix(hook.Command, m.CommandPrefix) {
					return true, nil
				}
			}
		}
	}
	return false, nil
}

// groupByEvent groups the managed specs by event so each event array is
// rewritten once.
func (m Manager) groupByEvent() map[string][]HookSpec {
	byEvent := map[string][]HookSpec{}
	for _, spec := range m.Managed {
		byEvent[spec.Event] = append(byEvent[spec.Event], spec)
	}
	return byEvent
}

// managedEvents returns the distinct managed events, in first-seen order.
func (m Manager) managedEvents() []string {
	seen := map[string]bool{}
	events := make([]string, 0, len(m.Managed))
	for _, spec := range m.Managed {
		if !seen[spec.Event] {
			seen[spec.Event] = true
			events = append(events, spec.Event)
		}
	}
	return events
}

// readHooksFile loads the file into a top-level raw map plus the decoded "hooks"
// sub-map, preserving every key AO doesn't manage. A missing or empty file
// yields empty maps.
func readHooksFile(hooksPath string) (topLevel, rawHooks map[string]json.RawMessage, err error) {
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

// writeHooksFile folds rawHooks back into topLevel and writes the file
// atomically. An empty hooks map drops the "hooks" key entirely.
func writeHooksFile(hooksPath string, topLevel, rawHooks map[string]json.RawMessage) error {
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

func parseEvent(rawHooks map[string]json.RawMessage, event string, target *[]MatcherGroup) error {
	data, ok := rawHooks[event]
	if !ok {
		return nil
	}
	if err := json.Unmarshal(data, target); err != nil {
		return fmt.Errorf("parse %s hooks: %w", event, err)
	}
	return nil
}

func marshalEvent(rawHooks map[string]json.RawMessage, event string, groups []MatcherGroup) error {
	if len(groups) == 0 {
		delete(rawHooks, event)
		return nil
	}
	data, err := json.Marshal(groups)
	if err != nil {
		return fmt.Errorf("encode %s hooks: %w", event, err)
	}
	rawHooks[event] = data
	return nil
}

// reconcileHook removes every copy of one AO-owned command before adding the
// current managed entry under its declared matcher. This lets adapter upgrades
// change a matcher or timeout without leaving stale or duplicate commands,
// while preserving every unrelated hook in the affected groups.
func reconcileHook(groups []MatcherGroup, hook HookEntry, matcher *string) []MatcherGroup {
	result := make([]MatcherGroup, 0, len(groups))
	keptEmptyTarget := false
	for _, group := range groups {
		kept := make([]HookEntry, 0, len(group.Hooks))
		removedManaged := false
		for _, existing := range group.Hooks {
			if existing.Command == hook.Command {
				removedManaged = true
				if hook.Extra == nil {
					hook.Extra = existing.Extra
				}
				continue
			}
			kept = append(kept, existing)
		}
		if len(kept) > 0 {
			group.Hooks = kept
			result = append(result, group)
		} else if removedManaged && !keptEmptyTarget && matchersEqual(group.Matcher, matcher) {
			// Reuse the target group so agent-specific group fields survive an
			// in-place refresh of AO's hook.
			group.Hooks = nil
			result = append(result, group)
			keptEmptyTarget = true
		}
	}
	return addHook(result, hook, matcher)
}

// addHook appends hook to the group with a matching matcher, creating that group
// if none matches.
func addHook(groups []MatcherGroup, hook HookEntry, matcher *string) []MatcherGroup {
	for i, group := range groups {
		if matchersEqual(group.Matcher, matcher) {
			groups[i].Hooks = append(groups[i].Hooks, hook)
			return groups
		}
	}
	return append(groups, MatcherGroup{Matcher: matcher, Hooks: []HookEntry{hook}})
}

// removeManaged strips AO hook entries (matched by command prefix) from every
// group, dropping any group left without hooks so the event array doesn't
// accumulate empty matcher objects.
func removeManaged(groups []MatcherGroup, prefix string) []MatcherGroup {
	result := make([]MatcherGroup, 0, len(groups))
	for _, group := range groups {
		kept := make([]HookEntry, 0, len(group.Hooks))
		for _, hook := range group.Hooks {
			if !strings.HasPrefix(hook.Command, prefix) {
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

func matchersEqual(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}
