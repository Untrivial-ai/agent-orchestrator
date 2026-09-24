package store_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
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
