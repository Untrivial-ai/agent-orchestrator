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

func TestPermissionConfigOptionsLeaveProviderCatalogUntouched(t *testing.T) {
	input := []ports.ChatConfigOption{{
		ID:      "mode",
		Current: ports.ChatConfigOptionValue{Select: "manual"},
		Choices: []ports.ChatConfigOptionChoice{{Value: "manual"}, {Value: "acceptEdits"}, {Value: "bypassPermissions"}},
	}}
	got := permissionConfigOptions(input)
	if len(got) != 1 || len(got[0].Choices) != 3 {
		t.Fatalf("options = %+v, want provider catalog passed through", got)
	}
	for _, choice := range got[0].Choices {
		if choice.PermissionMode != "" {
			t.Fatalf("opencode choice %q was mapped to AO permission mode %q", choice.Value, choice.PermissionMode)
		}
	}
}
