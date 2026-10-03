package opencodev2

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

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/opencode"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestGetAgentHooksInstallsV2AndRemovesOnlyManagedV1(t *testing.T) {
	workspace := t.TempDir()
	ctx := context.Background()
	userPlugin := filepath.Join(workspace, ".opencode", "plugins", "user.ts")
	writeV2TestFile(t, userPlugin, "export default { id: 'user' }\n", 0o600)

	if err := opencode.New().GetAgentHooks(ctx, ports.WorkspaceHookConfig{WorkspacePath: workspace}); err != nil {
		t.Fatalf("install v1 setup: %v", err)
	}
	if err := New().GetAgentHooks(ctx, ports.WorkspaceHookConfig{WorkspacePath: workspace}); err != nil {
		t.Fatalf("install v2: %v", err)
	}
	if err := New().GetAgentHooks(ctx, ports.WorkspaceHookConfig{WorkspacePath: workspace}); err != nil {
		t.Fatalf("reinstall v2: %v", err)
	}

	if installed, err := New().AreHooksInstalled(ctx, workspace); err != nil || !installed {
		t.Fatalf("AreHooksInstalled = (%v, %v), want (true, nil)", installed, err)
	}
	if _, err := os.Stat(filepath.Join(workspace, ".opencode", "plugins", "ao-activity.ts")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("managed v1 plugin survived v2 install: %v", err)
	}
	if data, err := os.ReadFile(userPlugin); err != nil || string(data) != "export default { id: 'user' }\n" {
		t.Fatalf("user plugin changed: data=%q err=%v", data, err)
	}
	if _, err := os.Stat(filepath.Join(workspace, ".opencode", "skills", "using-ao", "SKILL.md")); err != nil {
		t.Fatalf("shared using-ao skill missing: %v", err)
	}

	if err := New().UninstallHooks(ctx, workspace); err != nil {
		t.Fatalf("uninstall v2: %v", err)
	}
	if installed, err := New().AreHooksInstalled(ctx, workspace); err != nil || installed {
		t.Fatalf("AreHooksInstalled after uninstall = (%v, %v), want (false, nil)", installed, err)
	}
	if _, err := os.Stat(userPlugin); err != nil {
		t.Fatalf("uninstall removed user plugin: %v", err)
	}
	if _, err := os.Stat(filepath.Join(workspace, ".opencode", "skills", "using-ao")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("managed skill survived uninstall: %v", err)
	}
}

func TestV2HooksRejectOwnedPathCollision(t *testing.T) {
	workspace := t.TempDir()
	v2Path := filepath.Join(workspace, ".opencode", "plugins", "ao-activity-v2.ts")
	writeV2TestFile(t, v2Path, "export default { id: 'user-v2-path' }\n", 0o600)

	err := New().GetAgentHooks(context.Background(), ports.WorkspaceHookConfig{WorkspacePath: workspace})
	if err == nil {
		t.Fatal("GetAgentHooks overwrote foreign v2 path")
	}
	data, readErr := os.ReadFile(v2Path)
	if readErr != nil || string(data) != "export default { id: 'user-v2-path' }\n" {
		t.Fatalf("foreign file changed: data=%q err=%v", data, readErr)
	}
	if err := New().UninstallHooks(context.Background(), workspace); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(v2Path); err != nil || string(data) != "export default { id: 'user-v2-path' }\n" {
		t.Fatalf("uninstall changed foreign v2 file: data=%q err=%v", data, err)
	}
}

func TestV2HooksSuccessfulInstallPreservesForeignV1Plugin(t *testing.T) {
	workspace := t.TempDir()
	v1Path := filepath.Join(workspace, ".opencode", "plugins", "ao-activity.ts")
	foreign := "export default { id: 'user-v1-path' }\n"
	writeV2TestFile(t, v1Path, foreign, 0o600)

	p := New()
	if err := p.GetAgentHooks(context.Background(), ports.WorkspaceHookConfig{WorkspacePath: workspace}); err != nil {
		t.Fatalf("GetAgentHooks: %v", err)
	}
	if installed, err := p.AreHooksInstalled(context.Background(), workspace); err != nil || !installed {
		t.Fatalf("v2 install = (%v, %v), want (true, nil)", installed, err)
	}
	if data, err := os.ReadFile(v1Path); err != nil || string(data) != foreign {
		t.Fatalf("successful v2 install changed foreign v1-path plugin: data=%q err=%v", data, err)
	}
}

func TestV2HooksValidateWorkspaceAndContext(t *testing.T) {
	p := New()
	if err := p.GetAgentHooks(context.Background(), ports.WorkspaceHookConfig{}); err == nil {
		t.Fatal("GetAgentHooks accepted blank workspace")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := p.GetAgentHooks(ctx, ports.WorkspaceHookConfig{WorkspacePath: t.TempDir()}); !errors.Is(err, context.Canceled) {
		t.Fatalf("GetAgentHooks canceled error = %v", err)
	}
}

func TestManagedV2PluginLoadsWithoutWorkspaceNodeModules(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("plugin loading fixture uses Unix PATH semantics")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is required to execute the OpenCode 2 plugin fixture")
	}

	workspace := t.TempDir()
	if err := New().GetAgentHooks(context.Background(), ports.WorkspaceHookConfig{WorkspacePath: workspace}); err != nil {
		t.Fatal(err)
	}
	fixture := t.TempDir()
	modulePath := filepath.Join(fixture, "ao-activity.mjs")
	source, err := os.ReadFile(v2PluginPath(workspace))
	if err != nil {
		t.Fatal(err)
	}
	writeV2TestFile(t, modulePath, string(source), 0o600)
	harness := writeV2Harness(t, fixture)

	cmd := exec.CommandContext(context.Background(), node, harness, modulePath, workspace, "single")
	cmd.Env = append(envWithoutPath(os.Environ()), "PATH="+t.TempDir())
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("standalone managed plugin did not load: %v\n%s", err, output)
	}
}

func TestManagedV2PluginReportsLifecycleInOrderAndIgnoresHookFailures(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake ao executable fixture uses a Unix shebang")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is required to execute the OpenCode 2 plugin fixture")
	}

	workspace := t.TempDir()
	if err := New().GetAgentHooks(context.Background(), ports.WorkspaceHookConfig{WorkspacePath: workspace}); err != nil {
		t.Fatal(err)
	}
	fixture := t.TempDir()
	modulePath := filepath.Join(fixture, "ao-activity.mjs")
	source, err := os.ReadFile(v2PluginPath(workspace))
	if err != nil {
		t.Fatal(err)
	}
	// This test exercises lifecycle ordering through a Node-based fake ao, whose
	// cold startup can exceed the production bound under loaded CI. The dedicated
	// timeout test below runs the unmodified source.
	source = []byte(strings.Replace(string(source), "const HOOK_TIMEOUT_MS = 1_250", "const HOOK_TIMEOUT_MS = 10_000", 1))
	writeV2TestFile(t, modulePath, string(source), 0o600)
	capture := filepath.Join(fixture, "calls.jsonl")
	writeV2TestFile(t, filepath.Join(fixture, "ao"), `#!/usr/bin/env node
const fs = require("node:fs");
let input = "";
process.stdin.setEncoding("utf8");
process.stdin.on("data", chunk => input += chunk);
process.stdin.on("end", () => {
  fs.appendFileSync(process.env.AO_TEST_CAPTURE, JSON.stringify({ args: process.argv.slice(2), cwd: process.cwd(), input, launch: process.env.AO_RUNTIME_LAUNCH_ID }) + "\n");
  process.exit(9);
});
`, 0o755)
	harness := writeV2Harness(t, fixture)
	cmd := exec.CommandContext(context.Background(), node, harness, modulePath, workspace, "lifecycle")
	cmd.Env = append(os.Environ(),
		"PATH="+fixture+string(os.PathListSeparator)+os.Getenv("PATH"),
		"AO_TEST_CAPTURE="+capture,
		"AO_RUNTIME_LAUNCH_ID=launch-v2-7",
	)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("plugin harness failed despite hook exit 9: %v\n%s", err, output)
	}

	calls := readV2HookCalls(t, capture)
	wantEvents := []string{"session-start", "active", "active", "permission-blocked", "permission-resolved", "stop", "active", "stop", "active", "stop", "stop"}
	if len(calls) != len(wantEvents) {
		t.Fatalf("calls = %#v, want %d", calls, len(wantEvents))
	}
	for i, event := range wantEvents {
		if !reflect.DeepEqual(calls[i].Args, []string{"hooks", "opencode-v2", event}) {
			t.Fatalf("call %d args = %#v, want hooks/opencode-v2/%s", i, calls[i].Args, event)
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(strings.TrimSpace(calls[i].Input)), &payload); err != nil {
			t.Fatalf("call %d payload: %v", i, err)
		}
		if payload["session_id"] != "ses_native_v2" || payload["launch_id"] != "launch-v2-7" {
			t.Fatalf("call %d payload = %#v", i, payload)
		}
		if calls[i].Launch != "launch-v2-7" {
			t.Fatalf("call %d launch env = %q", i, calls[i].Launch)
		}
	}

	missing := exec.CommandContext(context.Background(), node, harness, modulePath, workspace, "single")
	missing.Env = append(envWithoutPath(os.Environ()), "PATH="+t.TempDir(), "AO_RUNTIME_LAUNCH_ID=launch-missing")
	if output, err := missing.CombinedOutput(); err != nil {
		t.Fatalf("plugin harness failed with ao missing: %v\n%s", err, output)
	}
}

func TestManagedV2PluginBoundsHungHook(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake ao executable fixture uses a Unix shebang")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is required to execute the OpenCode 2 plugin fixture")
	}
	workspace := t.TempDir()
	if err := New().GetAgentHooks(context.Background(), ports.WorkspaceHookConfig{WorkspacePath: workspace}); err != nil {
		t.Fatal(err)
	}
	fixture := t.TempDir()
	modulePath := filepath.Join(fixture, "ao-activity.mjs")
	source, err := os.ReadFile(v2PluginPath(workspace))
	if err != nil {
		t.Fatal(err)
	}
	writeV2TestFile(t, modulePath, string(source), 0o600)
	writeV2TestFile(t, filepath.Join(fixture, "ao"), "#!/bin/sh\nexec sleep 10\n", 0o755)
	harness := writeV2Harness(t, fixture)

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, harness, modulePath, workspace, "single")
	cmd.Env = append(os.Environ(), "PATH="+fixture+string(os.PathListSeparator)+os.Getenv("PATH"))
	started := time.Now()
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("plugin harness failed on hung hook after %v: %v\n%s", time.Since(started), err, output)
	}
	if elapsed := time.Since(started); elapsed >= 3*time.Second {
		t.Fatalf("hung hook blocked plugin for %v, want under 3s", elapsed)
	}
}

type v2HookCall struct {
	Args   []string `json:"args"`
	CWD    string   `json:"cwd"`
	Input  string   `json:"input"`
	Launch string   `json:"launch"`
}

func readV2HookCalls(t *testing.T, path string) []v2HookCall {
	t.Helper()
	f, err := os.Open(path) //nolint:gosec // test-owned path
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var calls []v2HookCall
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var call v2HookCall
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

func writeV2Harness(t *testing.T, root string) string {
	t.Helper()
	path := filepath.Join(root, "harness.mjs")
	writeV2TestFile(t, path, `import { pathToFileURL } from "node:url";
const loaded = await import(pathToFileURL(process.argv[2]).href);
const workspace = process.argv[3];
const mode = process.argv[4];
const callbacks = {};
const queue = [];
let waiter;
let closed = false;
const stream = {
  [Symbol.asyncIterator]() {
    return {
      next() {
        if (queue.length) return Promise.resolve({ done: false, value: queue.shift() });
        if (closed) return Promise.resolve({ done: true });
        return new Promise(resolve => waiter = resolve);
      },
      return() { closed = true; return Promise.resolve({ done: true }); },
    };
  },
};
function emit(value) {
  if (waiter) { const resolve = waiter; waiter = undefined; resolve({ done: false, value }); }
  else queue.push(value);
}
const context = {
  app: { name: "opencode", version: "2.0.19", channel: "test" },
  location: { directory: workspace, project: { id: "prj_test", directory: workspace, canonical: workspace } },
  event: { subscribe() { return stream; } },
  session: { async hook(name, callback) { callbacks["session:" + name] = callback; return { dispose: async () => {} }; } },
  tool: { async hook(name, callback) { callbacks["tool:" + name] = callback; return { dispose: async () => {} }; } },
};
const cleanup = await loaded.default.setup(context);
const flush = () => new Promise(resolve => setImmediate(resolve));
if (mode === "single") {
  await callbacks["tool:execute.before"]({ sessionID: "ses_native_v2", tool: "read" });
} else {
  emit({ type: "session.created", data: { sessionID: "ses_native_v2" } });
  await flush();
  emit({ type: "session.execution.started", data: { sessionID: "ses_native_v2" } });
  await flush();
  await callbacks["tool:execute.before"]({ sessionID: "ses_native_v2", tool: "read" });
  emit({ type: "permission.asked", data: { sessionID: "ses_native_v2", id: "per_1" } });
  await flush();
  emit({ type: "permission.replied", data: { sessionID: "ses_native_v2", requestID: "per_1", reply: "once" } });
  await flush();
  emit({ type: "session.execution.succeeded", data: { sessionID: "ses_native_v2" } });
  await flush();
  emit({ type: "session.execution.started", data: { sessionID: "ses_native_v2" } });
  emit({ type: "session.execution.failed", data: { sessionID: "ses_native_v2", error: { type: "unknown", message: "x" } } });
  await flush();
  emit({ type: "session.execution.started", data: { sessionID: "ses_native_v2" } });
  emit({ type: "session.execution.interrupted", data: { sessionID: "ses_native_v2", reason: "user" } });
  await flush();
  emit({ type: "session.status", data: { sessionID: "ses_native_v2", status: { type: "idle" } } });
  await flush();
}
closed = true;
if (waiter) { const resolve = waiter; waiter = undefined; resolve({ done: true }); }
await cleanup?.();
`, 0o600)
	return path
}

func writeV2TestFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

func envWithoutPath(env []string) []string {
	out := make([]string, 0, len(env))
	for _, item := range env {
		if !strings.HasPrefix(item, "PATH=") {
			out = append(out, item)
		}
	}
	return out
}
