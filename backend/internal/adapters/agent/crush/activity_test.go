package crush

import (
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func TestDeriveActivityStateReturnsFalse(t *testing.T) {
	state, ok := DeriveActivityState("some-event", []byte("payload"))
	if ok {
		t.Fatalf("unexpected ok: got true, want false (DeriveActivityState is a no-op for Crush)")
	}
	if state != "" {
		t.Fatalf("unexpected non-empty state: got %q", state)
	}
}

func TestDetectTerminalActivity(t *testing.T) {
	// These snippets mirror tmux capture-pane output from Crush v0.80.0:
	// the current composer row is prefixed with ">", and Crush paints ":::" rows
	// below it. Ready... was observed on fresh startup and Ready? after a turn.
	tests := []struct {
		name   string
		output string
		want   domain.ActivityState
		ok     bool
	}{
		{
			name: "idle ready prompt",
			output: "chat transcript\n" +
				"\x1b[2m  > Ready...\x1b[0m\n" +
				"\x1b[32m::: \x1b[0m\n" +
				"\x1b[32m::: \x1b[0m\n" +
				"gpt-5 · coding-session · 12k tokens\n",
			want: domain.ActivityIdle,
			ok:   true,
		},
		{
			name: "completed turn back at prompt",
			output: "chat transcript\n" +
				"OK\n" +
				"GLM-4.6 via Z.AI 6s\n" +
				"\x1b[2m  > Ready?\x1b[0m\n" +
				"\x1b[32m::: \x1b[0m\n" +
				"\x1b[32m::: \x1b[0m\n" +
				"gpt-5 · coding-session · 12k tokens\n",
			want: domain.ActivityIdle,
			ok:   true,
		},
		{
			name: "active working prompt",
			output: "assistant text\n" +
				"Thinking..\n" +
				"\x1b[2m  > Working!\x1b[0m\n" +
				"\x1b[32m::: \x1b[0m\n" +
				"\x1b[32m::: \x1b[0m\n" +
				"gpt-5 · coding-session · 12k tokens\n",
			want: domain.ActivityActive,
			ok:   true,
		},
		{
			name: "active interrupt hint",
			output: "running bash\n" +
				"Working (13s · esc to interrupt)\n",
			want: domain.ActivityActive,
			ok:   true,
		},
		{
			name: "permission prompt",
			output: "Permission Required\n" +
				"Tool Bash\n" +
				"Allow  Allow for Session\n" +
				"Deny\n",
			want: domain.ActivityWaitingInput,
			ok:   true,
		},
		{
			name: "agent user input prompt",
			output: "◆ Request user input Verify\n" +
				"Please confirm the deployment target\n",
			want: domain.ActivityWaitingInput,
			ok:   true,
		},
		{
			name: "newest marker wins",
			output: "\x1b[2m  > Ready...\x1b[0m\n" +
				"Working (13s · esc to interrupt)\n",
			want: domain.ActivityActive,
			ok:   true,
		},
		{
			name:   "transcript text rejected",
			output: "The UI may say Ready... or Thinking... depending on state.\n",
		},
		{
			name:   "confirmation prose rejected",
			output: "The instructions say press enter to confirm or esc to go back.\n",
		},
		{
			name:   "prompt marker in transcript rejected without known placeholder",
			output: "> Explain the codebase\n",
		},
		{
			name: "historical marker outside lookback rejected",
			output: "> Ready...\n1\n2\n3\n4\n5\n6\n7\n8\n9\n10\n" +
				"11\n12\n13\n14\n15\n16\n17\n18\n19\n20\n" +
				"21\n22\n23\n24\n25\n26\n27\n28\n29\n30\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := (&Plugin{}).DetectTerminalActivity(tt.output)
			if got != tt.want || ok != tt.ok {
				t.Fatalf("DetectTerminalActivity() = (%q, %v), want (%q, %v)", got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestContinuouslyDetectTerminalActivity(t *testing.T) {
	if !(&Plugin{}).ContinuouslyDetectTerminalActivity() {
		t.Fatal("ContinuouslyDetectTerminalActivity() = false, want true")
	}
}
