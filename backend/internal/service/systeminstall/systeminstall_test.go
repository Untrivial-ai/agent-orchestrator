package systeminstall

import (
	"context"
	"errors"
	"io"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type installJobStoreFake struct {
	mu                 sync.Mutex
	records            map[string]ports.AgentInstallJobRecord
	history            []ports.AgentInstallJobRecord
	upsertErr          error
	upsertErrForStatus map[string]error
	upsert             func(context.Context, ports.AgentInstallJobRecord) error
}

func newInstallJobStoreFake() *installJobStoreFake {
	return &installJobStoreFake{records: make(map[string]ports.AgentInstallJobRecord)}
}

func (s *installJobStoreFake) UpsertAgentInstallJob(ctx context.Context, record ports.AgentInstallJobRecord) error {
	if s.upsert != nil {
		return s.upsert(ctx, record)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.upsertErr != nil {
		return s.upsertErr
	}
	if err := s.upsertErrForStatus[record.Status]; err != nil {
		return err
	}
	s.records[record.Target] = record
	s.history = append(s.history, record)
	return nil
}

func TestAgentJobTransitionPersistenceIsBounded(t *testing.T) {
	store := newInstallJobStoreFake()
	store.upsert = func(ctx context.Context, _ ports.AgentInstallJobRecord) error {
		<-ctx.Done()
		return ctx.Err()
	}
	s := newTestService("darwin", "npm")
	s.jobStore = store
	s.persistenceTimeout = 20 * time.Millisecond
	now := time.Now().UTC()
	job := &Job{Target: TargetCodex, Status: StatusInstalling, StartedAt: &now, UpdatedAt: &now}

	started := time.Now()
	err := s.transitionAgentJob(job, StatusVerifying, "", "", "")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("transitionAgentJob error = %v, want deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > 250*time.Millisecond {
		t.Fatalf("transitionAgentJob blocked for %s", elapsed)
	}
}

func (s *installJobStoreFake) GetAgentInstallJob(_ context.Context, target string) (ports.AgentInstallJobRecord, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.records[target]
	return record, ok, nil
}

func (s *installJobStoreFake) ListAgentInstallJobs(context.Context) ([]ports.AgentInstallJobRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]ports.AgentInstallJobRecord, 0, len(s.records))
	for _, record := range s.records {
		out = append(out, record)
	}
	return out, nil
}

func (s *installJobStoreFake) InterruptActiveAgentInstallJobs(_ context.Context, interruptedAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for target, record := range s.records {
		if record.Status != string(StatusInstalling) && record.Status != string(StatusVerifying) {
			continue
		}
		record.Status = string(StatusInterrupted)
		record.Error = "AO restarted before this job completed."
		record.FinishedAt = &interruptedAt
		record.UpdatedAt = interruptedAt
		s.records[target] = record
	}
	return nil
}

type harnessVerifierFunc func(context.Context, Target) (VerifyResult, error)

func (f harnessVerifierFunc) Verify(ctx context.Context, target Target) (VerifyResult, error) {
	return f(ctx, target)
}

type sessionListerStub struct {
	sessions []domain.SessionRecord
	err      error
}

func (s sessionListerStub) ListAllSessions(context.Context) ([]domain.SessionRecord, error) {
	return s.sessions, s.err
}

type commandRunnerFunc func(context.Context, []string, io.Writer, io.Writer) error

func (f commandRunnerFunc) Run(ctx context.Context, argv []string, stdout, stderr io.Writer) error {
	return f(ctx, argv, stdout, stderr)
}

type installScriptRunnerFunc func(context.Context, ports.InstallScriptCommand, io.Writer, io.Writer) (ports.InstallScriptResult, error)

func (f installScriptRunnerFunc) RunInstallScript(ctx context.Context, command ports.InstallScriptCommand, stdout, stderr io.Writer) (ports.InstallScriptResult, error) {
	return f(ctx, command, stdout, stderr)
}

func testCommandRunner(command func(context.Context, []string) *exec.Cmd) commandRunnerFunc {
	return func(ctx context.Context, argv []string, stdout, stderr io.Writer) error {
		cmd := command(ctx, argv)
		cmd.Stdout = stdout
		cmd.Stderr = stderr
		return cmd.Run()
	}
}

// lookPathFound returns a lookPath fake that resolves only the names present
// in paths (defaulting each found name to "/usr/bin/<name>" when the map
// value is empty), and errors for everything else — mirroring the
// systemcheck test fake.
func lookPathFound(names ...string) func(string) (string, error) {
	set := make(map[string]bool, len(names))
	for _, n := range names {
		set[n] = true
	}
	return func(name string) (string, error) {
		if set[name] {
			return "/usr/bin/" + name, nil
		}
		return "", errors.New("exec: " + name + ": executable file not found in $PATH")
	}
}

func newTestService(goos string, found ...string) *Service {
	backgroundContext, stop := context.WithCancel(context.Background())
	return &Service{
		jobs:              make(map[Target]*Job),
		backgroundContext: backgroundContext,
		stop:              stop,
		executables:       executableFinderFunc(lookPathFound(found...)),
		installCapabilities: installCapabilitiesStub{
			prefix: "/Users/test/.npm", writable: true,
		},
		commands: testCommandRunner(func(ctx context.Context, argv []string) *exec.Cmd {
			return exec.CommandContext(ctx, argv[0], argv[1:]...) //nolint:gosec // test-only, deterministic argv
		}),
		goos:           goos,
		installTimeout: 2 * time.Second,
	}
}

func TestPlanFor(t *testing.T) {
	tests := []struct {
		name            string
		target          Target
		goos            string
		found           []string
		wantUnsupported bool
		wantReasonHas   string
		wantCommand     []string
	}{
		{
			name: "tmux windows is unsupported", target: TargetTmux, goos: "windows",
			wantUnsupported: true, wantReasonHas: "not required on Windows",
		},
		{
			name: "tmux darwin uses brew", target: TargetTmux, goos: "darwin", found: []string{"brew"},
			wantCommand: []string{"brew", "install", "tmux"},
		},
		{
			name: "tmux darwin without brew is unsupported", target: TargetTmux, goos: "darwin",
			wantUnsupported: true, wantReasonHas: "Homebrew was not found",
		},
		{
			name: "tmux linux apt-get is unsupported with instructions", target: TargetTmux, goos: "linux", found: []string{"apt-get", "dnf"},
			wantUnsupported: true, wantReasonHas: "administrator password",
		},
		{
			name: "tmux linux dnf is unsupported with instructions", target: TargetTmux, goos: "linux", found: []string{"dnf", "zypper"},
			wantUnsupported: true, wantReasonHas: "administrator password",
		},
		{
			name: "tmux linux pacman is unsupported with instructions", target: TargetTmux, goos: "linux", found: []string{"pacman"},
			wantUnsupported: true, wantReasonHas: "administrator password",
		},
		{
			name: "tmux linux zypper is unsupported with instructions", target: TargetTmux, goos: "linux", found: []string{"zypper"},
			wantUnsupported: true, wantReasonHas: "administrator password",
		},
		{
			name: "tmux linux no package manager is unsupported", target: TargetTmux, goos: "linux",
			wantUnsupported: true, wantReasonHas: "No supported Linux package manager",
		},
		// cloudflared is what makes a phone reachable from outside the local
		// network. Nothing installed it before, so a machine without it showed a
		// normal QR that only ever worked on Wi-Fi.
		{
			name: "cloudflared darwin uses brew", target: TargetCloudflared, goos: "darwin", found: []string{"brew"},
			wantCommand: []string{"brew", "install", "cloudflared"},
		},
		{
			name: "cloudflared darwin without brew is unsupported", target: TargetCloudflared, goos: "darwin",
			wantUnsupported: true, wantReasonHas: "Homebrew",
		},
		{
			name: "cloudflared windows uses winget", target: TargetCloudflared, goos: "windows", found: []string{"winget"},
			wantCommand: []string{"winget", "install", "-e", "--id", "Cloudflare.cloudflared"},
		},
		{
			name: "cloudflared windows without winget is unsupported", target: TargetCloudflared, goos: "windows",
			wantUnsupported: true, wantReasonHas: "winget",
		},
		// Same rule as every other Linux target: these need root, and AO never
		// asks for a password, so it hands over the command instead.
		{
			name: "cloudflared linux is unsupported with instructions", target: TargetCloudflared, goos: "linux",
			found: []string{"apt-get"}, wantUnsupported: true, wantReasonHas: "administrator password",
			wantCommand: []string{"apt-get", "install", "-y", "cloudflared"},
		},
		{
			name: "gh windows uses winget", target: TargetGH, goos: "windows", found: []string{"winget"},
			wantCommand: []string{"winget", "install", "-e", "--id", "GitHub.cli"},
		},
		{
			name: "gh windows without winget is unsupported", target: TargetGH, goos: "windows",
			wantUnsupported: true, wantReasonHas: "winget was not found",
		},
		{
			name: "gh darwin uses brew", target: TargetGH, goos: "darwin", found: []string{"brew"},
			wantCommand: []string{"brew", "install", "gh"},
		},
		{
			name: "gh linux apt-get is unsupported with instructions for the gh package", target: TargetGH, goos: "linux", found: []string{"apt-get"},
			wantUnsupported: true, wantReasonHas: "administrator password",
		},
		{
			name: "gh linux pacman is unsupported with instructions for the github-cli package", target: TargetGH, goos: "linux", found: []string{"pacman"},
			wantUnsupported: true, wantReasonHas: "administrator password",
		},
		{
			name: "claude uses npm on every platform", target: TargetClaude, goos: "darwin", found: []string{"npm"},
			wantCommand: []string{"npm", "install", "-g", "@anthropic-ai/claude-code"},
		},
		{
			name: "codex without npm is unsupported", target: TargetCodex, goos: "linux",
			wantUnsupported: true, wantReasonHas: "npm was not found",
		},
		{
			name: "copilot uses npm", target: TargetCopilot, goos: "windows", found: []string{"npm"},
			wantCommand: []string{"npm", "install", "-g", "@github/copilot"},
		},
		{
			name: "opencode windows uses winget", target: TargetOpencode, goos: "windows", found: []string{"winget"},
			wantCommand: []string{"winget", "install", "-e", "--id", "SST.opencode", "--silent", "--accept-package-agreements", "--accept-source-agreements", "--disable-interactivity"},
		},
		{
			name: "opencode darwin remote script is manual", target: TargetOpencode, goos: "darwin", found: []string{"curl", "bash"},
			wantUnsupported: true, wantReasonHas: "does not automatically execute",
		},
		{
			name: "opencode without curl remains manual", target: TargetOpencode, goos: "linux", found: []string{"bash"},
			wantUnsupported: true, wantReasonHas: "does not automatically execute",
		},
		{
			name: "opencode with sh remains manual", target: TargetOpencode, goos: "linux", found: []string{"curl", "sh"},
			wantUnsupported: true, wantReasonHas: "does not automatically execute",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newTestService(tt.goos, tt.found...)
			plan := s.planFor(tt.target)
			if plan.Target != tt.target {
				t.Fatalf("Target = %q, want %q", plan.Target, tt.target)
			}
			if plan.Unsupported != tt.wantUnsupported {
				t.Fatalf("Unsupported = %v, want %v (reason=%q)", plan.Unsupported, tt.wantUnsupported, plan.Reason)
			}
			if tt.wantReasonHas != "" && !strings.Contains(plan.Reason, tt.wantReasonHas) {
				t.Fatalf("Reason = %q, want substring %q", plan.Reason, tt.wantReasonHas)
			}
			if tt.wantCommand != nil {
				if strings.Join(plan.Command, " ") != strings.Join(tt.wantCommand, " ") {
					t.Fatalf("Command = %v, want %v", plan.Command, tt.wantCommand)
				}
			}
		})
	}
}

func TestValid(t *testing.T) {
	for _, target := range []Target{TargetTmux, TargetGH, TargetClaude, TargetCodex, TargetOpencode, TargetCopilot} {
		if !Valid(target) {
			t.Errorf("Valid(%q) = false, want true", target)
		}
	}
	for _, target := range []Target{"", "rm -rf /", "../../etc/passwd", "TMUX", "tmux "} {
		if Valid(target) {
			t.Errorf("Valid(%q) = true, want false", target)
		}
	}
}

func TestStartAndStatus_Succeeded(t *testing.T) {
	s := newTestService("darwin", "brew", "tmux")
	s.commands = testCommandRunner(func(context.Context, []string) *exec.Cmd { return exec.Command("true") })

	job, err := s.Start(context.Background(), TargetTmux)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if job.Status != StatusRunning {
		t.Fatalf("Status = %q, want %q", job.Status, StatusRunning)
	}
	if job.Command != "brew install tmux" {
		t.Fatalf("Command = %q, want %q", job.Command, "brew install tmux")
	}

	waitForStatus(t, s, TargetTmux, StatusSucceeded)

	final, err := s.Status(context.Background(), TargetTmux)
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if final.Error != "" {
		t.Fatalf("Error = %q, want empty", final.Error)
	}
	if final.FinishedAt == nil {
		t.Fatalf("FinishedAt is nil, want set")
	}
}

func TestStart_SuccessCallbackRunsAfterVerifiedInstall(t *testing.T) {
	s := newTestService("darwin", "npm", "codex")
	s.commands = testCommandRunner(func(context.Context, []string) *exec.Cmd { return exec.Command("true") })
	s.verifier = harnessVerifierFunc(func(context.Context, Target) (VerifyResult, error) {
		return VerifyResult{ResolvedPath: "/Users/test/.npm/bin/codex"}, nil
	})
	succeeded := make(chan Target, 1)
	s.SetOnSucceeded(func(target Target) { succeeded <- target })

	if _, err := s.Start(context.Background(), TargetCodex); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	waitForStatus(t, s, TargetCodex, StatusSucceeded)

	select {
	case target := <-succeeded:
		if target != TargetCodex {
			t.Fatalf("callback target = %q, want %q", target, TargetCodex)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for success callback")
	}
}

func TestStart_FailedInstallDoesNotRunSuccessCallback(t *testing.T) {
	s := newTestService("darwin", "npm")
	s.commands = testCommandRunner(func(context.Context, []string) *exec.Cmd { return exec.Command("false") })
	called := make(chan Target, 1)
	s.SetOnSucceeded(func(target Target) { called <- target })

	if _, err := s.Start(context.Background(), TargetCodex); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	waitForStatus(t, s, TargetCodex, StatusFailed)

	select {
	case target := <-called:
		t.Fatalf("success callback ran for failed target %q", target)
	case <-time.After(20 * time.Millisecond):
	}
}

func TestStart_ExitZeroWithoutTargetOnPATHFails(t *testing.T) {
	s := newTestService("darwin", "brew")
	s.commands = testCommandRunner(func(context.Context, []string) *exec.Cmd { return exec.Command("true") })

	if _, err := s.Start(context.Background(), TargetTmux); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	waitForStatus(t, s, TargetTmux, StatusFailed)

	final, err := s.Status(context.Background(), TargetTmux)
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if !strings.Contains(final.Error, "tmux is still not in PATH") {
		t.Fatalf("Error = %q, want failed PATH verification", final.Error)
	}
}

func TestStartAndStatus_Failed(t *testing.T) {
	s := newTestService("darwin", "brew")
	s.commands = testCommandRunner(func(context.Context, []string) *exec.Cmd { return exec.Command("false") })

	if _, err := s.Start(context.Background(), TargetTmux); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	waitForStatus(t, s, TargetTmux, StatusFailed)

	final, _ := s.Status(context.Background(), TargetTmux)
	if final.Error == "" {
		t.Fatalf("Error is empty, want the exec failure")
	}
}

func TestStart_Unsupported(t *testing.T) {
	s := newTestService("windows") // no winget on PATH

	job, err := s.Start(context.Background(), TargetTmux)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if job.Status != StatusUnsupported {
		t.Fatalf("Status = %q, want %q", job.Status, StatusUnsupported)
	}
	if job.Error == "" {
		t.Fatalf("Error is empty, want the Unsupported reason")
	}
	if job.FinishedAt == nil {
		t.Fatalf("FinishedAt is nil, want set immediately for an Unsupported job")
	}
}

func TestStart_UnknownTarget(t *testing.T) {
	s := newTestService("darwin")
	if _, err := s.Start(context.Background(), Target("bogus")); err == nil {
		t.Fatalf("Start(bogus) error = nil, want an error")
	}
}

func TestStatus_UnknownTarget(t *testing.T) {
	s := newTestService("darwin")
	if _, err := s.Status(context.Background(), Target("bogus")); err == nil {
		t.Fatalf("Status(bogus) error = nil, want an error")
	}
}

func TestStatus_NeverStartedIsIdle(t *testing.T) {
	s := newTestService("darwin", "brew")
	job, err := s.Status(context.Background(), TargetGH)
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if job.Status != StatusIdle {
		t.Fatalf("Status = %q, want %q", job.Status, StatusIdle)
	}
	if job.Target != TargetGH {
		t.Fatalf("Target = %q, want %q", job.Target, TargetGH)
	}
	if job.Command != "brew install gh" {
		t.Fatalf("Command = %q, want install preview", job.Command)
	}
}

func TestStatus_LinuxReturnsManualCommandBeforeStart(t *testing.T) {
	s := newTestService("linux", "apt-get")
	job, err := s.Status(context.Background(), TargetTmux)
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if job.Status != StatusUnsupported {
		t.Fatalf("Status = %q, want %q", job.Status, StatusUnsupported)
	}
	if job.Command != "sudo apt-get install -y tmux" {
		t.Fatalf("Command = %q, want exact sudo command", job.Command)
	}
}

// TestStart_IdempotentWhileRunning gates the fake install on a channel so the
// test controls exactly when it finishes, then fires two concurrent Starts
// and confirms neither one starts a second run.
func TestStart_IdempotentWhileRunning(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{}, 2)

	s := newTestService("darwin", "brew", "tmux")
	callCount := 0
	s.commands = testCommandRunner(func(context.Context, []string) *exec.Cmd {
		callCount++
		started <- struct{}{}
		<-release
		return exec.Command("true")
	})

	first, err := s.Start(context.Background(), TargetTmux)
	if err != nil {
		t.Fatalf("first Start() error = %v", err)
	}
	if first.Status != StatusRunning {
		t.Fatalf("first Status = %q, want %q", first.Status, StatusRunning)
	}

	<-started // the background goroutine has begun (and is blocked on release)

	second, err := s.Start(context.Background(), TargetTmux)
	if err != nil {
		t.Fatalf("second Start() error = %v", err)
	}
	if second.Status != StatusRunning {
		t.Fatalf("second Status = %q, want %q", second.Status, StatusRunning)
	}

	close(release)
	waitForStatus(t, s, TargetTmux, StatusSucceeded)

	if callCount != 1 {
		t.Fatalf("command runner called %d times, want 1 (Start must be idempotent while running)", callCount)
	}
}

// TestRun_Timeout confirms a stalled installer eventually surfaces as a
// failure instead of pinning its target in StatusRunning forever. The fake
// command actually respects ctx (exec.CommandContext, same as real installs),
// so the short installTimeout below kills it well before the real 5s sleep
// would return on its own.
func TestRun_Timeout(t *testing.T) {
	s := newTestService("darwin", "brew")
	s.installTimeout = 50 * time.Millisecond
	s.commands = testCommandRunner(func(ctx context.Context, _ []string) *exec.Cmd {
		return exec.CommandContext(ctx, "sleep", "5") //nolint:gosec // test-only, fixed argv
	})

	if _, err := s.Start(context.Background(), TargetTmux); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	waitForStatus(t, s, TargetTmux, StatusFailed)

	final, _ := s.Status(context.Background(), TargetTmux)
	if !strings.Contains(final.Error, "timed out") {
		t.Fatalf("Error = %q, want it to mention the timeout", final.Error)
	}
	if final.FinishedAt == nil {
		t.Fatalf("FinishedAt is nil, want set")
	}
}

func TestAgentInstallPersistsInstallVerifySuccessLifecycle(t *testing.T) {
	store := newInstallJobStoreFake()
	s := newTestService("darwin", "npm")
	s.installCapabilities = installCapabilitiesStub{prefix: "/Users/test/.npm", writable: true}
	s.jobStore = store
	s.commands = commandRunnerFunc(func(_ context.Context, _ []string, stdout, _ io.Writer) error {
		_, _ = io.WriteString(stdout, "installed\n")
		return nil
	})
	s.verifier = harnessVerifierFunc(func(context.Context, Target) (VerifyResult, error) {
		return VerifyResult{ResolvedPath: "/Users/test/.npm/bin/codex", Output: "codex 1.2.3\n"}, nil
	})

	job, err := s.StartAgent(context.Background(), TargetCodex, "npm")
	if err != nil {
		t.Fatalf("StartAgent: %v", err)
	}
	if job.Status != StatusInstalling || job.Method != "npm" {
		t.Fatalf("initial job = %+v", job)
	}
	waitForStatus(t, s, TargetCodex, StatusSucceeded)

	final, err := s.Status(context.Background(), TargetCodex)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if final.ExpectedDestination != "/Users/test/.npm/bin/codex" {
		t.Fatalf("expected destination = %q", final.ExpectedDestination)
	}
	if !strings.Contains(final.Output, "installed") || !strings.Contains(final.Output, "codex 1.2.3") {
		t.Fatalf("output = %q, want installer and verifier diagnostics", final.Output)
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	statuses := make([]string, 0, len(store.history))
	for _, record := range store.history {
		statuses = append(statuses, record.Status)
	}
	if strings.Join(statuses, ",") != "installing,verifying,succeeded" {
		t.Fatalf("persisted statuses = %v", statuses)
	}
}

func TestStartAgentSuccessCallbackRunsAfterPersistedVerification(t *testing.T) {
	store := newInstallJobStoreFake()
	s := newTestService("darwin", "npm")
	s.installCapabilities = installCapabilitiesStub{prefix: "/Users/test/.npm", writable: true}
	s.jobStore = store
	s.commands = commandRunnerFunc(func(context.Context, []string, io.Writer, io.Writer) error { return nil })
	s.verifier = harnessVerifierFunc(func(context.Context, Target) (VerifyResult, error) {
		return VerifyResult{ResolvedPath: "/Users/test/.npm/bin/codex"}, nil
	})
	succeeded := make(chan Target, 1)
	s.SetOnSucceeded(func(target Target) { succeeded <- target })

	if _, err := s.StartAgent(context.Background(), TargetCodex, "npm"); err != nil {
		t.Fatalf("StartAgent: %v", err)
	}
	waitForStatus(t, s, TargetCodex, StatusSucceeded)

	select {
	case target := <-succeeded:
		if target != TargetCodex {
			t.Fatalf("callback target = %q, want %q", target, TargetCodex)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for durable agent-install success callback")
	}
}

func TestAgentVendorScriptInstallPersistsInstallVerifySuccessLifecycle(t *testing.T) {
	store := newInstallJobStoreFake()
	s := newTestService("linux", "bash")
	s.jobStore = store
	captured := make(chan ports.InstallScriptCommand, 1)
	s.installScripts = installScriptRunnerFunc(func(_ context.Context, command ports.InstallScriptCommand, stdout, _ io.Writer) (ports.InstallScriptResult, error) {
		captured <- command
		_, _ = io.WriteString(stdout, "installed\n")
		return ports.InstallScriptResult{SHA256: "abc123"}, nil
	})
	s.verifier = harnessVerifierFunc(func(context.Context, Target) (VerifyResult, error) {
		return VerifyResult{ResolvedPath: "/home/test/.local/bin/agent", Output: "cursor 1.2.3\n"}, nil
	})

	job, err := s.StartAgent(context.Background(), TargetCursor, "official-installer")
	if err != nil {
		t.Fatalf("StartAgent: %v", err)
	}
	if job.Status != StatusInstalling || job.Method != "official-installer" {
		t.Fatalf("initial job = %+v", job)
	}
	waitForStatus(t, s, TargetCursor, StatusSucceeded)
	command := <-captured
	if command.URL != "https://cursor.com/install" {
		t.Fatalf("URL = %q", command.URL)
	}
	final, err := s.Status(context.Background(), TargetCursor)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(final.Output, "source: https://cursor.com/install") ||
		!strings.Contains(final.Output, "sha256: abc123") ||
		!strings.Contains(final.Output, "cursor 1.2.3") {
		t.Fatalf("output = %q", final.Output)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	var statuses []string
	for _, record := range store.history {
		statuses = append(statuses, record.Status)
	}
	if strings.Join(statuses, ",") != "installing,verifying,succeeded" {
		t.Fatalf("statuses = %v", statuses)
	}
}

func TestAgentVendorScriptInstallFailsWithoutRunner(t *testing.T) {
	s := newTestService("linux", "bash")
	if _, err := s.StartAgent(context.Background(), TargetCursor, "official-installer"); err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, s, TargetCursor, StatusFailed)
	job, _ := s.Status(context.Background(), TargetCursor)
	if !strings.Contains(job.Error, "runner is not configured") {
		t.Fatalf("error = %q", job.Error)
	}
}

func TestAgentVendorScriptInstallPreservesDigestOnRunnerFailure(t *testing.T) {
	s := newTestService("linux", "bash")
	s.installScripts = installScriptRunnerFunc(func(context.Context, ports.InstallScriptCommand, io.Writer, io.Writer) (ports.InstallScriptResult, error) {
		return ports.InstallScriptResult{SHA256: "deadbeef"}, errors.New("installer exited 7")
	})
	if _, err := s.StartAgent(context.Background(), TargetCursor, "official-installer"); err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, s, TargetCursor, StatusFailed)
	job, _ := s.Status(context.Background(), TargetCursor)
	if job.Error != "installer exited 7" || !strings.Contains(job.Output, "sha256: deadbeef") {
		t.Fatalf("job = %+v", job)
	}
}

func TestAgentVendorScriptInstallTimeoutAndShutdown(t *testing.T) {
	for _, test := range []struct {
		name       string
		stop       bool
		wantStatus Status
		wantError  string
	}{
		{name: "timeout", wantStatus: StatusFailed, wantError: "timed out"},
		{name: "shutdown", stop: true, wantStatus: StatusInterrupted, wantError: "shutdown interrupted"},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := newTestService("linux", "bash")
			s.installTimeout = 50 * time.Millisecond
			started := make(chan struct{})
			s.installScripts = installScriptRunnerFunc(func(ctx context.Context, _ ports.InstallScriptCommand, _, _ io.Writer) (ports.InstallScriptResult, error) {
				close(started)
				<-ctx.Done()
				return ports.InstallScriptResult{}, ctx.Err()
			})
			if _, err := s.StartAgent(context.Background(), TargetCursor, "official-installer"); err != nil {
				t.Fatal(err)
			}
			<-started
			if test.stop {
				if err := s.Close(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			waitForStatus(t, s, TargetCursor, test.wantStatus)
			job, _ := s.Status(context.Background(), TargetCursor)
			if !strings.Contains(job.Error, test.wantError) {
				t.Fatalf("error = %q", job.Error)
			}
		})
	}
}

func TestAgentInstallRejectsConcurrentWorkForSameHarness(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{})
	s := newTestService("darwin", "npm")
	s.installCapabilities = installCapabilitiesStub{prefix: "/Users/test/.npm", writable: true}
	s.commands = commandRunnerFunc(func(context.Context, []string, io.Writer, io.Writer) error {
		close(started)
		<-release
		return nil
	})

	if _, err := s.StartAgent(context.Background(), TargetCodex, "npm"); err != nil {
		t.Fatalf("StartAgent: %v", err)
	}
	<-started
	if _, err := s.StartAgent(context.Background(), TargetCodex, "npm"); !errors.Is(err, ErrInstallActive) {
		t.Fatalf("concurrent StartAgent error = %v, want ErrInstallActive", err)
	}
	if _, err := s.Verify(context.Background(), TargetCodex); !errors.Is(err, ErrInstallActive) {
		t.Fatalf("concurrent Verify error = %v, want ErrInstallActive", err)
	}
	close(release)
}

func TestCloseCancelsAndDrainsActiveAgentInstall(t *testing.T) {
	started := make(chan struct{})
	s := newTestService("darwin", "npm")
	s.installCapabilities = installCapabilitiesStub{prefix: "/Users/test/.npm", writable: true}
	s.commands = commandRunnerFunc(func(ctx context.Context, _ []string, _, _ io.Writer) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	})

	if _, err := s.StartAgent(context.Background(), TargetCodex, "npm"); err != nil {
		t.Fatalf("StartAgent: %v", err)
	}
	<-started
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := s.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}
	job, err := s.Status(context.Background(), TargetCodex)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if job.Status != StatusInterrupted {
		t.Fatalf("status after Close = %q, want interrupted", job.Status)
	}
}

func TestCloseCancelsAndDrainsLegacyClaudeInstall(t *testing.T) {
	started := make(chan struct{})
	s := newTestService("darwin", "npm")
	s.commands = commandRunnerFunc(func(ctx context.Context, _ []string, _, _ io.Writer) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	})

	if _, err := s.Start(context.Background(), TargetClaude); err != nil {
		t.Fatalf("Start Claude: %v", err)
	}
	<-started
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := s.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}
	job, err := s.Status(context.Background(), TargetClaude)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if job.Status != StatusInterrupted {
		t.Fatalf("status after Close = %q, want interrupted", job.Status)
	}
}

func TestAgentVerificationGetsFreshDaemonContext(t *testing.T) {
	s := newTestService("darwin", "npm")
	s.installCapabilities = installCapabilitiesStub{prefix: "/Users/test/.npm", writable: true}
	s.commands = commandRunnerFunc(func(context.Context, []string, io.Writer, io.Writer) error { return nil })
	s.verifier = harnessVerifierFunc(func(ctx context.Context, _ Target) (VerifyResult, error) {
		if _, hasDeadline := ctx.Deadline(); hasDeadline {
			return VerifyResult{}, errors.New("verification inherited installer deadline")
		}
		return VerifyResult{ResolvedPath: "/Users/test/.npm/bin/codex"}, nil
	})

	if _, err := s.StartAgent(context.Background(), TargetCodex, "npm"); err != nil {
		t.Fatalf("StartAgent: %v", err)
	}
	waitForStatus(t, s, TargetCodex, StatusSucceeded)
}

func TestAgentInstallVerificationFailureIsTerminalFailure(t *testing.T) {
	store := newInstallJobStoreFake()
	s := newTestService("darwin", "brew")
	s.jobStore = store
	s.commands = commandRunnerFunc(func(context.Context, []string, io.Writer, io.Writer) error { return nil })
	s.verifier = harnessVerifierFunc(func(context.Context, Target) (VerifyResult, error) {
		return VerifyResult{ResolvedPath: "/opt/homebrew/bin/codex", Output: "bad version output"}, errors.New("version probe failed")
	})

	if _, err := s.StartAgent(context.Background(), TargetCodex, "homebrew"); err != nil {
		t.Fatalf("StartAgent: %v", err)
	}
	waitForStatus(t, s, TargetCodex, StatusFailed)
	job, _ := s.Status(context.Background(), TargetCodex)
	if !strings.Contains(job.Error, "version probe failed") || !strings.Contains(job.Output, "bad version output") {
		t.Fatalf("failed job = %+v", job)
	}
}

func TestVerifyAgainDoesNotRunInstaller(t *testing.T) {
	store := newInstallJobStoreFake()
	s := newTestService("darwin", "brew")
	s.jobStore = store
	installCalls := 0
	s.commands = commandRunnerFunc(func(context.Context, []string, io.Writer, io.Writer) error {
		installCalls++
		return nil
	})
	s.verifier = harnessVerifierFunc(func(context.Context, Target) (VerifyResult, error) {
		return VerifyResult{ResolvedPath: "/opt/homebrew/bin/codex", Output: "codex 1.2.3"}, nil
	})

	job, err := s.Verify(context.Background(), TargetCodex)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if job.Status != StatusVerifying {
		t.Fatalf("initial status = %q, want verifying", job.Status)
	}
	waitForStatus(t, s, TargetCodex, StatusSucceeded)
	if installCalls != 0 {
		t.Fatalf("installer calls = %d, want 0", installCalls)
	}
}

func TestVerifyPersistenceFailureDoesNotLeavePhantomActiveJob(t *testing.T) {
	store := newInstallJobStoreFake()
	store.upsertErr = errors.New("database unavailable")
	s := newTestService("darwin", "brew")
	s.jobStore = store
	verifyCalls := 0
	s.verifier = harnessVerifierFunc(func(context.Context, Target) (VerifyResult, error) {
		verifyCalls++
		return VerifyResult{ResolvedPath: "/opt/homebrew/bin/codex", Output: "codex 1.2.3"}, nil
	})

	if _, err := s.Verify(context.Background(), TargetCodex); err == nil {
		t.Fatal("Verify error = nil, want persistence failure")
	}
	store.mu.Lock()
	store.upsertErr = nil
	store.mu.Unlock()

	job, err := s.Verify(context.Background(), TargetCodex)
	if err != nil {
		t.Fatalf("retry Verify: %v", err)
	}
	if job.Status != StatusVerifying {
		t.Fatalf("retry status = %q, want verifying", job.Status)
	}
	waitForStatus(t, s, TargetCodex, StatusSucceeded)
	if verifyCalls != 1 {
		t.Fatalf("verifier calls = %d, want 1", verifyCalls)
	}
}

func TestRecoverInterruptsAndHydratesDurableJobs(t *testing.T) {
	store := newInstallJobStoreFake()
	started := time.Now().Add(-time.Minute).UTC()
	store.records[string(TargetCodex)] = ports.AgentInstallJobRecord{
		Target: string(TargetCodex), Status: string(StatusVerifying), Method: "npm", StartedAt: started, UpdatedAt: started,
	}
	s := newTestService("darwin", "npm")
	s.jobStore = store
	if err := s.Recover(context.Background()); err != nil {
		t.Fatalf("Recover: %v", err)
	}
	job, err := s.Status(context.Background(), TargetCodex)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if job.Status != StatusInterrupted || job.FinishedAt == nil || job.Error == "" {
		t.Fatalf("recovered job = %+v", job)
	}
}

func TestListAgentJobsReturnsDurableJobs(t *testing.T) {
	store := newInstallJobStoreFake()
	now := time.Now().UTC()
	store.records[string(TargetCodex)] = ports.AgentInstallJobRecord{Target: string(TargetCodex), Status: string(StatusFailed), StartedAt: now, UpdatedAt: now}
	s := newTestService("darwin", "npm")
	s.jobStore = store
	jobs, err := s.AgentJobs(context.Background())
	if err != nil {
		t.Fatalf("AgentJobs: %v", err)
	}
	if len(jobs) != 1 || jobs[0].Target != TargetCodex || jobs[0].Status != StatusFailed {
		t.Fatalf("jobs = %+v", jobs)
	}
}

func TestTerminalPersistenceFailureOverridesStaleActiveDurableJob(t *testing.T) {
	store := newInstallJobStoreFake()
	store.upsertErrForStatus = map[string]error{string(StatusSucceeded): errors.New("database unavailable")}
	s := newTestService("darwin", "npm")
	s.installCapabilities = installCapabilitiesStub{prefix: "/Users/test/.npm", writable: true}
	s.jobStore = store
	s.commands = commandRunnerFunc(func(context.Context, []string, io.Writer, io.Writer) error { return nil })
	s.verifier = harnessVerifierFunc(func(context.Context, Target) (VerifyResult, error) {
		return VerifyResult{ResolvedPath: "/Users/test/.npm/bin/codex"}, nil
	})

	if _, err := s.StartAgent(context.Background(), TargetCodex, "npm"); err != nil {
		t.Fatalf("StartAgent: %v", err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		s.mu.Lock()
		terminal := s.jobs[TargetCodex] != nil && !activeStatus(s.jobs[TargetCodex].Status)
		s.mu.Unlock()
		if terminal || time.Now().After(deadline) {
			break
		}
		time.Sleep(time.Millisecond)
	}

	job, err := s.Status(context.Background(), TargetCodex)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if job.Status != StatusFailed || !strings.Contains(job.Error, "persist terminal install state") {
		t.Fatalf("Status returned stale durable job: %+v", job)
	}
	jobs, err := s.AgentJobs(context.Background())
	if err != nil {
		t.Fatalf("AgentJobs: %v", err)
	}
	if len(jobs) != 1 || jobs[0].Status != StatusFailed || !strings.Contains(jobs[0].Error, "persist terminal install state") {
		t.Fatalf("AgentJobs returned stale durable jobs: %+v", jobs)
	}
}

func TestDroidInstallIsBlockedWhileDroidSessionIsActive(t *testing.T) {
	s := newTestService("darwin", "brew")
	s.sessions = sessionListerStub{sessions: []domain.SessionRecord{{Harness: domain.HarnessDroid, IsTerminated: false}}}
	_, err := s.StartAgent(context.Background(), TargetDroid, "homebrew")
	if !errors.Is(err, ErrHarnessActive) {
		t.Fatalf("StartAgent error = %v, want ErrHarnessActive", err)
	}
}

func TestDroidInstallIsBlockedWhileDroidSessionIsStarting(t *testing.T) {
	s := newTestService("darwin", "brew")
	release, ok := s.TryBeginHarnessUse(domain.HarnessDroid)
	if !ok {
		t.Fatal("TryBeginHarnessUse unexpectedly rejected without an install")
	}
	defer release()

	if _, err := s.StartAgent(context.Background(), TargetDroid, "homebrew"); !errors.Is(err, ErrHarnessActive) {
		t.Fatalf("StartAgent error = %v, want ErrHarnessActive", err)
	}
}

func TestDroidInstallAllowsTerminatedSessionAndOtherHarnesses(t *testing.T) {
	for _, tt := range []struct {
		name     string
		target   Target
		sessions []domain.SessionRecord
	}{
		{name: "terminated droid", target: TargetDroid, sessions: []domain.SessionRecord{{Harness: domain.HarnessDroid, IsTerminated: true}}},
		{name: "active codex does not block droid", target: TargetDroid, sessions: []domain.SessionRecord{{Harness: domain.HarnessCodex, IsTerminated: false}}},
		{name: "active droid does not block codex", target: TargetCodex, sessions: []domain.SessionRecord{{Harness: domain.HarnessDroid, IsTerminated: false}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := newTestService("darwin", "brew")
			s.sessions = sessionListerStub{sessions: tt.sessions}
			s.commands = commandRunnerFunc(func(context.Context, []string, io.Writer, io.Writer) error { return errors.New("stop after guard") })
			if _, err := s.StartAgent(context.Background(), tt.target, "homebrew"); err != nil {
				t.Fatalf("StartAgent: %v", err)
			}
		})
	}
}

func waitForStatus(t *testing.T, s *Service, target Target, want Status) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		job, err := s.Status(context.Background(), target)
		if err != nil {
			t.Fatalf("Status() error = %v", err)
		}
		if job.Status == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s to reach status %q", target, want)
}

// A Linux package-manager plan must now carry the resolved argv even though it
// stays Unsupported. That pairing is the whole point of the split: the daemon
// refuses to elevate, while the CLI (which has a terminal and can prompt for a
// sudo password) runs the very same command. Before this, the argv existed only
// inside the Reason string, so the CLI had to re-derive it and the two surfaces
// could drift.
func TestLinuxPlanExposesArgvWhileStayingUnsupported(t *testing.T) {
	for _, tt := range []struct {
		name        string
		found       string
		wantCommand string
		wantManager string
	}{
		{"apt-get", "apt-get", "apt-get install -y tmux", "apt-get"},
		{"dnf", "dnf", "dnf install -y tmux", "dnf"},
		{"pacman", "pacman", "pacman -S --noconfirm tmux", "pacman"},
		{"zypper", "zypper", "zypper install -y tmux", "zypper"},
		{"apk", "apk", "apk add tmux", "apk"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			plan := newTestService("linux", tt.found).planFor(TargetTmux)
			if got := strings.Join(plan.Command, " "); got != tt.wantCommand {
				t.Fatalf("Command = %q, want %q", got, tt.wantCommand)
			}
			if plan.Manager != tt.wantManager {
				t.Fatalf("Manager = %q, want %q", plan.Manager, tt.wantManager)
			}
			if !plan.NeedsRoot {
				t.Fatal("NeedsRoot = false, want true: every Linux package manager needs root")
			}
			// The security invariant. Exposing the argv must not have made the
			// daemon willing to run it.
			if !plan.Unsupported {
				t.Fatal("Unsupported = false, want true: the daemon must never run a root install itself")
			}
		})
	}
}

// The daemon must still refuse to execute a Linux plan, argv or not. This is
// the behavioural half of the invariant asserted above.
func TestStartRefusesLinuxRootInstall(t *testing.T) {
	s := newTestService("linux", "apt-get")
	s.commands = commandRunnerFunc(func(context.Context, []string, io.Writer, io.Writer) error {
		t.Fatal("the daemon must not execute a root install")
		return nil
	})
	job, err := s.Start(context.Background(), TargetTmux)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if job.Status != StatusUnsupported {
		t.Fatalf("Status = %q, want %q", job.Status, StatusUnsupported)
	}
}

// Resolve is the seam the CLI consumes; it must agree with the Service's own
// planner rather than being a second implementation of the same table.
func TestResolveMatchesServicePlan(t *testing.T) {
	lookPath := lookPathFound("brew", "curl", "bash")
	service := &Service{goos: "darwin", executables: executableFinderFunc(lookPath)}
	for _, target := range []Target{TargetTmux, TargetCursor} {
		got := Resolve("darwin", lookPath, target)
		want := service.resolvePlan(target)
		if strings.Join(got.Command, " ") != strings.Join(want.Command, " ") {
			t.Fatalf("Resolve(%q) Command = %v, want %v", target, got.Command, want.Command)
		}
		if got.Unsupported != want.Unsupported {
			t.Fatalf("Resolve(%q) Unsupported = %v, want %v", target, got.Unsupported, want.Unsupported)
		}
		if got.Method != want.Method {
			t.Fatalf("Resolve(%q) Method = %q, want %q", target, got.Method, want.Method)
		}
	}
	if unknown := Resolve("linux", lookPath, Target("nope")); !unknown.Unsupported {
		t.Fatal("Resolve of an unknown target must be Unsupported")
	}
}
