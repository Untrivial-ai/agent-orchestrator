package workerexec

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/cloud/internal/worker"
)

func TestBuildInteractiveInjectsSystemPromptForEveryCloudHarness(t *testing.T) {
	for _, harness := range []string{"claude-code", "codex", "cursor"} {
		t.Run(harness, func(t *testing.T) {
			dataDir := t.TempDir()
			workspace := t.TempDir()
			builder := HarnessBuilder{
				DataDir: dataDir,
				Binaries: map[string]string{
					"claude-code": "/usr/bin/claude",
					"codex":       "/usr/bin/codex",
					"cursor":      "/usr/bin/cursor-agent",
				},
				CodexLogin: func(_, _, _, _ string) error { return nil },
			}
			credentialType := "api_key"
			command, err := builder.BuildInteractive(worker.LaunchContext{
				SessionID:    "session-1",
				ProjectID:    "project-1",
				Kind:         "worker",
				Harness:      harness,
				Prompt:       "Implement the task.",
				SystemPrompt: "SYSTEM ROLE MARKER",
				Mode:         "trusted",
			}, worker.CredentialResponse{
				Provider: harness, CredentialType: credentialType, Secret: "secret",
			}, workspace)
			if err != nil {
				t.Fatal(err)
			}

			systemPromptPath := filepath.Join(dataDir, "prompts", sessionKey("session-1"), "system.md")
			assertFileContains(t, systemPromptPath, "SYSTEM ROLE MARKER")
			if strings.Contains(strings.Join(command.Args, "\n"), "SYSTEM ROLE MARKER") {
				t.Fatalf("%s command leaked the system prompt inline: %#v", harness, command.Args)
			}

			switch harness {
			case "claude-code":
				assertArgPair(t, command.Args, "--append-system-prompt-file", systemPromptPath)
			case "codex":
				assertArgPair(t, command.Args, "-c", "model_instructions_file="+systemPromptPath)
			case "cursor":
				pluginDir := filepath.Join(dataDir, "cursor-plugins", sessionKey("session-1"))
				assertArgPair(t, command.Args, "--plugin-dir", pluginDir)
				assertFileContains(t, filepath.Join(pluginDir, "rules", "ao-standing.mdc"), "alwaysApply: true")
				assertFileContains(t, filepath.Join(pluginDir, "rules", "ao-standing.mdc"), "SYSTEM ROLE MARKER")
			}
		})
	}
}

func TestBuildInteractiveRejectsMissingCloudSystemPrompt(t *testing.T) {
	_, err := (HarnessBuilder{DataDir: t.TempDir()}).BuildInteractive(worker.LaunchContext{
		SessionID: "session-1", Kind: "worker", Harness: "claude-code", Mode: "trusted",
	}, worker.CredentialResponse{
		Provider: "claude-code", CredentialType: "api_key", Secret: "secret",
	}, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "system prompt is required") {
		t.Fatalf("error = %v, want missing system prompt error", err)
	}
}

func TestBuildInteractiveReappliesSystemPromptOnRestore(t *testing.T) {
	for _, harness := range []string{"claude-code", "codex", "cursor"} {
		t.Run(harness, func(t *testing.T) {
			dataDir := t.TempDir()
			workspace := t.TempDir()
			identity := "native-session-1"
			if harness == "claude-code" {
				conversation := filepath.Join(dataDir, "claude", "projects", "project", identity+".jsonl")
				if err := os.MkdirAll(filepath.Dir(conversation), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(conversation, nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			builder := HarnessBuilder{
				DataDir: dataDir,
				Binaries: map[string]string{
					"claude-code": "/usr/bin/claude",
					"codex":       "/usr/bin/codex",
					"cursor":      "/usr/bin/cursor-agent",
				},
				CodexLogin: func(_, _, _, _ string) error { return nil },
			}
			command, err := builder.BuildInteractive(worker.LaunchContext{
				SessionID:      "session-1",
				Kind:           "orchestrator",
				Harness:        harness,
				AgentSessionID: identity,
				SystemPrompt:   "RESTORED SYSTEM ROLE",
				Mode:           "trusted",
			}, worker.CredentialResponse{
				Provider: harness, CredentialType: "api_key", Secret: "secret",
			}, workspace)
			if err != nil {
				t.Fatal(err)
			}
			systemPromptPath := filepath.Join(dataDir, "prompts", sessionKey("session-1"), "system.md")
			switch harness {
			case "claude-code":
				assertArgPair(t, command.Args, "--append-system-prompt-file", systemPromptPath)
				assertArgPair(t, command.Args, "--resume", identity)
			case "codex":
				assertArgPair(t, command.Args, "-c", "model_instructions_file="+systemPromptPath)
				if len(command.Args) == 0 || command.Args[0] != "resume" {
					t.Fatalf("Codex restore args = %#v", command.Args)
				}
			case "cursor":
				pluginDir := filepath.Join(dataDir, "cursor-plugins", sessionKey("session-1"))
				assertArgPair(t, command.Args, "--plugin-dir", pluginDir)
				assertArgPair(t, command.Args, "--resume", identity)
			}
		})
	}
}

func assertArgPair(t *testing.T, args []string, first, second string) {
	t.Helper()
	for i := 0; i+1 < len(args); i++ {
		if args[i] == first && args[i+1] == second {
			return
		}
	}
	t.Fatalf("args %#v do not contain %q followed by %q", args, first, second)
}

func assertFileContains(t *testing.T, path, want string) {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(contents), want) {
		t.Fatalf("%s missing %q:\n%s", path, want, contents)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("%s mode = %o, want 600", path, info.Mode().Perm())
	}
}
