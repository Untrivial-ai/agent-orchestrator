package vibe

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func testReviewer(t *testing.T, version, help string) *Reviewer {
	t.Helper()
	return &Reviewer{
		dataDir:       t.TempDir(),
		resolveBinary: func(context.Context) (string, error) { return "/opt/vibe/bin/vibe", nil },
		run: func(_ context.Context, _ map[string]string, _ string, args ...string) ([]byte, error) {
			switch {
			case slices.Equal(args, []string{"--version"}):
				return []byte(version), nil
			case slices.Equal(args, []string{"--help"}):
				return []byte(help), nil
			default:
				return nil, errors.New("unexpected Vibe invocation")
			}
		},
	}
}

func invocation(t *testing.T) ports.ReviewInvocation {
	t.Helper()
	root := t.TempDir()
	return ports.ReviewInvocation{
		DataDir:         filepath.Join(root, "ao-data"),
		ReviewerID:      "review-worker-1",
		WorkerSessionID: "worker-1",
		WorkspacePath:   filepath.Join(root, "checkout"),
		TaskPromptRoot:  filepath.Join(root, "prompts"),
		TaskPromptFile:  filepath.Join(root, "prompts", "task.md"),
		Prompt:          "Open AO task capability `task-ref-7f18`.",
	}
}

func TestReviewCommandLaunchesHostTrustedAutoApproveTUI(t *testing.T) {
	inv := invocation(t)
	r := testReviewer(t, "vibe 2.23.2", strings.Join(requiredFlags, "\n"))
	if r.ReviewProcessReusable() {
		t.Fatal("Vibe reviewer tasks must launch fresh with their initial prompt")
	}
	spec, err := r.ReviewCommand(context.Background(), inv)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/opt/vibe/bin/vibe", "--trust", "--workdir", inv.WorkspacePath, "--add-dir", inv.TaskPromptRoot, "--agent", "auto-approve", inv.Prompt}
	if !reflect.DeepEqual(spec.Argv, want) || spec.InitialMessage != "" || spec.WorkingDirectory != inv.WorkspacePath {
		t.Fatalf("ReviewCommand spec = %+v, want argv %#v", spec, want)
	}
	if spec.Env["VIBE_HOME"] != filepath.Join(inv.DataDir, "reviewer-runtime", inv.ReviewerID, "config") || spec.Env["AO_DATA_DIR"] != inv.DataDir {
		t.Fatalf("AO-owned Vibe profile = %#v", spec.Env)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := testReviewer(t, "vibe 2.23.2", strings.Join(requiredFlags, "\n")).ReviewCommand(ctx, inv); !errors.Is(err, context.Canceled) {
		t.Fatalf("ReviewCommand canceled context err = %v", err)
	}
}

func TestReviewCommandSeedsPrivateProfileFromHostVibeHome(t *testing.T) {
	hostVibeHome := t.TempDir()
	t.Setenv("VIBE_HOME", hostVibeHome)
	hostConfig := []byte("api_key_env_var = \"VIBE_CODE_API_KEY\"\n")
	hostEnv := []byte("VIBE_CODE_API_KEY=test-key\n")
	if err := os.WriteFile(filepath.Join(hostVibeHome, "config.toml"), hostConfig, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hostVibeHome, ".env"), hostEnv, 0o600); err != nil {
		t.Fatal(err)
	}
	inv := invocation(t)

	spec, err := testReviewer(t, "vibe 2.23.2", strings.Join(requiredFlags, "\n")).ReviewCommand(context.Background(), inv)
	if err != nil {
		t.Fatal(err)
	}

	for name, want := range map[string][]byte{
		"config.toml": hostConfig,
		".env":        hostEnv,
	} {
		copied := filepath.Join(spec.Env["VIBE_HOME"], name)
		data, err := os.ReadFile(copied)
		if err != nil {
			t.Fatalf("read copied %s: %v", name, err)
		}
		if string(data) != string(want) {
			t.Fatalf("copied %s = %q", name, string(data))
		}
		info, err := os.Stat(copied)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("copied %s mode = %o, want 600", name, info.Mode().Perm())
		}
	}
}

func TestContainedInteractiveSpecUsesExactTUIArgvAndPostReadinessTask(t *testing.T) {
	r := testReviewer(t, "", "")
	inv := invocation(t)
	staged, err := r.containedInteractiveSpec(context.Background(), inv)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/opt/vibe/bin/vibe", "--trust", "--workdir", staged.Command.WorkingDirectory, "--agent", "plan"}
	if !reflect.DeepEqual(staged.Command.Argv, want) {
		t.Fatalf("argv = %#v, want %#v", staged.Command.Argv, want)
	}
	for _, forbidden := range []string{"-p", "--prompt", "--auto-approve", "--yolo", "--add-dir", inv.Prompt, inv.WorkspacePath} {
		if slices.Contains(staged.Command.Argv, forbidden) {
			t.Fatalf("interactive argv contains forbidden %q: %#v", forbidden, staged.Command.Argv)
		}
	}
	if staged.Command.InitialMessage != inv.Prompt {
		t.Fatalf("initial message = %q, want opaque task reference", staged.Command.InitialMessage)
	}
	if staged.Command.WorkingDirectory == inv.WorkspacePath || !strings.HasPrefix(staged.Command.WorkingDirectory, r.dataDir+string(filepath.Separator)) {
		t.Fatalf("working directory = %q", staged.Command.WorkingDirectory)
	}
}

func TestContainedInteractiveSpecRequiresReplacementEnvironmentAndNeutralDiscovery(t *testing.T) {
	t.Setenv("VIBE_DEFAULT_AGENT", "auto-approve")
	t.Setenv("VIBE_MCP_SERVERS", `[{"name":"hostile"}]`)
	t.Setenv("EDITOR", "/tmp/hostile-editor")
	r := testReviewer(t, "", "")
	staged, err := r.containedInteractiveSpec(context.Background(), invocation(t))
	if err != nil {
		t.Fatal(err)
	}
	if !staged.EnvironmentReplacement {
		t.Fatal("environment must replace, not overlay, the daemon environment")
	}
	wantKeys := []string{"HOME", "TEMP", "TMP", "TMPDIR", "VIBE_HOME", "XDG_CACHE_HOME", "XDG_CONFIG_HOME", "XDG_STATE_HOME"}
	gotKeys := make([]string, 0, len(staged.Command.Env))
	for key := range staged.Command.Env {
		gotKeys = append(gotKeys, key)
	}
	slices.Sort(gotKeys)
	if !slices.Equal(gotKeys, wantKeys) {
		t.Fatalf("replacement env keys = %#v, want %#v", gotKeys, wantKeys)
	}
	for _, forbidden := range []string{"EDITOR", "VISUAL", "SHELL", "VIBE_DEFAULT_AGENT", "VIBE_MCP_SERVERS", "MISTRAL_API_KEY"} {
		if _, ok := staged.Command.Env[forbidden]; ok {
			t.Fatalf("replacement env inherited %s", forbidden)
		}
	}
	vibeHome := staged.Command.Env["VIBE_HOME"]
	if !strings.HasPrefix(vibeHome, r.dataDir+string(filepath.Separator)) {
		t.Fatalf("VIBE_HOME = %q", vibeHome)
	}
	config, err := os.ReadFile(filepath.Join(vibeHome, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(config)
	for _, required := range []string{
		`default_agent = "plan"`,
		`enabled_agents = ["plan"]`,
		`enabled_tools = ["grep", "read"]`,
		`disabled_skills = ["*"]`,
		`mcp_servers = []`,
	} {
		if !strings.Contains(body, required) {
			t.Fatalf("neutral config missing %q:\n%s", required, body)
		}
	}
	for _, forbidden := range []string{"bash", "edit", "write_file", "auto-approve", "accept-edits"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("neutral config enables risky surface %q:\n%s", forbidden, body)
		}
	}
}

func TestContainedInteractiveSpecModelsShellEditorAndApprovalToggleRisks(t *testing.T) {
	staged, err := testReviewer(t, "", "").containedInteractiveSpec(context.Background(), invocation(t))
	if err != nil {
		t.Fatal(err)
	}
	// Vibe's `!` shell remains reachable from the interactive prompt,
	// regardless of the plan agent's tool policy. This is why the command may
	// never launch without OS containment.
	if !slices.Equal(staged.UncontainedRisks, []string{
		"terminal shell escape",
		"external editor",
		"approval-mode toggle",
		"layered config discovery",
	}) {
		t.Fatalf("uncontained risks = %#v", staged.UncontainedRisks)
	}
	// Ctrl+G launches an external editor and must be swallowed by the future
	// terminal-input filter. Shift+Tab is constrained separately by the single
	// enabled plan agent in the neutral config.
	if !slices.Equal(staged.BlockedInput, []string{"\x07"}) {
		t.Fatalf("blocked input = %#v, want one Ctrl+G", staged.BlockedInput)
	}
	if !slices.Contains(staged.Command.Argv, "plan") || slices.Contains(staged.Command.Argv, "auto-approve") {
		t.Fatalf("approval policy argv = %#v", staged.Command.Argv)
	}
}

func TestCompatibilityProbeAcceptsMinimumVibe2232AndInteractiveFlags(t *testing.T) {
	help := strings.Join(requiredFlags, "\n")
	for _, version := range []string{"vibe 2.23.2\n", "vibe 2.24.0"} {
		if err := testReviewer(t, version, help).verifyCompatibility(context.Background(), "/opt/vibe/bin/vibe", map[string]string{"HOME": "/ao/profile"}); err != nil {
			t.Fatalf("version %q verifyCompatibility: %v", version, err)
		}
	}
	for _, version := range []string{"vibe 2.23.1", "vibe unknown"} {
		if err := testReviewer(t, version, help).verifyCompatibility(context.Background(), "/opt/vibe/bin/vibe", nil); err == nil || !strings.Contains(err.Error(), pinnedVersion) {
			t.Fatalf("version %q err = %v, want minimum-version rejection", version, err)
		}
	}
	missing := testReviewer(t, "vibe 2.23.2", strings.Replace(help, "--workdir", "", 1))
	if err := missing.verifyCompatibility(context.Background(), "/opt/vibe/bin/vibe", nil); err == nil || !strings.Contains(err.Error(), "--workdir") {
		t.Fatalf("missing flag err = %v", err)
	}
}

func TestProductionPreflightResolvesHostTrustedCLI(t *testing.T) {
	if err := testReviewer(t, "", "").ReviewPreflight(context.Background(), "/worker"); err != nil {
		t.Fatalf("ReviewPreflight err = %v", err)
	}
}

func TestReviewMessageReusesLivePaneAndCancelUsesOneEscape(t *testing.T) {
	r := testReviewer(t, "", "")
	message, err := r.ReviewMessage(context.Background(), ports.ReviewInvocation{Prompt: "task-ref-next"})
	if err != nil || message != "task-ref-next" {
		t.Fatalf("ReviewMessage = %q, %v", message, err)
	}
	cancel, err := r.ReviewCancel(context.Background())
	if err != nil || cancel.Mode != ports.ReviewCancelInput || cancel.Input != "\x1b" {
		t.Fatalf("ReviewCancel = %+v, %v", cancel, err)
	}
}

func TestReviewerHarnessAndHostTrustWarning(t *testing.T) {
	if New().Harness() != HarnessID || !HarnessID.IsKnown() {
		t.Fatal("Vibe reviewer must be domain registered")
	}
	for _, phrase := range []string{"host-trusted", "shell", "without OS isolation"} {
		if !strings.Contains(HostTrustWarning, phrase) {
			t.Fatalf("warning %q missing %q", HostTrustWarning, phrase)
		}
	}
}

func TestContainedInteractiveSpecRejectsUnsafePaths(t *testing.T) {
	r := testReviewer(t, "", "")
	r.resolveBinary = func(context.Context) (string, error) { return "vibe", nil }
	if _, err := r.containedInteractiveSpec(context.Background(), invocation(t)); err == nil {
		t.Fatal("relative executable accepted")
	}
	r.resolveBinary = func(context.Context) (string, error) { return "/opt/vibe/bin/vibe", nil }
	inv := invocation(t)
	inv.ReviewerID = "../escape"
	if _, err := r.containedInteractiveSpec(context.Background(), inv); err == nil {
		t.Fatal("unsafe reviewer id accepted")
	}
}
