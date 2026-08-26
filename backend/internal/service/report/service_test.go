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

func TestCreateDerivesWorkerProjectAndPendingDelivery(t *testing.T) {
	now := time.Date(2026, 8, 26, 1, 2, 3, 0, time.UTC)
	st := &fakeStore{ok: true, session: domain.SessionRecord{ID: "ao-7", ProjectID: "ao", Kind: domain.KindWorker}}
	s := New(Deps{Store: st, Now: func() time.Time { return now }, NewID: func() string { return "rpt_1" }})
	r, err := s.Create(context.Background(), "ao-7", domain.ReportCheckpoint, "tests green")
	if err != nil {
		t.Fatal(err)
	}
	if r.ID != "rpt_1" || r.ProjectID != "ao" || r.DeliveryState != domain.ReportPending || !r.AvailableAt.Equal(now) {
		t.Fatalf("report=%+v", r)
	}
}

func TestCreateDefendsValidationAndOwnership(t *testing.T) {
	for _, tc := range []struct {
		name  string
		store *fakeStore
		typ   domain.ReportType
		note  string
	}{
		{"unknown session", &fakeStore{}, domain.ReportDone, "done"},
		{"orchestrator", &fakeStore{ok: true, session: domain.SessionRecord{ID: "ao-1", ProjectID: "ao", Kind: domain.KindOrchestrator}}, domain.ReportDone, "done"},
		{"bad type", &fakeStore{ok: true, session: domain.SessionRecord{ID: "ao-2", ProjectID: "ao", Kind: domain.KindWorker}}, "bad", "note"},
		{"bad pr", &fakeStore{ok: true, session: domain.SessionRecord{ID: "ao-2", ProjectID: "ao", Kind: domain.KindWorker}}, domain.ReportPRCreated, "https://example.com/x"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := New(Deps{Store: tc.store}).Create(context.Background(), "ao-2", tc.typ, tc.note)
			if err == nil {
				t.Fatal("expected error")
			}
		})
	}
}
