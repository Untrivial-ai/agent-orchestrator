package opencodeacp

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"strings"
	"testing"

	acpsdk "github.com/coder/acp-go-sdk"

	acpdriver "github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/acp"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestConfigureUsesNativeACPAndMergesSystemPromptConfig(t *testing.T) {
	args, env, err := configure(context.Background(), acpdriver.LaunchConfig{
		SessionID: "worker-1", SystemPrompt: "Follow AO worker rules.",
		Permissions: ports.PermissionModeBypassPermissions,
		Env: map[string]string{
			"OPENCODE_CONFIG":         "/user/custom.json",
			"OPENCODE_CONFIG_CONTENT": `{"provider":{"local":{"name":"Local"}},"agent":{"mine":{"mode":"primary"}}}`,
		},
	})
	if err != nil {
		t.Fatalf("configure: %v", err)
	}
	if len(args) != 1 || args[0] != "acp" {
		t.Fatalf("args = %#v", args)
	}
	var config struct {
		DefaultAgent string            `json:"default_agent"`
		Provider     map[string]any    `json:"provider"`
		Permission   map[string]string `json:"permission"`
		Agent        map[string]struct {
			Mode   string `json:"mode"`
			Prompt string `json:"prompt"`
		} `json:"agent"`
	}
	if err := json.Unmarshal([]byte(env["OPENCODE_CONFIG_CONTENT"]), &config); err != nil {
		t.Fatalf("decode config: %v", err)
	}
	if config.DefaultAgent != "ao-worker-1" {
		t.Fatalf("default agent = %q", config.DefaultAgent)
	}
	if got := config.Agent[config.DefaultAgent]; got.Mode != "primary" || got.Prompt != "Follow AO worker rules." {
		t.Fatalf("AO agent config = %#v", got)
	}
	if _, ok := config.Agent["mine"]; !ok || config.Provider["local"] == nil {
		t.Fatalf("user inline config was not preserved: %#v", config)
	}
	if config.Permission["*"] != "allow" {
		t.Fatalf("permission = %#v, want wildcard allow", config.Permission)
	}
}

func TestConfigureMapsOpenCodePermissionModes(t *testing.T) {
	for _, tt := range []struct {
		mode ports.PermissionMode
		want map[string]string
	}{
		{ports.PermissionModeDefault, nil},
		{ports.PermissionModeAcceptEdits, map[string]string{"edit": "allow"}},
		{ports.PermissionModeAuto, map[string]string{"edit": "allow", "bash": "allow"}},
		{ports.PermissionModeBypassPermissions, map[string]string{"*": "allow"}},
	} {
		t.Run(string(tt.mode), func(t *testing.T) {
			_, env, err := configure(context.Background(), acpdriver.LaunchConfig{Permissions: tt.mode})
			if err != nil {
				t.Fatal(err)
			}
			if tt.want == nil {
				if env != nil {
					t.Fatalf("env = %#v, want nil", env)
				}
				return
			}
			var config struct {
				Permission map[string]string `json:"permission"`
			}
			if err := json.Unmarshal([]byte(env["OPENCODE_CONFIG_CONTENT"]), &config); err != nil {
				t.Fatal(err)
			}
			if !maps.Equal(config.Permission, tt.want) {
				t.Fatalf("permission = %#v, want %#v", config.Permission, tt.want)
			}
		})
	}
}

func TestPermissionPolicyImplementsOpenCodeApprovalModes(t *testing.T) {
	edit := acpsdk.ToolKindEdit
	execute := acpsdk.ToolKindExecute
	options := []acpsdk.PermissionOption{
		{OptionId: "allow-once", Kind: acpsdk.PermissionOptionKindAllowOnce},
		{OptionId: "allow-always", Kind: acpsdk.PermissionOptionKindAllowAlways},
	}
	for _, tt := range []struct {
		name    string
		mode    ports.PermissionMode
		kind    *acpsdk.ToolKind
		want    acpsdk.PermissionOptionId
		handled bool
	}{
		{name: "default parks", mode: ports.PermissionModeDefault, kind: &edit},
		{name: "accept edits allows edit", mode: ports.PermissionModeAcceptEdits, kind: &edit, want: "allow-once", handled: true},
		{name: "accept edits parks execute", mode: ports.PermissionModeAcceptEdits, kind: &execute},
		{name: "auto allows execute", mode: ports.PermissionModeAuto, kind: &execute, want: "allow-once", handled: true},
		{name: "bypass persists allow", mode: ports.PermissionModeBypassPermissions, kind: &execute, want: "allow-always", handled: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, handled := permissionPolicy(tt.mode, acpsdk.RequestPermissionRequest{
				ToolCall: acpsdk.ToolCallUpdate{Kind: tt.kind}, Options: options,
			})
			if got != tt.want || handled != tt.handled {
				t.Fatalf("selection = (%q, %v), want (%q, %v)", got, handled, tt.want, tt.handled)
			}
		})
	}
}

func TestConfigureWithoutSystemPromptWritesNoAOConfig(t *testing.T) {
	args, env, err := configure(context.Background(), acpdriver.LaunchConfig{})
	if err != nil {
		t.Fatalf("configure: %v", err)
	}
	if len(args) != 1 || args[0] != "acp" || env != nil {
		t.Fatalf("args/env = %#v, %#v", args, env)
	}
}

func TestSessionOptionsUseProviderAdvertisedModelOption(t *testing.T) {
	if got := sessionOptions(ports.ChatTurnSettings{}); got != nil {
		t.Fatalf("empty settings = %#v", got)
	}
	got := sessionOptions(ports.ChatTurnSettings{Model: "anthropic/claude-sonnet"})
	if len(got) != 1 || got[0].ID != "model" || got[0].Value != "anthropic/claude-sonnet" {
		t.Fatalf("model settings = %#v", got)
	}
}

func TestRejectsProviderNameBeforeLaunchingOpenCode(t *testing.T) {
	for _, resume := range []bool{false, true} {
		name := "start"
		if resume {
			name = "resume"
		}
		t.Run(name, func(t *testing.T) {
			plugin := &unresolvedPlugin{}
			driver := New(plugin, nil)
			cfg := ports.ChatStartConfig{WorkspacePath: t.TempDir(), Model: "TensorMux"}
			var err error
			if resume {
				_, err = driver.Resume(context.Background(), ports.ChatResumeConfig{WorkspacePath: cfg.WorkspacePath, Model: cfg.Model, ProviderConversationID: "existing"})
			} else {
				_, err = driver.Start(context.Background(), cfg)
			}
			if !errors.Is(err, ports.ErrChatConfigOptionInvalid) || !strings.Contains(err.Error(), "provider/model") {
				t.Fatalf("error = %v, want model format validation with recovery guidance", err)
			}
			if plugin.resolved {
				t.Fatal("invalid model reached OpenCode binary resolution")
			}
		})
	}
}

type unresolvedPlugin struct{ resolved bool }

func (p *unresolvedPlugin) ResolveBinary(context.Context) (string, error) {
	p.resolved = true
	return "", errors.New("unexpected binary resolution")
}
func (*unresolvedPlugin) AuthStatus(context.Context) (ports.AgentAuthStatus, error) {
	return ports.AgentAuthStatusUnknown, nil
}

func TestValidateTurnSettingsModelFormat(t *testing.T) {
	for _, model := range []string{"", "tensormux/glm-4-7-flash", "openrouter/vendor/model", "custom-provider/private-model:latest"} {
		t.Run("valid/"+model, func(t *testing.T) {
			settings := ports.ChatTurnSettings{Model: model}
			if err := validateTurnSettings(ports.PermissionModeDefault, settings); err != nil {
				t.Fatal(err)
			}
			options := sessionOptions(settings)
			if model != "" && (len(options) != 1 || options[0].Value != model) {
				t.Fatalf("model ID changed: %#v", options)
			}
		})
	}
	for _, model := range []string{"TensorMux", "glm-4-7-flash", " ", "/model", "provider/", " /model", "provider/ "} {
		t.Run("invalid/"+model, func(t *testing.T) {
			err := validateTurnSettings(ports.PermissionModeDefault, ports.ChatTurnSettings{Model: model})
			if !errors.Is(err, ports.ErrChatConfigOptionInvalid) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestSessionOptionsForwardsEffortAfterModel(t *testing.T) {
	got := sessionOptions(ports.ChatTurnSettings{Model: "opencode-go/deepseek-v4.1-flash", Effort: "max"})
	if len(got) != 2 || got[0].ID != "model" || got[1].ID != "effort" || got[1].Value != "max" {
		t.Fatalf("model+effort settings = %#v", got)
	}
	got = sessionOptions(ports.ChatTurnSettings{Effort: "xhigh"})
	if len(got) != 1 || got[0].ID != "effort" || got[0].Value != "xhigh" {
		t.Fatalf("effort-only settings = %#v", got)
	}
}
