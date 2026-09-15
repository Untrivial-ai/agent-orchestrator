package lifecycle

import (
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func TestMarkChatReconnectedPreservesActivityAndRecency(t *testing.T) {
	for _, state := range []domain.ActivityState{domain.ActivityActive, domain.ActivityIdle, domain.ActivityBlocked, domain.ActivityWaitingInput} {
		t.Run(string(state), func(t *testing.T) {
			m, st, _ := newManager()
			rec := working("reconnect")
			rec.Mode = domain.SessionModeChat
			rec.Activity = domain.Activity{State: state, LastActivityAt: time.Unix(100, 0)}
			rec.FirstSignalAt = time.Unix(90, 0)
			rec.Metadata.ProviderConversationID = "live-provider"
			rec.Metadata.ControllerGeneration = "claimed-generation"
			st.sessions[rec.ID] = rec
			if err := m.MarkChatReconnected(ctx, rec.ID, rec.Metadata); err != nil {
				t.Fatal(err)
			}
			got := st.sessions[rec.ID]
			if got.Activity != rec.Activity || !got.UpdatedAt.Equal(rec.UpdatedAt) || !got.FirstSignalAt.Equal(rec.FirstSignalAt) {
				t.Fatalf("reconnect changed activity/recency: before=%+v after=%+v", rec, got)
			}
		})
	}
}

func TestMarkChatReconnectedRejectsChangedOwnership(t *testing.T) {
	for _, change := range []string{"terminated", "provider", "generation", "mode"} {
		t.Run(change, func(t *testing.T) {
			m, st, _ := newManager()
			rec := working("reconnect")
			rec.Mode = domain.SessionModeChat
			rec.Metadata.ProviderConversationID = "provider"
			rec.Metadata.ControllerGeneration = "generation"
			metadata := rec.Metadata
			switch change {
			case "terminated":
				rec.IsTerminated = true
			case "provider":
				rec.Metadata.ProviderConversationID = "replacement"
			case "generation":
				rec.Metadata.ControllerGeneration = "replacement"
			case "mode":
				rec.Mode = domain.SessionModeTUI
			}
			st.sessions[rec.ID] = rec
			if err := m.MarkChatReconnected(ctx, rec.ID, metadata); err == nil {
				t.Fatal("reconnected a different owner")
			}
			if st.sessions[rec.ID] != rec {
				t.Fatal("rejected reconnect mutated the session")
			}
		})
	}
}

func TestColdChatSpawnStillResetsActivity(t *testing.T) {
	m, st, _ := newManager()
	rec := working("cold-resume")
	rec.Mode = domain.SessionModeChat
	rec.Metadata.ProviderConversationID = "same-native-history"
	st.sessions[rec.ID] = rec
	if err := m.MarkSpawned(ctx, rec.ID, rec.Metadata); err != nil {
		t.Fatal(err)
	}
	if st.sessions[rec.ID].Activity.State != domain.ActivityIdle {
		t.Fatal("cold spawn inherited activity from a dead process")
	}
}
