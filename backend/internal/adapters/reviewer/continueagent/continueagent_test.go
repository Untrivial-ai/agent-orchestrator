package continueagent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func testReviewer() *Reviewer {
	return &Reviewer{resolveBinary: func(context.Context) (string, error) { return "/opt/continue/bin/cn", nil }}
}

func TestReviewCommandLaunchesAutoApprovedHostTrustedTUI(t *testing.T) {
	dataDir := t.TempDir()
	spec, err := testReviewer().ReviewCommand(context.Background(), ports.ReviewInvocation{
		DataDir: dataDir, ReviewerID: "review-worker-1", TaskPromptRoot: filepath.Join(dataDir, "prompts"),
		WorkspacePath: "/host/project", Prompt: "task-ref:opaque",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(spec.Argv, []string{"/opt/continue/bin/cn", "--auto"}) || spec.InitialMessage != "task-ref:opaque" || spec.WorkingDirectory != "/host/project" {
		t.Fatalf("ReviewCommand spec = %+v", spec)
	}
	if spec.Env["HOME"] != filepath.Join(dataDir, "reviewer-runtime", "review-worker-1", "config") || spec.Env["CONTINUE_CLI_ENABLE_TELEMETRY"] != "0" {
		t.Fatalf("AO-owned environment = %#v", spec.Env)
	}
}

func TestReviewCommandSeedsPrivateProfileFromHostConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	hostConfig := filepath.Join(home, ".continue", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(hostConfig), 0o700); err != nil {
		t.Fatal(err)
	}
	config := []byte("models:\n  - provider: anthropic\n    apiKey: continue-key\n")
	if err := os.WriteFile(hostConfig, config, 0o600); err != nil {
		t.Fatal(err)
	}
	dataDir := t.TempDir()

	spec, err := testReviewer().ReviewCommand(context.Background(), ports.ReviewInvocation{
		DataDir: dataDir, ReviewerID: "review-worker-1", TaskPromptRoot: filepath.Join(dataDir, "prompts"),
		WorkspacePath: "/host/project", Prompt: "task-ref:opaque",
	})
	if err != nil {
		t.Fatal(err)
	}

	copied := filepath.Join(spec.Env["HOME"], ".continue", "config.yaml")
	data, err := os.ReadFile(copied)
	if err != nil {
		t.Fatalf("read copied config: %v", err)
	}
	if string(data) != string(config) {
		t.Fatalf("copied config = %q", string(data))
	}
	info, err := os.Stat(copied)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("copied config mode = %o, want 600", info.Mode().Perm())
	}
}

func TestReviewCommandExportsContinueAPIKey(t *testing.T) {
	t.Setenv("CONTINUE_API_KEY", "continue-key")
	dataDir := t.TempDir()

	spec, err := testReviewer().ReviewCommand(context.Background(), ports.ReviewInvocation{
		DataDir: dataDir, ReviewerID: "review-worker-1", TaskPromptRoot: filepath.Join(dataDir, "prompts"),
		WorkspacePath: "/host/project", Prompt: "task-ref:opaque",
	})
	if err != nil {
		t.Fatal(err)
	}

	if spec.Env["CONTINUE_API_KEY"] != "continue-key" {
		t.Fatal("CONTINUE_API_KEY was not exported into reviewer env")
	}
}

func TestReviewPreflightAcceptsResolvedBinary(t *testing.T) {
	if err := testReviewer().ReviewPreflight(context.Background(), "/host/project"); err != nil {
		t.Fatalf("ReviewPreflight error = %v", err)
	}
}

func TestContainedTUIContractIsExact(t *testing.T) {
	launch := containedCommand(ports.ReviewInvocation{Prompt: "task-ref:opaque-7"})
	wantArgv := []string{"cn", "--config", "/ao/config/continue/config.yaml", "--readonly"}
	if !slices.Equal(launch.Command.Argv, wantArgv) {
		t.Fatalf("argv = %#v, want %#v", launch.Command.Argv, wantArgv)
	}
	if launch.Command.WorkingDirectory != "/ao/empty" {
		t.Fatalf("cwd = %q, want neutral container directory", launch.Command.WorkingDirectory)
	}
	if !launch.ReplaceEnvironment {
		t.Fatal("contained launch must replace, not merge, the host environment")
	}
	if launch.Command.InitialMessage != "" {
		t.Fatalf("current start-only delivery must remain unused, got %q", launch.Command.InitialMessage)
	}
	if launch.PostReadinessMessage != "task-ref:opaque-7" {
		t.Fatalf("post-readiness message = %q", launch.PostReadinessMessage)
	}
	if launch.RestoreSupported {
		t.Fatal("Continue reviewer must not restore a prior native session")
	}
	if slices.ContainsFunc(launch.Command.Argv, func(arg string) bool {
		return strings.Contains(arg, "task-ref") || arg == "--resume" || arg == "--fork"
	}) {
		t.Fatalf("argv contains a task or restore input: %#v", launch.Command.Argv)
	}
}

func TestContainedEnvironmentReplacesHostDiscoveryState(t *testing.T) {
	t.Setenv("HOME", "/host/home")
	t.Setenv("PATH", "/host/bin")
	t.Setenv("ANTHROPIC_API_KEY", "host-secret")

	launch := containedCommand(ports.ReviewInvocation{})
	want := map[string]string{
		"HOME": "/ao/home", "XDG_CONFIG_HOME": "/ao/config",
		"XDG_STATE_HOME": "/ao/state", "XDG_CACHE_HOME": "/ao/cache",
		"TMPDIR": "/ao/tmp", "TMP": "/ao/tmp", "TEMP": "/ao/tmp",
		"PATH": "/usr/local/bin:/usr/bin:/bin", "CONTINUE_CLI_ENABLE_TELEMETRY": "0",
	}
	if len(launch.Command.Env) != len(want) {
		t.Fatalf("replacement env = %#v, want %#v", launch.Command.Env, want)
	}
	for key, value := range want {
		if launch.Command.Env[key] != value {
			t.Errorf("env[%s] = %q, want %q", key, launch.Command.Env[key], value)
		}
	}
	for _, forbidden := range []string{"ANTHROPIC_API_KEY", "OPENAI_API_KEY", "AO_DATA_DIR", "AO_RUN_FILE", "GITHUB_TOKEN", "GH_TOKEN"} {
		if _, ok := launch.Command.Env[forbidden]; ok {
			t.Errorf("replacement env leaks %s", forbidden)
		}
	}
}

func TestAuditedContinuePackagePin(t *testing.T) {
	if packageName != "@continuedev/cli" || packageVersion != "1.5.47" {
		t.Fatalf("package pin = %s@%s", packageName, packageVersion)
	}
	wantIntegrity := "sha512-gtpewV3RoIOD9dyTtKIBi1SY0VOHRu3Ehe7C/mmnswm+j34MPyrcQhQaWj/m+jdfGO4fNIKdrgGIlLso1ULDFw=="
	if packageIntegrity != wantIntegrity {
		t.Fatalf("integrity = %q, want audited registry integrity", packageIntegrity)
	}
}

func TestHostTrustWarningCoversUnsafeInteractiveSurfaces(t *testing.T) {
	// These are independent escape/discovery surfaces in Continue 1.5.47.
	// An explicit config, neutral cwd, and replacement env reduce
	// ambient discovery but do not enforce a security boundary against them.
	risks := []struct {
		name    string
		surface string
	}{
		{"Shift-Tab mode switch", "Shift-Tab can change the interactive permission mode"},
		{"bang shell", "a terminal user can enter ! shell mode"},
		{"Bash tool", "the model can request arbitrary terminal commands"},
		{"Fetch tool", "the model can request outbound network access"},
		{"hooks", "Claude-compatible hooks can execute discovered commands"},
		{"project discovery", "the CLI can inspect configuration relative to cwd"},
		{"rules", "discovered rules can inject untrusted instructions"},
		{"skills", "discovered Markdown skills can expand tool use"},
		{"settings", "user or project settings can change permissions and hooks"},
		{"MCP", "configured MCP servers can spawn processes or access the network"},
	}
	if len(risks) != 10 {
		t.Fatalf("risk audit has %d entries", len(risks))
	}
	for _, risk := range risks {
		if strings.TrimSpace(risk.surface) == "" {
			t.Fatalf("risk %s needs an audited exploit surface", risk.name)
		}
	}
	for _, phrase := range []string{"host-trusted", "without OS isolation", "auto mode"} {
		if !strings.Contains(HostTrustWarning, phrase) {
			t.Fatalf("warning %q missing %q", HostTrustWarning, phrase)
		}
	}
}

func TestLiveReviewMessageReuseAndOneEscapeCancel(t *testing.T) {
	r := testReviewer()
	message, err := r.ReviewMessage(context.Background(), ports.ReviewInvocation{Prompt: "task-ref:opaque-next"})
	if err != nil || message != "task-ref:opaque-next" {
		t.Fatalf("ReviewMessage = %q, %v", message, err)
	}
	cancel, err := r.ReviewCancel(context.Background())
	if err != nil {
		t.Fatalf("ReviewCancel: %v", err)
	}
	if cancel.Mode != ports.ReviewCancelInput || cancel.Input != "\x1b" {
		t.Fatalf("ReviewCancel = %+v, want one Escape", cancel)
	}
}

func TestMethodsRespectCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := testReviewer()
	if _, err := r.ReviewCommand(ctx, ports.ReviewInvocation{}); !errors.Is(err, context.Canceled) {
		t.Errorf("ReviewCommand error = %v", err)
	}
	if err := r.ReviewPreflight(ctx, ""); !errors.Is(err, context.Canceled) {
		t.Errorf("ReviewPreflight error = %v", err)
	}
	if _, err := r.ReviewMessage(ctx, ports.ReviewInvocation{}); !errors.Is(err, context.Canceled) {
		t.Errorf("ReviewMessage error = %v", err)
	}
	if _, err := r.ReviewCancel(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("ReviewCancel error = %v", err)
	}
}
