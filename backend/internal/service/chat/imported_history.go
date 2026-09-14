package chat

import (
	"context"
	"fmt"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/sessionimport"
)

func importedHistorySnapshot(ctx context.Context, rec domain.SessionRecord, before, limit int64) (Snapshot, error) {
	all, err := sessionimport.ReadMessages(ctx, rec.Harness, rec.Metadata.NativeTranscriptPath)
	if err != nil {
		return Snapshot{}, fmt.Errorf("read imported history: %w", err)
	}
	id := "import:" + string(rec.ID)
	out := Snapshot{ImportedHistory: true, SessionID: rec.ID, Harness: rec.Harness, Mode: domain.SessionModeChat, Controller: ports.ChatControllerStopped,
		Conversation: domain.ConversationRecord{ID: id, SessionID: rec.ID, ProviderTitle: rec.DisplayName, LatestSequence: int64(len(all))},
	}
	end := len(all)
	if before > 0 && before <= int64(end) {
		end = int(before - 1)
	}
	start := 0
	if limit > 0 && int64(end) > limit {
		start = end - int(limit)
	}
	for i := start; i < end; i++ {
		msg := all[i]
		msg.ConversationID = id
		msg.Sequence = int64(i + 1)
		msg.ID = id + ":" + msg.ID
		out.Messages = append(out.Messages, msg)
	}
	if len(out.Messages) > 0 {
		out.OldestSequence = out.Messages[0].Sequence
	}
	out.HasMoreBefore = start > 0
	return out, nil
}
