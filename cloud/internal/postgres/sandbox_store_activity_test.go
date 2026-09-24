package postgres

import (
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/pkg/contract"
	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/aoagents/agent-orchestrator/cloud/internal/worker"
)

func TestShouldApplyWorkerActivity(t *testing.T) {
	tests := []struct {
		name      string
		iface     domain.SessionInterface
		activity  worker.ActivityEvent
		wantApply bool
	}{
		{
			name:  "chat ignores inherited Claude session-end",
			iface: domain.SessionInterfaceChat,
			activity: worker.ActivityEvent{
				Harness: "claude-code", Event: "session-end", State: contract.ActivityExited,
			},
		},
		{
			name:  "chat keeps untagged native conversation identity",
			iface: domain.SessionInterfaceChat,
			activity: worker.ActivityEvent{
				Harness: "claude-code", Event: "session-start", AgentSessionID: "native-conversation",
			},
			wantApply: true,
		},
		{
			name:  "chat keeps late tagged TUI shutdown",
			iface: domain.SessionInterfaceChat,
			activity: worker.ActivityEvent{
				Harness: "claude-code", Event: "session-end", State: contract.ActivityIdle, SourceInterface: "tui",
			},
			wantApply: true,
		},
		{
			name:  "TUI keeps legacy untagged lifecycle events",
			iface: domain.SessionInterfaceTUI,
			activity: worker.ActivityEvent{
				Harness: "claude-code", Event: "user-prompt-submit", State: contract.ActivityActive,
			},
			wantApply: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := shouldApplyWorkerActivity(test.iface, test.activity); got != test.wantApply {
				t.Fatalf("shouldApplyWorkerActivity(%q, %#v) = %t, want %t", test.iface, test.activity, got, test.wantApply)
			}
		})
	}
}
