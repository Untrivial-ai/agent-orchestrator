package goose

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const pinnedSessionHelp = `Start or resume interactive chat sessions

Usage: goose session [OPTIONS] [COMMAND]

Commands:
  list         List all available sessions
  remove       Remove sessions. Runs interactively if no ID, name, or regex is provided.
  export       Export a session
  import       Import a session from JSON, a Claude Code / Codex / Pi .jsonl, or an encrypted Nostr share link
  diagnostics` + "  " + `
  help         Print this message or the help of the given subcommand(s)

Options:
  -n, --name <NAME>
          Specify a name for your chat session. When used with --resume, will resume this specific session if it exists.

      --session-id <SESSION_ID>
          Specify a session ID directly. When used with --resume, will resume this specific session if it exists.

      --path <PATH>
          Legacy parameter for backward compatibility. Extracts session ID from the file path (e.g., '/path/to/20250325_200615.
          jsonl' -> '20250325_200615').

  -r, --resume
          Continue from a previous session. If --name or --session-id is provided, resumes that specific session. Otherwise, resumes the most recently used session.

      --fork
          Create a new session by copying all messages from a previous session. Must be used with --resume. If --name or --session-id is provided, forks that specific session. Otherwise, forks the most recently used session.

      --history
          Show previous messages when resuming a session

      --debug
          When enabled, shows complete tool responses without truncation and full paths.

      --max-tool-repetitions <NUMBER>
          Set a limit on how many times the same tool can be called consecutively with identical parameters. Helps prevent infinite loops.

      --max-turns <NUMBER>
          Set a limit on how many turns (iterations) the agent can take without asking for user input to continue.

      --container <CONTAINER_ID>
          Run extensions (stdio and built-in) inside the specified container. The extension must exist in the container. For built-in extensions, goose must be installed inside the container.

      --with-extension <COMMAND>
          Add stdio extensions from full commands with environment variables. Can be specified multiple times. Format: 'ENV1=val1 ENV2=val2 command args...'

      --with-streamable-http-extension <URL>
          Add streamable HTTP extensions from a URL. Can be specified multiple times. Format: 'url...' or 'url... timeout=100' to set up timeout other than default

      --with-builtin <NAME>
          Add one or more builtin extensions that are bundled with goose by specifying their names, comma-separated

      --no-profile
          Don't load your default extensions, only use CLI-specified extensions

  -h, --help
          Print help (see a summary with '-h')
`

func testReviewer(t *testing.T, version, help string) (*Reviewer, *[][]string) {
	t.Helper()
	calls := &[][]string{}
	return &Reviewer{resolveBinary: func(context.Context) (string, error) { return "/opt/ao/bin/goose", nil }, run: func(_ context.Context, _ map[string]string, binary string, args ...string) ([]byte, error) {
		*calls = append(*calls, append([]string{binary}, args...))
		if slices.Equal(args, []string{"--version"}) {
			return []byte(version), nil
		}
		return []byte(help), nil
	}}, calls
}

func TestReviewCommandLaunchesHostTrustedInteractiveRun(t *testing.T) {
	r, _ := testReviewer(t, pinnedVersion, pinnedSessionHelp)
	dataDir := t.TempDir()
	hostHome := t.TempDir()
	t.Setenv("HOME", hostHome)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("XDG_CACHE_HOME", "")
	taskPromptFile := "/host/ao/prompts/reviewer/requests/batch-1/run-1/task.md"
	spec, err := r.ReviewCommand(context.Background(), ports.ReviewInvocation{
		DataDir:          dataDir,
		ReviewerID:       "review-worker-1",
		WorkspacePath:    "/host/worktree",
		TaskPromptRoot:   "/host/ao/prompts/reviewer",
		TaskPromptFile:   taskPromptFile,
		SystemPromptFile: "/host/ao/prompts/reviewer/system.md",
		Prompt:           "Read AO task ref 4f09.",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/opt/ao/bin/goose", "run", "--instructions", taskPromptFile, "--interactive"}
	if !reflect.DeepEqual(spec.Argv, want) || spec.InitialMessage != "" || spec.WorkingDirectory != "/host/worktree" {
		t.Fatalf("ReviewCommand spec = %+v", spec)
	}
	if spec.Env["GOOSE_MODE"] != "auto" || spec.Env["GOOSE_SYSTEM_PROMPT_FILE_PATH"] != "/host/ao/prompts/reviewer/system.md" {
		t.Fatalf("ReviewCommand env = %#v", spec.Env)
	}
	if spec.Env["HOME"] != hostHome || spec.Env["XDG_CONFIG_HOME"] != filepath.Join(hostHome, ".config") || spec.Env["XDG_DATA_HOME"] != filepath.Join(hostHome, ".local", "share") || spec.Env["XDG_STATE_HOME"] != filepath.Join(hostHome, ".local", "state") {
		t.Fatalf("host Goose profile = %#v", spec.Env)
	}
	if spec.Env["TMPDIR"] != filepath.Join(dataDir, "reviewer-runtime", "review-worker-1", "tmp") {
		t.Fatalf("reviewer temp dir = %#v", spec.Env)
	}
	if _, ok := spec.Env["GOOSE_PATH_ROOT"]; ok {
		t.Fatalf("GOOSE_PATH_ROOT should not override host Goose state: %#v", spec.Env)
	}
}

func TestReviewCommandRejectsMissingTaskPromptFile(t *testing.T) {
	r, _ := testReviewer(t, pinnedVersion, pinnedSessionHelp)
	_, err := r.ReviewCommand(context.Background(), ports.ReviewInvocation{
		DataDir:        t.TempDir(),
		ReviewerID:     "review-worker-1",
		WorkspacePath:  "/host/worktree",
		TaskPromptRoot: "/host/ao/prompts/reviewer",
		Prompt:         "Read AO task ref 4f09.",
	})
	if err == nil || !strings.Contains(err.Error(), "task prompt file") {
		t.Fatalf("ReviewCommand error = %v, want task prompt file", err)
	}
}

func TestReviewCommandPreservesExplicitHostGooseXDGProfile(t *testing.T) {
	hostHome := t.TempDir()
	hostConfig := t.TempDir()
	hostData := t.TempDir()
	hostState := t.TempDir()
	hostCache := t.TempDir()
	t.Setenv("HOME", hostHome)
	t.Setenv("XDG_CONFIG_HOME", hostConfig)
	t.Setenv("XDG_DATA_HOME", hostData)
	t.Setenv("XDG_STATE_HOME", hostState)
	t.Setenv("XDG_CACHE_HOME", hostCache)
	r, _ := testReviewer(t, pinnedVersion, pinnedSessionHelp)
	dataDir := t.TempDir()

	spec, err := r.ReviewCommand(context.Background(), ports.ReviewInvocation{
		DataDir:        dataDir,
		ReviewerID:     "review-worker-1",
		WorkspacePath:  "/host/worktree",
		TaskPromptRoot: "/host/ao/prompts/reviewer",
		TaskPromptFile: "/host/ao/prompts/reviewer/requests/batch-1/run-1/task.md",
		Prompt:         "Read AO task ref 4f09.",
	})
	if err != nil {
		t.Fatal(err)
	}

	if spec.Env["HOME"] != hostHome || spec.Env["XDG_CONFIG_HOME"] != hostConfig || spec.Env["XDG_DATA_HOME"] != hostData || spec.Env["XDG_STATE_HOME"] != hostState || spec.Env["XDG_CACHE_HOME"] != hostCache {
		t.Fatalf("ReviewCommand env = %#v", spec.Env)
	}
}

func TestReviewCommandAliasesZhipuAPIKeyForGooseProvider(t *testing.T) {
	t.Setenv("ZHIPUAI_API_KEY", "host-key")
	t.Setenv("ZHIPU_API_KEY", "")
	inv := ports.ReviewInvocation{
		DataDir:        t.TempDir(),
		ReviewerID:     "review-worker-1",
		WorkspacePath:  "/host/worktree",
		TaskPromptRoot: "/host/ao/prompts/reviewer",
		TaskPromptFile: "/host/ao/prompts/reviewer/requests/batch-1/run-1/task.md",
		Prompt:         "Read AO task ref 4f09.",
	}

	r, _ := testReviewer(t, pinnedVersion, pinnedSessionHelp)
	spec, err := r.ReviewCommand(context.Background(), inv)
	if err != nil {
		t.Fatal(err)
	}
	if spec.Env["ZHIPU_API_KEY"] != "host-key" {
		t.Fatalf("ZHIPU_API_KEY = %q, want alias from ZHIPUAI_API_KEY", spec.Env["ZHIPU_API_KEY"])
	}
}

func TestCompatibilityProbePinsExactVersionAndHelp(t *testing.T) {
	r, calls := testReviewer(t, " 1.38.0\n", pinnedSessionHelp)
	if err := r.verifyCompatibility(context.Background(), gooseBinary, map[string]string{"HOME": "/ao/profile"}); err != nil {
		t.Fatalf("verifyCompatibility error = %v", err)
	}
	wantCalls := [][]string{
		{gooseBinary, "--version"},
		{gooseBinary, "session", "--help"},
	}
	if !reflect.DeepEqual(*calls, wantCalls) {
		t.Fatalf("preflight calls = %#v, want %#v", *calls, wantCalls)
	}
}

func TestCompatibilityProbeFailsClosedOnVersionOrHelpDrift(t *testing.T) {
	for _, tc := range []struct {
		name    string
		version string
		help    string
		want    string
	}{
		{name: "newer version", version: "1.38.1", help: pinnedSessionHelp, want: "exactly version 1.38.0"},
		{name: "version decoration", version: "v1.38.0", help: pinnedSessionHelp, want: "exactly version 1.38.0"},
		{name: "help addition", version: "1.38.0", help: pinnedSessionHelp + "      --unsafe-new-mode\n", want: "help contract drifted"},
		{name: "help removal", version: "1.38.0", help: strings.Replace(pinnedSessionHelp, "      --no-profile\n", "", 1), want: "help contract drifted"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := testReviewer(t, tc.version, tc.help)
			err := r.verifyCompatibility(context.Background(), gooseBinary, nil)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("ReviewPreflight error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestContainedCommandIsOnlyPinnedLongLivedTUI(t *testing.T) {
	const taskRef = "Read AO review task ref 4f09."
	spec := containedCommand(taskRef)
	wantArgv := []string{
		"/opt/ao/bin/goose", "session", "--no-profile",
		"--with-extension", "/opt/ao/bin/ao-review-gateway-mcp",
	}
	if !reflect.DeepEqual(spec.Argv, wantArgv) {
		t.Fatalf("argv = %#v, want %#v", spec.Argv, wantArgv)
	}
	if slices.Contains(spec.Argv, taskRef) || spec.InitialMessage != taskRef || !spec.InjectAfterReadiness {
		t.Fatalf("task must be injected after readiness: argv=%#v initial=%q", spec.Argv, spec.InitialMessage)
	}
	for _, forbidden := range []string{
		"run", "review", "tui", "json", "acp", "rpc", "serve", "--resume",
		"--with-streamable-http-extension", "--with-builtin", "developer", "edit",
		"--recipe", "--text", "--instructions",
	} {
		if slices.Contains(spec.Argv, forbidden) {
			t.Fatalf("contained argv includes forbidden mode or capability %q: %#v", forbidden, spec.Argv)
		}
	}
}

func TestReplacementEnvironmentBlocksHostProfileCredentialsAndDiscovery(t *testing.T) {
	hostHome := t.TempDir()
	project := t.TempDir()

	// These files model every implicit host/project surface Goose 1.38.0 can
	// discover. The contained contract must point at none of their roots.
	hostilePaths := []string{
		filepath.Join(hostHome, ".config", "goose", "config.yaml"),              // profile: developer/edit extension + auto mode
		filepath.Join(hostHome, ".agents", "plugins", "hostile", "plugin.json"), // global extension
		filepath.Join(hostHome, ".agents", "skills", "hostile", "SKILL.md"),     // global skill
		filepath.Join(project, ".agents", "plugins", "hostile", "plugin.json"),  // project extension
		filepath.Join(project, ".agents", "skills", "hostile", "SKILL.md"),      // project skill
		filepath.Join(project, ".goose", "skills", "hostile", "SKILL.md"),       // legacy project skill
		filepath.Join(project, ".goosehints"),                                   // project instructions
		filepath.Join(project, "hostile-recipe.yaml"),                           // recipe
	}
	for _, path := range hostilePaths {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("hostile"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	for key, value := range map[string]string{
		"HOME": hostHome, "USERPROFILE": hostHome,
		"GOOSE_PATH_ROOT": filepath.Join(hostHome, "goose"),
		"GOOSE_MODE":      "auto", "GOOSE_PROVIDER": "anthropic", "GOOSE_MODEL": "host-model",
		"ANTHROPIC_API_KEY": "host-secret", "OPENAI_API_KEY": "host-secret",
		"GOOGLE_APPLICATION_CREDENTIALS": "/host/google.json",
		"AWS_PROFILE":                    "host-profile",
		"EDITOR":                         filepath.Join(hostHome, "hostile-editor"),
		"GH_TOKEN":                       "host-secret",
		"GOOSE_RECIPE_GITHUB_REPO":       "host/recipes",
		"GOOSE_PROMPT_EDITOR":            filepath.Join(hostHome, "hostile-prompt-editor"),
		"PATH":                           filepath.Join(hostHome, "hostile-bin"),
		"VISUAL":                         filepath.Join(hostHome, "hostile-visual"),
	} {
		t.Setenv(key, value)
	}

	spec := containedCommand("opaque-task-ref")
	env := environmentMap(t, spec.Environment)
	if spec.WorkingDirectory != neutralWorkingDir || spec.WorkingDirectory == project {
		t.Fatalf("working directory = %q, want neutral OCI directory", spec.WorkingDirectory)
	}
	if env["HOME"] != isolatedHome || env["GOOSE_PATH_ROOT"] != isolatedGooseRoot {
		t.Fatalf("discovery roots are not isolated: %#v", env)
	}
	if env["GOOSE_DISABLE_KEYRING"] != "1" || env["CONTEXT_FILE_NAMES"] != "[]" {
		t.Fatalf("keyring/project hints not disabled: %#v", env)
	}
	if env["GOOSE_PROVIDER"] != "openai" || env["GOOSE_MODEL"] != "ao-reviewer" || env["OPENAI_BASE_URL"] != modelBrokerHost {
		t.Fatalf("model is not broker-only: %#v", env)
	}
	if got := strings.TrimRight(env["OPENAI_BASE_URL"], "/") + "/chat/completions"; got != "http://ao-review-model-broker/v1/chat/completions" || strings.Count(got, "/v1/") != 1 {
		t.Fatalf("broker request URL = %q, want exactly one /v1 segment", got)
	}
	if env["GOOSE_SYSTEM_PROMPT_FILE_PATH"] != systemPromptPath || !strings.HasPrefix(systemPromptPath, containedRoot+"/") || !slices.Equal(spec.ReadOnlyFiles, []string{systemPromptPath}) {
		t.Fatalf("system prompt is not the contained AO-owned path: %#v", env)
	}
	if env["GOOSE_MODE"] != "auto" || env["GOOSE_TELEMETRY_OFF"] != "1" || env["GOOSE_DISABLE_SESSION_NAMING"] != "true" {
		t.Fatalf("Goose contained controls are not pinned: %#v", env)
	}
	for _, forbidden := range []string{
		"USERPROFILE", "ANTHROPIC_API_KEY", "OPENAI_API_KEY", "OPENAI_HOST",
		"GOOGLE_APPLICATION_CREDENTIALS", "AWS_PROFILE", "GH_TOKEN",
		"GOOSE_PROMPT_EDITOR", "VISUAL", "EDITOR", "GOOSE_RECIPE_GITHUB_REPO",
	} {
		if _, ok := env[forbidden]; ok {
			t.Fatalf("replacement environment inherited %s", forbidden)
		}
	}
	if got, want := len(env), 14; got != want {
		t.Fatalf("replacement environment has %d keys, want exact allowlist of %d: %#v", got, want, env)
	}
	for _, path := range hostilePaths {
		if strings.HasPrefix(path, spec.WorkingDirectory) || strings.HasPrefix(path, env["HOME"]) || strings.HasPrefix(path, env["GOOSE_PATH_ROOT"]) {
			t.Fatalf("hostile discovery path %q is visible through contained roots", path)
		}
	}
}

func TestReviewMessageReusesLiveProcessAndCancelIsOneCtrlC(t *testing.T) {
	r := New()
	if r.ReviewProcessReusable() {
		t.Fatal("Goose reviewer tasks must launch fresh with their task file")
	}
	message, err := r.ReviewMessage(context.Background(), ports.ReviewInvocation{Prompt: "opaque-task-ref-2"})
	if err != nil || message != "opaque-task-ref-2" {
		t.Fatalf("ReviewMessage = %q, %v", message, err)
	}
	cancel, err := r.ReviewCancel(context.Background())
	if err != nil || cancel.Mode != ports.ReviewCancelInterrupt || cancel.Interrupts != 1 || cancel.Input != "" {
		t.Fatalf("ReviewCancel = %+v, %v", cancel, err)
	}
}

func TestGooseReviewerIdentityAndHostTrustWarning(t *testing.T) {
	if New().Harness() != HarnessID || !HarnessID.IsKnown() {
		t.Fatalf("reviewer harness is not enabled: %q", New().Harness())
	}
	for _, phrase := range []string{"host-trusted", "developer tools", "without OS or network isolation"} {
		if !strings.Contains(HostTrustWarning, phrase) {
			t.Fatalf("warning %q missing %q", HostTrustWarning, phrase)
		}
	}
}

func environmentMap(t *testing.T, entries []string) map[string]string {
	t.Helper()
	result := make(map[string]string, len(entries))
	for _, entry := range entries {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || key == "" {
			t.Fatalf("invalid replacement environment entry %q", entry)
		}
		if _, duplicate := result[key]; duplicate {
			t.Fatalf("duplicate replacement environment key %q", key)
		}
		result[key] = value
	}
	return result
}
