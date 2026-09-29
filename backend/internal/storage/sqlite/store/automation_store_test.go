package store_test

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// Dropping or mis-mapping any definition field in the SQLite boundary must
// make this test fail; the scheduler must restart from the same durable rule
// and timezone the user created.
func TestAutomationCreateAndGetRoundTripsDefinition(t *testing.T) {
	store := newTestStore(t)
	seedProject(t, store, "scheduled")
	ctx := context.Background()
	now := time.Date(2026, time.August, 25, 9, 0, 0, 0, time.UTC)
	lastRun := now.Add(-24 * time.Hour)
	want := domain.Automation{
		ID:          "automation-1",
		ProjectID:   "scheduled",
		DisplayName: "Morning triage",
		Prompt:      "Review new issues",
		Kind:        domain.KindWorker,
		Harness:     domain.HarnessCodex,
		RRuleText:   "FREQ=DAILY;BYHOUR=9;BYMINUTE=0",
		Timezone:    "Asia/Calcutta",
		Enabled:     true,
		NextRunAt:   now.Add(24 * time.Hour),
		LastRunAt:   &lastRun,
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	created, err := store.CreateAutomation(ctx, want)
	if err != nil {
		t.Fatalf("CreateAutomation: %v", err)
	}
	got, ok, err := store.GetAutomation(ctx, created.ID)
	if err != nil || !ok {
		t.Fatalf("GetAutomation: ok=%v err=%v", ok, err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("automation round trip = %#v, want %#v", got, want)
	}
}

// Removing the occurrence conflict handling must make this test fail: a
// repeated materialization pass must return the first durable run rather than
// manufacture a second execution identity.
func TestAutomationRunCreateIsIdempotentByOccurrence(t *testing.T) {
	store := newTestStore(t)
	seedProject(t, store, "scheduled")
	ctx := context.Background()
	now := time.Date(2026, time.August, 25, 9, 0, 0, 0, time.UTC)
	_, err := store.CreateAutomation(ctx, domain.Automation{
		ID: "automation-1", ProjectID: "scheduled", DisplayName: "Triage",
		Prompt: "Review issues", Kind: domain.KindWorker, RRuleText: "FREQ=DAILY",
		Timezone: "UTC", Enabled: true, NextRunAt: now, CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatalf("CreateAutomation: %v", err)
	}

	want := domain.AutomationRun{
		ID: "run-first", AutomationID: "automation-1", ScheduledFor: now,
		Status: domain.AutomationRunPending, CreatedAt: now, UpdatedAt: now,
	}
	first, created, err := store.CreateAutomationRun(ctx, want)
	if err != nil || !created {
		t.Fatalf("first CreateAutomationRun: created=%v err=%v", created, err)
	}
	duplicate := want
	duplicate.ID = "run-duplicate"
	got, created, err := store.CreateAutomationRun(ctx, duplicate)
	if err != nil || created {
		t.Fatalf("duplicate CreateAutomationRun: created=%v err=%v", created, err)
	}
	if got.ID != first.ID || got.ID != want.ID {
		t.Fatalf("duplicate resolved to run %q, want %q", got.ID, want.ID)
	}
}

// Removing the session-origin mapping must make this test fail: restart
// reconciliation relies on reading the exact session already created for a run.
func TestSessionPersistsAutomationRunOrigin(t *testing.T) {
	store := newTestStore(t)
	seedProject(t, store, "scheduled")
	ctx := context.Background()
	now := time.Date(2026, time.August, 25, 9, 0, 0, 0, time.UTC)
	_, err := store.CreateAutomation(ctx, domain.Automation{
		ID: "automation-1", ProjectID: "scheduled", DisplayName: "Triage",
		Prompt: "Review issues", Kind: domain.KindWorker, RRuleText: "FREQ=DAILY",
		Timezone: "UTC", Enabled: true, NextRunAt: now, CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatalf("CreateAutomation: %v", err)
	}
	runID := domain.AutomationRunID("run-1")
	_, _, err = store.CreateAutomationRun(ctx, domain.AutomationRun{
		ID: runID, AutomationID: "automation-1", ScheduledFor: now,
		Status: domain.AutomationRunSpawning, CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatalf("CreateAutomationRun: %v", err)
	}
	rec := sampleRecord("scheduled")
	rec.AutomationRunID = &runID
	created, err := store.CreateSession(ctx, rec)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	got, ok, err := store.GetSession(ctx, created.ID)
	if err != nil || !ok {
		t.Fatalf("GetSession: ok=%v err=%v", ok, err)
	}
	if got.AutomationRunID == nil || *got.AutomationRunID != runID {
		t.Fatalf("session automation run = %v, want %q", got.AutomationRunID, runID)
	}
}

// A retry after the scheduler's post-spawn crash window must adopt the first
// durable session instead of creating a second agent execution.
func TestSessionCreateIsIdempotentByAutomationRun(t *testing.T) {
	store := newTestStore(t)
	seedProject(t, store, "scheduled")
	ctx := context.Background()
	now := time.Date(2026, time.August, 25, 9, 0, 0, 0, time.UTC)
	if _, err := store.CreateAutomation(ctx, domain.Automation{ID: "automation-1", ProjectID: "scheduled", DisplayName: "Triage", Prompt: "Review", Kind: domain.KindWorker, RRuleText: "FREQ=DAILY", Timezone: "UTC", Enabled: true, NextRunAt: now, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	runID := domain.AutomationRunID("run-1")
	if _, _, err := store.CreateAutomationRun(ctx, domain.AutomationRun{ID: runID, AutomationID: "automation-1", ScheduledFor: now, Status: domain.AutomationRunSpawning, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	first := sampleRecord("scheduled")
	first.AutomationRunID = &runID
	created, err := store.CreateSession(ctx, first)
	if err != nil {
		t.Fatalf("first CreateSession: %v", err)
	}
	retry := sampleRecord("scheduled")
	retry.AutomationRunID = &runID
	adopted, err := store.CreateSession(ctx, retry)
	if err != nil {
		t.Fatalf("retry CreateSession: %v", err)
	}
	if adopted.ID != created.ID {
		t.Fatalf("retry session = %q, want adopted %q", adopted.ID, created.ID)
	}
}

// If this created flag is lost, Session Manager cannot distinguish a fresh
// seed from a crash-surviving seed and may launch the same occurrence twice.
func TestAutomationSessionCreateReportsWhetherSeedWasNew(t *testing.T) {
	store := newTestStore(t)
	seedProject(t, store, "scheduled")
	ctx := context.Background()
	now := time.Date(2026, time.August, 25, 9, 0, 0, 0, time.UTC)
	if _, err := store.CreateAutomation(ctx, domain.Automation{ID: "automation-1", ProjectID: "scheduled", DisplayName: "Triage", Prompt: "Review", Kind: domain.KindWorker, RRuleText: "FREQ=DAILY", Timezone: "UTC", Enabled: true, NextRunAt: now, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	runID := domain.AutomationRunID("run-1")
	if _, _, err := store.CreateAutomationRun(ctx, domain.AutomationRun{ID: runID, AutomationID: "automation-1", ScheduledFor: now, Status: domain.AutomationRunSpawning, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	seed := sampleRecord("scheduled")
	seed.AutomationRunID = &runID
	created, fresh, err := store.CreateAutomationSession(ctx, seed)
	if err != nil || !fresh {
		t.Fatalf("first CreateAutomationSession: fresh=%v err=%v", fresh, err)
	}
	created.AutomationLaunchCompleted = true
	if err := store.UpdateSession(ctx, created); err != nil {
		t.Fatal(err)
	}
	adopted, fresh, err := store.CreateAutomationSession(ctx, seed)
	if err != nil || fresh {
		t.Fatalf("retry CreateAutomationSession: fresh=%v err=%v", fresh, err)
	}
	if adopted.ID != created.ID || !adopted.AutomationLaunchCompleted {
		t.Fatalf("adopted session = %+v, want completed %q", adopted, created.ID)
	}
}

// Removing either project/enabled filtering or stable page ordering must make
// this test fail; the API must not leak definitions across projects or reshuffle
// rows between pages.
func TestAutomationListFiltersAndPaginatesDefinitions(t *testing.T) {
	store := newTestStore(t)
	seedProject(t, store, "alpha")
	seedProject(t, store, "beta")
	ctx := context.Background()
	base := time.Date(2026, time.August, 25, 9, 0, 0, 0, time.UTC)
	for _, rec := range []domain.Automation{
		{ID: "alpha-1", ProjectID: "alpha", DisplayName: "First", Prompt: "One", Kind: domain.KindWorker, RRuleText: "FREQ=DAILY", Timezone: "UTC", Enabled: true, NextRunAt: base, CreatedAt: base, UpdatedAt: base},
		{ID: "alpha-2", ProjectID: "alpha", DisplayName: "Second", Prompt: "Two", Kind: domain.KindWorker, RRuleText: "FREQ=DAILY", Timezone: "UTC", Enabled: false, NextRunAt: base, CreatedAt: base.Add(time.Second), UpdatedAt: base},
		{ID: "alpha-3", ProjectID: "alpha", DisplayName: "Third", Prompt: "Three", Kind: domain.KindWorker, RRuleText: "FREQ=DAILY", Timezone: "UTC", Enabled: true, NextRunAt: base, CreatedAt: base.Add(2 * time.Second), UpdatedAt: base},
		{ID: "beta-1", ProjectID: "beta", DisplayName: "Other", Prompt: "Other", Kind: domain.KindWorker, RRuleText: "FREQ=DAILY", Timezone: "UTC", Enabled: true, NextRunAt: base, CreatedAt: base.Add(3 * time.Second), UpdatedAt: base},
	} {
		if _, err := store.CreateAutomation(ctx, rec); err != nil {
			t.Fatalf("CreateAutomation %s: %v", rec.ID, err)
		}
	}
	enabled := true
	projectID := domain.ProjectID("alpha")
	page, err := store.ListAutomations(ctx, domain.AutomationFilter{
		ProjectID: &projectID, Enabled: &enabled, Limit: 1, Offset: 1,
	})
	if err != nil {
		t.Fatalf("ListAutomations: %v", err)
	}
	if page.Total != 2 || len(page.Items) != 1 || page.Items[0].ID != "alpha-3" {
		t.Fatalf("filtered page = total:%d items:%v, want total:2 [alpha-3]", page.Total, page.Items)
	}
}

// Omitting any mutable field from the update statement must make this test
// fail; schedule edits are persisted as one complete validated definition.
func TestAutomationUpdatePersistsAllMutableFields(t *testing.T) {
	store := newTestStore(t)
	seedProject(t, store, "scheduled")
	ctx := context.Background()
	now := time.Date(2026, time.August, 25, 9, 0, 0, 0, time.UTC)
	rec := domain.Automation{
		ID: "automation-1", ProjectID: "scheduled", DisplayName: "Triage",
		Prompt: "Review issues", Kind: domain.KindWorker, RRuleText: "FREQ=DAILY",
		Timezone: "UTC", Enabled: true, NextRunAt: now, CreatedAt: now, UpdatedAt: now,
	}
	if _, err := store.CreateAutomation(ctx, rec); err != nil {
		t.Fatalf("CreateAutomation: %v", err)
	}
	lastRun := now.Add(time.Hour)
	rec.DisplayName = "Weekly triage"
	rec.Prompt = "Review the weekly queue"
	rec.Kind = domain.KindOrchestrator
	rec.Harness = domain.HarnessClaudeCode
	rec.RRuleText = "FREQ=WEEKLY"
	rec.Timezone = "Asia/Calcutta"
	rec.Enabled = false
	rec.NextRunAt = now.Add(7 * 24 * time.Hour)
	rec.LastRunAt = &lastRun
	rec.UpdatedAt = now.Add(2 * time.Hour)
	updated, err := store.UpdateAutomation(ctx, rec)
	if err != nil || !updated {
		t.Fatalf("UpdateAutomation: updated=%v err=%v", updated, err)
	}
	got, ok, err := store.GetAutomation(ctx, rec.ID)
	if err != nil || !ok {
		t.Fatalf("GetAutomation: ok=%v err=%v", ok, err)
	}
	if !reflect.DeepEqual(got, rec) {
		t.Fatalf("updated automation = %#v, want %#v", got, rec)
	}
}

// Reversing history order or ignoring the status filter must make this test
// fail; users and reconciliation both depend on deterministic run selection.
func TestAutomationRunHistoryFiltersNewestFirst(t *testing.T) {
	store := newTestStore(t)
	seedProject(t, store, "scheduled")
	ctx := context.Background()
	base := time.Date(2026, time.August, 25, 9, 0, 0, 0, time.UTC)
	if _, err := store.CreateAutomation(ctx, domain.Automation{
		ID: "automation-1", ProjectID: "scheduled", DisplayName: "Triage",
		Prompt: "Review issues", Kind: domain.KindWorker, RRuleText: "FREQ=DAILY",
		Timezone: "UTC", Enabled: true, NextRunAt: base, CreatedAt: base, UpdatedAt: base,
	}); err != nil {
		t.Fatalf("CreateAutomation: %v", err)
	}
	for _, run := range []domain.AutomationRun{
		{ID: "run-1", AutomationID: "automation-1", ScheduledFor: base, Status: domain.AutomationRunCompleted, CreatedAt: base, UpdatedAt: base},
		{ID: "run-2", AutomationID: "automation-1", ScheduledFor: base.Add(time.Hour), Status: domain.AutomationRunFailed, ErrorMessage: "missing project", CreatedAt: base, UpdatedAt: base},
		{ID: "run-3", AutomationID: "automation-1", ScheduledFor: base.Add(2 * time.Hour), Status: domain.AutomationRunFailed, ErrorMessage: "invalid config", CreatedAt: base, UpdatedAt: base},
	} {
		if _, _, err := store.CreateAutomationRun(ctx, run); err != nil {
			t.Fatalf("CreateAutomationRun %s: %v", run.ID, err)
		}
	}
	status := domain.AutomationRunFailed
	page, err := store.ListAutomationRuns(ctx, domain.AutomationRunFilter{
		AutomationID: "automation-1", Status: &status, Limit: 1, Offset: 0,
	})
	if err != nil {
		t.Fatalf("ListAutomationRuns: %v", err)
	}
	if page.Total != 2 || len(page.Items) != 1 || page.Items[0].ID != "run-3" || page.Items[0].ErrorMessage != "invalid config" {
		t.Fatalf("run page = total:%d items:%v, want newest failed run-3", page.Total, page.Items)
	}
}

// Materialization must atomically advance the definition with its occurrence
// inserts; a stale scheduler pass must not append a second batch.
func TestAutomationMaterializationUsesNextRunCompareAndSwap(t *testing.T) {
	store := newTestStore(t)
	seedProject(t, store, "scheduled")
	ctx := context.Background()
	base := time.Date(2026, time.August, 25, 9, 0, 0, 0, time.UTC)
	if _, err := store.CreateAutomation(ctx, domain.Automation{
		ID: "automation-1", ProjectID: "scheduled", DisplayName: "Triage", Prompt: "Review",
		Kind: domain.KindWorker, RRuleText: "FREQ=HOURLY", Timezone: "UTC", Enabled: true,
		NextRunAt: base, CreatedAt: base, UpdatedAt: base,
	}); err != nil {
		t.Fatalf("CreateAutomation: %v", err)
	}
	runs := []domain.AutomationRun{
		{ID: "run-1", AutomationID: "automation-1", ScheduledFor: base, Status: domain.AutomationRunPending, CreatedAt: base, UpdatedAt: base},
		{ID: "run-2", AutomationID: "automation-1", ScheduledFor: base.Add(time.Hour), Status: domain.AutomationRunPending, CreatedAt: base, UpdatedAt: base},
	}
	next := base.Add(2 * time.Hour)
	applied, err := store.MaterializeAutomationRuns(ctx, "automation-1", base, runs, base.Add(time.Hour), next, base)
	if err != nil || !applied {
		t.Fatalf("MaterializeAutomationRuns: applied=%v err=%v", applied, err)
	}
	applied, err = store.MaterializeAutomationRuns(ctx, "automation-1", base, []domain.AutomationRun{{
		ID: "run-stale", AutomationID: "automation-1", ScheduledFor: base.Add(30 * time.Minute), Status: domain.AutomationRunPending, CreatedAt: base, UpdatedAt: base,
	}}, base, next, base)
	if err != nil || applied {
		t.Fatalf("stale MaterializeAutomationRuns: applied=%v err=%v", applied, err)
	}
	definition, _, _ := store.GetAutomation(ctx, "automation-1")
	page, err := store.ListAutomationRuns(ctx, domain.AutomationRunFilter{AutomationID: "automation-1", Limit: 10})
	if err != nil || page.Total != 2 || !definition.NextRunAt.Equal(next) {
		t.Fatalf("materialized state: definition=%#v runs=%#v err=%v", definition, page, err)
	}
}

// The claim transaction is the non-overlap boundary. Once one occurrence is
// spawning, later pending work for that definition cannot be leased.
func TestAutomationClaimLeasesOldestPendingWithoutOverlap(t *testing.T) {
	store := newTestStore(t)
	seedProject(t, store, "scheduled")
	ctx := context.Background()
	base := time.Date(2026, time.August, 25, 9, 0, 0, 0, time.UTC)
	if _, err := store.CreateAutomation(ctx, domain.Automation{
		ID: "automation-1", ProjectID: "scheduled", DisplayName: "Triage", Prompt: "Review",
		Kind: domain.KindWorker, RRuleText: "FREQ=HOURLY", Timezone: "UTC", Enabled: true,
		NextRunAt: base.Add(3 * time.Hour), CreatedAt: base, UpdatedAt: base,
	}); err != nil {
		t.Fatal(err)
	}
	for i, scheduled := range []time.Time{base, base.Add(time.Hour)} {
		if _, _, err := store.CreateAutomationRun(ctx, domain.AutomationRun{
			ID: domain.AutomationRunID("run-" + string(rune('1'+i))), AutomationID: "automation-1",
			ScheduledFor: scheduled, Status: domain.AutomationRunPending, CreatedAt: base, UpdatedAt: base,
		}); err != nil {
			t.Fatal(err)
		}
	}
	lease := base.Add(time.Minute)
	claimed, ok, err := store.ClaimNextAutomationRun(ctx, base, lease)
	if err != nil || !ok || claimed.ID != "run-1" || claimed.Status != domain.AutomationRunSpawning || claimed.AttemptCount != 1 {
		t.Fatalf("first claim = %#v ok=%v err=%v", claimed, ok, err)
	}
	if claimed.LeaseExpiresAt == nil || !claimed.LeaseExpiresAt.Equal(lease) {
		t.Fatalf("lease = %v, want %s", claimed.LeaseExpiresAt, lease)
	}
	if second, ok, err := store.ClaimNextAutomationRun(ctx, base, lease); err != nil || ok {
		t.Fatalf("overlapping claim = %#v ok=%v err=%v", second, ok, err)
	}
}

// Deleting a definition removes history while preserving linked sessions and
// clearing their origin link through ON DELETE SET NULL.
func TestAutomationDeleteCascadesRunsButPreservesSession(t *testing.T) {
	store := newTestStore(t)
	seedProject(t, store, "scheduled")
	ctx := context.Background()
	now := time.Date(2026, time.August, 25, 9, 0, 0, 0, time.UTC)
	if _, err := store.CreateAutomation(ctx, domain.Automation{ID: "automation-1", ProjectID: "scheduled", DisplayName: "Triage", Prompt: "Review", Kind: domain.KindWorker, RRuleText: "FREQ=DAILY", Timezone: "UTC", Enabled: true, NextRunAt: now, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	runID := domain.AutomationRunID("run-1")
	if _, _, err := store.CreateAutomationRun(ctx, domain.AutomationRun{ID: runID, AutomationID: "automation-1", ScheduledFor: now, Status: domain.AutomationRunRunning, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	rec := sampleRecord("scheduled")
	rec.AutomationRunID = &runID
	session, err := store.CreateSession(ctx, rec)
	if err != nil {
		t.Fatal(err)
	}
	deleted, err := store.DeleteAutomation(ctx, "automation-1")
	if err != nil || !deleted {
		t.Fatalf("DeleteAutomation: deleted=%v err=%v", deleted, err)
	}
	got, ok, err := store.GetSession(ctx, session.ID)
	if err != nil || !ok || got.AutomationRunID != nil {
		t.Fatalf("surviving session = %#v ok=%v err=%v", got, ok, err)
	}
}
