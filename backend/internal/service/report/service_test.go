package report

import (
	"context"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

type fakeStore struct {
	session domain.SessionRecord
	ok      bool
	created domain.ReportRecord
}

func (f *fakeStore) GetSession(context.Context, domain.SessionID) (domain.SessionRecord, bool, error) {
	return f.session, f.ok, nil
}
func (f *fakeStore) CreateReport(_ context.Context, r domain.ReportRecord) (domain.ReportRecord, error) {
	f.created = r
	return r, nil
}

func TestCreateDerivesOwnershipAndFixedDeliveryDeadlines(t *testing.T) {
	now := time.Date(2026, 9, 7, 1, 2, 3, 0, time.UTC)
	tests := []struct {
		name      string
		input     CreateInput
		available time.Time
		settles   time.Time
	}{
		{name: "free form batches for one hour", input: CreateInput{Message: "status"}, available: now.Add(time.Hour)},
		{name: "output only batches for one hour", input: CreateInput{Outputs: []domain.ReportOutput{{Kind: domain.ReportOutputArtifact, Reference: "result"}}}, available: now.Add(time.Hour)},
		{name: "checkpoint batches for one hour", input: CreateInput{State: domain.ReportCheckpoint, Note: "checkpoint"}, available: now.Add(time.Hour)},
		{name: "needs input is immediate", input: CreateInput{State: domain.ReportNeedsInput, Note: "decision"}, available: now},
		{name: "stuck is immediate", input: CreateInput{State: domain.ReportStuck, Note: "blocked"}, available: now},
		{name: "done settles for five minutes", input: CreateInput{State: domain.ReportDone, Note: "finished"}, available: now.Add(5 * time.Minute), settles: now.Add(5 * time.Minute)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			st := &fakeStore{ok: true, session: domain.SessionRecord{ID: "ao-7", ProjectID: "ao", Kind: domain.KindWorker}}
			s := New(Deps{Store: st, Now: func() time.Time { return now }, NewID: func() string { return "rpt_1" }})
			tc.input.SessionID = "ao-7"
			r, err := s.Create(context.Background(), tc.input)
			if err != nil {
				t.Fatal(err)
			}
			if r.ID != "rpt_1" || r.ProjectID != "ao" || r.DeliveryState != domain.ReportPending || r.RepeatCount != 1 || !r.AvailableAt.Equal(tc.available) || !r.SettlementDeadline.Equal(tc.settles) {
				t.Fatalf("report=%+v", r)
			}
		})
	}
}

func TestCreateDefendsValidationAndOwnership(t *testing.T) {
	worker := &fakeStore{ok: true, session: domain.SessionRecord{ID: "ao-2", ProjectID: "ao", Kind: domain.KindWorker}}
	for _, tc := range []struct {
		name  string
		store *fakeStore
		input CreateInput
	}{
		{"missing session", worker, CreateInput{Message: "message"}},
		{"unknown session", &fakeStore{}, CreateInput{SessionID: "ao-2", State: domain.ReportDone, Note: "done"}},
		{"orchestrator", &fakeStore{ok: true, session: domain.SessionRecord{ID: "ao-1", ProjectID: "ao", Kind: domain.KindOrchestrator}}, CreateInput{SessionID: "ao-1", State: domain.ReportDone, Note: "done"}},
		{"bad state", worker, CreateInput{SessionID: "ao-2", State: "bad", Note: "note"}},
		{"bad PR", worker, CreateInput{SessionID: "ao-2", Outputs: []domain.ReportOutput{{Kind: domain.ReportOutputPRCreated, Reference: "https://example.com/x"}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := New(Deps{Store: tc.store}).Create(context.Background(), tc.input)
			if err == nil {
				t.Fatal("expected error")
			}
		})
	}
}
