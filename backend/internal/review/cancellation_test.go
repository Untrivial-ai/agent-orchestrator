package review

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

type cancellationLauncher struct {
	*fakeLauncher
	onCancel  func(context.Context) error
	onDestroy func(context.Context, string) error
}

func (l *cancellationLauncher) Cancel(ctx context.Context, handle string, harness domain.ReviewerHarness) error {
	if err := l.fakeLauncher.Cancel(ctx, handle, harness); err != nil {
		return err
	}
	if l.onCancel != nil {
		return l.onCancel(ctx)
	}
	return nil
}

func (l *cancellationLauncher) Destroy(ctx context.Context, handle string) error {
	if err := l.fakeLauncher.Destroy(ctx, handle); err != nil {
		return err
	}
	if l.onDestroy != nil {
		return l.onDestroy(ctx, handle)
	}
	return nil
}

type cancellationStore struct {
	*fakeStore
	finalizeErr error
}

func (s *cancellationStore) CancelReviewRunsAndClearHandle(ctx context.Context, id domain.SessionID, harness domain.ReviewerHarness, body string) (int64, error) {
	if s.finalizeErr != nil {
		return 0, s.finalizeErr
	}
	return s.fakeStore.CancelReviewRunsAndClearHandle(ctx, id, harness, body)
}

func runningCancellationStore() *fakeStore {
	return &fakeStore{
		review: &domain.Review{
			ID: "review-1", SessionID: "mer-1", Harness: domain.ReviewerCodex,
			ReviewerHandleID: "review-mer-1", AgentSessionID: "native-review-1",
		},
		runs: []domain.ReviewRun{{
			ID: "run-1", ReviewID: "review-1", SessionID: "mer-1", Harness: domain.ReviewerCodex,
			PRURL: "https://github.com/o/r/pull/1", TargetSHA: "sha1", Status: domain.ReviewRunRunning,
		}},
	}
}

func TestCancelFinishesAfterRequestDisconnects(t *testing.T) {
	store := runningCancellationStore()
	ctx, disconnect := context.WithCancel(context.Background())
	defer disconnect()
	launcher := &cancellationLauncher{fakeLauncher: &fakeLauncher{alive: true}}
	launcher.onCancel = func(ctx context.Context) error {
		disconnect()
		if err := ctx.Err(); err != nil {
			t.Fatalf("graceful cancellation inherited disconnected request: %v", err)
		}
		return errors.New("interrupt transport unavailable")
	}
	launcher.onDestroy = func(ctx context.Context, _ string) error {
		if err := ctx.Err(); err != nil {
			t.Fatalf("teardown inherited disconnected request: %v", err)
		}
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > reviewerTeardownBudget {
			t.Fatal("teardown must have a bounded cleanup context")
		}
		return nil
	}
	eng := newEngineForTest(store, fakeSessions{rec: liveWorker(), ok: true}, prAt("sha1"), fakeProjects{}, launcher)
	res, err := eng.Cancel(ctx, "mer-1")
	if err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if !launcher.destroyed || len(res.CancelledRuns) != 1 || store.review.ReviewerHandleID != "" {
		t.Fatalf("disconnected cancellation left unfinished teardown: result=%+v review=%+v", res, store.review)
	}
}

func TestCancelInterruptTimeoutStillAttemptsVerifiedTeardown(t *testing.T) {
	store := runningCancellationStore()
	launcher := &cancellationLauncher{fakeLauncher: &fakeLauncher{}}
	launcher.onCancel = func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	}
	launcher.onDestroy = func(ctx context.Context, _ string) error {
		return ctx.Err()
	}
	eng := newEngineForTest(store, fakeSessions{rec: liveWorker(), ok: true}, prAt("sha1"), fakeProjects{}, launcher)
	result, err := eng.Cancel(context.Background(), "mer-1")
	if err != nil || !launcher.destroyed || len(result.CancelledRuns) != 1 {
		t.Fatalf("interrupt timeout prevented cleanup: result=%+v err=%v destroyed=%v", result, err, launcher.destroyed)
	}
}

func TestTerminateReviewerPreservesCallerCleanupDeadline(t *testing.T) {
	store := runningCancellationStore()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	wantDeadline, _ := ctx.Deadline()
	launcher := &cancellationLauncher{fakeLauncher: &fakeLauncher{}}
	launcher.onDestroy = func(ctx context.Context, _ string) error {
		deadline, ok := ctx.Deadline()
		if !ok || !deadline.Equal(wantDeadline) {
			t.Fatalf("reviewer extended owning cleanup deadline: got %v, want %v", deadline, wantDeadline)
		}
		return ctx.Err()
	}
	eng := newEngineForTest(store, fakeSessions{rec: liveWorker(), ok: true}, prAt("sha1"), fakeProjects{}, launcher)
	if _, err := eng.TerminateReviewer(ctx, "mer-1", "worker cleanup"); err != nil {
		t.Fatalf("TerminateReviewer: %v", err)
	}
}

func TestTerminateReviewerStopsWhenCallerCleanupIsCancelled(t *testing.T) {
	store := runningCancellationStore()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	launcher := &cancellationLauncher{fakeLauncher: &fakeLauncher{}}
	launcher.onDestroy = func(ctx context.Context, _ string) error {
		cancel()
		return ctx.Err()
	}
	eng := newEngineForTest(store, fakeSessions{rec: liveWorker(), ok: true}, prAt("sha1"), fakeProjects{}, launcher)
	if _, err := eng.TerminateReviewer(ctx, "mer-1", "worker cleanup"); !errors.Is(err, context.Canceled) {
		t.Fatalf("TerminateReviewer error = %v, want owning cleanup cancellation", err)
	}
	if store.review.ReviewerHandleID != "review-mer-1" || store.runs[0].Status != domain.ReviewRunRunning {
		t.Fatalf("expired cleanup lost retry identity: review=%+v runs=%+v", store.review, store.runs)
	}
}

func TestReviewerStopDeadlineWhileTriggerOwnsWorker(t *testing.T) {
	for _, operation := range []string{"cancel", "terminate"} {
		t.Run(operation, func(t *testing.T) {
			spawnStarted := make(chan struct{})
			unblockSpawn := make(chan struct{})
			store := &fakeStore{}
			launcher := &fakeLauncher{handle: "review-mer-1", spawnStarted: spawnStarted, unblockSpawn: unblockSpawn}
			eng := newEngineForTest(store, fakeSessions{rec: liveWorker(), ok: true}, prAt("sha1"), fakeProjects{}, launcher)
			triggerDone := make(chan error, 1)
			go func() {
				_, err := eng.Trigger(context.Background(), "mer-1", domain.ReviewerCodex, domain.AgentConfig{})
				triggerDone <- err
			}()
			<-spawnStarted
			ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
			defer cancel()
			stopDone := make(chan error, 1)
			go func() {
				var err error
				if operation == "cancel" {
					_, err = eng.Cancel(ctx, "mer-1")
				} else {
					_, err = eng.TerminateReviewer(ctx, "mer-1", "worker cleanup")
				}
				stopDone <- err
			}()
			select {
			case err := <-stopDone:
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Errorf("stop error = %v, want caller deadline", err)
				}
			case <-time.After(time.Second):
				close(unblockSpawn)
				<-triggerDone
				<-stopDone
				t.Fatal("expired stop remained blocked behind trigger")
			}
			close(unblockSpawn)
			if err := <-triggerDone; err != nil {
				t.Fatalf("Trigger: %v", err)
			}
			if launcher.destroyed || launcher.cancelled || store.review.ReviewerHandleID != "review-mer-1" || store.runs[0].Status != domain.ReviewRunRunning {
				t.Fatalf("unaccepted stop mutated reviewer: review=%+v runs=%+v", store.review, store.runs)
			}
			if _, err := eng.TerminateReviewer(context.Background(), "mer-1", "retry cleanup"); err != nil {
				t.Fatalf("fresh teardown could not acquire worker after expired stop: %v", err)
			}
		})
	}
}

func TestReviewerStopRejectsAlreadyCancelledRequest(t *testing.T) {
	for _, operation := range []string{"cancel", "terminate"} {
		t.Run(operation, func(t *testing.T) {
			store := runningCancellationStore()
			launcher := &fakeLauncher{}
			eng := newEngineForTest(store, fakeSessions{rec: liveWorker(), ok: true}, prAt("sha1"), fakeProjects{}, launcher)
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			var err error
			if operation == "cancel" {
				_, err = eng.Cancel(ctx, "mer-1")
			} else {
				_, err = eng.TerminateReviewer(ctx, "mer-1", "worker cleanup")
			}
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("stop error = %v, want original cancellation", err)
			}
			if launcher.destroyed || launcher.cancelled || store.review.ReviewerHandleID != "review-mer-1" || store.runs[0].Status != domain.ReviewRunRunning {
				t.Fatal("already cancelled request started reviewer teardown")
			}
		})
	}
}

func TestCancelPersistenceFailureRetainsTeardownIdentityForRetry(t *testing.T) {
	writeErr := errors.New("database busy")
	store := &cancellationStore{fakeStore: runningCancellationStore(), finalizeErr: writeErr}
	launcher := &fakeLauncher{alive: true}
	eng := newEngineForTest(store, fakeSessions{rec: liveWorker(), ok: true}, prAt("sha1"), fakeProjects{}, launcher)
	if _, err := eng.Cancel(context.Background(), "mer-1"); !errors.Is(err, writeErr) {
		t.Fatalf("Cancel error = %v, want persistence failure", err)
	}
	if !launcher.destroyed || store.review.ReviewerHandleID != "review-mer-1" || store.runs[0].Status != domain.ReviewRunRunning {
		t.Fatalf("failed persistence lost retry evidence: review=%+v runs=%+v", store.review, store.runs)
	}
	store.finalizeErr = nil
	launcher.cancelErr = errors.New("pane already absent")
	res, err := eng.Cancel(context.Background(), "mer-1")
	if err != nil || len(res.CancelledRuns) != 1 || launcher.destroyCalls != 2 {
		t.Fatalf("retry result=%+v error=%v destroys=%d", res, err, launcher.destroyCalls)
	}
	if _, err := eng.Cancel(context.Background(), "mer-1"); err != nil || launcher.destroyCalls != 2 {
		t.Fatalf("repeated cancellation should be idle: error=%v destroys=%d", err, launcher.destroyCalls)
	}
}

func TestCancelPreservesOtherActiveHarnessAndStartsFreshOnNextTrigger(t *testing.T) {
	store := runningCancellationStore()
	store.reviews = map[domain.ReviewerHarness]domain.Review{
		domain.ReviewerCodex: *store.review,
		domain.ReviewerOpenCode: {
			ID: "review-other", SessionID: "mer-1", Harness: domain.ReviewerOpenCode,
			ReviewerHandleID: "other-pane", AgentSessionID: "native-other",
		},
	}
	other := domain.ReviewRun{
		ID: "other-run", ReviewID: "review-other", SessionID: "mer-1", Harness: domain.ReviewerOpenCode,
		PRURL: "https://github.com/o/r/pull/2", TargetSHA: "sha2", Status: domain.ReviewRunRunning,
	}
	store.runs = append(store.runs, other)
	worker := liveWorker()
	worker.ReviewerHarness = domain.ReviewerCodex
	launcher := &fakeLauncher{alive: true, handle: "replacement-pane"}
	eng := newEngineForTest(store, fakeSessions{rec: worker, ok: true}, prAt("sha1"), fakeProjects{}, launcher)
	res, err := eng.Cancel(context.Background(), "mer-1")
	if err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if launcher.destroyedHandle != "review-mer-1" || len(res.CancelledRuns) != 1 || res.CancelledRuns[0].ID != "run-1" {
		t.Fatalf("wrong reviewer stopped: result=%+v destroyed=%q", res, launcher.destroyedHandle)
	}
	if store.runs[1] != other || store.reviews[domain.ReviewerOpenCode].ReviewerHandleID != "other-pane" {
		t.Fatal("cancellation changed another active reviewer")
	}
	if store.reviews[domain.ReviewerCodex].AgentSessionID != "native-review-1" {
		t.Fatal("cancellation lost native review history")
	}
	resumed, err := eng.Trigger(context.Background(), "mer-1", "", domain.AgentConfig{})
	if err != nil {
		t.Fatalf("Trigger after cancellation: %v", err)
	}
	if !resumed.Created || !launcher.spawned || launcher.notified || resumed.Run.ID == "run-1" {
		t.Fatalf("next trigger did not start a fresh runtime and run: %+v", resumed)
	}
}

func TestCancelDoesNotReportResultCompletedDuringTeardownAsCancelled(t *testing.T) {
	store := runningCancellationStore()
	launcher := &cancellationLauncher{fakeLauncher: &fakeLauncher{}}
	launcher.onDestroy = func(ctx context.Context, _ string) error {
		_, err := store.UpdateReviewRunResult(ctx, "run-1", domain.ReviewRunComplete, domain.VerdictApproved, "finished", "123", true)
		return err
	}
	eng := newEngineForTest(store, fakeSessions{rec: liveWorker(), ok: true}, prAt("sha1"), fakeProjects{}, launcher)
	res, err := eng.Cancel(context.Background(), "mer-1")
	if err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if len(res.CancelledRuns) != 0 || store.runs[0].Status != domain.ReviewRunComplete || store.runs[0].GithubReviewID != "123" {
		t.Fatalf("completed result was relabelled: result=%+v run=%+v", res, store.runs[0])
	}
	if store.review.ReviewerHandleID != "" {
		t.Fatal("stopped reviewer handle was not cleared")
	}
}

func TestCancelWaitsForInFlightTriggerSpawn(t *testing.T) {
	spawnStarted := make(chan struct{})
	unblockSpawn := make(chan struct{})
	destroyCalled := make(chan string, 1)
	store := &fakeStore{}
	launcher := &fakeLauncher{
		handle: "review-mer-1", spawnStarted: spawnStarted,
		unblockSpawn: unblockSpawn, destroyCalled: destroyCalled,
	}
	eng := newEngineForTest(store, fakeSessions{rec: liveWorker(), ok: true}, prAt("sha1"), fakeProjects{}, launcher)
	triggerDone := make(chan error, 1)
	go func() {
		_, err := eng.Trigger(context.Background(), "mer-1", domain.ReviewerCodex, domain.AgentConfig{})
		triggerDone <- err
	}()
	<-spawnStarted
	cancelDone := make(chan error, 1)
	go func() {
		_, err := eng.Cancel(context.Background(), "mer-1")
		cancelDone <- err
	}()
	select {
	case err := <-cancelDone:
		close(unblockSpawn)
		<-triggerDone
		t.Fatalf("Cancel returned before spawn was recorded: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	close(unblockSpawn)
	if err := <-triggerDone; err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	if err := <-cancelDone; err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if handle := <-destroyCalled; handle != "review-mer-1" {
		t.Fatalf("destroyed handle = %q", handle)
	}
	if store.review.ReviewerHandleID != "" || len(store.runs) != 1 || store.runs[0].Status != domain.ReviewRunCancelled {
		t.Fatalf("cancellation missed concurrently spawned reviewer: review=%+v runs=%+v", store.review, store.runs)
	}
}

func TestTriggerDoesNotFinalizeSupersededReviewBeforeRetiredRuntimeStops(t *testing.T) {
	for _, teardownFails := range []bool{false, true} {
		name := "stopped"
		if teardownFails {
			name = "cleanup_failed"
		}
		t.Run(name, func(t *testing.T) {
			store := runningCancellationStore()
			worker := liveWorker()
			worker.ReviewerHarness = domain.ReviewerCodex
			worker.ReviewerConfig = domain.AgentConfig{Model: "model-original"}
			launcher := &cancellationLauncher{fakeLauncher: &fakeLauncher{alive: true, handle: "replacement-pane"}}
			launcher.onDestroy = func(_ context.Context, handle string) error {
				if handle != "review-mer-1" {
					return nil
				}
				if store.runs[0].Status != domain.ReviewRunRunning {
					t.Fatalf("retired reviewer was finalized before teardown: %+v", store.runs[0])
				}
				if teardownFails {
					return errors.New("retired reviewer still running")
				}
				return nil
			}
			eng := newEngineForTest(store, fakeSessions{rec: worker, ok: true}, prAt("sha2"), fakeProjects{}, launcher)
			_, err := eng.Trigger(context.Background(), "mer-1", domain.ReviewerCodex, domain.AgentConfig{Model: "model-replacement"})
			if teardownFails {
				if err == nil || store.runs[0].Status != domain.ReviewRunRunning || store.review.ReviewerHandleID != "review-mer-1" {
					t.Fatalf("failed retired runtime lost recovery state: err=%v review=%+v run=%+v", err, store.review, store.runs[0])
				}
			} else if err != nil || store.runs[0].Status != domain.ReviewRunFailed {
				t.Fatalf("confirmed retired runtime was not superseded: err=%v run=%+v", err, store.runs[0])
			}
		})
	}
}

func TestTriggerDoesNotAssumeMissingPaneProvesReviewerStopped(t *testing.T) {
	store := runningCancellationStore()
	worker := liveWorker()
	worker.ReviewerHarness = domain.ReviewerCodex
	launcher := &fakeLauncher{alive: false, destroyErr: errors.New("reviewer descendant still running")}
	eng := newEngineForTest(store, fakeSessions{rec: worker, ok: true}, prAt("sha2"), fakeProjects{}, launcher)
	if _, err := eng.Trigger(context.Background(), "mer-1", "", domain.AgentConfig{}); err == nil {
		t.Fatal("Trigger accepted a missing pane while reviewer cleanup was unresolved")
	}
	if launcher.spawned || launcher.notified || store.runs[0].Status != domain.ReviewRunRunning || store.review.ReviewerHandleID != "review-mer-1" {
		t.Fatalf("incomplete teardown was treated as stopped: review=%+v runs=%+v", store.review, store.runs)
	}
}
