package systeminstall

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
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

func updateFixture(t *testing.T) (*Service, *maintenanceFake) {
	t.Helper()
	f := &maintenanceFake{installation: ports.CodexInstallation{Path: "/selected/codex", RealPath: "/npm/codex.js", Version: "1.0.0", Source: "npm", Scope: "npm:/selected", Fingerprint: "old", VersionSource: "npm", Command: ports.InstallCommand{Argv: []string{"/owner/npm", "install", "-g", "--prefix", "/selected", "@openai/codex@latest"}}}, latest: "1.1.0"}
	s := newTestService("linux", "npm")
	s.codexMaintenance = f
	s.sessions = sessionListerStub{}
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
	release, ok := s.TryBeginHarnessUse(domain.HarnessCodex)
	if !ok {
		t.Fatal("launch gate unavailable")
	}
	startUpdate(t, s)
	waitForStatus(t, s, TargetCodex, StatusFailed)
	release()
	if len(s.jobs) != 1 {
		t.Fatal("unexpected jobs")
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
