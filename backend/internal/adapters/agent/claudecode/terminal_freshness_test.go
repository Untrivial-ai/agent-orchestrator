package claudecode

import (
	"strings"
	"testing"
)

func TestInspectTerminalSurfaceOnlyProvesUntouchedClaudeComposer(t *testing.T) {
	header := " ▐▛███▜▌   Claude Code v2.1.233\n▝▜█████▛▘  Opus · Claude Team\n  ▘▘ ▝▝    /workspace\n\n"
	rule := strings.Repeat("─", 48)
	composer := rule + "\n❯ \n" + rule + "\n⏵⏵ auto mode on"
	for _, tc := range []struct {
		name   string
		output string
		want   bool
	}{
		{"initial composer", header + composer, true},
		{"animated banner", strings.Replace(header, "Claude Code v", "Claude CodClaude█Code v", 1) + composer, true},
		{"header missing", composer, false},
		{"composer missing", header, false},
		{"draft", header + strings.Replace(composer, "❯ ", "❯ keep this draft", 1), false},
		{"previous prompt", header + "❯ hello\n\n" + composer, false},
		{"assistant output", header + "⏺ Hello\n\n" + composer, false},
		{"active", header + "✻ Working… (esc to interrupt)\n" + composer, false},
		{"decision", header + "Allow this?\n❯ 1. Yes\n  2. No\nEsc to cancel", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := New().InspectTerminalSurface(tc.output)
			if got.NativeConversationNotStarted != tc.want {
				t.Fatalf("untouched = %v, want %v: %+v", got.NativeConversationNotStarted, tc.want, got)
			}
		})
	}
}
