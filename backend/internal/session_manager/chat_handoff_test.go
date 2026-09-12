package sessionmanager

import (
	"context"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func TestChatProviderHandoffRequiresExactCoordinatorProof(t *testing.T) {
	for _, scenario := range []string{"verified", "ordinary_resume", "wrong_native_id", "wrong_session", "wrong_direction", "source_still_running", "first_worker_chat", "unowned_history"} {
		t.Run(scenario, func(t *testing.T) {
			rec := domain.SessionRecord{
				ID: "p-1", ProjectID: "p", Kind: domain.KindWorker, Harness: domain.HarnessQwen,
				Mode: domain.SessionModeChat, Metadata: domain.SessionMetadata{ProviderConversationID: "opaque-B"},
			}
			st := &historicalChatRestoreStore{
				transitionStore: newTransitionStore(),
				conversation:    domain.ConversationRecord{ID: "conversation", SessionID: rec.ID, ActiveBranchID: "root", LatestSequence: 2},
				activeBranch:    domain.ConversationBranch{ID: "root", ConversationID: "conversation", SessionID: rec.ID, ProviderConversationID: "opaque-A"},
			}
			transition := domain.SessionInterfaceTransition{
				ID: "handoff", SessionID: rec.ID, SourceMode: domain.SessionModeTUI, TargetMode: domain.SessionModeChat,
				NativeConversationID: "opaque-B", Phase: domain.SessionInterfaceTransitionTargetStarting, CreatedAt: time.Now(),
			}
			live := true
			switch scenario {
			case "ordinary_resume":
				live = false
			case "wrong_native_id":
				transition.NativeConversationID = "opaque-C"
			case "wrong_session":
				transition.SessionID = "p-2"
			case "wrong_direction":
				transition.SourceMode, transition.TargetMode = domain.SessionModeChat, domain.SessionModeTUI
			case "source_still_running":
				transition.Phase = domain.SessionInterfaceTransitionRequested
			case "first_worker_chat":
				st.conversationErr = domain.ErrNoConversation
			case "unowned_history":
				st.activeBranch.SessionID = ""
			}
			st.transitions[transition.ID] = transition
			m := New(Deps{Store: st})
			plan, err := m.prepareChatProviderHandoff(context.Background(), rec, live)
			if scenario == "unowned_history" {
				if err == nil {
					t.Fatal("unknown history ownership must refuse adoption")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if (plan != nil) != (scenario == "verified") {
				t.Fatalf("unexpected handoff reservation: %+v", plan)
			}
		})
	}
}
