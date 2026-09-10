package cline

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func readClineFixture(t *testing.T, name string) string {
	t.Helper()
	output, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(output)
}

func TestDetectTerminalActivityClineFrames(t *testing.T) {
	tests := []struct {
		name    string
		fixture string
		want    domain.ActivityState
	}{
		{"completed turn at empty composer", "idle_composer.txt", domain.ActivityIdle},
		{"active generation", "active_generation.txt", domain.ActivityActive},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := (&Plugin{}).DetectTerminalActivity(readClineFixture(t, tt.fixture))
			if got != tt.want || !ok {
				t.Fatalf("DetectTerminalActivity(%s) = (%q, %v), want (%q, true)", tt.fixture, got, ok, tt.want)
			}
		})
	}
}

func TestDetectTerminalActivityUsesNewestMarker(t *testing.T) {
	idle := readClineFixture(t, "idle_composer.txt")
	active := readClineFixture(t, "active_generation.txt")
	tests := []struct {
		name   string
		output string
		want   domain.ActivityState
	}{
		{"generation after old composer", idle + "\n" + active, domain.ActivityActive},
		{"composer after old generation", active + "\n" + idle, domain.ActivityIdle},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := (&Plugin{}).DetectTerminalActivity(tt.output)
			if got != tt.want || !ok {
				t.Fatalf("DetectTerminalActivity() = (%q, %v), want (%q, true)", got, ok, tt.want)
			}
		})
	}
}

func TestDetectTerminalActivityRejectsTranscriptText(t *testing.T) {
	output := "The docs call the placeholder Ask anything... and the toggle Plan / Act (Tab).\n"
	got, ok := (&Plugin{}).DetectTerminalActivity(output)
	if ok {
		t.Fatalf("DetectTerminalActivity(transcript) = (%q, true), want no signal", got)
	}
}

func TestDetectTerminalActivityDoesNotOverrideToolApproval(t *testing.T) {
	got, ok := (&Plugin{}).DetectTerminalActivity(readClineFixture(t, "tool_approval.txt"))
	if ok {
		t.Fatalf("DetectTerminalActivity(tool approval) = (%q, true), want no signal", got)
	}
}

func TestClineManagedHooksClearCompletedTurns(t *testing.T) {
	want := map[string]string{
		"TaskComplete": "stop",
	}
	for _, spec := range clineManagedHooks {
		if subcommand, ok := want[spec.Event]; ok {
			if spec.Subcommand != subcommand {
				t.Fatalf("%s subcommand = %q, want %q", spec.Event, spec.Subcommand, subcommand)
			}
			delete(want, spec.Event)
		}
	}
	for event := range want {
		t.Errorf("missing managed %s hook", event)
	}
}
