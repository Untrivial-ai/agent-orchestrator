package chat_test

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	chatsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/chat"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/store"
)

type nativeCheckpointDriver struct {
	ports.ChatDriver
	verify func(context.Context, ports.NativeCheckpointRequest) (ports.NativeCheckpointBoundary, error)
}

func (d nativeCheckpointDriver) VerifyNativeCheckpoint(ctx context.Context, request ports.NativeCheckpointRequest) (ports.NativeCheckpointBoundary, error) {
	return d.verify(ctx, request)
}

func TestStartNativeCheckpointAdmission(t *testing.T) {
	sentinel := errors.New("native read failed")
	for _, test := range []struct {
		name                                  string
		verifyErr                             error
		wrongUUID, highWater, legacy, consent bool
		wantPass                              bool
	}{
		{name: "verified boundary", wantPass: true},
		{name: "verifier error before driver launch", verifyErr: sentinel},
		{name: "unsettled before driver launch", verifyErr: ports.ErrChatHistoryUnsettled},
		{name: "replayed wrong UUID", wrongUUID: true},
		{name: "AO high-water still enforced", highWater: true},
		{name: "legacy gate still enforced", legacy: true},
		{name: "legacy gate waived only by consent", legacy: true, consent: true, wantPass: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t)
			ctx := context.Background()
			if test.highWater {
				turnID := completeTurn(t, h, "earlier durable turn", "provider-turn-1")
				h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
					for _, turn := range s.Turns {
						if turn.ID == turnID && turn.State == domain.TurnStateCompleted {
							return true
						}
					}
					return false
				})
			}
			if err := h.svc.Stop(ctx, testSession); err != nil {
				t.Fatal(err)
			}
			rec, _, err := h.st.GetSession(ctx, testSession)
			if err != nil {
				t.Fatal(err)
			}
			rec.Metadata.NativeCheckpointEvidence = "owned evidence"
			rec.Metadata.ConversationCheckpointState = domain.ConversationCheckpointPrompt
			rec.Metadata.ConversationCheckpointGeneration = "source-launch"
			rec.Metadata.ConversationCheckpointNativeID = "thread-1"
			rec.Metadata.ConversationCheckpointUnsettled = true
			if test.legacy {
				rec.Metadata.ConversationCheckpointState = domain.ConversationCheckpointLegacy
				rec.Metadata.LatestUserPrompt = "legacy B"
			}
			if err := h.st.UpdateSession(ctx, rec); err != nil {
				t.Fatal(err)
			}
			verified, launched := false, false
			driver := nativeCheckpointDriver{
				ChatDriver: fakeDriver{resume: func(ports.ChatResumeConfig) (ports.ChatConversation, error) {
					if !verified {
						t.Error("launched before native verification")
					}
					launched = true
					nativeID := "native-B"
					if test.wrongUUID {
						nativeID = "native-A"
					}
					return &nativeHistoryConversation{fakeConversation: newFakeConversation(), events: []ports.ChatEvent{
						{Kind: ports.ChatEventTurnStarted, ProviderEventID: "start-B", ProviderTurnID: "turn-B"},
						{Kind: ports.ChatEventUserMessageCompleted, ProviderEventID: "user-B", ProviderTurnID: "turn-B", ProviderItemID: "user-B", NativeUserMessageID: nativeID, Text: "continue"},
						{Kind: ports.ChatEventMessageCompleted, ProviderEventID: "answer-B", ProviderTurnID: "turn-B", ProviderItemID: "answer-B", Text: "Done"},
						{Kind: ports.ChatEventTurnCompleted, ProviderEventID: "complete-B", ProviderTurnID: "turn-B", TurnState: domain.TurnStateRecovered},
					}}, nil
				}},
				verify: func(_ context.Context, request ports.NativeCheckpointRequest) (ports.NativeCheckpointBoundary, error) {
					if request.ProviderConversationID != "thread-1" || request.Evidence != rec.Metadata.NativeCheckpointEvidence {
						t.Errorf("wrong verification request: %+v", request)
					}
					verified = true
					return ports.NativeCheckpointBoundary{UserMessageID: "native-B", UserText: "continue", AssistantText: "Done"}, test.verifyErr
				},
			}
			svc := chatsvc.New(chatsvc.Options{Store: h.st, Sessions: h.st, Reader: fullSnapshotReader(h.st), Drivers: fakeRegistry{driver: driver}, Log: slog.New(slog.DiscardHandler), NewID: uuid.NewString})
			t.Cleanup(func() { _ = svc.Stop(context.Background(), testSession) })
			policy := domain.SessionInterfaceTransitionHistoryStrict
			if test.consent {
				policy = domain.SessionInterfaceTransitionHistoryProvider
			}
			startCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
			defer cancel()
			_, err = svc.Start(startCtx, chatsvc.StartConfig{
				SessionID: testSession, ProjectID: testProject, Harness: domain.HarnessCodex, WorkspacePath: t.TempDir(),
				ProviderConversationID: "thread-1", RequireNativeHistory: true, HistoryPolicy: policy,
			})
			if (err == nil) != test.wantPass {
				t.Fatalf("Start error=%v", err)
			}
			if test.verifyErr != nil && launched {
				t.Fatal("launched despite verification failure")
			}
			if !verified {
				t.Fatal("native evidence was not verified")
			}
		})
	}
}
