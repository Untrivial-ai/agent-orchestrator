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
