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
	for scenario, wantError := range map[string]string{
		"success": "", "provider_failure": "provider unavailable",
		"wrong_provider": "does not match requested handle", "history_failure": "transcript unavailable",
		"projection_failure": "CHECK constraint failed", "controller_changed": "controller ownership changed",
		"history_changed": "handoff history changed", "owner_changed": "no longer owned by session",
		"predecessor_revived": "not a retired predecessor", "competing_orchestrator": "competing live orchestrator",
		"missing_boundary": "incomplete native Chat handoff reservation", "missing_provider": "incomplete native Chat handoff reservation",
		"missing_callback": "incomplete native Chat handoff reservation", "skip_history": "incomplete native Chat handoff reservation",
		"wrong_scope": "incomplete native Chat handoff reservation", "stale_head_before_io": "handoff conversation changed",
		"stale_sequence_before_io":     "handoff conversation changed",
		"stale_conversation_before_io": "handoff conversation changed",
		"live_reconnect":               "independent native handoff requires a stable history replay",
	} {
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
			for _, kind := range []domain.ActivityKind{domain.ActivityKindApproval, domain.ActivityKindUserInput} {
				if err := f.store.UpsertActivity(ctx, f.conversation.ID, "old-turn", domain.ConversationActivity{
					ID: string(kind), ProviderItemID: string(kind), Kind: kind,
					Status: domain.ActivityStatusPending, RequestID: string(kind),
				}, f.now); err != nil {
					t.Fatal(err)
				}
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
			providerCalls := 0
			driver := fakeDriver{start: func(ports.ChatStartConfig) (ports.ChatConversation, error) {
				providerCalls++
				return nil, errors.New("independent handoff must not start a fresh provider")
			}, resume: func(cfg ports.ChatResumeConfig) (ports.ChatConversation, error) {
				providerCalls++
				if cfg.ProviderScopeID != handoff.BoundaryID {
					t.Fatalf("unreserved provider namespace: %s", cfg.ProviderScopeID)
				}
				current, err := f.store.ProjectConversation(ctx, testProject)
				if err != nil || current.SessionID != f.source.ID || current.ActiveBranchID != f.root.ID {
					t.Fatalf("ownership changed before provider I/O: %+v err=%v", current, err)
				}
				switch scenario {
				case "live_reconnect":
					return &liveReconnectedConversation{nativeHistoryConversation: provider}, nil
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
			cfg := chatsvc.StartConfig{
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
			}
			beforeIO := true
			switch scenario {
			case "missing_boundary":
				handoff.BoundaryID = ""
			case "missing_provider":
				cfg.ProviderConversationID = ""
			case "missing_callback":
				cfg.ControllerReady = nil
			case "skip_history":
				cfg.SkipNativeHistoryImport = true
			case "wrong_scope":
				cfg.ProviderScopeID = "unreserved"
			case "stale_head_before_io":
				handoff.PreviousBranchID = "stale-head"
			case "stale_sequence_before_io":
				handoff.PreviousSequence++
			case "stale_conversation_before_io":
				handoff.ConversationID = "stale-conversation"
			default:
				beforeIO = false
			}
			_, err = svc.Start(ctx, cfg)
			if (wantError == "" && err != nil) || (wantError != "" && (err == nil || !strings.Contains(err.Error(), wantError))) {
				t.Fatalf("want error containing %q, got %v", wantError, err)
			}
			if beforeIO && providerCalls != 0 {
				t.Fatal("invalid reservation reached the provider")
			}
			if !beforeIO && providerCalls != 1 {
				t.Fatalf("expected provider I/O, got %d calls", providerCalls)
			}
			if err != nil {
				t.Logf("rejected: %v", err)
			}
			rows, readErr := f.store.LoadConversationSnapshot(ctx, conversation.ID)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if scenario == "success" {
				for _, turn := range rows.Turns {
					if turn.ID == "old-turn" && turn.State != domain.TurnStateFailed {
						t.Fatalf("predecessor work can dispatch into the new context: %s", turn.State)
					}
				}
				for _, activity := range rows.Activities {
					if activity.RequestID != "" && activity.Status != domain.ActivityStatusFailed {
						t.Fatalf("dead predecessor request remains actionable: %+v", activity)
					}
				}
				if rows.Conversation.SessionID != f.target.ID || rows.Conversation.ActiveBranchID != handoff.BoundaryID || len(rows.Messages) != 3 {
					t.Fatalf("incomplete publication: %+v", rows)
				}
			} else {
				for _, turn := range rows.Turns {
					if turn.ID == "old-turn" && turn.State != domain.TurnStateQueued {
						t.Fatalf("failed handoff settled predecessor work: %s", turn.State)
					}
				}
				for _, activity := range rows.Activities {
					if activity.RequestID != "" && activity.Status != domain.ActivityStatusPending {
						t.Fatalf("failed handoff settled predecessor request: %+v", activity)
					}
				}
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
