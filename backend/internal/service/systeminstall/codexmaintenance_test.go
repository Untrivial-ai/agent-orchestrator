package systeminstall

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestClassifyCodexOwnership(t *testing.T) {
	tests := []struct {
		name       string
		goos       string
		binaryPath string
		npm        ports.NPMInstallCapabilities
		homebrew   ports.HomebrewInstallCapabilities
		want       CodexOwnershipKind
	}{
		{
			name: "empty binary path is unknown", goos: "darwin", binaryPath: "",
			want: CodexOwnershipUnknown,
		},
		{
			name: "under npm global prefix bin is npm-owned", goos: "darwin",
			binaryPath: "/Users/test/.npm-global/bin/codex",
			npm:        ports.NPMInstallCapabilities{GlobalPrefix: "/Users/test/.npm-global"},
			want:       CodexOwnershipNPM,
		},
		{
			name: "windows npm prefix has no bin suffix", goos: "windows",
			binaryPath: "C:/Users/test/AppData/Roaming/npm/codex.cmd",
			npm:        ports.NPMInstallCapabilities{GlobalPrefix: "C:/Users/test/AppData/Roaming/npm"},
			want:       CodexOwnershipNPM,
		},
		{
			name: "a different npm prefix does not claim ownership", goos: "darwin",
			binaryPath: "/opt/homebrew/bin/codex",
			npm:        ports.NPMInstallCapabilities{GlobalPrefix: "/Users/other/.npm-global"},
			want:       CodexOwnershipStandalone,
		},
		{
			name: "under the homebrew Cellar is homebrew-owned", goos: "darwin",
			binaryPath: "/opt/homebrew/Cellar/codex/0.153.4/bin/codex",
			homebrew:   ports.HomebrewInstallCapabilities{Prefix: "/opt/homebrew"},
			want:       CodexOwnershipHomebrew,
		},
		{
			name: "under the homebrew Caskroom is homebrew-owned", goos: "darwin",
			binaryPath: "/opt/homebrew/Caskroom/codex/1.2.0/codex",
			homebrew:   ports.HomebrewInstallCapabilities{Prefix: "/opt/homebrew"},
			want:       CodexOwnershipHomebrew,
		},
		{
			name: "npm has an error and is not consulted", goos: "darwin",
			binaryPath: "/Users/test/.npm-global/bin/codex",
			npm:        ports.NPMInstallCapabilities{GlobalPrefix: "/Users/test/.npm-global", Err: errors.New("boom")},
			want:       CodexOwnershipStandalone,
		},
		{
			name: "homebrew has an error and is not consulted", goos: "darwin",
			binaryPath: "/opt/homebrew/Cellar/codex/0.1/bin/codex",
			homebrew:   ports.HomebrewInstallCapabilities{Prefix: "/opt/homebrew", Err: errors.New("boom")},
			want:       CodexOwnershipStandalone,
		},
		{
			name: "a hand-placed binary outside any known manager is standalone", goos: "darwin",
			binaryPath: "/Users/test/.local/bin/codex",
			want:       CodexOwnershipStandalone,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			capabilities := &ports.InstallCapabilities{NPM: tt.npm, Homebrew: tt.homebrew}
			got := classifyCodexOwnership(tt.goos, tt.binaryPath, capabilities)
			if got != tt.want {
				t.Fatalf("classifyCodexOwnership(%q) = %q, want %q", tt.binaryPath, got, tt.want)
			}
		})
	}
}

func newCodexTestService(t *testing.T, goos string, run func(context.Context, []string, io.Writer, io.Writer) error) *Service {
	t.Helper()
	s := newTestService(goos, "npm", "brew")
	s.commands = commandRunnerFunc(run)
	return s
}

func TestResolveCodexUpdatePlanNPMOwned(t *testing.T) {
	s := newCodexTestService(t, "darwin", func(context.Context, []string, io.Writer, io.Writer) error {
		t.Fatal("no subprocess should run while only resolving a plan")
		return nil
	})
	s.installCapabilities = installCapabilitiesStub{prefix: "/Users/test/.npm-global", writable: true}
	s.SetCodexBinaryResolver(func(context.Context) (string, error) {
		return "/Users/test/.npm-global/bin/codex", nil
	})

	plan, err := s.resolveCodexUpdatePlan(context.Background())
	if err != nil {
		t.Fatalf("resolveCodexUpdatePlan: %v", err)
	}
	if plan.ownership != CodexOwnershipNPM {
		t.Fatalf("ownership = %q, want npm", plan.ownership)
	}
	if !plan.supported {
		t.Fatalf("expected npm update to be supported, reason=%q", plan.manualReason)
	}
	want := "npm install -g @openai/codex@latest"
	if got := strings.Join(plan.argv, " "); got != want {
		t.Fatalf("argv = %q, want %q", got, want)
	}
}

func TestResolveCodexUpdatePlanNPMPrefixNotWritable(t *testing.T) {
	s := newCodexTestService(t, "darwin", nil)
	s.installCapabilities = installCapabilitiesStub{prefix: "/Users/test/.npm-global", writable: false}
	s.SetCodexBinaryResolver(func(context.Context) (string, error) {
		return "/Users/test/.npm-global/bin/codex", nil
	})

	plan, err := s.resolveCodexUpdatePlan(context.Background())
	if err != nil {
		t.Fatalf("resolveCodexUpdatePlan: %v", err)
	}
	if plan.ownership != CodexOwnershipNPM {
		t.Fatalf("ownership = %q, want npm", plan.ownership)
	}
	if plan.supported {
		t.Fatal("expected update to be unsupported when the npm prefix is not writable")
	}
	if !strings.Contains(plan.manualReason, "not writable") {
		t.Fatalf("manualReason = %q, want a not-writable explanation", plan.manualReason)
	}
}

func TestResolveCodexUpdatePlanHomebrewOwned(t *testing.T) {
	s := newCodexTestService(t, "darwin", nil)
	s.installCapabilities = installCapabilitiesStub{prefix: "/Users/test/.npm-global", writable: true, homebrewInstalled: true}
	s.SetCodexBinaryResolver(func(context.Context) (string, error) {
		return "/opt/homebrew/Caskroom/codex/1.2.0/codex", nil
	})

	plan, err := s.resolveCodexUpdatePlan(context.Background())
	if err != nil {
		t.Fatalf("resolveCodexUpdatePlan: %v", err)
	}
	if plan.ownership != CodexOwnershipHomebrew {
		t.Fatalf("ownership = %q, want homebrew", plan.ownership)
	}
	if !plan.supported {
		t.Fatalf("expected homebrew update to be supported, reason=%q", plan.manualReason)
	}
	want := "brew upgrade --cask codex"
	if got := strings.Join(plan.argv, " "); got != want {
		t.Fatalf("argv = %q, want %q", got, want)
	}
}

func TestResolveCodexUpdatePlanStandaloneWithNativeUpdateSupport(t *testing.T) {
	s := newCodexTestService(t, "darwin", func(_ context.Context, argv []string, stdout, _ io.Writer) error {
		if strings.Join(argv, " ") != "/Users/test/.local/bin/codex --help" {
			t.Fatalf("unexpected probe argv: %v", argv)
		}
		_, _ = stdout.Write([]byte("USAGE:\n  codex [COMMAND]\n\nCommands:\n  update  Update the Codex CLI\n"))
		return nil
	})
	s.installCapabilities = installCapabilitiesStub{prefix: "/Users/other/.npm-global", writable: true}
	s.SetCodexBinaryResolver(func(context.Context) (string, error) {
		return "/Users/test/.local/bin/codex", nil
	})

	plan, err := s.resolveCodexUpdatePlan(context.Background())
	if err != nil {
		t.Fatalf("resolveCodexUpdatePlan: %v", err)
	}
	if plan.ownership != CodexOwnershipStandalone {
		t.Fatalf("ownership = %q, want standalone", plan.ownership)
	}
	if !plan.supported {
		t.Fatalf("expected standalone update to be supported, reason=%q", plan.manualReason)
	}
	want := "/Users/test/.local/bin/codex update"
	if got := strings.Join(plan.argv, " "); got != want {
		t.Fatalf("argv = %q, want %q", got, want)
	}
}

func TestResolveCodexUpdatePlanStandaloneWithoutNativeUpdateSupport(t *testing.T) {
	s := newCodexTestService(t, "darwin", func(_ context.Context, _ []string, stdout, _ io.Writer) error {
		_, _ = stdout.Write([]byte("USAGE:\n  codex [COMMAND]\n\nCommands:\n  login\n  logout\n"))
		return nil
	})
	s.installCapabilities = installCapabilitiesStub{prefix: "/Users/other/.npm-global", writable: true}
	s.SetCodexBinaryResolver(func(context.Context) (string, error) {
		return "/Users/test/.cargo/bin/codex", nil
	})

	plan, err := s.resolveCodexUpdatePlan(context.Background())
	if err != nil {
		t.Fatalf("resolveCodexUpdatePlan: %v", err)
	}
	if plan.ownership != CodexOwnershipStandalone {
		t.Fatalf("ownership = %q, want standalone", plan.ownership)
	}
	if plan.supported {
		t.Fatal("expected update to be unsupported when --help does not advertise `update`")
	}
	if plan.manualReason == "" {
		t.Fatal("expected a manual-update reason to be set")
	}
}

func TestResolveCodexUpdatePlanBinaryNotFound(t *testing.T) {
	s := newCodexTestService(t, "darwin", func(context.Context, []string, io.Writer, io.Writer) error {
		t.Fatal("no subprocess should run when the binary itself cannot be resolved")
		return nil
	})
	s.SetCodexBinaryResolver(func(context.Context) (string, error) {
		return "", errors.New("codex: executable not found")
	})

	plan, err := s.resolveCodexUpdatePlan(context.Background())
	if err == nil {
		t.Fatal("expected an error when the codex binary cannot be resolved")
	}
	if plan.ownership != CodexOwnershipUnknown {
		t.Fatalf("ownership = %q, want unknown", plan.ownership)
	}
}

func TestCodexMaintenanceStatusIsCached(t *testing.T) {
	var resolveCalls int
	s := newCodexTestService(t, "darwin", func(_ context.Context, argv []string, stdout, _ io.Writer) error {
		if strings.Join(argv, " ") == "/Users/test/.npm-global/bin/codex --version" {
			_, _ = stdout.Write([]byte("codex-cli 0.149.1\n"))
			return nil
		}
		if strings.Join(argv, " ") == "npm view @openai/codex version" {
			_, _ = stdout.Write([]byte("0.153.4\n"))
			return nil
		}
		t.Fatalf("unexpected command: %v", argv)
		return nil
	})
	s.installCapabilities = installCapabilitiesStub{prefix: "/Users/test/.npm-global", writable: true}
	s.SetCodexBinaryResolver(func(context.Context) (string, error) {
		resolveCalls++
		return "/Users/test/.npm-global/bin/codex", nil
	})

	status, err := s.CodexMaintenanceStatus(context.Background())
	if err != nil {
		t.Fatalf("CodexMaintenanceStatus: %v", err)
	}
	if status.Ownership != CodexOwnershipNPM {
		t.Fatalf("ownership = %q, want npm", status.Ownership)
	}
	if status.InstalledVersion != "0.149.1" {
		t.Fatalf("installedVersion = %q, want 0.149.1", status.InstalledVersion)
	}
	if status.LatestVersion != "0.153.4" {
		t.Fatalf("latestVersion = %q, want 0.153.4", status.LatestVersion)
	}
	if !status.UpdateAvailable {
		t.Fatal("expected updateAvailable to be true when latest > installed")
	}
	if !status.UpdateSupported {
		t.Fatal("expected updateSupported to be true")
	}

	if _, err := s.CodexMaintenanceStatus(context.Background()); err != nil {
		t.Fatalf("CodexMaintenanceStatus (cached): %v", err)
	}
	if resolveCalls != 1 {
		t.Fatalf("resolveCalls = %d, want 1 (second call should hit the cache)", resolveCalls)
	}
}

func TestCodexMaintenanceStatusUpToDateIsNotAnUpdate(t *testing.T) {
	s := newCodexTestService(t, "darwin", func(_ context.Context, argv []string, stdout, _ io.Writer) error {
		if strings.Join(argv, " ") == "/Users/test/.npm-global/bin/codex --version" {
			_, _ = stdout.Write([]byte("codex-cli 0.153.4\n"))
			return nil
		}
		if strings.Join(argv, " ") == "npm view @openai/codex version" {
			_, _ = stdout.Write([]byte("0.153.4\n"))
			return nil
		}
		t.Fatalf("unexpected command: %v", argv)
		return nil
	})
	s.installCapabilities = installCapabilitiesStub{prefix: "/Users/test/.npm-global", writable: true}
	s.SetCodexBinaryResolver(func(context.Context) (string, error) {
		return "/Users/test/.npm-global/bin/codex", nil
	})

	status, err := s.CodexMaintenanceStatus(context.Background())
	if err != nil {
		t.Fatalf("CodexMaintenanceStatus: %v", err)
	}
	if status.UpdateAvailable {
		t.Fatal("expected updateAvailable to be false when versions match")
	}
}

func TestCodexMaintenanceStatusUnresolvableBinaryIsAdvisoryNotError(t *testing.T) {
	s := newCodexTestService(t, "darwin", nil)
	s.SetCodexBinaryResolver(func(context.Context) (string, error) {
		return "", errors.New("codex: executable not found")
	})

	status, err := s.CodexMaintenanceStatus(context.Background())
	if err != nil {
		t.Fatalf("CodexMaintenanceStatus should not error on an unresolved binary: %v", err)
	}
	if status.Ownership != CodexOwnershipUnknown {
		t.Fatalf("ownership = %q, want unknown", status.Ownership)
	}
	if status.ManualReason == "" {
		t.Fatal("expected a manual reason explaining the unresolved binary")
	}
}

func TestStartCodexUpdateRunsTheAdvertisedCommand(t *testing.T) {
	s := newCodexTestService(t, "darwin", func(_ context.Context, argv []string, _, _ io.Writer) error {
		if strings.Join(argv, " ") != "npm install -g @openai/codex@latest" {
			t.Fatalf("unexpected install argv: %v", argv)
		}
		return nil
	})
	s.installTimeout = 2 * time.Second
	s.installCapabilities = installCapabilitiesStub{prefix: "/Users/test/.npm-global", writable: true}
	s.SetCodexBinaryResolver(func(context.Context) (string, error) {
		return "/Users/test/.npm-global/bin/codex", nil
	})
	s.verifier = harnessVerifierFunc(func(context.Context, Target) (VerifyResult, error) {
		return VerifyResult{ResolvedPath: "/Users/test/.npm-global/bin/codex", Output: "codex-cli 0.153.4"}, nil
	})

	job, err := s.StartCodexUpdate(context.Background(), "")
	if err != nil {
		t.Fatalf("StartCodexUpdate: %v", err)
	}
	if job.Status != StatusInstalling {
		t.Fatalf("job.Status = %q, want installing", job.Status)
	}
	if job.Method != string(CodexOwnershipNPM) {
		t.Fatalf("job.Method = %q, want npm", job.Method)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		got, err := s.Status(context.Background(), TargetCodex)
		if err != nil {
			t.Fatalf("Status: %v", err)
		}
		if got.Status == StatusSucceeded {
			return
		}
		if got.Status == StatusFailed {
			t.Fatalf("job failed: %s", got.Error)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out waiting for the codex update job to finish")
}

func TestStartCodexUpdateRejectsChangedOwnership(t *testing.T) {
	s := newCodexTestService(t, "darwin", func(context.Context, []string, io.Writer, io.Writer) error {
		t.Fatal("no install command should run when ownership changed since the advisory")
		return nil
	})
	s.installCapabilities = installCapabilitiesStub{prefix: "/Users/test/.npm-global", writable: true}
	s.SetCodexBinaryResolver(func(context.Context) (string, error) {
		return "/Users/test/.npm-global/bin/codex", nil
	})

	_, err := s.StartCodexUpdate(context.Background(), CodexOwnershipHomebrew)
	if !errors.Is(err, ErrCodexOwnershipChanged) {
		t.Fatalf("err = %v, want ErrCodexOwnershipChanged", err)
	}
}

func TestStartCodexUpdateUnsupportedOwnershipDoesNotRunAnything(t *testing.T) {
	s := newCodexTestService(t, "darwin", func(_ context.Context, argv []string, stdout, _ io.Writer) error {
		// Only the standalone --help probe may run; no update command may run.
		if len(argv) < 2 || argv[1] != "--help" {
			t.Fatalf("unexpected command: %v", argv)
		}
		_, _ = stdout.Write([]byte("no subcommands here"))
		return nil
	})
	s.installCapabilities = installCapabilitiesStub{prefix: "/Users/other/.npm-global", writable: true}
	s.SetCodexBinaryResolver(func(context.Context) (string, error) {
		return "/Users/test/.cargo/bin/codex", nil
	})

	job, err := s.StartCodexUpdate(context.Background(), CodexOwnershipStandalone)
	if err != nil {
		t.Fatalf("StartCodexUpdate: %v", err)
	}
	if job.Status != StatusUnsupported {
		t.Fatalf("job.Status = %q, want unsupported", job.Status)
	}
	if job.Error == "" {
		t.Fatal("expected the job to carry the manual-update reason")
	}
}
