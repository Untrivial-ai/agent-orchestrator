package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func TestActivityProjectionCannotOverwriteHumanMessageWithUnchangedTimestamp(t *testing.T) {
	for _, offset := range []time.Duration{0, -time.Minute} {
		t.Run(offset.String(), func(t *testing.T) {
			s, sessionID, conversationID := conversationFixture(t)
			ctx := context.Background()
			before, _, err := s.GetSession(ctx, sessionID)
			if err != nil {
				t.Fatal(err)
			}
			before.UpdatedAt = histClock.Add(time.Hour)
			before.Metadata.LatestUserPrompt = "old prompt"
			before.Metadata.LatestUserPromptAt = histClock
			before.Metadata.LatestAssistantUpdate = "old answer"
			before.Metadata.ConversationCheckpointState = domain.ConversationCheckpointComplete
			before.Metadata.ConversationCheckpointGeneration = "gen-1"
			before.Metadata.ConversationCheckpointNativeID = "native-1"
			if err := s.UpdateSession(ctx, before); err != nil {
				t.Fatal(err)
			}
			before, _, err = s.GetSession(ctx, sessionID)
			if err != nil {
				t.Fatal(err)
			}
			created, err := s.AppendUserMessage(ctx, conversationID, sessionID, "gen-1", domain.ConversationMessage{
				ID: "new-message", Origin: domain.MessageOriginHuman, Text: "new prompt",
			}, "new-turn", before.UpdatedAt.Add(offset))
			if err != nil || !created {
				t.Fatalf("append message: created=%v err=%v", created, err)
			}
			after, _, err := s.GetSession(ctx, sessionID)
			if err != nil || !after.UpdatedAt.Equal(before.UpdatedAt) {
				t.Fatalf("fixture must retain updated_at: before=%v after=%v err=%v", before.UpdatedAt, after.UpdatedAt, err)
			}
			applied, err := s.UpdateSessionFromActivitySignal(ctx, before, before.Revision)
			if err != nil || applied {
				t.Fatalf("stale projection overwrote human checkpoint: applied=%v err=%v", applied, err)
			}
			after, _, err = s.GetSession(ctx, sessionID)
			if err != nil || after.Metadata.LatestUserPrompt != "new prompt" || after.Metadata.LatestAssistantUpdate != "" {
				t.Fatalf("human checkpoint lost: %+v err=%v", after.Metadata, err)
			}
		})
	}
}
