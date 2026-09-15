//go:build chatui_regression

package chat_test

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	chatsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/chat"
)

// TestChatUIRegressionDraftDeliveryRecoveryIsAtMostOnce selects the ordinary
// package contracts for both duplicate prevention and eventual recovery across
// controller restart. The opt-in runner invokes this stable name alongside MQA-04.
func TestChatUIRegressionDraftDeliveryRecoveryIsAtMostOnce(t *testing.T) {
	t.Run("steer", TestReservedSteerStaysUncertainAcrossRetryAndRestart)
	t.Run("inline edit", TestEditCompletionGapStaysUncertainAcrossRetryAndControllerRestart)
	t.Run("reserved edit resumes", TestReservedEditRecoversAfterControllerStopAndResume)
	t.Run("completed edit repairs receipt", TestCompletedEditRepairsReceiptAfterControllerRestart)
}

// The provider-history recovery may replay the same native archive again after
// a later controller restart. MQA-06 requires stable event identities to remain
// exactly-once in AO and guarantees that replay never writes to the worktree.
func TestChatUIRegressionProviderHistoryRecoveryDeduplicatesReplayWithoutWorktreeMutation(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	rec, found, err := st.GetSession(ctx, testSession)
	if err != nil || !found {
		t.Fatalf("load session: found=%v err=%v", found, err)
	}
	rec.Metadata.LatestUserPrompt = "poisoned user checkpoint"
	rec.Metadata.LatestAssistantUpdate = "poisoned assistant checkpoint"
	rec.Metadata.ConversationCheckpointState = domain.ConversationCheckpointLegacy
	if err := st.UpdateSession(ctx, rec); err != nil {
		t.Fatalf("seed poisoned checkpoint: %v", err)
	}

	worktree := t.TempDir()
	sentinel := filepath.Join(worktree, "unchanged.txt")
	if err := os.WriteFile(sentinel, []byte("unchanged\n"), 0o644); err != nil {
		t.Fatalf("seed worktree: %v", err)
	}
	history := []ports.ChatEvent{
		{Kind: ports.ChatEventTurnStarted, ProviderEventID: "history-start", ProviderTurnID: "native-turn-1"},
		{Kind: ports.ChatEventUserMessageCompleted, ProviderEventID: "history-user", ProviderTurnID: "native-turn-1", ProviderItemID: "native-user-1", Text: "Provider replay user"},
		{Kind: ports.ChatEventMessageCompleted, ProviderEventID: "history-assistant", ProviderTurnID: "native-turn-1", ProviderItemID: "native-assistant-1", Text: "Provider replay assistant"},
		{Kind: ports.ChatEventTurnCompleted, ProviderEventID: "history-completed", ProviderTurnID: "native-turn-1", TurnState: domain.TurnStateCompleted},
	}
	newHistoryConversation := func() *nativeHistoryConversation {
		return &nativeHistoryConversation{fakeConversation: newFakeConversation(), events: history}
	}
	driver := &sequenceDriver{conversations: []ports.ChatConversation{
		newHistoryConversation(), newHistoryConversation(),
	}}
	var ids atomic.Int64
	svc := chatsvc.New(chatsvc.Options{
		Store: st, Sessions: st,
		Reader: chatsvc.SnapshotReaderFunc(func(ctx context.Context, conversationID string) (chatsvc.ConversationRows, error) {
			rows, err := st.LoadConversationSnapshot(ctx, conversationID)
			if err != nil {
				return chatsvc.ConversationRows{}, err
			}
			return chatsvc.ConversationRows{
				Conversation: rows.Conversation, Turns: rows.Turns,
				Messages: rows.Messages, Activities: rows.Activities,
			}, nil
		}),
		Drivers: fakeRegistry{driver: driver},
		Log:     slog.New(slog.DiscardHandler),
		NewID:   func() string { return fmt.Sprintf("mqa06-%d", ids.Add(1)) },
	})
	t.Cleanup(func() { _ = svc.Stop(context.Background(), testSession) })
	start := func() *chatsvc.Controller {
		ctrl, err := svc.Start(ctx, chatsvc.StartConfig{
			SessionID: testSession, ProjectID: testProject, Harness: domain.HarnessCodex,
			WorkspacePath: worktree, ProviderConversationID: "thread-1", RequireNativeHistory: true,
			HistoryPolicy: domain.SessionInterfaceTransitionHistoryProvider,
		})
		if err != nil {
			t.Fatalf("provider-history start: %v", err)
		}
		return ctrl
	}

	first := start()
	conversationID := first.ConversationID()
	if err := svc.Stop(ctx, testSession); err != nil {
		t.Fatalf("stop first controller: %v", err)
	}
	second := start()
	if second.ConversationID() != conversationID {
		t.Fatalf("conversation changed across replay: %q -> %q", conversationID, second.ConversationID())
	}
	snapshot, err := st.LoadConversationSnapshot(ctx, conversationID)
	if err != nil {
		t.Fatalf("load deduplicated snapshot: %v", err)
	}
	if len(snapshot.Turns) != 1 || len(snapshot.Messages) != 2 ||
		snapshot.Messages[0].Text != "Provider replay user" ||
		snapshot.Messages[1].Text != "Provider replay assistant" {
		t.Fatalf("replayed snapshot duplicated or changed: turns=%#v messages=%#v", snapshot.Turns, snapshot.Messages)
	}
	if got, err := os.ReadFile(sentinel); err != nil || string(got) != "unchanged\n" {
		t.Fatalf("provider replay mutated worktree: content=%q err=%v", got, err)
	}
}
