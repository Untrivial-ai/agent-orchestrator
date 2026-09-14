package acp

import (
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/persistenthost"
)

func TestACPIdentifiedEventFreshness(t *testing.T) {
	for _, tc := range []struct {
		name       string
		state      *persistenthost.ACPState
		freshTurn  bool
		activeTurn string
		sourceID   string
		want       bool
	}{
		{name: "replay prefix", state: &persistenthost.ACPState{EventIDPrefix: "acp-host:host:", EventSequence: 7}, sourceID: "acp-host:host:6"},
		{name: "replay boundary", state: &persistenthost.ACPState{EventIDPrefix: "acp-host:host:", EventSequence: 7}, sourceID: "acp-host:host:7"},
		{name: "fresh after reconnect", state: &persistenthost.ACPState{EventIDPrefix: "acp-host:host:", EventSequence: 7}, sourceID: "acp-host:host:8", want: true},
		{name: "another host", state: &persistenthost.ACPState{EventIDPrefix: "acp-host:host:", EventSequence: 7}, sourceID: "acp-host:other:8"},
		{name: "malformed sequence", state: &persistenthost.ACPState{EventIDPrefix: "acp-host:host:", EventSequence: 7}, sourceID: "acp-host:host:8:0"},
		{name: "old host replay", state: &persistenthost.ACPState{}, activeTurn: "turn", sourceID: "legacy-id"},
		{name: "old host new prompt", state: &persistenthost.ACPState{}, freshTurn: true, activeTurn: "turn", sourceID: "legacy-id", want: true},
		{name: "fresh launch prompt", freshTurn: true, activeTurn: "turn", sourceID: "host-id", want: true},
		{name: "turn changed during normalization", freshTurn: true, activeTurn: "replacement-turn", sourceID: "host-id"},
		{name: "anonymous", freshTurn: true, activeTurn: "turn"},
		{name: "replayed ID despite later turn", state: &persistenthost.ACPState{EventIDPrefix: "acp-host:host:", EventSequence: 7}, freshTurn: true, activeTurn: "turn", sourceID: "acp-host:host:6"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conversation := &conversation{
				proc: &process{terminate: func() error { return nil }}, liveState: tc.state,
				freshTurn: tc.freshTurn, activeTurn: tc.activeTurn,
			}
			if got := conversation.freshProviderEvent(tc.sourceID, "turn"); got != tc.want {
				t.Fatalf("freshness = %t, want %t", got, tc.want)
			}
			conversation.proc.terminate = nil
			if conversation.freshProviderEvent(tc.sourceID, "turn") {
				t.Fatal("non-host metadata was treated as proof of freshness")
			}
		})
	}
}
