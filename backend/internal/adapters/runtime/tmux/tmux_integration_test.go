package tmux

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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

func TestRuntimeIntegrationGenerationFencePreservesForeignSession(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux unavailable")
	}
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_TMPDIR", t.TempDir())
	ctx := context.Background()
	r := New(Options{Timeout: 5 * time.Second})
	id := SessionName(strings.ReplaceAll(t.Name(), "/", "_"))
	workspace := t.TempDir()
	t.Cleanup(func() { _ = r.Destroy(context.Background(), ports.RuntimeHandle{ID: id}) })

	if _, err := r.run(ctx, scopedNewSessionArgs(id, workspace, r.shell, "exec sleep 30", "launch-winner")...); err != nil {
		t.Fatalf("create marked session: %v", err)
	}
	loser := ports.RuntimeHandle{ID: id, RuntimeLaunchID: "launch-loser"}
	if outcome, err := r.destroyGenerationSession(ctx, loser); err != nil || outcome != generationForeign {
		t.Fatalf("foreign fence = (%v, %v), want foreign", outcome, err)
	}
	if alive, err := r.IsAlive(ctx, ports.RuntimeHandle{ID: id}); err != nil || !alive {
		t.Fatalf("winner after foreign cleanup = (%v, %v)", alive, err)
	}
	marker, present, err := r.observedRuntimeLaunchID(ctx, id)
	if err != nil || !present || marker != "launch-winner" {
		t.Fatalf("winner marker = (%q, %v, %v)", marker, present, err)
	}
	out, err := r.run(ctx, fencedRespawnPaneArgs(id, "launch-loser", workspace, r.shell, "exec sleep 30", "launch-replacement")...)
	if err != nil || strings.TrimSpace(string(out)) != runtimeLaunchReportPrefix+"launch-winner" {
		t.Fatalf("foreign restart fence = (%q, %v)", strings.TrimSpace(string(out)), err)
	}
	out, err = r.run(ctx, fencedRespawnPaneArgs(id, "launch-winner", workspace, r.shell, "exec sleep 30", "launch-replacement")...)
	if err != nil || strings.TrimSpace(string(out)) != "" {
		t.Fatalf("matching restart fence = (%q, %v)", strings.TrimSpace(string(out)), err)
	}
	marker, present, err = r.observedRuntimeLaunchID(ctx, id)
	if err != nil || !present || marker != "launch-replacement" {
		t.Fatalf("replacement marker = (%q, %v, %v)", marker, present, err)
	}
	out, err = r.run(ctx, fencedRestoreRuntimeLaunchIDArgs(id, "launch-loser", "launch-winner")...)
	if err != nil || strings.TrimSpace(string(out)) != runtimeLaunchReportPrefix+"launch-replacement" {
		t.Fatalf("foreign restore fence = (%q, %v)", strings.TrimSpace(string(out)), err)
	}
	out, err = r.run(ctx, fencedRestoreRuntimeLaunchIDArgs(id, "launch-replacement", "launch-winner")...)
	if err != nil || strings.TrimSpace(string(out)) != "" {
		t.Fatalf("matching restore fence = (%q, %v)", strings.TrimSpace(string(out)), err)
	}
	marker, present, err = r.observedRuntimeLaunchID(ctx, id)
	if err != nil || !present || marker != "launch-winner" {
		t.Fatalf("restored marker = (%q, %v, %v)", marker, present, err)
	}
	winner := ports.RuntimeHandle{ID: id, RuntimeLaunchID: "launch-winner"}
	if outcome, err := r.destroyGenerationSession(ctx, winner); err != nil || outcome != generationDestroyed {
		t.Fatalf("matching fence = (%v, %v), want destroyed", outcome, err)
	}
}

// TestRuntimeIntegrationSystemdContainment is an explicit host canary, not a
// default CI test. It proves the opt-in scope owns a child that calls setsid,
// while an unrelated process outside the scope survives the worker teardown.
func TestRuntimeIntegrationSystemdContainment(t *testing.T) {
	if os.Getenv("AO_TEST_SYSTEMD_CONTAINMENT") != "1" {
		t.Skip("set AO_TEST_SYSTEMD_CONTAINMENT=1 to run the host systemd canary")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Fatal("tmux unavailable: explicit systemd containment canary cannot run")
	}
	if _, err := exec.LookPath("systemd-run"); err != nil {
		t.Fatal("systemd-run unavailable: explicit systemd containment canary cannot run")
	}
	if _, err := exec.Command("systemctl", "--user", "show-environment").CombinedOutput(); err != nil {
		t.Fatalf("systemd user manager unavailable: %v", err)
	}

	t.Setenv("TMUX", "")
	t.Setenv("TMUX_TMPDIR", t.TempDir())
	ctx := context.Background()
	id := strings.ReplaceAll(t.Name(), "/", "_")
	workspace := t.TempDir()
	oldPIDPath := filepath.Join(workspace, "old.pid")
	newPIDPath := filepath.Join(workspace, "new.pid")
	oldLaunchID := "integration-old"
	newLaunchID := "integration-new"
	oldUnit := containmentUnitName(SessionName(id), oldLaunchID)
	newUnit := containmentUnitName(SessionName(id), newLaunchID)
	r := New(Options{Timeout: 5 * time.Second, ProcessContainment: processContainmentSystemd})
	handle := ports.RuntimeHandle{ID: SessionName(id)}
	_ = r.Destroy(ctx, handle)

	negative := exec.Command("sh", "-c", "trap '' TERM; while :; do sleep 1; done")
	if err := negative.Start(); err != nil {
		t.Fatalf("start negative-control process: %v", err)
	}
	negativePID := negative.Process.Pid
	negativeStart := mustProcStartTime(t, negativePID)
	t.Cleanup(func() {
		_ = r.Destroy(context.Background(), handle)
		_ = negative.Process.Kill()
		_ = negative.Wait()
	})

	oldCommand := setsidPIDCommand(oldPIDPath)
	created, err := r.Create(ctx, ports.RuntimeConfig{
		SessionID:       domain.SessionID(id),
		RuntimeLaunchID: oldLaunchID,
		WorkspacePath:   workspace,
		Argv:            []string{"sh", "-c", oldCommand},
	})
	if err != nil {
		t.Fatalf("scoped Create: %v", err)
	}
	handle = created
	if handle.RuntimeLaunchID != oldLaunchID {
		t.Fatalf("Create handle = %+v, want launch %q", handle, oldLaunchID)
	}
	if marker, present, markerErr := r.observedRuntimeLaunchID(ctx, handle.ID); markerErr != nil || !present || marker != oldLaunchID {
		t.Fatalf("Create marker = (%q, %v, %v), want %q", marker, present, markerErr, oldLaunchID)
	}
	oldPID := waitForPIDFile(t, oldPIDPath)
	oldSID, oldStart := mustProcIdentity(t, oldPID)
	panePIDs := r.paneSessionIDs(ctx, handle.ID)
	if len(panePIDs) != 1 {
		t.Fatalf("pane session pids = %#v, want one pane", panePIDs)
	}
	paneSID, _ := mustProcIdentity(t, panePIDs[0])
	if oldSID == paneSID {
		t.Fatalf("setsid child SID = pane SID = %d, want escaped session", oldSID)
	}
	if !procInUnit(oldPID, oldUnit) {
		t.Fatalf("escaped child pid %d is not in expected unit %s", oldPID, oldUnit)
	}

	restarted, err := r.Restart(ctx, handle, ports.RuntimeConfig{
		SessionID:       domain.SessionID(id),
		RuntimeLaunchID: newLaunchID,
		WorkspacePath:   workspace,
		Argv:            []string{"sh", "-c", setsidPIDCommand(newPIDPath)},
	})
	if err != nil {
		t.Fatalf("scoped Restart: %v", err)
	}
	if restarted.ID != handle.ID || restarted.RuntimeLaunchID != newLaunchID {
		t.Fatalf("Restart handle = %+v, want stable id %q and launch %q", restarted, handle.ID, newLaunchID)
	}
	waitForProcessGone(t, oldPID, oldStart)
	if state := systemdUnitStateForTest(t, oldUnit); state != "" && !strings.Contains(state, "inactive") && !strings.Contains(state, "dead") && !strings.Contains(state, "not-found") {
		t.Fatalf("old scope after Restart = %q", state)
	}
	if marker, present, markerErr := r.observedRuntimeLaunchID(ctx, restarted.ID); markerErr != nil || !present || marker != newLaunchID {
		t.Fatalf("Restart marker = (%q, %v, %v), want %q", marker, present, markerErr, newLaunchID)
	}
	newPID := waitForPIDFile(t, newPIDPath)
	newSID, _ := mustProcIdentity(t, newPID)
	if newSID == paneSID {
		t.Fatalf("restarted setsid child SID = pane SID = %d, want escaped session", newSID)
	}
	if !procInUnit(newPID, newUnit) {
		t.Fatalf("restarted child pid %d is not in expected unit %s", newPID, newUnit)
	}
	if err := r.SendMessage(ctx, restarted, "echo systemd-containment-terminal-ok"); err != nil {
		t.Fatal(err)
	}
	if out := waitForOutput(t, r, restarted, "systemd-containment-terminal-ok", 5*time.Second); !strings.Contains(out, "systemd-containment-terminal-ok") {
		t.Fatalf("terminal output = %q", out)
	}

	newStart := mustProcStartTime(t, newPID)
	if err := r.Destroy(ctx, restarted); err != nil {
		t.Fatalf("scoped Destroy: %v", err)
	}
	waitForProcessGone(t, newPID, newStart)
	// This test owns an isolated tmux server. Destroying its final session can
	// make the whole server exit before the probe; that is authoritative here,
	// while the production runtime correctly preserves it as unavailable rather
	// than inferring per-session death.
	if alive, err := r.IsAlive(ctx, restarted); alive || (err != nil && !errors.Is(err, ports.ErrRuntimeUnavailable)) {
		t.Fatalf("tmux after scoped Destroy = (%v, %v), want false with nil or runtime unavailable", alive, err)
	}
	if state := systemdUnitStateForTest(t, newUnit); state != "" && !strings.Contains(state, "inactive") && !strings.Contains(state, "dead") && !strings.Contains(state, "not-found") {
		t.Fatalf("scope state after Destroy = %q", state)
	}
	if !procIdentityAlive(negativePID, negativeStart) {
		t.Fatal("negative-control process did not survive scoped Destroy")
	}

	legacyID := SessionName(id + "_legacy")
	legacy, err := r.Create(ctx, ports.RuntimeConfig{
		SessionID: domain.SessionID(legacyID), WorkspacePath: workspace, Argv: []string{"sh", "-c", "sleep 30"},
	})
	if err != nil {
		t.Fatalf("legacy reviewer/shell Create: %v", err)
	}
	if legacy.RuntimeLaunchID != "" {
		t.Fatalf("legacy reviewer/shell handle gained launch id %q", legacy.RuntimeLaunchID)
	}
	if units := systemdUnitsForPrefix(t, "ao-session-"+legacyID+"-"); units != "" {
		t.Fatalf("legacy reviewer/shell created worker scopes: %q", units)
	}
	if err := r.Destroy(ctx, legacy); err != nil {
		t.Fatalf("legacy reviewer/shell Destroy: %v", err)
	}
}

func setsidPIDCommand(path string) string {
	return fmt.Sprintf("setsid sh -c %s >/dev/null 2>&1 & echo $! > %s", shellQuote("trap '' TERM; while :; do sleep 1; done"), shellQuote(path))
}

func systemdUnitsForPrefix(t *testing.T, prefix string) string {
	t.Helper()
	out, err := exec.Command("systemctl", "--user", "list-units", "--all", "--plain", "--no-legend", prefix+"*.scope").CombinedOutput()
	if err != nil {
		t.Fatalf("list systemd units with prefix %s: %v: %s", prefix, err, out)
	}
	return strings.TrimSpace(string(out))
}

func waitForPIDFile(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil {
			pid, parseErr := strconv.Atoi(strings.TrimSpace(string(data)))
			if parseErr == nil && pid > 1 {
				return pid
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("PID file %s did not become readable", path)
	return 0
}

func mustProcIdentity(t *testing.T, pid int) (sid int, start string) {
	t.Helper()
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		t.Fatalf("read /proc/%d/stat: %v", pid, err)
	}
	fields := strings.Fields(string(data)[strings.LastIndex(string(data), ")")+2:])
	if len(fields) <= 19 {
		t.Fatalf("/proc/%d/stat has too few fields", pid)
	}
	sid, err = strconv.Atoi(fields[3])
	if err != nil {
		t.Fatalf("parse /proc/%d session id: %v", pid, err)
	}
	return sid, fields[19]
}

func mustProcStartTime(t *testing.T, pid int) string {
	_, start := mustProcIdentity(t, pid)
	return start
}

func procIdentityAlive(pid int, start string) bool {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return false
	}
	fields := strings.Fields(string(data)[strings.LastIndex(string(data), ")")+2:])
	return len(fields) > 19 && fields[19] == start
}

func waitForProcessGone(t *testing.T, pid int, start string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !procIdentityAlive(pid, start) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("process %d with start time %s remained alive", pid, start)
}

func procInUnit(pid int, unit string) bool {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/cgroup", pid))
	return err == nil && strings.Contains(string(data), unit)
}

func systemdUnitStateForTest(t *testing.T, unit string) string {
	t.Helper()
	out, err := exec.Command("systemctl", "--user", "show", "--no-pager", "--property=LoadState", "--property=ActiveState", "--property=SubState", unit).CombinedOutput()
	if err != nil && len(out) == 0 {
		return ""
	}
	return string(out)
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
