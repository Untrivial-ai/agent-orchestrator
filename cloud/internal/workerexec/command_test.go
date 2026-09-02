package workerexec

import (
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
