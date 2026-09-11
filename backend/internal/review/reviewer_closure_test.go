package review

import (
	"context"
	"errors"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type closureRuntime struct {
	reviewerChildRuntime
	terminalAlive                bool
	destroyErr, probeErr         error
	probeCancel                  context.CancelFunc
	destroyCalls, terminalProbes int
	closedHandle, probedHandle   ports.RuntimeHandle
}

func (r *closureRuntime) Destroy(_ context.Context, handle ports.RuntimeHandle) error {
	r.destroyCalls++
	r.closedHandle = handle
	return r.destroyErr
}
func (r *closureRuntime) IsAlive(_ context.Context, handle ports.RuntimeHandle) (bool, error) {
	r.terminalProbes++
	r.probedHandle = handle
	if r.probeCancel != nil {
		r.probeCancel()
	}
	return r.terminalAlive, r.probeErr
}

func TestExplicitReviewerClosureRequiresTerminalAbsence(t *testing.T) {
	for _, tc := range []struct {
		name                      string
		alive                     bool
		destroyErr, probeErr      error
		cancelBefore, cancelProbe bool
		wantErr                   error
	}{
		{name: "confirmed terminal absence"},
		{name: "confirmed tmux server absence", probeErr: ports.ErrRuntimeUnavailable},
		{name: "idle retained terminal", alive: true, wantErr: ports.ErrRuntimeProbeInconclusive},
		{name: "unknown teardown", destroyErr: ports.ErrRuntimeProbeInconclusive, wantErr: ports.ErrRuntimeProbeInconclusive},
		{name: "unknown post-close probe", probeErr: ports.ErrRuntimeProbeInconclusive, wantErr: ports.ErrRuntimeProbeInconclusive},
		{name: "timeout", probeErr: context.DeadlineExceeded, wantErr: context.DeadlineExceeded},
		{name: "canceled before close", cancelBefore: true, wantErr: context.Canceled},
		{name: "canceled during verification", cancelProbe: true, wantErr: context.Canceled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			rt := &closureRuntime{terminalAlive: tc.alive, destroyErr: tc.destroyErr, probeErr: tc.probeErr}
			if tc.cancelBefore {
				cancel()
			}
			if tc.cancelProbe {
				rt.probeCancel = cancel
			}
			const handleID = "ptyhost-v1:review-worker"
			store := &fakeStore{review: &domain.Review{SessionID: "worker", Harness: domain.ReviewerCodex, ReviewerHandleID: handleID, AgentSessionID: "native-history"}}
			engine := New(Deps{Store: store, Launcher: NewLauncher(fakeReviewerResolver{}, rt, t.TempDir())})
			_, err := engine.TerminateReviewer(ctx, "worker", "explicit user closure")
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("closure = %v, want %v", err, tc.wantErr)
			}
			wantHandle := ""
			if tc.wantErr != nil {
				wantHandle = handleID
			}
			if store.review.ReviewerHandleID != wantHandle || store.review.AgentSessionID != "native-history" {
				t.Fatalf("closure lost terminal/history identity: %+v", store.review)
			}
			if tc.cancelBefore && rt.destroyCalls != 0 {
				t.Fatal("canceled closure destroyed terminal")
			}
			if rt.destroyCalls > 0 && rt.closedHandle.ID != handleID {
				t.Fatal("closed wrong handle")
			}
			if rt.terminalProbes > 0 && rt.probedHandle.ID != handleID {
				t.Fatal("verified wrong handle")
			}
			if tc.wantErr != nil {
				// A failed close keeps blocking identity but releases the worker lock,
				// so a subsequent explicit close can retry without losing history.
				rt.terminalAlive, rt.destroyErr, rt.probeErr, rt.probeCancel = false, nil, nil, nil
				if _, err := engine.TerminateReviewer(context.Background(), "worker", "explicit retry"); err != nil {
					t.Fatal(err)
				}
				if store.review.ReviewerHandleID != "" || store.review.AgentSessionID != "native-history" {
					t.Fatal("retry did not preserve history")
				}
			}
		})
	}
}
