package deepseekharnessacp

import (
	"context"
	"reflect"
	"testing"

	acpsdk "github.com/coder/acp-go-sdk"

	acpdriver "github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/acp"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestConfigureBootsTheACPProfile(t *testing.T) {
	args, env, err := configure(context.Background(), acpdriver.LaunchConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(args, []string{"--profile", "acp"}) {
		t.Fatalf("args = %#v", args)
	}
	// The profile owns the model route and credentials, so AO adds no overlay.
	if len(env) != 0 {
		t.Fatalf("env = %#v, want none", env)
	}
}

func TestSessionOptionsCarryModelAndMappedEffort(t *testing.T) {
	got := sessionOptions(ports.ChatTurnSettings{
		Model:  `["deepseek-official","deepseek-v4-flash"]`,
		Effort: "xhigh",
	})
	want := []acpdriver.SessionOption{
		{ID: "model", Value: `["deepseek-official","deepseek-v4-flash"]`},
		{ID: "reasoning_effort", Value: "max"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("options\nwant: %#v\n got: %#v", want, got)
	}
}

func TestSessionOptionsOmitUnsetSettings(t *testing.T) {
	if got := sessionOptions(ports.ChatTurnSettings{}); got != nil {
		t.Fatalf("options = %#v, want nil", got)
	}
}

// TestSessionOptionsMapAOVocabularyOntoHarnessLevels covers the mapping that
// keeps a project-level effort configured for another harness from breaking a
// DeepSeek Harness session: Harness accepts only off, low, high, and max.
func TestSessionOptionsMapAOVocabularyOntoHarnessLevels(t *testing.T) {
	cases := map[string]string{
		"none":    "off",
		"minimal": "off",
		"off":     "off",
		"low":     "low",
		"medium":  "high",
		"high":    "high",
		"xhigh":   "max",
		"max":     "max",
		// AO values are matched case-insensitively.
		"HIGH": "high",
	}
	for input, want := range cases {
		options := sessionOptions(ports.ChatTurnSettings{Effort: input})
		if len(options) != 1 || options[0].ID != "reasoning_effort" {
			t.Fatalf("effort %q produced %#v", input, options)
		}
		if options[0].Value != want {
			t.Fatalf("effort %q mapped to %q, want %q", input, options[0].Value, want)
		}
	}
}

func TestSessionOptionsPassThroughUnknownEffort(t *testing.T) {
	options := sessionOptions(ports.ChatTurnSettings{Effort: "turbo"})
	if len(options) != 1 || options[0].Value != "turbo" {
		t.Fatalf("options = %#v, want the value passed through", options)
	}
}

func TestValidateTurnSettings(t *testing.T) {
	cases := []struct {
		name    string
		effort  string
		wantErr bool
	}{
		{name: "empty effort defers to Harness", effort: ""},
		{name: "harness level", effort: "high"},
		{name: "mapped AO level", effort: "medium"},
		{name: "unmappable level", effort: "turbo", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateTurnSettings(ports.PermissionModeDefault, ports.ChatTurnSettings{Effort: tc.effort})
			if tc.wantErr != (err != nil) {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

// TestValidateTurnSettingsAcceptsOpaqueModelValues keeps AO out of Harness's
// model vocabulary: a session's model is the JSON array string its own catalog
// advertised, not a provider/model pair.
func TestValidateTurnSettingsAcceptsOpaqueModelValues(t *testing.T) {
	err := validateTurnSettings(ports.PermissionModeDefault, ports.ChatTurnSettings{
		Model: `["deepseek-official","deepseek-v4-pro"]`,
	})
	if err != nil {
		t.Fatalf("err = %v, want the opaque value accepted", err)
	}
}

func TestPermissionPolicyAnswersOnceForAutoAndEdits(t *testing.T) {
	allow := acpsdk.PermissionOptionKindAllowOnce
	edit := acpsdk.ToolKindEdit
	params := func(kind *acpsdk.ToolKind) acpsdk.RequestPermissionRequest {
		return acpsdk.RequestPermissionRequest{
			ToolCall: acpsdk.ToolCallUpdate{Kind: kind},
			Options: []acpsdk.PermissionOption{
				{OptionId: "reject", Kind: acpsdk.PermissionOptionKindRejectOnce},
				{OptionId: "allow", Kind: allow},
			},
		}
	}

	// auto answers any request, once.
	if id, ok := permissionPolicy(ports.PermissionModeAuto, params(nil)); !ok || id != "allow" {
		t.Fatalf("auto = (%q, %v), want (allow, true)", id, ok)
	}
	// accept-edits answers edit-like tools only.
	if id, ok := permissionPolicy(ports.PermissionModeAcceptEdits, params(&edit)); !ok || id != "allow" {
		t.Fatalf("accept-edits on edit = (%q, %v), want (allow, true)", id, ok)
	}
	if _, ok := permissionPolicy(ports.PermissionModeAcceptEdits, params(nil)); ok {
		t.Fatal("accept-edits answered a non-edit tool")
	}
	// default stays out of the way entirely.
	if _, ok := permissionPolicy(ports.PermissionModeDefault, params(&edit)); ok {
		t.Fatal("default mode answered a permission request")
	}
}

func TestHarnessConstantMatchesTheRegistryKey(t *testing.T) {
	if domain.HarnessDeepSeek != "deepseek-harness" {
		t.Fatalf("harness = %q", domain.HarnessDeepSeek)
	}
}
