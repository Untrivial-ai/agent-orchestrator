package chat_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/lifecycle"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	chatsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/chat"
)

func TestNativeReplayDoesNotSupersedeNewHooksWithRepeatedText(t *testing.T) {
	repeatedPrompt := ports.ActivitySignal{Event: "user-prompt-submit", LatestUserPrompt: "continue"}
	repeatedAnswer := ports.ActivitySignal{Event: "stop", LatestAssistantUpdate: "yes"}
	repeatedLatest := ports.ActivitySignal{Event: "user-prompt-submit", LatestUserPrompt: "different task", LatestAssistantUpdate: "new answer"}
	for _, tc := range []struct {
		name              string
		firstTurnState    domain.TurnState
		lastTurnState     domain.TurnState
		newHook           ports.ActivitySignal
		withBoundary      bool
		withTerminalTurn  bool
		reassignReplayIDs bool
		withSubagentStop  bool
		wantErr           error
	}{
		{name: "old_hooks"},
		{name: "old_failed_hooks", firstTurnState: domain.TurnStateFailed},
		{name: "repeated_prompt", newHook: repeatedPrompt, wantErr: ports.ErrChatHistoryUnsettled},
		{name: "repeated_answer", newHook: repeatedAnswer, wantErr: ports.ErrChatHistoryUnsettled},
		{name: "repeated_failed_prompt", firstTurnState: domain.TurnStateFailed, newHook: repeatedPrompt, wantErr: ports.ErrChatHistoryUnsettled},
		{name: "repeated_latest", newHook: repeatedLatest, wantErr: ports.ErrChatHistoryUnsettled},
		{name: "repeated_latest_with_failed_turn", firstTurnState: domain.TurnStateFailed, newHook: repeatedLatest, wantErr: ports.ErrChatHistoryUnsettled},
		{name: "repeated_latest_with_boundary", withBoundary: true, newHook: repeatedLatest, wantErr: ports.ErrChatHistoryUnsettled},
		{name: "repeated_latest_reassigned", reassignReplayIDs: true, newHook: repeatedLatest, wantErr: ports.ErrChatHistoryUnsettled},
		{name: "repeated_latest_complete", withTerminalTurn: true, newHook: repeatedLatest},
		{name: "repeated_latest_reassigned_complete", reassignReplayIDs: true, withTerminalTurn: true, newHook: repeatedLatest},
		{name: "repeated_latest_recovered", lastTurnState: domain.TurnStateRecovered, newHook: repeatedLatest, wantErr: ports.ErrChatHistoryUnsettled},
		{name: "repeated_latest_recovered_complete", lastTurnState: domain.TurnStateRecovered, withTerminalTurn: true, newHook: repeatedLatest},
		{name: "repeated_latest_only_recovered", firstTurnState: domain.TurnStateRecovered, lastTurnState: domain.TurnStateRecovered, newHook: repeatedLatest, wantErr: ports.ErrChatHistoryUnsettled},
		{name: "repeated_latest_only_recovered_complete", firstTurnState: domain.TurnStateRecovered, lastTurnState: domain.TurnStateRecovered, withTerminalTurn: true, newHook: repeatedLatest},
		{name: "repeated_latest_subagent_stop_complete", withSubagentStop: true, withTerminalTurn: true, newHook: repeatedLatest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			st := openStore(t)
			now := time.Date(2026, 9, 13, 0, 0, 0, 0, time.UTC)
			conversation, err := st.CreateConversation(ctx, "repeated-hooks", domain.ConversationScopeSession, testProject, testSession, now)
			if err != nil {
				t.Fatal(err)
			}
			if err := st.ClaimChatControllerGeneration(ctx, testSession, "old-generation", now); err != nil {
				t.Fatal(err)
			}
			var events []ports.ChatEvent
			prompts := []string{"continue", "different task"}
			if tc.withBoundary {
				prompts = []string{"continue", "<ao-handoff-request>", "different task"}
			}
			for i, prompt := range prompts {
				at := now.Add(time.Duration(i) * time.Minute)
				id := fmt.Sprintf("turn-%d", i)
				answer := "yes"
				if i == len(prompts)-1 {
					answer = "new answer"
				}
				created, err := st.AppendUserMessage(ctx, conversation.ID, testSession, "old-generation",
					domain.ConversationMessage{ID: id + "-user", Text: prompt, Origin: domain.MessageOriginHuman, ClientMessageID: id}, id, at)
				if err != nil || !created {
					t.Fatalf("append: created=%v err=%v", created, err)
				}
				if err := st.BindTurnToProvider(ctx, id, id, at); err != nil {
					t.Fatal(err)
				}
				if err := st.SettleAssistantMessage(ctx, conversation.ID, id+"-answer", id, answer, id+"-message", at); err != nil {
					t.Fatal(err)
				}
				state := domain.TurnStateCompleted
				if i == 0 && tc.firstTurnState != "" {
					state = tc.firstTurnState
				}
				if i == len(prompts)-1 && tc.lastTurnState != "" {
					state = tc.lastTurnState
				}
				if err := st.SettleTurn(ctx, conversation.ID, id, state, "", at.Add(10*time.Second)); err != nil {
					t.Fatal(err)
				}
				events = append(events,
					ports.ChatEvent{Kind: ports.ChatEventUserMessageCompleted, ProviderEventID: id + "-user-event", ProviderTurnID: id, ProviderItemID: id + "-user", Text: prompt},
					ports.ChatEvent{Kind: ports.ChatEventMessageCompleted, ProviderEventID: id + "-answer-event", ProviderTurnID: id, ProviderItemID: id + "-answer", Text: answer},
					ports.ChatEvent{Kind: ports.ChatEventTurnCompleted, ProviderEventID: id + "-completed", ProviderTurnID: id, TurnState: state})
			}
			lcm := lifecycle.New(st, nil)
			signal := ports.ActivitySignal{Event: "user-prompt-submit", ControllerGeneration: "old-generation", Timestamp: now.Add(time.Second), LatestUserPrompt: "continue", LatestAssistantUpdate: "yes"}
			if err := lcm.ApplyActivitySignal(ctx, testSession, signal); err != nil {
				t.Fatal(err)
			}
			if tc.newHook.Event != "" {
				signal = tc.newHook
				signal.ControllerGeneration = "old-generation"
				signal.Timestamp = now.Add(3 * time.Minute)
				if err := lcm.ApplyActivitySignal(ctx, testSession, signal); err != nil {
					t.Fatal(err)
				}
			}
			if tc.withSubagentStop {
				if err := lcm.ApplyActivitySignal(ctx, testSession, ports.ActivitySignal{
					Event: "subagent-stop", ControllerGeneration: "old-generation",
					Timestamp: signal.Timestamp.Add(time.Second), LatestAssistantUpdate: "continue",
				}); err != nil {
					t.Fatal(err)
				}
			}
			if tc.withTerminalTurn {
				events = append(events,
					ports.ChatEvent{Kind: ports.ChatEventTurnStarted, ProviderEventID: "terminal-started", ProviderTurnID: "terminal"},
					ports.ChatEvent{Kind: ports.ChatEventUserMessageCompleted, ProviderEventID: "terminal-user-event", ProviderTurnID: "terminal", ProviderItemID: "terminal-user", Text: "different task"},
					ports.ChatEvent{Kind: ports.ChatEventMessageCompleted, ProviderEventID: "terminal-answer-event", ProviderTurnID: "terminal", ProviderItemID: "terminal-answer", Text: "new answer"},
					ports.ChatEvent{Kind: ports.ChatEventTurnCompleted, ProviderEventID: "terminal-completed", ProviderTurnID: "terminal", TurnState: domain.TurnStateCompleted})
			}
			if tc.reassignReplayIDs {
				for i := range events {
					events[i].ProviderTurnID = "reloaded-" + events[i].ProviderTurnID
					events[i].ProviderItemID = "reloaded-" + events[i].ProviderItemID
				}
			}
			provider := &nativeHistoryConversation{fakeConversation: newFakeConversation(), events: events}
			svc := chatsvc.New(chatsvc.Options{Store: st, Sessions: st, Reader: snapshotReader(st), Drivers: fakeRegistry{driver: fakeDriver{conv: provider}}, NewID: uuid.NewString})
			t.Cleanup(func() { svc.StopAll(ctx) })
			_, err = svc.Start(ctx, chatsvc.StartConfig{SessionID: testSession, ProjectID: testProject, Harness: domain.HarnessCodex, ProviderConversationID: "thread-1", RequireNativeHistory: true})
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("Start error = %v, want %v", err, tc.wantErr)
			}
		})
	}
}
