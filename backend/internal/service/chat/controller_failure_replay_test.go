package chat_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"

	"github.com/google/uuid"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	chatsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/chat"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/store"
)

type historyFailureFallback string

func (e historyFailureFallback) Error() string             { return string(e) }
func (e historyFailureFallback) ChatFailureFallback() bool { return true }

func TestFailureExplanationSurvivesReconnectAndDuplicateHistory(t *testing.T) {
	for _, tt := range []struct {
		name      string
		cleanup   bool
		replayErr error
	}{
		{"provider failure without replay details", false, nil},
		{"provider failure with replay fallback", false, historyFailureFallback("no provider details")},
		{"controller cleanup with replay fallback", true, historyFailureFallback("no provider details")},
	} {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			ctx := context.Background()
			if _, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{Text: "go", ClientMessageID: "client-1"}); err != nil {
				t.Fatalf("Send: %v", err)
			}
			h.conv.emit(ports.ChatEvent{Kind: ports.ChatEventTurnStarted, ProviderTurnID: "provider-turn-1"})
			want := "quota exhausted"
			if tt.cleanup {
				want = "controller ended before the turn completed"
				if err := h.conv.Close(); err != nil {
					t.Fatalf("Close: %v", err)
				}
				h.ctrl.Wait()
			} else {
				h.conv.emit(ports.ChatEvent{
					Kind: ports.ChatEventTurnCompleted, ProviderTurnID: "provider-turn-1",
					TurnState: domain.TurnStateFailed, Err: errors.New(want),
				})
			}
			h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
				return len(s.Turns) == 1 && s.Turns[0].ErrorMessage == want
			})
			if err := h.svc.Stop(ctx, testSession); err != nil {
				t.Fatalf("Stop initial controller: %v", err)
			}

			svc := chatsvc.New(chatsvc.Options{
				Store: h.st, Sessions: h.st, Reader: fullSnapshotReader(h.st),
				Log: slog.New(slog.DiscardHandler), NewID: uuid.NewString,
				Drivers: fakeRegistry{driver: fakeDriver{resume: func(ports.ChatResumeConfig) (ports.ChatConversation, error) {
					return &nativeHistoryConversation{
						fakeConversation: newFakeConversation(),
						events: []ports.ChatEvent{
							{Kind: ports.ChatEventTurnStarted, ProviderEventID: "history-start", ProviderTurnID: "provider-turn-1"},
							{Kind: ports.ChatEventUserMessageCompleted, ProviderEventID: "history-user", ProviderTurnID: "provider-turn-1", ClientMessageID: "client-1", Text: "go"},
							{Kind: ports.ChatEventTurnCompleted, ProviderEventID: "history-complete", ProviderTurnID: "provider-turn-1", TurnState: domain.TurnStateFailed, Err: tt.replayErr},
						},
					}, nil
				}}},
			})
			t.Cleanup(func() { _ = svc.Stop(ctx, testSession) })
			archiveCount := 0
			for replay := 0; replay < 2; replay++ {
				ctrl, err := svc.Start(ctx, chatsvc.StartConfig{
					SessionID: testSession, ProjectID: testProject, Harness: domain.HarnessCodex,
					WorkspacePath: t.TempDir(), ProviderConversationID: "thread-1", RequireNativeHistory: true,
				})
				if err != nil {
					t.Fatalf("Resume %d: %v", replay, err)
				}
				if ctrl.ConversationID() != h.ctrl.ConversationID() {
					t.Fatal("reconnect changed the conversation")
				}
				snapshot, err := svc.Snapshot(ctx, testSession)
				if err != nil {
					t.Fatalf("Snapshot: %v", err)
				}
				if len(snapshot.Turns) != 1 || len(snapshot.Messages) != 1 || len(snapshot.Activities) != 0 {
					t.Fatalf("replay duplicated timeline rows: %+v", snapshot)
				}
				if turn := snapshot.Turns[0]; turn.State != domain.TurnStateFailed || turn.ErrorMessage != want {
					t.Fatalf("replayed turn = %+v, want failed with %q", turn, want)
				}
				archive, err := h.st.ProviderEventsSince(ctx, ctrl.ConversationID(), 0, 100)
				if err != nil {
					t.Fatalf("ProviderEventsSince: %v", err)
				}
				if replay == 1 && len(archive) != archiveCount {
					t.Fatalf("duplicate history changed archive size: %d -> %d", archiveCount, len(archive))
				}
				archiveCount = len(archive)
				completions := 0
				for _, event := range archive {
					if event.ProviderEventID != "history-complete" {
						continue
					}
					completions++
					var record struct {
						State domain.TurnState `json:"turnState"`
						Error string           `json:"error"`
					}
					if err := json.Unmarshal([]byte(event.PayloadJson), &record); err != nil {
						t.Fatalf("decode archived completion: %v", err)
					}
					if record.State != domain.TurnStateFailed || record.Error != want {
						t.Fatalf("archive disagrees with snapshot: %+v", record)
					}
				}
				if completions != 1 {
					t.Fatalf("archived history completions = %d, want 1", completions)
				}
				if err := svc.Stop(ctx, testSession); err != nil {
					t.Fatalf("Stop replay controller: %v", err)
				}
			}
		})
	}
}
