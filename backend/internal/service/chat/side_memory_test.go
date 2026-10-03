package chat

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func TestMemorySideStoreReusesAnchorAcrossConcurrentCreates(t *testing.T) {
	ctx := context.Background()
	store := newMemorySideStore()
	_, _ = store.ClaimSideLaunch(ctx, "launch-1", time.Now())
	const workers = 32
	ids := make(chan string, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			side, _, err := store.CreateSideConversation(ctx, domain.SideConversation{
				ID: string(rune('a' + n)), SessionID: "session-1", MainConversationID: "main-1",
				AnchorTurnID: "anchor-1", AppRunID: "launch-1", CreateKey: string(rune('A' + n)), Generation: "g",
			})
			if err != nil {
				t.Error(err)
				return
			}
			ids <- side.ID
		}(i)
	}
	wg.Wait()
	close(ids)
	first := ""
	for id := range ids {
		if first == "" {
			first = id
		}
		if id != first {
			t.Fatalf("created multiple sides: %q and %q", first, id)
		}
	}
	next, created, err := store.CreateSideConversation(ctx, domain.SideConversation{
		ID: "next-anchor", SessionID: "session-1", MainConversationID: "main-1", AnchorTurnID: "anchor-2", AppRunID: "launch-1",
	})
	if err != nil || !created || next.ID != "next-anchor" {
		t.Fatalf("next anchor create = %#v, %v, %v", next, created, err)
	}
	listed, err := store.ListSideConversations(ctx, "session-1", "launch-1")
	if err != nil || len(listed) != 2 {
		t.Fatalf("open sides = %#v, %v", listed, err)
	}
	for i := 3; i <= 12; i++ {
		if _, created, err := store.CreateSideConversation(ctx, domain.SideConversation{
			ID: fmt.Sprintf("side-%d", i), SessionID: "session-1", MainConversationID: "main-1",
			AnchorTurnID: fmt.Sprintf("anchor-%d", i), AppRunID: "launch-1",
		}); err != nil || !created {
			t.Fatalf("side %d: created=%v err=%v", i, created, err)
		}
	}
	listed, err = store.ListSideConversations(ctx, "session-1", "launch-1")
	if err != nil || len(listed) != 12 {
		t.Fatalf("open sides above previous limits = %d, %v", len(listed), err)
	}
	if _, err := store.CloseSideConversation(ctx, first, time.Now()); err != nil {
		t.Fatal(err)
	}
	other, created, err := store.CreateSideConversation(ctx, domain.SideConversation{ID: "replacement", SessionID: "session-1", MainConversationID: "main-1", AnchorTurnID: "anchor-1", AppRunID: "launch-1"})
	if err != nil || !created || other.ID != "replacement" {
		t.Fatalf("close then create = %#v, %v, %v", other, created, err)
	}
}

func TestMemorySideStoreRecoversMultipleAnchorsForOneMain(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	store := newMemorySideStore()
	_, _ = store.ClaimSideLaunch(ctx, "launch-1", now)
	records := []SideRecoveryRecord{
		{Side: domain.SideConversation{ID: "side-1", SessionID: "session-1", MainConversationID: "main-1", AnchorTurnID: "turn-1", State: "ready"}, ProviderHostID: "btw-side-1", ProviderForkID: "fork-1", Generation: "generation-1"},
		{Side: domain.SideConversation{ID: "side-2", SessionID: "session-1", MainConversationID: "main-1", AnchorTurnID: "turn-2", State: "ready"}, ProviderHostID: "btw-side-2", ProviderForkID: "fork-2", Generation: "generation-2"},
	}
	created, err := store.recover("launch-1", records, now)
	if err != nil || len(created) != 2 {
		t.Fatalf("recover multiple anchors = %#v, %v", created, err)
	}
	listed, err := store.ListSideConversations(ctx, "session-1", "launch-1")
	if err != nil || len(listed) != 2 {
		t.Fatalf("recovered sides = %#v, %v", listed, err)
	}
}

func TestMemorySideStoreRecoversInFlightTurnWithoutDurableRows(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	before := newMemorySideStore()
	_, _ = before.ClaimSideLaunch(ctx, "launch-1", now)
	side := domain.SideConversation{ID: "side-1", SessionID: "session-1", MainConversationID: "main-1", AppRunID: "launch-1", ProviderHostID: "btw-side-1", ProviderForkID: "provider-1", Generation: "generation-1", State: "ready"}
	_, _, _ = before.CreateSideConversation(ctx, side)
	_, _, _ = before.ReserveSideTurn(ctx, domain.SideTurn{ID: "turn-1", SideID: side.ID, ClientMessageID: "client-1", Text: "question", CreatedAt: now}, "launch-1")
	_, _, _, _ = before.ClaimNextSideTurn(ctx, "launch-1", nil, now)
	state := before.export("launch-1")
	after := newMemorySideStore()
	_, _ = after.ClaimSideLaunch(ctx, "launch-1", now)
	recovered, err := after.recover("launch-1", state, now)
	if err != nil || len(recovered) != 1 {
		t.Fatalf("recover = %#v, %v", recovered, err)
	}
	turns, _, err := after.SideTurns(ctx, side.ID, time.Time{}, 10)
	if err != nil || len(turns) != 1 || turns[0].State != "failed" {
		t.Fatalf("recovered turns = %#v, %v", turns, err)
	}
}

func TestMemorySideStoreRecoveryReplayKeepsLiveSideState(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	store := newMemorySideStore()
	_, _ = store.ClaimSideLaunch(ctx, "launch-1", now)
	record := SideRecoveryRecord{Side: domain.SideConversation{
		ID: "side-1", SessionID: "session-1", MainConversationID: "main-1", State: "ready",
	}, ProviderHostID: "btw-side-1", ProviderForkID: "fork-1", Generation: "generation-1"}
	if created, err := store.recover("launch-1", []SideRecoveryRecord{record}, now); err != nil || len(created) != 1 {
		t.Fatalf("first recovery = %#v, %v", created, err)
	}
	if err := store.SetSideReady(ctx, "side-1", "generation-1", "fork-1", now); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.ReserveSideTurn(ctx, domain.SideTurn{
		ID: "turn-1", SideID: "side-1", ClientMessageID: "client-1", Text: "new question", CreatedAt: now,
	}, "launch-1"); err != nil {
		t.Fatal(err)
	}
	if created, err := store.recover("launch-1", []SideRecoveryRecord{record}, now); err != nil || len(created) != 0 {
		t.Fatalf("replayed recovery = %#v, %v", created, err)
	}
	turns, _, err := store.SideTurns(ctx, "side-1", time.Time{}, 10)
	if err != nil || len(turns) != 1 || turns[0].Text != "new question" {
		t.Fatalf("live turn after replay = %#v, %v", turns, err)
	}
}

func TestMemorySideStoreFreezesReferencesAndIncrementsMessageRevision(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	store := newMemorySideStore()
	_, _ = store.ClaimSideLaunch(ctx, "launch-1", now)
	_, _, err := store.CreateSideConversation(ctx, domain.SideConversation{ID: "side-1", SessionID: "session-1", MainConversationID: "main-1", AppRunID: "launch-1", Generation: "g", State: "ready"})
	if err != nil {
		t.Fatal(err)
	}
	turn := domain.SideTurn{ID: "turn-1", SideID: "side-1", ClientMessageID: "client-1", Text: "What is this?", CreatedAt: now,
		References: []domain.SideReference{{ConversationID: "side-1", MessageID: "source", Revision: 1, Selection: "sun"}}}
	if _, created, err := store.ReserveSideTurn(ctx, turn, "launch-1"); err != nil || !created {
		t.Fatalf("reserve: %v, %v", created, err)
	}
	if _, created, err := store.ReserveSideTurn(ctx, turn, "launch-1"); err != nil || created {
		t.Fatalf("duplicate: %v, %v", created, err)
	}
	changed := turn
	changed.References = []domain.SideReference{{ConversationID: "side-1", MessageID: "source", Revision: 1, Selection: "moon"}}
	if _, _, err := store.ReserveSideTurn(ctx, changed, "launch-1"); !errors.Is(err, ErrSideIdempotencyConflict) {
		t.Fatalf("reference mismatch: %v", err)
	}
	first := domain.SideMessage{ID: "source", SideID: "side-1", TurnID: "turn-1", Role: "assistant", Text: "sun", Streaming: true, CreatedAt: now, UpdatedAt: now}
	if err := store.UpsertSideMessage(ctx, first, "g"); err != nil {
		t.Fatal(err)
	}
	first.Text = "sun and moon"
	first.Streaming = false
	if err := store.UpsertSideMessage(ctx, first, "g"); err != nil {
		t.Fatal(err)
	}
	messages, err := store.SideMessages(ctx, "side-1", []string{"turn-1"})
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range messages {
		if message.ID == "source" && message.Revision != 2 {
			t.Fatalf("revision = %d, want 2", message.Revision)
		}
	}
}

func TestMemorySideStoreResolvesProjectedUserInput(t *testing.T) {
	ctx := context.Background()
	store := newMemorySideStore()
	_, _ = store.ClaimSideLaunch(ctx, "launch-1", time.Now())
	_, _, err := store.CreateSideConversation(ctx, domain.SideConversation{
		ID: "side-1", SessionID: "session-1", MainConversationID: "main-1", AppRunID: "launch-1", Generation: "g",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertSideActivity("side-1", "g", domain.SideActivity{
		ID: "activity-1", SideID: "side-1", Kind: "user_input", Status: "pending", RequestID: "request-1",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SideInput("side-1", "request-1"); err != nil {
		t.Fatalf("pending user input unavailable: %v", err)
	}
}

func TestMemorySideStoreLateActivityDeltaCannotReopenCompletedActivity(t *testing.T) {
	ctx := context.Background()
	store := newMemorySideStore()
	_, _ = store.ClaimSideLaunch(ctx, "launch-1", time.Now())
	_, _, err := store.CreateSideConversation(ctx, domain.SideConversation{
		ID: "side-1", SessionID: "session-1", MainConversationID: "main-1", AppRunID: "launch-1", Generation: "g",
	})
	if err != nil {
		t.Fatal(err)
	}
	completed := domain.SideActivity{ID: "activity-1", SideID: "side-1", TurnID: "turn-1", Kind: "command", Status: "completed", Summary: "run"}
	if err := store.UpsertSideActivity("side-1", "g", completed); err != nil {
		t.Fatal(err)
	}
	late := completed
	late.Status = "running"
	late.Text = "late output"
	if err := store.UpsertSideActivity("side-1", "g", late); err != nil {
		t.Fatal(err)
	}
	activities := store.SideActivities("side-1", []string{"turn-1"})
	if len(activities) != 1 || activities[0].Status != "completed" || activities[0].Text != "late output" {
		t.Fatalf("late activity delta = %#v", activities)
	}
}

func TestMemorySideStoreKeepsInterruptedOpeningVisibleAfterRecovery(t *testing.T) {
	now := time.Now().UTC()
	store := newMemorySideStore()
	_, _ = store.ClaimSideLaunch(context.Background(), "launch-1", now)
	record := SideRecoveryRecord{
		Side:           domain.SideConversation{ID: "side-1", SessionID: "session-1", MainConversationID: "main-1", State: "opening"},
		ProviderHostID: "btw-side-1", Draft: "unfinished question",
	}
	recovered, err := store.recover("launch-1", []SideRecoveryRecord{record}, now)
	if err != nil || len(recovered) != 1 || recovered[0].State != "failed" {
		t.Fatalf("recover opening = %#v, %v", recovered, err)
	}
	draft, err := store.SideDraft(context.Background(), "side-1")
	if err != nil || draft != "unfinished question" {
		t.Fatalf("recovered draft = %q, %v", draft, err)
	}
}

func TestMemorySideStoreHasNoTurnLimitAndNewLaunchClearsSides(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	store := newMemorySideStore()
	_, _ = store.ClaimSideLaunch(ctx, "launch-1", now)
	side := domain.SideConversation{ID: "side-1", SessionID: "session-1", MainConversationID: "main-1", AppRunID: "launch-1", State: "ready"}
	_, _, _ = store.CreateSideConversation(ctx, side)
	for i := 0; i < 60; i++ {
		turn := domain.SideTurn{ID: string(rune(1000 + i)), SideID: side.ID, ClientMessageID: string(rune(2000 + i)), Text: "question", CreatedAt: now.Add(time.Duration(i) * time.Second)}
		if _, created, err := store.ReserveSideTurn(ctx, turn, "launch-1"); err != nil || !created {
			t.Fatalf("turn %d: created=%v err=%v", i, created, err)
		}
	}
	turns, _, err := store.SideTurns(ctx, side.ID, time.Time{}, 100)
	if err != nil || len(turns) != 60 {
		t.Fatalf("turns=%d err=%v", len(turns), err)
	}
	retired, err := store.ClaimSideLaunch(ctx, "launch-2", now)
	if err != nil || len(retired) != 1 {
		t.Fatalf("new launch retired=%d err=%v", len(retired), err)
	}
	current, err := store.ListSideConversations(ctx, side.SessionID, "launch-2")
	if err != nil || len(current) != 0 {
		t.Fatalf("new launch sides=%d err=%v", len(current), err)
	}
}

func TestMemorySideStoreEmptyTurnsAreAnArray(t *testing.T) {
	store := newMemorySideStore()
	turns, more, err := store.SideTurns(context.Background(), "missing", time.Time{}, 50)
	if err != nil || more || turns == nil || len(turns) != 0 {
		t.Fatalf("empty turns = %#v, more=%v, err=%v", turns, more, err)
	}
}

func TestClosedSideRejectsLateProviderEvents(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	store := newMemorySideStore()
	_, _ = store.ClaimSideLaunch(ctx, "launch-1", now)
	_, _, _ = store.CreateSideConversation(ctx, domain.SideConversation{ID: "side-1", MainConversationID: "main-1", AppRunID: "launch-1", Generation: "generation-1"})
	_, _ = store.CloseSideConversation(ctx, "side-1", now)
	if err := store.UpsertSideMessage(ctx, domain.SideMessage{ID: "late", SideID: "side-1"}, "generation-1"); !errors.Is(err, ErrSideClosed) {
		t.Fatalf("late message err=%v", err)
	}
	if err := store.UpsertSideActivity("side-1", "generation-1", domain.SideActivity{ID: "late"}); !errors.Is(err, ErrSideClosed) {
		t.Fatalf("late activity err=%v", err)
	}
}

func TestSideReservationUsesOnlySubmittedReferences(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	store := newMemorySideStore()
	_, _ = store.ClaimSideLaunch(ctx, "run", now)
	_, _, _ = store.CreateSideConversation(ctx, domain.SideConversation{ID: "side", AppRunID: "run", State: "ready", Generation: "g", SelectedText: "removed sun", ReferenceContext: "removed context", ReferencePending: true})
	turn, _, err := store.ReserveSideTurn(ctx, domain.SideTurn{ID: "t", SideID: "side", ClientMessageID: "c", Text: "question"}, "run")
	if err != nil {
		t.Fatal(err)
	}
	if turn.SelectionText != "" || turn.ReferenceContext != "" || len(turn.References) != 0 {
		t.Fatalf("removed reference sent: %+v", turn)
	}
	_ = store.SetSideFailed(ctx, "side", "g", "offline", now)
	duplicate, created, err := store.ReserveSideTurn(ctx, turn, "run")
	if err != nil || created || duplicate.ID != turn.ID {
		t.Fatalf("accepted retry while offline: %+v %v %v", duplicate, created, err)
	}
	if _, _, err := store.ReserveSideTurn(ctx, domain.SideTurn{ID: "new", SideID: "side", ClientMessageID: "new", Text: "new"}, "run"); !errors.Is(err, ErrSideNotReady) {
		t.Fatalf("offline admission: %v", err)
	}
}

func TestSideClaimsAreIndependentAndOrdered(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	store := newMemorySideStore()
	_, _ = store.ClaimSideLaunch(ctx, "run", now)
	for _, id := range []string{"a", "b"} {
		_, _, _ = store.CreateSideConversation(ctx, domain.SideConversation{ID: id, MainConversationID: id, AppRunID: "run", State: "ready", Generation: "g"})
	}
	for i, id := range []string{"a", "a", "b"} {
		_, _, err := store.ReserveSideTurn(ctx, domain.SideTurn{ID: fmt.Sprint(i), SideID: id, ClientMessageID: fmt.Sprint(i), Text: "q", CreatedAt: now.Add(time.Duration(i) * time.Second)}, "run")
		if err != nil {
			t.Fatal(err)
		}
	}
	first, _, _, _ := store.ClaimNextSideTurn(ctx, "run", nil, now)
	second, _, _, _ := store.ClaimNextSideTurn(ctx, "run", nil, now)
	if first.ID != "0" || second.ID != "2" {
		t.Fatalf("cross-side dispatch: %s %s", first.ID, second.ID)
	}
	if _, _, found, _ := store.ClaimNextSideTurn(ctx, "run", nil, now); found {
		t.Fatal("second turn claimed on running side")
	}
	_ = store.SettleSideTurn(ctx, "a", "0", "g", "completed", "", "", now)
	if _, _, found, _ := store.ClaimNextSideTurn(ctx, "run", map[string]bool{"a": true}, now); found {
		t.Fatal("compacting side claimed")
	}
	next, _, found, _ := store.ClaimNextSideTurn(ctx, "run", nil, now)
	if !found || next.ID != "1" {
		t.Fatalf("FIFO: %+v", next)
	}
}
