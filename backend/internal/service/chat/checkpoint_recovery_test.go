package chat_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/lifecycle"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	chatsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/chat"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
)

func TestInterfaceHandoffRecoversLegacyCheckpointFromUnsettledChatTurn(t *testing.T) {
	for _, state := range []domain.TurnState{
		domain.TurnStateCancelled, domain.TurnStateInterrupted, domain.TurnStateFailed,
	} {
		t.Run(string(state), func(t *testing.T) {
			ctx := context.Background()
			st := openStore(t)
			conversationID, promptAt := seedRecoveryCheckpointHistory(t, st, state)
			rec, found, err := st.GetSession(ctx, testSession)
			if err != nil || !found {
				t.Fatalf("read legacy checkpoint: found=%v err=%v", found, err)
			}
			// AppendUserMessage, not a fabricated provenance assignment, wrote
			// this legacy fact at exactly the durable turn's request time. This
			// models the cancelled queued prompt reported on September 11.
			if rec.Metadata.LatestUserPrompt != "Say hi to" ||
				!rec.Metadata.LatestUserPromptAt.Equal(promptAt) ||
				rec.Metadata.ConversationCheckpointState != domain.ConversationCheckpointLegacy ||
				rec.Metadata.ConversationCheckpointGeneration != "" ||
				rec.Metadata.ConversationCheckpointNativeID != "" {
				t.Fatalf("Chat message did not create the expected legacy checkpoint: %+v", rec.Metadata)
			}
			before, err := st.LoadConversationSnapshot(ctx, conversationID)
			if err != nil {
				t.Fatalf("snapshot before recovery: %v", err)
			}
			driver := &sequenceDriver{conversations: []ports.ChatConversation{
				checkpointRecoveryReplay(), checkpointRecoveryReplay(), checkpointRecoveryReplay(),
			}}
			svc := chatsvc.New(chatsvc.Options{
				Store: st, Sessions: st, Reader: fullSnapshotReader(st),
				Drivers: fakeRegistry{driver: driver}, Log: slog.New(slog.DiscardHandler),
				NewID: func() string { return fmt.Sprintf("checkpoint-recovery-%d", time.Now().UnixNano()) },
			})
			t.Cleanup(func() { _ = svc.Stop(context.Background(), testSession) })
			cfg := chatsvc.StartConfig{
				SessionID: testSession, ProjectID: testProject, Harness: domain.HarnessCodex,
				WorkspacePath: t.TempDir(), ProviderConversationID: "thread-1", RequireNativeHistory: true,
				HistoryPolicy: domain.SessionInterfaceTransitionHistoryStrict,
			}
			if _, err := svc.Start(ctx, cfg); !errors.Is(err, ports.ErrChatHistoryUnsettled) ||
				!ports.ChatHistoryMismatchOnlyUntrustedText(err) {
				t.Fatalf("strict admission must identify recoverable legacy text: %v", err)
			}
			cfg.HistoryPolicy = domain.SessionInterfaceTransitionHistoryProvider
			for attempt := 0; attempt < 2; attempt++ {
				if _, err := svc.Start(ctx, cfg); err != nil {
					t.Fatalf("provider-history recovery attempt %d: %v", attempt+1, err)
				}
				after, err := st.LoadConversationSnapshot(ctx, conversationID)
				if err != nil {
					t.Fatalf("snapshot after recovery: %v", err)
				}
				if len(after.Turns) != len(before.Turns) || len(after.Messages) != len(before.Messages) {
					t.Fatalf("replay duplicated history: turns %d -> %d, messages %d -> %d",
						len(before.Turns), len(after.Turns), len(before.Messages), len(after.Messages))
				}
				var priorAnswers int
				for _, message := range after.Messages {
					if message.Text == "Earlier answer" {
						priorAnswers++
					}
				}
				if priorAnswers != 1 {
					t.Fatalf("completed answer appeared %d times, want once", priorAnswers)
				}
				turn, err := st.TurnByID(ctx, "unsettled-turn")
				if err != nil || turn.State != state {
					t.Fatalf("recovery changed unsettled turn: state=%q err=%v", turn.State, err)
				}
				rec, found, err := st.GetSession(ctx, testSession)
				if err != nil || !found {
					t.Fatalf("read retained checkpoint: found=%v err=%v", found, err)
				}
				if rec.Metadata.LatestUserPrompt != "Say hi to" ||
					!rec.Metadata.LatestUserPromptAt.Equal(promptAt) ||
					rec.Metadata.ConversationCheckpointState != domain.ConversationCheckpointLegacy {
					t.Fatalf("recovery destructively rewrote the legacy fact: %+v", rec.Metadata)
				}
				if err := svc.Stop(ctx, testSession); err != nil {
					t.Fatalf("stop recovered controller: %v", err)
				}
			}
		})
	}
}

func TestInterfaceHandoffNewTrustedTUIPromptCannotBorrowOldFailedChatOutcome(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	_, oldPromptAt := seedRecoveryCheckpointHistory(t, st, domain.TurnStateFailed)
	lcm := lifecycle.New(st, nil)
	changed, err := lcm.CommitControllerEpoch(ctx, testSession,
		domain.SessionModeChat, domain.SessionModeTUI, "thread-1", false)
	if err != nil || !changed {
		t.Fatalf("commit Chat -> TUI: changed=%v err=%v", changed, err)
	}
	rec, found, err := st.GetSession(ctx, testSession)
	if err != nil || !found {
		t.Fatalf("read TUI checkpoint: found=%v err=%v", found, err)
	}
	if rec.Metadata.LatestUserPrompt != "" || rec.Metadata.ConversationCheckpointState != domain.ConversationCheckpointEmpty {
		t.Fatalf("Chat -> TUI did not retire the source checkpoint: %+v", rec.Metadata)
	}
	if err := lcm.MarkSpawned(ctx, testSession, domain.SessionMetadata{
		RuntimeLaunchID: "terminal-generation", RuntimeHandleID: "terminal-handle",
		AgentSessionID: "thread-1", AgentSessionIDLaunchID: "terminal-generation",
		ProviderConversationID: "thread-1",
	}); err != nil {
		t.Fatalf("start terminal owner: %v", err)
	}
	newPromptAt := time.Now().UTC()
	if err := lcm.ApplyActivitySignal(ctx, testSession, ports.ActivitySignal{
		Valid: true, State: domain.ActivityActive, Event: "user-prompt-submit",
		LaunchID: "terminal-generation", AgentSessionID: "thread-1", LatestUserPrompt: "Say hi to",
		ConversationCheckpointOrigin: domain.ConversationCheckpointOriginHuman, Timestamp: newPromptAt,
	}); err != nil {
		t.Fatalf("observe repeated TUI prompt: %v", err)
	}
	rec, found, err = st.GetSession(ctx, testSession)
	if err != nil || !found {
		t.Fatalf("read trusted checkpoint: found=%v err=%v", found, err)
	}
	if rec.Metadata.ConversationCheckpointState != domain.ConversationCheckpointPrompt ||
		rec.Metadata.ConversationCheckpointGeneration != "terminal-generation" ||
		rec.Metadata.ConversationCheckpointNativeID != "thread-1" ||
		!rec.Metadata.LatestUserPromptAt.Equal(newPromptAt) || !newPromptAt.After(oldPromptAt) {
		t.Fatalf("TUI hook did not establish a new trusted checkpoint: %+v", rec.Metadata)
	}
	changed, err = lcm.CommitControllerEpoch(ctx, testSession,
		domain.SessionModeTUI, domain.SessionModeChat, "thread-1", false)
	if err != nil || !changed {
		t.Fatalf("commit TUI -> Chat: changed=%v err=%v", changed, err)
	}
	driver := &sequenceDriver{conversations: []ports.ChatConversation{
		checkpointRecoveryReplay(), checkpointRecoveryReplay(),
	}}
	svc := chatsvc.New(chatsvc.Options{
		Store: st, Sessions: st, Reader: fullSnapshotReader(st),
		Drivers: fakeRegistry{driver: driver}, Log: slog.New(slog.DiscardHandler),
		NewID: func() string { return fmt.Sprintf("new-trusted-checkpoint-%d", time.Now().UnixNano()) },
	})
	t.Cleanup(func() { _ = svc.Stop(context.Background(), testSession) })
	for _, policy := range []domain.SessionInterfaceTransitionHistoryPolicy{
		domain.SessionInterfaceTransitionHistoryStrict, domain.SessionInterfaceTransitionHistoryProvider,
	} {
		_, err := svc.Start(ctx, chatsvc.StartConfig{
			SessionID: testSession, ProjectID: testProject, Harness: domain.HarnessCodex,
			WorkspacePath: t.TempDir(), ProviderConversationID: "thread-1", RequireNativeHistory: true,
			HistoryPolicy: policy,
		})
		if !errors.Is(err, ports.ErrChatHistoryUnsettled) || ports.ChatHistoryMismatchOnlyUntrustedText(err) ||
			!slices.Contains(ports.ChatHistoryMismatchDimensions(err), ports.ChatHistoryMismatchTrustedUserText) {
			t.Fatalf("%s waived a new TUI prompt because old Chat text matched: %v", policy, err)
		}
	}
}

func seedRecoveryCheckpointHistory(t *testing.T, st *sqlite.Store, state domain.TurnState) (string, time.Time) {
	t.Helper()
	ctx := context.Background()
	now := time.Date(2026, 9, 7, 14, 35, 15, 607692000, time.UTC)
	conversation, err := st.CreateConversation(ctx, "checkpoint-recovery-conversation",
		domain.ConversationScopeSession, testProject, testSession, now.Add(-time.Minute))
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	if err := st.ClaimChatControllerGeneration(ctx, testSession, "chat-generation", now); err != nil {
		t.Fatalf("claim Chat generation: %v", err)
	}
	created, err := st.AppendUserMessage(ctx, conversation.ID, testSession, "chat-generation",
		domain.ConversationMessage{ID: "prior-user", Text: "Earlier prompt", Origin: domain.MessageOriginHuman,
			ClientMessageID: "prior-client"}, "prior-turn", now.Add(-time.Minute))
	if err != nil || !created {
		t.Fatalf("append prior completed turn: created=%v err=%v", created, err)
	}
	if err := st.BindTurnToProvider(ctx, "prior-turn", "prior-provider-turn", now.Add(-time.Minute)); err != nil {
		t.Fatalf("bind prior turn: %v", err)
	}
	if err := st.SettleAssistantMessage(ctx, conversation.ID, "prior-answer-item", "prior-provider-turn",
		"Earlier answer", "prior-answer", now.Add(-time.Minute)); err != nil {
		t.Fatalf("record prior answer: %v", err)
	}
	if err := st.SettleTurn(ctx, conversation.ID, "prior-provider-turn", domain.TurnStateCompleted, "", now.Add(-time.Minute)); err != nil {
		t.Fatalf("complete prior turn: %v", err)
	}
	created, err = st.AppendUserMessage(ctx, conversation.ID, testSession, "chat-generation",
		domain.ConversationMessage{ID: "unsettled-user", Text: "Say hi to", Origin: domain.MessageOriginHuman,
			ClientMessageID: "unsettled-client"}, "unsettled-turn", now)
	if err != nil || !created {
		t.Fatalf("append unsettled turn: created=%v err=%v", created, err)
	}
	if state == domain.TurnStateCancelled {
		if err := st.CancelQueuedTurnByID(ctx, conversation.ID, "unsettled-turn", now.Add(time.Second)); err != nil {
			t.Fatalf("cancel undispatched prompt: %v", err)
		}
	} else {
		if err := st.BindTurnToProvider(ctx, "unsettled-turn", "unsettled-provider-turn", now); err != nil {
			t.Fatalf("bind unsettled turn: %v", err)
		}
		if err := st.SettleTurn(ctx, conversation.ID, "unsettled-provider-turn", state, "", now.Add(time.Second)); err != nil {
			t.Fatalf("settle unfinished turn: %v", err)
		}
	}
	return conversation.ID, now
}

func checkpointRecoveryReplay() *nativeHistoryConversation {
	return &nativeHistoryConversation{
		fakeConversation: newFakeConversation(),
		events: []ports.ChatEvent{
			{Kind: ports.ChatEventTurnStarted, ProviderEventID: "prior-start", ProviderTurnID: "prior-provider-turn"},
			{Kind: ports.ChatEventUserMessageCompleted, ProviderEventID: "prior-replay-user", ProviderTurnID: "prior-provider-turn",
				ProviderItemID: "prior-user-item", ClientMessageID: "prior-client", Text: "Earlier prompt"},
			{Kind: ports.ChatEventMessageCompleted, ProviderEventID: "prior-replay-answer", ProviderTurnID: "prior-provider-turn",
				ProviderItemID: "prior-answer-item", Text: "Earlier answer"},
			{Kind: ports.ChatEventTurnCompleted, ProviderEventID: "prior-completed", ProviderTurnID: "prior-provider-turn",
				TurnState: domain.TurnStateCompleted},
		},
	}
}
