package daemon

import (
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/config"
)

func TestGithubActorEventsGatesEachEventSeparately(t *testing.T) {
	on := config.Config{}
	on.Telemetry.Events = true
	if got := githubActorEvents(on); !got["ao.session.spawned"] || !got["ao.github.connected"] {
		t.Fatalf("telemetry on: %v, want both events", got)
	}
	spawnedOff := on
	spawnedOff.Telemetry.DisabledEvents = []string{"ao.session.spawned"}
	if got := githubActorEvents(spawnedOff); got["ao.session.spawned"] || !got["ao.github.connected"] {
		t.Fatalf("spawned disabled: %v, want only ao.github.connected", got)
	}
	if got := githubActorEvents(config.Config{}); len(got) != 0 {
		t.Fatalf("telemetry off: %v, want none", got)
	}
}
