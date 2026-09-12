package chat_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/lifecycle"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	chatsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/chat"
)

// Provider I/O is outside the transaction; publication must recheck the source
// narrative and target controller afterwards. These cases exercise the real
// Service -> Lifecycle -> SQLite transaction, not just a mocked commit callback.
func TestNativeChatHandoffAtomicPublication(t *testing.T) {
	for _, scenario := range []string{"success", "provider_failure", "wrong_provider", "history_failure", "projection_failure", "controller_changed", "history_changed", "owner_changed", "predecessor_revived", "competing_orchestrator"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			f := seedHistoricalProviderFixture(t)
			f.source.IsTerminated = true
			if err := f.store.UpdateSession(ctx, f.source); err != nil {
				t.Fatal(err)
			}
			f.target.IsTerminated = false
			f.target.Harness = domain.HarnessQwen
			f.target.CreatedAt = f.source.CreatedAt.Add(time.Hour)
			var err error
			f.target, err = f.store.CreateSession(ctx, f.target)
			if err != nil {
				t.Fatal(err)
			}
			// Unlike the historical fixture, the new owner has not rebound the
			// narrative. That is the transaction's responsibility after replay.
			conversation, err := f.store.CreateConversation(ctx, "unused", domain.ConversationScopeProject, testProject, f.source.ID, f.now)
			if err != nil {
				t.Fatal(err)
			}
			handoff := &domain.ChatProviderHandoff{
				BoundaryID: "terminal-handoff:provider", ConversationID: conversation.ID,
				PreviousSessionID: f.source.ID, PreviousBranchID: conversation.ActiveBranchID,
				PreviousSequence: conversation.LatestSequence, ExpectedControllerOwner: f.target.ControllerOwner(),
			}
			provider := &nativeHistoryConversation{fakeConversation: newFakeConversation(), events: historicalNativeHistory()}
			provider.providerConversationID = historicalTargetThread
			if scenario == "wrong_provider" {
				provider.providerConversationID = "unrelated-thread"
			}
			if scenario == "history_failure" {
				provider.err = errors.New("transcript unavailable")
			}
			if scenario == "projection_failure" {
				// Invalid activity fails after the transaction stages the new head
				// and the visible boundary, exercising rollback of partial replay.
				provider.events = append(provider.events, ports.ChatEvent{
					Kind: ports.ChatEventActivityCompleted, ProviderEventID: "bad-event", ProviderItemID: "bad-item",
					ActivityKind: domain.ActivityKind("invalid-kind"), ActivityStatus: domain.ActivityStatusCompleted,
				})
			}
			driver := fakeDriver{resume: func(cfg ports.ChatResumeConfig) (ports.ChatConversation, error) {
				if cfg.ProviderScopeID != handoff.BoundaryID {
					t.Fatalf("unreserved provider namespace: %s", cfg.ProviderScopeID)
				}
				current, err := f.store.ProjectConversation(ctx, testProject)
				if err != nil || current.SessionID != f.source.ID || current.ActiveBranchID != f.root.ID {
					t.Fatalf("ownership changed before provider I/O: %+v err=%v", current, err)
				}
				switch scenario {
				case "provider_failure":
					return nil, errors.New("provider unavailable")
				case "controller_changed":
					rec := f.target
					rec.Metadata.ControllerGeneration = "competing-generation"
					if err := f.store.UpdateSession(ctx, rec); err != nil {
						t.Fatal(err)
					}
				case "history_changed":
					err := f.store.UpsertActivity(ctx, conversation.ID, "", domain.ConversationActivity{
						ID: "concurrent-history", ProviderItemID: "concurrent-history", Kind: domain.ActivityKindSystem, Status: domain.ActivityStatusCompleted,
					}, time.Now())
					if err != nil {
						t.Fatal(err)
					}
				case "owner_changed":
					_, err := f.store.CreateConversation(ctx, "unused", domain.ConversationScopeProject, testProject, f.target.ID, time.Now())
					if err != nil {
						t.Fatal(err)
					}
				case "predecessor_revived":
					rec := f.source
					rec.IsTerminated = false
					if err := f.store.UpdateSession(ctx, rec); err != nil {
						t.Fatal(err)
					}
				case "competing_orchestrator":
					rec := f.target
					rec.CreatedAt = time.Now()
					if _, err := f.store.CreateSession(ctx, rec); err != nil {
						t.Fatal(err)
					}
				}
				return provider, nil
			}}
			lcm := lifecycle.New(f.store, nil)
			svc := chatsvc.New(chatsvc.Options{Store: f.store, Sessions: f.store, Reader: snapshotReader(f.store), Drivers: fakeRegistry{driver: driver}, NewID: uuid.NewString})
			t.Cleanup(func() { svc.StopAll(ctx) })
			_, err = svc.Start(ctx, chatsvc.StartConfig{
				SessionID: f.target.ID, ProjectID: testProject, Kind: domain.KindOrchestrator, Harness: domain.HarnessQwen,
				ProviderConversationID: historicalTargetThread, ProviderHandoff: handoff,
				ExpectedControllerOwner: f.target.ControllerOwner(),
				ControllerReady: func(started chatsvc.StartResult) (chatsvc.ControllerCommit, error) {
					metadata := f.target.Metadata
					metadata.ControllerGeneration = started.ControllerGeneration
					err := lcm.MarkChatSpawnedPrepared(ctx, f.target.ID, metadata, *started.ProviderBoundary, handoff, started.CommitProviderHistory)
					committed := started.Conversation
					committed.ActiveBranchID = handoff.BoundaryID
					committed.SessionID = f.target.ID
					return chatsvc.ControllerCommit{Conversation: committed}, err
				},
			})
			if (err == nil) != (scenario == "success") {
				t.Fatalf("unexpected result: %v", err)
			}
			if err != nil {
				t.Logf("rejected: %v", err)
			}
			rows, readErr := f.store.LoadConversationSnapshot(ctx, conversation.ID)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if scenario == "success" {
				if rows.Conversation.SessionID != f.target.ID || rows.Conversation.ActiveBranchID != handoff.BoundaryID || len(rows.Messages) != 3 {
					t.Fatalf("incomplete publication: %+v", rows)
				}
			} else {
				if rows.Conversation.ActiveBranchID != f.root.ID || len(rows.Messages) != 1 {
					t.Fatalf("failed handoff partially published history: head=%s messages=%d", rows.Conversation.ActiveBranchID, len(rows.Messages))
				}
				if _, err := f.store.ConversationBranch(ctx, conversation.ID, handoff.BoundaryID); !errors.Is(err, domain.ErrNoConversationBranch) {
					t.Fatalf("failed handoff left a provider branch: %v", err)
				}
				for _, activity := range rows.Activities {
					if strings.Contains(string(activity.Detail), "context.boundary") {
						t.Fatal("failed handoff leaked context boundary")
					}
				}
				if scenario != "owner_changed" && rows.Conversation.SessionID != f.source.ID {
					t.Fatal("failed handoff stole project ownership")
				}
			}
		})
	}
}

func TestOrdinaryNativeResumeCannotRebindAnotherProjectOwner(t *testing.T) {
	f := seedHistoricalProviderFixture(t)
	ctx := context.Background()
	called := false
	svc := chatsvc.New(chatsvc.Options{
		Store: f.store, Sessions: f.store, Reader: snapshotReader(f.store), NewID: uuid.NewString,
		Drivers: fakeRegistry{driver: fakeDriver{resume: func(ports.ChatResumeConfig) (ports.ChatConversation, error) {
			called = true
			return nil, errors.New("must not contact stale owner's provider")
		}}},
	})
	t.Cleanup(func() { svc.StopAll(ctx) })
	_, err := svc.Start(ctx, chatsvc.StartConfig{
		SessionID: f.source.ID, ProjectID: testProject, Kind: domain.KindOrchestrator,
		Harness: f.source.Harness, ProviderConversationID: f.source.Metadata.ProviderConversationID,
	})
	if err == nil || called {
		t.Fatalf("unproven owner contacted provider: called=%v err=%v", called, err)
	}
	current, err := f.store.ProjectConversation(ctx, testProject)
	if err != nil || current.SessionID != f.target.ID || current.ActiveBranchID != f.root.ID || current.LatestSequence != f.conversation.LatestSequence {
		t.Fatalf("ordinary resume changed ownership: %+v err=%v", current, err)
	}
}
