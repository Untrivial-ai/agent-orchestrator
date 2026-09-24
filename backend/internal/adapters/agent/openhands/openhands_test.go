package openhands

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/hooksjson"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// testPlugin skips binary resolution and the version probe.
func testPlugin() *Plugin {
	return &Plugin{resolvedBinary: "openhands", verifiedBinary: "openhands"}
}

func TestLaunchAndRestore(t *testing.T) {
	p := testPlugin()
	ctx := context.Background()
	cmd, err := p.GetLaunchCommand(ctx, ports.LaunchConfig{Prompt: "-fix this", NativeSessionID: "ignored", Permissions: ports.PermissionModeBypassPermissions})
	want := []string{"openhands", "--always-approve", "--task=-fix this"}
	if err != nil || !reflect.DeepEqual(cmd, want) {
		t.Fatalf("launch = %q, %v; want %q", cmd, err, want)
	}

	cmd, err = p.GetLaunchCommand(ctx, ports.LaunchConfig{})
	if err != nil || !reflect.DeepEqual(cmd, []string{"openhands"}) {
		t.Fatalf("promptless launch = %q, %v", cmd, err)
	}

	cmd, ok, err := p.GetRestoreCommand(ctx, ports.RestoreConfig{
		Session: ports.SessionRef{Metadata: map[string]string{ports.MetadataKeyAgentSessionID: " 0b6c1f5e-5a4e-4d8b-9c55-0d5c2b1f7a10 "}},
		Prompt:  "continue",
	})
	want = []string{"openhands", "--resume", "0b6c1f5e-5a4e-4d8b-9c55-0d5c2b1f7a10", "--task=continue"}
	if err != nil || !ok || !reflect.DeepEqual(cmd, want) {
		t.Fatalf("restore = %q, %v, %v; want %q", cmd, ok, err, want)
	}

	if _, ok, err := p.GetRestoreCommand(ctx, ports.RestoreConfig{}); ok || err != nil {
		t.Fatalf("missing identity: %v, %v", ok, err)
	}
}

func TestPermissions(t *testing.T) {
	for _, tc := range []struct {
		mode ports.PermissionMode
		want string
	}{
		{ports.PermissionModeDefault, ""},
		{ports.PermissionModeAcceptEdits, ""},
		{ports.PermissionModeAuto, "--llm-approve"},
		{ports.PermissionModeBypassPermissions, "--always-approve"},
		{"unknown", ""},
	} {
		t.Run(string(tc.mode), func(t *testing.T) {
			cmd, err := testPlugin().GetLaunchCommand(context.Background(), ports.LaunchConfig{Permissions: tc.mode})
			want := []string{"openhands"}
			if tc.want != "" {
				want = append(want, tc.want)
			}
			if err != nil || !reflect.DeepEqual(cmd, want) {
				t.Fatalf("got %q, %v; want %q", cmd, err, want)
			}
		})
	}
}

func TestRejectUnsupportedToolRestrictions(t *testing.T) {
	p := testPlugin()
	if _, err := p.GetLaunchCommand(context.Background(), ports.LaunchConfig{DisallowedTools: []string{"terminal"}}); err == nil {
		t.Fatal("silently ignored restricted tools")
	}
	restore := ports.RestoreConfig{
		Session:      ports.SessionRef{Metadata: map[string]string{ports.MetadataKeyAgentSessionID: "id"}},
		AllowedTools: []string{"file_editor"},
	}
	if _, _, err := p.GetRestoreCommand(context.Background(), restore); err == nil {
		t.Fatal("restore silently ignored restricted tools")
	}
}

func TestCancelledLaunch(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := testPlugin().GetLaunchCommand(ctx, ports.LaunchConfig{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
}

func TestLaunchVerifiesVersionOnce(t *testing.T) {
	calls := 0
	stubVersion(t, func(context.Context, string) ([]byte, error) {
		calls++
		return []byte("OpenHands CLI 1.16.0\n"), nil
	})
	p := &Plugin{resolvedBinary: "openhands"}
	for range 2 {
		if _, err := p.GetLaunchCommand(context.Background(), ports.LaunchConfig{}); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 1 {
		t.Fatalf("version probed %d times, want 1", calls)
	}
}

func TestLaunchRejectsOldVersion(t *testing.T) {
	stubVersion(t, func(context.Context, string) ([]byte, error) {
		return []byte("OpenHands CLI 1.11.0\n"), nil
	})
	p := &Plugin{resolvedBinary: "openhands"}
	if _, err := p.GetLaunchCommand(context.Background(), ports.LaunchConfig{}); err == nil {
		t.Fatal("launched an OpenHands CLI without hook support")
	}
	if p.verifiedBinary != "" {
		t.Fatal("cached a failed version check")
	}
}

func TestSupportedVersion(t *testing.T) {
	for _, tc := range []struct {
		output string
		want   bool
	}{
		{"OpenHands CLI 1.12.0", true},
		{"OpenHands CLI 1.16.0\r\n", true},
		{"OpenHands CLI 2.0.0", true},
		{"OpenHands CLI 1.11.9", false},
		{"OpenHands CLI 0.99.0", false},
		// The SDK banner carries its own, higher version; it must not count.
		{"OpenHands SDK v1.21.0", false},
		{"1.16.0", false},
		{"", false},
	} {
		t.Run(tc.output, func(t *testing.T) {
			if got := supportedVersion(tc.output); got != tc.want {
				t.Fatalf("supportedVersion(%q) = %v, want %v", tc.output, got, tc.want)
			}
		})
	}
}

func stubVersion(t *testing.T, fn func(context.Context, string) ([]byte, error)) {
	t.Helper()
	orig := versionCommand
	versionCommand = fn
	t.Cleanup(func() { versionCommand = orig })
}

func TestSessionInfo(t *testing.T) {
	info, ok, err := testPlugin().SessionInfo(context.Background(), ports.SessionRef{Metadata: map[string]string{ports.MetadataKeyAgentSessionID: "native"}})
	if err != nil || !ok || info.AgentSessionID != "native" {
		t.Fatalf("got %+v, %v, %v", info, ok, err)
	}
	if _, ok, _ := testPlugin().SessionInfo(context.Background(), ports.SessionRef{}); ok {
		t.Fatal("reported session info without metadata")
	}
}

func readHooks(t *testing.T, path string) map[string]json.RawMessage {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		t.Fatalf("parse %s: %v", data, err)
	}
	return top
}

func hookGroups(t *testing.T, top map[string]json.RawMessage) map[string][]hooksjson.MatcherGroup {
	t.Helper()
	var groups map[string][]hooksjson.MatcherGroup
	if err := json.Unmarshal(top["hooks"], &groups); err != nil {
		t.Fatalf("parse hooks: %v", err)
	}
	return groups
}

func writeHooks(t *testing.T, ws, content string) string {
	t.Helper()
	path := hooksPath(ws)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestHooksInstallIdempotentAndUninstall(t *testing.T) {
	ws := t.TempDir()
	p := New()
	ctx := context.Background()
	for range 2 {
		if err := p.GetAgentHooks(ctx, ports.WorkspaceHookConfig{WorkspacePath: ws}); err != nil {
			t.Fatal(err)
		}
	}
	groups := hookGroups(t, readHooks(t, hooksPath(ws)))
	for _, spec := range managedHooks {
		if len(groups[spec.Event]) != 1 || len(groups[spec.Event][0].Hooks) != 1 {
			t.Fatalf("%s = %+v, want one AO hook", spec.Event, groups[spec.Event])
		}
		hook := groups[spec.Event][0].Hooks[0]
		if hook.Command != spec.Command || hook.Timeout != hookTimeout {
			t.Fatalf("%s hook = %+v", spec.Event, hook)
		}
	}
	if ok, err := p.AreHooksInstalled(ctx, ws); err != nil || !ok {
		t.Fatalf("installed = %v, %v", ok, err)
	}
	if err := p.UninstallHooks(ctx, ws); err != nil {
		t.Fatal(err)
	}
	if ok, err := p.AreHooksInstalled(ctx, ws); err != nil || ok {
		t.Fatalf("after uninstall = %v, %v", ok, err)
	}
}

// OpenHands' own repo ships a direct-format file. Adding a "hooks" wrapper next
// to it would make OpenHands drop the user's "stop" hook.
func TestHooksMigrateDirectFormat(t *testing.T) {
	ws := t.TempDir()
	path := writeHooks(t, ws, `{"stop":[{"matcher":"*","hooks":[{"type":"command","command":"user-stop.sh","timeout":300}]}],"extra":true}`)
	if err := New().GetAgentHooks(context.Background(), ports.WorkspaceHookConfig{WorkspacePath: ws}); err != nil {
		t.Fatal(err)
	}
	top := readHooks(t, path)
	if _, ok := top["stop"]; ok {
		t.Fatalf("direct-format key left at top level: %s", top["stop"])
	}
	if string(top["extra"]) != "true" {
		t.Fatalf("dropped unrelated top-level key: %v", top)
	}
	stop := hookGroups(t, top)["Stop"]
	if len(stop) != 2 || stop[0].Hooks[0].Command != "user-stop.sh" || stop[0].Hooks[0].Timeout != 300 {
		t.Fatalf("Stop = %+v, want user group first then AO", stop)
	}
	if stop[1].Hooks[0].Command != hookCommandPrefix+"stop" {
		t.Fatalf("AO stop hook = %+v", stop[1])
	}

	// Uninstall leaves the user's hook in the migrated form.
	if err := New().UninstallHooks(context.Background(), ws); err != nil {
		t.Fatal(err)
	}
	stop = hookGroups(t, readHooks(t, path))["Stop"]
	if len(stop) != 1 || stop[0].Hooks[0].Command != "user-stop.sh" {
		t.Fatalf("after uninstall Stop = %+v", stop)
	}
}

// OpenHands rejects a file that spells one event twice, disabling every hook.
func TestHooksMergeMixedSpellingsInsideWrapper(t *testing.T) {
	ws := t.TempDir()
	path := writeHooks(t, ws, `{"hooks":{"session_start":[{"hooks":[{"type":"command","command":"user-start"}]}],"Custom":[1]}}`)
	if err := New().GetAgentHooks(context.Background(), ports.WorkspaceHookConfig{WorkspacePath: ws}); err != nil {
		t.Fatal(err)
	}
	var events map[string]json.RawMessage
	if err := json.Unmarshal(readHooks(t, path)["hooks"], &events); err != nil {
		t.Fatal(err)
	}
	if _, ok := events["session_start"]; ok {
		t.Fatal("snake_case spelling survived next to PascalCase")
	}
	var custom []int
	if err := json.Unmarshal(events["Custom"], &custom); err != nil || !reflect.DeepEqual(custom, []int{1}) {
		t.Fatalf("dropped unknown wrapper key: %s", events["Custom"])
	}
	var start []hooksjson.MatcherGroup
	if err := json.Unmarshal(events["SessionStart"], &start); err != nil {
		t.Fatal(err)
	}
	if len(start) != 1 || len(start[0].Hooks) != 2 || start[0].Hooks[0].Command != "user-start" {
		t.Fatalf("SessionStart = %+v", start)
	}
}

func TestNormalizeLeavesCanonicalFileUntouched(t *testing.T) {
	ws := t.TempDir()
	content := `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"x"}]}]}}`
	path := writeHooks(t, ws, content)
	if err := normalizeHooksFile(path); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != content {
		t.Fatalf("rewrote canonical file: %s, %v", data, err)
	}
}

func TestNormalizeRejectsInvalidJSON(t *testing.T) {
	ws := t.TempDir()
	path := writeHooks(t, ws, `{not json`)
	if err := New().GetAgentHooks(context.Background(), ports.WorkspaceHookConfig{WorkspacePath: ws}); err == nil {
		t.Fatal("installed hooks over an unparsable user file")
	}
	if data, _ := os.ReadFile(path); string(data) != `{not json` {
		t.Fatalf("modified unparsable file: %s", data)
	}
}

func TestSettingsAuthStatus(t *testing.T) {
	dir := t.TempDir()
	write := func(content string) string {
		path := filepath.Join(t.TempDir(), "agent_settings.json")
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	for _, tc := range []struct {
		name string
		path string
		want ports.AgentAuthStatus
	}{
		{"missing", filepath.Join(dir, "missing.json"), ports.AgentAuthStatusUnauthorized},
		{"no model", write(`{"llm":{"api_key":"k"}}`), ports.AgentAuthStatusUnauthorized},
		{"corrupt", write(`{`), ports.AgentAuthStatusUnknown},
		{"local model without key", write(`{"llm":{"model":"ollama/qwen3","base_url":"http://localhost:11434"}}`), ports.AgentAuthStatusAuthorized},
		{"no path", "", ports.AgentAuthStatusUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := settingsAuthStatus(tc.path)
			if err != nil || got != tc.want {
				t.Fatalf("got %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

func TestSettingsPathHonorsPersistenceDir(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("OPENHANDS_PERSISTENCE_DIR", dir)
	if got, want := settingsPath(), filepath.Join(dir, "agent_settings.json"); got != want {
		t.Fatalf("settingsPath = %q, want %q", got, want)
	}
}
