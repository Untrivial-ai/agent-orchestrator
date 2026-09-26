package automation

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
)

func TestCreateRejectsPromptOverSessionSpawnByteLimit(t *testing.T) {
	svc := New(Deps{Store: newFakeStore(), Clock: func() time.Time {
		return time.Date(2026, time.March, 6, 15, 0, 0, 0, time.UTC)
	}})

	_, err := svc.Create(context.Background(), CreateInput{
		ProjectID: "scheduled", DisplayName: "Unicode overflow",
		Prompt: strings.Repeat("界", 1366), Kind: domain.KindWorker,
		RRule: "FREQ=DAILY;BYHOUR=9;BYMINUTE=0;BYSECOND=0", Timezone: "UTC",
	})
	var apiErr *apierr.Error
	if !errors.As(err, &apiErr) || apiErr.Code != "INVALID_AUTOMATION_PROMPT" {
		t.Fatalf("Create error = %v, want INVALID_AUTOMATION_PROMPT", err)
	}
}

type fakeStore struct {
	projects    map[string]domain.ProjectRecord
	automations map[domain.AutomationID]domain.Automation
	latestRuns  map[domain.AutomationID]domain.AutomationRun
	latestCalls int
	latestErr   error
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		projects: map[string]domain.ProjectRecord{
			"scheduled": {ID: "scheduled", Path: "/tmp/scheduled"},
		},
		automations: map[domain.AutomationID]domain.Automation{},
		latestRuns:  map[domain.AutomationID]domain.AutomationRun{},
	}
}

func (f *fakeStore) ListLatestAutomationRuns(_ context.Context, ids []domain.AutomationID) (map[domain.AutomationID]domain.AutomationRun, error) {
	f.latestCalls++
	if f.latestErr != nil {
		return nil, f.latestErr
	}
	out := make(map[domain.AutomationID]domain.AutomationRun)
	for _, id := range ids {
		if run, ok := f.latestRuns[id]; ok {
			out[id] = run
		}
	}
	return out, nil
}

func (f *fakeStore) GetProject(_ context.Context, id string) (domain.ProjectRecord, bool, error) {
	rec, ok := f.projects[id]
	return rec, ok, nil
}

func (f *fakeStore) CreateAutomation(_ context.Context, rec domain.Automation) (domain.Automation, error) {
	f.automations[rec.ID] = rec
	return rec, nil
}

func (f *fakeStore) GetAutomation(_ context.Context, id domain.AutomationID) (domain.Automation, bool, error) {
	rec, ok := f.automations[id]
	return rec, ok, nil
}

func (f *fakeStore) ListAutomations(_ context.Context, filter domain.AutomationFilter) (domain.AutomationPage, error) {
	items := make([]domain.Automation, 0, len(f.automations))
	for _, item := range f.automations {
		items = append(items, item)
	}
	return domain.AutomationPage{Items: items, Total: int64(len(items))}, nil
}

func (f *fakeStore) UpdateAutomation(_ context.Context, rec domain.Automation) (bool, error) {
	if _, ok := f.automations[rec.ID]; !ok {
		return false, nil
	}
	f.automations[rec.ID] = rec
	return true, nil
}

func (f *fakeStore) DeleteAutomation(_ context.Context, id domain.AutomationID) (bool, error) {
	if _, ok := f.automations[id]; !ok {
		return false, nil
	}
	delete(f.automations, id)
	return true, nil
}

func (f *fakeStore) ListAutomationRuns(_ context.Context, filter domain.AutomationRunFilter) (domain.AutomationRunPage, error) {
	return domain.AutomationRunPage{}, nil
}

func TestListLoadsLatestRunsInOneStoreCall(t *testing.T) {
	store := newFakeStore()
	store.automations["automation-1"] = domain.Automation{ID: "automation-1"}
	store.automations["automation-2"] = domain.Automation{ID: "automation-2"}
	store.latestRuns["automation-2"] = domain.AutomationRun{ID: "run-2", AutomationID: "automation-2"}
	page, err := New(Deps{Store: store}).List(context.Background(), domain.AutomationFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if store.latestCalls != 1 {
		t.Fatalf("latest-run queries = %d, want 1", store.latestCalls)
	}
	found := false
	for _, item := range page.Items {
		if item.ID == "automation-2" {
			found = item.LatestRun != nil && item.LatestRun.ID == "run-2"
		}
	}
	if !found {
		t.Fatalf("page = %#v, want latest run attached", page)
	}
}

// Removing server-side canonicalization, default enabling, or injected durable
// identity must make this test fail.
func TestCreateValidatesAndPersistsCanonicalAutomation(t *testing.T) {
	now := time.Date(2026, time.March, 6, 15, 0, 0, 0, time.UTC)
	store := newFakeStore()
	svc := New(Deps{
		Store: store,
		Clock: func() time.Time { return now },
		NewID: func() string { return "generated-id" },
	})

	got, err := svc.Create(context.Background(), CreateInput{
		ProjectID: "scheduled", DisplayName: "  Morning triage  ",
		Prompt: "  Review new issues  ", Kind: domain.KindWorker,
		Harness: domain.HarnessCodex, Cron: "0 9 * * 1-5",
		Timezone: "America/New_York",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got.ID != "automation-generated-id" || got.DisplayName != "Morning triage" || got.Prompt != "Review new issues" || !got.Enabled {
		t.Fatalf("created automation = %#v", got)
	}
	wantNext := time.Date(2026, time.March, 9, 13, 0, 0, 0, time.UTC)
	if !got.NextRunAt.Equal(wantNext) || got.RRuleText == "" {
		t.Fatalf("canonical schedule = next:%s rule:%q, want next:%s", got.NextRunAt, got.RRuleText, wantNext)
	}
	if persisted := store.automations[got.ID]; persisted.ID != got.ID || persisted.RRuleText != got.RRuleText {
		t.Fatalf("persisted automation = %#v, want created record", persisted)
	}
}

// Removing patch semantics, re-enable schedule advancement, or schedule
// replacement canonicalization must make this test fail.
func TestUpdateAppliesOnlyProvidedFieldsAndReenablesFromNow(t *testing.T) {
	now := time.Date(2026, time.March, 8, 15, 30, 0, 0, time.UTC)
	store := newFakeStore()
	store.automations["automation-1"] = domain.Automation{
		ID: "automation-1", ProjectID: "scheduled", DisplayName: "Old name",
		Prompt: "Keep this", Kind: domain.KindWorker, Harness: domain.HarnessCodex,
		RRuleText: "DTSTART:20260301T140000\nRRULE:FREQ=DAILY;BYHOUR=9;BYMINUTE=0;BYSECOND=0",
		Timezone:  "America/New_York", Enabled: false,
		NextRunAt: time.Date(2026, time.March, 2, 14, 0, 0, 0, time.UTC),
		CreatedAt: now.Add(-24 * time.Hour), UpdatedAt: now.Add(-24 * time.Hour),
	}
	svc := New(Deps{Store: store, Clock: func() time.Time { return now }})
	name, enabled := "New name", true
	cron, zone := "45 10 * * *", "America/New_York"

	got, err := svc.Update(context.Background(), "automation-1", UpdateInput{
		DisplayName: &name, Cron: &cron, Timezone: &zone, Enabled: &enabled,
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if got.DisplayName != name || got.Prompt != "Keep this" || !got.Enabled {
		t.Fatalf("updated automation = %#v", got)
	}
	wantNext := time.Date(2026, time.March, 9, 14, 45, 0, 0, time.UTC)
	if !got.NextRunAt.Equal(wantNext) || got.Timezone != zone {
		t.Fatalf("next = %s in %q, want %s in %q", got.NextRunAt, got.Timezone, wantNext, zone)
	}
}

// Disabling is only a scheduling gate; it must not rewrite the outstanding
// occurrence or any durable history pointer.
func TestUpdateDisablePreservesNextOccurrence(t *testing.T) {
	now := time.Date(2026, time.March, 8, 15, 30, 0, 0, time.UTC)
	next := now.Add(time.Hour)
	last := now.Add(-24 * time.Hour)
	store := newFakeStore()
	store.automations["automation-1"] = domain.Automation{
		ID: "automation-1", ProjectID: "scheduled", DisplayName: "Daily", Prompt: "Work",
		Kind: domain.KindWorker, RRuleText: "DTSTART:20260301T160000\nRRULE:FREQ=DAILY",
		Timezone: "UTC", Enabled: true, NextRunAt: next, LastRunAt: &last,
	}
	svc := New(Deps{Store: store, Clock: func() time.Time { return now }})
	disabled := false
	got, err := svc.Update(context.Background(), "automation-1", UpdateInput{Enabled: &disabled})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if got.Enabled || !got.NextRunAt.Equal(next) || got.LastRunAt == nil || !got.LastRunAt.Equal(last) {
		t.Fatalf("disabled automation = %#v", got)
	}
}

func TestCRUDReturnsNotFoundAndDeleteRemovesDefinition(t *testing.T) {
	store := newFakeStore()
	store.automations["automation-1"] = domain.Automation{ID: "automation-1"}
	svc := New(Deps{Store: store})

	if _, err := svc.Get(context.Background(), "missing"); !isAPIError(err, apierr.KindNotFound) {
		t.Fatalf("Get missing error = %v", err)
	}
	if err := svc.Delete(context.Background(), "automation-1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok := store.automations["automation-1"]; ok {
		t.Fatal("deleted automation remains in store")
	}
	if err := svc.Delete(context.Background(), "automation-1"); !isAPIError(err, apierr.KindNotFound) {
		t.Fatalf("Delete missing error = %v", err)
	}
}

func isAPIError(err error, kind apierr.Kind) bool {
	var apiError *apierr.Error
	return errors.As(err, &apiError) && apiError.Kind == kind
}
