package tmux

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestRuntimeIntegration(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux unavailable")
	}

	ctx := context.Background()
	id := strings.ReplaceAll(t.Name(), "/", "_")
	r := New(Options{Timeout: 5 * time.Second})

	// Ensure clean slate: ignore errors (session may not exist).
	_ = r.Destroy(ctx, ports.RuntimeHandle{ID: id})

	t.Cleanup(func() {
		// Always destroy so a test failure never leaks a tmux session.
		_ = r.Destroy(context.Background(), ports.RuntimeHandle{ID: id})
	})

	h, err := r.Create(ctx, ports.RuntimeConfig{
		SessionID:     domain.SessionID(id),
		WorkspacePath: t.TempDir(),
		// Run a trivial command then drop into an interactive shell (the keep-alive
		// exec is added by buildLaunchCommand, but we also verify here that output
		// appears).
		Argv: []string{"sh", "-c", "echo hello-from-tmux"},
		Env:  map[string]string{"AO_SESSION_ID": id},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	alive, err := r.IsAlive(ctx, h)
	if err != nil {
		t.Fatalf("IsAlive: %v", err)
	}
	if !alive {
		t.Fatal("alive = false, want true after create")
	}

	// Wait for the echo output to appear (the session may take a moment to
	// write it to the pane history).
	out := waitForOutput(t, r, h, "hello-from-tmux", 5*time.Second)
	if !strings.Contains(out, "hello-from-tmux") {
		t.Fatalf("output = %q, want hello-from-tmux", out)
	}

	// Send a command and verify it echoes back.
	if err := r.SendMessage(ctx, h, "echo hello-send"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	out = waitForOutput(t, r, h, "hello-send", 5*time.Second)
	if !strings.Contains(out, "hello-send") {
		t.Fatalf("output after SendMessage = %q, want hello-send", out)
	}

	// Destroy and verify liveness goes false. When this was the server's last
	// session the server itself exits with it, and the probe reports the
	// server-level outage as ErrRuntimeUnavailable rather than a per-session
	// false result (issue #3475); both outcomes mean the tmux handle is gone.
	if err := r.Destroy(ctx, h); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	alive, err = r.IsAlive(ctx, h)
	if err != nil && !errors.Is(err, ports.ErrRuntimeUnavailable) {
		t.Fatalf("IsAlive after destroy: %v", err)
	}
	if alive {
		t.Fatal("alive after destroy = true, want false")
	}
}

// TestRuntimeIntegrationExactSessionParsing verifies that IsAlive uses exact
// session matching and does not treat a prefix as a live session.
func TestRuntimeIntegrationExactSessionParsing(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux unavailable")
	}

	ctx := context.Background()
	base := strings.ReplaceAll(t.Name(), "/", "_")
	longID := base + "_long"
	prefixID := base

	r := New(Options{Timeout: 5 * time.Second})
	_ = r.Destroy(ctx, ports.RuntimeHandle{ID: longID})
	_ = r.Destroy(ctx, ports.RuntimeHandle{ID: prefixID})

	t.Cleanup(func() {
		_ = r.Destroy(context.Background(), ports.RuntimeHandle{ID: longID})
		_ = r.Destroy(context.Background(), ports.RuntimeHandle{ID: prefixID})
	})

	h, err := r.Create(ctx, ports.RuntimeConfig{
		SessionID:     domain.SessionID(longID),
		WorkspacePath: t.TempDir(),
		Argv:          []string{"sh", "-c", "echo ready"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// tmux has-session -t <prefix> should NOT match <longID> because tmux
	// requires the exact session name when using -t with a plain string (not a
	// glob). Verify by probing the prefix handle directly.
	prefixAlive, err := r.IsAlive(ctx, ports.RuntimeHandle{ID: prefixID})
	if err != nil {
		// tmux may return an error (session not found) rather than exit 0.
		// That is acceptable here: the point is the prefix must not be alive.
		t.Logf("IsAlive prefix returned error (acceptable): %v", err)
	}
	if prefixAlive {
		_ = r.Destroy(ctx, h)
		t.Fatal("prefix handle reported alive; tmux session matching is not exact")
	}
}

func TestRuntimeIntegrationLegacyDefaultSocketIgnoresInheritedTMUX(t *testing.T) {
	systemTmux, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux unavailable")
	}

	// tmux's Unix socket path has a small platform limit; Go's ordinary test
	// temp root is long enough to exceed it on macOS.
	tmuxTmpDir, err := os.MkdirTemp("/tmp", "ao-tmux-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tmuxTmpDir) })
	t.Setenv("TMUX_TMPDIR", tmuxTmpDir)
	legacyID := strings.ReplaceAll(t.Name(), "/", "_") + "_legacy"
	spoofID := strings.ReplaceAll(t.Name(), "/", "_") + "_spoof"
	privateID := strings.ReplaceAll(t.Name(), "/", "_") + "_private"
	for _, socketName := range []string{"default", "spoof", "ao"} {
		t.Cleanup(func() {
			_ = exec.Command(systemTmux, "-L", socketName, "kill-server").Run()
		})
	}
	start := func(socketName, sessionID string) {
		t.Helper()
		if out, startErr := exec.Command(
			systemTmux,
			"-L", socketName,
			"new-session", "-d", "-s", sessionID,
			"sleep 30",
		).CombinedOutput(); startErr != nil {
			t.Fatalf("start tmux -L %s: %v: %s", socketName, startErr, out)
		}
	}
	start("default", legacyID)
	start("spoof", spoofID)
	start("ao", privateID)

	spoofIdentity, err := exec.Command(
		systemTmux,
		"-L", "spoof",
		"display-message", "-p", "#{socket_path},#{pid},0",
	).Output()
	if err != nil {
		t.Fatalf("read spoof socket identity: %v", err)
	}
	t.Setenv("TMUX", strings.TrimSpace(string(spoofIdentity)))
	if out, err := exec.Command(systemTmux, "has-session", "-t", spoofID).CombinedOutput(); err != nil {
		t.Fatalf("test setup did not redirect plain tmux through inherited TMUX: %v: %s", err, out)
	}

	r := New(Options{
		Binary:       systemTmux,
		LegacyBinary: systemTmux,
		SocketName:   "ao",
		Timeout:      5 * time.Second,
	})
	alive, err := r.IsAlive(context.Background(), ports.RuntimeHandle{ID: legacyID})
	if err != nil || !alive {
		t.Fatalf("legacy default-socket session = (%v, %v), want (true, nil)", alive, err)
	}
	alive, err = r.IsAlive(context.Background(), ports.RuntimeHandle{ID: spoofID})
	if err != nil {
		t.Fatalf("spoof-only session probe: %v", err)
	}
	if alive {
		t.Fatal("spoof-only session was misclassified as AO's legacy session")
	}
}

func TestRuntimeIntegrationAdoptsLegacyDefaultWhenNamedSocketDoesNotExist(t *testing.T) {
	systemTmux, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux unavailable")
	}

	// Isolate both socket names so the test starts with a live legacy default
	// server and no named AO server, matching an untouched pre-cutover install.
	tmuxTmpDir, err := os.MkdirTemp("/tmp", "ao-tmux-migration-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tmuxTmpDir) })
	t.Setenv("TMUX_TMPDIR", tmuxTmpDir)
	legacyID := strings.ReplaceAll(t.Name(), "/", "_") + "_legacy"
	for _, socketName := range []string{"default", "ao"} {
		t.Cleanup(func() {
			_ = exec.Command(systemTmux, "-L", socketName, "kill-server").Run()
		})
	}
	if out, startErr := exec.Command(
		systemTmux,
		"-L", "default",
		"new-session", "-d", "-s", legacyID,
		"sh",
	).CombinedOutput(); startErr != nil {
		t.Fatalf("start legacy tmux session: %v: %s", startErr, out)
	}
	missingOut, missingErr := exec.Command(systemTmux, "-L", "ao", "has-session", "-t", legacyID).CombinedOutput()
	if missingErr == nil {
		t.Fatal("test setup unexpectedly found a named AO server")
	}
	if !migrationSocketAbsentOutput(string(missingOut)) {
		t.Fatalf("named AO probe = %q, want missing-socket diagnostic", missingOut)
	}

	r := New(Options{
		Binary:       systemTmux,
		LegacyBinary: systemTmux,
		SocketName:   "ao",
		Timeout:      5 * time.Second,
	})
	r.enterDelay = 0
	handle := ports.RuntimeHandle{ID: legacyID}
	alive, err := r.IsAlive(context.Background(), handle)
	if err != nil || !alive {
		t.Fatalf("legacy default-socket session = (%v, %v), want (true, nil)", alive, err)
	}
	if err := r.SendMessage(context.Background(), handle, "echo legacy-send-ok"); err != nil {
		t.Fatalf("SendMessage to adopted legacy session: %v", err)
	}
	out := waitForOutput(t, r, handle, "legacy-send-ok", 5*time.Second)
	if !strings.Contains(out, "legacy-send-ok") {
		t.Fatalf("legacy output = %q, want legacy-send-ok", out)
	}
	if out, probeErr := exec.Command(systemTmux, "-L", "ao", "list-sessions").CombinedOutput(); probeErr == nil {
		t.Fatalf("legacy discovery unexpectedly created named AO server: %s", out)
	}
}

func TestRuntimeIntegrationSupervisedExitKeepsInteractiveShell(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux unavailable")
	}

	ctx := context.Background()
	id := strings.ReplaceAll(t.Name(), "/", "_")
	const launchID = "launch-1"
	r := New(Options{Timeout: 5 * time.Second})
	tmuxID := SessionName(id)
	workspace := t.TempDir()
	_ = r.Destroy(ctx, ports.RuntimeHandle{ID: tmuxID})
	t.Cleanup(func() { _ = r.Destroy(context.Background(), ports.RuntimeHandle{ID: tmuxID}) })

	// Re-run this test binary as a long-lived helper with the same controlled
	// command-line identity as AO's supervisor. The CLI package separately tests
	// that the real supervisor waits for and reports its child.
	h, err := r.Create(ctx, ports.RuntimeConfig{
		SessionID:     domain.SessionID(id),
		WorkspacePath: workspace,
		Argv:          []string{os.Args[0], "-test.run=TestSupervisorProcessHelper", "--", "agent-process", "supervise", "--session", id, "--launch", launchID, "--"},
		Env:           map[string]string{"AO_TMUX_SUPERVISOR_HELPER": "1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	ref := ports.SupervisedProcessRef{SessionID: domain.SessionID(id), LaunchID: launchID}
	deadline := time.Now().Add(5 * time.Second)
	for {
		alive, probeErr := r.IsSupervisedProcessAlive(ctx, h, ref)
		if probeErr != nil {
			t.Fatal(probeErr)
		}
		if alive {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("supervised workload did not appear in the tmux process tree")
		}
		time.Sleep(100 * time.Millisecond)
	}

	// The helper exits normally, matching Codex /exit or EOF. The launch shell
	// must then execute AO's keep-alive interactive shell.
	deadline = time.Now().Add(5 * time.Second)
	for {
		alive, probeErr := r.IsSupervisedProcessAlive(ctx, h, ref)
		if probeErr != nil {
			t.Fatal(probeErr)
		}
		if !alive {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("supervised workload remained alive after normal exit")
		}
		time.Sleep(100 * time.Millisecond)
	}
	if alive, err := r.IsAlive(ctx, h); err != nil || !alive {
		t.Fatalf("tmux after workload exit = (%v, %v), want (true, nil)", alive, err)
	}
	if err := r.SendMessage(ctx, h, "echo shell-after-agent-exit"); err != nil {
		t.Fatal(err)
	}
	out := waitForOutput(t, r, h, "shell-after-agent-exit", 5*time.Second)
	if !strings.Contains(out, "shell-after-agent-exit") {
		t.Fatalf("post-exit shell output = %q", out)
	}

	restarted, err := r.Restart(ctx, h, ports.RuntimeConfig{
		SessionID:     domain.SessionID(id),
		WorkspacePath: workspace,
		Argv:          []string{"sh", "-c", "echo managed-agent-resumed"},
	})
	if err != nil {
		t.Fatalf("Restart: %v", err)
	}
	if restarted != h {
		t.Fatalf("restart handle = %+v, want existing handle %+v", restarted, h)
	}
	out = waitForOutput(t, r, restarted, "managed-agent-resumed", 5*time.Second)
	if !strings.Contains(out, "managed-agent-resumed") {
		t.Fatalf("restart output = %q, want managed-agent-resumed", out)
	}
	if err := r.SendMessage(ctx, restarted, "echo shell-after-managed-resume"); err != nil {
		t.Fatal(err)
	}
	out = waitForOutput(t, r, restarted, "shell-after-managed-resume", 5*time.Second)
	if !strings.Contains(out, "shell-after-managed-resume") {
		t.Fatalf("post-resume shell output = %q", out)
	}
}

func TestSupervisorProcessHelper(t *testing.T) {
	if os.Getenv("AO_TMUX_SUPERVISOR_HELPER") != "1" {
		return
	}
	time.Sleep(2 * time.Second)
}

// waitForOutput polls GetOutput until out contains want or the deadline passes.
func waitForOutput(t *testing.T, r *Runtime, h ports.RuntimeHandle, want string, deadline time.Duration) string {
	t.Helper()
	end := time.Now().Add(deadline)
	var out string
	for time.Now().Before(end) {
		var err error
		out, err = r.GetOutput(context.Background(), h, 50)
		if err != nil {
			t.Fatalf("GetOutput: %v", err)
		}
		if strings.Contains(out, want) {
			return out
		}
		time.Sleep(100 * time.Millisecond)
	}
	return out
}
