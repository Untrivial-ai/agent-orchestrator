package openhands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/hooksjson"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/hookutil"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const (
	hooksDirName      = ".openhands"
	hooksFileName     = "hooks.json"
	hookCommandPrefix = "ao hooks openhands "
	// hookTimeout is in seconds, OpenHands' HookDefinition.timeout unit.
	hookTimeout = 30
)

// managedHooks is the source of truth for the hooks AO installs. OpenHands has
// no permission-request event (confirmation happens inside the TUI), so AO
// installs the session/prompt/stop signals that the standard activity deriver
// understands. UserPromptSubmit is also where AO instructions are injected via
// additionalContext, since OpenHands ignores SessionStart output.
var managedHooks = []hooksjson.HookSpec{
	{Event: "SessionStart", Command: hookCommandPrefix + "session-start"},
	{Event: "UserPromptSubmit", Command: hookCommandPrefix + "user-prompt-submit"},
	{Event: "Stop", Command: hookCommandPrefix + "stop"},
}

var hooks = hooksjson.Manager{
	Label:         adapterID,
	CommandPrefix: hookCommandPrefix,
	Timeout:       hookTimeout,
	Path:          hooksPath,
	Managed:       managedHooks,
}

func hooksPath(workspacePath string) string {
	return filepath.Join(workspacePath, hooksDirName, hooksFileName)
}

// hookEvents lists OpenHands' hook events in PascalCase. OpenHands also
// accepts each one in snake_case, with or without a {"hooks": {...}} wrapper.
var hookEvents = []string{"PreToolUse", "PostToolUse", "UserPromptSubmit", "SessionStart", "SessionEnd", "Stop"}

func toSnake(pascal string) string {
	var b strings.Builder
	for i, r := range pascal {
		if r >= 'A' && r <= 'Z' {
			if i > 0 {
				b.WriteByte('_')
			}
			r += 'a' - 'A'
		}
		b.WriteRune(r)
	}
	return b.String()
}

// GetAgentHooks installs AO's OpenHands hooks, preserving user-defined hooks.
func (p *Plugin) GetAgentHooks(ctx context.Context, cfg ports.WorkspaceHookConfig) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(cfg.WorkspacePath) != "" {
		if err := normalizeHooksFile(hooksPath(cfg.WorkspacePath)); err != nil {
			return fmt.Errorf("%s.GetAgentHooks: %w", adapterID, err)
		}
	}
	return hooks.Install(ctx, cfg.WorkspacePath)
}

// UninstallHooks removes AO's OpenHands hooks, leaving user-defined hooks untouched.
func (p *Plugin) UninstallHooks(ctx context.Context, workspacePath string) error {
	return hooks.Uninstall(ctx, workspacePath)
}

// AreHooksInstalled reports whether any AO OpenHands hook is present.
func (p *Plugin) AreHooksInstalled(ctx context.Context, workspacePath string) (bool, error) {
	return hooks.AreInstalled(ctx, workspacePath)
}

// normalizeHooksFile rewrites an existing hooks file into the wrapped
// PascalCase form hooksjson manages, so AO's entries merge with the user's
// instead of breaking them. Two OpenHands behaviors make this necessary:
//
//   - When a top-level "hooks" key is present, OpenHands reads only that key
//     and drops every other top-level key. Adding a wrapper to a direct-format
//     file ({"stop": [...]}) would silently disable the user's hooks.
//   - A file that spells one event both ways ("stop" and "Stop") fails
//     validation as a duplicate, which disables every hook in it.
//
// Matcher groups for one event keep the PascalCase spelling's groups first.
// A missing, empty, or already-normalized file is left untouched.
func normalizeHooksFile(path string) error {
	data, err := os.ReadFile(path) //nolint:gosec // path built from caller-owned workspace dir
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	if strings.TrimSpace(string(data)) == "" {
		return nil
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}

	events := top
	wrapped := false
	if raw, ok := top["hooks"]; ok {
		wrapped = true
		events = map[string]json.RawMessage{}
		if err := json.Unmarshal(raw, &events); err != nil {
			return fmt.Errorf("parse hooks in %s: %w", path, err)
		}
	}

	// Move every event spelling into the wrapper under its PascalCase key,
	// PascalCase groups first, then snake_case ones. Non-event keys stay where
	// they were: OpenHands ignores extra top-level keys next to a wrapper.
	wrappedEvents := map[string]json.RawMessage{}
	changed := !wrapped
	for _, pascal := range hookEvents {
		var groups []json.RawMessage
		found := false
		for _, key := range []string{pascal, toSnake(pascal)} {
			raw, ok := events[key]
			if !ok {
				continue
			}
			found = true
			delete(events, key)
			if key != pascal {
				changed = true
			}
			var more []json.RawMessage
			if err := json.Unmarshal(raw, &more); err != nil {
				return fmt.Errorf("parse %s in %s: %w", key, path, err)
			}
			groups = append(groups, more...)
		}
		if !found {
			continue
		}
		raw, err := json.Marshal(groups)
		if err != nil {
			return fmt.Errorf("encode %s: %w", pascal, err)
		}
		wrappedEvents[pascal] = raw
	}
	if !changed || len(wrappedEvents) == 0 {
		return nil
	}

	out := top
	if wrapped {
		// Unknown keys inside the wrapper are the user's; keep them there.
		for key, raw := range events {
			wrappedEvents[key] = raw
		}
	}
	eventsJSON, err := json.Marshal(wrappedEvents)
	if err != nil {
		return fmt.Errorf("encode hooks: %w", err)
	}
	out["hooks"] = eventsJSON
	encoded, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return fmt.Errorf("encode %s: %w", path, err)
	}
	encoded = append(encoded, '\n')
	if err := hookutil.AtomicWriteFile(path, encoded, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}
