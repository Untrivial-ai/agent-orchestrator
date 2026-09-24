package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func TestSessionHistoryTracksStopRestoreAndPages(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "mer")
	seedProject(t, s, "other")
	base := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	makeStopped := func(project, name string, offset time.Duration) domain.SessionRecord {
		rec := sampleRecord(project)
		rec.DisplayName = name
		rec.CreatedAt = base
		rec.UpdatedAt = base
		created, err := s.CreateSession(ctx, rec)
		if err != nil {
			t.Fatal(err)
		}
		created.IsTerminated = true
		created.UpdatedAt = base.Add(offset)
		if err := s.UpdateSession(ctx, created); err != nil {
			t.Fatal(err)
		}
		return created
	}
	first := makeStopped("mer", "first", time.Minute)
	second := makeStopped("mer", "second", 2*time.Minute)
	_ = makeStopped("other", "third", 3*time.Minute)

	entries, err := s.ListSessionHistory(ctx, domain.SessionHistoryFilter{ProjectID: "mer", Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].ID != second.ID || entries[0].StoppedAt == nil || !entries[0].StoppedAt.Equal(second.UpdatedAt) {
		t.Fatalf("first page = %+v", entries)
	}
	entries, err = s.ListSessionHistory(ctx, domain.SessionHistoryFilter{ProjectID: "mer", Limit: 1, BeforeEpoch: entries[0].SortEpoch, BeforeID: entries[0].ID})
	if err != nil || len(entries) != 1 || entries[0].ID != first.ID {
		t.Fatalf("second page = %+v, %v", entries, err)
	}

	second.IsTerminated = false
	second.UpdatedAt = base.Add(4 * time.Minute)
	if err := s.UpdateSession(ctx, second); err != nil {
		t.Fatal(err)
	}
	entries, err = s.ListSessionHistory(ctx, domain.SessionHistoryFilter{ProjectID: "mer", Limit: 10})
	if err != nil || len(entries) != 1 || entries[0].ID != first.ID {
		t.Fatalf("restored history = %+v, %v", entries, err)
	}
	second.IsTerminated = true
	second.UpdatedAt = base.Add(5 * time.Minute)
	if err := s.UpdateSession(ctx, second); err != nil {
		t.Fatal(err)
	}
	entries, err = s.ListSessionHistory(ctx, domain.SessionHistoryFilter{ProjectID: "mer", Query: "second", Limit: 10})
	if err != nil || len(entries) != 1 || entries[0].StoppedAt == nil || !entries[0].StoppedAt.Equal(second.UpdatedAt) {
		t.Fatalf("restopped history = %+v, %v", entries, err)
	}
}

func TestSessionHistoryTreatsSearchWildcardsLiterally(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "mer")
	rec := sampleRecord("mer")
	rec.DisplayName = "literal%"
	rec.IsTerminated = true
	if _, err := s.CreateSession(ctx, rec); err != nil {
		t.Fatal(err)
	}
	rows, err := s.ListSessionHistory(ctx, domain.SessionHistoryFilter{Query: "%", Limit: 10})
	if err != nil || len(rows) != 1 {
		t.Fatalf("literal percent = %+v, %v", rows, err)
	}
	rows, err = s.ListSessionHistory(ctx, domain.SessionHistoryFilter{Query: "_", Limit: 10})
	if err != nil || len(rows) != 0 {
		t.Fatalf("literal underscore = %+v, %v", rows, err)
	}
}

func TestActiveSessionRecordsExcludeStoppedArchive(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "mer")
	seedProject(t, s, "other")
	active, err := s.CreateSession(ctx, sampleRecord("mer"))
	if err != nil {
		t.Fatal(err)
	}
	stopped := sampleRecord("mer")
	stopped.IsTerminated = true
	if _, err := s.CreateSession(ctx, stopped); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateSession(ctx, sampleRecord("other")); err != nil {
		t.Fatal(err)
	}
	recs, err := s.ListActiveSessionRecords(ctx, "mer")
	if err != nil || len(recs) != 1 || recs[0].ID != active.ID {
		t.Fatalf("active = %+v, %v", recs, err)
	}
}
