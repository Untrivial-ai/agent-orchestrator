package crush

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/hookutil"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestGetLaunchCommandBuildsCrossPlatformArgv(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "crush"}

	cmd, err := plugin.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		Permissions:   ports.PermissionModeBypassPermissions,
		Kind:          domain.KindWorker,
		Prompt:        "fix this",
		WorkspacePath: "/tmp/workspace",
		SessionID:     "test-session-id",
	})
	if err != nil {
		t.Fatal(err)
	}

	// cfg.SessionID is the AO-internal id and must NOT be passed as --session on
	// launch; Crush mints its own native id, which GetRestoreCommand resumes by.
	want := []string{
		"crush",
		"--cwd", "/tmp/workspace",
		"--yolo",
	}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("unexpected command\nwant: %#v\n got: %#v", want, cmd)
	}
}

func TestGetLaunchCommandMapsPermissionModes(t *testing.T) {
	tests := []struct {
		name        string
		permission  ports.PermissionMode
		want        []string
		notExpected string
	}{
		{
			name:        "default",
			permission:  ports.PermissionModeDefault,
			notExpected: "--yolo",
		},
		{
			name:        "accept-edits",
			permission:  ports.PermissionModeAcceptEdits,
			want:        nil, // Crush doesn't have granular permission modes
			notExpected: "--yolo",
		},
		{
			name:        "auto",
			permission:  ports.PermissionModeAuto,
			want:        nil, // Crush doesn't have granular permission modes
			notExpected: "--yolo",
		},
		{
			name:       "bypass-permissions",
			permission: ports.PermissionModeBypassPermissions,
			want:       []string{"--yolo"},
		},
		{
			name:        "empty",
			permission:  "",
			notExpected: "--yolo",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plugin := &Plugin{resolvedBinary: "crush"}
			cmd, err := plugin.GetLaunchCommand(context.Background(), ports.LaunchConfig{
				Permissions: tt.permission,
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(tt.want) > 0 && !containsSubsequence(cmd, tt.want) {
				t.Fatalf("command %#v does not contain %#v", cmd, tt.want)
			}
			if tt.notExpected != "" && contains(cmd, tt.notExpected) {
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
		t.Fatalf("unexpected prompt delivery strategy: got %v, want %v", got, ports.PromptDeliveryInCommand)
	}
}

func TestGetPromptDeliveryStrategyPromptedSessionsAreAfterStart(t *testing.T) {
	tests := []struct {
		name string
		kind domain.SessionKind
	}{
		{name: "worker", kind: domain.KindWorker},
		{name: "orchestrator", kind: domain.KindOrchestrator},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plugin := &Plugin{}

			got, err := plugin.GetPromptDeliveryStrategy(context.Background(), ports.LaunchConfig{
				Kind:   tt.kind,
				Prompt: "fix this",
			})
			if err != nil {
				t.Fatal(err)
			}

			if got != ports.PromptDeliveryAfterStart {
				t.Fatalf("unexpected prompt delivery strategy: got %v, want %v", got, ports.PromptDeliveryAfterStart)
			}
		})
	}
}

func TestPromptReadinessHints(t *testing.T) {
	hints, err := (&Plugin{}).PromptReadinessHints(context.Background(), ports.LaunchConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if hints.Timeout <= 0 || len(hints.Patterns) == 0 {
		t.Fatalf("hints = %#v, want bounded readiness patterns", hints)
	}
}

func TestGetRestoreCommand(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "crush"}

	tests := []struct {
		name           string
		agentSessionID string
		workspacePath  string
		permission     ports.PermissionMode
		wantOk         bool
		wantContains   []string
	}{
		{
			name:           "restore with session id",
			agentSessionID: "crush-session-123",
			workspacePath:  "/tmp/workspace",
			permission:     ports.PermissionModeDefault,
			wantOk:         true,
			wantContains:   []string{"--cwd", "/tmp/workspace", "--session", "crush-session-123"},
		},
		{
			name:           "restore with bypass permissions",
			agentSessionID: "crush-session-456",
			workspacePath:  "/tmp/workspace",
			permission:     ports.PermissionModeBypassPermissions,
			wantOk:         true,
			wantContains:   []string{"--cwd", "/tmp/workspace", "--yolo", "--session", "crush-session-456"},
		},
		{
			name:          "fallback to most recent workspace session",
			workspacePath: "/tmp/workspace",
			permission:    ports.PermissionModeDefault,
			wantOk:        true,
			wantContains:  []string{"--cwd", "/tmp/workspace", "--continue"},
		},
		{
			name:           "no session id",
			agentSessionID: "",
			wantOk:         false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd, ok, err := plugin.GetRestoreCommand(context.Background(), ports.RestoreConfig{
				Session: ports.SessionRef{
					Metadata:      map[string]string{"agentSessionId": tt.agentSessionID},
					WorkspacePath: tt.workspacePath,
				},
				Permissions: tt.permission,
			})
			if err != nil {
				t.Fatal(err)
			}
			if ok != tt.wantOk {
				t.Fatalf("unexpected ok: got %v, want %v", ok, tt.wantOk)
			}
			if tt.wantOk && len(tt.wantContains) > 0 && !containsSubsequence(cmd, tt.wantContains) {
				t.Fatalf("command %#v does not contain %#v", cmd, tt.wantContains)
			}
		})
	}
}

func TestSessionInfoReturnsFalse(t *testing.T) {
	plugin := &Plugin{}

	info, ok, err := plugin.SessionInfo(context.Background(), ports.SessionRef{
		ID:       "session-123",
		Metadata: map[string]string{"agentSessionId": "crush-session-123"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatalf("unexpected ok: got true, want false (SessionInfo is a no-op for Crush)")
	}
	if info.AgentSessionID != "" || info.Title != "" || info.Summary != "" {
		t.Fatalf("unexpected non-empty info: got %#v", info)
	}
}

func TestManifest(t *testing.T) {
	plugin := &Plugin{}

	manifest := plugin.Manifest()
	if manifest.ID != adapterID {
		t.Fatalf("unexpected manifest ID: got %q, want %q", manifest.ID, adapterID)
	}
	if manifest.Name != "Crush" {
		t.Fatalf("unexpected manifest name: got %q, want \"Crush\"", manifest.Name)
	}
	if len(manifest.Capabilities) != 1 {
		t.Fatalf("unexpected capabilities count: got %d, want 1", len(manifest.Capabilities))
	}
}

func TestGetConfigSpecReportsModelField(t *testing.T) {
	plugin := &Plugin{}

	spec, err := plugin.GetConfigSpec(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(spec.Fields) != 1 {
		t.Fatalf("unexpected config spec fields: got %d, want 1", len(spec.Fields))
	}
	if spec.Fields[0].Key != "model" {
		t.Fatalf("unexpected config spec field key: got %q, want %q", spec.Fields[0].Key, "model")
	}
}

func TestGetAgentHooksInstallsSystemPromptContext(t *testing.T) {
	workspace := t.TempDir()
	promptFile := filepath.Join(t.TempDir(), "system.md")
	if err := os.WriteFile(promptFile, []byte("AO standing instructions\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := (&Plugin{}).GetAgentHooks(context.Background(), ports.WorkspaceHookConfig{
		WorkspacePath:    workspace,
		SystemPrompt:     "inline should lose",
		SystemPromptFile: promptFile,
	}); err != nil {
		t.Fatalf("GetAgentHooks err = %v", err)
	}

	data, err := os.ReadFile(crushSystemPromptFile(workspace))
	if err != nil {
		t.Fatalf("read system prompt: %v", err)
	}
	text := string(data)
	for _, want := range []string{crushSystemPromptMarker, "AO standing instructions"} {
		if !strings.Contains(text, want) {
			t.Fatalf("system prompt missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "inline should lose") {
		t.Fatalf("system prompt used inline prompt when file was provided:\n%s", text)
	}

	cfg := readCrushConfigForTest(t, crushConfigFile(workspace))
	paths := cfg["options"].(map[string]any)["context_paths"].([]any)
	if !jsonArrayContainsString(paths, crushSystemPromptPath) {
		t.Fatalf("context_paths = %#v, want %q", paths, crushSystemPromptPath)
	}
}

func TestGetAgentHooksMergesExistingCrushConfig(t *testing.T) {
	workspace := t.TempDir()
	configPath := crushConfigFile(workspace)
	if err := os.MkdirAll(filepath.Dir(configPath), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte(`{"options":{"context_paths":["CRUSH.md"],"debug":true},"providers":{"local":{"base_url":"http://localhost"}},"hooks":{"PreToolUse":[{"matcher":"^bash$","command":"user-hook"}]}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	for range 2 {
		if err := (&Plugin{}).GetAgentHooks(context.Background(), ports.WorkspaceHookConfig{
			WorkspacePath: workspace,
			SystemPrompt:  "AO standing instructions",
		}); err != nil {
			t.Fatalf("GetAgentHooks err = %v", err)
		}
	}

	cfg := readCrushConfigForTest(t, configPath)
	options := cfg["options"].(map[string]any)
	if options["debug"] != true {
		t.Fatalf("options.debug = %#v, want true", options["debug"])
	}
	if cfg["providers"].(map[string]any)["local"] == nil {
		t.Fatalf("providers.local was dropped: %#v", cfg)
	}
	paths := options["context_paths"].([]any)
	if !jsonArrayContainsString(paths, "CRUSH.md") || !jsonArrayContainsString(paths, crushSystemPromptPath) {
		t.Fatalf("context_paths = %#v", paths)
	}
	if countJSONStrings(paths, crushSystemPromptPath) != 1 {
		t.Fatalf("context_paths duplicated AO path: %#v", paths)
	}
	hooks := cfg["hooks"].(map[string]any)["PreToolUse"].([]any)
	if countJSONObjectsWithCommand(hooks, crushHookCommand) != 1 {
		t.Fatalf("PreToolUse hooks = %#v, want one AO hook", hooks)
	}
	if countJSONObjectsWithCommand(hooks, "user-hook") != 1 {
		t.Fatalf("PreToolUse hooks dropped user hook: %#v", hooks)
	}
}

func TestGetAgentHooksWritesModelOverride(t *testing.T) {
	workspace := t.TempDir()
	if err := (&Plugin{}).GetAgentHooks(context.Background(), ports.WorkspaceHookConfig{
		WorkspacePath: workspace,
		SystemPrompt:  "AO standing instructions",
		Config:        ports.AgentConfig{Model: "anthropic/claude-sonnet-4-5"},
	}); err != nil {
		t.Fatalf("GetAgentHooks err = %v", err)
	}

	cfg := readCrushConfigForTest(t, crushConfigFile(workspace))
	large := cfg["models"].(map[string]any)["large"].(map[string]any)
	if large["provider"] != "anthropic" {
		t.Fatalf("models.large.provider = %#v, want %q", large["provider"], "anthropic")
	}
	if large["model"] != "claude-sonnet-4-5" {
		t.Fatalf("models.large.model = %#v, want %q", large["model"], "claude-sonnet-4-5")
	}
}

func TestGetAgentHooksPreservesOtherModelFieldsOnOverride(t *testing.T) {
	workspace := t.TempDir()
	configPath := crushConfigFile(workspace)
	if err := os.MkdirAll(filepath.Dir(configPath), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte(`{"models":{"large":{"model":"old-model","provider":"old-provider","max_tokens":4096},"small":{"model":"small-model","provider":"small-provider"}},"providers":{"openai":{"base_url":"https://example.test"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := (&Plugin{}).GetAgentHooks(context.Background(), ports.WorkspaceHookConfig{
		WorkspacePath: workspace,
		SystemPrompt:  "AO standing instructions",
		Config:        ports.AgentConfig{Model: "openai/gpt-5"},
	}); err != nil {
		t.Fatalf("GetAgentHooks err = %v", err)
	}

	cfg := readCrushConfigForTest(t, configPath)
	models := cfg["models"].(map[string]any)
	large := models["large"].(map[string]any)
	if large["provider"] != "openai" || large["model"] != "gpt-5" {
		t.Fatalf("models.large = %#v, want provider=openai model=gpt-5", large)
	}
	if large["max_tokens"] != float64(4096) {
		t.Fatalf("models.large.max_tokens was dropped: %#v", large)
	}
	small := models["small"].(map[string]any)
	if small["provider"] != "small-provider" || small["model"] != "small-model" {
		t.Fatalf("models.small = %#v, want existing small model untouched", small)
	}
	providers := cfg["providers"].(map[string]any)
	if _, ok := providers["openai"].(map[string]any); !ok {
		t.Fatalf("providers were dropped: %#v", providers)
	}
}

func TestGetAgentHooksLeavesModelUntouchedWhenNoOverride(t *testing.T) {
	workspace := t.TempDir()
	configPath := crushConfigFile(workspace)
	if err := os.MkdirAll(filepath.Dir(configPath), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte(`{"models":{"large":{"model":"user-picked-model","provider":"user-provider"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := (&Plugin{}).GetAgentHooks(context.Background(), ports.WorkspaceHookConfig{
		WorkspacePath: workspace,
		SystemPrompt:  "AO standing instructions",
	}); err != nil {
		t.Fatalf("GetAgentHooks err = %v", err)
	}

	large := readCrushConfigForTest(t, configPath)["models"].(map[string]any)["large"].(map[string]any)
	if large["provider"] != "user-provider" || large["model"] != "user-picked-model" {
		t.Fatalf("models.large = %#v, want the user's existing selection untouched", large)
	}
}

func TestGetAgentHooksInfersProviderForBareModel(t *testing.T) {
	workspace := t.TempDir()
	configPath := crushConfigFile(workspace)
	if err := os.MkdirAll(filepath.Dir(configPath), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte(`{"models":{"large":{"model":"old-model","provider":"anthropic","temperature":0.2}}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := (&Plugin{}).GetAgentHooks(context.Background(), ports.WorkspaceHookConfig{
		WorkspacePath: workspace,
		SystemPrompt:  "AO standing instructions",
		Config:        ports.AgentConfig{Model: "claude-sonnet-4-5"},
	}); err != nil {
		t.Fatalf("GetAgentHooks err = %v", err)
	}

	large := readCrushConfigForTest(t, configPath)["models"].(map[string]any)["large"].(map[string]any)
	if large["provider"] != "anthropic" || large["model"] != "claude-sonnet-4-5" {
		t.Fatalf("models.large = %#v, want provider=anthropic model=claude-sonnet-4-5", large)
	}
	if large["temperature"] != 0.2 {
		t.Fatalf("models.large.temperature was dropped: %#v", large)
	}
}

func TestGetAgentHooksSkipsBareModelWithoutProvider(t *testing.T) {
	workspace := t.TempDir()

	if err := (&Plugin{}).GetAgentHooks(context.Background(), ports.WorkspaceHookConfig{
		WorkspacePath: workspace,
		SystemPrompt:  "AO standing instructions",
		Config:        ports.AgentConfig{Model: "claude-sonnet-4-5"},
	}); err != nil {
		t.Fatalf("GetAgentHooks err = %v", err)
	}

	cfg := readCrushConfigForTest(t, crushConfigFile(workspace))
	if _, ok := cfg["models"]; ok {
		t.Fatalf("models were written for bare model with no provider to infer: %#v", cfg)
	}
}

func TestGetAgentHooksSplitsModelOverrideOnFirstSlash(t *testing.T) {
	workspace := t.TempDir()

	if err := (&Plugin{}).GetAgentHooks(context.Background(), ports.WorkspaceHookConfig{
		WorkspacePath: workspace,
		SystemPrompt:  "AO standing instructions",
		Config:        ports.AgentConfig{Model: "openrouter/anthropic/claude-x"},
	}); err != nil {
		t.Fatalf("GetAgentHooks err = %v", err)
	}

	large := readCrushConfigForTest(t, crushConfigFile(workspace))["models"].(map[string]any)["large"].(map[string]any)
	if large["provider"] != "openrouter" || large["model"] != "anthropic/claude-x" {
		t.Fatalf("models.large = %#v, want provider=openrouter model=anthropic/claude-x", large)
	}
}

func TestGetAgentHooksGitignoresManagedCrushFiles(t *testing.T) {
	workspace := t.TempDir()
	if err := (&Plugin{}).GetAgentHooks(context.Background(), ports.WorkspaceHookConfig{
		WorkspacePath: workspace,
		SystemPrompt:  "AO standing instructions",
	}); err != nil {
		t.Fatalf("GetAgentHooks err = %v", err)
	}

	data, err := os.ReadFile(filepath.Join(workspace, crushConfigDirName, ".gitignore"))
	if err != nil {
		t.Fatalf("read gitignore: %v", err)
	}
	text := string(data)
	for _, want := range []string{hookutil.GitignoreSentinel, "/" + crushSystemPromptName} {
		if !strings.Contains(text, want) {
			t.Fatalf(".gitignore missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "/"+crushConfigFileName) {
		t.Fatalf(".gitignore should not ignore project config %q:\n%s", crushConfigFileName, text)
	}

	// AO must not write/own a repo-root .gitignore: EnsureWorkspaceGitignore
	// is a no-op when a root .gitignore already exists without AO's
	// sentinel, and unconditionally overwrites one that does carry it on
	// every subsequent call — neither is safe for a directory the user (or
	// their repo) already owns. .crush.json churn in the agent's worktree
	// is a known, pre-existing limitation left to a future AddExclude-based
	// fix (writing to .git/info/exclude instead).
	if _, err := os.Stat(filepath.Join(workspace, ".gitignore")); !os.IsNotExist(err) {
		t.Fatalf("workspace root .gitignore should not be created by crush's adapter, stat err = %v", err)
	}
}

func TestGetAgentHooksRefusesForeignSystemPromptFile(t *testing.T) {
	workspace := t.TempDir()
	path := crushSystemPromptFile(workspace)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("user file\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := (&Plugin{}).GetAgentHooks(context.Background(), ports.WorkspaceHookConfig{
		WorkspacePath: workspace,
		SystemPrompt:  "AO standing instructions",
	})
	if err == nil {
		t.Fatal("expected error for foreign system prompt file")
	}
	if !strings.Contains(err.Error(), "refusing to overwrite non-AO file") {
		t.Fatalf("err = %v", err)
	}
}

func TestGetAgentHooksEmptyPromptIsNoOp(t *testing.T) {
	workspace := t.TempDir()
	if err := (&Plugin{}).GetAgentHooks(context.Background(), ports.WorkspaceHookConfig{
		WorkspacePath: workspace,
	}); err != nil {
		t.Fatalf("GetAgentHooks err = %v", err)
	}
	if _, err := os.Stat(crushSystemPromptFile(workspace)); !os.IsNotExist(err) {
		t.Fatalf("system prompt file stat err = %v, want not exist", err)
	}
	if _, err := os.Stat(crushConfigFile(workspace)); !os.IsNotExist(err) {
		t.Fatalf("config file stat err = %v, want not exist", err)
	}
}

func TestGetAgentHooksAppliesModelOverrideWithEmptyPrompt(t *testing.T) {
	workspace := t.TempDir()
	if err := (&Plugin{}).GetAgentHooks(context.Background(), ports.WorkspaceHookConfig{
		WorkspacePath: workspace,
		Config:        ports.AgentConfig{Model: "anthropic/claude-sonnet-4-5"},
	}); err != nil {
		t.Fatalf("GetAgentHooks err = %v", err)
	}
	if _, err := os.Stat(crushSystemPromptFile(workspace)); !os.IsNotExist(err) {
		t.Fatalf("system prompt file stat err = %v, want not exist", err)
	}
	large := readCrushConfigForTest(t, crushConfigFile(workspace))["models"].(map[string]any)["large"].(map[string]any)
	if large["provider"] != "anthropic" || large["model"] != "claude-sonnet-4-5" {
		t.Fatalf("models.large = %#v, want provider=anthropic model=claude-sonnet-4-5", large)
	}
}

func TestUninstallHooksRemovesManagedCrushContext(t *testing.T) {
	workspace := t.TempDir()
	configPath := crushConfigFile(workspace)
	if err := os.MkdirAll(filepath.Dir(configPath), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte(`{"options":{"context_paths":["CRUSH.md"]},"hooks":{"PreToolUse":[{"command":"user-hook"}]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := (&Plugin{}).GetAgentHooks(context.Background(), ports.WorkspaceHookConfig{
		WorkspacePath: workspace,
		SystemPrompt:  "AO standing instructions",
	}); err != nil {
		t.Fatalf("GetAgentHooks err = %v", err)
	}

	if err := (&Plugin{}).UninstallHooks(context.Background(), workspace); err != nil {
		t.Fatalf("UninstallHooks err = %v", err)
	}

	if _, err := os.Stat(crushSystemPromptFile(workspace)); !os.IsNotExist(err) {
		t.Fatalf("system prompt file stat err = %v, want not exist", err)
	}
	cfg := readCrushConfigForTest(t, configPath)
	paths := cfg["options"].(map[string]any)["context_paths"].([]any)
	if !jsonArrayContainsString(paths, "CRUSH.md") || jsonArrayContainsString(paths, crushSystemPromptPath) {
		t.Fatalf("context_paths = %#v", paths)
	}
	hooks := cfg["hooks"].(map[string]any)["PreToolUse"].([]any)
	if countJSONObjectsWithCommand(hooks, crushHookCommand) != 0 {
		t.Fatalf("PreToolUse hooks still contain AO hook: %#v", hooks)
	}
	if countJSONObjectsWithCommand(hooks, "user-hook") != 1 {
		t.Fatalf("PreToolUse hooks dropped user hook: %#v", hooks)
	}
}

func TestUninstallHooksPreservesForeignSystemPromptFile(t *testing.T) {
	workspace := t.TempDir()
	path := crushSystemPromptFile(workspace)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("user file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := mergeCrushContextPath(crushConfigFile(workspace), crushSystemPromptPath); err != nil {
		t.Fatal(err)
	}

	if err := (&Plugin{}).UninstallHooks(context.Background(), workspace); err != nil {
		t.Fatalf("UninstallHooks err = %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read foreign prompt: %v", err)
	}
	if string(data) != "user file\n" {
		t.Fatalf("foreign prompt changed: %q", data)
	}
}

func TestAreHooksInstalledReportsManagedSystemPrompt(t *testing.T) {
	plugin := &Plugin{}
	workspace := t.TempDir()

	installed, err := plugin.AreHooksInstalled(context.Background(), workspace)
	if err != nil {
		t.Fatalf("AreHooksInstalled err = %v", err)
	}
	if installed {
		t.Fatal("installed = true before install, want false")
	}
	if err := plugin.GetAgentHooks(context.Background(), ports.WorkspaceHookConfig{
		WorkspacePath: workspace,
		SystemPrompt:  "AO standing instructions",
	}); err != nil {
		t.Fatalf("GetAgentHooks err = %v", err)
	}
	installed, err = plugin.AreHooksInstalled(context.Background(), workspace)
	if err != nil {
		t.Fatalf("AreHooksInstalled err = %v", err)
	}
	if !installed {
		t.Fatal("installed = false after install, want true")
	}
}

// Helper functions from codex_test.go

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

func containsSubsequence(haystack, needle []string) bool {
	for i := 0; i <= len(haystack)-len(needle); i++ {
		match := true
		for j, n := range needle {
			if haystack[i+j] != n {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

func readCrushConfigForTest(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("parse config %s: %v\n%s", path, err, data)
	}
	return cfg
}

func jsonArrayContainsString(items []any, want string) bool {
	return countJSONStrings(items, want) > 0
}

func countJSONStrings(items []any, want string) int {
	count := 0
	for _, item := range items {
		if item == want {
			count++
		}
	}
	return count
}

func countJSONObjectsWithCommand(items []any, command string) int {
	count := 0
	for _, item := range items {
		if object, ok := item.(map[string]any); ok && object["command"] == command {
			count++
		}
	}
	return count
}
