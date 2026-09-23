package primeagent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestGetAgentHooksInstallsManagedExtensionIdempotently(t *testing.T) {
	dataDir := t.TempDir()
	p := New()
	cfg := ports.WorkspaceHookConfig{DataDir: dataDir}
	if err := p.GetAgentHooks(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(dataDir, "agent-runtime", "prime-agent", "ao-activity.ts")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != primeAgentExtensionSource {
		t.Fatal("installed extension differs from embedded source")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("extension mode = %o, want 600", info.Mode().Perm())
	}

	oldTime := time.Unix(1_700_000_000, 0)
	if err := os.Chtimes(path, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	if err := p.GetAgentHooks(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	info, err = os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.ModTime().Equal(oldTime) {
		t.Fatalf("identical reinstall rewrote extension: modtime = %v, want %v", info.ModTime(), oldTime)
	}
}

func TestGetAgentHooksReplacesStaleManagedPath(t *testing.T) {
	dataDir := t.TempDir()
	path := filepath.Join(dataDir, "agent-runtime", "prime-agent", "ao-activity.ts")
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := New().GetAgentHooks(context.Background(), ports.WorkspaceHookConfig{DataDir: dataDir}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != primeAgentExtensionSource {
		t.Fatal("stale extension was not replaced")
	}
}

func TestGetAgentHooksValidatesDataDirAndContext(t *testing.T) {
	p := New()
	if err := p.GetAgentHooks(context.Background(), ports.WorkspaceHookConfig{}); err == nil {
		t.Fatal("expected blank DataDir error")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := p.GetAgentHooks(ctx, ports.WorkspaceHookConfig{DataDir: t.TempDir()}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled GetAgentHooks error = %v, want context.Canceled", err)
	}
}

func TestManagedExtensionMapsPrimeLifecycleEventsAndIgnoresHookFailures(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake ao executable fixture uses a Unix shebang")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is required to execute the TypeScript extension fixture")
	}

	dataDir := t.TempDir()
	if err := New().GetAgentHooks(context.Background(), ports.WorkspaceHookConfig{DataDir: dataDir}); err != nil {
		t.Fatal(err)
	}
	extensionPath := filepath.Join(dataDir, "agent-runtime", "prime-agent", "ao-activity.ts")
	fixtureDir := t.TempDir()
	extensionModulePath := filepath.Join(fixtureDir, "ao-activity.mjs")
	extensionSource, err := os.ReadFile(extensionPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(extensionModulePath, extensionSource, 0o600); err != nil {
		t.Fatal(err)
	}
	capturePath := filepath.Join(fixtureDir, "calls.jsonl")
	writeExecutable(t, filepath.Join(fixtureDir, "ao"), `#!/usr/bin/env node
const fs = require("node:fs");
let input = "";
process.stdin.setEncoding("utf8");
process.stdin.on("data", chunk => { input += chunk; });
process.stdin.on("end", () => {
  fs.appendFileSync(process.env.AO_TEST_CAPTURE, JSON.stringify({args: process.argv.slice(2), cwd: process.cwd(), input}) + "\n");
  process.exit(Number(process.env.AO_TEST_EXIT || "0"));
});
`)
	harnessPath := filepath.Join(fixtureDir, "harness.mjs")
	writeFile(t, harnessPath, `import { pathToFileURL } from "node:url";
const extensionPath = process.argv[2];
const cwd = process.argv[3];
const handlers = new Map();
const loaded = await import(pathToFileURL(extensionPath).href);
loaded.default({ on(name, handler) { handlers.set(name, handler); } });
const context = {
  cwd,
  sessionManager: { getSessionId() { return "prime-native-123"; } },
};
const contextWithoutSessionManager = { cwd };
const contextWithoutSessionGetter = { cwd, sessionManager: {} };
for (const [name, event, eventContext] of [
  ["session_start", { reason: "startup" }],
  ["before_agent_start", { prompt: "--leading-hyphen prompt" }],
  ["agent_start", {}],
  ["agent_end", {}],
  ["session_shutdown", { reason: "reload" }],
  ["session_start", { reason: "reload" }],
  ["session_shutdown", { reason: "new" }],
  ["session_start", { reason: "new" }],
  ["session_shutdown", { reason: "resume" }],
  ["session_start", { reason: "resume" }],
  ["session_shutdown", { reason: "fork" }],
  ["session_start", { reason: "fork" }],
  ["session_start", { reason: "old-prime" }, contextWithoutSessionManager],
  ["session_start", { reason: "missing-getter" }, contextWithoutSessionGetter],
  ["session_shutdown", { reason: "quit" }],
]) {
  await handlers.get(name)(event, eventContext ?? context);
}
for (const name of ["session_start", "before_agent_start", "agent_start", "agent_end", "session_shutdown"]) {
  await handlers.get(name)(undefined, undefined);
}
`)

	workspace := filepath.Join(fixtureDir, "workspace with spaces")
	if err := os.MkdirAll(workspace, 0o750); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(context.Background(), node, harnessPath, extensionModulePath, workspace)
	cmd.Env = append(os.Environ(), "PATH="+fixtureDir+string(os.PathListSeparator)+os.Getenv("PATH"), "AO_TEST_CAPTURE="+capturePath, "AO_TEST_EXIT=9")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("extension harness failed despite hook exit 9: %v\n%s", err, output)
	}

	calls := readHookCalls(t, capturePath)
	wantEvents := []string{
		"session-start",
		"user-prompt-submit",
		"stop",
		"session-start",
		"session-start",
		"session-start",
		"session-start",
		"session-start",
		"session-start",
		"session-end",
	}
	if len(calls) != len(wantEvents) {
		t.Fatalf("hook calls = %#v, want %d", calls, len(wantEvents))
	}
	wantCWD, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		t.Fatal(err)
	}
	for i, event := range wantEvents {
		if !reflect.DeepEqual(calls[i].Args, []string{"hooks", "prime-agent", event}) {
			t.Fatalf("call %d args = %#v", i, calls[i].Args)
		}
		if calls[i].CWD != wantCWD {
			t.Fatalf("call %d cwd = %q, want %q", i, calls[i].CWD, wantCWD)
		}
	}
	var promptPayload struct {
		Prompt string `json:"prompt"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(calls[1].Input)), &promptPayload); err != nil {
		t.Fatalf("prompt payload is not JSON: %v", err)
	}
	if promptPayload.Prompt != "--leading-hyphen prompt" {
		t.Fatalf("prompt payload = %q", promptPayload.Prompt)
	}
	wantReasons := map[int]string{
		0: "startup",
		3: "reload",
		4: "new",
		5: "resume",
		6: "fork",
		7: "old-prime",
		8: "missing-getter",
		9: "quit",
	}
	for i, wantReason := range wantReasons {
		var payload struct {
			Reason    string `json:"reason"`
			SessionID string `json:"session_id"`
		}
		if err := json.Unmarshal([]byte(strings.TrimSpace(calls[i].Input)), &payload); err != nil {
			t.Fatalf("call %d lifecycle payload is not JSON: %v", i, err)
		}
		if payload.Reason != wantReason {
			t.Fatalf("call %d reason = %q, want %q", i, payload.Reason, wantReason)
		}
		wantSessionID := "prime-native-123"
		if i == 7 || i == 8 {
			wantSessionID = ""
		}
		if calls[i].Args[2] == "session-start" && payload.SessionID != wantSessionID {
			t.Fatalf("call %d session_id = %q, want %q", i, payload.SessionID, wantSessionID)
		}
	}
}

type hookCall struct {
	Args  []string `json:"args"`
	CWD   string   `json:"cwd"`
	Input string   `json:"input"`
}

func readHookCalls(t *testing.T, path string) []hookCall {
	t.Helper()
	file, err := os.Open(path) //nolint:gosec // test-owned fixture
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	var calls []hookCall
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var call hookCall
		if err := json.Unmarshal(scanner.Bytes(), &call); err != nil {
			t.Fatal(err)
		}
		calls = append(calls, call)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return calls
}

func writeExecutable(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o700); err != nil {
		t.Fatal(err)
	}
}

func writeFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}
