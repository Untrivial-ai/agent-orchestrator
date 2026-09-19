package opencode

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/hookutil"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestOpenCodeLocalEvidenceIsConfigured(t *testing.T) {
	for _, source := range []string{"environment", "auth file", "auth list"} {
		t.Run(source, func(t *testing.T) {
			plugin := openCodeAuthFixture(t, "0 credentials\n")
			switch source {
			case "environment":
				t.Setenv("OPENAI_API_KEY", "secret")
			case "auth file":
				writeOpenCodeAuthFile(t, "{\"openai\":{\"type\":\"api\",\"key\":\"secret\"}}")
			case "auth list":
				plugin = openCodeAuthFixture(t, "OpenAI api\n1 credential\n")
			}
			got, err := plugin.AuthStatus(context.Background())
			if err != nil || got != ports.AgentAuthStatusConfigured {
				t.Fatalf("AuthStatus = %q, %v; want configured", got, err)
			}
		})
	}
}

func TestOpenCodeScopedProviderEvidence(t *testing.T) {
	for _, tc := range []struct {
		name, model, content string
		env                  map[string]string
		want                 ports.AgentAuthStatus
	}{
		{"matching key", "openai/gpt-5", "", map[string]string{"OPENAI_API_KEY": "key"}, ports.AgentAuthStatusConfigured},
		{"unrelated key", "anthropic/claude", "", map[string]string{"OPENAI_API_KEY": "key"}, ports.AgentAuthStatusUnknown},
		{"matching file", "anthropic/claude", "{\"anthropic\":{\"type\":\"api\",\"key\":\"secret\"}}", nil, ports.AgentAuthStatusConfigured},
		{"unrelated file", "openai/gpt-5", "{\"anthropic\":{\"type\":\"api\",\"key\":\"secret\"}}", nil, ports.AgentAuthStatusUnknown},
		{"empty key", "openai/gpt-5", "{\"openai\":{\"type\":\"api\",\"key\":\" \"}}", nil, ports.AgentAuthStatusUnknown},
		{"arbitrary object", "openai/gpt-5", "{\"openai\":{\"token\":\"secret\"}}", nil, ports.AgentAuthStatusUnknown},
		{"malformed entry with valid sibling", "openai/gpt-5", "{\"broken\":7,\"openai\":{\"type\":\"api\",\"key\":\"secret\"}}", nil, ports.AgentAuthStatusConfigured},
		{"malformed", "openai/gpt-5", "{", nil, ports.AgentAuthStatusUnknown},
		{"expired oauth", "openai/gpt-5", "{\"openai\":{\"type\":\"oauth\",\"access\":\"secret\",\"refresh\":\"\",\"expires\":1}}", nil, ports.AgentAuthStatusUnknown},
		{"refreshable oauth", "openai/gpt-5", "{\"openai\":{\"type\":\"oauth\",\"access\":\"old\",\"refresh\":\"refresh\",\"expires\":1}}", nil, ports.AgentAuthStatusConfigured},
		{"local", "ollama/llama3", "", map[string]string{"OPENAI_API_KEY": "unrelated"}, ports.AgentAuthStatusNotApplicable},
		{"free", "opencode/big-pickle", "", nil, ports.AgentAuthStatusNotApplicable},
		{"paid zen", "opencode/claude-opus-4-6", "", nil, ports.AgentAuthStatusUnknown},
		{"free OpenRouter still needs auth", "openrouter/minimax/minimax-m2.5:free", "", nil, ports.AgentAuthStatusUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := openCodeAuthFixture(t, "0 credentials\n")
			if tc.content != "" {
				writeOpenCodeAuthFile(t, tc.content)
			}
			checker, ok := any(p).(ports.AgentScopedAuthChecker)
			if !ok {
				t.Fatal("OpenCode does not implement scoped auth")
			}
			got, err := checker.AuthStatusFor(context.Background(), ports.AgentAuthCheck{Config: ports.AgentConfig{Model: tc.model}, Env: tc.env})
			if err != nil || got != tc.want {
				t.Fatalf("AuthStatusFor = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

func TestOpenCodeBedrockProfiles(t *testing.T) {
	for _, profile := range []string{"default", "work"} {
		t.Run(profile, func(t *testing.T) {
			p := openCodeAuthFixture(t, "0 credentials\n")
			dir := filepath.Join(os.Getenv("HOME"), ".aws")
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "credentials"), []byte("["+profile+"]\naws_access_key_id = id\naws_secret_access_key = secret\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if profile != "default" {
				t.Setenv("AWS_PROFILE", profile)
			}
			checker, ok := any(p).(ports.AgentScopedAuthChecker)
			if !ok {
				t.Fatal("OpenCode does not implement scoped auth")
			}
			got, err := checker.AuthStatusFor(context.Background(), ports.AgentAuthCheck{Config: ports.AgentConfig{Model: "amazon-bedrock/claude"}})
			if err != nil || got != ports.AgentAuthStatusConfigured {
				t.Fatalf("status = %q, %v; want configured", got, err)
			}
			got, err = checker.AuthStatusFor(context.Background(), ports.AgentAuthCheck{Config: ports.AgentConfig{Model: "openai/gpt-5"}})
			if err != nil || got != ports.AgentAuthStatusUnknown {
				t.Fatalf("unrelated AWS status = %q, %v; want unknown", got, err)
			}
		})
	}
}

func TestOpenCodeOfficialSourcesIgnoreGuessedDatabase(t *testing.T) {
	p := openCodeAuthFixture(t, "0 credentials\n")
	dir := filepath.Join(os.Getenv("HOME"), ".local", "share", "opencode")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, "opencode.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE TABLE account_state (active_account_id text); INSERT INTO account_state VALUES ('dangling')"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	got, err := p.AuthStatus(context.Background())
	if err != nil || got != ports.AgentAuthStatusUnknown {
		t.Fatalf("status = %q, %v; want unknown", got, err)
	}
	writeOpenCodeAuthFile(t, "{\"anthropic\":{\"type\":\"api\",\"key\":\"official\"}}")
	got, err = p.AuthStatus(context.Background())
	if err != nil || got != ports.AgentAuthStatusConfigured {
		t.Fatalf("official status = %q, %v; want configured", got, err)
	}
}

func TestOpenCodeExplicitDatabaseEvidence(t *testing.T) {
	for _, tc := range []struct {
		name, access, refresh string
		expiry                int64
		active                string
		want                  ports.AgentAuthStatus
	}{
		{"valid", "token", "", 4102444800000, "acct", ports.AgentAuthStatusConfigured},
		{"dangling", "token", "", 4102444800000, "other", ports.AgentAuthStatusUnknown},
		{"empty", "", "", 4102444800000, "acct", ports.AgentAuthStatusUnknown},
		{"expired", "token", "", 1, "acct", ports.AgentAuthStatusUnknown},
		{"refreshable", "token", "refresh", 1, "acct", ports.AgentAuthStatusConfigured},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "auth.db")
			db, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec("CREATE TABLE account(id text, access_token text, refresh_token text, token_expiry integer); CREATE TABLE account_state(active_account_id text)"); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec("INSERT INTO account VALUES ('acct', ?, ?, ?)", tc.access, tc.refresh, tc.expiry); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec("INSERT INTO account_state VALUES (?)", tc.active); err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			got, _, err := opencodeDBAuthStatus(context.Background(), path)
			if err != nil || got != tc.want {
				t.Fatalf("status = %q, %v; want %q", got, err, tc.want)
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, after) {
				t.Fatal("auth probe changed database")
			}
		})
	}
}

func TestOpenCodeAuthListIsConservative(t *testing.T) {
	for _, tc := range []struct {
		output string
		want   ports.AgentAuthStatus
	}{
		{"0 credentials\n", ports.AgentAuthStatusUnknown},
		{"No credentials found\n", ports.AgentAuthStatusUnknown},
		{"credential service unavailable\n", ports.AgentAuthStatusUnknown},
		{"0 credentials\nOpenAI OPENAI_API_KEY\n1 environment variable\n", ports.AgentAuthStatusConfigured},
	} {
		t.Run(tc.output, func(t *testing.T) {
			got, err := openCodeAuthFixture(t, tc.output).AuthStatus(context.Background())
			if err != nil || got != tc.want {
				t.Fatalf("status = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

func openCodeAuthFixture(t *testing.T, output string) *Plugin {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	for _, name := range append(append([]string{}, opencodeAPIKeyEnvVars...), "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_BEARER_TOKEN_BEDROCK", "AWS_PROFILE", "AWS_DEFAULT_PROFILE", "AWS_SHARED_CREDENTIALS_FILE", "AWS_CONFIG_FILE", "AWS_ROLE_ARN", "AWS_WEB_IDENTITY_TOKEN_FILE", "OPENCODE_DATA_DIR", "XDG_DATA_HOME", "XDG_CONFIG_HOME", "OPENCODE_CONFIG", "OPENCODE_CONFIG_CONTENT") {
		t.Setenv(name, "")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	binary := filepath.Join(t.TempDir(), "opencode")
	script := "#!/bin/sh\nif [ \"$1\" = auth ] && [ \"$2\" = list ]; then\nprintf '%s' '" + strings.ReplaceAll(output, "'", "'\\''") + "'\nelse\nexit 1\nfi\n"
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return &Plugin{resolvedBinary: binary}
}

func writeOpenCodeAuthFile(t *testing.T, content string) {
	t.Helper()
	dir := filepath.Join(os.Getenv("HOME"), ".local", "share", "opencode")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "auth.json"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestResolveOpenCodeBinaryFallback(t *testing.T) {
	bin, err := ResolveOpenCodeBinary(context.Background())
	if err != nil {
		if !errors.Is(err, ports.ErrAgentBinaryNotFound) {
			t.Fatalf("err = %v, want ports.ErrAgentBinaryNotFound", err)
		}
		return
	}
	if bin == "" {
		t.Fatal("ResolveOpenCodeBinary returned empty path with no error")
	}
}

func TestResolveOpenCodeBinaryFallbacks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix fallback candidate shape")
	}
	oldUnixPaths := opencodeUnixPaths
	opencodeUnixPaths = nil
	t.Cleanup(func() { opencodeUnixPaths = oldUnixPaths })

	tests := []struct {
		name string
		seed func(t *testing.T, home string) string
	}{
		{
			name: "npm global",
			seed: func(t *testing.T, home string) string {
				return writeOpenCodeExecutable(t, filepath.Join(home, ".npm-global", "bin", "opencode"))
			},
		},
		{
			name: "nvm",
			seed: func(t *testing.T, home string) string {
				return writeOpenCodeExecutable(t, filepath.Join(home, ".nvm", "versions", "node", "v22.23.1", "bin", "opencode"))
			},
		},
		{
			name: "mise shim",
			seed: func(t *testing.T, home string) string {
				return writeOpenCodeExecutable(t, filepath.Join(home, ".local", "share", "mise", "shims", "opencode"))
			},
		},
		{
			name: "bun global",
			seed: func(t *testing.T, home string) string {
				return writeOpenCodeExecutable(t, filepath.Join(home, ".bun", "bin", "opencode"))
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("PATH", t.TempDir())
			t.Setenv("VOLTA_HOME", filepath.Join(home, ".volta"))
			t.Setenv("FNM_DIR", "")
			want := tt.seed(t, home)

			got, err := ResolveOpenCodeBinary(context.Background())
			if err != nil {
				t.Fatalf("ResolveOpenCodeBinary: %v", err)
			}
			if got != want {
				t.Fatalf("ResolveOpenCodeBinary = %q, want %q", got, want)
			}
		})
	}
}

func writeOpenCodeExecutable(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestResolveOpenCodeBinaryContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := ResolveOpenCodeBinary(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("ResolveOpenCodeBinary err = %v, want context.Canceled", err)
	}
}

func TestGetLaunchCommandBuildsArgv(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "opencode"}
	promptFile := filepath.Join(t.TempDir(), "system.md")

	cmd, err := plugin.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		Permissions:      ports.PermissionModeBypassPermissions,
		Prompt:           "-fix this",
		SessionID:        "sess/1",
		SystemPromptFile: promptFile,
		SystemPrompt:     "follow AO rules",
	})
	if err != nil {
		t.Fatal(err)
	}

	configPath := filepath.Join(filepath.Dir(promptFile), "opencode.json")
	want := []string{
		"env", "OPENCODE_CONFIG=" + configPath,
		"opencode",
		"--dangerously-skip-permissions",
		"--agent", "ao-sess-1",
		"--prompt", "-fix this",
	}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("unexpected command\nwant: %#v\n got: %#v", want, cmd)
	}
	var config opencodeInlineConfig
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	agent := config.Agent["ao-sess-1"]
	if agent.Mode != "primary" || agent.Prompt != "follow AO rules" {
		t.Fatalf("agent config = %#v, want primary inline prompt", agent)
	}
}

func TestGetLaunchCommandSystemPromptFileConfig(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "opencode"}
	promptFile := filepath.Join(t.TempDir(), "system.md")

	cmd, err := plugin.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		SessionID:        "sess-2",
		SystemPromptFile: promptFile,
	})
	if err != nil {
		t.Fatal(err)
	}

	configPath := filepath.Join(filepath.Dir(promptFile), "opencode.json")
	want := []string{"env", "OPENCODE_CONFIG=" + configPath, "opencode", "--agent", "ao-sess-2"}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("unexpected command\nwant: %#v\n got: %#v", want, cmd)
	}
	var config opencodeInlineConfig
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	if got := config.Agent["ao-sess-2"].Prompt; got != "{file:./system.md}" {
		t.Fatalf("agent prompt = %q, want file reference", got)
	}
}

func TestGetLaunchCommandMapsPermissionModes(t *testing.T) {
	tests := []struct {
		name        string
		permission  ports.PermissionMode
		wantFlag    bool
		notExpected string
	}{
		{name: "default", permission: ports.PermissionModeDefault, notExpected: "--dangerously-skip-permissions"},
		{name: "accept-edits", permission: ports.PermissionModeAcceptEdits, notExpected: "--dangerously-skip-permissions"},
		{name: "auto", permission: ports.PermissionModeAuto, notExpected: "--dangerously-skip-permissions"},
		{name: "bypass-permissions", permission: ports.PermissionModeBypassPermissions, wantFlag: true},
		{name: "empty", permission: "", notExpected: "--dangerously-skip-permissions"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plugin := &Plugin{resolvedBinary: "opencode"}
			cmd, err := plugin.GetLaunchCommand(context.Background(), ports.LaunchConfig{Permissions: tt.permission})
			if err != nil {
				t.Fatal(err)
			}
			has := contains(cmd, "--dangerously-skip-permissions")
			if tt.wantFlag && !has {
				t.Fatalf("command %#v missing --dangerously-skip-permissions", cmd)
			}
			if tt.notExpected != "" && has {
				t.Fatalf("command %#v contains %q", cmd, tt.notExpected)
			}
		})
	}
}

func TestGetPromptDeliveryStrategyIsInCommand(t *testing.T) {
	plugin := &Plugin{}

	got, err := plugin.GetPromptDeliveryStrategy(context.Background(), ports.LaunchConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if got != ports.PromptDeliveryInCommand {
		t.Fatalf("unexpected strategy: %q", got)
	}
}

func TestGetConfigSpecReportsModel(t *testing.T) {
	plugin := &Plugin{}

	spec, err := plugin.GetConfigSpec(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(spec.Fields) != 1 || spec.Fields[0].Key != "model" {
		t.Fatalf("unexpected config fields: %#v", spec.Fields)
	}
}

func TestGetLaunchCommandForwardsModel(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "opencode"}
	cmd, err := plugin.GetLaunchCommand(context.Background(), ports.LaunchConfig{Config: ports.AgentConfig{Model: "  anthropic/claude-sonnet  "}})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"opencode", "--model", "anthropic/claude-sonnet"}; !reflect.DeepEqual(cmd, want) {
		t.Fatalf("cmd = %#v, want %#v", cmd, want)
	}
}

func TestGetAgentHooksInstallsPlugin(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "opencode"}
	workspace := t.TempDir()

	// A user's own plugin in the same dir must survive AO's install untouched.
	pluginDir := filepath.Dir(opencodePluginPath(workspace))
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		t.Fatal(err)
	}
	userPlugin := filepath.Join(pluginDir, "user.js")
	userBody := []byte("export const userPlugin = async () => ({})\n")
	if err := os.WriteFile(userPlugin, userBody, 0o644); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	cfg := ports.WorkspaceHookConfig{DataDir: t.TempDir(), SessionID: "sess-1", WorkspacePath: workspace}
	if err := plugin.GetAgentHooks(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	// A second install must be idempotent (overwrite with identical content).
	if err := plugin.GetAgentHooks(ctx, cfg); err != nil {
		t.Fatal(err)
	}

	if installed, err := plugin.AreHooksInstalled(ctx, workspace); err != nil || !installed {
		t.Fatalf("AreHooksInstalled after install = (%v, %v), want (true, nil)", installed, err)
	}

	data, err := os.ReadFile(opencodePluginPath(workspace))
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	if !strings.Contains(body, opencodePluginSentinel) {
		t.Fatalf("installed plugin missing AO sentinel:\n%s", body)
	}
	// Every normalized activity event must be wired via `ao hooks opencode <event>`.
	for _, event := range opencodeManagedEvents {
		want := opencodeHookCommandPrefix + event
		if !strings.Contains(body, want) {
			t.Fatalf("installed plugin missing hook command %q:\n%s", want, body)
		}
	}
	// The opencode-native lifecycle events the plugin subscribes to. Stop maps
	// to session.status(idle) — NOT the deprecated session.idle — and the user
	// prompt is detected from message.updated/message.part.updated.
	for _, marker := range []string{"session.created", "message.updated", "message.part.updated", "session.status"} {
		if !strings.Contains(body, marker) {
			t.Fatalf("installed plugin missing opencode event %q:\n%s", marker, body)
		}
	}
	// Tool execution is exposed as named plugin hooks. Permission approvals and
	// explicit questions are emitted through opencode's generic event callback.
	for _, hook := range []string{`"tool.execute.before":`, `"tool.execute.after":`} {
		if !strings.Contains(body, hook) {
			t.Fatalf("installed plugin missing opencode hook %q:\n%s", hook, body)
		}
	}
	for _, eventCase := range []string{`case "permission.asked":`, `case "permission.replied":`, `case "question.asked":`, `case "question.replied":`, `case "question.rejected":`} {
		if !strings.Contains(body, eventCase) {
			t.Fatalf("installed plugin missing opencode event handler %q:\n%s", eventCase, body)
		}
	}
	for _, unsupported := range []string{`"client.permissionRequest"`, `"permission.ask":`, `"tool.start"`, `"tool.end"`} {
		if strings.Contains(body, unsupported) {
			t.Fatalf("plugin subscribes to unsupported opencode event %q:\n%s", unsupported, body)
		}
	}
	// The plugin must carry the runtime launch id in hook payloads so the CLI
	// can fence signals even when child-process env inheritance is trimmed.
	if !strings.Contains(body, "launch_id:") {
		t.Fatalf("installed plugin missing launch_id in hook payload:\n%s", body)
	}
	if !strings.Contains(body, "AO_RUNTIME_LAUNCH_ID") {
		t.Fatalf("installed plugin missing AO_RUNTIME_LAUNCH_ID reference:\n%s", body)
	}
	// Guard against regressing back to subscribing to the deprecated/unreliable
	// session.idle event (the quoted event string is how a `case` would name it;
	// the explanatory comment mentions it unquoted, which is fine).
	if strings.Contains(body, `"session.idle"`) {
		t.Fatalf("plugin subscribes to deprecated session.idle; use session.status(idle):\n%s", body)
	}
	// A hung `ao hooks` call must not block opencode forever, so each spawn is
	// time-boxed (parity with the claude/codex 30s hook timeout).
	if !strings.Contains(body, "timeout:") {
		t.Fatalf("plugin spawn has no timeout; a hung hook would block opencode:\n%s", body)
	}

	// The user's plugin is untouched.
	got, err := os.ReadFile(userPlugin)
	if err != nil {
		t.Fatalf("user plugin removed by install: %v", err)
	}
	if !reflect.DeepEqual(got, userBody) {
		t.Fatalf("user plugin modified by install: %q", got)
	}

	// using-ao must land where opencode's skill tool discovers project skills.
	skillMD := filepath.Join(opencodeSkillDir(workspace), "SKILL.md")
	skillBody, err := os.ReadFile(skillMD)
	if err != nil {
		t.Fatalf("using-ao SKILL.md missing after install: %v", err)
	}
	if !strings.Contains(string(skillBody), "name: using-ao") {
		t.Fatalf("installed skill missing using-ao frontmatter:\n%s", skillBody)
	}
	if managed, err := isAOManagedSkill(workspace); err != nil || !managed {
		t.Fatalf("isAOManagedSkill after install = (%v, %v), want (true, nil)", managed, err)
	}
	if _, err := os.Stat(filepath.Join(opencodeSkillDir(workspace), "commands", "spawn.md")); err != nil {
		t.Fatalf("using-ao commands/spawn.md missing after install: %v", err)
	}
}

func TestGetAgentHooksRefusesToClobberForeignFile(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "opencode"}
	workspace := t.TempDir()
	ctx := context.Background()

	// A non-AO file occupying AO's exact path must NOT be silently overwritten.
	pluginPath := opencodePluginPath(workspace)
	if err := os.MkdirAll(filepath.Dir(pluginPath), 0o755); err != nil {
		t.Fatal(err)
	}
	foreign := []byte("export const notOurs = async () => ({})\n")
	if err := os.WriteFile(pluginPath, foreign, 0o644); err != nil {
		t.Fatal(err)
	}

	err := plugin.GetAgentHooks(ctx, ports.WorkspaceHookConfig{WorkspacePath: workspace})
	if err == nil {
		t.Fatal("GetAgentHooks overwrote a non-AO file; want a loud error")
	}
	got, readErr := os.ReadFile(pluginPath)
	if readErr != nil {
		t.Fatalf("foreign file removed by refused install: %v", readErr)
	}
	if !reflect.DeepEqual(got, foreign) {
		t.Fatalf("foreign file modified by refused install: %q", got)
	}
}

func TestUninstallHooksRemovesPlugin(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "opencode"}
	workspace := t.TempDir()
	ctx := context.Background()
	cfg := ports.WorkspaceHookConfig{DataDir: t.TempDir(), SessionID: "sess-1", WorkspacePath: workspace}

	// Pre-seed a user's own plugin; it must survive uninstall.
	pluginDir := filepath.Dir(opencodePluginPath(workspace))
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		t.Fatal(err)
	}
	userPlugin := filepath.Join(pluginDir, "user.js")
	if err := os.WriteFile(userPlugin, []byte("export const userPlugin = async () => ({})\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := plugin.GetAgentHooks(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if installed, err := plugin.AreHooksInstalled(ctx, workspace); err != nil || !installed {
		t.Fatalf("AreHooksInstalled after install = (%v, %v), want (true, nil)", installed, err)
	}

	if err := plugin.UninstallHooks(ctx, workspace); err != nil {
		t.Fatal(err)
	}
	if installed, err := plugin.AreHooksInstalled(ctx, workspace); err != nil || installed {
		t.Fatalf("AreHooksInstalled after uninstall = (%v, %v), want (false, nil)", installed, err)
	}
	if _, err := os.Stat(opencodePluginPath(workspace)); !os.IsNotExist(err) {
		t.Fatalf("AO plugin still present after uninstall: err=%v", err)
	}
	if _, err := os.Stat(opencodeSkillDir(workspace)); !os.IsNotExist(err) {
		t.Fatalf("AO using-ao skill still present after uninstall: err=%v", err)
	}
	if _, err := os.Stat(opencodeSkillMarkerPath(workspace)); !os.IsNotExist(err) {
		t.Fatalf("AO skill marker still present after uninstall: err=%v", err)
	}
	if _, err := os.Stat(userPlugin); err != nil {
		t.Fatalf("user plugin removed by uninstall: %v", err)
	}
}

func TestGetAgentHooksRecoversPartialSkillInstall(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "opencode"}
	workspace := t.TempDir()
	ctx := context.Background()

	skillDir := opencodeSkillDir(workspace)
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Simulate a crash after the marker was written but before Materialize finished.
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("# partial\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := hookutil.AtomicWriteFile(opencodeSkillMarkerPath(workspace), []byte(opencodeSkillSentinel+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := plugin.GetAgentHooks(ctx, ports.WorkspaceHookConfig{WorkspacePath: workspace}); err != nil {
		t.Fatalf("GetAgentHooks: %v", err)
	}
	if _, err := os.Stat(filepath.Join(skillDir, "commands", "spawn.md")); err != nil {
		t.Fatalf("recovered install missing commands/spawn.md: %v", err)
	}
}

func TestGetAgentHooksRefusesToClobberForeignSkill(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "opencode"}
	workspace := t.TempDir()
	ctx := context.Background()

	skillDir := opencodeSkillDir(workspace)
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	foreignSkill := []byte("---\nname: using-ao\ndescription: user owned\n---\n# mine\n")
	foreignPath := filepath.Join(skillDir, "SKILL.md")
	if err := os.WriteFile(foreignPath, foreignSkill, 0o644); err != nil {
		t.Fatal(err)
	}

	err := plugin.GetAgentHooks(ctx, ports.WorkspaceHookConfig{WorkspacePath: workspace})
	if err == nil {
		t.Fatal("GetAgentHooks overwrote a non-AO skill; want a loud error")
	}
	got, readErr := os.ReadFile(foreignPath)
	if readErr != nil {
		t.Fatalf("foreign skill removed by refused install: %v", readErr)
	}
	if !reflect.DeepEqual(got, foreignSkill) {
		t.Fatalf("foreign skill modified by refused install: %q", got)
	}
}

func TestUninstallHooksLeavesForeignSkill(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "opencode"}
	workspace := t.TempDir()
	ctx := context.Background()

	skillDir := opencodeSkillDir(workspace)
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	foreignSkill := []byte("---\nname: using-ao\ndescription: user owned\n---\n# mine\n")
	foreignPath := filepath.Join(skillDir, "SKILL.md")
	if err := os.WriteFile(foreignPath, foreignSkill, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := plugin.UninstallHooks(ctx, workspace); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(foreignPath)
	if err != nil {
		t.Fatalf("foreign skill removed by uninstall: %v", err)
	}
	if !reflect.DeepEqual(got, foreignSkill) {
		t.Fatalf("foreign skill modified by uninstall: %q", got)
	}
}

func TestUninstallHooksLeavesForeignFile(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "opencode"}
	workspace := t.TempDir()
	ctx := context.Background()

	// A non-AO file occupying AO's filename must NOT be deleted by uninstall.
	pluginPath := opencodePluginPath(workspace)
	if err := os.MkdirAll(filepath.Dir(pluginPath), 0o755); err != nil {
		t.Fatal(err)
	}
	foreign := []byte("export const notOurs = async () => ({})\n")
	if err := os.WriteFile(pluginPath, foreign, 0o644); err != nil {
		t.Fatal(err)
	}

	if installed, err := plugin.AreHooksInstalled(ctx, workspace); err != nil || installed {
		t.Fatalf("AreHooksInstalled on foreign file = (%v, %v), want (false, nil)", installed, err)
	}
	if err := plugin.UninstallHooks(ctx, workspace); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(pluginPath)
	if err != nil {
		t.Fatalf("foreign file removed by uninstall: %v", err)
	}
	if !reflect.DeepEqual(got, foreign) {
		t.Fatalf("foreign file modified by uninstall: %q", got)
	}
}

func TestGetRestoreCommandReadsAgentSessionID(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "opencode"}

	cmd, ok, err := plugin.GetRestoreCommand(context.Background(), ports.RestoreConfig{
		Permissions: ports.PermissionModeBypassPermissions,
		Session: ports.SessionRef{
			Metadata: map[string]string{opencodeAgentSessionIDMetadataKey: "ses_abc123"},
		},
	})
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if !ok {
		t.Fatal("ok = false, want true")
	}
	want := []string{
		"opencode",
		"--dangerously-skip-permissions",
		"--session", "ses_abc123",
	}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("restore cmd\nwant: %#v\n got: %#v", want, cmd)
	}
	if contains(cmd, "--continue") || contains(cmd, "--fork") {
		t.Fatalf("restore cmd must target the captured session directly, got %#v", cmd)
	}
}

func TestGetRestoreCommandReappliesSystemPromptConfig(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "opencode"}
	promptFile := filepath.Join(t.TempDir(), "system.md")

	cmd, ok, err := plugin.GetRestoreCommand(context.Background(), ports.RestoreConfig{
		SystemPrompt:     "restore AO rules",
		SystemPromptFile: promptFile,
		Session: ports.SessionRef{
			ID:       "sess-1",
			Metadata: map[string]string{opencodeAgentSessionIDMetadataKey: "ses_abc123"},
		},
	})
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if !ok {
		t.Fatal("ok = false, want true")
	}
	configPath := filepath.Join(filepath.Dir(promptFile), "opencode.json")
	want := []string{
		"env", "OPENCODE_CONFIG=" + configPath,
		"opencode",
		"--agent", "ao-sess-1",
		"--session", "ses_abc123",
	}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("restore cmd\nwant: %#v\n got: %#v", want, cmd)
	}
	var config opencodeInlineConfig
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	if got := config.Agent["ao-sess-1"].Prompt; got != "restore AO rules" {
		t.Fatalf("agent prompt = %q, want restore rules", got)
	}
}

// TestGetRestoreCommandAppendsResumeTimePrompt covers resuming a reviewer
// after it was killed and re-triggered: the new task must be embedded in the
// resume argv (mirroring GetLaunchCommand's --prompt), or the resumed session
// would sit idle with no work to act on.
func TestGetRestoreCommandAppendsResumeTimePrompt(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "opencode"}

	cmd, ok, err := plugin.GetRestoreCommand(context.Background(), ports.RestoreConfig{
		Permissions: ports.PermissionModeBypassPermissions,
		Prompt:      "review the new commit",
		Session: ports.SessionRef{
			Metadata: map[string]string{opencodeAgentSessionIDMetadataKey: "ses_abc123"},
		},
	})
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if !ok {
		t.Fatal("ok = false, want true")
	}
	want := []string{
		"opencode",
		"--dangerously-skip-permissions",
		"--session", "ses_abc123",
		"--prompt", "review the new commit",
	}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("restore cmd\nwant: %#v\n got: %#v", want, cmd)
	}
}

func TestGetRestoreCommandFalseWithoutAgentSessionID(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "opencode"}

	cases := []struct {
		name string
		ref  ports.SessionRef
	}{
		{"empty session ref", ports.SessionRef{}},
		{"empty metadata", ports.SessionRef{Metadata: map[string]string{}}},
		{"blank agent session metadata", ports.SessionRef{Metadata: map[string]string{opencodeAgentSessionIDMetadataKey: "   "}}},
		{"workspace path only", ports.SessionRef{WorkspacePath: "/some/path"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd, ok, err := plugin.GetRestoreCommand(context.Background(), ports.RestoreConfig{
				Permissions: ports.PermissionModeDefault,
				Session:     tc.ref,
			})
			if err != nil {
				t.Fatalf("err = %v, want nil", err)
			}
			if ok {
				t.Fatalf("ok = true, want false")
			}
			if cmd != nil {
				t.Fatalf("cmd = %#v, want nil", cmd)
			}
		})
	}
}

func TestSessionInfoReadsHookMetadata(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "opencode"}

	info, ok, err := plugin.SessionInfo(context.Background(), ports.SessionRef{
		WorkspacePath: "/some/path",
		Metadata: map[string]string{
			opencodeAgentSessionIDMetadataKey: "ses_abc123",
			ports.MetadataKeyTitle:            "Fix login redirect",
			ports.MetadataKeySummary:          "Updated the auth callback and tests.",
			"ignored":                         "not returned",
		},
	})
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if !ok {
		t.Fatalf("ok = false, want true")
	}
	if info.AgentSessionID != "ses_abc123" {
		t.Fatalf("AgentSessionID = %q, want native id", info.AgentSessionID)
	}
	if info.Title != "Fix login redirect" {
		t.Fatalf("Title = %q, want hook title", info.Title)
	}
	if info.Summary != "Updated the auth callback and tests." {
		t.Fatalf("Summary = %q, want hook summary", info.Summary)
	}
	if info.Metadata != nil {
		t.Fatalf("Metadata = %#v, want nil for opencode", info.Metadata)
	}
}

func TestSessionInfoFalseWhenNoHookMetadata(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "opencode"}

	info, ok, err := plugin.SessionInfo(context.Background(), ports.SessionRef{
		WorkspacePath: "/some/path",
		Metadata:      map[string]string{},
	})
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if ok {
		t.Fatalf("ok = true, want false")
	}
	if !reflect.DeepEqual(info, ports.SessionInfo{}) {
		t.Fatalf("info = %#v, want zero value", info)
	}
}

func contains(values []string, needle string) bool {
	for _, value := range values {
		if value == needle {
			return true
		}
	}
	return false
}
