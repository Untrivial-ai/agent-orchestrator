package session

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func TestHistoryBindsNextCursorAndDoesNotReadUnselectedRows(t *testing.T) {
	st := newFakeStore()
	now := time.Now().UTC().Truncate(time.Second)
	st.sessions["mer-3"] = domain.SessionRecord{ID: "mer-3", ProjectID: "mer", Kind: domain.KindWorker, IsTerminated: true, UpdatedAt: now}
	st.sessions["mer-2"] = domain.SessionRecord{ID: "mer-2", ProjectID: "mer", Kind: domain.KindWorker, IsTerminated: true, UpdatedAt: now}
	st.historyEntries = []domain.SessionHistoryEntry{
		{ID: "mer-3", SortEpoch: now.Unix(), StoppedAt: &now},
		{ID: "mer-2", SortEpoch: now.Unix() - 1, StoppedAt: &now},
	}
	page, err := (&Service{store: st}).History(context.Background(), HistoryFilter{Limit: 1, Delivery: "no_pr"})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Sessions) != 1 || page.Sessions[0].Session.ID != "mer-3" {
		t.Fatalf("page = %+v", page)
	}
	if !strings.Contains(string(stringMustDecode(t, page.NextCursor)), "mer-3") {
		t.Fatalf("cursor = %q", page.NextCursor)
	}
	if st.historyFilter.Limit != 1 || st.historyFilter.Delivery != "no_pr" {
		t.Fatalf("filter = %+v", st.historyFilter)
	}
	if !strings.Contains(strings.Join(page.Sessions[0].RetentionHolds, ","), "backup_unverified") {
		t.Fatalf("holds = %+v", page.Sessions[0].RetentionHolds)
	}
}

func stringMustDecode(t *testing.T, value string) []byte {
	t.Helper()
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		t.Fatal(err)
	}
	return decoded
}

func TestHistoryRejectsInvalidCursorAndDelivery(t *testing.T) {
	st := newFakeStore()
	service := &Service{store: st}
	for _, filter := range []HistoryFilter{{Cursor: "not-base64?"}, {Delivery: "deleted"}, {Limit: 101}} {
		if _, err := service.History(context.Background(), filter); err == nil {
			t.Fatalf("accepted %+v", filter)
		}
	}
}

func TestRetentionAssessmentFailsClosed(t *testing.T) {
	old := time.Now().UTC().AddDate(-1, 0, 0)
	worker := domain.SessionRecord{Kind: domain.KindWorker}
	pr := domain.PRFacts{Merged: true}
	holds := retentionHolds(worker, &old, domain.DispositionRemoved, []domain.PRFacts{pr}, false, time.Now().UTC())
	if len(holds) != 1 || holds[0] != "backup_unverified" {
		t.Fatalf("merged worker holds = %v", holds)
	}
	worker.IsPinned = true
	holds = retentionHolds(worker, nil, domain.DispositionPreservedDirty, nil, true, time.Now().UTC())
	for _, want := range []string{"pinned", "stop_time_unknown", "workspace_not_reclaimed", "agent_switch_active", "delivery_unverified", "backup_unverified"} {
		if !strings.Contains(strings.Join(holds, ","), want) {
			t.Fatalf("missing %q from %v", want, holds)
		}
	}
}
