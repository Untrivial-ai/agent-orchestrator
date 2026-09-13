package chat_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/lifecycle"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	chatsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/chat"
)

func TestNativeReplayDoesNotSupersedeNewHooksWithRepeatedText(t *testing.T) {
	for _, scenario := range []string{"old_hooks", "old_failed_hooks", "repeated_prompt", "repeated_answer", "repeated_failed_prompt", "repeated_latest", "repeated_latest_with_failed_turn", "repeated_latest_with_boundary", "repeated_latest_reassigned", "repeated_latest_complete", "repeated_latest_reassigned_complete", "repeated_latest_recovered", "repeated_latest_recovered_complete", "repeated_latest_only_recovered", "repeated_latest_only_recovered_complete", "repeated_latest_subagent_stop_complete"} {
		t.Run(scenario, func(t *testing.T) {
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
			if scenario == "repeated_latest_with_boundary" {
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
				if strings.Contains(scenario, "recovered") && (i == len(prompts)-1 || strings.Contains(scenario, "only_recovered")) {
					state = domain.TurnStateRecovered
				}
				if (scenario == "repeated_failed_prompt" || scenario == "old_failed_hooks" || scenario == "repeated_latest_with_failed_turn") && i == 0 {
					state = domain.TurnStateFailed
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
			oldHooks := scenario == "old_hooks" || scenario == "old_failed_hooks"
			if !oldHooks {
				signal.Timestamp = now.Add(3 * time.Minute)
				if strings.HasPrefix(scenario, "repeated_latest") {
					signal.LatestUserPrompt, signal.LatestAssistantUpdate = "different task", "new answer"
				} else if scenario == "repeated_prompt" || scenario == "repeated_failed_prompt" {
					signal.LatestAssistantUpdate = ""
				} else {
					signal.LatestUserPrompt = ""
					signal.Event = "stop"
				}
				if err := lcm.ApplyActivitySignal(ctx, testSession, signal); err != nil {
					t.Fatal(err)
				}
			}
			if strings.Contains(scenario, "subagent_stop") {
				if err := lcm.ApplyActivitySignal(ctx, testSession, ports.ActivitySignal{
					Event: "subagent-stop", ControllerGeneration: "old-generation",
					Timestamp: signal.Timestamp.Add(time.Second), LatestAssistantUpdate: "continue",
				}); err != nil {
					t.Fatal(err)
				}
			}
			if strings.HasSuffix(scenario, "complete") {
				events = append(events,
					ports.ChatEvent{Kind: ports.ChatEventTurnStarted, ProviderEventID: "terminal-started", ProviderTurnID: "terminal"},
					ports.ChatEvent{Kind: ports.ChatEventUserMessageCompleted, ProviderEventID: "terminal-user-event", ProviderTurnID: "terminal", ProviderItemID: "terminal-user", Text: "different task"},
					ports.ChatEvent{Kind: ports.ChatEventMessageCompleted, ProviderEventID: "terminal-answer-event", ProviderTurnID: "terminal", ProviderItemID: "terminal-answer", Text: "new answer"},
					ports.ChatEvent{Kind: ports.ChatEventTurnCompleted, ProviderEventID: "terminal-completed", ProviderTurnID: "terminal", TurnState: domain.TurnStateCompleted})
			}
			if strings.Contains(scenario, "reassigned") {
				for i := range events {
					events[i].ProviderTurnID = "reloaded-" + events[i].ProviderTurnID
					events[i].ProviderItemID = "reloaded-" + events[i].ProviderItemID
				}
			}
			provider := &nativeHistoryConversation{fakeConversation: newFakeConversation(), events: events}
			svc := chatsvc.New(chatsvc.Options{Store: st, Sessions: st, Reader: snapshotReader(st), Drivers: fakeRegistry{driver: fakeDriver{conv: provider}}, NewID: uuid.NewString})
			t.Cleanup(func() { svc.StopAll(ctx) })
			_, err = svc.Start(ctx, chatsvc.StartConfig{SessionID: testSession, ProjectID: testProject, Harness: domain.HarnessCodex, ProviderConversationID: "thread-1", RequireNativeHistory: true})
			if oldHooks || strings.HasSuffix(scenario, "complete") {
				if err != nil {
					t.Fatalf("complete replay rejected: %v", err)
				}
			} else if !errors.Is(err, ports.ErrChatHistoryUnsettled) {
				t.Fatalf("stale replay admitted after %s: want ErrChatHistoryUnsettled, got %v", scenario, err)
			}
		})
	}
}
