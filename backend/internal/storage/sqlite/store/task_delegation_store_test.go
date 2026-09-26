package store_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/sqlitetest"
)

func TestTaskDelegationReservationReplaysCompletedWorker(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "delegation")
	worker, err := s.CreateSession(ctx, sampleRecord("delegation"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	rec := domain.TaskDelegation{
		IdempotencyKey:     "request-1",
		RequestFingerprint: domain.NewTaskDelegationRequestFingerprint([]byte("first request")),
		State:              domain.TaskDelegationPending,
		CreatedAt:          now,
		UpdatedAt:          now,
	}

	reserved, created, err := s.ReserveTaskDelegation(ctx, rec)
	if err != nil || !created || reserved.State != domain.TaskDelegationPending {
		t.Fatalf("reserve = %#v, created=%v, err=%v", reserved, created, err)
	}
	completed, err := s.CompleteTaskDelegation(ctx, rec.IdempotencyKey, rec.RequestFingerprint, worker.ID, now.Add(time.Second))
	if err != nil || completed.State != domain.TaskDelegationCompleted || completed.WorkerID != worker.ID {
		t.Fatalf("complete = %#v, err=%v", completed, err)
	}

	replayed, created, err := s.ReserveTaskDelegation(ctx, rec)
	if err != nil || created || replayed.WorkerID != worker.ID || replayed.State != domain.TaskDelegationCompleted {
		t.Fatalf("replay = %#v, created=%v, err=%v", replayed, created, err)
	}
	replayed, err = s.CompleteTaskDelegation(ctx, rec.IdempotencyKey, rec.RequestFingerprint, worker.ID, now.Add(2*time.Second))
	if err != nil || replayed.WorkerID != worker.ID {
		t.Fatalf("repeat complete = %#v, err=%v", replayed, err)
	}
}

func TestTaskDelegationReservationRejectsKeyReuse(t *testing.T) {
	s := newTestStore(t)
	now := time.Now().UTC().Truncate(time.Second)
	rec := domain.TaskDelegation{
		IdempotencyKey:     "request-1",
		RequestFingerprint: domain.NewTaskDelegationRequestFingerprint([]byte("first request")),
		State:              domain.TaskDelegationPending,
		CreatedAt:          now,
		UpdatedAt:          now,
	}
	if _, _, err := s.ReserveTaskDelegation(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	rec.RequestFingerprint = domain.NewTaskDelegationRequestFingerprint([]byte("different request"))
	if _, _, err := s.ReserveTaskDelegation(context.Background(), rec); !errors.Is(err, domain.ErrTaskDelegationIdempotencyConflict) {
		t.Fatalf("conflict error = %v", err)
	}
}

func TestTaskDelegationReservationHasSingleConcurrentWinner(t *testing.T) {
	s := newTestStore(t)
	now := time.Now().UTC().Truncate(time.Second)
	rec := domain.TaskDelegation{
		IdempotencyKey:     "request-concurrent",
		RequestFingerprint: domain.NewTaskDelegationRequestFingerprint([]byte("same request")),
		State:              domain.TaskDelegationPending,
		CreatedAt:          now,
		UpdatedAt:          now,
	}

	const callers = 8
	results := make(chan bool, callers)
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, created, err := s.ReserveTaskDelegation(context.Background(), rec)
			results <- created
			errs <- err
		}()
	}
	wg.Wait()
	close(results)
	close(errs)

	winners := 0
	for created := range results {
		if created {
			winners++
		}
	}
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent reserve: %v", err)
		}
	}
	if winners != 1 {
		t.Fatalf("reservation winners = %d, want 1", winners)
	}
}

func TestTaskDelegationSpawnIdentityIsAtomicAndRecoverable(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "delegation")
	seed := sampleRecord("delegation")
	now := seed.UpdatedAt
	reservation := domain.TaskDelegation{
		IdempotencyKey: "recoverable", RequestFingerprint: domain.NewTaskDelegationRequestFingerprint([]byte("task")),
		State: domain.TaskDelegationPending, CreatedAt: now, UpdatedAt: now,
	}
	if _, _, err := s.ReserveTaskDelegation(ctx, reservation); err != nil {
		t.Fatal(err)
	}
	// A retry after reserve but before spawn can claim the same operation.
	worker, created, err := s.CreateTaskDelegationSession(ctx, seed, reservation.IdempotencyKey, reservation.RequestFingerprint)
	if err != nil || !created {
		t.Fatalf("spawn: created=%v err=%v", created, err)
	}
	// A durable identity is not proof that startup finished.
	replayed, created, err := s.ReserveTaskDelegation(ctx, reservation)
	if err != nil || created || replayed.WorkerID != worker.ID || replayed.Ready() || replayed.StartupState != domain.TaskDelegationStartupSeeded {
		t.Fatalf("reservation replay=%+v created=%v err=%v", replayed, created, err)
	}
	var group sync.WaitGroup
	for range 8 {
		group.Go(func() {
			replay, created, err := s.CreateTaskDelegationSession(ctx, seed, reservation.IdempotencyKey, reservation.RequestFingerprint)
			if err != nil || created || replay.ID != worker.ID {
				t.Errorf("spawn replay=%+v created=%v err=%v", replay, created, err)
			}
		})
	}
	group.Wait()
	rows, err := s.ListSessions(ctx, "delegation")
	if err != nil || len(rows) != 1 {
		t.Fatalf("sessions=%d err=%v", len(rows), err)
	}
}

func TestTaskDelegationStartupHasOneDurableClaim(t *testing.T) {
	dataDir := t.TempDir()
	s := sqlitetest.MustOpenAt(t, dataDir)
	ctx := context.Background()
	seedProject(t, s, "startup")
	seed := sampleRecord("startup")
	reservation := domain.TaskDelegation{IdempotencyKey: "claim", RequestFingerprint: domain.NewTaskDelegationRequestFingerprint([]byte("task")),
		State: domain.TaskDelegationPending, CreatedAt: seed.UpdatedAt, UpdatedAt: seed.UpdatedAt}
	if _, _, err := s.ReserveTaskDelegation(ctx, reservation); err != nil {
		t.Fatal(err)
	}
	worker, _, err := s.CreateTaskDelegationSession(ctx, seed, reservation.IdempotencyKey, reservation.RequestFingerprint)
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := sqlite.OpenPreMigrated(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restarted.Close() })
	replay, _, err := restarted.CreateTaskDelegationSession(ctx, seed, reservation.IdempotencyKey, reservation.RequestFingerprint)
	if err != nil || replay.ID != worker.ID {
		t.Fatalf("restart lost seed: worker=%s err=%v", replay.ID, err)
	}
	results := make(chan bool, 8)
	var group sync.WaitGroup
	for i := range 8 {
		group.Go(func() {
			store := s
			if i%2 == 0 {
				store = restarted
			}
			claimed, err := store.ClaimTaskDelegationStartup(ctx, reservation.IdempotencyKey, reservation.RequestFingerprint, worker.ID)
			if !claimed && !errors.Is(err, domain.ErrTaskDelegationRecoveryRequired) || claimed && err != nil {
				t.Errorf("claim=%v err=%v", claimed, err)
			}
			results <- claimed
		})
	}
	group.Wait()
	close(results)
	winners := 0
	for claimed := range results {
		if claimed {
			winners++
		}
	}
	if winners != 1 {
		t.Fatalf("startup winners=%d", winners)
	}
	incomplete, _, err := restarted.ReserveTaskDelegation(ctx, reservation)
	if err != nil || incomplete.Ready() || incomplete.StartupState != domain.TaskDelegationStartupStarting {
		t.Fatalf("incomplete=%+v err=%v", incomplete, err)
	}
	if _, err := s.CompleteTaskDelegation(ctx, reservation.IdempotencyKey, reservation.RequestFingerprint, worker.ID, seed.UpdatedAt); err != nil {
		t.Fatal(err)
	}
	ready, _, err := restarted.ReserveTaskDelegation(ctx, reservation)
	if err != nil || !ready.Ready() {
		t.Fatalf("completed=%+v err=%v", ready, err)
	}
	if claimed, err := restarted.ClaimTaskDelegationStartup(ctx, reservation.IdempotencyKey, reservation.RequestFingerprint, worker.ID); err != nil || claimed {
		t.Fatalf("completed startup reran: claim=%v err=%v", claimed, err)
	}
}

func TestTaskDelegationConfirmedRollbackAllowsRetry(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "rollback")
	seed := sampleRecord("rollback")
	seed.Metadata = domain.SessionMetadata{}
	reservation := domain.TaskDelegation{IdempotencyKey: "rollback", RequestFingerprint: domain.NewTaskDelegationRequestFingerprint([]byte("task")),
		State: domain.TaskDelegationPending, CreatedAt: seed.UpdatedAt, UpdatedAt: seed.UpdatedAt}
	if _, _, err := s.ReserveTaskDelegation(ctx, reservation); err != nil {
		t.Fatal(err)
	}
	worker, _, err := s.CreateTaskDelegationSession(ctx, seed, reservation.IdempotencyKey, reservation.RequestFingerprint)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimTaskDelegationStartup(ctx, reservation.IdempotencyKey, reservation.RequestFingerprint, worker.ID); err != nil {
		t.Fatal(err)
	}
	if deleted, err := s.DeleteSession(ctx, worker.ID); err != nil || !deleted {
		t.Fatalf("rollback: deleted=%v err=%v", deleted, err)
	}
	if _, created, err := s.ReserveTaskDelegation(ctx, reservation); err != nil || !created {
		t.Fatalf("retry: created=%v err=%v", created, err)
	}
}

func TestTaskDelegationSpawnFailureLeavesReservationRetryable(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seed := sampleRecord("delegation")
	reservation := domain.TaskDelegation{
		IdempotencyKey: "retryable", RequestFingerprint: domain.NewTaskDelegationRequestFingerprint([]byte("task")),
		State: domain.TaskDelegationPending, CreatedAt: seed.UpdatedAt, UpdatedAt: seed.UpdatedAt,
	}
	if _, _, err := s.ReserveTaskDelegation(ctx, reservation); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.CreateTaskDelegationSession(ctx, seed, reservation.IdempotencyKey, reservation.RequestFingerprint); err == nil {
		t.Fatal("missing project must reject insert")
	}
	seedProject(t, s, "delegation")
	if _, created, err := s.CreateTaskDelegationSession(ctx, seed, reservation.IdempotencyKey, reservation.RequestFingerprint); err != nil || !created {
		t.Fatalf("retry: created=%v err=%v", created, err)
	}
}

func TestTaskDelegationLegacyPendingRequiresRecovery(t *testing.T) {
	dataDir := t.TempDir()
	s := sqlitetest.MustOpenAt(t, dataDir)
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dataDir, "ao.db")+"?_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	seedProject(t, s, "legacy")
	seed := sampleRecord("legacy")
	reservation := domain.TaskDelegation{
		IdempotencyKey: "legacy-pending", RequestFingerprint: domain.NewTaskDelegationRequestFingerprint([]byte("task")),
		State: domain.TaskDelegationPending, CreatedAt: seed.UpdatedAt, UpdatedAt: seed.UpdatedAt,
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO task_delegations
		(idempotency_key, request_fingerprint, state, created_at, updated_at) VALUES (?, ?, 'pending', ?, ?)`,
		reservation.IdempotencyKey, reservation.RequestFingerprint, reservation.CreatedAt, reservation.UpdatedAt); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.ReserveTaskDelegation(ctx, reservation); !errors.Is(err, domain.ErrTaskDelegationRecoveryRequired) {
		t.Fatalf("legacy reservation retry=%v", err)
	}
	if _, _, err := s.CreateTaskDelegationSession(ctx, seed, reservation.IdempotencyKey, reservation.RequestFingerprint); !errors.Is(err, domain.ErrTaskDelegationRecoveryRequired) {
		t.Fatalf("legacy spawn retry=%v", err)
	}
	rows, err := s.ListSessions(ctx, "legacy")
	if err != nil || len(rows) != 0 {
		t.Fatalf("legacy retry created sessions=%d err=%v", len(rows), err)
	}
}
