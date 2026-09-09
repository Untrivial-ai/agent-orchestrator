package review

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type failedLaunchRuntime struct {
	*fakeRuntime
	disconnect        context.CancelFunc
	disconnectOnStart bool
	messageErr        error
	createErr         error
	cleanupErr        error
	cleanupCalls      int
	cleanupContextErr error
	cleanupBounded    bool
}

func (r *failedLaunchRuntime) Create(ctx context.Context, cfg ports.RuntimeConfig) (ports.RuntimeHandle, error) {
	handle, err := r.fakeRuntime.Create(ctx, cfg)
	if r.disconnectOnStart && r.disconnect != nil {
		r.disconnect()
	}
	if r.createErr != nil {
		return ports.RuntimeHandle{}, r.createErr
	}
	return handle, err
}

func (r *failedLaunchRuntime) SendMessage(context.Context, ports.RuntimeHandle, string) error {
	if r.disconnect != nil {
		r.disconnect()
	}
	return r.messageErr
}

func (r *failedLaunchRuntime) Destroy(ctx context.Context, handle ports.RuntimeHandle) error {
	if r.created {
		r.cleanupCalls++
		r.cleanupContextErr = ctx.Err()
		deadline, ok := ctx.Deadline()
		r.cleanupBounded = ok && time.Until(deadline) <= reviewerTeardownBudget
		if r.cleanupErr != nil {
			return r.cleanupErr
		}
	}
	return r.fakeRuntime.Destroy(ctx, handle)
}

func TestLauncherCleansCreatedRuntimeAfterInitialPromptFailure(t *testing.T) {
	for _, atReadiness := range []bool{false, true} {
		name := "initial_message"
		if atReadiness {
			name = "readiness"
		}
		t.Run(name, func(t *testing.T) {
			ctx, disconnect := context.WithCancel(context.Background())
			defer disconnect()
			messageErr := errors.New("initial message rejected")
			reviewer := &fakeReviewerWithLaunchSpec{spec: ports.ReviewCommandSpec{Argv: []string{"reviewer"}, InitialMessage: "review task"}}
			rt := &failedLaunchRuntime{fakeRuntime: &fakeRuntime{}, disconnect: disconnect, messageErr: messageErr, disconnectOnStart: atReadiness}
			wantErr := messageErr
			if atReadiness {
				reviewer.hints.InitialDelay = time.Hour
				wantErr = context.Canceled
			}
			launcher := newTestLauncher(t, reviewer, rt)
			result, err := launcher.Spawn(ctx, launchSpec())
			if !errors.Is(err, wantErr) {
				t.Fatalf("Spawn error = %v, want %v", err, wantErr)
			}
			if rt.cleanupCalls != 1 || rt.cleanupContextErr != nil || !rt.cleanupBounded || result.HandleID != "" {
				t.Fatalf("failed launch cleanup = calls:%d context:%v bounded:%v result:%+v", rt.cleanupCalls, rt.cleanupContextErr, rt.cleanupBounded, result)
			}
		})
	}
}

type possibleReviewerCreateError struct{ handle ports.RuntimeHandle }

func (possibleReviewerCreateError) Error() string { return "create response timed out" }
func (e possibleReviewerCreateError) PossibleHandle() ports.RuntimeHandle {
	return e.handle
}
func (possibleReviewerCreateError) EffectOutcome() ports.RuntimeEffectOutcome {
	return ports.RuntimeEffectPossible
}
func (possibleReviewerCreateError) CleanupOutcome() ports.RuntimeCleanupOutcome {
	return ports.RuntimeCleanupNotAttempted
}

func TestLauncherCleansPossibleRuntimeFromCreateError(t *testing.T) {
	for _, cleanupFails := range []bool{false, true} {
		name := "stopped"
		if cleanupFails {
			name = "cleanup_failed"
		}
		t.Run(name, func(t *testing.T) {
			createErr := possibleReviewerCreateError{handle: ports.RuntimeHandle{ID: "review-mer-1"}}
			rt := &failedLaunchRuntime{fakeRuntime: &fakeRuntime{}, createErr: createErr}
			if cleanupFails {
				rt.cleanupErr = errors.New("cleanup unconfirmed")
			}
			launcher := newTestLauncher(t, &fakeReviewer{}, rt)
			result, err := launcher.Spawn(context.Background(), launchSpec())
			if !errors.Is(err, createErr) || rt.cleanupCalls != 1 || rt.cleanupContextErr != nil || !rt.cleanupBounded {
				t.Fatalf("possible runtime cleanup = result:%+v err:%v calls:%d context:%v bounded:%v", result, err, rt.cleanupCalls, rt.cleanupContextErr, rt.cleanupBounded)
			}
			if cleanupFails {
				if !errors.Is(err, rt.cleanupErr) || result.HandleID != "review-mer-1" {
					t.Fatalf("unresolved possible handle was lost: result=%+v err=%v", result, err)
				}
			} else if result.HandleID != "" {
				t.Fatalf("confirmed cleanup retained a live handle: %+v", result)
			}
		})
	}
}

type launcherWithoutPreflight struct{ Launcher }

func (launcherWithoutPreflight) Preflight(context.Context, domain.ReviewerHarness, string) error {
	return nil
}

type launchContextStore struct {
	*fakeStore
	writeContextErrors []error
}

func (s *launchContextStore) UpsertReview(ctx context.Context, review domain.Review) error {
	s.writeContextErrors = append(s.writeContextErrors, ctx.Err())
	return s.fakeStore.UpsertReview(ctx, review)
}

func (s *launchContextStore) UpdateReviewRunResult(ctx context.Context, id string, status domain.ReviewRunStatus, verdict domain.ReviewVerdict, body, githubReviewID string, autoInject bool) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	return s.fakeStore.UpdateReviewRunResult(ctx, id, status, verdict, body, githubReviewID, autoInject)
}

func TestTriggerRecordsFailedReviewAfterDisconnectedLaunchWasCleanedUp(t *testing.T) {
	ctx, disconnect := context.WithCancel(context.Background())
	defer disconnect()
	messageErr := errors.New("initial message rejected")
	reviewer := &fakeReviewerWithLaunchSpec{spec: ports.ReviewCommandSpec{Argv: []string{"reviewer"}, InitialMessage: "review task"}}
	rt := &failedLaunchRuntime{fakeRuntime: &fakeRuntime{}, disconnect: disconnect, messageErr: messageErr}
	launcher := launcherWithoutPreflight{newTestLauncher(t, reviewer, rt)}
	store := &launchContextStore{fakeStore: &fakeStore{}}
	eng := newEngineForTest(store, fakeSessions{rec: liveWorker(), ok: true}, prAt("sha1"), fakeProjects{}, launcher)
	_, err := eng.Trigger(ctx, "mer-1", "", domain.AgentConfig{})
	if !errors.Is(err, messageErr) || errors.Is(err, context.Canceled) {
		t.Fatalf("Trigger failure was not recorded independently of request cancellation: %v", err)
	}
	if rt.cleanupCalls != 1 || len(store.runs) != 1 || store.runs[0].Status != domain.ReviewRunFailed {
		t.Fatalf("stopped failed reviewer still appears active: cleanup=%d runs=%+v", rt.cleanupCalls, store.runs)
	}
}

func TestTriggerRetainsFailedReviewerLaunchUntilVerifiedCleanup(t *testing.T) {
	ctx, disconnect := context.WithCancel(context.Background())
	defer disconnect()
	messageErr := errors.New("initial message rejected")
	cleanupErr := errors.New("reviewer descendant still running")
	reviewer := &fakeReviewerWithLaunchSpec{spec: ports.ReviewCommandSpec{Argv: []string{"reviewer"}, InitialMessage: "review task"}}
	rt := &failedLaunchRuntime{fakeRuntime: &fakeRuntime{}, disconnect: disconnect, messageErr: messageErr, cleanupErr: cleanupErr}
	launcher := launcherWithoutPreflight{newTestLauncher(t, reviewer, rt)}
	store := &launchContextStore{fakeStore: &fakeStore{}}
	worker := liveWorker()
	worker.ReviewerHarness = domain.ReviewerCodex
	eng := newEngineForTest(store, fakeSessions{rec: worker, ok: true}, prAt("sha1"), fakeProjects{}, launcher)
	result, err := eng.Trigger(ctx, "mer-1", "", domain.AgentConfig{})
	if !errors.Is(err, messageErr) || !errors.Is(err, cleanupErr) {
		t.Fatalf("Trigger error = %v, want launch and cleanup errors", err)
	}
	if result.ReviewerHandleID != "review-mer-1" || store.review == nil || store.review.ReviewerHandleID != result.ReviewerHandleID {
		t.Fatalf("incomplete cleanup lost runtime identity: result=%+v stored=%+v", result, store.review)
	}
	if len(store.runs) != 1 || store.runs[0].Status != domain.ReviewRunRunning {
		t.Fatalf("unresolved reviewer was finalized: %+v", store.runs)
	}
	for _, err := range store.writeContextErrors {
		if err != nil {
			t.Fatalf("reviewer cleanup persistence inherited disconnected request: %v", err)
		}
	}
	rt.cleanupErr = nil
	cancelled, err := eng.Cancel(context.Background(), "mer-1")
	if err != nil || len(cancelled.CancelledRuns) != 1 || store.review.ReviewerHandleID != "" || rt.cleanupCalls != 2 {
		t.Fatalf("cleanup retry result=%+v err=%v review=%+v cleanup calls=%d", cancelled, err, store.review, rt.cleanupCalls)
	}
}

func TestTriggerReportsFailureToRetainIncompleteReviewerCleanup(t *testing.T) {
	messageErr := errors.New("initial message rejected")
	cleanupErr := errors.New("reviewer descendant still running")
	writeErr := errors.New("cleanup identity write failed")
	reviewer := &fakeReviewerWithLaunchSpec{spec: ports.ReviewCommandSpec{Argv: []string{"reviewer"}, InitialMessage: "review task"}}
	rt := &failedLaunchRuntime{fakeRuntime: &fakeRuntime{}, messageErr: messageErr, cleanupErr: cleanupErr}
	launcher := launcherWithoutPreflight{newTestLauncher(t, reviewer, rt)}
	store := &fakeStore{upsertErr: writeErr, upsertErrCall: 2}
	eng := newEngineForTest(store, fakeSessions{rec: liveWorker(), ok: true}, prAt("sha1"), fakeProjects{}, launcher)
	result, err := eng.Trigger(context.Background(), "mer-1", "", domain.AgentConfig{})
	if !errors.Is(err, messageErr) || !errors.Is(err, cleanupErr) || !errors.Is(err, writeErr) || result.ReviewerHandleID != "review-mer-1" {
		t.Fatalf("combined launch failure lost evidence: result=%+v err=%v", result, err)
	}
	if len(store.runs) != 1 || store.runs[0].Status != domain.ReviewRunRunning {
		t.Fatalf("untracked reviewer was finalized despite cleanup failure: %+v", store.runs)
	}
}
