package workerexec

import (
	"fmt"
	"path/filepath"
	"strings"
)

const cloudHookBinary = "/usr/local/bin/ao"

type activityHook struct {
	nativeEvent string
	event       string
	matcher     string
}

var claudeActivityHooks = []activityHook{
	{"SessionStart", "session-start", "startup"},
	{"UserPromptSubmit", "user-prompt-submit", ""},
	{"PreToolUse", "pre-tool-use", ""},
	{"PostToolUse", "post-tool-use", ""},
	{"PostToolUseFailure", "post-tool-use-failure", ""},
	{"PermissionRequest", "permission-request", ""},
	{"Stop", "stop", ""},
	{"Notification", "notification", ""},
	{"SessionEnd", "session-end", ""},
}

var codexActivityHooks = []activityHook{
	{"SessionStart", "session-start", ""},
	{"UserPromptSubmit", "user-prompt-submit", ""},
	{"PermissionRequest", "permission-request", ""},
	{"Stop", "stop", ""},
}

var cursorActivityHooks = []activityHook{
	{"sessionStart", "session-start", ""},
	{"beforeSubmitPrompt", "user-prompt-submit", ""},
	{"stop", "stop", ""},
	{"beforeShellExecution", "permission-request", ""},
	{"beforeMCPExecution", "permission-request", ""},
}

func hookCommand(harness, event string) string {
	return cloudHookBinary + " hooks " + harness + " " + event
}

func installClaudeActivityHooks(settings map[string]any) {
	hooks := objectValue(settings, "hooks")
	for _, hook := range claudeActivityHooks {
		entry := map[string]any{
			"hooks": []any{map[string]any{
				"type":    "command",
				"command": hookCommand("claude-code", hook.event),
				"timeout": 5,
			}},
		}
		if hook.matcher != "" {
			entry["matcher"] = hook.matcher
		}
		appendJSONHook(
			hooks,
			hook.nativeEvent,
			entry,
			hookCommand("claude-code", hook.event),
		)
	}
}

func removeGlobalClaudeActivityHooks(settings map[string]any) {
	hooks, _ := settings["hooks"].(map[string]any)
	for event, value := range hooks {
		groups, _ := value.([]any)
		keptGroups := make([]any, 0, len(groups))
		for _, value := range groups {
			group, _ := value.(map[string]any)
			if group == nil {
				keptGroups = append(keptGroups, value)
				continue
			}
			commands, _ := group["hooks"].([]any)
			keptCommands := commands[:0]
			for _, candidate := range commands {
				entry, _ := candidate.(map[string]any)
				command, _ := entry["command"].(string)
				if strings.HasPrefix(
					command,
					cloudHookBinary+" hooks claude-code ",
				) {
					continue
				}
				keptCommands = append(keptCommands, candidate)
			}
			if len(keptCommands) == 0 && len(commands) > 0 {
				continue
			}
			group["hooks"] = keptCommands
			keptGroups = append(keptGroups, group)
		}
		if len(keptGroups) == 0 {
			delete(hooks, event)
		} else {
			hooks[event] = keptGroups
		}
	}
	if len(hooks) == 0 {
		delete(settings, "hooks")
	}
}

func installCursorActivityHooks(workspace string) error {
	path := filepath.Join(workspace, ".cursor", "hooks.json")
	if err := updateJSONFile(path, func(root map[string]any) {
		if _, ok := root["version"]; !ok {
			root["version"] = 1
		}
		hooks := objectValue(root, "hooks")
		for _, hook := range cursorActivityHooks {
			appendJSONHook(
				hooks,
				hook.nativeEvent,
				map[string]any{"command": hookCommand("cursor", hook.event)},
				hookCommand("cursor", hook.event),
			)
		}
	}); err != nil {
		return fmt.Errorf("install Cursor activity hooks: %w", err)
	}
	return nil
}

func codexActivityHookArgs() []string {
	args := make([]string, 0, len(codexActivityHooks)*2)
	for _, hook := range codexActivityHooks {
		command := strings.ReplaceAll(
			hookCommand("codex", hook.event),
			`"`,
			`\"`,
		)
		value := fmt.Sprintf(
			`hooks.%s=[{hooks=[{type="command",command="%s",timeout=5}]}]`,
			hook.nativeEvent,
			command,
		)
		args = append(args, "-c", value)
	}
	return args
}

func objectValue(parent map[string]any, key string) map[string]any {
	value, _ := parent[key].(map[string]any)
	if value == nil {
		value = map[string]any{}
		parent[key] = value
	}
	return value
}

func appendJSONHook(
	hooks map[string]any,
	event string,
	entry map[string]any,
	command string,
) {
	entries, _ := hooks[event].([]any)
	for _, candidate := range entries {
		if containsHookCommand(candidate, command) {
			return
		}
	}
	hooks[event] = append(entries, entry)
}

func containsHookCommand(value any, command string) bool {
	switch typed := value.(type) {
	case map[string]any:
		if typed["command"] == command {
			return true
		}
		for _, child := range typed {
			if containsHookCommand(child, command) {
				return true
			}
		}
	case []any:
		for _, child := range typed {
			if containsHookCommand(child, command) {
				return true
			}
		}
	}
	return false
}
