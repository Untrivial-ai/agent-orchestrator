package tmux

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
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
	runFile := filepath.Join(tmuxTmpDir, "running.json")
	for _, socketName := range []string{"default", "spoof", "ao"} {
		t.Cleanup(func() {
			_ = exec.Command(systemTmux, "-L", socketName, "kill-server").Run()
		})
	}
	start := func(socketName, sessionID, command string) {
		t.Helper()
		if out, startErr := exec.Command(
			systemTmux,
			"-L", socketName,
			"new-session", "-d", "-s", sessionID,
			command,
		).CombinedOutput(); startErr != nil {
			t.Fatalf("start tmux -L %s: %v: %s", socketName, startErr, out)
		}
	}
	start("default", legacyID, ownedPaneCommand(runFile, legacyID, "launch-1"))
	start("spoof", spoofID, "sleep 30")
	start("ao", privateID, "sleep 30")

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
		RunFilePath:  runFile,
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
	runFile := filepath.Join(tmuxTmpDir, "running.json")
	for _, socketName := range []string{"default", "ao"} {
		t.Cleanup(func() {
			_ = exec.Command(systemTmux, "-L", socketName, "kill-server").Run()
		})
	}
	if out, startErr := exec.Command(
		systemTmux,
		"-L", "default",
		"new-session", "-d", "-s", legacyID,
		ownedPaneCommand(runFile, legacyID, "launch-1"),
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
		RunFilePath:  runFile,
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

func TestRuntimeIntegrationAdoptsHistoricalPrivateSocket(t *testing.T) {
	systemTmux, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux unavailable")
	}

	// Reproduce the upgrade boundary that stranded live sessions on the
	// deterministic -S socket while the next release moved back to -L ao.
	tmuxTmpDir, err := os.MkdirTemp("/tmp", "ao-tmux-private-migration-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tmuxTmpDir) })
	t.Setenv("TMUX_TMPDIR", tmuxTmpDir)
	historicalSocket := filepath.Join(tmuxTmpDir, "tmux-historical.sock")
	legacyID := strings.ReplaceAll(t.Name(), "/", "_") + "_legacy"
	runFile := filepath.Join(tmuxTmpDir, "running.json")
	t.Cleanup(func() {
		_ = exec.Command(systemTmux, "-S", historicalSocket, "kill-server").Run()
		_ = exec.Command(systemTmux, "-L", "ao", "kill-server").Run()
	})
	if out, startErr := exec.Command(
		systemTmux,
		"-S", historicalSocket,
		"-f", os.DevNull,
		"new-session", "-d", "-s", legacyID,
		ownedPaneCommand(runFile, legacyID, "launch-1"),
	).CombinedOutput(); startErr != nil {
		t.Fatalf("start historical private tmux session: %v: %s", startErr, out)
	}

	r := New(Options{
		Binary:           systemTmux,
		LegacyBinary:     systemTmux,
		SocketName:       "ao",
		LegacySocketPath: historicalSocket,
		RunFilePath:      runFile,
		Timeout:          5 * time.Second,
	})
	r.enterDelay = 0
	handle := ports.RuntimeHandle{ID: legacyID}
	alive, err := r.IsAlive(context.Background(), handle)
	if err != nil || !alive {
		t.Fatalf("historical private-socket session = (%v, %v), want (true, nil)", alive, err)
	}
	if err := r.SendMessage(context.Background(), handle, "echo historical-private-send-ok"); err != nil {
		t.Fatalf("SendMessage to historical private session: %v", err)
	}
	out := waitForOutput(t, r, handle, "historical-private-send-ok", 5*time.Second)
	if !strings.Contains(out, "historical-private-send-ok") {
		t.Fatalf("historical private output = %q, want historical-private-send-ok", out)
	}
	if out, probeErr := exec.Command(systemTmux, "-L", "ao", "list-sessions").CombinedOutput(); probeErr == nil {
		t.Fatalf("historical discovery unexpectedly created named AO server: %s", out)
	}
}

func TestRuntimeIntegrationOwnerFencedHandleRelocatesWithoutDestroyingForeignReplacement(t *testing.T) {
	systemTmux, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux unavailable")
	}

	tmuxTmpDir, err := os.MkdirTemp("/tmp", "ao-tmux-owner-fence-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tmuxTmpDir) })
	t.Setenv("TMUX_TMPDIR", tmuxTmpDir)
	const (
		currentSocketName = "ao-owner-fence"
		sessionID         = "owner-fence-session"
		launchID          = "owner-fence-launch"
	)
	historicalSocket := filepath.Join(tmuxTmpDir, "tmux-historical.sock")
	runFile := filepath.Join(tmuxTmpDir, "running.json")
	t.Cleanup(func() {
		_ = exec.Command(systemTmux, "-L", currentSocketName, "kill-server").Run()
		_ = exec.Command(systemTmux, "-S", historicalSocket, "-f", os.DevNull, "kill-server").Run()
	})

	if out, startErr := exec.Command(
		systemTmux, "-S", historicalSocket, "-f", os.DevNull,
		"new-session", "-d", "-s", sessionID,
		ownedPaneCommand(runFile, sessionID, launchID),
	).CombinedOutput(); startErr != nil {
		t.Fatalf("start exact historical owner: %v: %s", startErr, out)
	}
	if out, startErr := exec.Command(
		systemTmux, "-L", currentSocketName,
		"new-session", "-d", "-s", sessionID, "sleep 300",
	).CombinedOutput(); startErr != nil {
		t.Fatalf("start foreign same-name replacement: %v: %s", startErr, out)
	}

	r := New(Options{
		Binary:           systemTmux,
		LegacyBinary:     systemTmux,
		SocketName:       currentSocketName,
		LegacySocketPath: historicalSocket,
		RunFilePath:      runFile,
		Timeout:          5 * time.Second,
	})
	r.enterDelay = 0
	handle, err := qualifiedRuntimeHandleForOwner(
		sessionID,
		socketTarget{kind: socketTargetNamed, value: currentSocketName},
		ports.SupervisedProcessRef{SessionID: sessionID, LaunchID: launchID},
	)
	if err != nil {
		t.Fatal(err)
	}
	handle, found, err := r.ResolveExactRuntimeHandle(
		context.Background(),
		handle,
		ports.SupervisedProcessRef{SessionID: sessionID, LaunchID: launchID},
	)
	if err != nil || !found {
		t.Fatalf("canonicalize relocated exact owner = (%q, %v, %v)", handle.ID, found, err)
	}

	if err := r.SendMessage(context.Background(), handle, "echo exact-owner-route-ok"); err != nil {
		t.Fatalf("send through relocated owner-fenced handle: %v", err)
	}
	if out := waitForOutput(t, r, handle, "exact-owner-route-ok", 5*time.Second); !strings.Contains(out, "exact-owner-route-ok") {
		t.Fatalf("relocated owner output = %q", out)
	}
	if err := r.Destroy(context.Background(), handle); err != nil {
		t.Fatalf("destroy relocated exact owner: %v", err)
	}
	if out, probeErr := exec.Command(
		systemTmux, "-L", currentSocketName, "has-session", "-t", "="+sessionID,
	).CombinedOutput(); probeErr != nil {
		t.Fatalf("foreign same-name replacement was destroyed: %v: %s", probeErr, out)
	}
}

func TestRuntimeIntegrationCanonicalHandleRejectsReplacementServerReusingObjectIDs(t *testing.T) {
	systemTmux, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux unavailable")
	}
	tmuxTmpDir, err := os.MkdirTemp("/tmp", "ao-tmux-server-fence-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tmuxTmpDir) })
	t.Setenv("TMUX_TMPDIR", tmuxTmpDir)
	const (
		socketName = "ao-server-fence"
		sessionID  = "server-fence-session"
	)
	t.Cleanup(func() { _ = exec.Command(systemTmux, "-L", socketName, "kill-server").Run() })

	r := New(Options{Binary: systemTmux, SocketName: socketName, Timeout: 5 * time.Second, Shell: "/bin/sh"})
	r.enterDelay = 0
	handle, err := r.Create(context.Background(), ports.RuntimeConfig{
		SessionID:     sessionID,
		WorkspacePath: "/tmp",
		Argv:          []string{"sleep", "300"},
	})
	if err != nil {
		t.Fatal(err)
	}
	original, err := decodeRuntimeHandle(handle)
	if err != nil {
		t.Fatal(err)
	}
	if out, killErr := exec.Command(systemTmux, "-L", socketName, "kill-server").CombinedOutput(); killErr != nil {
		t.Fatalf("kill original server: %v: %s", killErr, out)
	}
	if out, startErr := exec.Command(
		systemTmux, "-L", socketName,
		"new-session", "-d", "-s", sessionID,
		"sh", "-c", "printf foreign-ready; sleep 300",
	).CombinedOutput(); startErr != nil {
		t.Fatalf("start replacement server: %v: %s", startErr, out)
	}
	identityOut, err := exec.Command(
		systemTmux, "-L", socketName,
		"list-panes", "-t", "="+sessionID, "-F", paneIdentityFormat,
	).CombinedOutput()
	if err != nil {
		t.Fatal(err)
	}
	replacementPID, replacementSessionID, replacementPaneID, _, err := parsePaneIdentityOutput(string(identityOut))
	if err != nil {
		t.Fatal(err)
	}
	if replacementSessionID != original.tmuxSessionID || replacementPaneID != original.tmuxPaneID {
		t.Fatalf("replacement object ids = %s/%s, want reused %s/%s", replacementSessionID, replacementPaneID, original.tmuxSessionID, original.tmuxPaneID)
	}
	if replacementPID == original.tmuxServerPID {
		t.Fatalf("replacement unexpectedly reused live server pid %d", replacementPID)
	}

	if err := r.SendInput(context.Background(), handle, "FOREIGN-MUST-NOT-RECEIVE"); !errors.Is(err, ports.ErrRuntimeProbeInconclusive) {
		t.Fatalf("SendInput error = %v, want ErrRuntimeProbeInconclusive", err)
	}
	if err := r.Destroy(context.Background(), handle); !errors.Is(err, ports.ErrRuntimeProbeInconclusive) {
		t.Fatalf("Destroy error = %v, want ErrRuntimeProbeInconclusive", err)
	}
	if out, probeErr := exec.Command(systemTmux, "-L", socketName, "has-session", "-t", "="+sessionID).CombinedOutput(); probeErr != nil {
		t.Fatalf("replacement was destroyed: %v: %s", probeErr, out)
	}
	out, err := exec.Command(systemTmux, "-L", socketName, "capture-pane", "-p", "-t", replacementPaneID).CombinedOutput()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "FOREIGN-MUST-NOT-RECEIVE") {
		t.Fatalf("replacement received fenced input: %q", out)
	}
}

func TestRuntimeIntegrationSanitizedLegacyHandleSurvivesCanonicalizationAndTwoRuntimeReplacements(t *testing.T) {
	systemTmux, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux unavailable")
	}

	tmuxTmpDir, err := os.MkdirTemp("/tmp", "ao-tmux-sanitized-continuity-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tmuxTmpDir) })
	t.Setenv("TMUX_TMPDIR", tmuxTmpDir)

	historicalSocket := filepath.Join(tmuxTmpDir, "tmux-historical.sock")
	runFile := filepath.Join(tmuxTmpDir, "running.json")
	rawSessionID := domain.SessionID("project/feature with spaces and 🚀/" + strings.Repeat("very-long-", 6))
	tmuxID := SessionName(string(rawSessionID))
	const launchID = "launch-before-daemon-replacement"
	if tmuxID == string(rawSessionID) {
		t.Fatalf("tmux session name = raw session id %q, want sanitization", tmuxID)
	}
	t.Cleanup(func() {
		_ = exec.Command(systemTmux, "-S", historicalSocket, "kill-server").Run()
		for _, socketName := range []string{"ao", "replacement-one", "replacement-two"} {
			_ = exec.Command(systemTmux, "-L", socketName, "kill-server").Run()
		}
	})
	if out, startErr := exec.Command(
		systemTmux,
		"-S", historicalSocket,
		"-f", os.DevNull,
		"new-session", "-d", "-s", tmuxID,
		ownedPaneCommand(runFile, string(rawSessionID), launchID),
	).CombinedOutput(); startErr != nil {
		t.Fatalf("start sanitized historical tmux session: %v: %s", startErr, out)
	}

	resolver := New(Options{
		Binary:           systemTmux,
		LegacyBinary:     systemTmux,
		SocketName:       "ao",
		LegacySocketPath: historicalSocket,
		RunFilePath:      runFile,
		Timeout:          5 * time.Second,
	})
	resolved, found, err := resolver.ResolveRuntimeHandle(
		context.Background(),
		ports.RuntimeHandle{ID: tmuxID},
		ports.SupervisedProcessRef{SessionID: rawSessionID, LaunchID: launchID},
	)
	if err != nil || !found {
		t.Fatalf("ResolveRuntimeHandle = (%q, %v, %v), want canonical historical handle", resolved.ID, found, err)
	}

	// The first replacement has no discovery cache. Identity must still compare
	// the pane's raw AO_SESSION_ID while routing the qualified handle by tmuxID.
	firstReplacement := New(Options{
		Binary:       systemTmux,
		LegacyBinary: systemTmux,
		SocketName:   "replacement-one",
		RunFilePath:  runFile,
		Timeout:      5 * time.Second,
	})
	identity, err := firstReplacement.InspectRuntimeIdentity(context.Background(), resolved, rawSessionID)
	if err != nil || !identity.OwnershipProven || identity.LaunchID != launchID {
		t.Fatalf("InspectRuntimeIdentity = (%+v, %v), want proven raw identity", identity, err)
	}

	// A second replacement restarts through the already-canonical handle. The
	// pane target must remain the sanitized name on the historical socket.
	secondReplacement := New(Options{
		Binary:       systemTmux,
		LegacyBinary: systemTmux,
		SocketName:   "replacement-two",
		RunFilePath:  runFile,
		Timeout:      5 * time.Second,
	})
	secondReplacement.enterDelay = 0
	const restartedLaunchID = "launch-2"
	agentProcessDir := t.TempDir()
	agentProcessPath := filepath.Join(agentProcessDir, "agent-process")
	if err := os.WriteFile(agentProcessPath, []byte("#!/bin/sh\nwhile [ \"$1\" != \"--\" ]; do shift; done\nshift\nexec \"$@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	restarted, err := secondReplacement.Restart(context.Background(), resolved, ports.RuntimeConfig{
		SessionID:     rawSessionID,
		WorkspacePath: t.TempDir(),
		Argv: []string{
			"agent-process", "supervise", "--session", string(rawSessionID),
			"--launch", restartedLaunchID, "--", "sh", "-c", "echo sanitized-restart-ok",
		},
		Env: map[string]string{
			"AO_RUN_FILE":           runFile,
			"AO_SESSION_ID":         string(rawSessionID),
			"AO_SUPERVISED_PROCESS": "1",
			runtimeLaunchEnv:        restartedLaunchID,
			"PATH":                  agentProcessDir + string(os.PathListSeparator) + os.Getenv("PATH"),
		},
	})
	if err != nil {
		t.Fatalf("Restart after second Runtime replacement: %v", err)
	}
	if restarted == resolved {
		t.Fatalf("Restart handle retained stale launch owner: %+v", restarted)
	}
	out := waitForOutput(t, secondReplacement, restarted, "sanitized-restart-ok", 5*time.Second)
	if !strings.Contains(out, "sanitized-restart-ok") {
		t.Fatalf("restart output = %q, want sanitized-restart-ok", out)
	}
	if alive, probeErr := secondReplacement.IsAlive(context.Background(), restarted); probeErr != nil || !alive {
		t.Fatalf("IsAlive after second Runtime replacement = (%v, %v), want (true, nil)", alive, probeErr)
	}

	if probeOut, probeErr := exec.Command(
		systemTmux,
		"-S", historicalSocket,
		"has-session", "-t", "="+tmuxID,
	).CombinedOutput(); probeErr != nil {
		t.Fatalf("probe restarted sanitized tmux target: %v: %s", probeErr, probeOut)
	}
	nameOut, err := exec.Command(
		systemTmux,
		"-S", historicalSocket,
		"list-sessions", "-F", "#{session_name}",
	).CombinedOutput()
	if err != nil {
		t.Fatalf("read restarted tmux session name: %v: %s", err, nameOut)
	}
	if got := strings.TrimSpace(string(nameOut)); got != tmuxID {
		t.Fatalf("restarted tmux session = %q, want sanitized target %q", got, tmuxID)
	}
	for _, socketName := range []string{"replacement-one", "replacement-two"} {
		if stray, listErr := exec.Command(systemTmux, "-L", socketName, "list-sessions").CombinedOutput(); listErr == nil {
			t.Errorf("qualified-handle operation created or used %s namespace: %s", socketName, stray)
		}
	}
}

func TestRuntimeIntegrationBareHandleRejectsSameNameAcrossNamespaces(t *testing.T) {
	systemTmux, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux unavailable")
	}

	// Put all three servers under a test-owned tmux root. The same session name
	// intentionally exists on each one; selecting the first match would attach
	// to an arbitrary controller and make later destructive operations unsafe.
	tmuxTmpDir, err := os.MkdirTemp("/tmp", "ao-tmux-ambiguity-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tmuxTmpDir) })
	t.Setenv("TMUX_TMPDIR", tmuxTmpDir)
	historicalSocket := filepath.Join(tmuxTmpDir, "tmux-historical.sock")
	const sessionID = "same-session"

	t.Cleanup(func() {
		_ = exec.Command(systemTmux, "-L", "ao", "kill-server").Run()
		_ = exec.Command(systemTmux, "-L", "default", "kill-server").Run()
		_ = exec.Command(systemTmux, "-S", historicalSocket, "kill-server").Run()
	})
	start := func(args ...string) {
		t.Helper()
		command := append(append([]string(nil), args...),
			"new-session", "-d", "-s", sessionID, "sleep 30")
		if out, startErr := exec.Command(systemTmux, command...).CombinedOutput(); startErr != nil {
			t.Fatalf("start tmux %v: %v: %s", args, startErr, out)
		}
	}
	start("-L", "ao")
	start("-L", "default")
	start("-S", historicalSocket, "-f", os.DevNull)

	r := New(Options{
		Binary:           systemTmux,
		LegacyBinary:     systemTmux,
		SocketName:       "ao",
		LegacySocketPath: historicalSocket,
		Timeout:          5 * time.Second,
	})
	alive, probeErr := r.IsAlive(context.Background(), ports.RuntimeHandle{ID: sessionID})
	if alive {
		t.Error("ambiguous bare handle reported alive")
	}
	if !errors.Is(probeErr, ports.ErrRuntimeProbeInconclusive) {
		t.Errorf("IsAlive error = %v, want ErrRuntimeProbeInconclusive", probeErr)
	}
	var ambiguity runtimeAmbiguity
	if !errors.As(probeErr, &ambiguity) || !ambiguity.RuntimeAmbiguity() {
		t.Errorf("IsAlive error = %v, want typed runtime ambiguity", probeErr)
	}

	// Ambiguity detection is read-only: all three candidates must still exist.
	for label, args := range map[string][]string{
		"named":      {"-L", "ao"},
		"default":    {"-L", "default"},
		"historical": {"-S", historicalSocket, "-f", os.DevNull},
	} {
		probe := append(append([]string(nil), args...), "has-session", "-t", "="+sessionID)
		if out, liveErr := exec.Command(systemTmux, probe...).CombinedOutput(); liveErr != nil {
			t.Errorf("%s candidate was mutated during ambiguous discovery: %v: %s", label, liveErr, out)
		}
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
