package opencodeacp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	acpdriver "github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/acp"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestConfigureUsesNativeACPAndAlwaysCarriesTheTiers(t *testing.T) {
	args, env, err := configure(context.Background(), acpdriver.LaunchConfig{})
	if err != nil {
		t.Fatalf("configure: %v", err)
	}
	if len(args) != 1 || args[0] != "acp" {
		t.Fatalf("args = %#v", args)
	}
	var config struct {
		DefaultAgent string                    `json:"default_agent"`
		Agent        map[string]map[string]any `json:"agent"`
	}
	if err := json.Unmarshal([]byte(env["OPENCODE_CONFIG_CONTENT"]), &config); err != nil {
		t.Fatalf("decode config: %v", err)
	}
	// A session without standing instructions still needs its permission tiers.
	if config.DefaultAgent != "ao-default" || len(config.Agent) != 4 {
		t.Fatalf("config = %#v", config)
	}
	if prompt, ok := config.Agent["ao-default"]["prompt"]; ok {
		t.Fatalf("prompt = %q, want none", prompt)
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

func TestConfigureInjectsThePermissionTiersOpenCodeEnforces(t *testing.T) {
	for _, test := range []struct {
		mode ports.PermissionMode
		want string
	}{
		{mode: ports.PermissionModeDefault, want: "ao-default"},
		{mode: ports.PermissionModeAcceptEdits, want: "ao-accept-edits"},
		{mode: ports.PermissionModeAuto, want: "ao-auto"},
		{mode: ports.PermissionModeBypassPermissions, want: "ao-bypass"},
	} {
		t.Run(string(test.mode), func(t *testing.T) {
			_, env, err := configure(context.Background(), acpdriver.LaunchConfig{
				SessionID: "worker-1", SystemPrompt: "Follow AO worker rules.", Permissions: test.mode,
			})
			if err != nil {
				t.Fatalf("configure: %v", err)
			}
			var config struct {
				DefaultAgent string `json:"default_agent"`
				Agent        map[string]struct {
					Prompt     string            `json:"prompt"`
					Permission map[string]string `json:"permission"`
				} `json:"agent"`
			}
			if err := json.Unmarshal([]byte(env["OPENCODE_CONFIG_CONTENT"]), &config); err != nil {
				t.Fatalf("decode config: %v", err)
			}
			if config.DefaultAgent != test.want {
				t.Fatalf("default agent = %q, want %q", config.DefaultAgent, test.want)
			}
			// Every tier is advertised, so the picker can switch to any of them
			// mid-session, and each carries AO's standing instructions.
			for _, tier := range []string{"ao-default", "ao-accept-edits", "ao-auto", "ao-bypass"} {
				agent, ok := config.Agent[tier]
				if !ok {
					t.Fatalf("tier %q missing from %#v", tier, config.Agent)
				}
				if agent.Prompt != "Follow AO worker rules." {
					t.Fatalf("tier %q prompt = %q", tier, agent.Prompt)
				}
				if _, reads := agent.Permission["read"]; reads {
					t.Fatalf("tier %q overrides read; OpenCode's own .env deny must survive", tier)
				}
			}
			if config.Agent["ao-default"].Permission != nil {
				t.Fatalf("default tier = %#v, want the user's own rules", config.Agent["ao-default"].Permission)
			}
			if got := config.Agent["ao-accept-edits"].Permission; got["edit"] != "allow" || got["bash"] != "ask" {
				t.Fatalf("accept-edits tier = %#v", got)
			}
			if got := config.Agent["ao-auto"].Permission; got["bash"] != "allow" || got["external_directory"] != "allow" {
				t.Fatalf("auto tier = %#v", got)
			}
			if got := config.Agent["ao-bypass"].Permission; got["*"] != "allow" {
				t.Fatalf("bypass tier = %#v", got)
			}
		})
	}
}

func TestConfigureKeepsTheUsersOwnInlineConfig(t *testing.T) {
	_, env, err := configure(context.Background(), acpdriver.LaunchConfig{
		SessionID: "worker-2", Permissions: ports.PermissionModeDefault,
		Env: map[string]string{
			"OPENCODE_CONFIG_CONTENT": `{"provider":{"local":{"name":"Local"}},"agent":{"mine":{"mode":"primary"}},"permission":{"bash":"deny"}}`,
		},
	})
	if err != nil {
		t.Fatalf("configure: %v", err)
	}
	var config struct {
		Provider   map[string]any            `json:"provider"`
		Permission map[string]any            `json:"permission"`
		Agent      map[string]map[string]any `json:"agent"`
	}
	if err := json.Unmarshal([]byte(env["OPENCODE_CONFIG_CONTENT"]), &config); err != nil {
		t.Fatalf("decode config: %v", err)
	}
	if config.Provider["local"] == nil || config.Agent["mine"] == nil || config.Permission["bash"] != "deny" {
		t.Fatalf("user config was not preserved: %#v", config)
	}
}
