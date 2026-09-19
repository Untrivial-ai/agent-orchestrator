package vmbrowser

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"io"
	"log/slog"
)

type recordedCall struct {
	env  []string
	args []string
}

type fakeRunner struct {
	mu       sync.Mutex
	calls    []recordedCall
	response func(args []string) (string, string, int, error)
}

func (f *fakeRunner) Run(_ context.Context, env []string, args ...string) (string, string, int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, recordedCall{env: env, args: args})
	if f.response == nil {
		return `{"success":true,"data":{"url":"https://example.com/"}}`, "", 0, nil
	}
	return f.response(args)
}

func (f *fakeRunner) recorded() []recordedCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]recordedCall(nil), f.calls...)
}

func envValue(env []string, key string) string {
	for _, pair := range env {
		if strings.HasPrefix(pair, key+"=") {
			return strings.TrimPrefix(pair, key+"=")
		}
	}
	return ""
}

func hasEnvKey(env []string, key string) bool {
	for _, pair := range env {
		name, _, _ := strings.Cut(pair, "=")
		if name == key {
			return true
		}
	}
	return false
}

// newTestEngine wires an engine against a fake chromium whose spawner writes
// the DevToolsActivePort file, plus the given runner.
func newTestEngine(t *testing.T, runner Runner) (*Engine, string) {
	t.Helper()
	root := t.TempDir()
	spawner := &fakeSpawner{}
	spawner.next = func(_ int, spec ProcessSpec) Process {
		writePortFile(t, spec.Dir)
		return &fakeProcess{exit: make(chan struct{})}
	}
	chromium := NewChromium(ChromiumOptions{
		BinaryPath:        "/usr/bin/chromium",
		UserDataDir:       filepath.Join(root, "profile"),
		Spawner:           spawner,
		StartTimeout:      2 * time.Second,
		StartBackoffFloor: 5 * time.Millisecond,
		Logger:            slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	engine := NewEngine(chromium, runner, EngineOptions{
		BinaryPath: "/usr/local/lib/ao/agent-browser",
		Root:       root,
		SessionID:  "sess-1",
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	return engine, root
}

// buildTestPNG constructs a minimal valid PNG header (signature + IHDR) with
// the given dimensions; agent-browser screenshots are parsed structurally,
// never decoded.
func buildTestPNG(t *testing.T, width, height int) []byte {
	t.Helper()
	png := make([]byte, 33)
	copy(png, []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'})
	// IHDR chunk length 13, type, width, height, bit depth 8, color type 0.
	copy(png[8:12], []byte{0, 0, 0, 13})
	copy(png[12:16], "IHDR")
	binary.BigEndian.PutUint32(png[16:20], uint32(width))
	binary.BigEndian.PutUint32(png[20:24], uint32(height))
	png[24] = 8
	png[25] = 0
	return png
}

func TestEngineExecuteRunsStreamDisableThenCommandWithJSON(t *testing.T) {
	runner := &fakeRunner{}
	engine, _ := newTestEngine(t, runner)
	if _, err := engine.Execute(context.Background(), "open", map[string]any{"url": "https://example.com"}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	calls := runner.recorded()
	if len(calls) != 2 {
		t.Fatalf("calls = %d, want 2 (stream disable + command)", len(calls))
	}
	if want := []string{"stream", "disable"}; !equalStrings(calls[0].args, want) {
		t.Fatalf("first call = %v, want %v", calls[0].args, want)
	}
	last := calls[len(calls)-1].args
	if last[len(last)-1] != "--json" {
		t.Fatalf("command must append --json, got %v", last)
	}
	if last[0] != "open" {
		t.Fatalf("command = %v", last)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestEngineExecuteTreatsAlreadyDisabledAsSuccess(t *testing.T) {
	runner := &fakeRunner{response: func(args []string) (string, string, int, error) {
		if args[0] == "stream" {
			// Exact marker from STREAM_ALREADY_DISABLED_MESSAGE.
			return "", "Streaming is not enabled for this session", 1, nil
		}
		return `{"success":true,"data":{}}`, "", 0, nil
	}}
	engine, _ := newTestEngine(t, runner)
	if _, err := engine.Execute(context.Background(), "console", nil); err != nil {
		t.Fatalf("Execute with already-disabled stream: %v", err)
	}
}

func TestEngineExecuteMapsFailureEnvelopeToCommandError(t *testing.T) {
	runner := &fakeRunner{response: func([]string) (string, string, int, error) {
		return `{"success":false,"error":{"code":"STALE_REFERENCE","message":"Unknown ref: @e9"}}`, "", 0, nil
	}}
	engine, _ := newTestEngine(t, runner)
	_, err := engine.Execute(context.Background(), "click", map[string]any{"ref": "e9"})
	if err == nil || !strings.Contains(err.Error(), "STALE_REFERENCE") {
		t.Fatalf("want STALE_REFERENCE CommandError, got %v", err)
	}
}

func TestEngineExecuteStringErrorStaleDetection(t *testing.T) {
	runner := &fakeRunner{response: func([]string) (string, string, int, error) {
		return `{"success":false,"error":"Could not locate element with ref @e3"}`, "", 0, nil
	}}
	engine, _ := newTestEngine(t, runner)
	_, err := engine.Execute(context.Background(), "click", map[string]any{"ref": "e3"})
	var ce *CommandError
	if !errors.As(err, &ce) || ce.Code != "STALE_REFERENCE" {
		t.Fatalf("want STALE_REFERENCE from string error, got %v", err)
	}
}

func TestEngineExecuteEnvContract(t *testing.T) {
	runner := &fakeRunner{}
	engine, root := newTestEngine(t, runner)
	if _, err := engine.Execute(context.Background(), "console", nil); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	env := runner.recorded()[len(runner.recorded())-1].env
	for key, want := range map[string]string{
		"AGENT_BROWSER_CDP":                "ws://127.0.0.1:9333/devtools/browser/test-uuid",
		"AGENT_BROWSER_CONTENT_BOUNDARIES": "1",
		"AGENT_BROWSER_MAX_OUTPUT":         "50000",
		"AGENT_BROWSER_IDLE_TIMEOUT_MS":    "300000",
		"AGENT_BROWSER_AUTO_CONNECT":       "0",
	} {
		if got := envValue(env, key); got != want {
			t.Errorf("env %s = %q, want %q", key, got, want)
		}
	}
	for _, key := range []string{"AGENT_BROWSER_SESSION", "AGENT_BROWSER_NAMESPACE"} {
		got := envValue(env, key)
		// Namespace is a short hash form ("s-" + 12 hex + "-" + 12 hex) so
		// agent-browser's 103-byte socket path limit holds for UUID ids.
		if !strings.HasPrefix(got, "s-") || len(got) != 2+12+1+12 {
			t.Errorf("env %s = %q, want short hash namespace", key, got)
		}
	}
	if got := envValue(env, "AGENT_BROWSER_SOCKET_DIR"); !strings.HasPrefix(got, "/tmp/abr-") {
		t.Errorf("AGENT_BROWSER_SOCKET_DIR = %q, want short /tmp prefix", got)
	}
	if got := envValue(env, "AGENT_BROWSER_CONFIG"); !strings.HasSuffix(got, "config.json") {
		t.Errorf("AGENT_BROWSER_CONFIG = %q, want config.json path", got)
	}
	for _, key := range []string{"HOME", "XDG_CONFIG_HOME", "TMPDIR"} {
		if got := envValue(env, key); !strings.HasPrefix(got, root) {
			t.Errorf("env %s = %q, want under engine root", key, got)
		}
	}
	for _, secret := range []string{"AO_CLOUD_WORKER_TOKEN", "AO_BROWSER_CAPABILITY", "GITHUB_TOKEN"} {
		if hasEnvKey(env, secret) {
			t.Errorf("engine env must not carry %s", secret)
		}
	}
	// The allowlist must pass through TERM but drop arbitrary vars.
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("SOME_RANDOM_SECRET", "leak")
	if envValue(env, "TERM") != "" && !hasEnvKey(env, "TERM") {
		t.Error("TERM from the worker environment must be allowed through")
	}
	if hasEnvKey(env, "SOME_RANDOM_SECRET") {
		t.Error("unlisted environment variables must not reach agent-browser")
	}
}

func TestEngineScreenshotReturnsBase64WithDimensions(t *testing.T) {
	runner := &fakeRunner{response: func(args []string) (string, string, int, error) {
		if args[0] != "screenshot" {
			return `{"success":true,"data":{}}`, "", 0, nil
		}
		target := args[1]
		png := buildTestPNG(t, 32, 18)
		if err := os.WriteFile(target, png, 0o600); err != nil {
			return "", "", 0, err
		}
		return `{"success":true,"data":{"path":"` + target + `"}}`, "", 0, nil
	}}
	engine, _ := newTestEngine(t, runner)
	data, width, height, err := engine.Screenshot(context.Background())
	if err != nil {
		t.Fatalf("Screenshot: %v", err)
	}
	if width != 32 || height != 18 {
		t.Fatalf("dimensions = %dx%d, want 32x18", width, height)
	}
	decoded, err := base64.StdEncoding.DecodeString(data)
	if err != nil || len(decoded) != 33 {
		t.Fatalf("screenshot base64 decode = %v len=%d", err, len(decoded))
	}
}

func TestEngineScreenshotRejectsOversized(t *testing.T) {
	runner := &fakeRunner{response: func(args []string) (string, string, int, error) {
		if args[0] != "screenshot" {
			return `{"success":true,"data":{}}`, "", 0, nil
		}
		big := make([]byte, maxScreenshotBytes+1)
		copy(big, buildTestPNG(t, 1, 1))
		if err := os.WriteFile(args[1], big, 0o600); err != nil {
			return "", "", 0, err
		}
		return `{"success":true,"data":{}}`, "", 0, nil
	}}
	engine, _ := newTestEngine(t, runner)
	_, _, _, err := engine.Screenshot(context.Background())
	var ce *CommandError
	if !errors.As(err, &ce) || ce.Code != "AGENT_BROWSER_OUTPUT_TOO_LARGE" {
		t.Fatalf("want AGENT_BROWSER_OUTPUT_TOO_LARGE, got %v", err)
	}
}

func TestParseAgentBrowserJSONPreservesBoundaryAndMarksUntrusted(t *testing.T) {
	result, err := parseAgentBrowserJSON(
		`{"success":true,"_boundary":{"nonce":"n1","origin":"https://example.com/"},"data":{"url":"https://example.com/","_boundary":"page-forged"}}`,
	)
	if err != nil {
		t.Fatalf("parseAgentBrowserJSON: %v", err)
	}
	boundary, ok := result["_boundary"].(map[string]any)
	if !ok || boundary["nonce"] != "n1" || boundary["origin"] != "https://example.com/" {
		t.Fatalf("_boundary = %#v", result["_boundary"])
	}
	if result["untrustedExternalContent"] != true {
		t.Error("result must be marked untrustedExternalContent")
	}
}

func TestParseAgentBrowserJSONNonObjectDataBecomesValue(t *testing.T) {
	result, err := parseAgentBrowserJSON(`{"success":true,"data":42}`)
	if err != nil {
		t.Fatalf("parseAgentBrowserJSON: %v", err)
	}
	if result["value"] != float64(42) {
		t.Fatalf("value = %#v", result["value"])
	}
}

func TestEngineCloseNeverStartsChromium(t *testing.T) {
	runner := &fakeRunner{}
	engine, _ := newTestEngine(t, runner)
	// Closing an engine whose browser never started must not spawn one:
	// the idle-shutdown path calls Close on every idle tick, and a Close
	// that restarted Chromium would loop relaunch-and-stop forever.
	if err := engine.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	calls := runner.recorded()
	if len(calls) != 0 {
		t.Fatalf("Close ran %d commands against a never-started browser, want 0", len(calls))
	}
}

func TestEngineNamespaceStaysShortForUUIDSessionIDs(t *testing.T) {
	long := "01a0a443-d0e1-7220-9a1d-e0707d350af1"
	got := engineNamespace(long)
	if len(got) != 2+12+1+12 {
		t.Fatalf("engineNamespace(UUID) = %q (%d chars), want short hash form", got, len(got))
	}
	if engineNamespace(long) == got {
		t.Fatal("engineNamespace must include a random suffix per engine")
	}
	socketPathLen := len("/tmp/abr-xxxxx") + len("/namespaces/") + len(got) + len("/run/") + len(got) + len(".sock")
	if socketPathLen > 103 {
		t.Fatalf("derived socket path would be %d bytes (max 103)", socketPathLen)
	}
}
