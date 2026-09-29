package authprobe

import (
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// Measured from kiro-cli 2.21.0 on macOS with an isolated HOME: signed out,
// `whoami --format json` prints this and exits 1. It contains none of the
// prose the classifier looked for, so it read as unknown.
// https://github.com/Untrivial-ai/agent-orchestrator/issues/5048
func TestStatusFromTextReadsANullAccountAsSignedOut(t *testing.T) {
	for _, out := range []string{
		`{"account":null}`,
		`{"account": null}`,
		"{\n  \"account\": null\n}\n",
	} {
		if got := StatusFromText(out); got != ports.AgentAuthStatusUnauthorized {
			t.Errorf("StatusFromText(%q) = %q, want unauthorized", out, got)
		}
	}
}

// A populated account must not be dragged into the signed-out bucket by the
// substring match.
func TestStatusFromTextLeavesAPopulatedAccountAlone(t *testing.T) {
	if got := StatusFromText(`{"account":{"id":"abc"}}`); got == ports.AgentAuthStatusUnauthorized {
		t.Fatalf("StatusFromText = %q, want anything but unauthorized", got)
	}
}
