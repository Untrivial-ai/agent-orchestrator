// Package reviewerupdate connects production reviewer launch/snapshot and update
// admission in runtime adapter tests. It never invokes a real installer or Codex.
package reviewerupdate

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/codexops"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/internal/review"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/systeminstall"
	sessionmanager "github.com/aoagents/agent-orchestrator/backend/internal/session_manager"
)

// Runtime is the production reviewer runtime boundary.
type Runtime interface {
	ports.Runtime
	Interrupt(context.Context, ports.RuntimeHandle) error
	SendInput(context.Context, ports.RuntimeHandle, string) error
	SendMessage(context.Context, ports.RuntimeHandle, string) error
	GetOutput(context.Context, ports.RuntimeHandle, int) (string, error)
}

// Fixture owns one launched reviewer and a fake installer behind real admission.
type Fixture struct {
	Engine    *review.Engine
	Launcher  review.Launcher
	Result    review.LaunchResult
	Service   *systeminstall.Service
	Gate      *codexops.Gate
	store     *reviewerStore
	installer *installer
}

type reviewerStore struct {
	review.Store
	row domain.Review
}

func (s *reviewerStore) GetReviewBySessionAndHarness(ctx context.Context, id domain.SessionID, harness domain.ReviewerHarness) (domain.Review, bool, error) {
	return s.row, id == s.row.SessionID && harness == s.row.Harness, ctx.Err()
}
func (s *reviewerStore) ListAllSessions(ctx context.Context) ([]domain.SessionRecord, error) {
	return []domain.SessionRecord{{ID: s.row.SessionID, Harness: domain.HarnessClaudeCode, ReviewerHarness: domain.ReviewerCodex}}, ctx.Err()
}

func (s *reviewerStore) ListReviewsBySession(ctx context.Context, _ domain.SessionID) ([]domain.Review, error) {
	return []domain.Review{s.row}, ctx.Err()
}
func (s *reviewerStore) ListRunningReviewRunsBySession(ctx context.Context, _ domain.SessionID) ([]domain.ReviewRun, error) {
	return nil, ctx.Err()
}
func (s *reviewerStore) ClearReviewerHandle(ctx context.Context, _ domain.SessionID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.row.ReviewerHandleID = ""
	return nil
}
func (s *reviewerStore) CancelRunningReviewRunsBySession(ctx context.Context, _ domain.SessionID, _ string) (int64, error) {
	return 0, ctx.Err()
}

type adapter struct{ binary string }

func (a adapter) Reviewer(domain.ReviewerHarness) (ports.Reviewer, bool) { return a, true }
func (a adapter) ReviewCommand(context.Context, ports.ReviewInvocation) (ports.ReviewCommandSpec, error) {
	return ports.ReviewCommandSpec{Argv: []string{a.binary}, AgentSessionID: "native-history"}, nil
}
func (a adapter) ReviewRestoreCommand(_ context.Context, inv ports.ReviewInvocation) (ports.ReviewCommandSpec, bool, error) {
	return ports.ReviewCommandSpec{Argv: []string{a.binary, "resume", inv.AgentSessionID}, AgentSessionID: inv.AgentSessionID, NativeResumed: true}, true, nil
}
func (adapter) ReviewMessage(context.Context, ports.ReviewInvocation) (string, error) {
	return "next review", nil
}

type installer struct {
	binary string
	ports.CommandRunner
	runs      atomic.Int32
	refreshed atomic.Bool
}

func (i *installer) Resolve(ctx context.Context) (ports.CodexInstallation, error) {
	version := "1.0.0"
	if i.runs.Load() > 0 {
		version = "1.1.0"
	}
	return ports.CodexInstallation{Path: i.binary, RealPath: i.binary, Version: version, Source: "npm", Scope: "npm:fixture", Fingerprint: version, VersionSource: "npm", Command: ports.InstallCommand{Argv: []string{"/fixture/npm", "update"}}}, ctx.Err()
}
func (*installer) Latest(ctx context.Context, _ ports.CodexInstallation) (string, error) {
	return "1.1.0", ctx.Err()
}
func (i *installer) RunInstall(ctx context.Context, _ ports.InstallCommand, _, _ io.Writer) error {
	i.runs.Add(1)
	return ctx.Err()
}

// New launches a fresh, resumed, or restored reviewer through the real launcher.
func New(ctx context.Context, t *testing.T, rt Runtime, mode, binary string) *Fixture {
	t.Helper()
	l := review.NewLauncher(adapter{binary: binary}, rt, t.TempDir())
	spec := review.LaunchSpec{WorkerID: "worker", Harness: domain.ReviewerCodex, WorkspacePath: t.TempDir(), BatchID: "batch", RunID: "run"}
	if mode != "fresh" {
		spec.AgentSessionID = "native-history"
		spec.RequireNativeHistory = true
	}
	var result review.LaunchResult
	var err error
	if mode == "restore" {
		result, err = l.RestoreTerminal(ctx, spec)
	} else {
		result, err = l.Spawn(ctx, spec)
	}
	if err != nil {
		t.Fatal(err)
	}
	if result.NativeResumed != (mode != "fresh") || result.AgentSessionID != "native-history" {
		t.Fatalf("launch identity/mode changed: %+v", result)
	}
	store := &reviewerStore{row: domain.Review{SessionID: "worker", Harness: domain.ReviewerCodex, ReviewerHandleID: result.HandleID, AgentSessionID: result.AgentSessionID}}
	engine := review.New(review.Deps{Store: store, Launcher: l})
	runner := &installer{binary: binary}
	gate := codexops.NewGate()
	service := systeminstall.NewWithDeps(nil, runner, systeminstall.Deps{ReviewerInput: sessionmanager.New(sessionmanager.Deps{DataDir: t.TempDir()}), Sessions: store, CodexReviewers: engine, CodexMaintenance: runner, CodexOperationGate: gate, RefreshCodex: func(ctx context.Context) error { runner.refreshed.Store(true); return ctx.Err() }})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := service.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	return &Fixture{Engine: engine, Launcher: l, Result: result, Service: service, Gate: gate, store: store, installer: runner}
}

// Blocked requires both the snapshot and updater to preserve live/unknown proof.
func (f *Fixture) Blocked(ctx context.Context, t *testing.T, wantErr error) {
	t.Helper()
	snapshot, err := f.Engine.SnapshotCodexReviewer(ctx, "worker")
	if !errors.Is(err, wantErr) || (wantErr == nil && !snapshot.Running && snapshot.HandleID == "") {
		t.Fatalf("snapshot=%+v, %v; want blocking %v", snapshot, err, wantErr)
	}
	if snapshot.HandleID != "" && (snapshot.HandleID != f.Result.HandleID || snapshot.NativeSessionID != "native-history") {
		t.Fatalf("snapshot lost identity: %+v", snapshot)
	}
	_, err = f.Service.StartCodexUpdate(ctx, "1.0.0")
	if wantErr == nil {
		wantErr = systeminstall.ErrHarnessActive
	}
	if !errors.Is(err, wantErr) || f.installer.runs.Load() != 0 {
		t.Fatalf("update admitted blocking reviewer: %v, runs=%d", err, f.installer.runs.Load())
	}
}

// Close exercises the existing explicit Kill review session lifecycle. It keeps
// native history and clears the recorded handle only after confirmed teardown.
func (f *Fixture) Close(ctx context.Context, t *testing.T) {
	t.Helper()
	if _, err := f.Engine.TerminateReviewer(ctx, "worker", "explicit user closure"); err != nil {
		t.Fatal(err)
	}
}

// Ready requires explicit terminal closure to permit and verify a fake update job.
func (f *Fixture) Ready(ctx context.Context, t *testing.T) {
	t.Helper()
	snapshot, err := f.Engine.SnapshotCodexReviewer(ctx, "worker")
	if err != nil || snapshot.Running {
		t.Fatalf("completed reviewer snapshot=%+v, %v", snapshot, err)
	}
	if snapshot.NativeSessionID != "native-history" || snapshot.HandleID != "" {
		t.Fatalf("closure lost history: %+v", snapshot)
	}
	if _, err = f.Service.StartCodexUpdate(ctx, "1.0.0"); err != nil {
		t.Fatal(err)
	}
	f.waitStatus(ctx, t, systeminstall.StatusSucceeded)
	if f.installer.runs.Load() != 1 || !f.installer.refreshed.Load() {
		t.Fatal("update omitted installer or fresh verification")
	}
}

// RecheckBlocked simulates a previously admitted launch finishing registration
// after the advisory; exclusive admission must reprobe the actual runtime.
func (f *Fixture) RecheckBlocked(ctx context.Context, t *testing.T, register func()) {
	t.Helper()
	release, err := f.Gate.AcquireShared(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	f.store.row.ReviewerHandleID = "" // launch has not registered its terminal yet
	if _, err = f.Service.StartCodexUpdate(ctx, "1.0.0"); err != nil {
		t.Fatal(err)
	}
	register()
	f.store.row.ReviewerHandleID = f.Result.HandleID
	release()
	job := f.waitStatus(ctx, t, systeminstall.StatusFailed)
	if f.installer.runs.Load() != 0 || !strings.Contains(job.Error, "reviewers") {
		t.Fatalf("recheck missed reviewer: %+v", job)
	}
}

func (f *Fixture) waitStatus(ctx context.Context, t *testing.T, want systeminstall.Status) systeminstall.Job {
	t.Helper()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		job, err := f.Service.Status(ctx, systeminstall.TargetCodex)
		if err != nil {
			t.Fatal(err)
		}
		if job.Status == want {
			return job
		}
		if job.Status == systeminstall.StatusFailed || job.Status == systeminstall.StatusInterrupted {
			t.Fatalf("update failed: %+v", job)
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-deadline.C:
			t.Fatalf("update did not finish: %+v", job)
		case <-ticker.C:
		}
	}
}
