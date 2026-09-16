package chat_test

import (
	"context"
	"fmt"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/lifecycle"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	chatsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/chat"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/store"
)

func TestOriginalBranchTUIRoundtripDoesNotInheritEditedBranchCheckpoint(t *testing.T) {
	ctx := context.Background()
	h, _, editedDriver := newEditHarness(t, true)
	original := completeTurn(t, h, "original", "provider-turn-1")
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool { return len(s.Messages) == 2 })
	edited, err := h.svc.EditMessage(ctx, testSession, original, ports.ChatUserMessage{
		Text: "edited", ClientMessageID: "branch-roundtrip-edit", Origin: domain.MessageOriginHuman,
	})
	if err != nil {
		t.Fatalf("edit original message: %v", err)
	}
	editedDriver.fresh.emit(
		ports.ChatEvent{Kind: ports.ChatEventTurnStarted, ProviderTurnID: "provider-turn-101"},
		ports.ChatEvent{Kind: ports.ChatEventMessageCompleted, ProviderTurnID: "provider-turn-101",
			ProviderItemID: "msg-provider-turn-101", Text: "reply to edited"},
		ports.ChatEvent{Kind: ports.ChatEventTurnCompleted, ProviderTurnID: "provider-turn-101", TurnState: domain.TurnStateCompleted},
	)
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		return len(s.Turns) == 1 && s.Turns[0].State == domain.TurnStateCompleted
	})
	if err := h.svc.Stop(ctx, testSession); err != nil {
		t.Fatal(err)
	}
	lcm := lifecycle.New(h.st, nil)
	commitMode := func(from, to domain.SessionMode, nativeID string) {
		t.Helper()
		changed, err := lcm.CommitControllerEpoch(ctx, testSession, from, to, nativeID, false)
		if err != nil || !changed {
			t.Fatalf("commit %s -> %s: changed=%v err=%v", from, to, changed, err)
		}
	}
	commitMode(domain.SessionModeChat, domain.SessionModeTUI, "thread-fresh")
	if err := lcm.MarkSpawned(ctx, testSession, domain.SessionMetadata{
		RuntimeLaunchID: "edited-terminal", RuntimeHandleID: "edited-terminal-handle",
		AgentSessionID: "thread-fresh", AgentSessionIDLaunchID: "edited-terminal",
		ProviderConversationID: "thread-fresh",
	}); err != nil {
		t.Fatal(err)
	}
	for _, signal := range []ports.ActivitySignal{
		{Valid: true, State: domain.ActivityActive, Event: "user-prompt-submit", LatestUserPrompt: "edited terminal"},
		{Valid: true, State: domain.ActivityIdle, Event: "stop", LatestAssistantUpdate: "reply to edited terminal"},
	} {
		signal.LaunchID, signal.AgentSessionID = "edited-terminal", "thread-fresh"
		signal.ConversationCheckpointOrigin = domain.ConversationCheckpointOriginHuman
		signal.Timestamp = time.Now().UTC()
		if err := lcm.ApplyActivitySignal(ctx, testSession, signal); err != nil {
			t.Fatal(err)
		}
	}
	commitMode(domain.SessionModeTUI, domain.SessionModeChat, "thread-fresh")

	// The provider returns only the requested branch, never the other branch's suffix.
	replayTurn := func(id, text string) []ports.ChatEvent {
		return []ports.ChatEvent{
			{Kind: ports.ChatEventTurnStarted, ProviderEventID: "start-" + id, ProviderTurnID: id},
			{Kind: ports.ChatEventUserMessageCompleted, ProviderEventID: "user-" + id, ProviderTurnID: id, ProviderItemID: "user-" + id, Text: text},
			{Kind: ports.ChatEventMessageCompleted, ProviderEventID: "answer-" + id, ProviderTurnID: id, ProviderItemID: "msg-" + id, Text: "reply to " + text},
			{Kind: ports.ChatEventTurnCompleted, ProviderEventID: "complete-" + id, ProviderTurnID: id, TurnState: domain.TurnStateCompleted},
		}
	}
	originalReplay := replayTurn("provider-turn-1", "original")
	editedReplay := append(replayTurn("provider-turn-101", "edited"), replayTurn("provider-turn-102", "edited terminal")...)
	driver := fakeDriver{resume: func(cfg ports.ChatResumeConfig) (ports.ChatConversation, error) {
		conv := newFakeConversation()
		conv.providerConversationID = cfg.ProviderConversationID
		switch cfg.ProviderConversationID {
		case "thread-1":
			return &nativeHistoryConversation{fakeConversation: conv, events: originalReplay}, nil
		case "thread-fresh":
			return &nativeHistoryConversation{fakeConversation: conv, events: editedReplay}, nil
		default:
			return nil, fmt.Errorf("unexpected native conversation %q", cfg.ProviderConversationID)
		}
	}}
	var nextID atomic.Int64
	svc := chatsvc.New(chatsvc.Options{
		Store: h.st, Sessions: h.st, Reader: fullSnapshotReader(h.st), Drivers: fakeRegistry{driver: driver},
		Log: slog.New(slog.DiscardHandler), NewID: func() string { return fmt.Sprintf("branch-roundtrip-%d", nextID.Add(1)) },
	})
	t.Cleanup(func() { _ = svc.Stop(context.Background(), testSession) })
	cfg := chatsvc.StartConfig{
		SessionID: testSession, ProjectID: testProject, Harness: domain.HarnessCodex,
		WorkspacePath: t.TempDir(), ProviderConversationID: "thread-fresh", RequireNativeHistory: true,
		HistoryPolicy: domain.SessionInterfaceTransitionHistoryStrict,
	}
	if _, err := svc.Start(ctx, cfg); err != nil {
		t.Fatalf("import edited Terminal reply: %v", err)
	}
	if _, err := svc.ActivateBranch(ctx, testSession, edited.SourceBranchID); err != nil {
		t.Fatalf("activate original branch: %v", err)
	}
	before, found, err := h.st.GetSession(ctx, testSession)
	if err != nil || !found || before.Metadata.ConversationCheckpointNativeID != "thread-fresh" ||
		before.Metadata.LatestUserPrompt != "edited terminal" || before.Metadata.LatestAssistantUpdate != "reply to edited terminal" {
		t.Fatalf("missing source-TUI checkpoint before roundtrip: %+v, found=%v err=%v", before.Metadata, found, err)
	}
	if err := svc.Stop(ctx, testSession); err != nil {
		t.Fatal(err)
	}
	commitMode(domain.SessionModeChat, domain.SessionModeTUI, "thread-1")
	// No new TUI input: the original branch's durable high-water mark is the only
	// replay boundary. A stale edited-branch hook must not become mandatory text.
	commitMode(domain.SessionModeTUI, domain.SessionModeChat, "thread-1")
	cfg.ProviderConversationID = "thread-1"
	if _, err := svc.Start(ctx, cfg); err != nil {
		t.Fatalf("strict original-branch return rejected valid history: %v", err)
	}
	snapshot, err := h.st.LoadConversationSnapshot(ctx, h.ctrl.ConversationID())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.ActiveBranch.ID != edited.SourceBranchID {
		t.Fatalf("active branch = %q, want original %q", snapshot.ActiveBranch.ID, edited.SourceBranchID)
	}
	requireMessageTexts(t, snapshot.Messages, []string{"original", "reply to original"})
}
