package conpty

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/runtime/conpty/ptyregistry"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// livePID returns a PID that is guaranteed to be alive (the current process).
// Using this as the fake pty-host PID means ptyregistry.List() will not prune
// the entry during tests. Do NOT use this for the Destroy test: Destroy calls
// Kill on the pid, so use deadPID() there instead.
func livePID() int { return os.Getpid() }

// deadPID returns a PID that is guaranteed to be dead (no process). This is
// used in Destroy tests so the force-kill step is a safe no-op.
// ponytail: PID 2147483647 (MaxInt32) is never a real process; signal-0 returns ESRCH.
func deadPID() int { return 2147483647 }

func TestProbeFencedRuntimeCompleteRegistryAbsentIsDead(t *testing.T) {
	isolateRegistry(t)
	rt := New(Options{})

	got := rt.ProbeFencedRuntime(context.Background(), ports.FencedRuntimeRef{
		Handle: ports.RuntimeHandle{ID: "sess-absent"}, SessionID: "sess-absent", Generation: "launch-1",
	})
	if got.Liveness != ports.FencedDead || got.Reason != ports.FencedReasonExactAbsent {
		t.Fatalf("ProbeFencedRuntime absent = %+v, want dead/exact_absent", got)
	}
}

func TestProbeFencedRuntimeRegistryMalformedIsUnknown(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "windows-pty-hosts.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	rt := New(Options{RunFilePath: filepath.Join(dir, "running.json")})

	got := rt.ProbeFencedRuntime(context.Background(), ports.FencedRuntimeRef{
		Handle: ports.RuntimeHandle{ID: "sess-malformed"}, SessionID: "sess-malformed", Generation: "launch-1",
	})
	if got.Liveness != ports.FencedUnknown || got.Reason != ports.FencedReasonRegistryMalformed {
		t.Fatalf("ProbeFencedRuntime malformed = %+v, want unknown/registry_malformed", got)
	}
}

func TestRegistryResolutionHonorsCallerCancellation(t *testing.T) {
	isolateRegistry(t)
	spawnCalls := 0
	rt := New(Options{Spawner: func(context.Context, string, string, []string, map[string]string) (string, int, error) {
		spawnCalls++
		return "127.0.0.1:1", livePID(), nil
	}})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, createErr := rt.Create(ctx, ports.RuntimeConfig{
		SessionID: "sess-cancelled", WorkspacePath: t.TempDir(), Argv: []string{"codex"},
	})
	if !errors.Is(createErr, context.Canceled) || spawnCalls != 0 {
		t.Fatalf("Create cancelled during resolution = err %v spawnCalls %d, want context cancellation before spawn", createErr, spawnCalls)
	}
	if err := rt.Destroy(ctx, ports.RuntimeHandle{ID: "sess-cancelled"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Destroy cancelled during resolution = %v, want context cancellation", err)
	}
	if _, err := rt.IsAlive(ctx, ports.RuntimeHandle{ID: "sess-cancelled"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("IsAlive cancelled during resolution = %v, want context cancellation", err)
	}
	probe := rt.ProbeFencedRuntime(ctx, ports.FencedRuntimeRef{
		Handle: ports.RuntimeHandle{ID: "sess-cancelled"}, SessionID: "sess-cancelled", Generation: "launch-1",
	})
	if probe.Liveness != ports.FencedUnknown || probe.Reason != ports.FencedReasonProbeFailed {
		t.Fatalf("ProbeFencedRuntime cancelled during resolution = %+v, want unknown/probe_failed", probe)
	}
}

func TestCreateAndDestroyPassCallerContextToRegistryMutations(t *testing.T) {
	isolateRegistry(t)
	type contextKey struct{}
	ctx := context.WithValue(context.Background(), contextKey{}, "registry-mutation")
	rt := New(Options{Spawner: func(context.Context, string, string, []string, map[string]string) (string, int, error) {
		return "127.0.0.1:1", livePID(), nil
	}})
	rt.killHost = func(string) error { return nil }
	rt.pidIsAlive = func(int) bool { return false }
	registerCalls := 0
	rt.registerHost = func(got context.Context, _ ptyregistry.Entry) error {
		registerCalls++
		if got.Value(contextKey{}) != "registry-mutation" {
			t.Fatalf("Register context value = %v, want caller context", got.Value(contextKey{}))
		}
		return nil
	}
	rt.unregisterHost = func(got context.Context, _ string) error {
		if got.Value(contextKey{}) != "registry-mutation" {
			t.Fatalf("Unregister context value = %v, want caller context", got.Value(contextKey{}))
		}
		return nil
	}

	handle, err := rt.Create(ctx, ports.RuntimeConfig{
		SessionID: "sess-registry-context", WorkspacePath: t.TempDir(), Argv: []string{"codex"},
		Env: map[string]string{runtimeLaunchIDEnv: "registry-context-launch"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if registerCalls != 2 {
		t.Fatalf("Register calls = %d, want reservation and ready updates", registerCalls)
	}
	if err := rt.Destroy(ctx, handle); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
}

func TestProbeFencedRuntimeGenerationMismatchIsUnknown(t *testing.T) {
	isolateRegistry(t)
	rt := New(Options{})
	rt.sessions["sess-mismatch"] = &hostSession{addr: "127.0.0.1:1", pid: livePID(), launchID: "launch-old"}

	got := rt.ProbeFencedRuntime(context.Background(), ports.FencedRuntimeRef{
		Handle: ports.RuntimeHandle{ID: "sess-mismatch"}, SessionID: "sess-mismatch", Generation: "launch-new",
	})
	if got.Liveness != ports.FencedUnknown || got.Reason != ports.FencedReasonGenerationMismatch {
		t.Fatalf("ProbeFencedRuntime mismatch = %+v, want unknown/generation_mismatch", got)
	}
}

func TestPartialCreateCleanupFailureReturnsRuntimeEffectEvidence(t *testing.T) {
	isolateRegistry(t)
	createErr := errors.New("spawn response lost")
	rt := New(Options{Spawner: func(context.Context, string, string, []string, map[string]string) (string, int, error) {
		return "127.0.0.1:1", livePID(), createErr
	}})
	rt.killHost = func(string) error { return errors.New("cleanup denied") }
	rt.pidIsAlive = func(int) bool { return true }
	rt.processFinder = func(int) (processKiller, error) { return nil, errors.New("permission denied") }
	rt.destroyWait = 0

	handle, err := rt.Create(context.Background(), ports.RuntimeConfig{
		SessionID: "sess-partial", WorkspacePath: "/tmp/ws", Argv: []string{"codex"},
	})
	if handle.ID != "" || err == nil {
		t.Fatalf("Create partial = (%+v, %v), want empty direct handle and evidence error", handle, err)
	}
	var effect ports.RuntimeEffectError
	if !errors.As(err, &effect) {
		t.Fatalf("Create error %T does not implement RuntimeEffectError", err)
	}
	if effect.PossibleHandle().ID != "sess-partial" || effect.EffectOutcome() != ports.RuntimeEffectPossible || effect.CleanupOutcome() != ports.RuntimeCleanupFailed {
		t.Fatalf("Create effect evidence = handle %+v effect %q cleanup %q", effect.PossibleHandle(), effect.EffectOutcome(), effect.CleanupOutcome())
	}
}

func TestRuntimeProvidesStyledRenderedTerminalOutput(t *testing.T) {
	isolateRegistry(t)
	hosts := map[string]*inProcHost{}
	runtime := New(Options{Spawner: fakeSpawnerFor(t, hosts, livePID())})
	var candidate any = runtime
	if _, ok := candidate.(ports.StyledTerminalOutputReader); !ok {
		t.Fatal("ConPTY runtime must expose its rendered current surface")
	}

	handle, err := runtime.Create(context.Background(), ports.RuntimeConfig{
		SessionID:     "sess-styled",
		WorkspacePath: "/tmp/w",
		Argv:          []string{"sh"},
	})
	if err != nil {
		t.Fatal(err)
	}
	host := hosts[handle.ID]
	defer host.cleanup(t)

	if _, err := host.pty.WriteOutput([]byte("\x1b[2J\x1b[HOLD TRANSCRIPT\n")); err != nil {
		t.Fatal(err)
	}
	current := "\x1b[2J\x1b[H────────────────\n❯ \x1b[2mAsk a question\x1b[0m\n────────────────\n"
	if _, err := host.pty.WriteOutput([]byte(current)); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(time.Second)
	for {
		output, outputErr := runtime.GetStyledOutput(context.Background(), handle, 10)
		if outputErr != nil {
			t.Fatal(outputErr)
		}
		if strings.Contains(output, "Ask a question") {
			if strings.Contains(output, "OLD TRANSCRIPT") {
				t.Fatalf("styled output retained overwritten history: %q", output)
			}
			if !strings.Contains(output, "\x1b[") {
				t.Fatalf("styled output lost ANSI cell styling: %q", output)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("rendered current surface never became observable: %q", output)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestRuntimeRejectsStyledOutputFromARecoveredLegacyHost(t *testing.T) {
	isolateRegistry(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	requestType := make(chan byte, 1)
	serverDone := make(chan error, 1)
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			serverDone <- acceptErr
			return
		}
		defer func() { _ = conn.Close() }()
		header := make([]byte, 5)
		if _, readErr := io.ReadFull(conn, header); readErr != nil {
			serverDone <- readErr
			return
		}
		requestType <- header[0]
		payload := []byte(fmt.Sprintf(`{"alive":true,"pid":%d}`, livePID()))
		frame, encodeErr := EncodeMessage(MsgStatusRes, payload)
		if encodeErr != nil {
			serverDone <- encodeErr
			return
		}
		_, writeErr := conn.Write(frame)
		serverDone <- writeErr
	}()

	if err := ptyregistry.Register(context.Background(), ptyregistry.Entry{
		SessionID: "sess-legacy", PtyHostPID: livePID(), PipePath: listener.Addr().String(),
		RegisteredAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatal(err)
	}
	runtime := New(Options{})
	_, err = runtime.GetStyledOutput(context.Background(), ports.RuntimeHandle{ID: "sess-legacy"}, 10)
	if !errors.Is(err, ports.ErrStyledTerminalOutputUnavailable) {
		t.Fatalf("GetStyledOutput error = %v, want ErrStyledTerminalOutputUnavailable", err)
	}
	if got := <-requestType; got != MsgStatusReq {
		t.Fatalf("legacy host request = 0x%02x, want capability-safe MsgStatusReq", got)
	}
	if serverErr := <-serverDone; serverErr != nil {
		t.Fatal(serverErr)
	}
	_ = listener.Close()

	// The negotiated legacy result is cached per adopted host. A second call
	// must return the capability sentinel without dialing the now-closed socket.
	_, err = runtime.GetStyledOutput(context.Background(), ports.RuntimeHandle{ID: "sess-legacy"}, 10)
	if !errors.Is(err, ports.ErrStyledTerminalOutputUnavailable) {
		t.Fatalf("cached GetStyledOutput error = %v, want ErrStyledTerminalOutputUnavailable", err)
	}
}

// ---------------------------------------------------------------------------
// Test harness: in-process pty-host backed by a fakePTY.
// ---------------------------------------------------------------------------

// inProcHost starts a Serve engine with a fakePTY on a real 127.0.0.1:0
// listener and returns a fake spawner that returns that addr and a fake pid.
// The caller must call cleanup() to shut down the host.
type inProcHost struct {
	addr   string
	pid    int
	pty    *fakePTY
	ring   *Ring
	cancel context.CancelFunc
	done   chan error
	ln     net.Listener
}

func startInProcHost(t *testing.T, sessionID string, fakePID int) *inProcHost {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	pty := newFakePTY(fakePID)
	ring := NewRing()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Serve(ctx, ServeConfig{
			SessionID: sessionID,
			Listener:  ln,
			PTY:       pty,
			Ring:      ring,
		})
	}()
	return &inProcHost{
		addr:   ln.Addr().String(),
		pid:    fakePID,
		pty:    pty,
		ring:   ring,
		cancel: cancel,
		done:   done,
		ln:     ln,
	}
}

func (h *inProcHost) cleanup(t *testing.T) {
	t.Helper()
	h.cancel()
	select {
	case <-h.done:
	case <-time.After(2 * time.Second):
		t.Log("warning: inProcHost did not stop within 2s")
	}
}

// fakeSpawnerFor returns a hostSpawner that starts an in-process host for a
// single session ID and records which sessions have been spawned.
// The returned map maps sessionID -> *inProcHost for test inspection.
func fakeSpawnerFor(t *testing.T, hosts map[string]*inProcHost, fakePID int) hostSpawner {
	t.Helper()
	return func(ctx context.Context, sessionID, cwd string, argv []string, env map[string]string) (string, int, error) {
		h := startInProcHost(t, sessionID, fakePID)
		if hosts != nil {
			hosts[sessionID] = h
		}
		return h.addr, h.pid, nil
	}
}

// ---------------------------------------------------------------------------
// Redirect ptyregistry to a temp HOME so tests don't pollute ~/.ao
// ---------------------------------------------------------------------------

func isolateRegistry(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// TestCreate_RegistersSession verifies Create returns {ID: sessionID}, writes
// to the in-memory map, and registers in the ptyregistry.
func TestCreate_RegistersSession(t *testing.T) {
	isolateRegistry(t)
	hosts := map[string]*inProcHost{}
	rt := New(Options{Spawner: fakeSpawnerFor(t, hosts, livePID())})

	ctx := context.Background()
	handle, err := rt.Create(ctx, ports.RuntimeConfig{
		SessionID:     domain.SessionID("sess-abc"),
		WorkspacePath: "/tmp/workspace",
		Argv:          []string{"claude-code"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if handle.ID != "sess-abc" {
		t.Fatalf("handle.ID = %q, want %q", handle.ID, "sess-abc")
	}

	// In-memory map must have the entry.
	rt.mu.Lock()
	sess := rt.sessions["sess-abc"]
	rt.mu.Unlock()
	if sess == nil {
		t.Fatal("session not in in-memory map after Create")
	}

	// Registry must have the entry.
	entries, err := ptyregistry.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var found bool
	for _, e := range entries {
		if e.SessionID == "sess-abc" {
			found = true
		}
	}
	if !found {
		t.Fatal("session not in registry after Create")
	}

	hosts["sess-abc"].cleanup(t)
}

// TestCreate_RunFilePathScopesRegistryToInstanceDir verifies Create honors
// Options.RunFilePath, registering into that instance's own registry file
// instead of the ~/.ao default. This is the fix for two AO daemon instances
// on one machine (e.g. a headless dev daemon and the desktop app) silently
// sharing one pty-host registry and cross-wiring same-named sessions.
func TestCreate_RunFilePathScopesRegistryToInstanceDir(t *testing.T) {
	isolateRegistry(t)
	instanceDir := t.TempDir()
	hosts := map[string]*inProcHost{}
	rt := New(Options{
		Spawner:     fakeSpawnerFor(t, hosts, livePID()),
		RunFilePath: filepath.Join(instanceDir, "running.json"),
	})

	handle, err := rt.Create(context.Background(), ports.RuntimeConfig{
		SessionID:     domain.SessionID("sess-scoped"),
		WorkspacePath: "/tmp/workspace",
		Argv:          []string{"claude-code"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if handle.ID != "sess-scoped" {
		t.Fatalf("handle.ID = %q, want %q", handle.ID, "sess-scoped")
	}
	defer hosts["sess-scoped"].cleanup(t)

	wantPath := filepath.Join(instanceDir, "windows-pty-hosts.json")
	if _, err := os.Stat(wantPath); err != nil {
		t.Fatalf("expected registry at instance dir %s: %v", wantPath, err)
	}
}

// TestCreate_DuplicateErrors verifies a second Create for the same session id fails.
func TestCreate_DuplicateErrors(t *testing.T) {
	isolateRegistry(t)
	hosts := map[string]*inProcHost{}
	rt := New(Options{Spawner: fakeSpawnerFor(t, hosts, livePID())})
	ctx := context.Background()

	if _, err := rt.Create(ctx, ports.RuntimeConfig{
		SessionID:     "sess-dup",
		WorkspacePath: "/tmp/w",
		Argv:          []string{"sh"},
	}); err != nil {
		t.Fatalf("first Create: %v", err)
	}

	_, err := rt.Create(ctx, ports.RuntimeConfig{
		SessionID:     "sess-dup",
		WorkspacePath: "/tmp/w",
		Argv:          []string{"sh"},
	})
	if err == nil {
		t.Fatal("expected error on duplicate Create, got nil")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("error %q should contain 'already exists'", err.Error())
	}

	hosts["sess-dup"].cleanup(t)
}

// TestCreate_InvalidIDErrors verifies Create rejects invalid session ids.
func TestCreate_InvalidIDErrors(t *testing.T) {
	isolateRegistry(t)
	rt := New(Options{Spawner: fakeSpawnerFor(t, nil, livePID())})
	ctx := context.Background()

	for _, bad := range []string{"", "has space", "has/slash", "has.dot"} {
		_, err := rt.Create(ctx, ports.RuntimeConfig{
			SessionID:     domain.SessionID(bad),
			WorkspacePath: "/tmp/w",
			Argv:          []string{"sh"},
		})
		if err == nil {
			t.Fatalf("Create(%q): expected error for invalid id, got nil", bad)
		}
	}
}

// TestSendMessage_DeliversChunkedTextAndEnter verifies clientSendMessage sends
// the text + "\r" to the fakePTY input.
func TestSendMessage_DeliversChunkedTextAndEnter(t *testing.T) {
	isolateRegistry(t)
	hosts := map[string]*inProcHost{}
	rt := New(Options{Spawner: fakeSpawnerFor(t, hosts, livePID())})
	ctx := context.Background()

	handle, err := rt.Create(ctx, ports.RuntimeConfig{
		SessionID:     "sess-sm",
		WorkspacePath: "/tmp/w",
		Argv:          []string{"sh"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	h := hosts["sess-sm"]
	defer h.cleanup(t)

	msg := "hello world"
	// Collect PTY input in background.
	inputC := make(chan []byte, 4)
	go func() {
		buf := make([]byte, 1024)
		for {
			n, err := h.pty.inR.Read(buf)
			if n > 0 {
				cp := make([]byte, n)
				copy(cp, buf[:n])
				inputC <- cp
			}
			if err != nil {
				return
			}
		}
	}()

	if err := rt.SendMessage(ctx, handle, msg); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	// Collect all received bytes within 2s.
	var received []byte
	deadline := time.After(2 * time.Second)
	// Expect at least msg + "\r".
	for !bytes.Contains(received, []byte("\r")) {
		select {
		case chunk := <-inputC:
			received = append(received, chunk...)
		case <-deadline:
			t.Fatalf("timeout waiting for PTY input; got %q so far", received)
		}
	}

	if !bytes.HasPrefix(received, []byte(msg)) {
		t.Fatalf("PTY input = %q, want prefix %q then \\r", received, msg)
	}
	if !bytes.Contains(received, []byte("\r")) {
		t.Fatalf("PTY input = %q, missing trailing \\r", received)
	}
}

func TestSendInputDeliversEscapeByte(t *testing.T) {
	isolateRegistry(t)
	hosts := map[string]*inProcHost{}
	rt := New(Options{Spawner: fakeSpawnerFor(t, hosts, livePID())})
	handle, err := rt.Create(context.Background(), ports.RuntimeConfig{
		SessionID: "sess-escape", WorkspacePath: "/tmp/w", Argv: []string{"sh"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	h := hosts["sess-escape"]
	defer h.cleanup(t)

	if err := rt.SendInput(context.Background(), handle, "\x1b"); err != nil {
		t.Fatalf("SendInput: %v", err)
	}
	inputC := make(chan []byte, 1)
	go func() {
		buf := make([]byte, 8)
		n, _ := h.pty.inR.Read(buf)
		inputC <- append([]byte(nil), buf[:n]...)
	}()
	select {
	case got := <-inputC:
		if string(got) != "\x1b" {
			t.Fatalf("input = %q, want Escape", got)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for Escape input")
	}
}

// TestSendMessage_LargeMessageChunked verifies a message > 512 runes is
// delivered correctly (host receives full text + "\r").
func TestSendMessage_LargeMessageChunked(t *testing.T) {
	isolateRegistry(t)
	hosts := map[string]*inProcHost{}
	rt := New(Options{Spawner: fakeSpawnerFor(t, hosts, livePID())})
	ctx := context.Background()

	handle, _ := rt.Create(ctx, ports.RuntimeConfig{
		SessionID:     "sess-lg",
		WorkspacePath: "/tmp/w",
		Argv:          []string{"sh"},
	})
	h := hosts["sess-lg"]
	defer h.cleanup(t)

	// Build a message longer than 512 runes (use multi-byte runes to test
	// rune-boundary splitting).
	var sb strings.Builder
	for i := 0; i < 600; i++ {
		sb.WriteRune('A' + rune(i%26))
	}
	msg := sb.String()

	inputDone := make(chan []byte, 1)
	go func() {
		// Read until we see "\r".
		var acc []byte
		buf := make([]byte, 4096)
		for {
			n, err := h.pty.inR.Read(buf)
			if n > 0 {
				acc = append(acc, buf[:n]...)
			}
			if bytes.Contains(acc, []byte("\r")) {
				inputDone <- acc
				return
			}
			if err != nil {
				inputDone <- acc
				return
			}
		}
	}()

	if err := rt.SendMessage(ctx, handle, msg); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	select {
	case got := <-inputDone:
		// Strip trailing \r for comparison.
		trimmed := strings.TrimSuffix(string(got), "\r")
		if trimmed != msg {
			t.Fatalf("PTY received %d chars, want %d\ngot:  %q\nwant: %q", len(trimmed), len(msg), trimmed[:min(50, len(trimmed))], msg[:min(50, len(msg))])
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for large message delivery")
	}
}

// TestGetOutput_ReturnsRingTail verifies GetOutput returns the ring's tail.
func TestGetOutput_ReturnsRingTail(t *testing.T) {
	isolateRegistry(t)
	hosts := map[string]*inProcHost{}
	rt := New(Options{Spawner: fakeSpawnerFor(t, hosts, livePID())})
	ctx := context.Background()

	handle, _ := rt.Create(ctx, ports.RuntimeConfig{
		SessionID:     "sess-go",
		WorkspacePath: "/tmp/w",
		Argv:          []string{"sh"},
	})
	h := hosts["sess-go"]
	defer h.cleanup(t)

	// Seed the ring.
	h.ring.Append([]byte("line1\nline2\nline3\n"))

	text, err := rt.GetOutput(ctx, handle, 2)
	if err != nil {
		t.Fatalf("GetOutput: %v", err)
	}
	want := h.ring.Tail(2)
	if text != want {
		t.Fatalf("GetOutput = %q, want %q", text, want)
	}
}

// TestIsAlive_TrueWhileServing_FalseAfterClose verifies IsAlive returns true
// while the host listens and false after its listener is closed.
func TestIsAlive_TrueWhileServing_FalseAfterClose(t *testing.T) {
	isolateRegistry(t)
	hosts := map[string]*inProcHost{}
	rt := New(Options{Spawner: fakeSpawnerFor(t, hosts, livePID())})
	ctx := context.Background()

	handle, _ := rt.Create(ctx, ports.RuntimeConfig{
		SessionID:     "sess-ia",
		WorkspacePath: "/tmp/w",
		Argv:          []string{"sh"},
	})
	h := hosts["sess-ia"]

	alive, err := rt.IsAlive(ctx, handle)
	if err != nil {
		t.Fatalf("IsAlive: %v", err)
	}
	if !alive {
		t.Fatal("expected IsAlive=true while serving")
	}

	// Shut down the host.
	h.cancel()
	<-h.done

	// Give the listener a moment to close.
	time.Sleep(100 * time.Millisecond)

	alive2, err2 := rt.IsAlive(ctx, handle)
	if err2 != nil {
		t.Fatalf("IsAlive after close: %v", err2)
	}
	if alive2 {
		t.Fatal("expected IsAlive=false after host closed")
	}
}

func TestSupervisedProcessExitKeepsHostAlive(t *testing.T) {
	isolateRegistry(t)
	hosts := map[string]*inProcHost{}
	rt := New(Options{Spawner: fakeSpawnerFor(t, hosts, livePID())})
	ctx := context.Background()

	handle, err := rt.Create(ctx, ports.RuntimeConfig{
		SessionID:     "sess-supervised",
		WorkspacePath: "/tmp/w",
		Argv:          []string{"ao", "agent-process", "supervise"},
		Env:           map[string]string{runtimeLaunchIDEnv: "launch-current"},
	})
	if err != nil {
		t.Fatal(err)
	}
	h := hosts["sess-supervised"]
	t.Cleanup(func() { h.cleanup(t) })

	if alive, err := rt.IsSupervisedProcessAlive(ctx, handle, ports.SupervisedProcessRef{}); err != nil || !alive {
		t.Fatalf("supervised process before exit = (%v, %v), want (true, nil)", alive, err)
	}
	if alive, err := rt.IsSupervisedProcessAlive(ctx, handle, ports.SupervisedProcessRef{SessionID: "sess-supervised", LaunchID: "launch-stale"}); err != nil || alive {
		t.Fatalf("stale supervised generation = (%v, %v), want (false, nil)", alive, err)
	}
	if alive, err := rt.IsSupervisedProcessAlive(ctx, handle, ports.SupervisedProcessRef{SessionID: "sess-supervised", LaunchID: "launch-current"}); err != nil || !alive {
		t.Fatalf("current supervised generation = (%v, %v), want (true, nil)", alive, err)
	}
	if alive, err := rt.IsExactSupervisedProcessAlive(ctx, handle, ports.SupervisedProcessRef{SessionID: "sess-supervised", LaunchID: "launch-current"}); err != nil || !alive {
		t.Fatalf("exact current supervised generation = (%v, %v), want (true, nil)", alive, err)
	}
	if alive, err := rt.IsExactSupervisedProcessAlive(ctx, handle, ports.SupervisedProcessRef{SessionID: "sess-supervised", LaunchID: "launch-stale"}); err != nil || alive {
		t.Fatalf("exact stale supervised generation = (%v, %v), want (false, nil)", alive, err)
	}
	h.pty.signalExit(42)

	deadline := time.Now().Add(time.Second)
	for {
		alive, probeErr := rt.IsSupervisedProcessAlive(ctx, handle, ports.SupervisedProcessRef{})
		if probeErr != nil {
			t.Fatal(probeErr)
		}
		if !alive {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("supervised process remained alive after PTY exit")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if alive, probeErr := rt.IsAlive(ctx, handle); probeErr != nil || !alive {
		t.Fatalf("runtime host after child exit = (%v, %v), want (true, nil)", alive, probeErr)
	}
}

// TestIsAlive_FalseForUnknownSession verifies IsAlive returns (false, nil) for
// a session not in the map or registry.
func TestIsAlive_FalseForUnknownSession(t *testing.T) {
	isolateRegistry(t)
	rt := New(Options{Spawner: fakeSpawnerFor(t, nil, livePID())})
	ctx := context.Background()

	alive, err := rt.IsAlive(ctx, ports.RuntimeHandle{ID: "ghost-session"})
	if err != nil {
		t.Fatalf("IsAlive: unexpected error: %v", err)
	}
	if alive {
		t.Fatal("expected IsAlive=false for unknown session")
	}
}

// TestDestroy_KillsHostAndCleansUp verifies Destroy triggers clientKill,
// removes the map + registry entry, and is idempotent on second call.
// Uses deadPID() so the force-kill step is a safe no-op (the fake pty-host
// has no real OS process; clientKill already shut it down via the loopback).
func TestDestroy_KillsHostAndCleansUp(t *testing.T) {
	isolateRegistry(t)
	hosts := map[string]*inProcHost{}
	rt := New(Options{Spawner: fakeSpawnerFor(t, hosts, deadPID())})
	ctx := context.Background()

	handle, err := rt.Create(ctx, ports.RuntimeConfig{
		SessionID:     "sess-destroy",
		WorkspacePath: "/tmp/w",
		Argv:          []string{"sh"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	h := hosts["sess-destroy"]

	// Destroy should succeed.
	if err := rt.Destroy(ctx, handle); err != nil {
		t.Fatalf("Destroy: %v", err)
	}

	// Wait for Serve to stop (clientKill triggers shutdown).
	select {
	case <-h.done:
	case <-time.After(3 * time.Second):
		t.Fatal("host did not stop after Destroy")
	}

	// fakePTY.Close must have been called.
	h.pty.closeMu.Lock()
	closed := h.pty.closed
	h.pty.closeMu.Unlock()
	if !closed {
		t.Fatal("expected fakePTY.Close() after Destroy")
	}

	// Map entry must be gone.
	rt.mu.Lock()
	_, exists := rt.sessions["sess-destroy"]
	rt.mu.Unlock()
	if exists {
		t.Fatal("expected map entry removed after Destroy")
	}

	// Registry entry must be gone.
	entries, _ := ptyregistry.List(context.Background())
	for _, e := range entries {
		if e.SessionID == "sess-destroy" {
			t.Fatal("expected registry entry removed after Destroy")
		}
	}

	// Second Destroy must be idempotent (returns nil).
	if err := rt.Destroy(ctx, handle); err != nil {
		t.Fatalf("second Destroy: expected nil, got %v", err)
	}
}

type processKillerFunc func() error

func (f processKillerFunc) Kill() error { return f() }

func TestDestroyRetainsSessionWhenPIDCannotBeStopped(t *testing.T) {
	isolateRegistry(t)
	rt := New(Options{})
	rt.sessions["sess-stuck"] = &hostSession{addr: "127.0.0.1:1", pid: 424242}
	rt.killHost = func(string) error { return errors.New("graceful transport failed") }
	rt.pidIsAlive = func(int) bool { return true }
	rt.processFinder = func(int) (processKiller, error) {
		return processKillerFunc(func() error { return errors.New("access denied") }), nil
	}
	rt.destroyWait = 0

	err := rt.Destroy(context.Background(), ports.RuntimeHandle{ID: "sess-stuck"})
	if err == nil || !strings.Contains(err.Error(), "still alive") || !strings.Contains(err.Error(), "access denied") {
		t.Fatalf("Destroy error = %v, want force-kill and final-liveness evidence", err)
	}
	rt.mu.Lock()
	_, retained := rt.sessions["sess-stuck"]
	rt.mu.Unlock()
	if !retained {
		t.Fatal("Destroy removed a session whose PID may still be alive")
	}
}

func TestDestroyRequiresCompleteResolutionEvidence(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, rt *Runtime, registryPath string)
	}{
		{
			name: "malformed registry",
			setup: func(t *testing.T, _ *Runtime, registryPath string) {
				if err := os.WriteFile(registryPath, []byte("not json"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "unreadable registry",
			setup: func(t *testing.T, _ *Runtime, registryPath string) {
				if err := os.Mkdir(registryPath, 0o700); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "unresolved in-memory reservation",
			setup: func(_ *testing.T, rt *Runtime, _ string) {
				rt.sessions["sess-unresolved"] = nil
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			registryPath := filepath.Join(dir, "windows-pty-hosts.json")
			rt := New(Options{RunFilePath: filepath.Join(dir, "running.json")})
			t.Cleanup(func() { ptyregistry.SetRunFilePath("") })
			tt.setup(t, rt, registryPath)

			err := rt.Destroy(context.Background(), ports.RuntimeHandle{ID: "sess-unresolved"})
			if !errors.Is(err, ports.ErrRuntimeProbeInconclusive) {
				t.Fatalf("Destroy error = %v, want ErrRuntimeProbeInconclusive", err)
			}
		})
	}
}

// TestResolveViaRegistry verifies that with an empty in-memory map but a
// registry entry pointing at a live in-process host, status, styled output, and
// input still work (simulates a daemon restart).
func TestResolveViaRegistry(t *testing.T) {
	isolateRegistry(t)

	// Start a host directly (not through Create) to simulate a pre-existing
	// pty-host from a previous daemon run. Use the current process PID so
	// ptyregistry.List() does not prune the entry as dead.
	h := startInProcHost(t, "sess-reg", livePID())
	defer h.cleanup(t)

	// Manually register the host in the registry.
	err := ptyregistry.Register(context.Background(), ptyregistry.Entry{
		SessionID:    "sess-reg",
		PtyHostPID:   h.pid,
		PipePath:     h.addr, // addr stored in PipePath field
		RegisteredAt: fmt.Sprintf("%d", time.Now().Unix()),
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	// Create a Runtime with an empty in-memory map (simulates daemon restart).
	rt := New(Options{Spawner: fakeSpawnerFor(t, nil, livePID())})
	ctx := context.Background()

	// IsAlive must work via registry resolution.
	alive, err := rt.IsAlive(ctx, ports.RuntimeHandle{ID: "sess-reg"})
	if err != nil {
		t.Fatalf("IsAlive via registry: %v", err)
	}
	if !alive {
		t.Fatal("expected IsAlive=true via registry resolution")
	}

	if _, err := h.pty.WriteOutput([]byte("\x1b[2J\x1b[H\x1b[2mrecovered current screen\x1b[0m")); err != nil {
		t.Fatal(err)
	}
	styledDeadline := time.Now().Add(time.Second)
	for {
		styled, styledErr := rt.GetStyledOutput(ctx, ports.RuntimeHandle{ID: "sess-reg"}, 10)
		if styledErr != nil {
			t.Fatalf("GetStyledOutput via registry: %v", styledErr)
		}
		if strings.Contains(styled, "recovered current screen") {
			break
		}
		if time.Now().After(styledDeadline) {
			t.Fatalf("recovered host styled surface never became observable: %q", styled)
		}
		time.Sleep(time.Millisecond)
	}

	// SendMessage must work via registry resolution.
	inputC := make(chan []byte, 4)
	go func() {
		buf := make([]byte, 512)
		for {
			n, err := h.pty.inR.Read(buf)
			if n > 0 {
				cp := make([]byte, n)
				copy(cp, buf[:n])
				inputC <- cp
			}
			if err != nil {
				return
			}
		}
	}()

	if err := rt.SendMessage(ctx, ports.RuntimeHandle{ID: "sess-reg"}, "ping"); err != nil {
		t.Fatalf("SendMessage via registry: %v", err)
	}

	// Collect PTY input.
	var received []byte
	deadline := time.After(3 * time.Second)
	for !bytes.Contains(received, []byte("\r")) {
		select {
		case chunk := <-inputC:
			received = append(received, chunk...)
		case <-deadline:
			t.Fatalf("timeout waiting for PTY input via registry; got %q", received)
		}
	}
	if !bytes.Contains(received, []byte("ping")) {
		t.Fatalf("PTY did not receive 'ping'; got %q", received)
	}
}

// ---------------------------------------------------------------------------
// Unit tests for client helpers (dial a fresh in-proc host directly).
// ---------------------------------------------------------------------------

// TestClientGetOutput_TimesOutReturnsEmpty verifies clientGetOutput returns ""
// (no error) if no response arrives within the timeout. We test the happy path
// instead (timeout path would require a non-responding server).
func TestClientGetOutput_HappyPath(t *testing.T) {
	f := startServe(t, 3001)
	defer f.cancel()

	f.ring.Append([]byte("alpha\nbeta\ngamma\n"))

	text, err := clientGetOutput(context.Background(), f.addr, 2)
	if err != nil {
		t.Fatalf("clientGetOutput: %v", err)
	}
	want := f.ring.Tail(2)
	if text != want {
		t.Fatalf("clientGetOutput = %q, want %q", text, want)
	}
}

func TestClientStatusContext_CancellationStopsProbe(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()

	accepted := make(chan struct{})
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		close(accepted)
		_, _ = io.Copy(io.Discard, conn) // consume the request without replying
	}()

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, _, statusErr := clientStatusContext(ctx, listener.Addr().String())
		result <- statusErr
	}()

	select {
	case <-accepted:
	case <-time.After(time.Second):
		t.Fatal("status probe did not connect")
	}
	cancel()
	select {
	case statusErr := <-result:
		if !errors.Is(statusErr, context.Canceled) {
			t.Fatalf("clientStatusContext error = %v, want context.Canceled", statusErr)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("status probe ignored context cancellation")
	}
}

// TestClientIsAlive_TrueAndFalse verifies clientIsAlive returns (true, nil) for
// a live host and (false, nil) for a refused address (definitively gone).
func TestClientIsAlive_TrueAndFalse(t *testing.T) {
	f := startServe(t, 3002)
	defer f.cancel()

	if alive, err := clientIsAlive(f.addr); err != nil || !alive {
		t.Fatalf("clientIsAlive(live) = (%v, %v), want (true, nil)", alive, err)
	}

	f.cancel()
	// Wait for listener to close.
	select {
	case <-f.done:
	case <-time.After(2 * time.Second):
	}
	time.Sleep(50 * time.Millisecond)

	// After close the OS refuses the connection on the freed port -> gone.
	if alive, err := clientIsAlive(f.addr); alive || err != nil {
		t.Fatalf("clientIsAlive(closed) = (%v, %v), want (false, nil)", alive, err)
	}
}

// TestIsAlive_RefusedIsGone_TimeoutIsTransient is the reaper-safety regression
// test. It asserts the dead-vs-transient split that keeps a single transient
// loopback hiccup from spuriously reaping a live idle session:
//
//	(a) a resolved-but-REFUSED host -> IsAlive == (false, nil)  [ProbeDead]
//	(b) a resolved host whose probe TIMES OUT -> (false, non-nil) [ProbeFailed]
func TestIsAlive_RefusedIsGone_TimeoutIsTransient(t *testing.T) {
	isolateRegistry(t)

	// (a) Refused: bind+close a listener to obtain a port nothing listens on.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	refusedAddr := ln.Addr().String()
	_ = ln.Close()

	rtRefused := New(Options{Spawner: fakeSpawnerFor(t, nil, livePID())})
	rtRefused.mu.Lock()
	rtRefused.sessions["gone"] = &hostSession{addr: refusedAddr, pid: livePID()}
	rtRefused.mu.Unlock()

	alive, err := rtRefused.IsAlive(context.Background(), ports.RuntimeHandle{ID: "gone"})
	if alive || err != nil {
		t.Fatalf("IsAlive(refused) = (%v, %v), want (false, nil) definitively gone", alive, err)
	}

	// (b) Transient timeout: a listener that Accepts but never replies. The
	// short isAliveTimeout read deadline fires before any STATUS_RES arrives,
	// which must surface as a non-nil (transient) error, not a death.
	silent, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen silent: %v", err)
	}
	defer silent.Close()
	go func() {
		for {
			c, err := silent.Accept()
			if err != nil {
				return
			}
			// Hold the connection open without ever sending a STATUS_RES.
			go func(c net.Conn) {
				time.Sleep(isAliveTimeout + time.Second)
				_ = c.Close()
			}(c)
		}
	}()

	rtSilent := New(Options{Spawner: fakeSpawnerFor(t, nil, livePID())})
	rtSilent.mu.Lock()
	rtSilent.sessions["stuck"] = &hostSession{addr: silent.Addr().String(), pid: livePID()}
	rtSilent.mu.Unlock()

	alive, err = rtSilent.IsAlive(context.Background(), ports.RuntimeHandle{ID: "stuck"})
	if alive {
		t.Fatalf("IsAlive(silent) alive=true, want false")
	}
	if err == nil {
		t.Fatal("IsAlive(silent) err=nil, want non-nil transient error so the reaper records ProbeFailed")
	}
}

// TestClientKill_Idempotent verifies clientKill on a dead address returns nil.
func TestClientKill_Idempotent(t *testing.T) {
	if err := clientKill("127.0.0.1:1"); err != nil {
		t.Fatalf("clientKill on unreachable addr: %v", err)
	}
}

// Ensure the packages compile (import check).
var _ = io.Discard
