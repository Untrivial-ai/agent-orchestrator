package agy

import (
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func TestDetectTerminalActivity(t *testing.T) {
	plugin := New()

	tests := []struct {
		name          string
		output        string
		expectedState domain.ActivityState
		expectedValid bool
	}{
		{
			name: "aborted turn is idle",
			output: `some transcript history
> explain why it says thinking...
more transcript history
⌊ Interrupted · What should Antigravity CLI do instead?
> 
? for shortcuts`,
			expectedState: domain.ActivityIdle,
			expectedValid: true,
		},
		{
			name: "active thinking marker prevents idle",
			output: `⌊ Interrupted · What should Antigravity CLI do instead?
> 
? for shortcuts
thinking...`,
			expectedState: "",
			expectedValid: false,
		},
		{
			name: "active esc to interrupt marker prevents idle",
			output: `⌊ Interrupted · What should Antigravity CLI do instead?
> 
? for shortcuts
(esc to interrupt)`,
			expectedState: "",
			expectedValid: false,
		},
		{
			name: "active ctrl c to cancel marker prevents idle",
			output: `⌊ Interrupted · What should Antigravity CLI do instead?
> 
? for shortcuts
(ctrl+c to cancel)`,
			expectedState: "",
			expectedValid: false,
		},
		{
			name:          "empty output is ignored",
			output:        ``,
			expectedState: "",
			expectedValid: false,
		},
		{
			name: "incomplete output without footer is ignored",
			output: `⌊ Interrupted · What should Antigravity CLI do instead?
> `,
			expectedState: "",
			expectedValid: false,
		},
		{
			name: "incomplete output without prompt is ignored",
			output: `⌊ Interrupted · What should Antigravity CLI do instead?
? for shortcuts`,
			expectedState: "",
			expectedValid: false,
		},
		{
			name: "transcript matches do not spoof active when footer is present",
			output: `> explain why it says (esc to interrupt)
thinking about it...
⌊ Interrupted · What should Antigravity CLI do instead?
> 
? for shortcuts`,
			expectedState: domain.ActivityIdle,
			expectedValid: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state, valid := plugin.DetectTerminalActivity(tt.output)
			if valid != tt.expectedValid {
				t.Errorf("expected valid=%v, got %v", tt.expectedValid, valid)
			}
			if state != tt.expectedState {
				t.Errorf("expected state=%v, got %v", tt.expectedState, state)
			}
		})
	}
}
