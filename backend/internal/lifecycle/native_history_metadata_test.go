package lifecycle

import (
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestActivityNativeIdentityScopesHistoryFacts(t *testing.T) {
	for _, scenario := range []string{"same_identity", "new_identity", "provider_identity_only", "stale_generation"} {
		t.Run(scenario, func(t *testing.T) {
			m, store, _ := newManager()
			rec := working("mer-1")
			rec.Metadata.RuntimeLaunchID = "launch-current"
			rec.Metadata.AgentSessionID = "native-A"
			rec.Metadata.LatestUserPrompt = "prompt A"
			rec.Metadata.LatestUserPromptAt = time.Unix(100, 0)
			rec.Metadata.LatestAssistantUpdate = "answer A"
			rec.Metadata.NativeTranscriptPath = "/transcript-A"
			store.sessions[rec.ID] = rec
			if scenario == "provider_identity_only" {
				rec.Metadata.AgentSessionID = ""
				rec.Metadata.ProviderConversationID = "native-A"
				store.sessions[rec.ID] = rec
			}
			signal := ports.ActivitySignal{LaunchID: "launch-current", AgentSessionID: "native-A"}
			if scenario != "same_identity" {
				signal.AgentSessionID = "native-B"
			}
			if scenario == "stale_generation" {
				signal.LaunchID = "launch-old"
			}
			if err := m.ApplyActivitySignal(ctx, rec.ID, signal); err != nil {
				t.Fatal(err)
			}
			got := store.sessions[rec.ID].Metadata
			if scenario == "new_identity" || scenario == "provider_identity_only" {
				if got.AgentSessionID != "native-B" || got.LatestUserPrompt != "" || !got.LatestUserPromptAt.IsZero() || got.LatestAssistantUpdate != "" || got.NativeTranscriptPath != "" {
					t.Fatalf("native A's history leaked into B: %+v", got)
				}
			} else if got.LatestUserPrompt != rec.Metadata.LatestUserPrompt || got.LatestAssistantUpdate != rec.Metadata.LatestAssistantUpdate || got.NativeTranscriptPath != rec.Metadata.NativeTranscriptPath || got.AgentSessionID != "native-A" {
				t.Fatalf("same-owner or stale signal erased history: %+v", got)
			}
			if store.sessions[rec.ID].Activity.State != domain.ActivityActive {
				t.Fatal("identity-only signal changed activity")
			}
		})
	}
}

func TestDelayedNativeHookCannotReplaceCurrentIdentity(t *testing.T) {
	m, store, _ := newManager()
	rec := working("mer-1")
	rec.Metadata.RuntimeLaunchID = "launch"
	store.sessions[rec.ID] = rec
	for _, signal := range []ports.ActivitySignal{
		{AgentSessionID: "A", Timestamp: time.Unix(100, 0), LatestUserPrompt: "A prompt"},
		{AgentSessionID: "B", Timestamp: time.Unix(200, 0), LatestUserPrompt: "B prompt", LatestAssistantUpdate: "B answer"},
		{AgentSessionID: "A", Timestamp: time.Unix(150, 0), LatestAssistantUpdate: "delayed A answer"},
	} {
		signal.LaunchID = "launch"
		if err := m.ApplyActivitySignal(ctx, rec.ID, signal); err != nil {
			t.Fatal(err)
		}
	}
	got := store.sessions[rec.ID].Metadata
	if got.AgentSessionID != "B" || got.LatestUserPrompt != "B prompt" || got.LatestAssistantUpdate != "B answer" {
		t.Fatalf("delayed same-launch hook destroyed B's facts: %+v", got)
	}
	if err := m.ApplyActivitySignal(ctx, rec.ID, ports.ActivitySignal{LaunchID: "launch", AgentSessionID: "A", Timestamp: time.Unix(300, 0), LatestUserPrompt: "new A prompt"}); err != nil {
		t.Fatal(err)
	}
	got = store.sessions[rec.ID].Metadata
	if got.AgentSessionID != "A" || got.LatestUserPrompt != "new A prompt" || got.LatestAssistantUpdate != "" {
		t.Fatalf("genuine return to A was rejected or inherited B: %+v", got)
	}
}

func TestReorderedHooksWithinNativeIdentityPreserveNewestFacts(t *testing.T) {
	m, store, _ := newManager()
	rec := working("mer-1")
	rec.Metadata.RuntimeLaunchID = "launch"
	store.sessions[rec.ID] = rec
	for _, signal := range []ports.ActivitySignal{
		{Event: "stop", Timestamp: time.Unix(200, 0), LatestAssistantUpdate: "current answer"},
		{Event: "user-prompt-submit", Timestamp: time.Unix(100, 0), LatestUserPrompt: "current prompt"},
		{Event: "stop", Timestamp: time.Unix(50, 0), LatestUserPrompt: "old prompt", LatestAssistantUpdate: "old answer"},
	} {
		signal.LaunchID, signal.AgentSessionID = "launch", "A"
		if err := m.ApplyActivitySignal(ctx, rec.ID, signal); err != nil {
			t.Fatal(err)
		}
	}
	got := store.sessions[rec.ID].Metadata
	if got.LatestUserPrompt != "current prompt" || got.LatestAssistantUpdate != "current answer" || !got.NativeIdentityObservedAt.Equal(time.Unix(200, 0)) {
		t.Fatalf("reordered same-identity facts were lost or regressed: %+v", got)
	}
}
