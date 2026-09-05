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
// Tests use it when exercising the runtime's live-host authentication path.
func livePID() int { return os.Getpid() }

// deadPID returns a PID that is guaranteed to be dead (no process).
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

func TestCreatePassesCallerContextToRegistryMutations(t *testing.T) {
	isolateRegistry(t)
	type contextKey struct{}
	ctx := context.WithValue(context.Background(), contextKey{}, "registry-mutation")
	rt := New(Options{Spawner: func(context.Context, string, string, []string, map[string]string) (string, int, error) {
		return "127.0.0.1:1", livePID(), nil
	}})
	rt.pidLiveness = func(int) (bool, error) { return false, nil }
	registerCalls := 0
	rt.registerHost = func(got context.Context, entry ptyregistry.Entry) error {
		registerCalls++
		if got.Value(contextKey{}) != "registry-mutation" {
			t.Fatalf("Register context value = %v, want caller context", got.Value(contextKey{}))
		}
		return ptyregistry.Register(got, entry)
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
	rt.pidLiveness = func(int) (bool, error) { return true, nil }
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

func TestInspectRuntimeIdentityAuthenticatesDirectHost(t *testing.T) {
	isolateRegistry(t)
	hosts := map[string]*inProcHost{}
	runtime := New(Options{Spawner: fakeSpawnerFor(t, hosts, livePID())})
	handle, err := runtime.Create(context.Background(), ports.RuntimeConfig{
		SessionID:     "sess-identity",
		WorkspacePath: "/tmp/w",
		Argv:          []string{"sh"},
		Env: map[string]string{
			runtimeLaunchIDEnv: "launch-identity",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { hosts[handle.ID].cleanup(t) })

	identity, err := runtime.InspectRuntimeIdentity(context.Background(), handle, "sess-identity")
	if err != nil {
		t.Fatal(err)
	}
	if identity.LaunchID != "launch-identity" || !identity.OwnershipProven {
		t.Fatalf("identity = %+v, want authenticated launch-identity", identity)
	}
	if _, err := runtime.InspectRuntimeIdentity(context.Background(), handle, "other-session"); !errors.Is(err, ports.ErrRuntimeProbeInconclusive) {
		t.Fatalf("mismatched identity error = %v, want inconclusive", err)
	}
}

func TestRuntimeRecoversShippedProtocolV2HostWithOSIdentityProof(t *testing.T) {
	isolateRegistry(t)
	host := startInProcLegacyHost(t, "sess-legacy", "legacy-launch", livePID())
	t.Cleanup(func() { host.cleanup(t) })
	if err := ptyregistry.Register(context.Background(), ptyregistry.Entry{
		SessionID: "sess-legacy", PtyHostPID: host.pid, PipePath: host.addr,
		LaunchID:     "legacy-launch",
		RegisteredAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatal(err)
	}
	runtime := New(Options{})
	collected := 0
	revalidated := 0
	runtime.legacyCollector = func(_ context.Context, sess *hostSession, status StatusPayload) (legacyHostIdentityEvidence, error) {
		collected++
		if sess.sessionID != "sess-legacy" || sess.launchID != "legacy-launch" ||
			sess.pid != host.pid || sess.registeredAt == "" {
			t.Fatalf("legacy verifier session = %+v", sess)
		}
		if status.ProtocolVersion != conPTYStyledOutputProtocolVersion || !status.Alive || status.PID != host.pty.PID() {
			t.Fatalf("legacy verifier status = %+v", status)
		}
		startedAt := time.Now().Add(-time.Second)
		return legacyHostIdentityEvidence{
			listenerPID: host.pid,
			host: legacyProcessIdentity{
				pid: host.pid, startedAt: startedAt, executable: "/app/ao",
				argv: []string{"/app/ao", "pty-host", "sess-legacy", "/workspace", "/app/ao", "agent-process", "supervise", "--session", "sess-legacy", "--launch", "legacy-launch", "--", "agent"},
			},
			child: &legacyProcessIdentity{
				pid: host.pty.PID(), ppid: host.pid, startedAt: startedAt.Add(time.Millisecond),
				executable: "/app/ao",
				argv:       []string{"/app/ao", "agent-process", "supervise", "--session", "sess-legacy", "--launch", "legacy-launch", "--", "agent"},
			},
		}, nil
	}
	runtime.legacyRevalidator = func(_ context.Context, _ *hostSession, status StatusPayload, proof legacyHostIdentityFingerprint) error {
		revalidated++
		if proof.hostPID != host.pid || proof.childPID != host.pty.PID() || status.PID != proof.childPID {
			t.Fatalf("legacy revalidation = status %+v proof %+v", status, proof)
		}
		return nil
	}
	handle := ports.RuntimeHandle{ID: "sess-legacy"}
	if alive, err := runtime.IsAlive(context.Background(), handle); err != nil || !alive {
		t.Fatalf("IsAlive(protocol v2) = (%v, %v), want (true, nil)", alive, err)
	}
	if err := runtime.SendInput(context.Background(), handle, "legacy-input"); err != nil {
		t.Fatalf("SendInput(protocol v2): %v", err)
	}
	buf := make([]byte, len("legacy-input"))
	if _, err := io.ReadFull(host.pty.inR, buf); err != nil || string(buf) != "legacy-input" {
		t.Fatalf("legacy PTY input = %q, %v", buf, err)
	}
	if collected != 1 || revalidated != 1 {
		t.Fatalf("legacy proof calls = collect %d, revalidate %d; want 1/1", collected, revalidated)
	}
}

func TestRuntimeRejectsProtocolV2HostWhenOSIdentityIsUnproven(t *testing.T) {
	isolateRegistry(t)
	host := startInProcLegacyHost(t, "sess-legacy", "legacy-launch", livePID())
	t.Cleanup(func() { host.cleanup(t) })
	if err := ptyregistry.Register(context.Background(), ptyregistry.Entry{
		SessionID: "sess-legacy", PtyHostPID: host.pid, PipePath: host.addr,
		LaunchID: "legacy-launch", RegisteredAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatal(err)
	}
	runtime := New(Options{})
	runtime.legacyCollector = func(context.Context, *hostSession, StatusPayload) (legacyHostIdentityEvidence, error) {
		return legacyHostIdentityEvidence{}, errors.New("listener owner mismatch")
	}
	handle := ports.RuntimeHandle{ID: "sess-legacy"}
	if alive, err := runtime.IsAlive(context.Background(), handle); alive || !errors.Is(err, ports.ErrRuntimeProbeInconclusive) {
		t.Fatalf("IsAlive(unproven v2) = (%v, %v), want inconclusive", alive, err)
	}
	if err := runtime.SendInput(context.Background(), handle, "do-not-send"); !errors.Is(err, ports.ErrRuntimeProbeInconclusive) {
		t.Fatalf("SendInput(unproven v2) = %v, want inconclusive", err)
	}
	read := make(chan int, 1)
	go func() {
		buf := make([]byte, 1)
		n, _ := host.pty.inR.Read(buf)
		read <- n
	}()
	select {
	case n := <-read:
		if n > 0 {
			t.Fatal("unproven legacy host received terminal input")
		}
	case <-time.After(50 * time.Millisecond):
	}
}

// ---------------------------------------------------------------------------
// Test harness: in-process pty-host backed by a fakePTY.
// ---------------------------------------------------------------------------

// inProcHost starts a Serve engine with a fakePTY on a real 127.0.0.1:0
// listener and returns a fake spawner that returns that addr and a fake pid.
// The caller must call cleanup() to shut down the host.
type inProcHost struct {
	addr      string
	pid       int
	launchID  string
	hostToken string
	pty       *fakePTY
	ring      *Ring
	cancel    context.CancelFunc
	done      chan error
	ln        net.Listener
}

func startInProcHost(t *testing.T, sessionID string, fakePID int) *inProcHost {
	return startInProcHostWithIdentity(t, sessionID, "", "test-host-token-"+sessionID, fakePID)
}

func startInProcHostWithIdentity(t *testing.T, sessionID, launchID, hostToken string, fakePID int) *inProcHost {
	return startInProcHostProtocol(t, sessionID, launchID, hostToken, fakePID, 0)
}

func startInProcLegacyHost(t *testing.T, sessionID, launchID string, fakePID int) *inProcHost {
	return startInProcHostProtocol(
		t, sessionID, launchID, "", fakePID, conPTYStyledOutputProtocolVersion,
	)
}

func startInProcHostProtocol(t *testing.T, sessionID, launchID, hostToken string, fakePID, protocolVersion int) *inProcHost {
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
			SessionID:       sessionID,
			LaunchID:        launchID,
			HostPID:         fakePID,
			HostToken:       hostToken,
			protocolVersion: protocolVersion,
			Listener:        ln,
			PTY:             pty,
			Ring:            ring,
		})
		close(done)
	}()
	return &inProcHost{
		addr:      ln.Addr().String(),
		pid:       fakePID,
		launchID:  launchID,
		hostToken: hostToken,
		pty:       pty,
		ring:      ring,
		cancel:    cancel,
		done:      done,
		ln:        ln,
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

func (h *inProcHost) running() bool {
	select {
	case <-h.done:
		return false
	default:
		return true
	}
}

// fakeSpawnerFor returns a hostSpawner that starts an in-process host for a
// single session ID and records which sessions have been spawned.
// The returned map maps sessionID -> *inProcHost for test inspection.
func fakeSpawnerFor(t *testing.T, hosts map[string]*inProcHost, fakePID int) hostSpawner {
	t.Helper()
	return func(ctx context.Context, sessionID, cwd string, argv []string, env map[string]string) (string, int, error) {
		h := startInProcHostWithIdentity(
			t,
			sessionID,
			env[runtimeLaunchIDEnv],
			env[runtimeHostTokenEnv],
			fakePID,
		)
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
		Env: map[string]string{
			runtimeSessionIDEnv: "sess-abc",
			runtimeLaunchIDEnv:  "launch-abc",
		},
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
			if e.LaunchID != "launch-abc" {
				t.Fatalf("registry launch id = %q, want exact managed generation", e.LaunchID)
			}
		}
	}
	if !found {
		t.Fatal("session not in registry after Create")
	}

	hosts["sess-abc"].cleanup(t)
}

func TestCreate_RegistryFailureRollsBackSpawnedHost(t *testing.T) {
	isolateRegistry(t)
	var host *inProcHost
	t.Cleanup(func() {
		if host != nil {
			host.cancel()
		}
	})
	spawner := func(_ context.Context, sessionID, _ string, _ []string, env map[string]string) (string, int, error) {
		host = startInProcHostWithIdentity(
			t, sessionID, env[runtimeLaunchIDEnv], env[runtimeHostTokenEnv], deadPID(),
		)
		return host.addr, host.pid, nil
	}
	rt := New(Options{Spawner: spawner})
	rt.pidLiveness = func(int) (bool, error) { return host == nil || host.running(), nil }
	registerCalls := 0
	registryErr := errors.New("ready registry update denied")
	rt.registerHost = func(ctx context.Context, entry ptyregistry.Entry) error {
		registerCalls++
		if registerCalls == 1 {
			return ptyregistry.Register(ctx, entry)
		}
		return registryErr
	}

	handle, err := rt.Create(context.Background(), ports.RuntimeConfig{
		SessionID:     "sess-registry-failure",
		WorkspacePath: "/tmp/workspace",
		Argv:          []string{"sh"},
		Env:           map[string]string{runtimeLaunchIDEnv: "launch-registry-failure"},
	})
	if err == nil {
		t.Fatalf("Create() = (%q, nil), want registry error", handle.ID)
	}
	if !errors.Is(err, ports.ErrRuntimeProbeInconclusive) {
		t.Fatalf("Create() error = %v, want ErrRuntimeProbeInconclusive", err)
	}
	if host == nil {
		t.Fatal("Create did not reach the post-spawn registration boundary")
	}
	select {
	case <-host.done:
	case <-time.After(3 * time.Second):
		host.cleanup(t)
		t.Fatal("spawned pty-host survived failed durable registration")
	}
	rt.mu.Lock()
	_, retained := rt.sessions["sess-registry-failure"]
	rt.mu.Unlock()
	if retained {
		t.Fatal("failed Create retained a stopped in-memory host")
	}
}

func TestCreate_ManagedSessionRequiresLaunchIdentity(t *testing.T) {
	isolateRegistry(t)
	spawned := false
	rt := New(Options{Spawner: func(context.Context, string, string, []string, map[string]string) (string, int, error) {
		spawned = true
		return "", 0, errors.New("must not spawn")
	}})

	_, err := rt.Create(context.Background(), ports.RuntimeConfig{
		SessionID:     "sess-managed",
		WorkspacePath: "/tmp/workspace",
		Argv:          []string{"sh"},
		Env:           map[string]string{runtimeSessionIDEnv: "sess-managed"},
	})
	if err == nil || !strings.Contains(err.Error(), "requires a runtime launch id") {
		t.Fatalf("Create() error = %v, want missing launch identity", err)
	}
	if !errors.Is(err, ports.ErrRuntimeProbeInconclusive) {
		t.Fatalf("Create() error = %v, want no-fallback sentinel", err)
	}
	if spawned {
		t.Fatal("Create spawned a managed host without durable launch identity")
	}
}

func TestCreate_DoesNotSpawnOverExistingRegisteredHost(t *testing.T) {
	isolateRegistry(t)
	rt := New(Options{})
	h := startInProcHostWithIdentity(t, "sess-existing", "launch-existing", "existing-token", livePID())
	t.Cleanup(func() { h.cleanup(t) })
	if err := ptyregistry.Register(context.Background(), ptyregistry.Entry{
		SessionID:    "sess-existing",
		PtyHostPID:   h.pid,
		PipePath:     h.addr,
		LaunchID:     "launch-existing",
		HostToken:    h.hostToken,
		RegisteredAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatal(err)
	}
	spawned := false
	rt.spawner = func(context.Context, string, string, []string, map[string]string) (string, int, error) {
		spawned = true
		return "", 0, errors.New("must not spawn")
	}

	_, err := rt.Create(context.Background(), ports.RuntimeConfig{
		SessionID:     "sess-existing",
		WorkspacePath: "/tmp/workspace",
		Argv:          []string{"sh"},
	})
	if !errors.Is(err, ports.ErrRuntimeProbeInconclusive) {
		t.Fatalf("Create() error = %v, want inconclusive existing-owner error", err)
	}
	if spawned {
		t.Fatal("Create spawned a duplicate over a registered live pty-host")
	}
}

func TestPIDProbeFailureFailsRecoveryClosedAndPreventsReplacement(t *testing.T) {
	isolateRegistry(t)
	entry := ptyregistry.Entry{
		SessionID:    "sess-pid-probe",
		PtyHostPID:   livePID(),
		PipePath:     "127.0.0.1:1",
		LaunchID:     "launch-pid-probe",
		HostToken:    "token-pid-probe",
		RegisteredAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err := ptyregistry.Register(context.Background(), entry); err != nil {
		t.Fatal(err)
	}
	probeFailure := errors.New("injected Windows process-query failure")
	failedProbe := func(pid int) (bool, error) {
		if pid != entry.PtyHostPID {
			t.Fatalf("pid probe = %d, want %d", pid, entry.PtyHostPID)
		}
		return false, probeFailure
	}

	// Startup recovery resolves the exact durable owner before publishing
	// readiness. An OS-level PID probe failure must stop recovery rather than
	// report the runtime absent and permit a replacement.
	recovery := New(Options{})
	recovery.pidLiveness = failedProbe
	handle := ports.RuntimeHandle{ID: entry.SessionID}
	owner := ports.SupervisedProcessRef{SessionID: domain.SessionID(entry.SessionID), LaunchID: entry.LaunchID}
	resolved, found, err := recovery.ResolveExactRuntimeHandle(context.Background(), handle, owner)
	if found || resolved.ID != "" || !errors.Is(err, ports.ErrRuntimeProbeInconclusive) || !errors.Is(err, probeFailure) {
		t.Fatalf("ResolveExactRuntimeHandle() = (%q, %v, %v), want injected inconclusive failure", resolved.ID, found, err)
	}
	if alive, err := recovery.IsAlive(context.Background(), handle); alive || !errors.Is(err, ports.ErrRuntimeProbeInconclusive) || !errors.Is(err, probeFailure) {
		t.Fatalf("IsAlive() = (%v, %v), want injected inconclusive failure", alive, err)
	}

	spawned := false
	replacement := New(Options{Spawner: func(context.Context, string, string, []string, map[string]string) (string, int, error) {
		spawned = true
		return "", 0, errors.New("must not spawn")
	}})
	replacement.pidLiveness = failedProbe
	if _, err := replacement.Create(context.Background(), ports.RuntimeConfig{
		SessionID:     domain.SessionID(entry.SessionID),
		WorkspacePath: "/tmp/workspace",
		Argv:          []string{"sh"},
		Env:           map[string]string{runtimeLaunchIDEnv: entry.LaunchID},
	}); !errors.Is(err, ports.ErrRuntimeProbeInconclusive) || !errors.Is(err, probeFailure) {
		t.Fatalf("Create() error = %v, want injected inconclusive failure", err)
	}
	if spawned {
		t.Fatal("Create spawned a replacement after an inconclusive PID probe")
	}

	entries, err := ptyregistry.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0] != entry {
		t.Fatalf("registry after inconclusive PID probes = %+v, want unchanged %+v", entries, entry)
	}
}

func TestRecoveryRegistryErrorIsInconclusive(t *testing.T) {
	isolateRegistry(t)
	instanceDir := t.TempDir()
	registryPath := filepath.Join(instanceDir, "windows-pty-hosts.json")
	if err := os.MkdirAll(instanceDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(registryPath, []byte("not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	rt := New(Options{RunFilePath: filepath.Join(instanceDir, "running.json")})
	handle := ports.RuntimeHandle{ID: "sess-recovery"}

	alive, err := rt.IsAlive(context.Background(), handle)
	if err == nil || alive {
		t.Fatalf("IsAlive() = (%v, %v), want (false, registry error)", alive, err)
	}
	if !errors.Is(err, ports.ErrRuntimeProbeInconclusive) {
		t.Fatalf("IsAlive() error = %v, want ErrRuntimeProbeInconclusive", err)
	}
}

func TestResolveRuntimeHandleRejectsLaunchMismatch(t *testing.T) {
	isolateRegistry(t)
	rt := New(Options{})
	h := startInProcHostWithIdentity(t, "sess-owner", "launch-actual", "owner-token", livePID())
	t.Cleanup(func() { h.cleanup(t) })
	if err := ptyregistry.Register(context.Background(), ptyregistry.Entry{
		SessionID:    "sess-owner",
		PtyHostPID:   h.pid,
		PipePath:     h.addr,
		LaunchID:     "launch-actual",
		HostToken:    h.hostToken,
		RegisteredAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatal(err)
	}
	resolved, found, err := rt.ResolveRuntimeHandle(
		context.Background(),
		ports.RuntimeHandle{ID: "sess-owner"},
		ports.SupervisedProcessRef{SessionID: "sess-owner", LaunchID: "launch-actual"},
	)
	if err != nil || !found || resolved.ID != "sess-owner" {
		t.Fatalf("ResolveRuntimeHandle(exact owner) = (%q, %v, %v)", resolved.ID, found, err)
	}
	resolved, found, err = rt.ResolveRuntimeHandle(
		context.Background(),
		ports.RuntimeHandle{ID: "sess-owner"},
		ports.SupervisedProcessRef{SessionID: "sess-owner", LaunchID: "launch-expected"},
	)
	if err == nil || found || resolved.ID != "" {
		t.Fatalf("ResolveRuntimeHandle() = (%q, %v, %v), want launch-mismatch error", resolved.ID, found, err)
	}
	if !errors.Is(err, ports.ErrRuntimeProbeInconclusive) {
		t.Fatalf("ResolveRuntimeHandle() error = %v, want ErrRuntimeProbeInconclusive", err)
	}
}

func TestResolveRuntimeHandleRejectsLegacyEntryWithoutLaunchProof(t *testing.T) {
	isolateRegistry(t)
	instanceDir := t.TempDir()
	registryPath := filepath.Join(instanceDir, "windows-pty-hosts.json")
	legacy := fmt.Sprintf(`[{"sessionId":"sess-legacy-owner","ptyHostPid":%d,"pipePath":"127.0.0.1:50000","registeredAt":"legacy"}]`, livePID())
	if err := os.WriteFile(registryPath, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	rt := New(Options{RunFilePath: filepath.Join(instanceDir, "running.json")})

	resolved, found, err := rt.ResolveRuntimeHandle(
		context.Background(),
		ports.RuntimeHandle{ID: "sess-legacy-owner"},
		ports.SupervisedProcessRef{SessionID: "sess-legacy-owner", LaunchID: "launch-expected"},
	)
	if err == nil || found || resolved.ID != "" {
		t.Fatalf("ResolveRuntimeHandle(legacy owner) = (%q, %v, %v), want ownership error", resolved.ID, found, err)
	}
	if !errors.Is(err, ports.ErrRuntimeProbeInconclusive) {
		t.Fatalf("ResolveRuntimeHandle(legacy owner) error = %v, want inconclusive", err)
	}
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
	if !errors.Is(err, ports.ErrRuntimeProbeInconclusive) {
		t.Fatalf("duplicate Create error = %v, want ErrRuntimeProbeInconclusive to prevent fallback", err)
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

func TestClientSendMessageConnCancelsBetweenChunks(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- clientSendMessageConn(ctx, client, strings.Repeat("x", ptyInputChunkRunes+1))
	}()
	firstFrame := make([]byte, 5+ptyInputChunkRunes)
	if _, err := io.ReadFull(server, firstFrame); err != nil {
		t.Fatalf("read first chunk: %v", err)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("clientSendMessageConn error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("clientSendMessageConn ignored cancellation during chunk delay")
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
	rt.pidLiveness = func(int) (bool, error) { return h.running(), nil }

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
	if alive, err := rt.IsSupervisedProcessAlive(ctx, handle, ports.SupervisedProcessRef{SessionID: "sess-supervised", LaunchID: "launch-stale"}); alive || !errors.Is(err, ports.ErrRuntimeProbeInconclusive) {
		t.Fatalf("stale supervised generation = (%v, %v), want inconclusive", alive, err)
	}
	if alive, err := rt.IsSupervisedProcessAlive(ctx, handle, ports.SupervisedProcessRef{SessionID: "sess-supervised", LaunchID: "launch-current"}); err != nil || !alive {
		t.Fatalf("current supervised generation = (%v, %v), want (true, nil)", alive, err)
	}
	if alive, err := rt.IsExactSupervisedProcessAlive(ctx, handle, ports.SupervisedProcessRef{SessionID: "sess-supervised", LaunchID: "launch-current"}); err != nil || !alive {
		t.Fatalf("exact current supervised generation = (%v, %v), want (true, nil)", alive, err)
	}
	if alive, err := rt.IsExactSupervisedProcessAlive(ctx, handle, ports.SupervisedProcessRef{SessionID: "sess-supervised", LaunchID: "launch-stale"}); alive || !errors.Is(err, ports.ErrRuntimeProbeInconclusive) {
		t.Fatalf("exact stale supervised generation = (%v, %v), want inconclusive", alive, err)
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
func TestDestroy_KillsHostAndCleansUp(t *testing.T) {
	isolateRegistry(t)
	hosts := map[string]*inProcHost{}
	rt := New(Options{Spawner: fakeSpawnerFor(t, hosts, livePID())})
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
	rt.pidLiveness = func(int) (bool, error) { return h.running(), nil }

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

func TestDestroy_PIDWaitProbeFailurePreservesRecoveryOwner(t *testing.T) {
	isolateRegistry(t)
	hosts := map[string]*inProcHost{}
	rt := New(Options{Spawner: fakeSpawnerFor(t, hosts, livePID())})
	handle, err := rt.Create(context.Background(), ports.RuntimeConfig{
		SessionID:     "sess-destroy-probe",
		WorkspacePath: "/tmp/workspace",
		Argv:          []string{"sh"},
		Env:           map[string]string{runtimeLaunchIDEnv: "launch-destroy-probe"},
	})
	if err != nil {
		t.Fatal(err)
	}
	host := hosts[handle.ID]
	t.Cleanup(func() { host.cleanup(t) })

	probeFailure := errors.New("injected Windows wait failure")
	probeCalls := 0
	rt.pidLiveness = func(pid int) (bool, error) {
		if pid != host.pid {
			t.Fatalf("pid probe = %d, want %d", pid, host.pid)
		}
		probeCalls++
		if probeCalls == 1 {
			return true, nil // authenticate the exact host before KILL
		}
		return false, probeFailure
	}

	err = rt.Destroy(context.Background(), handle)
	if !errors.Is(err, ports.ErrRuntimeProbeInconclusive) || !errors.Is(err, probeFailure) {
		t.Fatalf("Destroy() error = %v, want injected inconclusive PID-wait failure", err)
	}
	if probeCalls != 2 {
		t.Fatalf("pid probe calls = %d, want connect plus first exit check", probeCalls)
	}

	entries, err := ptyregistry.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].SessionID != handle.ID {
		t.Fatalf("registry after inconclusive teardown = %+v, want recovery owner retained", entries)
	}
	rt.mu.Lock()
	retained := rt.sessions[handle.ID]
	rt.mu.Unlock()
	if retained == nil {
		t.Fatal("inconclusive teardown removed the in-memory recovery owner")
	}
}

func TestStaleRegistryPIDAndPortReuseCannotReachForeignHost(t *testing.T) {
	isolateRegistry(t)
	// The victim's recorded PID and port now both belong to a different valid
	// pty-host. Bare liveness probes would accept this endpoint; immutable host
	// identity must reject it before any read or mutation.
	foreign := startInProcHostWithIdentity(
		t,
		"foreign-session",
		"foreign-launch",
		"foreign-token",
		livePID(),
	)
	t.Cleanup(func() { foreign.cleanup(t) })
	if err := ptyregistry.Register(context.Background(), ptyregistry.Entry{
		SessionID:    "victim-session",
		PtyHostPID:   foreign.pid,
		PipePath:     foreign.addr,
		LaunchID:     "victim-launch",
		HostToken:    "victim-token",
		RegisteredAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatal(err)
	}
	spawned := false
	rt := New(Options{Spawner: func(context.Context, string, string, []string, map[string]string) (string, int, error) {
		spawned = true
		return "", 0, errors.New("must not spawn")
	}})
	handle := ports.RuntimeHandle{ID: "victim-session"}
	owner := ports.SupervisedProcessRef{SessionID: "victim-session", LaunchID: "victim-launch"}

	if alive, err := rt.IsAlive(context.Background(), handle); alive || !errors.Is(err, ports.ErrRuntimeProbeInconclusive) {
		t.Fatalf("IsAlive() = (%v, %v), want inconclusive foreign endpoint", alive, err)
	}
	if resolved, found, err := rt.ResolveRuntimeHandle(context.Background(), handle, owner); found || resolved.ID != "" || !errors.Is(err, ports.ErrRuntimeProbeInconclusive) {
		t.Fatalf("ResolveRuntimeHandle() = (%q, %v, %v), want inconclusive", resolved.ID, found, err)
	}
	if alive, err := rt.IsSupervisedProcessAlive(context.Background(), handle, owner); alive || !errors.Is(err, ports.ErrRuntimeProbeInconclusive) {
		t.Fatalf("IsSupervisedProcessAlive() = (%v, %v), want inconclusive", alive, err)
	}
	if alive, err := rt.IsExactSupervisedProcessAlive(context.Background(), handle, owner); alive || !errors.Is(err, ports.ErrRuntimeProbeInconclusive) {
		t.Fatalf("IsExactSupervisedProcessAlive() = (%v, %v), want inconclusive", alive, err)
	}
	if resolved, found, err := rt.ResolveExactRuntimeHandle(context.Background(), handle, owner); found || resolved.ID != "" || !errors.Is(err, ports.ErrRuntimeProbeInconclusive) {
		t.Fatalf("ResolveExactRuntimeHandle() = (%q, %v, %v), want inconclusive", resolved.ID, found, err)
	}
	if err := rt.SendMessage(context.Background(), handle, "do-not-send"); !errors.Is(err, ports.ErrRuntimeProbeInconclusive) {
		t.Fatalf("SendMessage() error = %v, want inconclusive", err)
	}
	if err := rt.SendInput(context.Background(), handle, "do-not-send"); !errors.Is(err, ports.ErrRuntimeProbeInconclusive) {
		t.Fatalf("SendInput() error = %v, want inconclusive", err)
	}
	if err := rt.Interrupt(context.Background(), handle); !errors.Is(err, ports.ErrRuntimeProbeInconclusive) {
		t.Fatalf("Interrupt() error = %v, want inconclusive", err)
	}
	if output, err := rt.GetOutput(context.Background(), handle, 10); output != "" || !errors.Is(err, ports.ErrRuntimeProbeInconclusive) {
		t.Fatalf("GetOutput() = (%q, %v), want inconclusive", output, err)
	}
	if output, err := rt.GetStyledOutput(context.Background(), handle, 10); output != "" || !errors.Is(err, ports.ErrRuntimeProbeInconclusive) {
		t.Fatalf("GetStyledOutput() = (%q, %v), want inconclusive", output, err)
	}
	if stream, err := rt.Attach(context.Background(), handle, 24, 80); err == nil || stream != nil || !errors.Is(err, ports.ErrRuntimeProbeInconclusive) {
		t.Fatalf("Attach() = (%v, %v), want inconclusive", stream, err)
	}
	if err := rt.Destroy(context.Background(), handle); !errors.Is(err, ports.ErrRuntimeProbeInconclusive) {
		t.Fatalf("Destroy() error = %v, want inconclusive", err)
	}
	if _, err := rt.Create(context.Background(), ports.RuntimeConfig{
		SessionID:     "victim-session",
		WorkspacePath: "/tmp/workspace",
		Argv:          []string{"sh"},
		Env:           map[string]string{runtimeLaunchIDEnv: "victim-launch"},
	}); !errors.Is(err, ports.ErrRuntimeProbeInconclusive) {
		t.Fatalf("Create() error = %v, want inconclusive", err)
	}
	if spawned {
		t.Fatal("Create spawned a replacement over an unproven live registry entry")
	}

	foreign.pty.closeMu.Lock()
	foreignClosed := foreign.pty.closed
	foreign.pty.closeMu.Unlock()
	if foreignClosed {
		t.Fatal("Destroy killed the foreign pty-host")
	}
	inputRead := make(chan []byte, 1)
	go func() {
		buf := make([]byte, 32)
		n, _ := foreign.pty.inR.Read(buf)
		inputRead <- append([]byte(nil), buf[:n]...)
	}()
	select {
	case got := <-inputRead:
		if len(got) > 0 {
			t.Fatalf("foreign PTY received input %q", got)
		}
	case <-time.After(50 * time.Millisecond):
		// Expected: identity rejection happened before terminal input.
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
	// the runtime's PID probe treats the durable entry as live.
	h := startInProcHost(t, "sess-reg", livePID())
	defer h.cleanup(t)

	// Manually register the host in the registry.
	err := ptyregistry.Register(context.Background(), ptyregistry.Entry{
		SessionID:    "sess-reg",
		PtyHostPID:   h.pid,
		PipePath:     h.addr, // addr stored in PipePath field
		HostToken:    h.hostToken,
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

// TestIsAlive_RefusedWithLivePIDAndTimeoutAreInconclusive is the reaper-safety
// regression test. Neither a reused live PID nor a transient loopback failure
// may be converted into proof that the registered workload is dead:
//
//	(a) a resolved-but-REFUSED host with a live recorded PID -> inconclusive
//	(b) a resolved host whose probe TIMES OUT -> (false, non-nil) [ProbeFailed]
func TestIsAlive_RefusedWithLivePIDAndTimeoutAreInconclusive(t *testing.T) {
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
	rtRefused.sessions["gone"] = &hostSession{
		sessionID: "gone", addr: refusedAddr, pid: livePID(), hostToken: "gone-token",
	}
	rtRefused.mu.Unlock()

	alive, err := rtRefused.IsAlive(context.Background(), ports.RuntimeHandle{ID: "gone"})
	if alive || !errors.Is(err, ports.ErrRuntimeProbeInconclusive) {
		t.Fatalf("IsAlive(refused) = (%v, %v), want live-PID inconclusive", alive, err)
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
	rtSilent.sessions["stuck"] = &hostSession{
		sessionID: "stuck", addr: silent.Addr().String(), pid: livePID(), hostToken: "stuck-token",
	}
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
	if err := clientKill(context.Background(), "127.0.0.1:1"); err != nil {
		t.Fatalf("clientKill on unreachable addr: %v", err)
	}
}

// Ensure the packages compile (import check).
var _ = io.Discard
