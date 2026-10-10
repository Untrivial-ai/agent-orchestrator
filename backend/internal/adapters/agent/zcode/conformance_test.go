//go:build !windows

package zcode

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
	vt "github.com/unixshells/vt-go"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type conformanceRequest struct {
	Model    string `json:"model"`
	Stream   bool   `json:"stream"`
	Messages []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"messages"`
}

type conformanceEvent struct {
	Event   string `json:"event"`
	Payload struct {
		SessionID string `json:"session_id"`
		Prompt    string `json:"prompt"`
	} `json:"payload"`
}

// TestSourceBuiltZCodeTUIConformance runs the unmodified pinned upstream CLI
// and TUI built in PR CI. Only the model HTTP boundary and AO hook receiver are
// fixtures; native trust commands, terminal UI, and SQLite storage are real.
func TestSourceBuiltZCodeTUIConformance(t *testing.T) {
	entry := os.Getenv("AO_ZCODE_ENTRY")
	node := os.Getenv("AO_ZCODE_NODE")
	if entry == "" || node == "" {
		t.Skip("set AO_ZCODE_ENTRY and AO_ZCODE_NODE to the pinned source-built CLI and Node runtime")
	}
	workspace, home := t.TempDir(), t.TempDir()
	requests := make(chan conformanceRequest, 12)
	canceled := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.Error(w, "unexpected model route", http.StatusNotFound)
			return
		}
		defer r.Body.Close()
		var request conformanceRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		var firstContent string
		if len(request.Messages) > 0 {
			_ = json.Unmarshal(request.Messages[0].Content, &firstContent)
		}
		isTitle := strings.HasPrefix(firstContent, "Generate a concise title for this coding session.")
		response := "AO fake response"
		if isTitle {
			response = `{"title":"AO conformance"}`
		} else {
			select {
			case requests <- request:
			default:
				http.Error(w, "too many coding requests", http.StatusTooManyRequests)
				return
			}
		}
		if !request.Stream {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "ao-response", "object": "chat.completion", "created": 1, "model": "ao-test", "choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": response}, "finish_reason": "stop"}}, "usage": map[string]int{"prompt_tokens": 100, "completion_tokens": 3, "total_tokens": 103}})
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		content, _ := json.Marshal(response)
		_, _ = fmt.Fprintf(w, "data: {\"id\":\"ao-response\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"ao-test\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":%s},\"finish_reason\":null}]}\n\n", content)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		if !isTitle && latestConformanceTask(request) == "AO_CANCEL_THIS_TURN" {
			select {
			case <-r.Context().Done():
				select {
				case canceled <- struct{}{}:
				default:
				}
			case <-time.After(45 * time.Second):
			}
			return
		}
		_, _ = io.WriteString(w, "data: {\"id\":\"ao-response\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"ao-test\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":100,\"completion_tokens\":3,\"total_tokens\":103}}\n\ndata: [DONE]\n\n")
	}))
	t.Cleanup(server.Close)
	builtin := filepath.Join(home, "provider", "builtin.json")
	data, err := os.ReadFile(filepath.Join(filepath.Dir(entry), "provider", "zcode-builtin.json"))
	if err != nil {
		t.Fatal(err)
	}
	writeConformanceFile(t, builtin, string(data))
	personal := filepath.Join(home, "provider", "personal.json")
	// Preserve upstream native model rules, whose catch-all supplies complete
	// model metadata. Explicit paths prevent builtin remote-config refresh.
	writeConformanceFile(t, personal, fmt.Sprintf(`{
 "schemaVersion":1,"config":{
 "providerConfigRules":{"providerRules":[{"providerId":"ao-local","providerName":"AO fixture","enabled":true,"config":{"group":"standard-personal","access":{"type":"api-key","apiKey":"local-test-only"},"api":{"type":"openai-chat-completions","baseUrl":%q},"personalModelIds":["ao-test"]}}]},
 "modelConfigRules":{"providerModelRules":[],"manualProviderModelRules":[]},
 "defaultModelSelection":{"providerId":"ao-local","modelId":"ao-test","options":{"reasoningLevel":"disabled"}}
 }}`, server.URL+"/v1"))
	writeConformanceFile(t, filepath.Join(home, ".zcode", "cli", "config.json"), `{"ui":{"locale":"en-US"},"plugins":{"enabled":false},"features":{"mcp":false}}`)
	writeConformanceFile(t, filepath.Join(workspace, "AGENTS.md"), "AO_PROJECT_RULE_PRESERVED")
	hookLog, recorder := installConformanceHookRecorder(t, home)
	hiddenFile := filepath.Join(home, "standing.txt")
	writeConformanceFile(t, hiddenFile, "AO_HIDDEN_FIRST")
	binary := filepath.Join(home, "bin", "zcode")
	writeConformanceFile(t, binary, `#!/bin/sh
printf '%s\n' "$*" >> "$AO_TEST_CLI_LOG"
exec "$AO_ZCODE_NODE" "$AO_ZCODE_ENTRY" "$@"
`)
	if err := os.Chmod(binary, 0o700); err != nil {
		t.Fatal(err)
	}
	envMap := map[string]string{
		"HOME": home, "USERPROFILE": home, "ZCODE_DATA_BASE_DIR": home,
		"ZCODE_SESSION_DB_PATH":              filepath.Join(home, ".zcode", "cli", "db", "db.sqlite"),
		"ZCODE_BUILTIN_PROVIDER_CONFIG_FILE": builtin, "ZCODE_PERSONAL_PROVIDER_CONFIG_FILE": personal,
		"AO_ZCODE_ENTRY": entry, "AO_ZCODE_NODE": node, "AO_TEST_CLI_LOG": filepath.Join(home, "cli.log"),
		"PATH": filepath.Dir(recorder) + string(os.PathListSeparator) + os.Getenv("PATH"),
		"TERM": "xterm-256color", "LANG": "en_US.UTF-8",
		"AO_SESSION_ID": "ao-zcode-conformance", "AO_TEST_HOOK_LOG": hookLog, "AO_TEST_HIDDEN_FILE": hiddenFile,
	}
	var env []string
	for k, v := range envMap {
		env = append(env, k+"="+v)
	}
	plugin := &Plugin{resolvedBinary: binary}
	if err := plugin.GetAgentHooks(context.Background(), ports.WorkspaceHookConfig{WorkspacePath: workspace, SessionID: "ao-zcode-conformance"}); err != nil {
		t.Fatal(err)
	}
	initialTask := "--help AO_INITIAL_TASK\nKeep 'quotes' and $(literal) intact."
	launch := ports.LaunchConfig{WorkspacePath: workspace, SessionID: "ao-zcode-conformance", Prompt: initialTask, SystemPrompt: "AO_HIDDEN_FIRST", Env: envMap}
	argv, err := plugin.GetLaunchCommand(context.Background(), launch)
	if err != nil {
		probe := exec.Command(binary, "hooks", "trust", "status", "--workspace", workspace, "--json")
		probe.Dir, probe.Env = workspace, env
		output, probeErr := probe.CombinedOutput()
		t.Fatalf("launch: %v; native trust status (%v):\n%s", err, probeErr, output)
	}
	first := startConformanceTUI(t, workspace, env, argv)
	// SessionStart is lazy until a real turn; the native composer is readiness.
	waitConformanceComposer(t, first)
	sendConformanceInput(t, first, initialTask)
	request := receiveConformanceRequest(t, requests, first)
	assertConformancePrompt(t, request, initialTask, "AO_HIDDEN_FIRST")
	started := waitConformanceEvent(t, hookLog, "session-start", 1, first)
	submitted := waitConformanceEvent(t, hookLog, "user-prompt-submit", 1, first)
	if !nativeIDPattern.MatchString(started.Payload.SessionID) || submitted.Payload.Prompt != initialTask {
		t.Fatalf("native identity/task changed: %#v, %#v", started, submitted)
	}
	waitConformanceEvent(t, hookLog, "stop", 1, first)
	// A Stop callback is not proof of durable history. Validate the actual store
	// before SIGKILL so exact restore is independent of graceful process exit.
	waitConformanceActivity(t, first, domain.ActivityIdle)
	waitConformanceHistory(t, workspace, started.Payload.SessionID, envMap, first)
	first.stop()
	writeConformanceFile(t, hiddenFile, "AO_HIDDEN_RESTORED")
	restore := ports.RestoreConfig{Session: ports.SessionRef{ID: "ao-zcode-conformance", WorkspacePath: workspace, Metadata: map[string]string{ports.MetadataKeyAgentSessionID: started.Payload.SessionID}}, SystemPrompt: "AO_HIDDEN_RESTORED", Env: envMap}
	argv, ok, err := plugin.GetRestoreCommand(context.Background(), restore)
	if err != nil || !ok {
		t.Fatalf("exact persisted restore = %v, %v", ok, err)
	}
	restored := startConformanceTUI(t, workspace, env, argv)
	waitConformanceComposer(t, restored)
	sendConformanceInput(t, restored, "AO_RESTORE_FOLLOWUP")
	request = receiveConformanceRequest(t, requests, restored)
	assertConformancePrompt(t, request, "AO_RESTORE_FOLLOWUP", "AO_HIDDEN_RESTORED")
	if countRequestText(request, initialTask) != 1 {
		t.Fatal("restore lost or duplicated original task history")
	}
	waitConformanceEvent(t, hookLog, "session-start", 2, restored)
	waitConformanceEvent(t, hookLog, "stop", 2, restored)
	waitConformanceActivity(t, restored, domain.ActivityIdle)
	sendConformanceInput(t, restored, "AO_CANCEL_THIS_TURN")
	request = receiveConformanceRequest(t, requests, restored)
	if latestConformanceTask(request) != "AO_CANCEL_THIS_TURN" {
		t.Fatal("cancel task was not current")
	}
	waitConformanceActivity(t, restored, domain.ActivityActive)
	if _, err := restored.terminal.WriteString(plugin.InterruptInput()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-canceled:
	case <-time.After(15 * time.Second):
		t.Fatalf("native Escape did not cancel model stream\n%s", restored.output())
	}
	// Stop is not promised for interruption. Continued input and another actual
	// completed request prove Escape kept the same native session usable.
	waitConformanceActivity(t, restored, domain.ActivityIdle)
	stopCount := 0
	for _, event := range readConformanceEvents(t, hookLog) {
		if event.Event == "stop" {
			stopCount++
		}
	}
	sendConformanceInput(t, restored, "AO_AFTER_CANCEL")
	request = receiveConformanceRequest(t, requests, restored)
	assertConformancePrompt(t, request, "AO_AFTER_CANCEL", "AO_HIDDEN_RESTORED")
	waitConformanceEvent(t, hookLog, "user-prompt-submit", 4, restored)
	waitConformanceEvent(t, hookLog, "stop", stopCount+1, restored)
	waitConformanceActivity(t, restored, domain.ActivityIdle)
	restored.stop()
	for _, event := range readConformanceEvents(t, hookLog) {
		if event.Payload.SessionID != started.Payload.SessionID {
			t.Fatalf("native session changed: %#v", event)
		}
	}
	for _, process := range []*conformanceTUI{first, restored} {
		for _, secret := range []string{"AO_HIDDEN_FIRST", "AO_HIDDEN_RESTORED"} {
			if strings.Contains(process.output(), secret) {
				t.Fatalf("private hook context leaked into terminal: %s", secret)
			}
		}
	}
	cliLog, err := os.ReadFile(envMap["AO_TEST_CLI_LOG"])
	if err != nil {
		t.Fatal(err)
	}
	for _, nativeCommand := range []string{"hooks trust status --workspace", "hooks trust grant --workspace", "--hook-digest", "--mode build --resume " + started.Payload.SessionID} {
		if !strings.Contains(string(cliLog), nativeCommand) {
			t.Fatalf("native command did not execute: %s\n%s", nativeCommand, cliLog)
		}
	}
	restore.Session.Metadata[ports.MetadataKeyAgentSessionID] = "sess_ao_missing"
	if _, ok, err := plugin.GetRestoreCommand(context.Background(), restore); err == nil || ok {
		t.Fatalf("missing native session accepted: %v, %v", ok, err)
	}
	restore.Session.Metadata[ports.MetadataKeyAgentSessionID] = started.Payload.SessionID
	restore.Session.WorkspacePath = t.TempDir()
	if _, ok, err := plugin.GetRestoreCommand(context.Background(), restore); err == nil || ok {
		t.Fatalf("foreign native workspace accepted: %v, %v", ok, err)
	}
	select {
	case extra := <-requests:
		t.Fatalf("duplicate coding request: %#v", extra)
	default:
	}
}

func latestConformanceTask(request conformanceRequest) string {
	latest := ""
	for _, message := range request.Messages {
		if message.Role != "user" {
			continue
		}
		for _, task := range []string{"AO_INITIAL_TASK", "AO_RESTORE_FOLLOWUP", "AO_CANCEL_THIS_TURN", "AO_AFTER_CANCEL"} {
			if strings.Contains(string(message.Content), task) {
				latest = task
			}
		}
	}
	return latest
}

func assertConformancePrompt(t *testing.T, request conformanceRequest, task, hidden string) {
	t.Helper()
	if request.Model != "ao-test" || countRequestText(request, task) != 1 || countRequestText(request, hidden) == 0 || countRequestText(request, "AO_PROJECT_RULE_PRESERVED") == 0 {
		t.Fatalf("native model, task, hidden context or project rules missing: %#v", request)
	}
	defaults, privateReminder := false, false
	for _, message := range request.Messages {
		content := string(message.Content)
		if message.Role == "system" && (strings.Contains(content, "You are ZCode, an interactive coding agent") || strings.Contains(content, "You are an interactive ZCode agent")) {
			defaults = true
		}
		if strings.Contains(content, hidden) && strings.Contains(content, "UserPromptSubmit hook additional context") && strings.Contains(content, "system-reminder") {
			privateReminder = true
		}
	}
	if !defaults || !privateReminder {
		t.Fatalf("native defaults or internal hook reminder missing: %#v", request)
	}
}

func waitConformanceHistory(t *testing.T, workspace, id string, env map[string]string, process *conformanceTUI) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	var err error
	for time.Now().Before(deadline) {
		err = validateNativeRestore(context.Background(), workspace, id, env)
		if err == nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("native user history not durable: %v\n%s", err, process.output())
}

func countRequestText(request conformanceRequest, needle string) int {
	count := 0
	for _, message := range request.Messages {
		var text string
		if json.Unmarshal(message.Content, &text) == nil {
			count += strings.Count(text, needle)
			continue
		}
		var parts []struct {
			Text string `json:"text"`
		}
		if json.Unmarshal(message.Content, &parts) == nil {
			for _, part := range parts {
				count += strings.Count(part.Text, needle)
			}
		}
	}
	return count
}

type conformanceTUI struct {
	emulator    *vt.SafeEmulator
	terminal    *os.File
	command     *exec.Cmd
	done        chan struct{}
	outputDone  chan struct{}
	repliesDone chan struct{}
	mu          sync.Mutex
	buffer      bytes.Buffer
	stopOnce    sync.Once
}

func startConformanceTUI(t *testing.T, workspace string, env, argv []string) *conformanceTUI {
	t.Helper()
	command := exec.Command(argv[0], argv[1:]...)
	command.Dir, command.Env = workspace, env
	terminal, err := pty.StartWithSize(command, &pty.Winsize{Rows: 40, Cols: 140})
	if err != nil {
		t.Fatal(err)
	}
	process := &conformanceTUI{terminal: terminal, command: command, done: make(chan struct{}), emulator: vt.NewSafeEmulator(140, 40), outputDone: make(chan struct{}), repliesDone: make(chan struct{})}
	go func() { _, _ = io.Copy(terminal, process.emulator); close(process.repliesDone) }()
	go func() { _, _ = io.Copy(process, terminal); close(process.outputDone) }()
	go func() { _ = command.Wait(); close(process.done) }()
	t.Cleanup(process.stop)
	return process
}

func (p *conformanceTUI) Write(data []byte) (int, error) {
	_, _ = p.emulator.Write(data)
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.buffer.Len() < 1024*1024 {
		_, _ = p.buffer.Write(data)
	}
	return len(data), nil
}

func (p *conformanceTUI) output() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.buffer.String()
}

func (p *conformanceTUI) stop() {
	p.stopOnce.Do(func() {
		_ = syscall.Kill(-p.command.Process.Pid, syscall.SIGKILL)
		<-p.done
		_ = p.terminal.Close()
		// Close the synchronized pipe first. SafeEmulator's embedded Close
		// changes an unsynchronized flag, so it must run after both pumps exit.
		if closer, ok := p.emulator.InputPipe().(io.Closer); ok {
			_ = closer.Close()
		}
		<-p.outputDone
		<-p.repliesDone
		_ = p.emulator.Close()
	})
}

func receiveConformanceRequest(t *testing.T, requests <-chan conformanceRequest, process *conformanceTUI) conformanceRequest {
	t.Helper()
	select {
	case request := <-requests:
		return request
	case <-process.done:
		t.Fatalf("TUI exited before model request\n%s", process.output())
	case <-time.After(45 * time.Second):
		t.Fatalf("TUI did not send a model request\n%s", process.output())
	}
	return conformanceRequest{}
}

func readConformanceEvents(t *testing.T, path string) []conformanceEvent {
	t.Helper()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var events []conformanceEvent
	for _, line := range bytes.Split(bytes.TrimSpace(data), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var event conformanceEvent
		if json.Unmarshal(line, &event) == nil {
			events = append(events, event)
		}
	}
	return events
}

func waitConformanceEvent(t *testing.T, path, event string, want int, process *conformanceTUI) conformanceEvent {
	t.Helper()
	deadline := time.NewTimer(30 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		count := 0
		for _, entry := range readConformanceEvents(t, path) {
			if entry.Event == event {
				count++
				if count == want {
					return entry
				}
			}
		}
		select {
		case <-ticker.C:
		case <-process.done:
			t.Fatalf("TUI exited before %s\n%s", event, process.output())
		case <-deadline.C:
			t.Fatalf("missing native %s event %d\n%s", event, want, process.output())
		}
	}
}

func (p *conformanceTUI) screen() string {
	return strings.TrimRight(terminalEscape.ReplaceAllString(p.emulator.Render(), ""), " \t\r\n")
}

func waitConformanceActivity(t *testing.T, process *conformanceTUI, want domain.ActivityState) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	plugin := New()
	for time.Now().Before(deadline) {
		if got, known := plugin.DetectTerminalActivity(process.screen()); known && got == want {
			return
		}
		select {
		case <-process.done:
			t.Fatalf("TUI exited before activity %s\n%s", want, process.output())
		default:
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("native current-screen activity never reached %s\nSCREEN:\n%s\nRAW:\n%s", want, process.screen(), process.output())
}

func waitConformanceComposer(t *testing.T, process *conformanceTUI) {
	t.Helper()
	waitConformanceActivity(t, process, domain.ActivityIdle)
}

func sendConformanceInput(t *testing.T, process *conformanceTUI, prompt string) {
	t.Helper()
	time.Sleep(200 * time.Millisecond)
	if _, err := process.terminal.WriteString("\x1b[200~" + prompt + "\x1b[201~"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if _, err := process.terminal.WriteString("\r"); err != nil {
		t.Fatal(err)
	}
}

func writeConformanceFile(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
}

func installConformanceHookRecorder(t *testing.T, home string) (string, string) {
	t.Helper()
	log, path := filepath.Join(home, "hooks.jsonl"), filepath.Join(home, "bin", "ao")
	writeConformanceFile(t, path, `#!/usr/bin/env python3
import json, os, sys
payload = json.load(sys.stdin)
with open(os.environ["AO_TEST_HOOK_LOG"], "a", encoding="utf-8") as output:
    output.write(json.dumps({"event": sys.argv[3], "payload": payload}) + "\n")
if sys.argv[3] == "user-prompt-submit":
    with open(os.environ["AO_TEST_HIDDEN_FILE"], encoding="utf-8") as instructions:
        print(json.dumps({"additionalContext": instructions.read()}))
`)
	if err := os.Chmod(path, 0o700); err != nil {
		t.Fatal(err)
	}
	return log, path
}
