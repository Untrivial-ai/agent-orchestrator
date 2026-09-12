package continueagent

import (
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func TestDetectTerminalActivityContinueQuestionPicker(t *testing.T) {
	output := `
○ Ask Question(Which background color do you want to change to red?)

 ⣿⣿⣿  ( 27s • esc to interrupt )
╭───────────────────────────────────────────────────────────────────────────────╮
│                                                                               │
│  ? Which background color do you want to change to red?                       │
│                                                                               │
│    ❯ App-wide background (body/root)                                          │
│      A specific component or page                                             │
│      A specific CSS variable/token                                            │
│      (or start typing for custom answer)                                      │
│                                                                               │
│  ↑/↓ navigate, Enter select                                                   │
│                                                                               │
╰───────────────────────────────────────────────────────────────────────────────╯
`
	got, ok := (&Plugin{}).DetectTerminalActivity(output)
	if got != domain.ActivityWaitingInput || !ok {
		t.Fatalf("DetectTerminalActivity() = (%q, %v), want (%q, true)", got, ok, domain.ActivityWaitingInput)
	}
}

func TestDetectTerminalActivityContinueNewestMarkerWins(t *testing.T) {
	output := `
○ Ask Question(Which background color do you want to change to red?)
│  ? Which background color do you want to change to red?
│  ↑/↓ navigate, Enter select

 ⣿⣿⣿  ( 2s • esc to interrupt )
`
	got, ok := (&Plugin{}).DetectTerminalActivity(output)
	if got != domain.ActivityActive || !ok {
		t.Fatalf("DetectTerminalActivity() = (%q, %v), want (%q, true)", got, ok, domain.ActivityActive)
	}
}

func TestDetectTerminalActivityContinueRejectsTranscriptText(t *testing.T) {
	output := "The UI says ↑/↓ navigate, Enter select when asking a question.\n"
	got, ok := (&Plugin{}).DetectTerminalActivity(output)
	if ok {
		t.Fatalf("DetectTerminalActivity() = (%q, true), want no signal", got)
	}
}

func TestDetectTerminalActivityContinueIgnoresCompletedQuestionInIdleComposer(t *testing.T) {
	output := `
○ Ask Question(Which background color do you want to change to red?)
│  ? Which background color do you want to change to red?
│  ↑/↓ navigate, Enter select

╭───────────────────────────────────────────────────────────────────────────────╮
│  ❯                                                                         │
╰───────────────────────────────────────────────────────────────────────────────╯
`
	got, ok := (&Plugin{}).DetectTerminalActivity(output)
	if !ok || got != domain.ActivityIdle {
		t.Fatalf("DetectTerminalActivity() = (%q, %v), want (%q, true)", got, ok, domain.ActivityIdle)
	}
}

func TestDetectTerminalActivityContinueIgnoresCompletedQuestionSpinnerBeforeIdleComposer(t *testing.T) {
	output := `
○ Ask Question(Which background color do you want to change to red?)

 ⣿⣿⣿  ( 27s • esc to interrupt )
╭───────────────────────────────────────────────────────────────────────────────╮
│  ? Which background color do you want to change to red?                       │
│    ❯ App-wide background (body/root)                                          │
│  ↑/↓ navigate, Enter select                                                   │
╰───────────────────────────────────────────────────────────────────────────────╯

╭───────────────────────────────────────────────────────────────────────────────╮
│  ❯                                                                            │
╰───────────────────────────────────────────────────────────────────────────────╯
`
	got, ok := (&Plugin{}).DetectTerminalActivity(output)
	if !ok || got != domain.ActivityIdle {
		t.Fatalf("DetectTerminalActivity() = (%q, %v), want (%q, true)", got, ok, domain.ActivityIdle)
	}
}

func TestComposerIsEmpty(t *testing.T) {
	idle := "│  ❯                                                                            │\n"
	if !(&Plugin{}).ComposerIsEmpty(idle) {
		t.Fatal("ComposerIsEmpty(idle composer) = false, want true")
	}
	if (&Plugin{}).ComposerIsEmpty("│  ❯ finish the fix                                                            │\n") {
		t.Fatal("ComposerIsEmpty(draft) = true, want false")
	}
}
