package store_test

import (
	"context"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/cdc"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func TestStartupProgressPublishesSessionChange(t *testing.T) {
	s := newTestStore(t)
	seedProject(t, s, "mer")
	seed := sampleRecord("mer")
	seed.Metadata.Startup = &domain.SessionStartup{ID: "operation", Stage: "provisioning"}
	rec, err := s.CreateSession(context.Background(), seed)
	if err != nil {
		t.Fatal(err)
	}
	for _, stage := range []string{"cleanup_pending", ""} {
		seq, err := s.LatestSeq(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		expected := rec.ControllerOwner()
		if stage == "" {
			rec.Metadata.Startup = nil
		} else {
			rec.Metadata.Startup = &domain.SessionStartup{ID: "operation", Stage: stage, LastError: "cleanup needs retry"}
		}
		applied, err := s.UpdateSessionStartup(context.Background(), rec, "operation", expected)
		if err != nil || !applied {
			t.Fatalf("startup write applied=%v error=%v", applied, err)
		}
		events, err := s.EventsAfter(context.Background(), seq, 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(events) != 1 || events[0].Type != cdc.EventSessionUpdated || events[0].SessionID != string(rec.ID) {
			t.Fatalf("startup progress events = %+v", events)
		}
	}
}
