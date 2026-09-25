package automation

import (
	"context"
	"sort"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type schedulerMemoryStore struct {
	*fakeStore
	runs     map[domain.AutomationRunID]domain.AutomationRun
	sessions map[domain.SessionID]domain.SessionRecord
}

func newSchedulerStore() *schedulerMemoryStore {
	return &schedulerMemoryStore{fakeStore: newFakeStore(), runs: map[domain.AutomationRunID]domain.AutomationRun{}, sessions: map[domain.SessionID]domain.SessionRecord{}}
}

func (f *schedulerMemoryStore) ListDueAutomations(_ context.Context, now time.Time, _ int64) ([]domain.Automation, error) {
	var out []domain.Automation
	for _, rec := range f.automations {
		if rec.Enabled && !rec.NextRunAt.After(now) {
			out = append(out, rec)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].NextRunAt.Before(out[j].NextRunAt) })
	return out, nil
}

func (f *schedulerMemoryStore) MaterializeAutomationRuns(_ context.Context, id domain.AutomationID, expected time.Time, runs []domain.AutomationRun, last, next, updated time.Time) (bool, error) {
	rec := f.automations[id]
	if !rec.Enabled || !rec.NextRunAt.Equal(expected) {
		return false, nil
	}
	for _, run := range runs {
		f.runs[run.ID] = run
	}
	rec.LastRunAt, rec.NextRunAt, rec.UpdatedAt = &last, next, updated
	f.automations[id] = rec
	return true, nil
}

func (f *schedulerMemoryStore) ClaimNextAutomationRun(_ context.Context, now, lease time.Time) (domain.AutomationRun, bool, error) {
	active := map[domain.AutomationID]bool{}
	for _, run := range f.runs {
		if run.Status == domain.AutomationRunSpawning || run.Status == domain.AutomationRunRunning {
			active[run.AutomationID] = true
		}
	}
	var candidates []domain.AutomationRun
	for _, run := range f.runs {
		if run.Status == domain.AutomationRunPending && f.automations[run.AutomationID].Enabled && !active[run.AutomationID] {
			candidates = append(candidates, run)
		}
	}
	if len(candidates) == 0 {
		return domain.AutomationRun{}, false, nil
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].ScheduledFor.Before(candidates[j].ScheduledFor) })
	run := candidates[0]
	run.Status, run.AttemptCount, run.ClaimedAt, run.LeaseExpiresAt, run.UpdatedAt = domain.AutomationRunSpawning, run.AttemptCount+1, &now, &lease, now
	f.runs[run.ID] = run
	return run, true, nil
}

func (f *schedulerMemoryStore) ListActiveAutomationRuns(context.Context) ([]domain.AutomationRun, error) {
	var out []domain.AutomationRun
	for _, run := range f.runs {
		if run.Status == domain.AutomationRunSpawning || run.Status == domain.AutomationRunRunning {
			out = append(out, run)
		}
	}
	return out, nil
}

func (f *schedulerMemoryStore) ListExpiredSpawningAutomationRuns(_ context.Context, now time.Time) ([]domain.AutomationRun, error) {
	var out []domain.AutomationRun
	for _, run := range f.runs {
		if run.Status == domain.AutomationRunSpawning && run.LeaseExpiresAt != nil && !run.LeaseExpiresAt.After(now) {
			out = append(out, run)
		}
	}
	return out, nil
}

func (f *schedulerMemoryStore) GetSessionByAutomationRunID(_ context.Context, id domain.AutomationRunID) (domain.SessionRecord, bool, error) {
	for _, session := range f.sessions {
		if session.AutomationRunID != nil && *session.AutomationRunID == id {
			return session, true, nil
		}
	}
	return domain.SessionRecord{}, false, nil
}

func (f *schedulerMemoryStore) GetSession(_ context.Context, id domain.SessionID) (domain.SessionRecord, bool, error) {
	rec, ok := f.sessions[id]
	return rec, ok, nil
}

func (f *schedulerMemoryStore) MarkAutomationRunRunning(_ context.Context, id domain.AutomationRunID, sessionID domain.SessionID, now time.Time) (bool, error) {
	run := f.runs[id]
	if run.Status != domain.AutomationRunSpawning {
		return false, nil
	}
	run.Status, run.SessionID, run.StartedAt, run.LeaseExpiresAt, run.UpdatedAt = domain.AutomationRunRunning, &sessionID, &now, nil, now
	f.runs[id] = run
	return true, nil
}

func (f *schedulerMemoryStore) CompleteAutomationRun(_ context.Context, id domain.AutomationRunID, now time.Time) (bool, error) {
	run := f.runs[id]
	if run.Status != domain.AutomationRunRunning {
		return false, nil
	}
	run.Status, run.FinishedAt, run.UpdatedAt = domain.AutomationRunCompleted, &now, now
	f.runs[id] = run
	return true, nil
}

func (f *schedulerMemoryStore) FailAutomationRun(_ context.Context, id domain.AutomationRunID, message string, now time.Time) (bool, error) {
	run := f.runs[id]
	run.Status, run.ErrorMessage, run.FinishedAt, run.UpdatedAt = domain.AutomationRunFailed, message, &now, now
	f.runs[id] = run
	return true, nil
}

func (f *schedulerMemoryStore) ReleaseAutomationRun(_ context.Context, id domain.AutomationRunID, message string, now time.Time) (bool, error) {
	run := f.runs[id]
	run.Status, run.ErrorMessage, run.ClaimedAt, run.LeaseExpiresAt, run.UpdatedAt = domain.AutomationRunPending, message, nil, nil, now
	f.runs[id] = run
	return true, nil
}

type recordingSpawner struct {
	store *schedulerMemoryStore
	calls []ports.SpawnConfig
	kills []domain.SessionID
}

func (s *recordingSpawner) Spawn(_ context.Context, cfg ports.SpawnConfig) (domain.Session, int, int, error) {
	s.calls = append(s.calls, cfg)
	id := domain.SessionID("scheduled-session-" + string(rune('1'+len(s.calls)-1)))
	rec := domain.SessionRecord{ID: id, ProjectID: cfg.ProjectID, AutomationRunID: cfg.AutomationRunID}
	s.store.sessions[id] = rec
	return domain.Session{SessionRecord: rec}, 0, 0, nil
}

func (s *recordingSpawner) Kill(_ context.Context, id domain.SessionID) (bool, error) {
	s.kills = append(s.kills, id)
	rec, ok := s.store.sessions[id]
	if ok {
		rec.IsTerminated = true
		s.store.sessions[id] = rec
	}
	return ok, nil
}

// Catch-up is capped at three durable rows but advances past the whole missed
// window, and only the oldest occurrence is dispatched for the definition.
func TestTickMaterializesBoundedCatchupAndDispatchesOldest(t *testing.T) {
	base := time.Date(2026, time.August, 25, 9, 0, 0, 0, time.UTC)
	now := base.Add(5 * time.Hour)
	store := newSchedulerStore()
	store.automations["automation-1"] = domain.Automation{ID: "automation-1", ProjectID: "scheduled", DisplayName: "Hourly", Prompt: "Review", Kind: domain.KindWorker, RRuleText: "DTSTART:20260825T090000Z\nRRULE:FREQ=HOURLY", Timezone: "UTC", Enabled: true, NextRunAt: base}
	spawner := &recordingSpawner{store: store}
	sequence := 0
	svc := New(Deps{Store: store, Spawner: spawner, Clock: func() time.Time { return now }, NewID: func() string { sequence++; return string(rune('a' + sequence - 1)) }})

	if err := svc.Tick(context.Background(), now); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if len(store.runs) != 3 || len(spawner.calls) != 1 {
		t.Fatalf("runs=%d spawns=%d, want 3 and 1", len(store.runs), len(spawner.calls))
	}
	if got := store.automations["automation-1"].NextRunAt; !got.Equal(base.Add(6 * time.Hour)) {
		t.Fatalf("next run = %s, want %s", got, base.Add(6*time.Hour))
	}
	if spawner.calls[0].AutomationRunID == nil || store.runs[*spawner.calls[0].AutomationRunID].ScheduledFor != base {
		t.Fatalf("spawn config = %#v, want oldest occurrence", spawner.calls[0])
	}
}

// Durable session termination, not a runtime probe, releases the non-overlap
// gate and allows the next queued occurrence to start.
func TestTickCompletesTerminatedSessionBeforeNextDispatch(t *testing.T) {
	now := time.Date(2026, time.August, 25, 12, 0, 0, 0, time.UTC)
	store := newSchedulerStore()
	store.automations["automation-1"] = domain.Automation{ID: "automation-1", ProjectID: "scheduled", Enabled: true, NextRunAt: now.Add(time.Hour)}
	runID, sessionID := domain.AutomationRunID("run-1"), domain.SessionID("session-1")
	store.runs[runID] = domain.AutomationRun{ID: runID, AutomationID: "automation-1", ScheduledFor: now.Add(-time.Hour), SessionID: &sessionID, Status: domain.AutomationRunRunning}
	store.runs["run-2"] = domain.AutomationRun{ID: "run-2", AutomationID: "automation-1", ScheduledFor: now, Status: domain.AutomationRunPending}
	store.sessions[sessionID] = domain.SessionRecord{ID: sessionID, AutomationRunID: &runID, IsTerminated: true}
	spawner := &recordingSpawner{store: store}
	svc := New(Deps{Store: store, Spawner: spawner, Clock: func() time.Time { return now }})

	if err := svc.Tick(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	if store.runs[runID].Status != domain.AutomationRunCompleted || store.runs["run-2"].Status != domain.AutomationRunRunning || len(spawner.calls) != 1 {
		t.Fatalf("runs=%#v calls=%d", store.runs, len(spawner.calls))
	}
}

// Boot reconciliation adopts a crash-surviving idempotent session instead of
// releasing the occurrence and spawning a duplicate.
func TestReconcileAdoptsSessionForExpiredClaim(t *testing.T) {
	now := time.Date(2026, time.August, 25, 12, 0, 0, 0, time.UTC)
	store := newSchedulerStore()
	store.automations["automation-1"] = domain.Automation{ID: "automation-1", Enabled: true, NextRunAt: now.Add(time.Hour)}
	runID, sessionID := domain.AutomationRunID("run-1"), domain.SessionID("session-1")
	lease := now.Add(-time.Minute)
	store.runs[runID] = domain.AutomationRun{ID: runID, AutomationID: "automation-1", Status: domain.AutomationRunSpawning, LeaseExpiresAt: &lease}
	store.sessions[sessionID] = domain.SessionRecord{ID: sessionID, AutomationRunID: &runID, AutomationLaunchCompleted: true}
	spawner := &recordingSpawner{store: store}
	svc := New(Deps{Store: store, Spawner: spawner, Clock: func() time.Time { return now }})

	if err := svc.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if store.runs[runID].Status != domain.AutomationRunRunning || store.runs[runID].SessionID == nil || *store.runs[runID].SessionID != sessionID || len(spawner.calls) != 0 {
		t.Fatalf("run=%#v calls=%d", store.runs[runID], len(spawner.calls))
	}
}

// A seed row is not a completed launch. Reconciliation must tear it down and
// fail the occurrence instead of adopting it or starting a duplicate runtime.
func TestReconcileRollsBackIncompleteAutomationSession(t *testing.T) {
	now := time.Date(2026, time.August, 25, 9, 0, 0, 0, time.UTC)
	store := newSchedulerStore()
	runID, sessionID := domain.AutomationRunID("run-1"), domain.SessionID("session-1")
	lease := now.Add(-time.Second)
	store.runs[runID] = domain.AutomationRun{ID: runID, AutomationID: "automation-1", Status: domain.AutomationRunSpawning, LeaseExpiresAt: &lease}
	store.sessions[sessionID] = domain.SessionRecord{ID: sessionID, AutomationRunID: &runID}
	spawner := &recordingSpawner{store: store}
	svc := New(Deps{Store: store, Spawner: spawner, Clock: func() time.Time { return now }})

	if err := svc.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(spawner.kills) != 1 || spawner.kills[0] != sessionID {
		t.Fatalf("killed sessions = %v, want [%s]", spawner.kills, sessionID)
	}
	if got := store.runs[runID].Status; got != domain.AutomationRunFailed {
		t.Fatalf("run status = %s, want failed", got)
	}
}

// A restart before the lease expires skips startup reconciliation, so regular
// ticks must release an expired claim that never produced a session instead of
// leaving the run stuck in spawning and blocking every later occurrence.
func TestTickReleasesExpiredSpawningClaimWithoutSession(t *testing.T) {
	now := time.Date(2026, time.August, 25, 12, 0, 0, 0, time.UTC)
	store := newSchedulerStore()
	runID := domain.AutomationRunID("run-1")
	lease := now.Add(-time.Minute)
	store.runs[runID] = domain.AutomationRun{ID: runID, AutomationID: "automation-1", Status: domain.AutomationRunSpawning, LeaseExpiresAt: &lease}
	spawner := &recordingSpawner{store: store}
	svc := New(Deps{Store: store, Spawner: spawner, Clock: func() time.Time { return now }})

	if err := svc.Tick(context.Background(), now); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	run := store.runs[runID]
	if run.Status != domain.AutomationRunPending || run.LeaseExpiresAt != nil || run.ClaimedAt != nil {
		t.Fatalf("run = %#v, want released to pending", run)
	}
	if len(spawner.calls) != 0 {
		t.Fatalf("spawns = %d, want 0", len(spawner.calls))
	}
}
