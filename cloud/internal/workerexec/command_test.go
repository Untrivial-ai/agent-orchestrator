package workerexec

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
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
