package store_test

import (
	"context"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
)

// seedChangeLogRows creates n sessions so their CDC triggers append n
// change_log rows with contiguous seqs, then reports the log head.
func seedChangeLogRows(t *testing.T, s *sqlite.Store, project string, n int) int64 {
	t.Helper()
	seedProject(t, s, project)
	ctx := context.Background()
	for i := 0; i < n; i++ {
		if _, err := s.CreateSession(ctx, sampleRecord(project)); err != nil {
			t.Fatalf("seed session %d: %v", i, err)
		}
	}
	head, err := s.LatestSeq(ctx)
	if err != nil {
		t.Fatalf("latest seq: %v", err)
	}
	return head
}

func TestPruneChangeLogKeepsNewestRows(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	head := seedChangeLogRows(t, s, "mer", 150)

	n, err := s.PruneChangeLog(ctx, 50)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if n != head-50 {
		t.Fatalf("pruned = %d, want %d", n, head-50)
	}

	events, err := s.EventsAfter(ctx, 0, 1000)
	if err != nil {
		t.Fatalf("events after prune: %v", err)
	}
	if len(events) != 50 {
		t.Fatalf("remaining events = %d, want 50", len(events))
	}
	if events[0].Seq != head-49 {
		t.Fatalf("oldest retained seq = %d, want %d", events[0].Seq, head-49)
	}
}

func TestPruneChangeLogNoopBelowCap(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedChangeLogRows(t, s, "mer", 10)

	n, err := s.PruneChangeLog(ctx, 100)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if n != 0 {
		t.Fatalf("pruned = %d, want 0 (log below cap must be untouched)", n)
	}
}

func TestPruneChangeLogIdempotent(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedChangeLogRows(t, s, "mer", 120)

	if _, err := s.PruneChangeLog(ctx, 50); err != nil {
		t.Fatalf("first prune: %v", err)
	}
	n, err := s.PruneChangeLog(ctx, 50)
	if err != nil {
		t.Fatalf("second prune: %v", err)
	}
	if n != 0 {
		t.Fatalf("second prune removed %d rows, want 0", n)
	}
}
