package zcode

import (
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func TestNativePermissionBoundaryAndToolCompletion(t *testing.T) {
	for _, tc := range []struct {
		event string
		want  domain.ActivityState
	}{
		{"permission-request", domain.ActivityWaitingInput},
		{"post-tool-use", domain.ActivityActive},
		{"post-tool-use-failure", domain.ActivityActive},
	} {
		got, ok := DeriveActivityState(tc.event, nil)
		if !ok || got != tc.want {
			t.Fatalf("%s = %s,%v; want %s", tc.event, got, ok, tc.want)
		}
	}
	for _, event := range []string{"session-start", "user-prompt-submit", "stop", "session-end"} {
		if _, ok := DeriveActivityState(event, nil); ok {
			t.Fatalf("%s invented a final activity boundary", event)
		}
	}
}

func TestTerminalComposerReconcilesNativeHookVetoes(t *testing.T) {
	for _, tc := range []struct {
		name, output string
		want         domain.ActivityState
		known        bool
	}{
		{"idle", "╭─ Build ─╮\n│ Type a prompt │\n│ glm zai | high │\n╰─────────╯\n", domain.ActivityIdle, true},
		{"stop continuation", "╭─ Build ─╮\n│ Type to queue input │\n╰─────────╯\n⠋ esc to interrupt", domain.ActivityActive, true},
		{"busy draft", "╭─ Build ─╮\n│ my draft │\n╰─────────╯\n⠋ esc to interrupt", domain.ActivityActive, true},
		{"login", "No available models. Configure a provider or sign in with /login.\n╭─ Build ─╮\n│ Type a prompt │\n╰─────────╯", domain.ActivityWaitingInput, true},
		{"Chinese idle", "╭─ Build ─╮\n│ 输入提示词 │\n╰─────────╯", domain.ActivityIdle, true},
		{"transcript", "The docs say Type a prompt and esc to interrupt.", "", false},
		{"edited draft", "╭─ Build ─╮\n│ unfinished user draft │\n╰─────────╯", "", false},
		{"newest composer wins", "╭─ Build ─╮\n│ Type to queue input │\n╰─────────╯\n⠋ esc to interrupt\n╭─ Build ─╮\n│ Type a prompt │\n╰─────────╯", domain.ActivityIdle, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, known := (&Plugin{}).DetectTerminalActivity(tc.output)
			if got != tc.want || known != tc.known {
				t.Fatalf("got %s,%v; want %s,%v", got, known, tc.want, tc.known)
			}
		})
	}
}
