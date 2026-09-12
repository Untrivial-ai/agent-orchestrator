package store_test

import (
	"context"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func TestCreateSessionsAtomicRollbackAndCDC(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "batch")
	good := sampleRecord("batch")
	bad := sampleRecord("missing")
	if rows, err := s.CreateSessions(ctx, []domain.SessionRecord{good, bad}); err == nil || len(rows) != 0 {
		t.Fatalf("failed batch returned rows=%v err=%v", rows, err)
	}
	rows, err := s.ListAllSessions(ctx)
	if err != nil || len(rows) != 0 {
		t.Fatalf("partial batch persisted: %v %v", rows, err)
	}
	events, err := s.EventsAfter(ctx, 0, 100)
	if err != nil || len(events) != 0 {
		t.Fatalf("rolled-back batch leaked CDC: %v %v", events, err)
	}
	rows, err = s.CreateSessions(ctx, []domain.SessionRecord{good, good})
	if err != nil || len(rows) != 2 || rows[0].ID != "batch-1" || rows[1].ID != "batch-2" {
		t.Fatalf("retry identities: %v %v", rows, err)
	}
	events, err = s.EventsAfter(ctx, 0, 100)
	if err != nil || len(events) != 2 {
		t.Fatalf("committed batch CDC: %v %v", events, err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := s.CreateSessions(cancelled, []domain.SessionRecord{good}); err == nil {
		t.Fatal("cancelled batch succeeded")
	}
	row, err := s.CreateSession(ctx, good)
	if err != nil || row.ID != "batch-3" {
		t.Fatalf("single create after batch: %v %v", row, err)
	}
}

func TestImportedSourceBranchAndPermissionsRoundTripTogether(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "combined")
	rec := sampleRecord("combined")
	rec.Metadata.SourceBranch = "feature/imported"
	rec.Metadata.Permissions = domain.PermissionModeAcceptEdits
	if _, err := s.CreateSessions(ctx, []domain.SessionRecord{rec}); err != nil {
		t.Fatal(err)
	}
	rows, err := s.ListAllSessions(ctx)
	if err != nil || len(rows) != 1 {
		t.Fatalf("read sessions: %+v %v", rows, err)
	}
	if rows[0].Metadata.SourceBranch != rec.Metadata.SourceBranch || rows[0].Metadata.Permissions != rec.Metadata.Permissions {
		t.Fatalf("metadata did not survive batch storage: %+v", rows[0].Metadata)
	}
}
