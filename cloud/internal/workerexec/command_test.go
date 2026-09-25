package workerexec

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/pkg/agentruntime"
	"github.com/aoagents/agent-orchestrator/cloud/internal/worker"
)

func TestCodexArgsMatchesCloudSessionPermissionMode(t *testing.T) {
	tests := []struct {
		name string
		mode string
		want []string
	}{
		{
			name: "trusted keeps the TUI yolo policy",
			mode: "trusted",
			want: []string{
				"exec", "--json", "--skip-git-repo-check", "--dangerously-bypass-hook-trust",
				"--dangerously-bypass-approvals-and-sandbox", "--", "describe the change",
			},
		},
		{
			name: "standard stays workspace scoped",
			mode: "standard",
			want: []string{
				"exec", "--json", "--skip-git-repo-check", "--dangerously-bypass-hook-trust",
				"--sandbox", "workspace-write", "--ask-for-approval", "on-request",
				"-c", `approvals_reviewer="auto_review"`, "--", "describe the change",
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := codexArgs(worker.Turn{Mode: test.mode, Prompt: "describe the change"})
			if err != nil {
				t.Fatalf("codex args: %v", err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("args = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestCodexArgsResumesNativeConversation(t *testing.T) {
	got, err := codexArgs(worker.Turn{
		Mode:           "trusted",
		Prompt:         "continue",
		AgentSessionID: "thread-1",
	})
	if err != nil {
		t.Fatalf("codex args: %v", err)
	}
	wantTail := []string{"resume", "thread-1", "--", "continue"}
	if !reflect.DeepEqual(got[len(got)-len(wantTail):], wantTail) {
		t.Fatalf("args tail = %#v, want %#v", got, wantTail)
	}
}

func TestCodexArgsAppliesSelectedModelAndReasoningEffort(t *testing.T) {
	got, err := codexArgs(worker.Turn{
		Mode: "trusted", Prompt: "continue", AgentSessionID: "thread-1",
		Model: "codex-test", ReasoningEffort: "high",
	})
	if err != nil {
		t.Fatalf("codex args: %v", err)
	}
	want := []string{
		"exec", "--json", "--skip-git-repo-check", "--dangerously-bypass-hook-trust",
		"--dangerously-bypass-approvals-and-sandbox", "-m", "codex-test",
		"-c", "model_reasoning_effort=high", "resume", "thread-1", "--", "continue",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("args = %#v, want %#v", got, want)
	}
}

func TestClaudeArgsStreamsJSONWithVerboseAndResume(t *testing.T) {
	got, err := claudeArgs(worker.Turn{
		Mode:           "trusted",
		Prompt:         "describe the change",
		AgentSessionID: "claude-native-1",
	})
	if err != nil {
		t.Fatalf("claude args: %v", err)
	}
	// Claude Code hard-errors on `--print --output-format stream-json` without
	// --verbose, so the headless Chat adapter depends on this exact prefix.
	wantPrefix := []string{"--print", "--output-format", "stream-json", "--verbose"}
	if !reflect.DeepEqual(got[:len(wantPrefix)], wantPrefix) {
		t.Fatalf("args prefix = %#v, want %#v", got, wantPrefix)
	}
	wantTail := []string{"--dangerously-skip-permissions", "--resume", "claude-native-1", "describe the change"}
	if !reflect.DeepEqual(got[len(got)-len(wantTail):], wantTail) {
		t.Fatalf("args tail = %#v, want %#v", got, wantTail)
	}
}

func TestClaudeArgsOmitsResumeWithoutNativeConversation(t *testing.T) {
	got, err := claudeArgs(worker.Turn{Mode: "standard", Prompt: "fresh"})
	if err != nil {
		t.Fatalf("claude args: %v", err)
	}
	for _, arg := range got {
		if arg == "--resume" {
			t.Fatalf("fresh Claude turn resumed a nonexistent conversation: %#v", got)
		}
	}
}

func TestCursorArgsStreamsJSONWithForceAndResume(t *testing.T) {
	got, err := cursorArgs(worker.Turn{
		Mode:           "trusted",
		Prompt:         "describe the change",
		AgentSessionID: "cursor-native-1",
	})
	if err != nil {
		t.Fatalf("cursor args: %v", err)
	}
	want := []string{
		"agent", "--print", "--output-format", "stream-json", "--force",
		"--resume", "cursor-native-1", "describe the change",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("args = %#v, want %#v", got, want)
	}
}

func TestCursorArgsOmitsResumeWithoutNativeConversation(t *testing.T) {
	got, err := cursorArgs(worker.Turn{Mode: "standard", Prompt: "fresh"})
	if err != nil {
		t.Fatalf("cursor args: %v", err)
	}
	want := []string{"agent", "--print", "--output-format", "stream-json", "fresh"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("args = %#v, want %#v", got, want)
	}
}

func TestBuildRejectsMismatchedCredential(t *testing.T) {
	builder := HarnessBuilder{Binaries: map[string]string{"codex": "/bin/echo"}, DataDir: t.TempDir()}
	_, err := builder.Build(
		context.Background(),
		worker.Turn{Harness: "codex", Mode: "standard", Prompt: "hi"},
		worker.CredentialResponse{Provider: "claude-code", CredentialType: "api_key", Secret: "s"},
		t.TempDir(),
	)
	if err == nil {
		t.Fatal("expected a credential/provider mismatch to fail closed")
	}
}

func TestBuildHeadlessDropsUnverifiableClaudeResume(t *testing.T) {
	dataDir := t.TempDir()
	builder := HarnessBuilder{Binaries: map[string]string{"claude-code": "/bin/echo"}, DataDir: dataDir}
	// The control plane can hold a stale native id after a worker is
	// reprovisioned; without the JSONL transcript on disk, Claude must start
	// fresh rather than --resume a conversation that does not exist.
	command, err := builder.Build(
		context.Background(),
		worker.Turn{Harness: "claude-code", Mode: "trusted", Prompt: "hi", AgentSessionID: "stale-claude"},
		worker.CredentialResponse{Provider: "claude-code", CredentialType: "api_key", Secret: "s"},
		t.TempDir(),
	)
	if err != nil {
		t.Fatalf("build headless claude command: %v", err)
	}
	for _, arg := range command.Args {
		if arg == "stale-claude" {
			t.Fatalf("headless Claude resumed an unavailable conversation: %#v", command.Args)
		}
	}
	if command.Env["CLAUDE_CONFIG_DIR"] == "" {
		t.Fatal("expected CLAUDE_CONFIG_DIR to scope the worker's Claude state")
	}
	if command.Env["ANTHROPIC_API_KEY"] != "s" {
		t.Fatalf("credential env = %q, want the api key", command.Env["ANTHROPIC_API_KEY"])
	}
}

func TestBuildInteractiveMarksTUISourceForHookProjection(t *testing.T) {
	workspace := t.TempDir()
	builder := HarnessBuilder{
		Binaries: map[string]string{"codex": "/bin/echo"},
		DataDir:  t.TempDir(),
		CodexLogin: func(string, string, string, string) error {
			return nil
		},
	}
	command, err := builder.BuildInteractive(
		worker.LaunchContext{Harness: "codex", SessionID: "session-1", Mode: "trusted"},
		worker.CredentialResponse{Provider: "codex", CredentialType: "api_key", Secret: "test-secret"},
		workspace,
	)
	if err != nil {
		t.Fatalf("build interactive command: %v", err)
	}
	if command.Env["AO_CLOUD_SOURCE_INTERFACE"] != "tui" {
		t.Fatalf("source interface env = %q, want tui", command.Env["AO_CLOUD_SOURCE_INTERFACE"])
	}
}

func TestInteractiveRestoreIdentityDoesNotInferFreshClaudeConversation(t *testing.T) {
	dataDir := t.TempDir()
	launch := worker.LaunchContext{Harness: "claude-code", SessionID: "session-1"}
	identity := agentruntime.ClaudeSessionID(launch.SessionID)
	path := filepath.Join(dataDir, "claude", "projects", "workspace", identity+".jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("create Claude project directory: %v", err)
	}
	if err := os.WriteFile(path, []byte("stale conversation"), 0o600); err != nil {
		t.Fatalf("write stale Claude conversation: %v", err)
	}

	got := (HarnessBuilder{DataDir: dataDir}).interactiveRestoreIdentity(launch)
	if got != "" {
		t.Fatalf("fresh Claude launch identity = %q, want empty", got)
	}
}

func TestInteractiveRestoreIdentityUsesExplicitClaudeConversation(t *testing.T) {
	dataDir := t.TempDir()
	launch := worker.LaunchContext{
		Harness:        "claude-code",
		SessionID:      "session-1",
		AgentSessionID: "native-chat",
	}
	path := filepath.Join(dataDir, "claude", "projects", "workspace", launch.AgentSessionID+".jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("create Claude project directory: %v", err)
	}
	if err := os.WriteFile(path, []byte("native conversation"), 0o600); err != nil {
		t.Fatalf("write Claude conversation: %v", err)
	}

	got := (HarnessBuilder{DataDir: dataDir}).interactiveRestoreIdentity(launch)
	if got != launch.AgentSessionID {
		t.Fatalf("explicit Claude launch identity = %q, want %q", got, launch.AgentSessionID)
	}
}

func TestBuildInteractiveRestoresClaudeConversationFromDurableConfig(t *testing.T) {
	configDir := filepath.Join(t.TempDir(), "claude")
	identity := "99a68dd6-2ad8-4fd8-9ea7-d833ceb2914e"
	conversation := filepath.Join(configDir, "projects", "repository", identity+".jsonl")
	if err := os.MkdirAll(filepath.Dir(conversation), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(conversation, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAUDE_CONFIG_DIR", configDir)
	command, err := (HarnessBuilder{DataDir: t.TempDir()}).BuildInteractive(worker.LaunchContext{
		SessionID: "session-1", Harness: "claude-code", AgentSessionID: identity,
		Mode: "standard",
	}, worker.CredentialResponse{
		Provider: "claude-code", CredentialType: "api_key", Secret: "secret",
	}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if !containsAdjacent(command.Args, "--resume", identity) {
		t.Fatalf("restore args missing from %#v", command.Args)
	}
	if slices.Contains(command.Args, "--session-id") {
		t.Fatalf("fresh-launch identity present in restore command %#v", command.Args)
	}
	if command.Env["CLAUDE_CONFIG_DIR"] != configDir {
		t.Errorf("CLAUDE_CONFIG_DIR = %q", command.Env["CLAUDE_CONFIG_DIR"])
	}
}

func TestBuildInteractiveUsesConfiguredDurableCodexHomeOnRestore(t *testing.T) {
	codexHome := filepath.Join(t.TempDir(), "codex")
	t.Setenv("CODEX_HOME", codexHome)
	loginHome := ""
	builder := HarnessBuilder{
		DataDir: t.TempDir(),
		CodexLogin: func(_, home, _, _ string) error {
			loginHome = home
			return nil
		},
	}
	command, err := builder.BuildInteractive(worker.LaunchContext{
		SessionID: "session-1", Harness: "codex", AgentSessionID: "thread-1",
		Mode: "standard",
	}, worker.CredentialResponse{
		Provider: "codex", CredentialType: "api_key", Secret: "secret",
	}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if loginHome != codexHome || command.Env["CODEX_HOME"] != codexHome {
		t.Fatalf("Codex durable home: login=%q env=%q", loginHome, command.Env["CODEX_HOME"])
	}
	if !slices.Contains(command.Args, "thread-1") || len(command.Args) == 0 || command.Args[0] != "resume" {
		t.Fatalf("unexpected Codex restore args: %#v", command.Args)
	}
}

func TestBuildInteractiveWritesOpaqueCodexAuthJSONWithoutRelogin(t *testing.T) {
	codexHome := filepath.Join(t.TempDir(), "codex")
	t.Setenv("CODEX_HOME", codexHome)
	loginCalled := false
	credential := `{"tokens":{"access_token":"opaque"}}`
	command, err := (HarnessBuilder{
		DataDir: t.TempDir(),
		CodexLogin: func(_, _, _, _ string) error {
			loginCalled = true
			return nil
		},
	}).BuildInteractive(worker.LaunchContext{
		SessionID: "session-1", Harness: "codex", Mode: "standard",
	}, worker.CredentialResponse{Provider: "codex", CredentialType: "auth_json", Secret: credential}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if loginCalled {
		t.Fatal("auth JSON must be handed to Codex as its native file, not passed through login")
	}
	got, err := os.ReadFile(filepath.Join(codexHome, "auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != credential || command.Env["CODEX_HOME"] != codexHome {
		t.Fatalf("Codex auth handoff = %q, home = %q", got, command.Env["CODEX_HOME"])
	}
	info, err := os.Stat(filepath.Join(codexHome, "auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	expectedPerm := os.FileMode(0o600)
	if runtime.GOOS == "windows" {
		expectedPerm = 0o666
	}
	if info.Mode().Perm() != expectedPerm {
		t.Errorf("auth.json permissions = %#o, want %#o", info.Mode().Perm(), expectedPerm)
	}
}

func containsAdjacent(values []string, first, second string) bool {
	for index := 0; index+1 < len(values); index++ {
		if values[index] == first && values[index+1] == second {
			return true
		}
	}
	return false
}

func buildInteractive(t *testing.T, launch worker.LaunchContext) Command {
	t.Helper()
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	credential := worker.CredentialResponse{
		Provider: launch.Harness, CredentialType: "api_key", Secret: "test-secret",
	}
	command, err := HarnessBuilder{DataDir: t.TempDir()}.BuildInteractive(
		launch, credential, t.TempDir(),
	)
	if err != nil {
		t.Fatalf("BuildInteractive: %v", err)
	}
	t.Cleanup(func() {
		if command.Cleanup != nil {
			command.Cleanup()
		}
	})
	return command
}

// systemPromptArg extracts either an inline Claude prompt or the contents of
// the generated prompt file. It returns "" when neither is present.
func systemPromptArg(command Command) string {
	args := append([]string{command.Path}, command.Args...)
	for i, arg := range args {
		if arg == "--append-system-prompt" && i+1 < len(args) {
			return args[i+1]
		}
		if arg == "--append-system-prompt-file" && i+1 < len(args) {
			contents, _ := os.ReadFile(args[i+1])
			return string(contents)
		}
	}
	return ""
}

func TestBuildInteractiveOrchestratorPrompt(t *testing.T) {
	command := buildInteractive(t, worker.LaunchContext{
		SessionID: "11111111-1111-4111-8111-111111111111",
		Kind:      "orchestrator", Harness: "claude-code", Mode: "trusted",
	})
	prompt := systemPromptArg(command)
	if prompt == "" {
		t.Fatal("orchestrator launch carries no system prompt")
	}
	// Cloud grammar in, desktop grammar out.
	for _, needle := range []string{
		"AO Orchestrator Role",
		"ao spawn --name",
		"ao list",
		"ao kill",
		"using-ao/SKILL.md",
		"coordination-only",
		"Never guess file names",
	} {
		if !strings.Contains(prompt, needle) {
			t.Fatalf("orchestrator prompt missing %q", needle)
		}
	}
	for _, forbidden := range []string{"ao session ls", "ao status", "--project"} {
		if strings.Contains(prompt, forbidden) {
			t.Fatalf("orchestrator prompt suggests desktop-only %q", forbidden)
		}
	}
}

func TestBuildInteractiveWorkerPromptWithParent(t *testing.T) {
	command := buildInteractive(t, worker.LaunchContext{
		SessionID: "11111111-1111-4111-8111-111111111111",
		Kind:      "worker", Harness: "claude-code", Mode: "trusted",
		ParentSessionID: "22222222-2222-4222-8222-222222222222",
	})
	prompt := systemPromptArg(command)
	if prompt == "" {
		t.Fatal("worker launch carries no system prompt")
	}
	for _, needle := range []string{
		"AO Worker Role",
		"ao report",
		"never paste diffs",
		"$AO_PULL_REQUEST_HELP",
		"$AO_SESSION_BRANCH",
		"ao claim-pr",
		"using-ao/SKILL.md",
	} {
		if !strings.Contains(prompt, needle) {
			t.Fatalf("worker prompt missing %q", needle)
		}
	}
}

func TestBuildInteractiveWorkerPromptWithoutParent(t *testing.T) {
	command := buildInteractive(t, worker.LaunchContext{
		SessionID: "11111111-1111-4111-8111-111111111111",
		Kind:      "worker", Harness: "claude-code", Mode: "trusted",
	})
	prompt := systemPromptArg(command)
	if strings.Contains(prompt, "ao report") {
		t.Fatal("parentless worker prompt must not suggest ao report (scope is stripped)")
	}
	if !strings.Contains(prompt, "no orchestrator is attached") {
		t.Fatal("parentless worker prompt missing the direct-report guidance")
	}
}

func TestBuildInteractiveCursorBuildsWithoutPrompt(t *testing.T) {
	// Cursor receives standing instructions through its generated plugin rather
	// than a prompt flag, so the launch command itself must remain prompt-free.
	command := buildInteractive(t, worker.LaunchContext{
		SessionID: "11111111-1111-4111-8111-111111111111",
		Kind:      "worker", Harness: "cursor", Mode: "trusted",
	})
	if prompt := systemPromptArg(command); prompt != "" {
		t.Fatalf("cursor unexpectedly carries a system prompt flag: %q", prompt)
	}
}
