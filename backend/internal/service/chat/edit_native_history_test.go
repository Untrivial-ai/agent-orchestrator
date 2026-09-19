package chat_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/lifecycle"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	chatsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/chat"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/store"
)

func TestEditedFirstMessageResumesNativeHistoryAfterTerminalHooks(t *testing.T) {
	for _, tc := range []struct {
		name              string
		legacyCheckpoint  bool
		terminalHook      bool
		terminalReplay    bool
		unknownTime       bool
		trustedCheckpoint bool
		wantErr           error
	}{
		{name: "edit_clears_previous_checkpoint"},
		{name: "already_stuck_edit", legacyCheckpoint: true},
		{name: "new_terminal_work_missing", legacyCheckpoint: true, terminalHook: true, wantErr: ports.ErrChatHistoryUnsettled},
		{name: "new_terminal_work_replayed", legacyCheckpoint: true, terminalHook: true, terminalReplay: true},
		{name: "unknown_checkpoint_age", legacyCheckpoint: true, unknownTime: true, wantErr: ports.ErrChatHistoryUnsettled},
		{name: "trusted_checkpoint_keeps_text_requirements", legacyCheckpoint: true, trustedCheckpoint: true, wantErr: ports.ErrChatHistoryUnsettled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, _, driver := newEditHarness(t, false)
			ctx := context.Background()
			first := completeTurn(t, h, "Remember FRESH-7420", "provider-turn-1")
			h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool { return len(s.Messages) == 2 })
			completeTurn(t, h, "continue", "provider-turn-2")
			h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
				return len(s.Turns) == 2 && s.Turns[1].State == domain.TurnStateCompleted
			})
			if err := lifecycle.New(h.st, nil).ApplyActivitySignal(ctx, testSession, ports.ActivitySignal{
				Event: "stop", ControllerGeneration: h.ctrl.Generation(), AgentSessionID: "thread-1",
				LatestUserPrompt: "continue", LatestAssistantUpdate: "reply to continue", Timestamp: h.clock,
			}); err != nil {
				t.Fatal(err)
			}
			edited, err := h.svc.EditMessage(ctx, testSession, first, ports.ChatUserMessage{
				Text: "Remember EDIT-9317", ClientMessageID: "edit-marker", Origin: domain.MessageOriginHuman,
			})
			if err != nil {
				t.Fatalf("EditMessage: %v", err)
			}
			driver.fresh.emit(
				ports.ChatEvent{Kind: ports.ChatEventTurnStarted, ProviderTurnID: "provider-turn-101"},
				ports.ChatEvent{Kind: ports.ChatEventMessageCompleted, ProviderTurnID: "provider-turn-101", ProviderItemID: "edited-answer", Text: "EDITED EDIT-9317"},
				ports.ChatEvent{Kind: ports.ChatEventTurnCompleted, ProviderTurnID: "provider-turn-101", TurnState: domain.TurnStateCompleted},
			)
			h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
				return len(s.Turns) == 1 && s.Turns[0].ID == edited.Turn.ID && s.Turns[0].State == domain.TurnStateCompleted
			})
			if err := h.svc.Stop(ctx, testSession); err != nil {
				t.Fatal(err)
			}
			if tc.legacyCheckpoint {
				// Persist the metadata left by older builds: both identities have moved
				// to the edited branch, but the earlier Terminal checkpoint survived.
				rec, found, err := h.st.GetSession(ctx, testSession)
				if err != nil || !found {
					t.Fatalf("GetSession: found=%v err=%v", found, err)
				}
				rec.Metadata.AgentSessionID = "thread-fresh"
				rec.Metadata.LatestUserPrompt = "continue"
				rec.Metadata.LatestAssistantUpdate = "reply to continue"
				rec.Metadata.LatestUserPromptAt = h.clock.Add(-time.Minute)
				rec.Metadata.LatestAssistantUpdateAt = h.clock.Add(-time.Minute)
				if tc.unknownTime {
					rec.Metadata.LatestUserPromptAt = time.Time{}
					rec.Metadata.LatestAssistantUpdateAt = time.Time{}
				}
				if tc.trustedCheckpoint {
					rec.Harness = domain.HarnessCodex
					rec.Metadata.ConversationCheckpointState = domain.ConversationCheckpointComplete
					rec.Metadata.ConversationCheckpointGeneration = "native-launch"
					rec.Metadata.ConversationCheckpointNativeID = "thread-fresh"
					rec.Metadata.ConversationCheckpointTurnID = "provider-turn-101"
				}
				if err := h.st.UpdateSession(ctx, rec); err != nil {
					t.Fatal(err)
				}
				if tc.terminalHook {
					// A legacy build supplied these later Terminal facts without native
					// turn provenance. Their age must still gate the replay.
					rec.Metadata.LatestUserPromptAt = h.clock.Add(time.Minute)
					rec.Metadata.LatestAssistantUpdateAt = h.clock.Add(time.Minute)
					if err := h.st.UpdateSession(ctx, rec); err != nil {
						t.Fatal(err)
					}
				}
			}
			provider := &nativeHistoryConversation{fakeConversation: newFakeConversation(), events: []ports.ChatEvent{
				{Kind: ports.ChatEventUserMessageCompleted, ProviderEventID: "edited-user", ProviderTurnID: "provider-turn-101", ProviderItemID: "edited-user", Text: "Remember EDIT-9317"},
				{Kind: ports.ChatEventMessageCompleted, ProviderEventID: "edited-answer", ProviderTurnID: "provider-turn-101", ProviderItemID: "edited-answer", Text: "EDITED EDIT-9317"},
				{Kind: ports.ChatEventTurnCompleted, ProviderEventID: "edited-completed", ProviderTurnID: "provider-turn-101", TurnState: domain.TurnStateCompleted},
			}}
			provider.providerConversationID = "thread-fresh"
			if tc.terminalReplay {
				provider.events = append(provider.events,
					ports.ChatEvent{Kind: ports.ChatEventTurnStarted, ProviderEventID: "terminal-started", ProviderTurnID: "terminal-turn"},
					ports.ChatEvent{Kind: ports.ChatEventUserMessageCompleted, ProviderEventID: "terminal-user", ProviderTurnID: "terminal-turn", ProviderItemID: "terminal-user", Text: "continue"},
					ports.ChatEvent{Kind: ports.ChatEventMessageCompleted, ProviderEventID: "terminal-answer", ProviderTurnID: "terminal-turn", ProviderItemID: "terminal-answer", Text: "reply to continue"},
					ports.ChatEvent{Kind: ports.ChatEventTurnCompleted, ProviderEventID: "terminal-completed", ProviderTurnID: "terminal-turn", TurnState: domain.TurnStateCompleted},
				)
			}
			resumed := chatsvc.New(chatsvc.Options{
				Store: h.st, Sessions: h.st, Reader: fullSnapshotReader(h.st),
				Drivers: fakeRegistry{driver: fakeDriver{conv: provider}}, NewID: uuid.NewString,
			})
			t.Cleanup(func() { resumed.StopAll(context.Background()) })
			_, err = resumed.Start(ctx, chatsvc.StartConfig{
				SessionID: testSession, ProjectID: testProject, Harness: domain.HarnessClaudeCode,
				ProviderConversationID: "thread-fresh", HistoryMode: ports.ChatHistoryRequired,
			})
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("resume edited native conversation: got %v, want %v", err, tc.wantErr)
			}
			if tc.wantErr != nil {
				return
			}
			snapshot, err := resumed.Snapshot(ctx, testSession)
			if err != nil {
				t.Fatal(err)
			}
			want := []string{"Remember EDIT-9317", "EDITED EDIT-9317"}
			if tc.terminalReplay {
				want = append(want, "continue", "reply to continue")
			}
			requireMessageTexts(t, snapshot.Messages, want)
		})
	}
}
