package chat

import (
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestSettingsFromConfigOptionsKeepsClaudeModelAndEffortAcrossRestart(t *testing.T) {
	settings, changed := settingsFromConfigOptions(domain.ConversationSettings{
		ApprovalMode: domain.PermissionModeBypassPermissions,
	}, []ports.ChatConfigOption{
		{ID: "model", Category: "model", Current: ports.ChatConfigOptionValue{Select: "sonnet"}},
		{ID: "effort", Category: "thought_level", Current: ports.ChatConfigOptionValue{Select: "high"}},
	})
	if !changed {
		t.Fatal("settings should change")
	}
	if settings.Model != "sonnet" || settings.ReasoningEffort != "high" || settings.ApprovalMode != domain.PermissionModeBypassPermissions {
		t.Fatalf("settings = %+v, want model and effort while preserving approval", settings)
	}
}

func TestPermissionConfigOptions(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  domain.PermissionMode
	}{{"manual", domain.PermissionModeDefault}, {"default", domain.PermissionModeDefault}, {"acceptEdits", domain.PermissionModeAcceptEdits}, {"auto", domain.PermissionModeAuto}, {"bypassPermissions", domain.PermissionModeBypassPermissions}, {"dontAsk", ""}, {"plan", ""}, {"custom", ""}} {
		input := []ports.ChatConfigOption{{ID: "mode", Current: ports.ChatConfigOptionValue{Select: tc.value}, Choices: []ports.ChatConfigOptionChoice{{Value: tc.value}}}}
		got := permissionConfigOptions(domain.HarnessClaudeCode, input)
		if got[0].Choices[0].PermissionMode != tc.want {
			t.Fatalf("%s mapping = %q", tc.value, got[0].Choices[0].PermissionMode)
		}
		settings, _ := settingsFromConfigOptions(domain.ConversationSettings{ApprovalMode: domain.PermissionModeAuto}, got)
		want := tc.want
		if want == "" {
			want = domain.PermissionModeAuto
		}
		if settings.ApprovalMode != want {
			t.Fatalf("%s settings=%q", tc.value, settings.ApprovalMode)
		}
		if input[0].Choices[0].PermissionMode != "" {
			t.Fatal("mutated provider catalog")
		}
		if other := permissionConfigOptions(domain.HarnessOpenCode, input); other[0].Choices[0].PermissionMode != "" {
			t.Fatal("mapped unknown provider")
		}
		input[0].ID = "model"
		if model := permissionConfigOptions(domain.HarnessClaudeCode, input); model[0].Choices[0].PermissionMode != "" {
			t.Fatal("mapped model choice")
		}
	}
}

func TestPermissionConfigOptionsLabelsOpenCodeTiers(t *testing.T) {
	// AO injects these as OpenCode agents; build and plan are OpenCode's own
	// execution modes and must stay out of the approvals vocabulary.
	for _, tc := range []struct {
		value string
		mode  domain.PermissionMode
		label string
	}{
		{"ao-default", domain.PermissionModeDefault, "Use agent permissions"},
		{"ao-accept-edits", domain.PermissionModeAcceptEdits, "Accept edits"},
		{"ao-auto", domain.PermissionModeAuto, "Auto-approve"},
		{"ao-bypass", domain.PermissionModeBypassPermissions, "Bypass permissions"},
		{"build", "", "build"},
		{"plan", "", "plan"},
	} {
		input := []ports.ChatConfigOption{{
			ID:      "mode",
			Current: ports.ChatConfigOptionValue{Select: tc.value},
			Choices: []ports.ChatConfigOptionChoice{{Value: tc.value, Name: tc.value}},
		}}
		got := permissionConfigOptions(domain.HarnessOpenCode, input)
		if got[0].Choices[0].PermissionMode != tc.mode || got[0].Choices[0].Name != tc.label {
			t.Fatalf("%s -> (%q, %q), want (%q, %q)",
				tc.value, got[0].Choices[0].PermissionMode, got[0].Choices[0].Name, tc.mode, tc.label)
		}
		settings, _ := settingsFromConfigOptions(
			domain.ConversationSettings{ApprovalMode: domain.PermissionModeDefault}, got)
		want := tc.mode
		if want == "" {
			want = domain.PermissionModeDefault
		}
		if settings.ApprovalMode != want {
			t.Fatalf("%s settings = %q, want %q", tc.value, settings.ApprovalMode, want)
		}
		if input[0].Choices[0].Name != tc.value {
			t.Fatal("mutated provider catalog")
		}
	}
}
