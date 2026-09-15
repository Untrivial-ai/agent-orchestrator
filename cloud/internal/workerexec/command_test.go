package workerexec

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/aoagents/agent-orchestrator/cloud/internal/worker"
	"strings"
)

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
	command, err := (HarnessBuilder{}).BuildInteractive(worker.LaunchContext{
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
	builder := HarnessBuilder{CodexLogin: func(_, home, _, _ string) error {
		loginHome = home
		return nil
	}}
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

// systemPromptArg extracts the value following --append-system-prompt, "" when
// the flag is absent.
func systemPromptArg(command Command) string {
	args := append([]string{command.Path}, command.Args...)
	for i, arg := range args {
		if arg == "--append-system-prompt" && i+1 < len(args) {
			return args[i+1]
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
		"skills/using-ao/SKILL.md",
		"coordination-only",
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
		"$AO_PULL_REQUEST_HELP",
		"$AO_SESSION_BRANCH",
		"ao claim-pr",
		"skills/using-ao/SKILL.md",
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
	// The cursor launch builder discards SystemPrompt (known limitation); the
	// build must still succeed so the session gets its terminal and on-disk
	// skill.
	command := buildInteractive(t, worker.LaunchContext{
		SessionID: "11111111-1111-4111-8111-111111111111",
		Kind:      "worker", Harness: "cursor", Mode: "trusted",
	})
	if prompt := systemPromptArg(command); prompt != "" {
		t.Fatalf("cursor unexpectedly carries a system prompt flag: %q", prompt)
	}
}
