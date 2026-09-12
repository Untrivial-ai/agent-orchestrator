package aider

import (
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func TestDetectTerminalActivityReportsSubmittedPromptAsActive(t *testing.T) {
	tests := []struct {
		name   string
		output string
	}{
		{name: "default prompt", output: "> Update the app background to red\n"},
		{name: "chat mode prompt", output: "ask> Explain how this package works\n"},
		{name: "multiline mode prompt", output: "ask multi> Compare both implementations\n"},
		{name: "ANSI prompt", output: "\x1b[32m> Fix the failing test\x1b[0m\r\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := (&Plugin{}).DetectTerminalActivity(tt.output)
			if got != domain.ActivityActive || !ok {
				t.Fatalf("DetectTerminalActivity() = (%q, %v), want (%q, true)", got, ok, domain.ActivityActive)
			}
		})
	}
}

func TestDetectTerminalActivityBarePromptPreservesCurrentState(t *testing.T) {
	tests := []string{
		"> \n",
		"ask> \n",
		"> Previous task\nAssistant response\n> \n",
	}
	for _, output := range tests {
		got, ok := (&Plugin{}).DetectTerminalActivity(output)
		if ok {
			t.Fatalf("DetectTerminalActivity(%q) = (%q, true), want no signal", output, got)
		}
	}
}

func TestDetectTerminalActivityRejectsTranscriptText(t *testing.T) {
	output := "The Aider prompt looked like > Update the app background to red.\n"
	got, ok := (&Plugin{}).DetectTerminalActivity(output)
	if ok {
		t.Fatalf("DetectTerminalActivity() = (%q, true), want no signal", got)
	}
}

func TestContinuouslyDetectTerminalActivity(t *testing.T) {
	if !(&Plugin{}).ContinuouslyDetectTerminalActivity() {
		t.Fatal("ContinuouslyDetectTerminalActivity() = false, want true")
	}
}

func TestComposerIsEmpty(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   bool
	}{
		{name: "default prompt", output: "> \n", want: true},
		{name: "chat prompt", output: "ask> \n", want: true},
		{name: "submitted prompt", output: "> Fix the failing test\n", want: false},
		{name: "no prompt", output: "Loading model...\n", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := (&Plugin{}).ComposerIsEmpty(tt.output); got != tt.want {
				t.Fatalf("ComposerIsEmpty() = %v, want %v", got, tt.want)
			}
		})
	}
}
