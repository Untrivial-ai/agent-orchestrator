package chat_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/lifecycle"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	chatsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/chat"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
)

const (
	historicalTargetThread = "native-248"
	historicalTransitionID = "handoff-248"
)

type historicalProviderFixture struct {
	store        *sqlite.Store
	conversation domain.ConversationRecord
	root         domain.ConversationBranch
	source       domain.SessionRecord
	target       domain.SessionRecord
	now          time.Time
}

func seedHistoricalProviderFixture(t *testing.T) historicalProviderFixture {
	t.Helper()
	ctx := context.Background()
	st := openStore(t)
	now := time.Date(2026, 8, 28, 7, 0, 0, 0, time.UTC)

	source, found, err := st.GetSession(ctx, testSession)
	if err != nil || !found {
		t.Fatalf("load source session: found=%v err=%v", found, err)
	}
	source.Metadata.ProviderConversationID = "native-88"
	source.Metadata.ControllerGeneration = "generation-88"
	source.Metadata.WorkspacePath = "/worktrees/orchestrator-88"
	source.Metadata.Branch = "ao/orchestrator"
	source.Activity = domain.Activity{State: domain.ActivityIdle, LastActivityAt: now}
	source.UpdatedAt = now
	if err := st.UpdateSession(ctx, source); err != nil {
		t.Fatalf("seed source provider owner: %v", err)
	}
	conversation, err := st.CreateConversation(ctx, "project-conversation",
		domain.ConversationScopeProject, testProject, source.ID, now)
	if err != nil {
		t.Fatalf("create project conversation: %v", err)
	}
	created, err := st.AppendUserMessage(ctx, conversation.ID, source.ID,
		source.Metadata.ControllerGeneration, domain.ConversationMessage{
			ID: "old-user-message", Text: "preserve the old orchestrator transcript",
			Origin: domain.MessageOriginHuman, ClientMessageID: "old-client-message",
		}, "old-turn", now.Add(time.Second))
	if err != nil || !created {
		t.Fatalf("seed old transcript: created=%v err=%v", created, err)
	}

	target, err := st.CreateSession(ctx, domain.SessionRecord{
		ProjectID: testProject, Kind: domain.KindOrchestrator,
		Harness: domain.HarnessCodex, Mode: domain.SessionModeChat, IsTerminated: true,
		Activity: domain.Activity{State: domain.ActivityExited, LastActivityAt: now.Add(2 * time.Second)},
		Metadata: domain.SessionMetadata{
			ProviderConversationID: historicalTargetThread,
			WorkspacePath:          "/worktrees/orchestrator-248",
			Branch:                 "ao/orchestrator",
		},
		CreatedAt: now.Add(2 * time.Second), UpdatedAt: now.Add(2 * time.Second),
	})
	if err != nil {
		t.Fatalf("create historical target: %v", err)
	}
	transition, createdTransition, err := st.CreateSessionInterfaceTransition(ctx,
		domain.SessionInterfaceTransition{
			ID: historicalTransitionID, SessionID: target.ID,
			SourceMode: domain.SessionModeTUI, TargetMode: domain.SessionModeChat,
			Policy:               domain.SessionInterfaceTransitionDrain,
			Phase:                domain.SessionInterfaceTransitionRequested,
			NativeConversationID: historicalTargetThread,
			CreatedAt:            now.Add(3 * time.Second), UpdatedAt: now.Add(3 * time.Second),
		})
	if err != nil || !createdTransition {
		t.Fatalf("create historical handoff: created=%v transition=%+v err=%v",
			createdTransition, transition, err)
	}
	moved, err := st.AdvanceSessionInterfaceTransition(ctx, historicalTransitionID,
		domain.SessionInterfaceTransitionRequested, domain.SessionInterfaceTransitionCompleted,
		historicalTargetThread, "", "", now.Add(4*time.Second))
	if err != nil || !moved {
		t.Fatalf("complete historical handoff: moved=%v err=%v", moved, err)
	}
	conversation, err = st.CreateConversation(ctx, "unused-rebound-conversation",
		domain.ConversationScopeProject, testProject, target.ID, now.Add(5*time.Second))
	if err != nil {
		t.Fatalf("rebind project conversation: %v", err)
	}
	root, err := st.ConversationBranch(ctx, conversation.ID, conversation.ActiveBranchID)
	if err != nil {
		t.Fatalf("load historical root: %v", err)
	}
	if root.SessionID != source.ID || root.ProviderConversationID != "native-88" {
		t.Fatalf("historical root = %+v, want older provider owner", root)
	}
	return historicalProviderFixture{
		store: st, conversation: conversation, root: root, source: source, target: target, now: now,
	}
}

func snapshotReader(st *sqlite.Store) chatsvc.SnapshotReaderFunc {
	return func(ctx context.Context, conversationID string) (chatsvc.ConversationRows, error) {
		snapshot, err := st.LoadConversationSnapshot(ctx, conversationID)
		if err != nil {
			return chatsvc.ConversationRows{}, err
		}
		return chatsvc.ConversationRows{
			Conversation: snapshot.Conversation, Turns: snapshot.Turns,
			Messages: snapshot.Messages, Activities: snapshot.Activities,
		}, nil
	}
}

func historicalControllerReady(
	lcm *lifecycle.Manager,
	target domain.SessionRecord,
) func(chatsvc.StartResult) (chatsvc.ControllerCommit, error) {
	return func(started chatsvc.StartResult) (chatsvc.ControllerCommit, error) {
		metadata := target.Metadata
		metadata.ProviderConversationID = started.ProviderConversationID
		metadata.ControllerGeneration = started.ControllerGeneration
		if started.ProviderBoundary == nil {
			if err := lcm.MarkSpawned(context.Background(), target.ID, metadata); err != nil {
				return chatsvc.ControllerCommit{}, err
			}
			return chatsvc.ControllerCommit{Conversation: started.Conversation}, nil
		}
		var err error
		if started.CommitProviderHistory == nil {
			err = lcm.MarkChatSpawned(
				context.Background(), target.ID, metadata, *started.ProviderBoundary,
			)
		} else {
			err = lcm.MarkChatSpawnedPrepared(
				context.Background(), target.ID, metadata, *started.ProviderBoundary,
				started.CommitProviderHistory,
			)
		}
		if err != nil {
			return chatsvc.ControllerCommit{}, err
		}
		conversation := started.Conversation
		conversation.ActiveBranchID = started.ProviderBoundary.ID
		conversation.UpdatedAt = started.ProviderBoundary.CreatedAt
		return chatsvc.ControllerCommit{Conversation: conversation}, nil
	}
}

func historicalNativeHistory() []ports.ChatEvent {
	return []ports.ChatEvent{
		{
			Kind: ports.ChatEventTurnStarted, ProviderEventID: "history-turn-started",
			ProviderConversationID: historicalTargetThread, ProviderTurnID: "history-turn",
		},
		{
			Kind: ports.ChatEventUserMessageCompleted, ProviderEventID: "history-user-message",
			ProviderConversationID: historicalTargetThread, ProviderTurnID: "history-turn",
			ProviderItemID: "history-user-item", Text: "restore my prior question",
		},
		{
			Kind: ports.ChatEventMessageCompleted, ProviderEventID: "history-assistant-message",
			ProviderConversationID: historicalTargetThread, ProviderTurnID: "history-turn",
			ProviderItemID: "history-assistant-item", Text: "restored prior answer",
		},
		{
			Kind: ports.ChatEventTurnCompleted, ProviderEventID: "history-turn-completed",
			ProviderConversationID: historicalTargetThread, ProviderTurnID: "history-turn",
			TurnState: domain.TurnStateCompleted,
		},
	}
}

func TestHistoricalProjectProviderRestoreAppendsOwnershipEpochAtomically(t *testing.T) {
	fixture := seedHistoricalProviderFixture(t)
	ctx := context.Background()
	var scopes []string
	firstConversation := &nativeHistoryConversation{
		fakeConversation: newFakeConversation(), events: historicalNativeHistory(),
	}
	conversations := []*nativeHistoryConversation{
		firstConversation,
		{fakeConversation: newFakeConversation(), events: historicalNativeHistory()},
	}
	for _, conversation := range conversations {
		conversation.providerConversationID = historicalTargetThread
	}
	nextID := 0
	driver := fakeDriver{resume: func(cfg ports.ChatResumeConfig) (ports.ChatConversation, error) {
		scopes = append(scopes, cfg.ProviderScopeID)
		if len(conversations) == 0 {
			return nil, errors.New("no provider conversation queued")
		}
		conversation := conversations[0]
		conversations = conversations[1:]
		return conversation, nil
	}}
	lcm := lifecycle.New(fixture.store, nil)
	svc := chatsvc.New(chatsvc.Options{
		Store: fixture.store, Reader: snapshotReader(fixture.store), Sessions: fixture.store,
		Drivers: fakeRegistry{driver: driver}, NewID: func() string {
			nextID++
			return fmt.Sprintf("historical-id-%d", nextID)
		},
		Now: func() time.Time { return fixture.now.Add(10 * time.Second) },
	})
	t.Cleanup(func() { _ = svc.Stop(context.Background(), fixture.target.ID) })

	controller, err := svc.Start(ctx, chatsvc.StartConfig{
		SessionID: fixture.target.ID, ProjectID: testProject,
		Kind: domain.KindOrchestrator, Harness: domain.HarnessCodex,
		WorkspacePath:          fixture.target.Metadata.WorkspacePath,
		ProviderConversationID: historicalTargetThread,
		ProviderScopeID:        historicalTransitionID + ":provider",
		ControllerReady:        historicalControllerReady(lcm, fixture.target),
	})
	if err != nil {
		t.Fatalf("historical provider restore: %v", err)
	}
	if controller.ProviderConversationID() != historicalTargetThread {
		t.Fatalf("provider conversation = %q, want %q",
			controller.ProviderConversationID(), historicalTargetThread)
	}
	if firstConversation.historyReads() != 1 {
		t.Fatalf("native history reads = %d, want successful import before ownership commit",
			firstConversation.historyReads())
	}

	restored, found, err := fixture.store.GetSession(ctx, fixture.target.ID)
	if err != nil || !found {
		t.Fatalf("load restored target: found=%v err=%v", found, err)
	}
	if restored.IsTerminated || restored.ID != fixture.target.ID ||
		restored.Metadata.ProviderConversationID != historicalTargetThread ||
		restored.Metadata.ControllerGeneration == "" ||
		restored.Metadata.WorkspacePath != fixture.target.Metadata.WorkspacePath ||
		restored.Metadata.Branch != fixture.target.Metadata.Branch {
		t.Fatalf("restored target lost identity/workspace: %+v", restored)
	}
	conversation, err := fixture.store.ConversationForSession(ctx, fixture.target.ID)
	if err != nil {
		t.Fatalf("load restored conversation: %v", err)
	}
	wantBoundary := historicalTransitionID + ":provider"
	if conversation.ID != fixture.conversation.ID || conversation.ActiveBranchID != wantBoundary {
		t.Fatalf("restored conversation = %+v, want original narrative with active %q",
			conversation, wantBoundary)
	}
	root, err := fixture.store.ConversationBranch(ctx, conversation.ID, fixture.root.ID)
	if err != nil {
		t.Fatalf("reload historical root: %v", err)
	}
	if root.SessionID != fixture.source.ID || root.ProviderConversationID != "native-88" || root.ParentBranchID != "" {
		t.Fatalf("historical root was rewritten: %+v", root)
	}
	boundary, err := fixture.store.ConversationBranch(ctx, conversation.ID, wantBoundary)
	if err != nil {
		t.Fatalf("load provider boundary: %v", err)
	}
	if !boundary.Active || boundary.ParentBranchID != root.ID ||
		boundary.SessionID != fixture.target.ID ||
		boundary.ProviderConversationID != historicalTargetThread ||
		boundary.ProviderScopeID != wantBoundary ||
		boundary.ForkAfterSequence != fixture.conversation.LatestSequence {
		t.Fatalf("provider ownership epoch = %+v", boundary)
	}
	snapshot, err := fixture.store.LoadConversationSnapshot(ctx, conversation.ID)
	if err != nil {
		t.Fatalf("load restored transcript: %v", err)
	}
	if len(snapshot.Messages) != 3 ||
		snapshot.Messages[0].Text != "preserve the old orchestrator transcript" ||
		snapshot.Messages[1].Text != "restore my prior question" ||
		snapshot.Messages[2].Text != "restored prior answer" {
		t.Fatalf("restored transcript = %+v", snapshot.Messages)
	}
	if len(snapshot.Turns) != 2 || snapshot.Turns[1].ProviderTurnID != "history-turn" ||
		snapshot.Turns[1].State != domain.TurnStateCompleted ||
		snapshot.Turns[1].HandledBySessionID != fixture.target.ID ||
		snapshot.Turns[1].BranchID != wantBoundary {
		t.Fatalf("restored native turn = %+v", snapshot.Turns)
	}
	providerEvents, err := fixture.store.ProviderEventsSince(ctx, conversation.ID, 0, 20)
	if err != nil || len(providerEvents) != 4 {
		t.Fatalf("restored provider events = %+v err=%v, want four", providerEvents, err)
	}
	for _, event := range providerEvents {
		if event.BranchID != wantBoundary || event.SessionID != fixture.target.ID {
			t.Fatalf("provider event was not committed on recovered ownership epoch: %+v", event)
		}
	}
	if _, err := svc.Send(ctx, fixture.target.ID, ports.ChatUserMessage{
		Text: "continue on the same native conversation", ClientMessageID: "post-restore-message",
	}); err != nil {
		t.Fatalf("send after historical restore: %v", err)
	}
	if got := scopes; len(got) != 1 || got[0] != wantBoundary {
		t.Fatalf("first provider resume scopes = %v, want [%s]", got, wantBoundary)
	}

	// A restart after the atomic commit follows the ordinary path. The active
	// branch now owns the target handle, so no second ownership epoch is needed.
	if err := svc.Stop(ctx, fixture.target.ID); err != nil {
		t.Fatalf("stop first restored controller: %v", err)
	}
	if _, err := svc.Start(ctx, chatsvc.StartConfig{
		SessionID: fixture.target.ID, ProjectID: testProject,
		Kind: domain.KindOrchestrator, Harness: domain.HarnessCodex,
		WorkspacePath:          fixture.target.Metadata.WorkspacePath,
		ProviderConversationID: historicalTargetThread,
		ControllerReady:        historicalControllerReady(lcm, restored),
	}); err != nil {
		t.Fatalf("ordinary retry after committed epoch: %v", err)
	}
	retried, found, err := fixture.store.GetSession(ctx, fixture.target.ID)
	if err != nil || !found {
		t.Fatalf("load retried target: found=%v err=%v", found, err)
	}
	if retried.Metadata.ControllerGeneration == restored.Metadata.ControllerGeneration {
		t.Fatalf("controller generation did not advance across retry: %q",
			retried.Metadata.ControllerGeneration)
	}
	branches, err := fixture.store.ConversationBranches(ctx, conversation.ID)
	if err != nil {
		t.Fatalf("list branches after retry: %v", err)
	}
	if len(branches) != 2 || len(scopes) != 2 || scopes[1] != wantBoundary {
		t.Fatalf("retry created a duplicate epoch: branches=%+v scopes=%v", branches, scopes)
	}
}

func TestHistoricalProjectProviderRestoreFailureNeverPublishesEpoch(t *testing.T) {
	tests := []struct {
		name   string
		driver func() ports.ChatDriver
	}{
		{
			name: "provider resume",
			driver: func() ports.ChatDriver {
				return fakeDriver{resume: func(ports.ChatResumeConfig) (ports.ChatConversation, error) {
					return nil, errors.New("provider resume failed")
				}}
			},
		},
		{
			name: "native history",
			driver: func() ports.ChatDriver {
				conversation := &nativeHistoryConversation{
					fakeConversation: newFakeConversation(), err: errors.New("history read failed"),
				}
				conversation.providerConversationID = historicalTargetThread
				return fakeDriver{conv: conversation}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := seedHistoricalProviderFixture(t)
			lcm := lifecycle.New(fixture.store, nil)
			svc := chatsvc.New(chatsvc.Options{
				Store: fixture.store, Reader: snapshotReader(fixture.store), Sessions: fixture.store,
				Drivers: fakeRegistry{driver: tt.driver()}, NewID: func() string { return "generation-248" },
			})
			_, err := svc.Start(context.Background(), chatsvc.StartConfig{
				SessionID: fixture.target.ID, ProjectID: testProject,
				Kind: domain.KindOrchestrator, Harness: domain.HarnessCodex,
				WorkspacePath:          fixture.target.Metadata.WorkspacePath,
				ProviderConversationID: historicalTargetThread,
				ProviderScopeID:        historicalTransitionID + ":provider",
				ControllerReady:        historicalControllerReady(lcm, fixture.target),
			})
			if err == nil {
				t.Fatal("historical restore unexpectedly succeeded")
			}
			assertHistoricalTargetUnchanged(t, fixture)
		})
	}
}

func TestHistoricalProjectProviderRestoreRejectsWrongReturnedHandle(t *testing.T) {
	fixture := seedHistoricalProviderFixture(t)
	conversation := &nativeHistoryConversation{
		fakeConversation: newFakeConversation(), events: historicalNativeHistory(),
	}
	conversation.providerConversationID = "native-WRONG"
	svc := chatsvc.New(chatsvc.Options{
		Store: fixture.store, Reader: snapshotReader(fixture.store), Sessions: fixture.store,
		Drivers: fakeRegistry{driver: fakeDriver{conv: conversation}},
		NewID:   func() string { return "generation-248" },
	})
	_, err := svc.Start(context.Background(), chatsvc.StartConfig{
		SessionID: fixture.target.ID, ProjectID: testProject,
		Kind: domain.KindOrchestrator, Harness: domain.HarnessCodex,
		WorkspacePath:          fixture.target.Metadata.WorkspacePath,
		ProviderConversationID: historicalTargetThread,
		ProviderScopeID:        historicalTransitionID + ":provider",
	})
	if err == nil || !strings.Contains(err.Error(), "does not match requested handle") {
		t.Fatalf("wrong returned handle error = %v", err)
	}
	if conversation.historyReads() != 0 {
		t.Fatalf("wrong provider history reads = %d, want rejection before history import",
			conversation.historyReads())
	}
	assertHistoricalTargetUnchanged(t, fixture)
}

type rollbackChatSpawnStore struct{ *sqlite.Store }

func (s *rollbackChatSpawnStore) CommitChatSpawn(
	ctx context.Context,
	rec domain.SessionRecord,
	branch domain.ConversationBranch,
) error {
	// Fail the final session write after the branch insert and head move have run
	// inside Store.CommitChatSpawn's transaction.
	rec.Activity.State = domain.ActivityState("invalid-state")
	return s.Store.CommitChatSpawn(ctx, rec, branch)
}

func (s *rollbackChatSpawnStore) CommitChatSpawnPrepared(
	ctx context.Context,
	rec domain.SessionRecord,
	branch domain.ConversationBranch,
	prepare func(context.Context) error,
) error {
	// Exercise rollback after branch activation and native-history projection by
	// invalidating only the final lifecycle record write.
	rec.Activity.State = domain.ActivityState("invalid-state")
	return s.Store.CommitChatSpawnPrepared(ctx, rec, branch, prepare)
}

func TestHistoricalProjectProviderRestoreCommitFailureRollsBack(t *testing.T) {
	fixture := seedHistoricalProviderFixture(t)
	wrapped := &rollbackChatSpawnStore{Store: fixture.store}
	conversation := &nativeHistoryConversation{
		fakeConversation: newFakeConversation(), events: historicalNativeHistory(),
	}
	conversation.providerConversationID = historicalTargetThread
	lcm := lifecycle.New(wrapped, nil)
	svc := chatsvc.New(chatsvc.Options{
		Store: wrapped, Reader: snapshotReader(fixture.store), Sessions: wrapped,
		Drivers: fakeRegistry{driver: fakeDriver{conv: conversation}},
		NewID:   func() string { return "generation-248" },
	})
	_, err := svc.Start(context.Background(), chatsvc.StartConfig{
		SessionID: fixture.target.ID, ProjectID: testProject,
		Kind: domain.KindOrchestrator, Harness: domain.HarnessCodex,
		WorkspacePath:          fixture.target.Metadata.WorkspacePath,
		ProviderConversationID: historicalTargetThread,
		ProviderScopeID:        historicalTransitionID + ":provider",
		ControllerReady:        historicalControllerReady(lcm, fixture.target),
	})
	if err == nil || !strings.Contains(err.Error(), "commit chat controller") {
		t.Fatalf("commit failure = %v, want controller commit error", err)
	}
	assertHistoricalTargetUnchanged(t, fixture)
}

func TestHistoricalProjectProviderRestoreRejectsUnprovedMismatch(t *testing.T) {
	fixture := seedHistoricalProviderFixture(t)
	resumeCalls := 0
	svc := chatsvc.New(chatsvc.Options{
		Store: fixture.store, Reader: snapshotReader(fixture.store), Sessions: fixture.store,
		Drivers: fakeRegistry{driver: fakeDriver{resume: func(ports.ChatResumeConfig) (ports.ChatConversation, error) {
			resumeCalls++
			return newFakeConversation(), nil
		}}},
		NewID: func() string { return "unused-generation" },
	})
	_, err := svc.Start(context.Background(), chatsvc.StartConfig{
		SessionID: fixture.target.ID, ProjectID: testProject,
		Kind: domain.KindOrchestrator, Harness: domain.HarnessCodex,
		WorkspacePath:          fixture.target.Metadata.WorkspacePath,
		ProviderConversationID: historicalTargetThread,
		// No Session Manager proof => no reserved ProviderScopeID.
	})
	if err == nil || !strings.Contains(err.Error(), "does not match session handle") {
		t.Fatalf("unproved mismatch error = %v", err)
	}
	if resumeCalls != 0 {
		t.Fatalf("provider resume calls = %d, want rejection before provider launch", resumeCalls)
	}
	assertHistoricalTargetUnchanged(t, fixture)
}

func TestHistoricalProjectProviderRestoreRejectsStaleOwnerAndHead(t *testing.T) {
	t.Run("owner changed before provider open", func(t *testing.T) {
		fixture := seedHistoricalProviderFixture(t)
		other, err := fixture.store.CreateSession(context.Background(), domain.SessionRecord{
			ProjectID: testProject, Kind: domain.KindOrchestrator,
			Harness: domain.HarnessCodex, Mode: domain.SessionModeChat,
			Activity:  domain.Activity{State: domain.ActivityIdle},
			CreatedAt: fixture.now, UpdatedAt: fixture.now,
		})
		if err != nil {
			t.Fatalf("create newer owner: %v", err)
		}
		if _, err := fixture.store.CreateConversation(context.Background(), "unused-newer-owner",
			domain.ConversationScopeProject, testProject, other.ID, fixture.now.Add(time.Hour)); err != nil {
			t.Fatalf("rebind newer owner: %v", err)
		}
		resumeCalls := 0
		svc := chatsvc.New(chatsvc.Options{
			Store: fixture.store, Reader: snapshotReader(fixture.store), Sessions: fixture.store,
			Drivers: fakeRegistry{driver: fakeDriver{resume: func(ports.ChatResumeConfig) (ports.ChatConversation, error) {
				resumeCalls++
				return newFakeConversation(), nil
			}}},
			NewID: func() string { return "generation-248" },
		})
		_, err = svc.Start(context.Background(), chatsvc.StartConfig{
			SessionID: fixture.target.ID, ProjectID: testProject,
			Kind: domain.KindOrchestrator, Harness: domain.HarnessCodex,
			WorkspacePath:          fixture.target.Metadata.WorkspacePath,
			ProviderConversationID: historicalTargetThread,
			ProviderScopeID:        historicalTransitionID + ":provider",
		})
		if !errors.Is(err, domain.ErrNoConversation) {
			t.Fatalf("stale pre-open owner error = %v, want ErrNoConversation", err)
		}
		if resumeCalls != 0 {
			t.Fatalf("provider resume calls = %d, want owner rejection before provider I/O", resumeCalls)
		}
		owned, err := fixture.store.ConversationForSession(context.Background(), other.ID)
		if err != nil || owned.ID != fixture.conversation.ID {
			t.Fatalf("newer owner was rebound: conversation=%+v err=%v", owned, err)
		}
		assertNoHistoricalBoundary(t, fixture)
	})

	t.Run("conversation owner", func(t *testing.T) {
		fixture := seedHistoricalProviderFixture(t)
		other, err := fixture.store.CreateSession(context.Background(), domain.SessionRecord{
			ID: "p1-249", ProjectID: testProject, Kind: domain.KindOrchestrator,
			Harness: domain.HarnessCodex, Mode: domain.SessionModeChat,
			Activity:  domain.Activity{State: domain.ActivityIdle},
			CreatedAt: fixture.now, UpdatedAt: fixture.now,
		})
		if err != nil {
			t.Fatalf("create competing owner: %v", err)
		}
		conversation := &nativeHistoryConversation{fakeConversation: newFakeConversation()}
		conversation.providerConversationID = historicalTargetThread
		lcm := lifecycle.New(fixture.store, nil)
		svc := chatsvc.New(chatsvc.Options{
			Store: fixture.store, Reader: snapshotReader(fixture.store), Sessions: fixture.store,
			Drivers: fakeRegistry{driver: fakeDriver{conv: conversation}},
			NewID:   func() string { return "generation-248" },
		})
		_, err = svc.Start(context.Background(), chatsvc.StartConfig{
			SessionID: fixture.target.ID, ProjectID: testProject,
			Kind: domain.KindOrchestrator, Harness: domain.HarnessCodex,
			WorkspacePath:          fixture.target.Metadata.WorkspacePath,
			ProviderConversationID: historicalTargetThread,
			ProviderScopeID:        historicalTransitionID + ":provider",
			ControllerReady: func(started chatsvc.StartResult) (chatsvc.ControllerCommit, error) {
				if _, err := fixture.store.CreateConversation(context.Background(), "unused-race",
					domain.ConversationScopeProject, testProject, other.ID, fixture.now.Add(time.Hour)); err != nil {
					return chatsvc.ControllerCommit{}, err
				}
				return historicalControllerReady(lcm, fixture.target)(started)
			},
		})
		if err == nil || !strings.Contains(err.Error(), "no longer owned") {
			t.Fatalf("stale owner error = %v", err)
		}
		assertNoHistoricalBoundary(t, fixture)
	})

	t.Run("active head", func(t *testing.T) {
		fixture := seedHistoricalProviderFixture(t)
		competing, err := fixture.store.CreateSession(context.Background(), domain.SessionRecord{
			ID: "p1-249", ProjectID: testProject, Kind: domain.KindOrchestrator,
			Harness: domain.HarnessCodex, Mode: domain.SessionModeChat,
			Activity:  domain.Activity{State: domain.ActivityIdle},
			CreatedAt: fixture.now, UpdatedAt: fixture.now,
		})
		if err != nil {
			t.Fatalf("create competing head owner: %v", err)
		}
		conversation := &nativeHistoryConversation{fakeConversation: newFakeConversation()}
		conversation.providerConversationID = historicalTargetThread
		lcm := lifecycle.New(fixture.store, nil)
		svc := chatsvc.New(chatsvc.Options{
			Store: fixture.store, Reader: snapshotReader(fixture.store), Sessions: fixture.store,
			Drivers: fakeRegistry{driver: fakeDriver{conv: conversation}},
			NewID:   func() string { return "generation-248" },
		})
		_, err = svc.Start(context.Background(), chatsvc.StartConfig{
			SessionID: fixture.target.ID, ProjectID: testProject,
			Kind: domain.KindOrchestrator, Harness: domain.HarnessCodex,
			WorkspacePath:          fixture.target.Metadata.WorkspacePath,
			ProviderConversationID: historicalTargetThread,
			ProviderScopeID:        historicalTransitionID + ":provider",
			ControllerReady: func(started chatsvc.StartResult) (chatsvc.ControllerCommit, error) {
				err := fixture.store.CreateAndActivateConversationBranch(context.Background(), competing.ID,
					domain.ConversationBranch{
						ID: "competing-head", ConversationID: fixture.conversation.ID,
						SessionID: competing.ID, ProviderConversationID: "native-249",
						ParentBranchID: fixture.root.ID, ForkAfterSequence: fixture.conversation.LatestSequence,
						ProviderScopeID: "competing-head", CreatedAt: fixture.now.Add(time.Hour),
					}, "generation-249", fixture.now.Add(time.Hour))
				if err != nil {
					return chatsvc.ControllerCommit{}, fmt.Errorf("create competing head: %w", err)
				}
				return historicalControllerReady(lcm, fixture.target)(started)
			},
		})
		if err == nil || !strings.Contains(err.Error(), "active branch changed") {
			t.Fatalf("stale head error = %v", err)
		}
		assertNoHistoricalBoundary(t, fixture)
	})
}

func assertHistoricalTargetUnchanged(t *testing.T, fixture historicalProviderFixture) {
	t.Helper()
	record, found, err := fixture.store.GetSession(context.Background(), fixture.target.ID)
	if err != nil || !found {
		t.Fatalf("load historical target: found=%v err=%v", found, err)
	}
	if !record.IsTerminated || record.Metadata.ProviderConversationID != historicalTargetThread ||
		record.Metadata.ControllerGeneration != fixture.target.Metadata.ControllerGeneration ||
		record.Metadata.WorkspacePath != fixture.target.Metadata.WorkspacePath ||
		record.Metadata.Branch != fixture.target.Metadata.Branch {
		t.Fatalf("failed restore changed historical target: %+v", record)
	}
	assertNoHistoricalBoundary(t, fixture)
	snapshot, err := fixture.store.LoadConversationSnapshot(
		context.Background(), fixture.conversation.ID,
	)
	if err != nil {
		t.Fatalf("load conversation after failed restore: %v", err)
	}
	if snapshot.Conversation.ActiveBranchID != fixture.root.ID ||
		len(snapshot.Messages) != 1 ||
		snapshot.Messages[0].Text != "preserve the old orchestrator transcript" {
		t.Fatalf("failed restore changed transcript/head: %+v", snapshot)
	}
	events, err := fixture.store.ProviderEventsSince(
		context.Background(), fixture.conversation.ID, 0, 20,
	)
	if err != nil || len(events) != 0 {
		t.Fatalf("failed restore committed provider events = %+v err=%v", events, err)
	}
}

func assertNoHistoricalBoundary(t *testing.T, fixture historicalProviderFixture) {
	t.Helper()
	if _, err := fixture.store.ConversationBranch(context.Background(), fixture.conversation.ID,
		historicalTransitionID+":provider"); !errors.Is(err, domain.ErrNoConversationBranch) {
		t.Fatalf("historical boundary exists after failed commit: %v", err)
	}
}
