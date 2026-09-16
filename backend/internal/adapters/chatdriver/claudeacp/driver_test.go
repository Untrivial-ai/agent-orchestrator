package claudeacp

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	acpsdk "github.com/coder/acp-go-sdk"

	acpdriver "github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/acp"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestClaudeACPLaunchEnvAdvertisesProviderModels(t *testing.T) {
	t.Setenv("CLAUDE_MODEL_CONFIG", "")
	models := []ports.AgentModelInfo{
		{ID: "claude-opus-4-7"},
		{ID: "claude-sonnet-4-6"},
	}
	env := claudeACPLaunchEnv(map[string]string{"PROJECT_ENV": "kept"}, "/opt/claude", "", models)

	if env["CLAUDE_CODE_EXECUTABLE"] != "/opt/claude" || env["PROJECT_ENV"] != "kept" {
		t.Fatalf("launch env = %#v, want executable and project env preserved", env)
	}
	var config struct {
		AvailableModels []string `json:"availableModels"`
	}
	if err := json.Unmarshal([]byte(env["CLAUDE_MODEL_CONFIG"]), &config); err != nil {
		t.Fatalf("CLAUDE_MODEL_CONFIG = %q: %v", env["CLAUDE_MODEL_CONFIG"], err)
	}
	want := []string{"claude-opus-4-7", "claude-sonnet-4-6"}
	if !reflect.DeepEqual(config.AvailableModels, want) {
		t.Fatalf("availableModels = %v, want %v", config.AvailableModels, want)
	}
}

func TestClaudeACPLaunchEnvPreservesExplicitModelConfiguration(t *testing.T) {
	existing := `{"availableModels":["team-opus"],"modelOverrides":{"team-opus":"arn:team:opus"}}`
	env := claudeACPLaunchEnv(
		map[string]string{"CLAUDE_MODEL_CONFIG": existing},
		"/opt/claude",
		"",
		[]ports.AgentModelInfo{{ID: "claude-opus-4-7"}},
	)
	if env["CLAUDE_MODEL_CONFIG"] != existing {
		t.Fatalf("CLAUDE_MODEL_CONFIG = %q, want explicit user configuration preserved", env["CLAUDE_MODEL_CONFIG"])
	}
}

func TestClaudeACPLaunchEnvAddsModelsBesideExistingOverrides(t *testing.T) {
	t.Setenv("CLAUDE_MODEL_CONFIG", "")
	existing := `{"modelOverrides":{"claude-opus-4-7":"arn:team:opus"}}`
	env := claudeACPLaunchEnv(
		map[string]string{"CLAUDE_MODEL_CONFIG": existing},
		"/opt/claude",
		"",
		[]ports.AgentModelInfo{{ID: "claude-opus-4-7"}},
	)
	var config struct {
		AvailableModels []string          `json:"availableModels"`
		ModelOverrides  map[string]string `json:"modelOverrides"`
	}
	if err := json.Unmarshal([]byte(env["CLAUDE_MODEL_CONFIG"]), &config); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(config.AvailableModels, []string{"claude-opus-4-7"}) {
		t.Fatalf("availableModels = %v, want provider model", config.AvailableModels)
	}
	if config.ModelOverrides["claude-opus-4-7"] != "arn:team:opus" {
		t.Fatalf("modelOverrides = %v, want existing override preserved", config.ModelOverrides)
	}
}

func TestClaudeACPLaunchEnvPreservesInvalidModelConfiguration(t *testing.T) {
	for _, value := range []string{"null", "{"} {
		t.Run(value, func(t *testing.T) {
			env := claudeACPLaunchEnv(
				map[string]string{"CLAUDE_MODEL_CONFIG": value},
				"/opt/claude",
				"",
				[]ports.AgentModelInfo{{ID: "claude-opus-4-7"}},
			)
			if env["CLAUDE_MODEL_CONFIG"] != value {
				t.Fatalf("CLAUDE_MODEL_CONFIG = %q, want invalid user value %q preserved for ACP to report", env["CLAUDE_MODEL_CONFIG"], value)
			}
		})
	}
}

func TestClaudeACPLaunchEnvAdvertisesSelectedModelWithoutProviderCatalog(t *testing.T) {
	t.Setenv("CLAUDE_MODEL_CONFIG", "")
	env := claudeACPLaunchEnv(nil, "/opt/claude", "claude-opus-4-7", nil)
	var config struct {
		AvailableModels []string `json:"availableModels"`
	}
	if err := json.Unmarshal([]byte(env["CLAUDE_MODEL_CONFIG"]), &config); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(config.AvailableModels, []string{"claude-opus-4-7"}) {
		t.Fatalf("availableModels = %v, want the selected model even without provider discovery", config.AvailableModels)
	}
}

func TestClaudeACPLaunchEnvAddsSelectedProviderModelAsCustomOption(t *testing.T) {
	t.Setenv("CLAUDE_MODEL_CONFIG", "")
	t.Setenv("ANTHROPIC_CUSTOM_MODEL_OPTION", "")
	env := claudeACPLaunchEnv(
		nil,
		"/opt/claude",
		"claude-fable-5",
		[]ports.AgentModelInfo{{ID: "claude-fable-5"}},
	)
	if got := env["ANTHROPIC_CUSTOM_MODEL_OPTION"]; got != "claude-fable-5" {
		t.Fatalf("ANTHROPIC_CUSTOM_MODEL_OPTION = %q, want selected provider model", got)
	}
}

func TestClaudeACPLaunchEnvDoesNotAddNativeAliasesAsCustomOptions(t *testing.T) {
	t.Setenv("CLAUDE_MODEL_CONFIG", "")
	t.Setenv("ANTHROPIC_CUSTOM_MODEL_OPTION", "")
	for _, alias := range []string{"default", "sonnet", "opus", "haiku", "fable", "opus[1m]"} {
		t.Run(alias, func(t *testing.T) {
			env := claudeACPLaunchEnv(nil, "/opt/claude", alias, []ports.AgentModelInfo{{ID: alias}})
			if got := env["ANTHROPIC_CUSTOM_MODEL_OPTION"]; got != "" {
				t.Fatalf("ANTHROPIC_CUSTOM_MODEL_OPTION = %q, want native alias omitted", got)
			}
		})
	}
}

func TestClaudeACPLaunchEnvPreservesExplicitCustomModelOption(t *testing.T) {
	t.Setenv("ANTHROPIC_CUSTOM_MODEL_OPTION", "")
	env := claudeACPLaunchEnv(
		map[string]string{"ANTHROPIC_CUSTOM_MODEL_OPTION": "team-model"},
		"/opt/claude",
		"claude-fable-5",
		[]ports.AgentModelInfo{{ID: "claude-fable-5"}},
	)
	if got := env["ANTHROPIC_CUSTOM_MODEL_OPTION"]; got != "team-model" {
		t.Fatalf("ANTHROPIC_CUSTOM_MODEL_OPTION = %q, want explicit user value preserved", got)
	}
}

func TestClaudeACPModelConfigSkipsDiscoveryWhenUserConfigurationMustBePreserved(t *testing.T) {
	t.Setenv("CLAUDE_MODEL_CONFIG", "")
	tests := []struct {
		name     string
		input    map[string]string
		preserve bool
	}{
		{name: "unset"},
		{name: "overrides can be merged", input: map[string]string{
			"CLAUDE_MODEL_CONFIG": `{"modelOverrides":{"team-opus":"arn:team:opus"}}`,
		}},
		{name: "explicit available models", input: map[string]string{
			"CLAUDE_MODEL_CONFIG": `{"availableModels":["team-opus"]}`,
		}, preserve: true},
		{name: "malformed", input: map[string]string{
			"CLAUDE_MODEL_CONFIG": `{`,
		}, preserve: true},
		{name: "null", input: map[string]string{
			"CLAUDE_MODEL_CONFIG": `null`,
		}, preserve: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, preserve := claudeACPModelConfig(tc.input)
			if preserve != tc.preserve {
				t.Fatalf("preserve = %v, want %v", preserve, tc.preserve)
			}
		})
	}
}

func TestClaudeSessionMetaAppendsWithoutReplacingPreset(t *testing.T) {
	if got := claudeSessionMeta(acpdriver.LaunchConfig{}); got != nil {
		t.Fatalf("empty prompt metadata = %#v", got)
	}
	meta := claudeSessionMeta(acpdriver.LaunchConfig{SystemPrompt: "AO standing instructions"})
	prompt, ok := meta["systemPrompt"].(map[string]any)
	if !ok {
		t.Fatalf("systemPrompt = %#v", meta["systemPrompt"])
	}
	if prompt["type"] != "preset" || prompt["preset"] != "claude_code" || prompt["append"] != "AO standing instructions" {
		t.Fatalf("systemPrompt = %#v", prompt)
	}
}

func TestClaudeSessionMetaNeverIncludesReplayContext(t *testing.T) {
	meta := claudeSessionMeta(acpdriver.LaunchConfig{SystemPrompt: "AO standing instructions"})
	prompt := meta["systemPrompt"].(map[string]any)
	if strings.Contains(prompt["append"].(string), "replayed-conversation") {
		t.Fatal("replay context entered the system prompt")
	}
}

func TestClaudeSessionModeUsesAdapterModeIDs(t *testing.T) {
	tests := map[ports.PermissionMode]string{
		ports.PermissionModeDefault:           "",
		ports.PermissionModeAcceptEdits:       "acceptEdits",
		ports.PermissionModeAuto:              "auto",
		ports.PermissionModeBypassPermissions: "bypassPermissions",
	}
	for permission, want := range tests {
		if got := claudeSessionMode(permission); got != want {
			t.Errorf("mode(%q) = %q, want %q", permission, got, want)
		}
	}
}

func TestClaudeSessionOptionsUseACPConfigIDs(t *testing.T) {
	got := claudeSessionOptions(ports.ChatTurnSettings{Model: "sonnet", Effort: "high"})
	want := []acpdriver.SessionOption{{ID: "model", Value: "sonnet"}, {ID: "effort", Value: "high"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("options = %#v, want %#v", got, want)
	}
}

func TestValidateClaudeACPExecutableRejectsWindowsCommandShims(t *testing.T) {
	tests := []struct {
		name    string
		binary  string
		goos    string
		wantErr bool
	}{
		{name: "native executable", binary: `C:\\npm\\claude.exe`, goos: "windows"},
		{name: "cmd shim", binary: `C:\\npm\\claude.cmd`, goos: "windows", wantErr: true},
		{name: "bat shim", binary: `C:\\npm\\claude.BAT`, goos: "windows", wantErr: true},
		{name: "non-Windows shim", binary: "/tmp/claude.cmd", goos: "linux"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateClaudeACPExecutable(tc.binary, tc.goos)
			if (err != nil) != tc.wantErr {
				t.Fatalf("validateClaudeACPExecutable(%q, %q) error = %v, wantErr %v", tc.binary, tc.goos, err, tc.wantErr)
			}
			if tc.wantErr && !strings.Contains(err.Error(), "native claude.exe") {
				t.Fatalf("error = %q, want actionable native executable guidance", err)
			}
		})
	}
}

func TestRuntimeCommandOverride(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("AO_CLAUDE_ACP_COMMAND", executable)
	launch, err := resolveRuntime(context.Background())
	if err != nil {
		t.Fatalf("resolveRuntime: %v", err)
	}
	if launch.command != executable || len(launch.args) != 0 {
		t.Fatalf("runtime = %#v", launch)
	}
}

type rejectedClaudePlugin struct{ binary string }

func (p rejectedClaudePlugin) ResolveBinary(context.Context) (string, error) { return p.binary, nil }
func (rejectedClaudePlugin) AuthStatus(context.Context) (ports.AgentAuthStatus, error) {
	return ports.AgentAuthStatusUnauthorized, nil
}

func TestPreflightBlocksAConfirmedRejection(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("AO_CLAUDE_ACP_COMMAND", executable)
	_, err = New(rejectedClaudePlugin{binary: executable}, nil, nil).Probe(context.Background())
	if !errors.Is(err, ports.ErrChatAuthRequired) {
		t.Fatalf("Probe error = %v, want ErrChatAuthRequired", err)
	}
}

func TestClaudePromptResponseFailureRequiresStructuredLoginAction(t *testing.T) {
	response := acpsdk.PromptResponse{
		StopReason: acpsdk.StopReasonEndTurn,
		Meta: map[string]any{"jetbrains": map[string]any{"air": map[string]any{
			"version": float64(1),
			"sessionFailure": map[string]any{
				"id": "auth-expired", "severity": "error",
				"title":   "Your login has expired.",
				"details": "Run /login to sign in again.",
				"actions": []any{"login"},
			},
		}}},
	}

	err := claudePromptResponseFailure(response)
	if !errors.Is(err, ports.ErrChatAuthRequired) {
		t.Fatalf("error = %v, want ErrChatAuthRequired", err)
	}
}

func TestClaudePromptResponseFailureIgnoresAssistantAuthProse(t *testing.T) {
	response := acpsdk.PromptResponse{
		StopReason: acpsdk.StopReasonEndTurn,
		Meta:       map[string]any{"assistantMessage": "Your OAuth token has expired; please run /login."},
	}
	if err := claudePromptResponseFailure(response); err != nil {
		t.Fatalf("assistant prose classified as terminal auth failure: %v", err)
	}
}

func TestClaudeAuthRejectionNotifiesDaemon(t *testing.T) {
	called := 0
	callback := claudeAuthRejected(func() { called++ })
	callback()
	if called != 1 {
		t.Fatalf("daemon callback calls = %d, want 1", called)
	}
}
