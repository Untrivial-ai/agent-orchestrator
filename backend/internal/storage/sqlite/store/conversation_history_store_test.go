package store_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/store"
)

// The durable half of rollback and of the provider thread title.
//
// These go through the real store because the invariants they protect are the
// database's: a compare-and-set that a manual rename must be able to win, and a
// snapshot that must stop returning prose the agent has forgotten. Neither can be
// demonstrated against a fake.

var histClock = time.Date(2026, 8, 2, 10, 0, 0, 0, time.UTC)

// conversationFixture seeds a project, a chat session and a conversation.
func conversationFixture(t *testing.T) (*sqlite.Store, domain.SessionID, string) {
	t.Helper()
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "hist")

	rec := sampleRecord("hist")
	rec.Mode = domain.SessionModeChat
	session, err := s.CreateSession(ctx, rec)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	conversation, err := s.CreateConversation(ctx, "conv-1", domain.ConversationScopeSession, "hist", session.ID, histClock)
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	if err := s.ClaimChatControllerGeneration(ctx, session.ID, "gen-1", histClock); err != nil {
		t.Fatalf("claim controller generation: %v", err)
	}
	return s, session.ID, conversation.ID
}

func TestProjectConversationRebindsAcrossOrchestratorReplacement(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "project-chat")

	firstRecord := sampleRecord("project-chat")
	firstRecord.Kind = domain.KindOrchestrator
	firstRecord.Mode = domain.SessionModeChat
	first, err := s.CreateSession(ctx, firstRecord)
	if err != nil {
		t.Fatalf("create first orchestrator: %v", err)
	}
	conversation, err := s.CreateConversation(ctx, "project-conversation", domain.ConversationScopeProject,
		"project-chat", first.ID, histClock)
	if err != nil {
		t.Fatalf("create project conversation: %v", err)
	}

	secondRecord := sampleRecord("project-chat")
	secondRecord.Kind = domain.KindOrchestrator
	secondRecord.Mode = domain.SessionModeChat
	second, err := s.CreateSession(ctx, secondRecord)
	if err != nil {
		t.Fatalf("create replacement orchestrator: %v", err)
	}
	rebound, err := s.CreateConversation(ctx, "must-not-be-used", domain.ConversationScopeProject,
		"project-chat", second.ID, histClock.Add(time.Minute))
	if err != nil {
		t.Fatalf("rebind project conversation: %v", err)
	}
	if rebound.ID != conversation.ID || rebound.SessionID != second.ID {
		t.Fatalf("rebound = %+v, want conversation %s on session %s", rebound, conversation.ID, second.ID)
	}
	lookup, err := s.ConversationForSession(ctx, second.ID)
	if err != nil || lookup.ID != conversation.ID {
		t.Fatalf("replacement lookup = %+v, %v; want %s", lookup, err, conversation.ID)
	}
	if _, err := s.ConversationForSession(ctx, first.ID); !errors.Is(err, store.ErrConversationNotFound) {
		t.Fatalf("retired orchestrator still owns project conversation: %v", err)
	}
}

func TestProviderArchiveAndProjectionCommitAtomically(t *testing.T) {
	s, session, conversation := conversationFixture(t)
	ctx := context.Background()
	projectionErr := errors.New("projection failed")

	_, err := s.ProjectProviderEvent(ctx, conversation, session, "gen-1", "event-1", "activity.started", `{}`,
		histClock, func(txCtx context.Context) error {
			if err := s.UpsertActivity(txCtx, conversation, "", domain.ConversationActivity{
				ID: "activity-1", Kind: domain.ActivityKindCommand,
				Status: domain.ActivityStatusRunning, Summary: "go test",
			}, histClock); err != nil {
				return err
			}
			return projectionErr
		})
	if !errors.Is(err, projectionErr) {
		t.Fatalf("ProjectProviderEvent error = %v, want projection failure", err)
	}
	events, err := s.ProviderEventsSince(ctx, conversation, 0, 10)
	if err != nil || len(events) != 0 {
		t.Fatalf("rolled-back archive = %+v, %v; want none", events, err)
	}
	snapshot, err := s.LoadConversationSnapshot(ctx, conversation)
	if err != nil || len(snapshot.Activities) != 0 {
		t.Fatalf("rolled-back projection activities = %+v, %v; want none", snapshot.Activities, err)
	}
}

func TestStaleControllerGenerationDropsArchiveAndProjection(t *testing.T) {
	s, session, conversation := conversationFixture(t)
	ctx := context.Background()
	projected := 0

	applied, err := s.ProjectProviderEvent(ctx, conversation, session, "stale-generation", "event-1",
		"message.delta", `{}`, histClock, func(context.Context) error {
			projected++
			return nil
		})
	if err != nil {
		t.Fatalf("ProjectProviderEvent: %v", err)
	}
	if applied || projected != 0 {
		t.Fatalf("stale event applied=%v projection count=%d, want false/0", applied, projected)
	}
	events, err := s.ProviderEventsSince(ctx, conversation, 0, 10)
	if err != nil || len(events) != 0 {
		t.Fatalf("stale archive = %+v, %v; want none", events, err)
	}
}

func TestProviderEventIdentityDeduplicatesTheWholeProjection(t *testing.T) {
	s, session, conversation := conversationFixture(t)
	ctx := context.Background()
	projected := 0
	project := func(context.Context) error {
		projected++
		return nil
	}
	for range 2 {
		if _, err := s.ProjectProviderEvent(ctx, conversation, session, "gen-1", "provider-event-1", "turn.started", `{}`,
			histClock, project); err != nil {
			t.Fatalf("ProjectProviderEvent: %v", err)
		}
	}
	if projected != 1 {
		t.Fatalf("projection count = %d, want 1", projected)
	}
	events, err := s.ProviderEventsSince(ctx, conversation, 0, 10)
	if err != nil || len(events) != 1 {
		t.Fatalf("events = %+v, %v; want one", events, err)
	}
}

func TestCommandOutputStopsChangingAfterTruncation(t *testing.T) {
	s, _, conversation := conversationFixture(t)
	ctx := context.Background()
	if err := s.UpsertActivity(ctx, conversation, "provider-turn-1", domain.ConversationActivity{
		ID:             "activity-output-cap",
		Kind:           domain.ActivityKindCommand,
		Status:         domain.ActivityStatusRunning,
		Summary:        "npm run dev",
		ProviderItemID: "exec-output-cap",
	}, histClock); err != nil {
		t.Fatalf("seed command activity: %v", err)
	}

	truncatedAt := histClock.Add(time.Second)
	found, err := s.AppendCommandOutput(ctx, conversation, "exec-output-cap",
		strings.Repeat("x", store.MaxCommandOutputChars+1), truncatedAt)
	if err != nil || !found {
		t.Fatalf("append output through cap: found=%v err=%v", found, err)
	}

	snapshot, err := s.LoadConversationSnapshot(ctx, conversation)
	if err != nil {
		t.Fatalf("load capped snapshot: %v", err)
	}
	if len(snapshot.Activities) != 1 {
		t.Fatalf("activities = %d, want 1", len(snapshot.Activities))
	}
	capped := snapshot.Activities[0]
	if len(capped.CommandOutput) != store.MaxCommandOutputChars || !capped.CommandOutputTruncated {
		t.Fatalf("capped output = %d chars truncated=%v, want %d/true",
			len(capped.CommandOutput), capped.CommandOutputTruncated, store.MaxCommandOutputChars)
	}
	cdcAfterCap, err := s.LatestSeq(ctx)
	if err != nil {
		t.Fatalf("read CDC head after cap: %v", err)
	}

	found, err = s.AppendCommandOutput(ctx, conversation, "exec-output-cap", "still running\n", truncatedAt.Add(time.Second))
	if err != nil || !found {
		t.Fatalf("append output after cap: found=%v err=%v", found, err)
	}
	snapshot, err = s.LoadConversationSnapshot(ctx, conversation)
	if err != nil {
		t.Fatalf("load snapshot after capped delta: %v", err)
	}
	after := snapshot.Activities[0]
	if after.CommandOutput != capped.CommandOutput || !after.CommandOutputTruncated {
		t.Fatalf("capped output changed after another delta: len=%d truncated=%v",
			len(after.CommandOutput), after.CommandOutputTruncated)
	}
	if after.Revision != capped.Revision {
		t.Errorf("revision after capped delta = %d, want unchanged %d", after.Revision, capped.Revision)
	}
	if !after.UpdatedAt.Equal(capped.UpdatedAt) {
		t.Errorf("updated_at after capped delta = %s, want unchanged %s", after.UpdatedAt, capped.UpdatedAt)
	}
	cdcAfterNoop, err := s.LatestSeq(ctx)
	if err != nil {
		t.Fatalf("read CDC head after capped delta: %v", err)
	}
	if cdcAfterNoop != cdcAfterCap {
		t.Errorf("CDC head after capped delta = %d, want unchanged %d", cdcAfterNoop, cdcAfterCap)
	}
}

func TestActivityStreamedTextStopsChangingAfterTruncation(t *testing.T) {
	s, _, conversation := conversationFixture(t)
	ctx := context.Background()
	if err := s.UpsertActivity(ctx, conversation, "provider-turn-1", domain.ConversationActivity{
		ID:             "activity-text-cap",
		Kind:           domain.ActivityKindReasoning,
		Status:         domain.ActivityStatusRunning,
		Summary:        "Reasoning",
		ProviderItemID: "reasoning-text-cap",
	}, histClock); err != nil {
		t.Fatalf("seed reasoning activity: %v", err)
	}

	truncatedAt := histClock.Add(time.Second)
	found, err := s.AppendActivityStreamedText(ctx, conversation, "reasoning-text-cap",
		strings.Repeat("x", store.MaxStreamedTextChars+1), truncatedAt)
	if err != nil || !found {
		t.Fatalf("append streamed text through cap: found=%v err=%v", found, err)
	}
	snapshot, err := s.LoadConversationSnapshot(ctx, conversation)
	if err != nil {
		t.Fatalf("load capped snapshot: %v", err)
	}
	if len(snapshot.Activities) != 1 {
		t.Fatalf("activities = %d, want 1", len(snapshot.Activities))
	}
	capped := snapshot.Activities[0]
	if len(capped.StreamedText) != store.MaxStreamedTextChars || !capped.StreamedTextTruncated {
		t.Fatalf("capped streamed text = %d chars truncated=%v, want %d/true",
			len(capped.StreamedText), capped.StreamedTextTruncated, store.MaxStreamedTextChars)
	}
	cdcAfterCap, err := s.LatestSeq(ctx)
	if err != nil {
		t.Fatalf("read CDC head after cap: %v", err)
	}

	found, err = s.AppendActivityStreamedText(ctx, conversation, "reasoning-text-cap",
		"still reasoning", truncatedAt.Add(time.Second))
	if err != nil || !found {
		t.Fatalf("append streamed text after cap: found=%v err=%v", found, err)
	}
	snapshot, err = s.LoadConversationSnapshot(ctx, conversation)
	if err != nil {
		t.Fatalf("load snapshot after capped delta: %v", err)
	}
	after := snapshot.Activities[0]
	if after.StreamedText != capped.StreamedText || !after.StreamedTextTruncated {
		t.Fatalf("capped streamed text changed after another delta: len=%d truncated=%v",
			len(after.StreamedText), after.StreamedTextTruncated)
	}
	if after.Revision != capped.Revision {
		t.Errorf("revision after capped delta = %d, want unchanged %d", after.Revision, capped.Revision)
	}
	if !after.UpdatedAt.Equal(capped.UpdatedAt) {
		t.Errorf("updated_at after capped delta = %s, want unchanged %s", after.UpdatedAt, capped.UpdatedAt)
	}
	cdcAfterNoop, err := s.LatestSeq(ctx)
	if err != nil {
		t.Fatalf("read CDC head after capped delta: %v", err)
	}
	if cdcAfterNoop != cdcAfterCap {
		t.Errorf("CDC head after capped delta = %d, want unchanged %d", cdcAfterNoop, cdcAfterCap)
	}
}

func TestConversationSnapshotPagesCombinedTimelineBySequence(t *testing.T) {
	s, session, conversation := conversationFixture(t)
	ctx := context.Background()
	for i, text := range []string{"one", "two", "three", "four", "five"} {
		turnID := fmt.Sprintf("turn-page-%d", i+1)
		created, err := s.AppendUserMessage(ctx, conversation, session, "gen-1", domain.ConversationMessage{
			ID: turnID + "-message", Text: text, Origin: domain.MessageOriginHuman,
		}, turnID, histClock.Add(time.Duration(i)*time.Second))
		if err != nil || !created {
			t.Fatalf("append %s: created=%v err=%v", text, created, err)
		}
	}

	newest, err := s.LoadConversationSnapshotPage(ctx, conversation, 0, 2)
	if err != nil {
		t.Fatalf("newest page: %v", err)
	}
	if got := []string{newest.Messages[0].Text, newest.Messages[1].Text}; !reflect.DeepEqual(got, []string{"four", "five"}) {
		t.Fatalf("newest messages = %v", got)
	}
	if !newest.HasMoreBefore || newest.OldestSequence != 4 {
		t.Fatalf("newest cursor = (%d, %v), want (4, true)", newest.OldestSequence, newest.HasMoreBefore)
	}

	older, err := s.LoadConversationSnapshotPage(ctx, conversation, newest.OldestSequence, 2)
	if err != nil {
		t.Fatalf("older page: %v", err)
	}
	if got := []string{older.Messages[0].Text, older.Messages[1].Text}; !reflect.DeepEqual(got, []string{"two", "three"}) {
		t.Fatalf("older messages = %v", got)
	}
}

// A promotion reservation removes only the selected row from automatic drain.
// Without the conditional reservation, two clients can steer the same queued
// message or the drain loop can start it as a fresh turn while steering is in
// flight.
func TestQueuedTurnPromotionReservationPreservesTheOtherQueueOrder(t *testing.T) {
	s, session, conversation := conversationFixture(t)
	ctx := context.Background()
	for i, text := range []string{"first queued", "second queued", "third queued"} {
		turnID := fmt.Sprintf("queued-%d", i+1)
		created, err := s.AppendUserMessage(ctx, conversation, session, "gen-1",
			domain.ConversationMessage{
				ID: turnID + "-message", Text: text, Origin: domain.MessageOriginHuman,
			}, turnID, histClock.Add(time.Duration(i)*time.Second))
		if err != nil || !created {
			t.Fatalf("append %s: created=%v err=%v", turnID, created, err)
		}
	}

	selected, err := s.ReserveQueuedTurnForPromotion(
		ctx, conversation, "queued-2", histClock.Add(time.Minute))
	if err != nil {
		t.Fatalf("reserve second queued turn: %v", err)
	}
	if selected.TurnID != "queued-2" || selected.Text != "second queued" {
		t.Fatalf("reserved = %+v, want queued-2 with its durable text", selected)
	}
	if _, err := s.ReserveQueuedTurnForPromotion(
		ctx, conversation, "queued-2", histClock.Add(2*time.Minute)); !errors.Is(err, store.ErrQueuedTurnNotAvailable) {
		t.Fatalf("second reservation error = %v, want ErrQueuedTurnNotAvailable", err)
	}

	next, err := s.NextQueuedTurn(ctx, conversation)
	if err != nil {
		t.Fatalf("next queued turn: %v", err)
	}
	if next.TurnID != "queued-1" {
		t.Fatalf("queue head = %q, want queued-1", next.TurnID)
	}
	if err := s.SettleTurnByID(ctx, "queued-1", domain.TurnStateCompleted, "", histClock); err != nil {
		t.Fatalf("remove queue head: %v", err)
	}
	next, err = s.NextQueuedTurn(ctx, conversation)
	if err != nil {
		t.Fatalf("next queued turn behind reservation: %v", err)
	}
	if next.TurnID != "queued-3" {
		t.Fatalf("queue behind reservation = %q, want queued-3", next.TurnID)
	}

	if err := s.ReleaseQueuedTurnPromotion(ctx, conversation, "queued-2"); err != nil {
		t.Fatalf("release reservation: %v", err)
	}
	next, err = s.NextQueuedTurn(ctx, conversation)
	if err != nil {
		t.Fatalf("next queued turn after release: %v", err)
	}
	if next.TurnID != "queued-2" {
		t.Fatalf("released queue head = %q, want queued-2 in its original order", next.TurnID)
	}
}

// seedTurn records one dispatched turn with a user message and an activity, which is
// the shape a real turn leaves behind.
func seedTurn(t *testing.T, s *sqlite.Store, conversationID string, session domain.SessionID, turnID, text string, at time.Time) {
	t.Helper()
	ctx := context.Background()
	created, err := s.AppendUserMessage(ctx, conversationID, session, "gen-1", domain.ConversationMessage{
		ID:     turnID + "-msg",
		Text:   text,
		Origin: domain.MessageOriginHuman,
	}, turnID, at)
	if err != nil || !created {
		t.Fatalf("append user message for %s: created=%v err=%v", turnID, created, err)
	}
	if err := s.BindTurnToProvider(ctx, turnID, "provider-"+turnID, at); err != nil {
		t.Fatalf("bind %s: %v", turnID, err)
	}
	if err := s.UpsertActivity(ctx, conversationID, "provider-"+turnID, domain.ConversationActivity{
		ID:             turnID + "-act",
		Kind:           domain.ActivityKindCommand,
		Status:         domain.ActivityStatusCompleted,
		Summary:        "git status",
		ProviderItemID: turnID + "-exec",
	}, at); err != nil {
		t.Fatalf("upsert activity for %s: %v", turnID, err)
	}
	if err := s.SettleTurn(ctx, conversationID, "provider-"+turnID,
		domain.TurnStateCompleted, "", at); err != nil {
		t.Fatalf("settle %s: %v", turnID, err)
	}
}

// The core of the model: rollback hides what the agent forgot without destroying it.
// A turn's rows stay, marked; its prose leaves the timeline.
func TestRollbackHidesDiscardedProseAndKeepsTheTurnRows(t *testing.T) {
	s, session, conversation := conversationFixture(t)
	ctx := context.Background()

	seedTurn(t, s, conversation, session, "turn-1", "first", histClock)
	seedTurn(t, s, conversation, session, "turn-2", "second", histClock.Add(time.Minute))
	seedTurn(t, s, conversation, session, "turn-3", "third", histClock.Add(2*time.Minute))

	discarded, err := s.RollbackTurns(ctx, conversation, "turn-2", histClock.Add(3*time.Minute))
	if err != nil {
		t.Fatalf("RollbackTurns: %v", err)
	}
	if discarded != 2 {
		t.Fatalf("discarded = %d, want 2 (the named turn and the one after it)", discarded)
	}

	snapshot, err := s.LoadConversationSnapshot(ctx, conversation)
	if err != nil {
		t.Fatalf("load snapshot: %v", err)
	}

	// Every turn row survives. Deleting them would make "this was taken back"
	// indistinguishable from "this never happened".
	if len(snapshot.Turns) != 3 {
		t.Fatalf("turns = %d, want all 3 still readable", len(snapshot.Turns))
	}
	wantRolledBack := map[string]bool{"turn-1": false, "turn-2": true, "turn-3": true}
	for _, turn := range snapshot.Turns {
		if got := turn.RolledBackAt != nil; got != wantRolledBack[turn.ID] {
			t.Errorf("%s rolled back = %v, want %v", turn.ID, got, wantRolledBack[turn.ID])
		}
	}

	// The prose the agent no longer remembers is gone from the timeline.
	if len(snapshot.Messages) != 1 || snapshot.Messages[0].Text != "first" {
		t.Fatalf("messages = %+v, want only the surviving turn's message", snapshot.Messages)
	}
	if len(snapshot.Activities) != 1 || snapshot.Activities[0].TurnID != "turn-1" {
		t.Fatalf("activities = %+v, want only the surviving turn's activity", snapshot.Activities)
	}

	// Sequence is immutable, so the surviving row keeps the position it was given.
	if snapshot.Messages[0].Sequence != 1 {
		t.Errorf("surviving message sequence = %d, want its original 1",
			snapshot.Messages[0].Sequence)
	}
}

// Rolling back to the first turn empties the conversation as far as the agent is
// concerned, which has to be expressible: it is what "start over" means.
func TestRollbackToTheFirstTurnDiscardsEverything(t *testing.T) {
	s, session, conversation := conversationFixture(t)
	ctx := context.Background()

	seedTurn(t, s, conversation, session, "turn-1", "first", histClock)
	seedTurn(t, s, conversation, session, "turn-2", "second", histClock.Add(time.Minute))

	discarded, err := s.RollbackTurns(ctx, conversation, "turn-1", histClock.Add(2*time.Minute))
	if err != nil {
		t.Fatalf("RollbackTurns: %v", err)
	}
	if discarded != 2 {
		t.Fatalf("discarded = %d, want 2", discarded)
	}

	snapshot, err := s.LoadConversationSnapshot(ctx, conversation)
	if err != nil {
		t.Fatalf("load snapshot: %v", err)
	}
	if len(snapshot.Messages) != 0 || len(snapshot.Activities) != 0 {
		t.Fatalf("timeline = %d messages / %d activities, want empty",
			len(snapshot.Messages), len(snapshot.Activities))
	}
}

// A message still waiting to be sent must not be dispatched against a history it was
// never written for, so a rollback that sweeps past it settles it.
func TestRollbackInterruptsAQueuedTurnItDiscarded(t *testing.T) {
	s, session, conversation := conversationFixture(t)
	ctx := context.Background()

	seedTurn(t, s, conversation, session, "turn-1", "first", histClock)
	// Recorded but never dispatched, exactly as a mid-turn send is.
	if created, err := s.AppendUserMessage(ctx, conversation, session, "gen-1",
		domain.ConversationMessage{ID: "queued-msg", Text: "and also", Origin: domain.MessageOriginHuman},
		"turn-queued", histClock.Add(time.Minute)); err != nil || !created {
		t.Fatalf("append queued message: created=%v err=%v", created, err)
	}

	if _, err := s.RollbackTurns(ctx, conversation, "turn-1", histClock.Add(2*time.Minute)); err != nil {
		t.Fatalf("RollbackTurns: %v", err)
	}

	snapshot, err := s.LoadConversationSnapshot(ctx, conversation)
	if err != nil {
		t.Fatalf("load snapshot: %v", err)
	}
	for _, turn := range snapshot.Turns {
		if turn.ID != "turn-queued" {
			continue
		}
		if turn.State != domain.TurnStateInterrupted {
			t.Errorf("queued turn state = %q, want interrupted: nothing failed, the user undid it",
				turn.State)
		}
		if turn.RolledBackAt == nil {
			t.Error("queued turn was not marked rolled back")
		}
		return
	}
	t.Fatal("queued turn row disappeared")
}

// An approval inside a discarded turn can never be answered, so it must not be left
// looking actionable.
func TestRollbackFailsAPendingApprovalItDiscarded(t *testing.T) {
	s, session, conversation := conversationFixture(t)
	ctx := context.Background()

	seedTurn(t, s, conversation, session, "turn-1", "first", histClock)
	if err := s.UpsertActivity(ctx, conversation, "provider-turn-1", domain.ConversationActivity{
		ID:             "approval-1",
		Kind:           domain.ActivityKindApproval,
		Status:         domain.ActivityStatusPending,
		Summary:        "Run rm -rf",
		RequestID:      "req-1",
		ProviderItemID: "approval-item-1",
	}, histClock); err != nil {
		t.Fatalf("seed approval: %v", err)
	}

	if _, err := s.RollbackTurns(ctx, conversation, "turn-1", histClock.Add(time.Minute)); err != nil {
		t.Fatalf("RollbackTurns: %v", err)
	}

	// The row is hidden from the snapshot, so read it back through the archive-free
	// path the pending-approval query uses instead.
	pending, err := s.LoadConversationSnapshot(ctx, conversation)
	if err != nil {
		t.Fatalf("load snapshot: %v", err)
	}
	for _, activity := range pending.Activities {
		if activity.RequestID == "req-1" {
			t.Fatalf("discarded approval is still in the timeline: %+v", activity)
		}
	}
}

// A turn from a different conversation would anchor the rowid cut somewhere
// arbitrary in this one and discard a range nobody named.
func TestRollbackRefusesATurnFromAnotherConversation(t *testing.T) {
	s, session, conversation := conversationFixture(t)
	ctx := context.Background()
	seedTurn(t, s, conversation, session, "turn-1", "first", histClock)

	otherRec := sampleRecord("hist")
	otherRec.Mode = domain.SessionModeChat
	otherSession, err := s.CreateSession(ctx, otherRec)
	if err != nil {
		t.Fatalf("create second session: %v", err)
	}
	otherConversation, err := s.CreateConversation(ctx, "conv-2", domain.ConversationScopeSession, "hist", otherSession.ID, histClock)
	if err != nil {
		t.Fatalf("create second conversation: %v", err)
	}
	seedTurn(t, s, otherConversation.ID, otherSession.ID, "turn-elsewhere", "not mine", histClock)

	_, err = s.RollbackTurns(ctx, conversation, "turn-elsewhere", histClock.Add(time.Minute))
	if !errors.Is(err, store.ErrConversationTurnNotFound) {
		t.Fatalf("err = %v, want ErrConversationTurnNotFound", err)
	}

	// And nothing in the target conversation moved.
	snapshot, err := s.LoadConversationSnapshot(ctx, conversation)
	if err != nil {
		t.Fatalf("load snapshot: %v", err)
	}
	if len(snapshot.Messages) != 1 {
		t.Errorf("messages = %d, want the conversation untouched", len(snapshot.Messages))
	}
}

func TestRollbackRefusesAnUnknownTurn(t *testing.T) {
	s, _, conversation := conversationFixture(t)
	_, err := s.RollbackTurns(context.Background(), conversation, "turn-nope", histClock)
	if !errors.Is(err, store.ErrConversationTurnNotFound) {
		t.Fatalf("err = %v, want ErrConversationTurnNotFound", err)
	}
}

// A session nobody has named adopts the provider's title. This is the whole point of
// routing it through display_name: every surface that shows a session name gets it.
func TestApplyProviderTitleNamesAnUnnamedSession(t *testing.T) {
	s, session, conversation := conversationFixture(t)
	ctx := context.Background()

	applied, err := s.ApplyProviderTitle(ctx, conversation, session, "Fix OAuth Return URL Loss", histClock)
	if err != nil {
		t.Fatalf("ApplyProviderTitle: %v", err)
	}
	if !applied {
		t.Fatal("title was not applied to a session with no name")
	}

	rec, ok, err := s.GetSession(ctx, session)
	if err != nil || !ok {
		t.Fatalf("get session: ok=%v err=%v", ok, err)
	}
	if rec.DisplayName != "Fix OAuth Return URL Loss" {
		t.Errorf("display name = %q, want the provider title", rec.DisplayName)
	}
}

// The rule that matters most: a name a person chose is never overwritten, however
// late the provider's title turns up.
func TestApplyProviderTitleNeverOverwritesAManualName(t *testing.T) {
	s, session, conversation := conversationFixture(t)
	ctx := context.Background()

	if renamed, err := s.RenameSession(ctx, session, "My own name", histClock); err != nil || !renamed {
		t.Fatalf("rename: renamed=%v err=%v", renamed, err)
	}

	applied, err := s.ApplyProviderTitle(ctx, conversation, session, "Something The Model Chose", histClock)
	if err != nil {
		t.Fatalf("ApplyProviderTitle: %v", err)
	}
	if applied {
		t.Fatal("a provider title overwrote a name the user chose")
	}

	rec, ok, err := s.GetSession(ctx, session)
	if err != nil || !ok {
		t.Fatalf("get session: ok=%v err=%v", ok, err)
	}
	if rec.DisplayName != "My own name" {
		t.Errorf("display name = %q, want the user's name to have won", rec.DisplayName)
	}
}

// A later provider title may replace an earlier one AO applied itself: refining the
// name it chose is not the same as overruling the user.
func TestApplyProviderTitleReplacesTheTitleAOAppliedBefore(t *testing.T) {
	s, session, conversation := conversationFixture(t)
	ctx := context.Background()

	if _, err := s.ApplyProviderTitle(ctx, conversation, session, "First Guess", histClock); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	applied, err := s.ApplyProviderTitle(ctx, conversation, session, "Better Second Guess",
		histClock.Add(time.Minute))
	if err != nil {
		t.Fatalf("second apply: %v", err)
	}
	if !applied {
		t.Fatal("a second provider title did not replace the first one AO applied")
	}

	rec, ok, err := s.GetSession(ctx, session)
	if err != nil || !ok {
		t.Fatalf("get session: ok=%v err=%v", ok, err)
	}
	if rec.DisplayName != "Better Second Guess" {
		t.Errorf("display name = %q, want the newer provider title", rec.DisplayName)
	}
}

// A rename landing after AO applied a title still wins, because the witness no longer
// matches. This is the race the compare-and-set exists for.
func TestApplyProviderTitleLosesToARenameThatFollowedIt(t *testing.T) {
	s, session, conversation := conversationFixture(t)
	ctx := context.Background()

	if _, err := s.ApplyProviderTitle(ctx, conversation, session, "Model Title", histClock); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if renamed, err := s.RenameSession(ctx, session, "Mine now", histClock.Add(time.Minute)); err != nil || !renamed {
		t.Fatalf("rename: renamed=%v err=%v", renamed, err)
	}

	applied, err := s.ApplyProviderTitle(ctx, conversation, session, "Another Model Title",
		histClock.Add(2*time.Minute))
	if err != nil {
		t.Fatalf("second apply: %v", err)
	}
	if applied {
		t.Fatal("a provider title overwrote a rename that came after AO's own title")
	}
}

// The provider's name for the thread is kept whether or not it became the label, so
// the conversation's own identity survives a manual override.
func TestSetProviderTitleIsRecordedIndependentlyOfTheLabel(t *testing.T) {
	s, session, conversation := conversationFixture(t)
	ctx := context.Background()

	if renamed, err := s.RenameSession(ctx, session, "Mine", histClock); err != nil || !renamed {
		t.Fatalf("rename: renamed=%v err=%v", renamed, err)
	}
	if err := s.SetProviderTitle(ctx, conversation, "What Codex Calls It", histClock); err != nil {
		t.Fatalf("SetProviderTitle: %v", err)
	}

	snapshot, err := s.LoadConversationSnapshot(ctx, conversation)
	if err != nil {
		t.Fatalf("load snapshot: %v", err)
	}
	if snapshot.Conversation.ProviderTitle != "What Codex Calls It" {
		t.Errorf("provider title = %q, want it kept even though the label is the user's",
			snapshot.Conversation.ProviderTitle)
	}
}

func TestQueuedTurnRetainsNativeDeliveryContent(t *testing.T) {
	s, session, conversation := conversationFixture(t)
	ctx := context.Background()
	want := `[{"type":"image","data":"aW1hZ2U=","mimeType":"image/png"}]`
	created, err := s.AppendUserMessage(ctx, conversation, session, "gen-1", domain.ConversationMessage{
		ID:                  "native-message",
		Text:                "inspect this image",
		Origin:              domain.MessageOriginHuman,
		DeliveryContentJSON: want,
	}, "native-turn", histClock)
	if err != nil || !created {
		t.Fatalf("append native message: created=%v err=%v", created, err)
	}

	queued, err := s.NextQueuedTurn(ctx, conversation)
	if err != nil {
		t.Fatalf("NextQueuedTurn: %v", err)
	}
	if queued.DeliveryContentJSON != want {
		t.Fatalf("delivery content = %q, want %q", queued.DeliveryContentJSON, want)
	}
}

func TestConversationSnapshotRetainsProviderCost(t *testing.T) {
	s, _, conversation := conversationFixture(t)
	cost := 1.25
	if err := s.RecordUsage(context.Background(), conversation, domain.ConversationUsage{
		ContextUsed:   25,
		ContextWindow: 100,
		Cost:          &cost,
		Currency:      "USD",
	}); err != nil {
		t.Fatalf("RecordUsage: %v", err)
	}

	snapshot, err := s.LoadConversationSnapshot(context.Background(), conversation)
	if err != nil {
		t.Fatalf("LoadConversationSnapshot: %v", err)
	}
	if snapshot.Conversation.Usage == nil || snapshot.Conversation.Usage.Cost == nil ||
		*snapshot.Conversation.Usage.Cost != cost || snapshot.Conversation.Usage.Currency != "USD" {
		t.Fatalf("usage = %#v", snapshot.Conversation.Usage)
	}
}

func TestFailPendingInputsDoesNotTouchApprovals(t *testing.T) {
	s, _, conversation := conversationFixture(t)
	ctx := context.Background()
	for _, activity := range []domain.ConversationActivity{
		{ID: "input", Kind: domain.ActivityKindUserInput, Status: domain.ActivityStatusPending,
			Summary: "Choose", RequestID: "input-request", ProviderItemID: "input-item"},
		{ID: "approval", Kind: domain.ActivityKindApproval, Status: domain.ActivityStatusPending,
			Summary: "Run command", RequestID: "approval-request", ProviderItemID: "approval-item"},
	} {
		if err := s.UpsertActivity(ctx, conversation, "", activity, histClock); err != nil {
			t.Fatalf("UpsertActivity(%s): %v", activity.ID, err)
		}
	}
	if err := s.FailPendingInputs(ctx, conversation, histClock.Add(time.Minute)); err != nil {
		t.Fatalf("FailPendingInputs: %v", err)
	}
	snapshot, err := s.LoadConversationSnapshot(ctx, conversation)
	if err != nil {
		t.Fatalf("LoadConversationSnapshot: %v", err)
	}
	states := make(map[string]domain.ActivityStatus, len(snapshot.Activities))
	for _, activity := range snapshot.Activities {
		states[activity.ID] = activity.Status
	}
	if states["input"] != domain.ActivityStatusFailed || states["approval"] != domain.ActivityStatusPending {
		t.Fatalf("activity states = %#v", states)
	}
}
