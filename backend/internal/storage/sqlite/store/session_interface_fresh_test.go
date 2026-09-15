package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func TestFreshInterfaceEpochReleasesOnlyUntouchedRootProvider(t *testing.T) {
	for _, name := range []string{"fresh", "provider turn", "stale mode"} {
		t.Run(name, func(t *testing.T) {
			st := newTestStore(t)
			ctx := context.Background()
			seedProject(t, st, "fresh-switch")
			now := time.Now()
			rec := sampleRecord("fresh-switch")
			rec.Mode = domain.SessionModeChat
			rec.Metadata.ProviderConversationID = "reserved-chat-id"
			rec.Metadata.ControllerGeneration = "chat-generation"
			sess, err := st.CreateSession(ctx, rec)
			if err != nil {
				t.Fatal(err)
			}
			conversation, err := st.CreateConversation(ctx, "fresh-conversation", domain.ConversationScopeSession,
				sess.ProjectID, sess.ID, now)
			if err != nil {
				t.Fatal(err)
			}
			before, err := st.ConversationBranch(ctx, conversation.ID, conversation.ActiveBranchID)
			if err != nil {
				t.Fatal(err)
			}
			if name == "provider turn" {
				if err := st.AdoptProviderTurn(ctx, conversation.ID, sess.ID, "chat-generation", "turn-1", "provider-turn-1", now); err != nil {
					t.Fatal(err)
				}
			}
			source := domain.SessionModeChat
			if name == "stale mode" {
				source = domain.SessionModeTUI
			}
			changed, err := st.CommitSessionControllerEpoch(ctx, sess.ID, source, domain.SessionModeTUI, "", now)
			if name == "fresh" {
				if err != nil || !changed {
					t.Fatalf("fresh epoch changed=%v err=%v", changed, err)
				}
			} else if changed || (name == "provider turn" && err == nil) {
				t.Fatalf("unsafe epoch changed=%v err=%v", changed, err)
			}
			after, err := st.ConversationBranch(ctx, conversation.ID, conversation.ActiveBranchID)
			if err != nil {
				t.Fatal(err)
			}
			current, _, err := st.GetSession(ctx, sess.ID)
			if err != nil {
				t.Fatal(err)
			}
			if name == "fresh" {
				if after.ProviderConversationID != "" || after.ProviderScopeID == before.ProviderScopeID || after.ProviderScopeID == "" {
					t.Fatalf("unused provider binding was retained: before=%+v after=%+v", before, after)
				}
				if after.ID != before.ID || current.Mode != domain.SessionModeTUI {
					t.Fatalf("fresh root was replaced or mode not committed: branch=%+v mode=%s", after, current.Mode)
				}
			} else if after.ProviderConversationID != before.ProviderConversationID ||
				after.ProviderScopeID != before.ProviderScopeID || current.Mode != domain.SessionModeChat ||
				current.Metadata.ProviderConversationID != "reserved-chat-id" {
				t.Fatalf("failed epoch changed ownership: branch=%+v session=%+v", after, current)
			}
		})
	}
}
