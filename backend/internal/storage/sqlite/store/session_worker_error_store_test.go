package store_test

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
)

// workerErrNow anchors timestamps for the worker-error log tests.
var workerErrNow = time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)

func TestSessionWorkerErrorRecordAndLatest(t *testing.T) {
	ctx := context.Background()
	s, session, conversation := conversationFixture(t)

	if _, err := s.AppendUserMessage(ctx, conversation, session, "gen-1", domain.ConversationMessage{
		ID: "turn-1-msg", Text: "do it", Origin: domain.MessageOriginHuman,
	}, "turn-1", workerErrNow); err != nil {
		t.Fatal(err)
	}
	if err := s.BindTurnToProvider(ctx, "turn-1", "provider-turn-1", workerErrNow); err != nil {
		t.Fatal(err)
	}
	if err := s.SettleTurn(ctx, conversation, "provider-turn-1",
		domain.TurnStateFailed, "429 rate limit exceeded", workerErrNow.Add(time.Minute)); err != nil {
		t.Fatalf("settle failed turn: %v", err)
	}
	if err := s.RecordSessionWorkerError(ctx, session, domain.WorkerErrorSourceChatTurn, "turn-2", "timeout contacting provider", workerErrNow.Add(2*time.Minute)); err != nil {
		t.Fatalf("record worker error: %v", err)
	}

	errs, err := s.LatestSessionWorkerErrors(ctx, session)
	if err != nil {
		t.Fatalf("list worker errors: %v", err)
	}
	if len(errs) != 2 {
		t.Fatalf("errors = %d, want 2", len(errs))
	}
	// Newest first.
	if errs[0].Message != "timeout contacting provider" || errs[1].Message != "429 rate limit exceeded" {
		t.Fatalf("order = %q then %q, want newest first", errs[0].Message, errs[1].Message)
	}
	if errs[0].SessionID != session || errs[0].Source != domain.WorkerErrorSourceChatTurn {
		t.Fatalf("event = %+v, want session+source attributed", errs[0])
	}
	if !errs[0].OccurredAt.Equal(workerErrNow.Add(2 * time.Minute)) {
		t.Fatalf("occurredAt = %v, want recorded instant", errs[0].OccurredAt)
	}
}

func TestSessionWorkerErrorEmptyMessageRecordsNothing(t *testing.T) {
	ctx := context.Background()
	s, session, _ := conversationFixture(t)

	if err := s.RecordSessionWorkerError(ctx, session, domain.WorkerErrorSourceChatTurn, "turn-x", "", workerErrNow); err != nil {
		t.Fatalf("record empty: %v", err)
	}
	errs, err := s.LatestSessionWorkerErrors(ctx, session)
	if err != nil {
		t.Fatal(err)
	}
	if len(errs) != 0 {
		t.Fatalf("empty message recorded %d errors, want 0", len(errs))
	}
}

func TestSessionWorkerErrorPrunesToNewest(t *testing.T) {
	ctx := context.Background()
	s, session, _ := conversationFixture(t)

	// Record well over the per-session bound; the log must stay bounded.
	for i := 0; i < 30; i++ {
		msg := "429 boom-" + strings.Repeat("x", i) + "-idx-" + strconv.Itoa(i)
		if err := s.RecordSessionWorkerError(ctx, session, domain.WorkerErrorSourceChatTurn, "turn-x", msg, workerErrNow.Add(time.Duration(i)*time.Minute)); err != nil {
			t.Fatalf("record %d: %v", i, err)
		}
	}
	errs, err := s.LatestSessionWorkerErrors(ctx, session)
	if err != nil {
		t.Fatal(err)
	}
	// The writer prunes each session to its newest rows.
	const bound = 25
	if len(errs) != bound {
		t.Fatalf("errors = %d, want bounded at %d", len(errs), bound)
	}
	// The newest (highest index) rows survived.
	if !strings.HasSuffix(errs[0].Message, "-idx-29") {
		t.Fatalf("first = %q, want newest", errs[0].Message)
	}
}

func TestSessionWorkerErrorRolledBackTurnExcluded(t *testing.T) {
	ctx := context.Background()
	s, session, conversation := conversationFixture(t)

	if _, err := s.AppendUserMessage(ctx, conversation, session, "gen-1", domain.ConversationMessage{
		ID: "rollback-turn-msg", Text: "do it then undo", Origin: domain.MessageOriginHuman,
	}, "rollback-turn", workerErrNow); err != nil {
		t.Fatal(err)
	}
	if err := s.BindTurnToProvider(ctx, "rollback-turn", "provider-rollback", workerErrNow); err != nil {
		t.Fatal(err)
	}
	if err := s.SettleTurn(ctx, conversation, "provider-rollback",
		domain.TurnStateFailed, "429 rate limit exceeded", workerErrNow.Add(time.Minute)); err != nil {
		t.Fatalf("settle failed turn: %v", err)
	}
	if _, err := s.RollbackTurns(ctx, conversation, "rollback-turn", workerErrNow.Add(2*time.Minute)); err != nil {
		t.Fatalf("rollback: %v", err)
	}

	errs, err := s.LatestSessionWorkerErrors(ctx, session)
	if err != nil {
		t.Fatal(err)
	}
	if len(errs) != 0 {
		t.Fatalf("rolled-back turn's error still listed: %d errors", len(errs))
	}
}

func TestSessionWorkerErrorBatchRead(t *testing.T) {
	ctx := context.Background()
	s, sessionA, _ := conversationFixture(t)
	sessionB := createChatSession(t, s)

	if err := s.RecordSessionWorkerError(ctx, sessionA, domain.WorkerErrorSourceChatTurn, "t1", "quota exceeded", workerErrNow); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordSessionWorkerError(ctx, sessionB, domain.WorkerErrorSourceChatTurn, "t2", "auth failed", workerErrNow.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}

	bySession, err := s.LatestSessionWorkerErrorsForSessions(ctx, []domain.SessionID{sessionA, sessionB, "never-seen"})
	if err != nil {
		t.Fatalf("batch read: %v", err)
	}
	// Every requested id is present; unknown sessions carry an empty list.
	if _, ok := bySession["never-seen"]; !ok {
		t.Fatal("unknown session missing from batch map")
	}
	if len(bySession[sessionA]) != 1 || len(bySession[sessionB]) != 1 || len(bySession["never-seen"]) != 0 {
		t.Fatalf("batch sizes = %d/%d/%d, want 1/1/0",
			len(bySession[sessionA]), len(bySession[sessionB]), len(bySession["never-seen"]))
	}
	if bySession[sessionA][0].Message != "quota exceeded" || bySession[sessionB][0].Message != "auth failed" {
		t.Fatalf("batch contents wrong: %+v / %+v", bySession[sessionA], bySession[sessionB])
	}
}

func TestSessionWorkerErrorBatchEmptyNoErr(t *testing.T) {
	ctx := context.Background()
	s, _, _ := conversationFixture(t)

	bySession, err := s.LatestSessionWorkerErrorsForSessions(ctx, nil)
	if err != nil {
		t.Fatalf("empty batch read: %v", err)
	}
	if len(bySession) != 0 {
		t.Fatalf("empty batch returned %d entries", len(bySession))
	}
}

func TestSettleTurnByIDRecordsWatcherErrorForFailedTurn(t *testing.T) {
	ctx := context.Background()
	s, session, conversation := conversationFixture(t)

	if _, err := s.AppendUserMessage(ctx, conversation, session, "gen-1", domain.ConversationMessage{
		ID: "never-dispatched-msg", Text: "hi", Origin: domain.MessageOriginHuman,
	}, "never-dispatched", workerErrNow); err != nil {
		t.Fatal(err)
	}
	if err := s.SettleTurnByID(ctx, "never-dispatched", domain.TurnStateFailed, "provider rejected auth: 401", workerErrNow.Add(time.Minute)); err != nil {
		t.Fatalf("settle by id: %v", err)
	}

	errs, err := s.LatestSessionWorkerErrors(ctx, session)
	if err != nil {
		t.Fatal(err)
	}
	if len(errs) != 1 || errs[0].Message != "provider rejected auth: 401" {
		t.Fatalf("settled-by-id error not recorded: %+v", errs)
	}
}

func TestSettleTurnByIDUnknownTurnSettlesWithoutRecord(t *testing.T) {
	ctx := context.Background()
	s, session, _ := conversationFixture(t)

	// A turn the store has never seen keeps the blind-settle behavior:
	// settle succeeds, but nothing is attributed to a session.
	if err := s.SettleTurnByID(ctx, "ghost-turn", domain.TurnStateFailed, "429 too many requests", workerErrNow); err != nil {
		t.Fatalf("blind settle failed: %v", err)
	}
	errs, err := s.LatestSessionWorkerErrors(ctx, session)
	if err != nil {
		t.Fatal(err)
	}
	if len(errs) != 0 {
		t.Fatalf("ghost turn attributed %d errors, want 0", len(errs))
	}
}

// createChatSession seeds a second chat session for batch tests.
func createChatSession(t *testing.T, s *sqlite.Store) domain.SessionID {
	t.Helper()
	ctx := context.Background()
	rec := sampleRecord("hist")
	rec.Mode = domain.SessionModeChat
	sess, err := s.CreateSession(ctx, rec)
	if err != nil {
		t.Fatalf("create chat session: %v", err)
	}
	return sess.ID
}
