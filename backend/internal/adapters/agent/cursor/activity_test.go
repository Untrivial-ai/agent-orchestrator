package cursor

import (
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestDeriveActivityState(t *testing.T) {
	tests := []struct {
		event  string
		want   domain.ActivityState
		wantOK bool
	}{
		{"session-start", domain.ActivityActive, true},
		{"user-prompt-submit", domain.ActivityActive, true},
		{"stop", domain.ActivityIdle, true},
		{"after-shell-execution", domain.ActivityActive, true},
		{"after-mcp-execution", domain.ActivityActive, true},
		{"post-tool-use", domain.ActivityActive, true},
		{"post-tool-use-failure", domain.ActivityActive, true},
		{"before-shell-execution", "", false},
		{"before-mcp-execution", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.event, func(t *testing.T) {
			got, ok := DeriveActivityState(tt.event, []byte(`{}`))
			if ok != tt.wantOK || got != tt.want {
				t.Fatalf("DeriveActivityState(%q) = (%q, %v), want (%q, %v)", tt.event, got, ok, tt.want, tt.wantOK)
			}
		})
	}
}

func TestEvaluatePermission(t *testing.T) {
	tests := []struct {
		name      string
		mode      ports.PermissionMode
		wantPerm  string
		wantState domain.ActivityState
	}{
		{"default asks", ports.PermissionModeDefault, "ask", domain.ActivityBlocked},
		{"accept-edits asks", ports.PermissionModeAcceptEdits, "ask", domain.ActivityBlocked},
		{"auto allows", ports.PermissionModeAuto, "allow", domain.ActivityActive},
		{"bypass allows", ports.PermissionModeBypassPermissions, "allow", domain.ActivityActive},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := EvaluatePermission(tt.mode, "before-shell-execution", []byte(`{"command":"git status"}`))
			if got.Permission != tt.wantPerm || got.State != tt.wantState || !got.ReportActivity {
				t.Fatalf("EvaluatePermission() = %+v, want permission=%q state=%q report=true", got, tt.wantPerm, tt.wantState)
			}
		})
	}
}

func TestHookToolName(t *testing.T) {
	tests := []struct {
		event   string
		payload string
		want    string
	}{
		{"before-shell-execution", `{"command":"git status"}`, "git status"},
		{"after-shell-execution", `{"command":"npm test"}`, "npm test"},
		{"before-mcp-execution", `{"tool_name":"search"}`, "search"},
		{"after-mcp-execution", `{"tool_name":"fetch"}`, "fetch"},
	}
	for _, tt := range tests {
		t.Run(tt.event, func(t *testing.T) {
			if got := HookToolName(tt.event, []byte(tt.payload)); got != tt.want {
				t.Fatalf("HookToolName(%q) = %q, want %q", tt.event, got, tt.want)
			}
		})
	}
}
