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

type adapter struct{}

func (adapter) Reviewer(domain.ReviewerHarness) (ports.Reviewer, bool) { return adapter{}, true }
func (adapter) ReviewCommand(context.Context, ports.ReviewInvocation) (ports.ReviewCommandSpec, error) {
	return ports.ReviewCommandSpec{Argv: []string{"/fixture/codex"}, AgentSessionID: "native-history"}, nil
}
func (adapter) ReviewRestoreCommand(_ context.Context, inv ports.ReviewInvocation) (ports.ReviewCommandSpec, bool, error) {
	return ports.ReviewCommandSpec{Argv: []string{"/fixture/codex", "resume", inv.AgentSessionID}, AgentSessionID: inv.AgentSessionID, NativeResumed: true}, true, nil
}
func (adapter) ReviewMessage(context.Context, ports.ReviewInvocation) (string, error) {
	return "next review", nil
}

type installer struct {
	ports.CommandRunner
	runs      atomic.Int32
	refreshed atomic.Bool
}

func (i *installer) Resolve(ctx context.Context) (ports.CodexInstallation, error) {
	version := "1.0.0"
	if i.runs.Load() > 0 {
		version = "1.1.0"
	}
	return ports.CodexInstallation{Path: "/fixture/codex", RealPath: "/fixture/codex", Version: version, Source: "npm", Scope: "npm:fixture", Fingerprint: version, VersionSource: "npm", Command: ports.InstallCommand{Argv: []string{"/fixture/npm", "update"}}}, ctx.Err()
}
func (*installer) Latest(ctx context.Context, _ ports.CodexInstallation) (string, error) {
	return "1.1.0", ctx.Err()
}
func (i *installer) RunInstall(ctx context.Context, _ ports.InstallCommand, _, _ io.Writer) error {
	i.runs.Add(1)
	return ctx.Err()
}

// New launches a fresh, resumed, or restored reviewer through the real launcher.
func New(ctx context.Context, t *testing.T, rt Runtime, mode string) *Fixture {
	t.Helper()
	l := review.NewLauncher(adapter{}, rt, t.TempDir())
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
	runner := &installer{}
	gate := codexops.NewGate()
	service := systeminstall.NewWithDeps(nil, runner, systeminstall.Deps{Sessions: store, CodexReviewers: engine, CodexMaintenance: runner, CodexOperationGate: gate, RefreshCodex: func(ctx context.Context) error { runner.refreshed.Store(true); return ctx.Err() }})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := service.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	return &Fixture{Engine: engine, Launcher: l, Result: result, Service: service, Gate: gate, installer: runner}
}

// Blocked requires both the snapshot and updater to preserve live/unknown proof.
func (f *Fixture) Blocked(ctx context.Context, t *testing.T, wantErr error) {
	t.Helper()
	snapshot, err := f.Engine.SnapshotCodexReviewer(ctx, "worker")
	if !errors.Is(err, wantErr) || (wantErr == nil && !snapshot.Running) {
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

// Ready requires confirmed completion to permit and verify a fake update job.
func (f *Fixture) Ready(ctx context.Context, t *testing.T) {
	t.Helper()
	snapshot, err := f.Engine.SnapshotCodexReviewer(ctx, "worker")
	if err != nil || snapshot.Running {
		t.Fatalf("completed reviewer snapshot=%+v, %v", snapshot, err)
	}
	if snapshot.NativeSessionID != "native-history" || snapshot.HandleID != f.Result.HandleID {
		t.Fatalf("completion lost history: %+v", snapshot)
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
	if _, err = f.Service.StartCodexUpdate(ctx, "1.0.0"); err != nil {
		t.Fatal(err)
	}
	register()
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
