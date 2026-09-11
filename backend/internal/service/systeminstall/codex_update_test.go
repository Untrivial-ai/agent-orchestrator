package systeminstall

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/codexops"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	sessionmanager "github.com/aoagents/agent-orchestrator/backend/internal/session_manager"
)

type maintenanceFake struct {
	mu                    sync.Mutex
	installation          ports.CodexInstallation
	resolveErr, latestErr error
	latest                string
	reads                 int
}

func (f *maintenanceFake) Resolve(context.Context) (ports.CodexInstallation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	return f.installation, f.resolveErr
}
func (f *maintenanceFake) Latest(context.Context, ports.CodexInstallation) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.latest, f.latestErr
}

type updateRunner func(context.Context, ports.InstallCommand, io.Writer, io.Writer) error

func (f updateRunner) RunInstall(c context.Context, p ports.InstallCommand, o, e io.Writer) error {
	return f(c, p, o, e)
}

type codexReviewerProbe func(context.Context, domain.SessionID) (ports.CodexReviewerControllerSnapshot, error)

func (f codexReviewerProbe) SnapshotCodexReviewer(ctx context.Context, id domain.SessionID) (ports.CodexReviewerControllerSnapshot, error) {
	return f(ctx, id)
}

func updateFixture(t *testing.T) (*Service, *maintenanceFake) {
	t.Helper()
	f := &maintenanceFake{installation: ports.CodexInstallation{Path: "/selected/codex", RealPath: "/npm/codex.js", Version: "1.0.0", Source: "npm", Scope: "npm:/selected", Fingerprint: "old", VersionSource: "npm", Command: ports.InstallCommand{Argv: []string{"/owner/npm", "install", "-g", "--prefix", "/selected", "@openai/codex@latest"}}}, latest: "1.1.0"}
	s := newTestService("linux", "npm")
	s.codexMaintenance = f
	s.sessions = sessionListerStub{}
	s.codexOperationGate = codexops.NewGate()
	s.reviewerInput = sessionmanager.New(sessionmanager.Deps{DataDir: t.TempDir()})
	s.codexReviewers = codexReviewerProbe(func(context.Context, domain.SessionID) (ports.CodexReviewerControllerSnapshot, error) {
		return ports.CodexReviewerControllerSnapshot{}, nil
	})
	s.installerGate = make(chan struct{}, 1)
	s.jobStore = newInstallJobStoreFake()
	s.refreshCodex = func(context.Context) error { return nil }
	s.installCommands = updateRunner(func(_ context.Context, cmd ports.InstallCommand, out, stderr io.Writer) error {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.installation.Version = "1.1.0"
		f.installation.Fingerprint = "new"
		_, _ = io.WriteString(out, "updated exact prefix")
		return nil
	})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = s.Close(ctx)
	})
	return s, f
}
func startUpdate(t *testing.T, s *Service) {
	t.Helper()
	a, err := s.CodexUpdate(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	job, err := s.StartCodexUpdate(context.Background(), a.Token)
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != StatusQueued {
		t.Fatalf("initial %+v", job)
	}
}

func TestCodexAdvisoryCachedAndOfflineIndependent(t *testing.T) {
	s, f := updateFixture(t)
	a, err := s.CodexUpdate(context.Background(), false)
	if err != nil || !a.CanUpdate || !a.UpdateAvailable {
		t.Fatalf("%+v %v", a, err)
	}
	_, _ = s.CodexUpdate(context.Background(), false)
	f.mu.Lock()
	reads := f.reads
	f.latestErr = errors.New("offline")
	f.mu.Unlock()
	if reads != 1 {
		t.Fatal(reads)
	}
	a, err = s.CodexUpdate(context.Background(), true)
	if err != nil || !a.Stale || a.CanUpdate || a.AvailableVersion != "1.1.0" || !strings.Contains(a.Warning, "continue using") {
		t.Fatalf("%+v %v", a, err)
	}
	if len(s.jobs) != 0 {
		t.Fatal("version read started an installer")
	}
}

func TestCodexUpdateVerifiedFreshProvider(t *testing.T) {
	s, _ := updateFixture(t)
	var refreshed atomic.Bool
	s.refreshCodex = func(context.Context) error { refreshed.Store(true); return nil }
	startUpdate(t, s)
	waitForStatus(t, s, TargetCodex, StatusSucceeded)
	job, _ := s.Status(context.Background(), TargetCodex)
	if !refreshed.Load() || !strings.Contains(job.Output, "fresh Codex 1.1.0") || job.Method != "update:npm" {
		t.Fatalf("%+v", job)
	}
}

func TestCodexUpdateFailureOutcomes(t *testing.T) {
	for _, scenario := range []string{"nonzero", "timeout", "unchanged", "still-outdated", "missing", "unknown-version", "refresh", "changed-selection"} {
		t.Run(scenario, func(t *testing.T) {
			s, f := updateFixture(t)
			var refreshed atomic.Bool
			s.refreshCodex = func(context.Context) error {
				refreshed.Store(true)
				if scenario == "refresh" {
					return errors.New("model/list failed; cached models retained")
				}
				return nil
			}
			s.installTimeout = 30 * time.Millisecond
			s.installCommands = updateRunner(func(ctx context.Context, _ ports.InstallCommand, out, stderr io.Writer) error {
				if scenario == "timeout" {
					<-ctx.Done()
					return ctx.Err()
				}
				if scenario == "nonzero" {
					_, _ = io.WriteString(stderr, "EACCES permission denied")
					return errors.New("exit status 1")
				}
				f.mu.Lock()
				defer f.mu.Unlock()
				switch scenario {
				case "unchanged":
				case "still-outdated":
					f.installation.Version = "1.0.1"
				case "missing":
					f.resolveErr = errors.New("binary missing")
				case "unknown-version":
					f.installation.Version = ""
				case "changed-selection":
					f.installation.Path = "/other/codex"
					f.installation.Version = "1.1.0"
				default:
					f.installation.Version = "1.1.0"
				}
				return nil
			})
			startUpdate(t, s)
			waitForStatus(t, s, TargetCodex, StatusFailed)
			job, _ := s.Status(context.Background(), TargetCodex)
			if !refreshed.Load() || job.Error == "" {
				t.Fatalf("%+v refresh=%t", job, refreshed.Load())
			}
			if scenario == "nonzero" && !strings.Contains(job.Output, "EACCES") {
				t.Fatal(job.Output)
			}
		})
	}
}

func TestCodexQueueRechecksOwnershipAndSerializesInstallers(t *testing.T) {
	s, f := updateFixture(t)
	s.installerGate <- struct{}{} // another provider's installer owns the shared scope
	var runs atomic.Int32
	s.installCommands = updateRunner(func(context.Context, ports.InstallCommand, io.Writer, io.Writer) error { runs.Add(1); return nil })
	startUpdate(t, s)
	if _, err := s.StartCodexUpdate(context.Background(), "old"); !errors.Is(err, ErrInstallActive) {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.installation.Fingerprint = "new owner"
	f.mu.Unlock()
	s.releaseInstaller()
	waitForStatus(t, s, TargetCodex, StatusFailed)
	job, _ := s.Status(context.Background(), TargetCodex)
	if runs.Load() != 0 || !strings.Contains(job.Error, "installation changed") {
		t.Fatalf("%+v runs=%d", job, runs.Load())
	}
}

func TestCodexUpdateDoesNotTerminateOrRaceSessions(t *testing.T) {
	s, _ := updateFixture(t)
	s.sessions = sessionListerStub{sessions: []domain.SessionRecord{{Harness: domain.HarnessCodex, ID: "surviving-host"}}}
	a, err := s.CodexUpdate(context.Background(), false)
	if err != nil || a.RunningSessions != 1 {
		t.Fatal(a, err)
	}
	if _, err = s.StartCodexUpdate(context.Background(), a.Token); !errors.Is(err, ErrHarnessActive) {
		t.Fatal(err)
	}
	s.sessions = sessionListerStub{}
	release, err := s.codexOperationGate.AcquireShared(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	s.installTimeout = 30 * time.Millisecond
	startUpdate(t, s)
	waitForStatus(t, s, TargetCodex, StatusFailed)
	release()
	if len(s.jobs) != 1 {
		t.Fatal("unexpected jobs")
	}
}

func TestCodexUpdateBlocksLiveReviewersOnOtherHarnesses(t *testing.T) {
	for _, tc := range []struct {
		name       string
		preference domain.ReviewerHarness
		terminated bool
	}{
		{"selected Codex reviewer", domain.ReviewerCodex, false},
		{"project default reviewer", "", false},
		{"surviving reviewer after worker stop", domain.ReviewerClaudeCode, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := updateFixture(t)
			s.sessions = sessionListerStub{sessions: []domain.SessionRecord{{ID: "claude-worker", Harness: domain.HarnessClaudeCode, ReviewerHarness: tc.preference, IsTerminated: tc.terminated}}}
			var running atomic.Bool
			running.Store(true)
			s.codexReviewers = codexReviewerProbe(func(_ context.Context, id domain.SessionID) (ports.CodexReviewerControllerSnapshot, error) {
				if id != "claude-worker" {
					return ports.CodexReviewerControllerSnapshot{}, errors.New("wrong reviewer worker")
				}
				return ports.CodexReviewerControllerSnapshot{Running: running.Load()}, nil
			})
			a, err := s.CodexUpdate(context.Background(), false)
			if err != nil || a.RunningSessions != 1 {
				t.Fatalf("advisory = %+v, %v", a, err)
			}
			if _, err := s.StartCodexUpdate(context.Background(), a.Token); !errors.Is(err, ErrHarnessActive) {
				t.Fatalf("live reviewer admitted update: %v", err)
			}
			// Only an external explicit stop changes the liveness proof. A stored
			// Codex preference alone must not keep the update blocked afterward.
			running.Store(false)
			startUpdate(t, s)
			waitForStatus(t, s, TargetCodex, StatusSucceeded)
		})
	}
}

func TestCodexUpdateRechecksReviewerAfterLaunchRegistration(t *testing.T) {
	s, _ := updateFixture(t)
	s.sessions = sessionListerStub{sessions: []domain.SessionRecord{{ID: "claude-worker", Harness: domain.HarnessClaudeCode, ReviewerHarness: domain.ReviewerCodex}}}
	var running, installed atomic.Bool
	s.codexReviewers = codexReviewerProbe(func(context.Context, domain.SessionID) (ports.CodexReviewerControllerSnapshot, error) {
		return ports.CodexReviewerControllerSnapshot{Running: running.Load()}, nil
	})
	s.installCommands = updateRunner(func(context.Context, ports.InstallCommand, io.Writer, io.Writer) error {
		installed.Store(true)
		return nil
	})
	release, err := s.codexOperationGate.AcquireShared(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	startUpdate(t, s)
	// The reviewer launch registered after the advisory but before relinquishing
	// shared admission. The updater must drain admission before its final probe.
	running.Store(true)
	release()
	waitForStatus(t, s, TargetCodex, StatusFailed)
	job, _ := s.Status(context.Background(), TargetCodex)
	if installed.Load() || !strings.Contains(job.Error, "reviewers") {
		t.Fatalf("job = %+v, installer ran = %t", job, installed.Load())
	}
}

func TestCodexUpdateHoldsSharedLaunchInterlockUntilInstallerReturns(t *testing.T) {
	for _, fails := range []bool{false, true} {
		t.Run(fmt.Sprint(fails), func(t *testing.T) {
			s, _ := updateFixture(t)
			installer := s.installCommands
			var blocked, refreshed atomic.Bool
			s.installCommands = updateRunner(func(ctx context.Context, cmd ports.InstallCommand, out, stderr io.Writer) error {
				release, err := s.codexOperationGate.AcquireShared(ctx)
				if err == nil {
					release()
					return errors.New("reviewer launch admitted during replacement")
				}
				blocked.Store(true)
				if fails {
					return errors.New("installer failed")
				}
				return installer.RunInstall(ctx, cmd, out, stderr)
			})
			s.refreshCodex = func(ctx context.Context) error {
				// Fresh account/readiness clients need shared admission themselves.
				release, err := s.codexOperationGate.AcquireShared(ctx)
				if err != nil {
					return err
				}
				defer release()
				refreshed.Store(true)
				return nil
			}
			startUpdate(t, s)
			status := StatusSucceeded
			if fails {
				status = StatusFailed
			}
			waitForStatus(t, s, TargetCodex, status)
			if !blocked.Load() || !refreshed.Load() {
				t.Fatalf("launch blocked = %t, fresh verification admitted = %t", blocked.Load(), refreshed.Load())
			}
		})
	}
}

func TestCodexUpdateRejectsUnknownReviewerLiveness(t *testing.T) {
	s, _ := updateFixture(t)
	s.sessions = sessionListerStub{sessions: []domain.SessionRecord{{ID: "claude-worker", Harness: domain.HarnessClaudeCode}}}
	s.codexReviewers = codexReviewerProbe(func(context.Context, domain.SessionID) (ports.CodexReviewerControllerSnapshot, error) {
		return ports.CodexReviewerControllerSnapshot{}, errors.New("reviewer probe unavailable")
	})
	if _, err := s.StartCodexUpdate(context.Background(), "old"); err == nil || !strings.Contains(err.Error(), "reviewer probe unavailable") {
		t.Fatalf("unknown reviewer liveness admitted update: %v", err)
	}
}

func TestCodexUpdateShutdownWhileWaitingForReviewerLaunch(t *testing.T) {
	s, _ := updateFixture(t)
	release, err := s.codexOperationGate.AcquireShared(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	startUpdate(t, s)
	s.stop()
	waitForStatus(t, s, TargetCodex, StatusInterrupted)
	if s.codexOperationGate.ExclusivePendingOrHeld() {
		t.Fatal("shutdown left reviewer launch admission closed")
	}
}

func TestCodexUpdateWrongTokenAndManualOnly(t *testing.T) {
	s, f := updateFixture(t)
	if _, err := s.StartCodexUpdate(context.Background(), "client-controlled-command"); !errors.Is(err, ErrInstallationChanged) {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.installation.Command = ports.InstallCommand{}
	f.mu.Unlock()
	a, _ := s.CodexUpdate(context.Background(), true)
	if _, err := s.StartCodexUpdate(context.Background(), a.Token); !errors.Is(err, ErrInstallMethod) {
		t.Fatal(err)
	}
}

func TestCodexVersionComparison(t *testing.T) {
	for _, tt := range []struct {
		a, b string
		want bool
	}{{"1.10.0", "1.9.0", true}, {"1.1.0", "1.1.0-beta.1", true}, {"1.1.0-beta.2", "1.1.0", false}, {"garbage", "1.0.0", false}, {"1.0.0", "1.0.0", false}} {
		if got := newer(tt.a, tt.b); got != tt.want {
			t.Fatalf("%s > %s = %v", tt.a, tt.b, got)
		}
	}
}

func TestCodexForcedRefreshDoesNotReuseEarlierLookup(t *testing.T) {
	s, f := updateFixture(t)
	// A refresh requested during an earlier lookup must perform its own read
	// after that lookup completes; the earlier result may predate an update.
	call := make(chan struct{})
	s.advisoryCall = call
	result := make(chan CodexUpdateAdvisory, 1)
	ctx := &observedContext{Context: context.Background(), observed: make(chan struct{})}
	go func() {
		a, _ := s.CodexUpdate(ctx, true)
		result <- a
	}()
	<-ctx.observed
	s.advisoryMu.Lock()
	s.advisory = advisoryFor(f.installation, f.latest)
	s.advisoryUntil = time.Now().Add(time.Hour)
	f.mu.Lock()
	f.installation.Version = "1.1.0"
	f.mu.Unlock()
	s.advisoryCall = nil
	close(call)
	s.advisoryMu.Unlock()
	select {
	case a := <-result:
		if a.Version != "1.1.0" || a.UpdateAvailable {
			t.Fatalf("post-update refresh reused earlier advisory: %+v", a)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("refresh did not complete")
	}
}

func TestCodexVerificationRejectsRetargetingDuringModelRefresh(t *testing.T) {
	s, f := updateFixture(t)
	s.refreshCodex = func(context.Context) error {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.installation.Fingerprint = "externally-retargeted"
		return nil
	}
	startUpdate(t, s)
	waitForStatus(t, s, TargetCodex, StatusFailed)
	job, _ := s.Status(context.Background(), TargetCodex)
	if !strings.Contains(job.Error, "changed during provider verification") {
		t.Fatal(job)
	}
}

// observedContext signals when a caller begins waiting for an in-flight read.
type observedContext struct {
	context.Context
	observed chan struct{}
	once     sync.Once
}

func (c *observedContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.observed) })
	return c.Context.Done()
}

func TestCodexUpdateWorkerDerivesBothTimeoutsFromCaller(t *testing.T) {
	s, _ := updateFixture(t)
	type callerKey struct{}
	parent := context.WithValue(context.Background(), callerKey{}, "caller")
	s.installTimeout = 30 * time.Millisecond
	var refreshed bool
	s.installCommands = updateRunner(func(ctx context.Context, _ ports.InstallCommand, _, _ io.Writer) error {
		if ctx.Value(callerKey{}) != "caller" {
			t.Error("installer lost caller context")
		}
		<-ctx.Done()
		return ctx.Err()
	})
	s.refreshCodex = func(ctx context.Context) error {
		if ctx.Value(callerKey{}) != "caller" || ctx.Err() != nil {
			t.Errorf("verification did not derive fresh timeout from caller: %v", ctx.Err())
		}
		refreshed = true
		return nil
	}
	before, err := s.CodexUpdate(parent, false)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	job := &Job{Target: TargetCodex, Status: StatusQueued, Method: "update:npm", StartedAt: &now, UpdatedAt: &now}
	s.jobs[TargetCodex] = job
	s.runCodexUpdate(parent, job, before)
	if job.Status != StatusFailed || !refreshed {
		t.Fatalf("expired installer was not independently verified: %+v refreshed=%v", job, refreshed)
	}
}

func TestCodexUpdateRequiresReviewerInputAdmission(t *testing.T) {
	s, _ := updateFixture(t)
	s.reviewerInput = nil
	a, err := s.CodexUpdate(context.Background(), false)
	if err != nil || a.CanUpdate {
		t.Fatalf("missing raw-input fence left update enabled: %+v %v", a, err)
	}
}
