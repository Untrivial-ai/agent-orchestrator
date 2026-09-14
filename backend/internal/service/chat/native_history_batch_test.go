package chat_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	chatsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/chat"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
)

type countingHistoryStore struct {
	*sqlite.Store
	batches int
}

func (s *countingHistoryStore) ProjectProviderHistory(ctx context.Context, project func(context.Context) error) error {
	s.batches++
	return s.Store.ProjectProviderHistory(ctx, project)
}
func TestNativeHistoryReplayUsesBoundedBatchesAndPreservesEvents(t *testing.T) {
	st := &countingHistoryStore{Store: openStore(t)}
	events := []ports.ChatEvent{{Kind: ports.ChatEventTurnStarted, ProviderEventID: "start", ProviderTurnID: "turn"}}
	for i := range 1000 {
		events = append(events, ports.ChatEvent{Kind: ports.ChatEventActivityCompleted, ProviderEventID: fmt.Sprint("event-", i), ProviderItemID: fmt.Sprint("item-", i), ProviderTurnID: "turn", ActivityKind: domain.ActivityKindCommand, ActivityStatus: domain.ActivityStatusCompleted, Summary: fmt.Sprint("command-", i)})
	}
	events = append(events, ports.ChatEvent{Kind: ports.ChatEventTurnCompleted, ProviderEventID: "done", ProviderTurnID: "turn", TurnState: domain.TurnStateCompleted})
	conv := &nativeHistoryConversation{fakeConversation: newFakeConversation(), events: events}
	svc := chatsvc.New(chatsvc.Options{Store: st, Sessions: st, NewID: uuid.NewString, Drivers: fakeRegistry{driver: fakeDriver{conv: conv}}})
	t.Cleanup(func() { _ = svc.Stop(context.Background(), testSession) })
	started := time.Now()
	ctrl, err := svc.Start(context.Background(), chatsvc.StartConfig{SessionID: testSession, ProjectID: testProject, Harness: domain.HarnessCodex, WorkspacePath: t.TempDir(), ProviderConversationID: "thread-1"})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("1002 replay events: %s", time.Since(started))
	if st.batches < 2 || st.batches > 9 {
		t.Fatalf("expected bounded replay transactions, got %d", st.batches)
	}
	rows, err := st.LoadConversationSnapshot(context.Background(), ctrl.ConversationID())
	if err != nil || len(rows.Activities) != 1000 || len(rows.Turns) != 1 {
		t.Fatalf("activities=%d turns=%d err=%v", len(rows.Activities), len(rows.Turns), err)
	}
}
