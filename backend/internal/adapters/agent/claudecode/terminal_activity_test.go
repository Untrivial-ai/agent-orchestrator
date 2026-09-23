package claudecode

import (
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// claudeAbortedTurnScreen reproduces the live pane of a Claude Code session
// whose turn died on an expired OAuth login: the turn's Stop hook never fired,
// the composer holds an unsent user draft, and the CLI sits idle at the
// prompt. Captured plain (tmux capture-pane without -e), which is exactly what
// the activity observer's GetOutput provides.
func claudeAbortedTurnScreen(draft string) string {
	rule := strings.Repeat("─", 48)
	return "⏺ Stopped watching Artifact: \"scm-observer.md\" (connection lost)\n" +
		"\n" +
		"⏺ Login expired · Please run /login\n" +
		"\n" +
		"✻ Worked for 0s\n" +
		"\n" +
		rule + "\n" +
		draft +
		rule + "\n" +
		"\n" +
		"  ⏵⏵ bypass permissions on (shift+tab to cycle) · PR #4090\n" +
		"  ⧉  scm-observer"
}

func TestDetectTerminalActivityAuthoritativeIdleAfterAbortedTurn(t *testing.T) {
	plugin := &Plugin{}
	tests := []struct {
		name   string
		output string
	}{
		{
			name: "login expired with staged draft",
			output: claudeAbortedTurnScreen("❯ so btw, the status isn't permanently stuck at \"Checking merge readiness\". It's stuck at that for\n" +
				"  most of the time but keeps showing the merge status once in a while.\n"),
		},
		{
			name:   "login expired with empty composer",
			output: claudeAbortedTurnScreen("❯\n"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state, ok := plugin.DetectTerminalActivity(tt.output)
			if !ok || state != domain.ActivityIdle {
				t.Fatalf("DetectTerminalActivity = (%q, %v), want (idle, true)", state, ok)
			}
		})
	}
}

func TestDetectTerminalActivityFailsClosedOffIdle(t *testing.T) {
	plugin := &Plugin{}
	rule := strings.Repeat("─", 48)
	tests := []struct {
		name   string
		output string
	}{
		{
			name:   "active spinner row",
			output: "✶ Generating… (esc to interrupt · 2s)\n" + rule + "\n❯\n" + rule,
		},
		{
			name: "active with interrupt hint in footer",
			output: "✻ Computing… (24s · ↓ 114 tokens)\n" + rule + "\n❯\n" + rule +
				"\n⏵⏵ auto mode on (shift+tab to cycle) · esc to interrupt · ← for agents",
		},
		{
			name:   "permission dialog",
			output: "Do you want to proceed?\n❯ 1. Yes\n  2. No\nPress enter to confirm",
		},
		{
			name:   "no recognizable surface",
			output: "plain build output\nwithout any provider chrome\n",
		},
		{
			name:   "empty capture",
			output: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state, ok := plugin.DetectTerminalActivity(tt.output)
			if ok {
				t.Fatalf("DetectTerminalActivity = (%q, true), want not authoritative", state)
			}
		})
	}
}
