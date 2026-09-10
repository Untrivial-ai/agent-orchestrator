package review

import (
	"context"
	"errors"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func TestReviewerStartsRespectPendingWorkerCleanup(t *testing.T) {
	starts := []struct {
		name    string
		rejects bool
		start   func(*Engine) error
	}{
		{"trigger", true, func(e *Engine) error {
			_, err := e.Trigger(context.Background(), "mer-1", "", domain.AgentConfig{})
			return err
		}},
		{"automatic_trigger", true, func(e *Engine) error {
			_, err := e.TriggerWithSource(context.Background(), "mer-1", "", domain.AgentConfig{}, domain.ReviewTriggerAuto)
			return err
		}},
		{"switch", true, func(e *Engine) error {
			_, err := e.SwitchReviewer(context.Background(), "mer-1", domain.ReviewerCodex, domain.AgentConfig{})
			return err
		}},
		{"restore", false, func(e *Engine) error {
			_, err := e.RestoreReviewer(context.Background(), "mer-1")
			return err
		}},
		{"restore_native", false, func(e *Engine) error {
			return e.RestoreCodexReviewer(context.Background(), "mer-1")
		}},
		{"restore_exact_native", false, func(e *Engine) error {
			return e.RestoreCodexReviewerExact(context.Background(), "mer-1", "native-review-1")
		}},
	}
	for _, stage := range []string{"cleanup_pending", "completion_unknown"} {
		for _, start := range starts {
			t.Run(stage+"/"+start.name, func(t *testing.T) {
				worker := liveWorker()
				worker.ReviewerHarness = domain.ReviewerCodex
				worker.Metadata.Startup = &domain.SessionStartup{ID: "startup-1", Stage: stage, Committed: true}
				store := runningCancellationStore()
				store.runs[0].Status = domain.ReviewRunComplete
				store.runs[0].Verdict = domain.VerdictApproved
				history := store.runs[0]
				launcher := &fakeLauncher{handle: "new-reviewer"}
				e := newEngineForTest(store, fakeSessions{rec: worker, ok: true}, prAt("new-head"), fakeProjects{}, launcher)
				// Model the gap after cleanup stops reviewers but before it removes
				// the worker workspace. The worker still has a workspace and is live.
				if _, err := e.TerminateReviewer(context.Background(), worker.ID, "worker cleanup"); err != nil {
					t.Fatalf("TerminateReviewer: %v", err)
				}
				err := start.start(e)
				if stage == "completion_unknown" {
					if err != nil || (!launcher.spawned && !launcher.restored) {
						t.Fatalf("committed worker could not start reviewer: err=%v spawned=%v restored=%v", err, launcher.spawned, launcher.restored)
					}
					return
				}
				if start.rejects && !errors.Is(err, ErrInvalid) {
					t.Fatalf("start error = %v, want pending cleanup rejection", err)
				}
				if !start.rejects && err != nil {
					t.Fatalf("restore should skip worker cleanup: %v", err)
				}
				if launcher.spawned || launcher.restored || launcher.notified {
					t.Fatal("reviewer started after worker cleanup stopped its previous runtime")
				}
				state, err := e.List(context.Background(), worker.ID)
				if err != nil || state.ReviewerHandleID != "" || len(state.Runs) != 1 || state.Runs[0] != history {
					t.Fatalf("cleanup guard changed completed history: state=%+v err=%v", state, err)
				}
				if store.review.AgentSessionID != "native-review-1" || len(store.reviewerConfigUpdates) != 0 {
					t.Fatalf("cleanup guard changed reviewer identity or selection: %+v", store)
				}
			})
		}
	}
}

func TestPendingWorkerCleanupPreservesIdleReviewerInspection(t *testing.T) {
	worker := liveWorker()
	worker.ReviewerHarness = domain.ReviewerCodex
	worker.Metadata.Startup = &domain.SessionStartup{Stage: "cleanup_pending"}
	store := runningCancellationStore()
	store.runs[0].Status = domain.ReviewRunComplete
	launcher := &fakeLauncher{alive: true}
	e := newEngineForTest(store, fakeSessions{rec: worker, ok: true}, prAt("sha1"), fakeProjects{}, launcher)
	state, err := e.List(context.Background(), worker.ID)
	if err != nil || state.ReviewerHandleID != "review-mer-1" || len(state.Runs) != 1 {
		t.Fatalf("idle reviewer inspection lost: state=%+v err=%v", state, err)
	}
	if _, err := e.RestoreReviewer(context.Background(), worker.ID); err != nil {
		t.Fatalf("RestoreReviewer: %v", err)
	}
	if launcher.destroyed || launcher.spawned || launcher.restored || store.review.ReviewerHandleID != "review-mer-1" {
		t.Fatal("inspection changed idle reviewer runtime")
	}
}
