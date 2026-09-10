package store_test

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/lifecycle"
)

func TestSessionUpdatePreservesStartupOwnership(t *testing.T) {
	for _, state := range []string{"completed", "cleanup_progressed", "stale_nil"} {
		t.Run(state, func(t *testing.T) {
			ctx := context.Background()
			s := newTestStore(t)
			seedProject(t, s, "mer")
			seed := sampleRecord("mer")
			seed.Metadata.Startup = &domain.SessionStartup{ID: "operation", Stage: "launch_commit", Committed: true}
			stale, err := s.CreateSession(ctx, seed)
			if err != nil {
				t.Fatal(err)
			}
			current := stale
			current.Metadata.Startup = &domain.SessionStartup{ID: "operation", Stage: "cleanup_pending", Committed: true, LastError: "teardown requires retry"}
			if state == "completed" {
				current.Metadata.Startup = nil
			}
			if applied, err := s.UpdateSessionStartup(ctx, current, "operation", stale.ControllerOwner()); err != nil || !applied {
				t.Fatalf("advance startup: applied=%v err=%v", applied, err)
			}
			if state == "stale_nil" {
				stale.Metadata.Startup = nil
			}
			stale.Activity.State = domain.ActivityExited
			if err := s.UpdateSession(ctx, stale); err != nil {
				t.Fatal(err)
			}
			after, found, err := s.GetSession(ctx, stale.ID)
			if err != nil || !found {
				t.Fatalf("read session: found=%v err=%v", found, err)
			}
			if !reflect.DeepEqual(after.Metadata.Startup, current.Metadata.Startup) {
				t.Fatalf("ordinary session update changed startup ownership: got %+v, want %+v", after.Metadata.Startup, current.Metadata.Startup)
			}
			if after.Activity.State != domain.ActivityExited {
				t.Fatalf("activity update was lost: %s", after.Activity.State)
			}
		})
	}
}

func TestLaunchCommitPublishesStartupOwnership(t *testing.T) {
	for _, mode := range []domain.SessionMode{domain.SessionModeTUI, domain.SessionModeChat} {
		t.Run(string(mode), func(t *testing.T) {
			ctx := context.Background()
			s := newTestStore(t)
			seedProject(t, s, "mer")
			seed := sampleRecord("mer")
			seed.Mode = mode
			seed.IsTerminated = true
			seed.Metadata.Startup = &domain.SessionStartup{ID: "operation", Stage: "launch_commit"}
			rec, err := s.CreateSession(ctx, seed)
			if err != nil {
				t.Fatal(err)
			}
			metadata := rec.Metadata
			metadata.Startup = &domain.SessionStartup{ID: "operation", Stage: "launch_commit", Committed: true}
			lc := lifecycle.New(s, nil)
			if mode == domain.SessionModeTUI {
				metadata.RuntimeHandleID = "new-runtime"
				metadata.RuntimeLaunchID = "new-launch"
				metadata.Startup.RuntimePossible = true
				err = lc.MarkSpawned(ctx, rec.ID, metadata)
			} else {
				conversation, createErr := s.CreateConversation(ctx, "conversation", domain.ConversationScopeSession, rec.ProjectID, rec.ID, time.Now().UTC())
				if createErr != nil {
					t.Fatal(createErr)
				}
				metadata.ProviderConversationID = "new-provider-thread"
				metadata.ControllerGeneration = "new-generation"
				metadata.Startup.ControllerPossible = true
				metadata.Startup.ControllerGeneration = metadata.ControllerGeneration
				boundary := domain.ConversationBranch{
					ID: "new-boundary", ConversationID: conversation.ID, SessionID: rec.ID,
					ParentBranchID: conversation.ActiveBranchID, ProviderConversationID: metadata.ProviderConversationID,
					ProviderScopeID: "new-boundary", CreatedAt: time.Now().UTC(),
				}
				err = lc.MarkChatSpawned(ctx, rec.ID, metadata, boundary)
			}
			if err != nil {
				t.Fatalf("commit launch: %v", err)
			}
			after, found, err := s.GetSession(ctx, rec.ID)
			if err != nil || !found {
				t.Fatalf("read session: found=%v err=%v", found, err)
			}
			if !reflect.DeepEqual(after.Metadata.Startup, metadata.Startup) || after.IsTerminated || after.Activity.State != domain.ActivityIdle {
				t.Fatalf("launch did not publish lifecycle and startup facts together: %+v", after)
			}
			if after.Metadata.RuntimeHandleID != metadata.RuntimeHandleID || after.Metadata.RuntimeLaunchID != metadata.RuntimeLaunchID ||
				after.Metadata.ProviderConversationID != metadata.ProviderConversationID || after.Metadata.ControllerGeneration != metadata.ControllerGeneration {
				t.Fatalf("launch ownership not committed: %+v", after.Metadata)
			}
		})
	}
}
