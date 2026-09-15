package review

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func TestTriggerCancelledWhileReviewerSpawns(t *testing.T) {
	spawnStarted := make(chan struct{})
	unblockSpawn := make(chan struct{})
	launcher := &fakeLauncher{
		handle: "review-mer-1", spawnStarted: spawnStarted, unblockSpawn: unblockSpawn,
	}
	store := &fakeStore{}
	eng := newEngineForTest(store, fakeSessions{rec: liveWorker(), ok: true}, prAt("sha1"), fakeProjects{}, launcher)
	var wg sync.WaitGroup
	var unblock sync.Once
	t.Cleanup(func() {
		unblock.Do(func() { close(unblockSpawn) })
		wg.Wait()
	})
	firstDone := make(chan error, 1)
	wg.Go(func() {
		_, err := eng.Trigger(context.Background(), "mer-1", "", domain.AgentConfig{})
		firstDone <- err
	})
	<-spawnStarted

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{})
	waiterDone := make(chan error, 1)
	wg.Go(func() {
		close(started)
		_, err := eng.Trigger(ctx, "mer-1", "", domain.AgentConfig{})
		waiterDone <- err
	})
	<-started
	cancel()
	select {
	case err := <-waiterDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled trigger error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled trigger remained blocked behind reviewer spawn")
	}

	unblock.Do(func() { close(unblockSpawn) })
	wg.Wait()
	if err := <-firstDone; err != nil {
		t.Fatalf("first trigger: %v", err)
	}
	if launcher.spawnCount != 1 || len(store.runs) != 1 {
		t.Fatalf("spawn count = %d, runs = %d, want one each", launcher.spawnCount, len(store.runs))
	}
	if _, err := eng.Trigger(context.Background(), "mer-1", "", domain.AgentConfig{}); err != nil {
		t.Fatalf("trigger after cancellation: %v", err)
	}
	if launcher.spawnCount != 1 {
		t.Fatalf("spawn count after retry = %d, want 1", launcher.spawnCount)
	}
	assertWorkerLocksReleased(t, eng)
}

func TestTriggerMissingWorkersReleaseLocks(t *testing.T) {
	eng := newEngineForTest(nil, fakeSessions{}, nil, nil, nil)
	for i := range 1000 {
		id := domain.SessionID(fmt.Sprintf("missing-%d", i))
		if _, err := eng.Trigger(context.Background(), id, "", domain.AgentConfig{}); !errors.Is(err, ErrNotFound) {
			t.Fatalf("trigger %q: %v, want ErrNotFound", id, err)
		}
	}
	assertWorkerLocksReleased(t, eng)
}

func TestWorkerOperationsCancelWhileWaiting(t *testing.T) {
	for _, tt := range []struct {
		name string
		call func(context.Context, *Engine) error
	}{
		{"trigger", func(ctx context.Context, eng *Engine) error {
			_, err := eng.Trigger(ctx, "worker", "", domain.AgentConfig{})
			return err
		}},
		{"switch", func(ctx context.Context, eng *Engine) error {
			_, err := eng.SwitchReviewer(ctx, "worker", "", domain.AgentConfig{})
			return err
		}},
		{"restore", func(ctx context.Context, eng *Engine) error {
			_, err := eng.RestoreReviewer(ctx, "worker")
			return err
		}},
		{"snapshot", func(ctx context.Context, eng *Engine) error {
			_, err := eng.SnapshotCodexReviewer(ctx, "worker")
			return err
		}},
		{"suspend", func(ctx context.Context, eng *Engine) error {
			_, err := eng.SuspendCodexReviewerExact(ctx, "worker", "handle", "native")
			return err
		}},
		{"restore exact", func(ctx context.Context, eng *Engine) error {
			return eng.RestoreCodexReviewerExact(ctx, "worker", "native")
		}},
		{"teardown", func(ctx context.Context, eng *Engine) error {
			return eng.TeardownReviewerTerminal(ctx, "worker")
		}},
		{"terminate", func(ctx context.Context, eng *Engine) error {
			_, err := eng.TerminateReviewer(ctx, "worker", "")
			return err
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			eng := New(Deps{})
			unlock, err := eng.lockWorker(context.Background(), "worker")
			if err != nil {
				t.Fatal(err)
			}
			defer unlock()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			waiting := &workerWaitContext{Context: ctx, waiting: make(chan struct{})}
			done := make(chan error, 1)
			go func() { done <- tt.call(waiting, eng) }()
			waitForWorkerWaiter(t, waiting)
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("cancelled operation: %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("operation did not stop waiting after cancellation")
			}
			eng.triggerMu.Lock()
			refs := eng.triggerLocks["worker"].refs
			eng.triggerMu.Unlock()
			if refs != 1 {
				t.Fatalf("references after cancellation = %d, want holder only", refs)
			}
			unlock()
			assertWorkerLocksReleased(t, eng)
		})
	}
}

func TestWorkerLockHandoffKeepsWaitersOnOneEntry(t *testing.T) {
	eng := New(Deps{})
	unlockFirst, err := eng.lockWorker(context.Background(), "worker")
	if err != nil {
		t.Fatal(err)
	}
	defer unlockFirst()
	eng.triggerMu.Lock()
	original := eng.triggerLocks["worker"]
	eng.triggerMu.Unlock()

	queue := func() <-chan func() {
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		waiting := &workerWaitContext{Context: ctx, waiting: make(chan struct{})}
		acquired := make(chan func(), 1)
		go func() {
			unlock, err := eng.lockWorker(waiting, "worker")
			if err == nil {
				acquired <- unlock
			}
		}()
		waitForWorkerWaiter(t, waiting)
		return acquired
	}
	second := queue()
	third := queue()
	unlockFirst()
	var unlockSecond func()
	select {
	case unlockSecond = <-second:
	case unlockSecond = <-third:
		third = second
	case <-time.After(time.Second):
		t.Fatal("lock was not handed to a waiter")
	}
	defer unlockSecond()
	fourth := queue()
	eng.triggerMu.Lock()
	current := eng.triggerLocks["worker"]
	refs := current.refs
	eng.triggerMu.Unlock()
	if current != original || refs != 3 {
		t.Fatalf("handoff replaced the shared entry or lost references: same=%v refs=%d", current == original, refs)
	}
	select {
	case unlock := <-fourth:
		unlock()
		t.Fatal("new caller acquired a split lock while a waiter held the original")
	default:
	}
	unlockSecond()
	for range 2 {
		select {
		case unlock := <-third:
			unlock()
		case unlock := <-fourth:
			unlock()
		case <-time.After(time.Second):
			t.Fatal("remaining waiter never acquired the lock")
		}
	}
	assertWorkerLocksReleased(t, eng)
}

func TestWorkerLocksAllowIndependentWorkers(t *testing.T) {
	eng := New(Deps{})
	unlockFirst, err := eng.lockWorker(context.Background(), "first")
	if err != nil {
		t.Fatal(err)
	}
	defer unlockFirst()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	unlockSecond, err := eng.lockWorker(ctx, "second")
	if err != nil {
		t.Fatalf("independent worker blocked: %v", err)
	}
	unlockSecond()
	unlockFirst()
	assertWorkerLocksReleased(t, eng)
}

func TestWorkerLockCancellationAndReleaseRace(t *testing.T) {
	eng := New(Deps{})
	var active atomic.Int32
	var overlap atomic.Bool
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			for range 100 {
				ctx, cancel := context.WithCancel(context.Background())
				cancelled := make(chan struct{})
				go func() {
					cancel()
					close(cancelled)
				}()
				unlock, err := eng.lockWorker(ctx, "worker")
				if err == nil {
					if active.Add(1) != 1 {
						overlap.Store(true)
					}
					active.Add(-1)
					unlock()
				} else if !errors.Is(err, context.Canceled) {
					t.Errorf("lock error = %v", err)
				}
				<-cancelled
			}
		})
	}
	wg.Wait()
	if overlap.Load() {
		t.Fatal("same-worker operations overlapped")
	}
	assertWorkerLocksReleased(t, eng)
}

func TestWorkerLockReturnsContextErrors(t *testing.T) {
	eng := New(Deps{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if unlock, err := eng.lockWorker(ctx, "worker"); unlock != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("already cancelled acquisition: unlock present=%v error=%v", unlock != nil, err)
	}
	assertWorkerLocksReleased(t, eng)
	unlock, err := eng.lockWorker(context.Background(), "worker")
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	ctx, cancel = context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if release, err := eng.lockWorker(ctx, "worker"); release != nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waiting past deadline: unlock present=%v error=%v", release != nil, err)
	}
	unlock()
	assertWorkerLocksReleased(t, eng)
}

// Done is evaluated after the waiter retains its entry and before acquisition.
type workerWaitContext struct {
	context.Context
	waiting chan struct{}
	once    sync.Once
}

func (ctx *workerWaitContext) Done() <-chan struct{} {
	ctx.once.Do(func() { close(ctx.waiting) })
	return ctx.Context.Done()
}

func waitForWorkerWaiter(t *testing.T, ctx *workerWaitContext) {
	t.Helper()
	select {
	case <-ctx.waiting:
	case <-time.After(time.Second):
		t.Fatal("caller did not reach worker lock acquisition")
	}
}

func assertWorkerLocksReleased(t *testing.T, eng *Engine) {
	t.Helper()
	eng.triggerMu.Lock()
	defer eng.triggerMu.Unlock()
	if got := len(eng.triggerLocks); got != 0 {
		t.Errorf("retained worker locks = %d, want 0", got)
	}
}
