package workerexec

import (
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/cloud/internal/worker"
)

func TestBuildInteractiveRestoresOpenCodeSession(t *testing.T) {
	command, err := (HarnessBuilder{DataDir: t.TempDir()}).BuildInteractive(worker.LaunchContext{
		SessionID: "session-1", Harness: "opencode", AgentSessionID: "thread-1",
		Mode: "standard",
	}, worker.CredentialResponse{
		Provider: "opencode", CredentialType: "api_key", Secret: "secret",
	}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if !containsAdjacent(command.Args, "--session", "thread-1") {
		t.Fatalf("restore args missing from %#v", command.Args)
	}
	if slices.Contains(command.Args, "--session-id") {
		t.Fatalf("fresh-launch identity present in restore command %#v", command.Args)
	}
	if command.Env["OPENCODE_API_KEY"] != "secret" {
		t.Errorf("OPENCODE_API_KEY = %q", command.Env["OPENCODE_API_KEY"])
	}
}

func TestBuildInteractiveLaunchPermissionAndCredential(t *testing.T) {
	command, err := (HarnessBuilder{DataDir: t.TempDir()}).BuildInteractive(worker.LaunchContext{
		SessionID: "session-1", Harness: "opencode", Kind: "worker",
		Mode: "trusted", Prompt: "-fix auth",
	}, worker.CredentialResponse{
		Provider: "opencode", CredentialType: "api_key", Secret: "secret",
	}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(command.Args, "--dangerously-skip-permissions") {
		t.Fatalf("trusted launch missing bypass flag: %#v", command.Args)
	}
	for _, forbidden := range []string{"--resume", "--session", "--print", "--format", "json"} {
		if slices.Contains(command.Args, forbidden) {
			t.Fatalf("interactive launch must not carry headless/restore args: %#v", command.Args)
		}
	}
	// The task prompt travels via --prompt so a leading "-" is not a flag.
	if !containsAdjacent(command.Args, "--prompt", "-fix auth") {
		t.Fatalf("launch prompt missing from %#v", command.Args)
	}
	if command.Env["OPENCODE_API_KEY"] != "secret" {
		t.Errorf("OPENCODE_API_KEY = %q", command.Env["OPENCODE_API_KEY"])
	}
}

func TestBuildInteractiveRejectsCredentialsForOtherHarnesses(t *testing.T) {
	if _, err := (HarnessBuilder{DataDir: t.TempDir()}).BuildInteractive(worker.LaunchContext{
		SessionID: "session-1", Harness: "opencode", Mode: "standard",
	}, worker.CredentialResponse{
		Provider: "claude-code", CredentialType: "api_key", Secret: "secret",
	}, t.TempDir()); err == nil {
		t.Fatal("mismatched provider credential was accepted")
	}
}

func TestBuildInteractiveRejectsReadOnlyMode(t *testing.T) {
	_, err := (HarnessBuilder{DataDir: t.TempDir()}).BuildInteractive(worker.LaunchContext{
		SessionID: "session-1", Harness: "opencode", Mode: "read-only",
	}, worker.CredentialResponse{
		Provider: "opencode", CredentialType: "api_key", Secret: "secret",
	}, t.TempDir())
	if !errors.Is(err, ErrUnsupportedPolicy) {
		t.Fatalf("read-only interactive build error = %v, want ErrUnsupportedPolicy", err)
	}
}

func TestBuildInteractiveRejectsDeniedCommands(t *testing.T) {
	_, err := (HarnessBuilder{DataDir: t.TempDir()}).BuildInteractive(worker.LaunchContext{
		SessionID: "session-1", Harness: "opencode", Mode: "standard",
		DeniedCommands: []string{"rm"},
	}, worker.CredentialResponse{
		Provider: "opencode", CredentialType: "api_key", Secret: "secret",
	}, t.TempDir())
	if !errors.Is(err, ErrUnsupportedPolicy) {
		t.Fatalf("denied-command interactive build error = %v, want ErrUnsupportedPolicy", err)
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

// systemPromptConfig returns the standing-instructions prompt from the
// AO-generated opencode config pointed at by OPENCODE_CONFIG. opencode has no
// system-prompt flag, so the worker prompt travels as an AO-generated agent's
// prompt in that config; "" means no config was written.
func systemPromptConfig(t *testing.T, command Command) string {
	t.Helper()
	configPath := command.Env["OPENCODE_CONFIG"]
	if configPath == "" {
		return ""
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read opencode config %s: %v", configPath, err)
	}
	var config struct {
		Agent map[string]struct {
			Prompt string `json:"prompt"`
		} `json:"agent"`
	}
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatalf("decode opencode config %s: %v", configPath, err)
	}
	for _, agent := range config.Agent {
		return agent.Prompt
	}
	return ""
}

func TestBuildInteractiveOrchestratorPrompt(t *testing.T) {
	command := buildInteractive(t, worker.LaunchContext{
		SessionID: "11111111-1111-4111-8111-111111111111",
		Kind:      "orchestrator", Harness: "opencode", Mode: "trusted",
	})
	prompt := systemPromptConfig(t, command)
	if prompt == "" {
		t.Fatal("orchestrator launch carries no system prompt")
	}
	// Cloud grammar in, cloud grammar out.
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
		Kind:      "worker", Harness: "opencode", Mode: "trusted",
		ParentSessionID: "22222222-2222-4222-8222-222222222222",
	})
	prompt := systemPromptConfig(t, command)
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
		Kind:      "worker", Harness: "opencode", Mode: "trusted",
	})
	prompt := systemPromptConfig(t, command)
	if strings.Contains(prompt, "ao report") {
		t.Fatal("parentless worker prompt must not suggest ao report (scope is stripped)")
	}
	if !strings.Contains(prompt, "no orchestrator is attached") {
		t.Fatal("parentless worker prompt missing the direct-report guidance")
	}
}

func TestBuildInteractiveAgentSelectionMatchesConfig(t *testing.T) {
	command := buildInteractive(t, worker.LaunchContext{
		SessionID: "11111111-1111-4111-8111-111111111111",
		Kind:      "worker", Harness: "opencode", Mode: "trusted",
	})
	// The launch builder selects the AO agent with --agent; the config written
	// into OPENCODE_CONFIG must define exactly that name or opencode errors on
	// an unknown agent.
	agent := ""
	args := append([]string{command.Path}, command.Args...)
	for i, arg := range args {
		if arg == "--agent" && i+1 < len(args) {
			agent = args[i+1]
		}
	}
	if agent == "" {
		t.Fatal("launch selects no AO agent")
	}
	data, err := os.ReadFile(command.Env["OPENCODE_CONFIG"])
	if err != nil {
		t.Fatalf("read opencode config: %v", err)
	}
	var config struct {
		Agent map[string]any `json:"agent"`
	}
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatalf("decode opencode config: %v", err)
	}
	if _, ok := config.Agent[agent]; !ok {
		t.Fatalf("--agent %q not defined in config %s", agent, data)
	}
}

func TestBuildHeadlessOpenCodeRun(t *testing.T) {
	builder := HarnessBuilder{DataDir: t.TempDir()}
	credential := worker.CredentialResponse{
		Provider: "opencode", CredentialType: "api_key", Secret: "secret",
	}
	workspace := t.TempDir()

	command, err := builder.Build(t.Context(), worker.Turn{
		Harness: "opencode", Mode: "standard", Prompt: "fix auth",
	}, credential, workspace)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"run", "--format", "json", "fix auth"}
	if command.Path != "opencode" || !slices.Equal(command.Args, want) {
		t.Fatalf("standard run command = %#v %#v, want %#v", command.Path, command.Args, want)
	}
	if !slices.Contains(command.Args, "--format") {
		t.Fatalf("headless run missing json output flag: %#v", command.Args)
	}
	if !opencodePluginInstalled(t, workspace) {
		t.Fatal("headless build did not install the opencode activity plugin")
	}
}

func TestBuildHeadlessOpenCodeRunTrustedAndRestore(t *testing.T) {
	builder := HarnessBuilder{DataDir: t.TempDir()}
	credential := worker.CredentialResponse{
		Provider: "opencode", CredentialType: "api_key", Secret: "secret",
	}
	command, err := builder.Build(t.Context(), worker.Turn{
		Harness: "opencode", Mode: "trusted", AgentSessionID: "thread-1", Prompt: "continue",
	}, credential, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, needle := range []string{"run", "--dangerously-skip-permissions", "--session", "thread-1", "--format", "json", "continue"} {
		if !slices.Contains(command.Args, needle) {
			t.Fatalf("trusted run args %#v missing %q", command.Args, needle)
		}
	}
}

func TestBuildHeadlessOpenCodeRunReadOnly(t *testing.T) {
	command, err := (HarnessBuilder{DataDir: t.TempDir()}).Build(t.Context(), worker.Turn{
		Harness: "opencode", Mode: "read-only", Prompt: "summarize",
	}, worker.CredentialResponse{
		Provider: "opencode", CredentialType: "api_key", Secret: "secret",
	}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(command.Args, "--dangerously-skip-permissions") {
		t.Fatalf("read-only run must keep opencode's own permission config: %#v", command.Args)
	}
}

func TestBuildHeadlessRejectsUnknownHarnessOrMode(t *testing.T) {
	builder := HarnessBuilder{DataDir: t.TempDir()}
	credential := worker.CredentialResponse{
		Provider: "opencode", CredentialType: "api_key", Secret: "secret",
	}
	if _, err := builder.Build(t.Context(), worker.Turn{
		Harness: "claude-code", Mode: "standard", Prompt: "x",
	}, credential, t.TempDir()); err == nil {
		t.Fatal("unsupported harness was accepted")
	}
	if _, err := builder.Build(t.Context(), worker.Turn{
		Harness: "opencode", Mode: "nonsense", Prompt: "x",
	}, credential, t.TempDir()); !errors.Is(err, ErrUnsupportedPolicy) {
		t.Fatalf("unknown mode error = %v, want ErrUnsupportedPolicy", err)
	}
}

func opencodePluginInstalled(t *testing.T, workspace string) bool {
	t.Helper()
	path := opencodePluginPath(workspace)
	existing, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read plugin: %v", err)
	}
	return strings.Contains(string(existing), opencodePluginSentinel)
}