package opencodeacp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	acpsdk "github.com/coder/acp-go-sdk"

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
					Prompt     string `json:"prompt"`
					Permission any    `json:"permission"`
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
			}
			if config.Agent["ao-default"].Permission != nil {
				t.Fatalf("default tier = %#v, want the user's own rules", config.Agent["ao-default"].Permission)
			}
			// Accept edits and auto write no rules at all: AO answers their
			// requests instead, so whatever the user or repository denied is
			// never asked about and stays denied.
			for _, tier := range []string{"ao-default", "ao-accept-edits", "ao-auto"} {
				if got := config.Agent[tier].Permission; got != nil {
					t.Fatalf("tier %q permission = %#v, want the provider's own policy", tier, got)
				}
			}
			// Bypass is the exception, and OpenCode enforces it.
			if got := config.Agent["ao-bypass"].Permission; got != "allow" {
				t.Fatalf("bypass tier = %#v, want scalar full access", got)
			}
			// OpenCode's scalar full-access form: a wildcard rule can still lose
			// to a more specific deny contributed by another config layer.
			if got := config.Agent["ao-bypass"].Permission; got != "allow" {
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

func TestPermissionPolicyAnswersRequestsTheWayAutoDoes(t *testing.T) {
	edit, execute, fetch := acpsdk.ToolKindEdit, acpsdk.ToolKindExecute, acpsdk.ToolKindFetch
	options := []acpsdk.PermissionOption{
		{OptionId: "reject", Kind: acpsdk.PermissionOptionKindRejectOnce},
		{OptionId: "once", Kind: acpsdk.PermissionOptionKindAllowOnce},
		{OptionId: "always", Kind: acpsdk.PermissionOptionKindAllowAlways},
	}
	for _, test := range []struct {
		name   string
		mode   ports.PermissionMode
		kind   *acpsdk.ToolKind
		wantID acpsdk.PermissionOptionId
	}{
		{name: "default parks an edit", mode: ports.PermissionModeDefault, kind: &edit},
		{name: "accept edits answers an edit", mode: ports.PermissionModeAcceptEdits, kind: &edit, wantID: "once"},
		{name: "accept edits parks a command", mode: ports.PermissionModeAcceptEdits, kind: &execute},
		{name: "auto answers a command", mode: ports.PermissionModeAuto, kind: &execute, wantID: "once"},
		{name: "auto answers a fetch", mode: ports.PermissionModeAuto, kind: &fetch, wantID: "once"},
		{name: "auto answers an unclassified request", mode: ports.PermissionModeAuto, wantID: "once"},
	} {
		t.Run(test.name, func(t *testing.T) {
			id, handled := permissionPolicy(test.mode, acpsdk.RequestPermissionRequest{
				ToolCall: acpsdk.ToolCallUpdate{Kind: test.kind}, Options: options,
			})
			if id != test.wantID || handled != (test.wantID != "") {
				t.Fatalf("selection = (%q, %v), want (%q, %v)", id, handled, test.wantID, test.wantID != "")
			}
		})
	}

	// A denied tool never raises a request, so nothing here can override it —
	// which is the whole reason AO stopped writing rules for these modes.
	if _, handled := permissionPolicy(ports.PermissionModeAuto,
		acpsdk.RequestPermissionRequest{Options: options[:1]}); handled {
		t.Fatal("auto answered a request offering no allow-once option")
	}
}
