package qodercli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type decodedSettings struct {
	Permissions struct {
		TrustDirectories []string `json:"trustDirectories"`
	} `json:"permissions"`
	General struct {
		EnableAutoUpdate bool `json:"enableAutoUpdate"`
	} `json:"general"`
	Hooks map[string][]struct {
		Matcher *string `json:"matcher"`
		Hooks   []struct {
			Type    string            `json:"type"`
			Command string            `json:"command"`
			Timeout int               `json:"timeout"`
			Env     map[string]string `json:"env"`
		} `json:"hooks"`
	} `json:"hooks"`
}

func decodeSettings(t *testing.T, payload string) decodedSettings {
	t.Helper()
	var decoded decodedSettings
	// A malformed payload makes Qoder CLI exit before it starts, with nothing
	// AO can attribute, so the exact shape is worth asserting.
	if err := json.Unmarshal([]byte(payload), &decoded); err != nil {
		t.Fatalf("settings payload is not valid JSON: %v\n%s", err, payload)
	}
	return decoded
}

func TestSettingsJSONTrustsTheWorkspaceAndDisablesAutoUpdate(t *testing.T) {
	workspace := t.TempDir()
	payload, err := SettingsJSON(workspace)
	if err != nil {
		t.Fatal(err)
	}
	decoded := decodeSettings(t, payload)

	if decoded.General.EnableAutoUpdate {
		t.Fatal("auto-update must be off so the binary cannot change mid-session")
	}
	found := false
	for _, dir := range decoded.Permissions.TrustDirectories {
		if dir == workspace {
			found = true
		}
	}
	if !found {
		t.Fatalf("workspace %q is not trusted: %#v", workspace, decoded.Permissions.TrustDirectories)
	}
}

func TestSettingsJSONInstallsClaudeShapedHooksInSeconds(t *testing.T) {
	decoded := decodeSettings(t, mustSettings(t, "/work"))

	want := []string{
		"SessionStart", "UserPromptSubmit", "PreToolUse", "PostToolUse",
		"PostToolUseFailure", "PermissionRequest", "Stop", "Notification", "SessionEnd",
	}
	for _, event := range want {
		groups, ok := decoded.Hooks[event]
		if !ok || len(groups) != 1 || len(groups[0].Hooks) != 1 {
			t.Fatalf("event %q is not installed exactly once: %#v", event, decoded.Hooks[event])
		}
		entry := groups[0].Hooks[0]
		if entry.Type != "command" || !strings.HasPrefix(entry.Command, "ao hooks qodercli ") {
			t.Fatalf("event %q has an unexpected hook: %#v", event, entry)
		}
		// Seconds, not milliseconds: a 30000 here would be an 8-hour timeout.
		if entry.Timeout != 30 {
			t.Fatalf("event %q timeout = %d, want 30 seconds", event, entry.Timeout)
		}
		if len(entry.Env) != 0 {
			t.Fatalf("event %q pins env %#v; hooks inherit the pane environment instead", event, entry.Env)
		}
	}

	// SubagentStop would let a subagent's closing line overwrite the session
	// summary, and it carries no activity signal of its own.
	if _, ok := decoded.Hooks["SubagentStop"]; ok {
		t.Fatal("SubagentStop must not be installed")
	}

	matcher := decoded.Hooks["SessionStart"][0].Matcher
	if matcher == nil || *matcher != "startup|resume|clear|new|compact" {
		t.Fatalf("SessionStart matcher = %v, want Qoder CLI's own source list", matcher)
	}
}

func TestSettingsArgWritesUnderTheDataDir(t *testing.T) {
	dataDir := t.TempDir()
	arg, err := SettingsArg(dataDir, "ao-session-1", "/work")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(arg, dataDir) {
		t.Fatalf("settings arg = %q, want a path under the data dir", arg)
	}
	contents, err := os.ReadFile(arg)
	if err != nil {
		t.Fatal(err)
	}
	decodeSettings(t, string(contents))

	info, err := os.Stat(arg)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("settings file mode = %o, want 600", perm)
	}
	if filepath.Base(arg) != "settings.json" {
		t.Fatalf("unexpected settings file name %q", arg)
	}
}

func TestSettingsArgFallsBackToInlineJSON(t *testing.T) {
	arg, err := SettingsArg("", "ao-session-1", "/work")
	if err != nil {
		t.Fatal(err)
	}
	// Without a data dir there is nowhere AO-owned to write, so the payload
	// travels inline rather than failing the launch.
	decodeSettings(t, arg)
}

func TestChatSettingsJSONCarriesNoHooks(t *testing.T) {
	payload, err := ChatSettingsJSON()
	if err != nil {
		t.Fatal(err)
	}
	decoded := decodeSettings(t, payload)
	if len(decoded.Hooks) != 0 {
		t.Fatalf("chat must not install hooks, or every turn is reported twice: %#v", decoded.Hooks)
	}
	if decoded.General.EnableAutoUpdate {
		t.Fatal("auto-update must be off in chat too")
	}
}

func mustSettings(t *testing.T, workspace string) string {
	t.Helper()
	payload, err := SettingsJSON(workspace)
	if err != nil {
		t.Fatal(err)
	}
	return payload
}
