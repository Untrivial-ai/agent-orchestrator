package workerexec

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/cloud/internal/worker"
)

// Capture the actual provider process arguments and RPC requests without
// credentials, network access, or a model call.
func TestCloudPromptProviderHelper(t *testing.T) {
	if os.Getenv("AO_PROMPT_TEST") != "1" {
		return
	}
	capture, err := os.Create(os.Getenv("AO_PROMPT_CAPTURE"))
	if err != nil {
		t.Fatal(err)
	}
	defer capture.Close()
	record := json.NewEncoder(capture)
	var cursorRules []string
	if os.Getenv("AO_PROMPT_TEST_HARNESS") == "cursor" {
		root, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		for {
			paths, err := filepath.Glob(filepath.Join(root, ".cursor", "rules", "*.mdc"))
			if err != nil {
				t.Fatal(err)
			}
			for _, path := range paths {
				contents, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				cursorRules = append(cursorRules, string(contents))
			}
			parent := filepath.Dir(root)
			if parent == root {
				break
			}
			root = parent
		}
	}
	if err := record.Encode(map[string]any{"args": os.Args, "cursorRules": cursorRules, "env": map[string]string{
		"AO_PULL_REQUEST_SOCKET":     os.Getenv("AO_PULL_REQUEST_SOCKET"),
		"AO_REVIEW_SOCKET":           os.Getenv("AO_REVIEW_SOCKET"),
		"AO_CLOUD_WORKER_API_URL":    os.Getenv("AO_CLOUD_WORKER_API_URL"),
		"AO_CLOUD_WORKER_TOKEN_FILE": os.Getenv("AO_CLOUD_WORKER_TOKEN_FILE"),
		"AO_SESSION_KIND":            os.Getenv("AO_SESSION_KIND"),
	}}); err != nil {
		t.Fatal(err)
	}

	input := json.NewDecoder(os.Stdin)
	output := json.NewEncoder(os.Stdout)
	for {
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params map[string]any  `json:"params"`
		}
		if err := input.Decode(&request); err != nil {
			return
		}
		if err := record.Encode(request); err != nil {
			t.Fatal(err)
		}
		if len(request.ID) == 0 {
			continue
		}
		result := map[string]any{}
		switch request.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": 1, "agentCapabilities": map[string]any{"loadSession": true}}
		case "thread/start":
			result = map[string]any{"thread": map[string]any{"id": "native-1"}}
		case "turn/start":
			result = map[string]any{"turn": map[string]any{"id": "provider-turn"}}
		case "session/new", "session/load":
			result = map[string]any{
				"sessionId": "native-1",
				"modes":     map[string]any{"currentModeId": "default", "availableModes": []map[string]any{{"id": "default", "name": "Default"}}},
			}
		case "session/prompt":
			result = map[string]any{"stopReason": "end_turn"}
		}
		if err := output.Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result}); err != nil {
			t.Fatal(err)
		}
		if request.Method == "turn/start" {
			if err := output.Encode(map[string]any{
				"method": "turn/completed", "params": map[string]any{
					"threadId": "native-1", "turn": map[string]any{"id": "provider-turn", "status": "completed"},
				},
			}); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestCloudChatDeliversTerminalRolePromptOnStartAndResume(t *testing.T) {
	for _, role := range []string{"worker", "orchestrator"} {
		for _, harness := range []string{"codex", "claude-code", "cursor"} {
			for _, resumed := range []bool{false, true} {
				name := role + "/" + harness + "/start"
				if resumed {
					name = role + "/" + harness + "/resume"
				}
				t.Run(name, func(t *testing.T) {
					dataDir, workspace, binDir := t.TempDir(), t.TempDir(), t.TempDir()
					t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(dataDir, "claude"))
					t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
					executable, err := os.Executable()
					if err != nil {
						t.Fatal(err)
					}
					binary := filepath.Join(binDir, "claude-agent-acp")
					wrapper := "#!/bin/sh\nexec '" + strings.ReplaceAll(executable, "'", "'\\''") + "' -test.run='^TestCloudPromptProviderHelper$' -- \"$@\"\n"
					if err := os.WriteFile(binary, []byte(wrapper), 0o700); err != nil {
						t.Fatal(err)
					}
					launch := worker.LaunchContext{
						SessionID: "session-1", Kind: role, Harness: harness, Mode: "trusted",
						ParentSessionID: "parent-1", SystemPrompt: "PROJECT RULE MARKER",
						ExtraRepos: []worker.RepoRef{{URL: "https://github.com/example/extra"}},
					}
					turn := worker.Turn{ID: "turn-1", Attempt: 1, Harness: harness, Mode: "trusted", Prompt: "USER TASK MARKER"}
					if resumed {
						turn.AgentSessionID, launch.AgentSessionID = "native-1", "native-1"
						if harness == "claude-code" {
							transcript := filepath.Join(dataDir, "claude", "projects", "project", "native-1.jsonl")
							if err := writePrivateFile(transcript, nil); err != nil {
								t.Fatal(err)
							}
						}
					}
					builder := HarnessBuilder{
						DataDir: dataDir, Launch: launch, Binaries: map[string]string{harness: binary},
						Env: map[string]string{
							"AO_PULL_REQUEST_SOCKET":     filepath.Join(dataDir, "pr.sock"),
							"AO_REVIEW_SOCKET":           filepath.Join(dataDir, "review.sock"),
							"AO_CLOUD_WORKER_API_URL":    "https://cloud.example.test/api/cloud/v1",
							"AO_CLOUD_WORKER_TOKEN_FILE": filepath.Join(dataDir, "worker-token"),
							"AO_SESSION_KIND":            role,
						},
						CodexLogin: func(_, _, _, _ string) error { return nil },
					}
					credential := worker.CredentialResponse{Provider: harness, CredentialType: "api_key", Secret: "test-secret"}
					command, err := builder.Build(context.Background(), turn, credential, workspace)
					if err != nil {
						t.Fatal(err)
					}
					if command.Cleanup != nil {
						defer command.Cleanup()
					}
					terminalCommand, err := builder.BuildInteractive(launch, credential, workspace)
					if err != nil {
						t.Fatal(err)
					}
					for key, value := range builder.Env {
						if terminalCommand.Env[key] != value || command.Env[key] != value {
							t.Fatalf("tooling environment differs for %s", key)
						}
					}
					terminalPrompt, err := os.ReadFile(filepath.Join(dataDir, "prompts", sessionKey(launch.SessionID), "system.md"))
					if err != nil || strings.TrimRight(string(terminalPrompt), "\n") != strings.TrimRight(command.SystemPrompt, "\n") {
						t.Fatalf("Chat/Terminal prompt mismatch: %v", err)
					}
					roleHeader := "## AO Worker Role"
					if role == "orchestrator" {
						roleHeader = "## AO Orchestrator Role"
					}
					if !strings.Contains(command.SystemPrompt, roleHeader) {
						t.Fatalf("wrong role instructions for %s", role)
					}
					if role == "orchestrator" && !strings.Contains(command.SystemPrompt, "Never say a worker") {
						t.Fatal("Chat orchestrator may claim a worker without an ao spawn result")
					}
					if role == "orchestrator" && !strings.Contains(command.SystemPrompt, "ao spawn --name") {
						t.Fatal("Chat orchestrator has no AO worker creation command")
					}
					if !strings.Contains(command.SystemPrompt, "PROJECT RULE MARKER") || strings.Contains(command.SystemPrompt, turn.Prompt) {
						t.Fatal("project rules missing or user task mixed into standing instructions")
					}
					if strings.Contains(command.SystemPrompt, "## Additional repositories") != (role == "worker") {
						t.Fatal("additional repository instructions crossed the role boundary")
					}
					capture := filepath.Join(t.TempDir(), "requests.jsonl")
					builder.Env["AO_PROMPT_TEST"], builder.Env["AO_PROMPT_CAPTURE"] = "1", capture
					builder.Env["AO_PROMPT_TEST_HARNESS"] = harness
					ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
					defer cancel()
					control := &promptControl{controlStub: controlStub{credential: credential}}
					supervisor := &Supervisor{
						Control: control, Builder: builder, UseProviderProtocol: true, Workspace: workspace,
						CancelInterval: time.Millisecond, CompletionRetry: time.Millisecond,
					}
					if err := supervisor.execute(ctx, turn); err != nil {
						t.Fatal(err)
					}
					if control.failed != "" || !control.completed || control.cancelled {
						t.Fatalf("Chat execution failed: %q, completed=%v, cancelled=%v", control.failed, control.completed, control.cancelled)
					}
					assertPromptRequests(t, capture, command, turn, resumed)
					if harness == "cursor" {
						path, err := cursorACPStandingRulePath(workspace, launch.SessionID)
						if err != nil {
							t.Fatal(err)
						}
						if _, err := os.Stat(path); !os.IsNotExist(err) {
							t.Fatalf("Cursor standing rule survived turn cleanup: %v", err)
						}
					}
				})
			}
		}
	}
}

type promptControl struct {
	controlStub
	approvalFlowControl
}

func assertPromptRequests(t *testing.T, path string, command Command, turn worker.Turn, resumed bool) {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	foundSession, foundTask := false, false
	for scanner.Scan() {
		var frame struct {
			CursorRules []string          `json:"cursorRules"`
			Args        []string          `json:"args"`
			Env         map[string]string `json:"env"`
			Method      string            `json:"method"`
			Params      map[string]any    `json:"params"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &frame); err != nil {
			t.Fatal(err)
		}
		if len(frame.Args) > 0 {
			for key, value := range frame.Env {
				if value != command.Env[key] || value == "" {
					t.Fatalf("provider omitted tooling environment %s", key)
				}
			}
		}
		if turn.Harness == "cursor" && len(frame.Args) > 0 {
			for _, arg := range frame.Args {
				if arg == "--plugin-dir" {
					t.Fatal("Cursor ACP received its unsupported plugin flag")
				}
			}
			foundRule := false
			for _, rule := range frame.CursorRules {
				if rule == cursorACPRuleMarker+strings.TrimRight(command.SystemPrompt, "\n")+"\n" {
					foundRule = true
				}
			}
			if !foundRule {
				t.Fatal("Cursor ACP did not load standing instructions from the workspace ancestors")
			}
		}
		method := "session/new"
		if resumed {
			method = "session/load"
		}
		if turn.Harness == "codex" {
			method = "thread/start"
			if resumed {
				method = "thread/resume"
			}
		}
		if frame.Method == method {
			foundSession = true
			switch turn.Harness {
			case "codex":
				if frame.Params["developerInstructions"] != command.SystemPrompt {
					t.Fatal("Codex session request omitted standing instructions")
				}
			case "claude-code":
				want := map[string]any{"systemPrompt": map[string]any{"type": "preset", "preset": "claude_code", "append": command.SystemPrompt}}
				if !reflect.DeepEqual(frame.Params["_meta"], want) {
					t.Fatal("Claude session request omitted appended standing instructions")
				}
			case "cursor":
				if frame.Params["_meta"] != nil {
					t.Fatal("Cursor received unsupported instruction metadata")
				}
			}
		}
		if frame.Method == "session/prompt" || frame.Method == "turn/start" {
			foundTask = true
			content := frame.Params["prompt"]
			if frame.Method == "turn/start" {
				content = frame.Params["input"]
			}
			if !reflect.DeepEqual(content, []any{map[string]any{"type": "text", "text": turn.Prompt}}) {
				t.Fatal("user task was changed by standing instruction delivery")
			}
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if !foundSession || !foundTask {
		t.Fatal("provider did not receive both session setup and user task")
	}
}

func TestCloudChatRequiresRoleContext(t *testing.T) {
	_, err := (HarnessBuilder{DataDir: t.TempDir()}).Build(context.Background(),
		worker.Turn{Harness: "cursor", Mode: "trusted"},
		worker.CredentialResponse{Provider: "cursor", Secret: "test-secret"}, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "session role context") {
		t.Fatalf("missing role context = %v", err)
	}
}
