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
	// server-level outage as an inconclusive ErrRuntimeUnavailable rather than
	// a per-session death (issue #3475); both outcomes mean the handle is gone.
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

// startPrivateTmuxServer brings up a tmux server that belongs to this test
// alone, and returns once it is ready to take options.
//
// Isolation is deliberate and load-bearing in two directions. `-f /dev/null`
// skips the developer's tmux.conf, so a test that needs a non-default option can
// set it explicitly and reproduce on a default machine and on CI, rather than
// silently passing because of how the developer happens to have configured tmux.
// And clearing TMUX matters more than it looks: tmux only honours TMUX_TMPDIR
// when it is not already inside a session, so `go test` run from inside tmux —
// likely, for tests about tmux — would otherwise point every call below at the
// developer's real server, mutate its global options, and let the cleanup
// kill-server destroy their sessions, AO's own agent sessions included.
func startPrivateTmuxServer(t *testing.T) {
	t.Helper()

	// os.MkdirTemp rather than t.TempDir: the tmux socket is a unix path capped
	// near 108 bytes and t.TempDir embeds the (long) test name.
	tmuxTmp, err := os.MkdirTemp("", "aotmux")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_TMPDIR", tmuxTmp)
	t.Cleanup(func() {
		_ = exec.Command("tmux", "kill-server").Run()
		_ = os.RemoveAll(tmuxTmp)
	})

	// A tmux server exits as soon as it owns no sessions, so this keep-alive
	// session is what holds it open long enough to configure and use.
	if out, err := exec.Command("tmux", "-f", "/dev/null", "new-session", "-d", "-s", "ao-test-keepalive", "sh", "-c", "sleep 120").CombinedOutput(); err != nil {
		t.Skipf("cannot start private tmux server: %v: %s", err, out)
	}
}

// tmuxOption sets one option on the private server, failing the test if it does
// not take — a test that silently ran with default options would pass without
// exercising anything.
func tmuxOption(t *testing.T, args ...string) {
	t.Helper()
	if out, err := exec.Command("tmux", args...).CombinedOutput(); err != nil {
		t.Fatalf("tmux %v: %v: %s", args, err, out)
	}
}

// TestRuntimeIntegrationRestartUnderCustomBaseIndex pins Restart against a tmux
// server whose base-index and pane-base-index are 1 rather than tmux's default
// 0 — a very common setting in a hand-written tmux.conf, since it puts window 1
// under the "1" key.
//
// Restart used to target the session's pane as "<id>:0.0". tmux honours
// base-index when new-session picks the window index, so on such a server the
// session's only window is index 1 and respawn-pane failed with
// "can't find pane: 0" — every resume of an existing agent hard-failed, for the
// whole life of the install. A config-free tmux defaults both indices to 0, so
// neither CI nor a default dev box could ever see it.
func TestRuntimeIntegrationRestartUnderCustomBaseIndex(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux unavailable")
	}
	startPrivateTmuxServer(t)
	tmuxOption(t, "set-option", "-g", "base-index", "1")
	tmuxOption(t, "set-window-option", "-g", "pane-base-index", "1")

	ctx := context.Background()
	id := "ao-baseindex-" + strings.ReplaceAll(t.Name(), "/", "_")
	r := New(Options{Timeout: 5 * time.Second})
	workspace := t.TempDir()

	h, err := r.Create(ctx, ports.RuntimeConfig{
		SessionID:     domain.SessionID(id),
		WorkspacePath: workspace,
		Argv:          []string{"sh", "-c", "echo agent-first-run"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	t.Cleanup(func() { _ = r.Destroy(context.Background(), h) })

	// Guard the guard: if this server ignored the options there is no base-index
	// 1 here and the test would pass without exercising anything. Target h.ID,
	// not id — Create truncates and hashes long session ids.
	out, err := exec.Command("tmux", "list-windows", "-t", h.ID, "-F", "#{window_index}").CombinedOutput()
	if err != nil {
		t.Fatalf("list-windows: %v: %s", err, out)
	}
	if got := strings.TrimSpace(string(out)); got != "1" {
		t.Fatalf("window index = %q, want 1 — the private server did not adopt base-index 1", got)
	}

	if _, err := r.Restart(ctx, h, ports.RuntimeConfig{
		SessionID:     domain.SessionID(id),
		WorkspacePath: workspace,
		Argv:          []string{"sh", "-c", "echo agent-resumed"},
	}); err != nil {
		t.Fatalf("Restart under base-index 1: %v", err)
	}
	if got := waitForOutput(t, r, h, "agent-resumed", 5*time.Second); !strings.Contains(got, "agent-resumed") {
		t.Fatalf("restart output = %q, want agent-resumed", got)
	}
}

// TestRuntimeIntegrationRestartTargetsAgentPaneNotActivePane pins the other half
// of the pane-target contract: the target must be deterministic, not merely
// index-independent.
//
// AO hands the user an ordinary attach client with tmux's default prefix key, so
// they can open a second window or split one at any time. Targeting the session
// by bare name resolves to whatever pane is *currently active*, which means
// Restart's `respawn-pane -k` would kill the pane the user is working in and
// respawn the agent there, leaving the real agent pane behind as a corpse that
// `has-session` still reports healthy. Naming the pane by position instead keeps
// Restart on the agent's own pane no matter what the user has focused.
//
// Deliberately left on default indices: this hazard is independent of
// base-index, and it reproduces on a stock machine.
func TestRuntimeIntegrationRestartTargetsAgentPaneNotActivePane(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux unavailable")
	}
	startPrivateTmuxServer(t)

	ctx := context.Background()
	id := "ao-panetarget-" + strings.ReplaceAll(t.Name(), "/", "_")
	r := New(Options{Timeout: 5 * time.Second})
	workspace := t.TempDir()

	h, err := r.Create(ctx, ports.RuntimeConfig{
		SessionID:     domain.SessionID(id),
		WorkspacePath: workspace,
		Argv:          []string{"sh", "-c", "echo agent-first-run"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	t.Cleanup(func() { _ = r.Destroy(context.Background(), h) })

	panePIDs := func() []string {
		t.Helper()
		out, listErr := exec.Command("tmux", "list-panes", "-s", "-t", h.ID, "-F", "#{pane_pid}").CombinedOutput()
		if listErr != nil {
			t.Fatalf("list-panes: %v: %s", listErr, out)
		}
		return strings.Fields(string(out))
	}

	before := panePIDs()
	if len(before) != 1 {
		t.Fatalf("panes after Create = %v, want exactly the agent pane", before)
	}
	agentPane := before[0]

	// The user opens a window of their own and it becomes the active one.
	tmuxOption(t, "new-window", "-t", h.ID, "sh", "-c", "sleep 120")
	userPanes := panePIDs()
	if len(userPanes) != 2 {
		t.Fatalf("panes after new-window = %v, want the agent pane plus the user's", userPanes)
	}

	if _, err := r.Restart(ctx, h, ports.RuntimeConfig{
		SessionID:     domain.SessionID(id),
		WorkspacePath: workspace,
		Argv:          []string{"sh", "-c", "echo agent-resumed"},
	}); err != nil {
		t.Fatalf("Restart: %v", err)
	}

	after := panePIDs()
	if len(after) != 2 {
		t.Fatalf("panes after Restart = %v, want both still present", after)
	}
	// The agent's pane must have been respawned...
	for _, pid := range after {
		if pid == agentPane {
			t.Fatalf("agent pane pid %s unchanged after Restart — respawn hit some other pane; panes now %v", agentPane, after)
		}
	}
	// ...and the user's pane must have survived untouched.
	userPane := userPanes[1]
	found := false
	for _, pid := range after {
		if pid == userPane {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("user's pane pid %s is gone after Restart — respawn -k killed the wrong pane; panes now %v", userPane, after)
	}
}
