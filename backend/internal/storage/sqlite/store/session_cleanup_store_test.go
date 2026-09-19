package store_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
)

func TestSessionCleanupFactsRoundTrip(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "cf")
	rec, err := s.CreateSession(ctx, sampleRecord("cf"))
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	// A session with no facts row reads back as absent.
	if _, ok, err := s.GetSessionCleanupFacts(ctx, rec.ID); err != nil || ok {
		t.Fatalf("get absent facts = ok=%v err=%v, want ok=false err=nil", ok, err)
	}

	now := time.Now().UTC().Truncate(time.Second)
	want := domain.SessionCleanupRecord{
		SessionID:            rec.ID,
		SessionGeneration:    3,
		RuntimeReleasedAt:    now,
		WorkspaceDisposition: domain.DispositionPreservedDirty,
		AttemptCount:         2,
		LastAttemptAt:        now,
		NextAttemptAt:        now.Add(time.Minute),
		FailureCode:          "workspace_dirty",
	}
	if err := s.UpsertSessionCleanupFacts(ctx, want); err != nil {
		t.Fatalf("upsert facts: %v", err)
	}
	got, ok, err := s.GetSessionCleanupFacts(ctx, rec.ID)
	if err != nil || !ok {
		t.Fatalf("get facts: ok=%v err=%v", ok, err)
	}
	requireCleanupEqual(t, got, want)

	// Upsert-updates the same row in place (disposition + bookkeeping change).
	want.WorkspaceDisposition = domain.DispositionFailed
	want.AttemptCount = 5
	want.FailureCode = "worktree_locked"
	want.RuntimeReleasedAt = time.Time{} // clear back to NULL
	if err := s.UpsertSessionCleanupFacts(ctx, want); err != nil {
		t.Fatalf("re-upsert facts: %v", err)
	}
	got, ok, err = s.GetSessionCleanupFacts(ctx, rec.ID)
	if err != nil || !ok {
		t.Fatalf("re-get facts: ok=%v err=%v", ok, err)
	}
	requireCleanupEqual(t, got, want)
}

// TestSessionCleanupFactsCDCEmitsSessionUpdated pins the facts-table CDC
// contract: an insert and any disposition / runtime-release change fan out the
// existing session_updated event with an {id}-only payload (deliberately no
// isTerminated field), while a retry-bookkeeping-only write is suppressed by the
// trigger WHEN guard so it cannot self-wake the reconciler.
func TestSessionCleanupFactsCDCEmitsSessionUpdated(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "cf")
	r, err := s.CreateSession(ctx, sampleRecord("cf"))
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	base, _ := s.LatestSeq(ctx)

	released := time.Now().UTC().Truncate(time.Second)
	// (1) insert -> session_updated. (2) disposition + runtime release change ->
	// session_updated. (3) attempt/next-attempt-only change -> suppressed.
	steps := []domain.SessionCleanupRecord{
		{SessionID: r.ID, WorkspaceDisposition: domain.DispositionPending},
		{SessionID: r.ID, WorkspaceDisposition: domain.DispositionRemoved, RuntimeReleasedAt: released},
		{SessionID: r.ID, WorkspaceDisposition: domain.DispositionRemoved, RuntimeReleasedAt: released, AttemptCount: 4, NextAttemptAt: released.Add(time.Hour)},
	}
	for i, step := range steps {
		if err := s.UpsertSessionCleanupFacts(ctx, step); err != nil {
			t.Fatalf("upsert step %d: %v", i, err)
		}
	}

	evs, err := s.EventsAfter(ctx, base, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 2 {
		var types []string
		for _, e := range evs {
			types = append(types, string(e.Type))
		}
		t.Fatalf("facts CDC events = %v (%d), want 2 session_updated (bookkeeping-only write suppressed)", types, len(evs))
	}
	for _, e := range evs {
		if string(e.Type) != "session_updated" {
			t.Fatalf("event type = %s, want session_updated", e.Type)
		}
		if e.SessionID != string(r.ID) || e.ProjectID != "cf" {
			t.Fatalf("event ids = session %s project %s, want %s/cf", e.SessionID, e.ProjectID, r.ID)
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(e.Payload), &payload); err != nil {
			t.Fatalf("payload JSON: %v", err)
		}
		if len(payload) != 1 || payload["id"] != string(r.ID) {
			t.Fatalf("facts payload = %v, want {id: %s} only", payload, r.ID)
		}
		if _, hasTerminated := payload["isTerminated"]; hasTerminated {
			t.Fatalf("facts payload carries isTerminated, want {id}-only: %v", payload)
		}
	}
}

// TestListTerminalCleanupCandidates covers the coarse sessions-driven scan: it
// surfaces terminated sessions with no facts row (the pre-existing backlog),
// with facts stale relative to cleanup_generation, and with a pending cleanup
// due for retry — while excluding current, not-yet-due, and live sessions.
func TestListTerminalCleanupCandidates(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "cand")
	now := time.Now().UTC().Truncate(time.Second)

	// A: terminal, no facts row -> candidate (the leaked backlog).
	a := terminalSession(t, s, "cand", 0)
	// B: terminal, facts current (removed, generation matches) -> excluded.
	b := terminalSession(t, s, "cand", 0)
	upsertFacts(t, s, domain.SessionCleanupRecord{SessionID: b.ID, SessionGeneration: 0, WorkspaceDisposition: domain.DispositionRemoved, RuntimeReleasedAt: now})
	// C: terminal, facts stale (session bumped to gen 2, facts written at gen 1) -> candidate.
	c := terminalSession(t, s, "cand", 2)
	upsertFacts(t, s, domain.SessionCleanupRecord{SessionID: c.ID, SessionGeneration: 1, WorkspaceDisposition: domain.DispositionRemoved})
	// D: terminal, pending and due -> candidate.
	d := terminalSession(t, s, "cand", 0)
	upsertFacts(t, s, domain.SessionCleanupRecord{SessionID: d.ID, WorkspaceDisposition: domain.DispositionPending, NextAttemptAt: now.Add(-time.Hour)})
	// E: terminal, pending but not yet due -> excluded.
	e := terminalSession(t, s, "cand", 0)
	upsertFacts(t, s, domain.SessionCleanupRecord{SessionID: e.ID, WorkspaceDisposition: domain.DispositionPending, RuntimeReleasedAt: now, NextAttemptAt: now.Add(time.Hour)})
	// F: terminal, workspace removed but runtime release unresolved -> candidate.
	f := terminalSession(t, s, "cand", 0)
	upsertFacts(t, s, domain.SessionCleanupRecord{SessionID: f.ID, WorkspaceDisposition: domain.DispositionRemoved})
	// G: live (not terminated) -> excluded.
	if _, err := s.CreateSession(ctx, sampleRecord("cand")); err != nil {
		t.Fatalf("create live session: %v", err)
	}

	ids, err := s.ListTerminalCleanupCandidates(ctx, now)
	if err != nil {
		t.Fatalf("list candidates: %v", err)
	}
	got := map[domain.SessionID]bool{}
	for _, id := range ids {
		got[id] = true
	}
	want := []domain.SessionID{a.ID, c.ID, d.ID, f.ID}
	if len(got) != len(want) {
		t.Fatalf("candidates = %v, want exactly %v", ids, want)
	}
	for _, id := range want {
		if !got[id] {
			t.Fatalf("candidates = %v, missing %s", ids, id)
		}
	}
	for _, id := range []domain.SessionID{b.ID, e.ID} {
		if got[id] {
			t.Fatalf("candidates = %v, should exclude %s", ids, id)
		}
	}
}

func terminalSession(t *testing.T, s *sqlite.Store, project string, generation int64) domain.SessionRecord {
	t.Helper()
	ctx := context.Background()
	r, err := s.CreateSession(ctx, sampleRecord(project))
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	r.IsTerminated = true
	r.CleanupGeneration = generation
	if err := s.UpdateSession(ctx, r); err != nil {
		t.Fatalf("mark session terminal: %v", err)
	}
	return r
}

func upsertFacts(t *testing.T, s *sqlite.Store, rec domain.SessionCleanupRecord) {
	t.Helper()
	if err := s.UpsertSessionCleanupFacts(context.Background(), rec); err != nil {
		t.Fatalf("upsert facts for %s: %v", rec.SessionID, err)
	}
}

func requireCleanupEqual(t *testing.T, got, want domain.SessionCleanupRecord) {
	t.Helper()
	if got.SessionID != want.SessionID ||
		got.SessionGeneration != want.SessionGeneration ||
		got.WorkspaceDisposition != want.WorkspaceDisposition ||
		got.AttemptCount != want.AttemptCount ||
		got.FailureCode != want.FailureCode {
		t.Fatalf("cleanup facts scalars = %#v, want %#v", got, want)
	}
	if !got.RuntimeReleasedAt.Equal(want.RuntimeReleasedAt) {
		t.Fatalf("runtimeReleasedAt = %v, want %v", got.RuntimeReleasedAt, want.RuntimeReleasedAt)
	}
	if !got.LastAttemptAt.Equal(want.LastAttemptAt) {
		t.Fatalf("lastAttemptAt = %v, want %v", got.LastAttemptAt, want.LastAttemptAt)
	}
	if !got.NextAttemptAt.Equal(want.NextAttemptAt) {
		t.Fatalf("nextAttemptAt = %v, want %v", got.NextAttemptAt, want.NextAttemptAt)
	}
}

// Ordinary UpdateSession writes cannot change controller ownership. Only
// Lifecycle Manager's dedicated compare-and-swap may do that, so unrelated
// metadata updates cannot accidentally switch the interface.
func TestOrdinarySessionUpdatesCannotChangeControllerMode(t *testing.T) {
	s := newTestStore(t)
	seedProject(t, s, "cm")
	ctx := context.Background()

	rec := sampleRecord("cm")
	rec.Mode = domain.SessionModeChat
	rec.Metadata.ProviderConversationID = "thread-1"
	created, err := s.CreateSession(ctx, rec)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if created.Mode != domain.SessionModeChat {
		t.Fatalf("created mode = %q, want chat", created.Mode)
	}

	// A caller trying to flip the mode, alongside a legitimate change.
	created.Mode = domain.SessionModeTUI
	created.DisplayName = "renamed"
	created.Metadata.ProviderConversationID = "thread-2"
	if err := s.UpdateSession(ctx, created); err != nil {
		t.Fatalf("UpdateSession: %v", err)
	}

	after, found, err := s.GetSession(ctx, created.ID)
	if err != nil || !found {
		t.Fatalf("GetSession: err=%v found=%v", err, found)
	}
	if after.Mode != domain.SessionModeChat {
		t.Fatalf("ordinary update changed controller mode to %q", after.Mode)
	}
	// The controller handle, by contrast, is meant to be updatable: a resume
	// rebinds it.
	if after.Metadata.ProviderConversationID != "thread-2" {
		t.Errorf("provider conversation id = %q, want the update to have applied",
			after.Metadata.ProviderConversationID)
	}
	if after.DisplayName != "renamed" {
		t.Errorf("display name = %q, want the update to have applied", after.DisplayName)
	}
}
