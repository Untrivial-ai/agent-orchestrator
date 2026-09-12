package agy

import (
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// agyAbortedTurnScreen reproduces the live terminal pane of an Agy session
// whose in-flight tool execution was interrupted with Escape:
// the Stop hook never fired, but Agy printed the interruption banner,
// separator rule, prompt, and footer shortcuts bar.
func agyAbortedTurnScreen(promptLine string) string {
	rule := strings.Repeat("_", 65)
	return "• Thought for 5s, 454 tokens\n" +
		"  Assessing Engine's Non-Existence\n" +
		"\n" +
		"• Bash(uv run --directory backend python -c \") (ctrl+o to expand)\n" +
		"\n" +
		"  ⌊ Interrupted · What should Antigravity CLI do instead?\n" +
		rule + "\n" +
		promptLine + "\n" +
		"? for shortcuts                    accept-edits · Gemini 3.8 Flash · high\n"
}

func TestDetectTerminalActivityAuthoritativeIdleAfterAbortedTurn(t *testing.T) {
	plugin := New()
	tests := []struct {
		name   string
		output string
	}{
		{
			name:   "aborted turn sitting at empty prompt",
			output: agyAbortedTurnScreen("> "),
		},
		{
			name:   "aborted turn sitting at bare prompt",
			output: agyAbortedTurnScreen(">"),
		},
		{
			name:   "aborted turn sitting at prompt with block cursor",
			output: agyAbortedTurnScreen("> █"),
		},
		{
			name:   "aborted turn with user draft typed in prompt",
			output: agyAbortedTurnScreen("> run backend tests instead"),
		},
		{
			name: "fresh session idle prompt",
			output: strings.Repeat("_", 65) + "\n" +
				"> \n" +
				"? for shortcuts                    accept-edits · Gemini 3.8 Flash · high\n",
		},
		{
			name: "completed turn idle prompt",
			output: "• Thought for 6s, 617 tokens\n" +
				"  Done with task.\n" +
				strings.Repeat("_", 65) + "\n" +
				"> \n" +
				"? for shortcuts                    accept-edits · Gemini 3.8 Flash · high\n",
		},
		{
			name: "aborted turn sitting at prompt with esc to cancel footer",
			output: "• Bash(python -c \"import time; time.sleep(10)\") (ctrl+o to expand)\n\n" +
				"  ⌊ Interrupted · What should Antigravity CLI do instead?\n" +
				strings.Repeat("─", 65) + "\n" +
				">\n" +
				strings.Repeat("─", 65) + "\n" +
				"esc to cancel                                  Gemini 3.8 Flash · high\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state, ok := plugin.DetectTerminalActivity(tt.output)
			if !ok || state != domain.ActivityIdle {
				t.Fatalf("DetectTerminalActivity() = (%q, %v), want (%q, true)", state, ok, domain.ActivityIdle)
			}
		})
	}
}

func TestDetectTerminalActivityActiveExecution(t *testing.T) {
	plugin := New()
	tests := []struct {
		name   string
		output string
	}{
		{
			name:   "tool execution in flight with esc to interrupt",
			output: "• Bash(uv run --directory backend python -c \") (esc to interrupt)",
		},
		{
			name:   "tool execution in flight with ctrl+c to cancel",
			output: "• Search(Search reliability in app) (ctrl+c to cancel)",
		},
		{
			name:   "thinking indicator in flight",
			output: "Thinking... (esc to interrupt)",
		},
		{
			name:   "streaming token generation in flight",
			output: "⣽  Generating...\n─────────────────────────────────────────────────\n>\n─────────────────────────────────────────────────\nesc to cancel   Gemini 3.8 Flash · high",
		},
		{
			name: "scrollback with old footer followed by new active tool execution",
			output: "? for shortcuts                    accept-edits · Gemini 3.8 Flash · high\n" +
				"> run tests\n" +
				"• Bash(npm test) (esc to interrupt)\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state, ok := plugin.DetectTerminalActivity(tt.output)
			if !ok || state != domain.ActivityActive {
				t.Fatalf("DetectTerminalActivity() = (%q, %v), want (%q, true)", state, ok, domain.ActivityActive)
			}
		})
	}
}

func TestDetectTerminalActivityFailsClosedOffIdle(t *testing.T) {
	plugin := New()
	tests := []struct {
		name   string
		output string
	}{
		{
			name:   "empty output",
			output: "",
		},
		{
			name:   "only whitespace",
			output: "   \n\n\t  \n",
		},
		{
			name: "unrelated build output",
			output: "$ make test\n" +
				"PASS\n" +
				"ok  	command-line-arguments	0.012s\n",
		},
		{
			name:   "footer alone without prompt",
			output: "? for shortcuts                    accept-edits · Gemini 3.8 Flash · high\n",
		},
		{
			name:   "prompt alone without footer",
			output: "> \n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state, ok := plugin.DetectTerminalActivity(tt.output)
			if ok {
				t.Fatalf("DetectTerminalActivity() = (%q, %v), want (_, false)", state, ok)
			}
		})
	}
}
