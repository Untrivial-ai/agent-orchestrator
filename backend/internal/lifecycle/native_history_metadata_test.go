package lifecycle

import (
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestActivityNativeIdentityScopesHistoryFacts(t *testing.T) {
	for _, scenario := range []string{"same_identity", "new_identity", "stale_generation"} {
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
			if scenario == "new_identity" {
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
