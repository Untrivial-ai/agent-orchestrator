//go:build linux

package tmux

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestDestroyNaturallyExitedRetainedPane(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux unavailable")
	}
	r := newIntegrationRuntime(t)
	id := "dead-pane-regression"
	ctx := context.Background()
	// A real shell exits normally after the release file appears. Keeping the
	// dead pane is a supported window option, including in user tmux config.
	dir := t.TempDir()
	command := fmt.Sprintf("while [ ! -e %s ]; do sleep 0.01; done", filepath.Join(dir, "release"))
	if out, err := r.run(ctx, "new-session", "-d", "-s", id, "/bin/sh", "-c", command); err != nil {
		t.Fatalf("create fixture: %s: %v", out, err)
	}
	r.rememberSessionSocket(id, r.socketName)
	if out, err := r.run(ctx, "set-window-option", "-t", id, "remain-on-exit", "on"); err != nil {
		t.Fatalf("set remain-on-exit: %s: %v", out, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "release"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		out, err := r.run(ctx, "display-message", "-p", "-t", id, "#{pane_dead} #{pane_pid} #{pane_dead_status}")
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(string(out), "1 ") {
			t.Logf("natural exit retained pane: %s", strings.TrimSpace(string(out)))
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("fixture pane did not exit")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := r.Destroy(ctx, ports.RuntimeHandle{ID: id}); err != nil {
		alive, probeErr := r.IsAlive(ctx, ports.RuntimeHandle{ID: id})
		t.Fatalf("Destroy of naturally exited pane: %v; tmux session still exists=%v, probe=%v", err, alive, probeErr)
	}
}

func TestDestroyShutdownForkRetainsUnconfirmedOwnership(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux unavailable")
	}
	r := newIntegrationRuntime(t)
	r.reapGrace = 300 * time.Millisecond
	dir := t.TempDir()
	ctx := context.Background()
	handle, err := r.Create(ctx, ports.RuntimeConfig{
		SessionID: "shutdown-fork-regression", WorkspacePath: dir,
		Argv: []string{os.Args[0], "-test.run=^TestShutdownForkProcessHelper$"},
		Env:  map[string]string{"AO_SHUTDOWN_TEST_MODE": "parent", "AO_SHUTDOWN_TEST_DIR": dir, "GORACE": "atexit_sleep_ms=0"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Destroy(ctx, handle) })
	parent := waitForShutdownPID(t, filepath.Join(dir, "parent"))
	// Clean up only the fixture's exact live identities, even when the runtime
	// loses ownership and reports success.
	t.Cleanup(func() {
		table, err := readOwnedProcesses(ctx)
		if err != nil {
			return
		}
		for _, name := range []string{"parent", "child"} {
			data, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				continue
			}
			pid, _ := strconv.Atoi(strings.TrimSpace(string(data)))
			for _, p := range table {
				if p.PID == pid {
					_ = signalOwnedProcess(ctx, p, true)
				}
			}
		}
	})
	unrelated := exec.Command("sleep", "30")
	if err := unrelated.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = unrelated.Process.Kill(); _ = unrelated.Wait() })
	destroyErr := r.Destroy(ctx, handle)
	child := waitForShutdownPID(t, filepath.Join(dir, "child"))
	table, err := readOwnedProcesses(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var childLive bool
	for _, p := range table {
		if p.PID == parent || p.PID == child {
			t.Logf("post-Destroy fixture process: %+v", p)
		}
		if p.PID == child && !p.Stopped {
			childLive = true
		}
	}
	if err := unrelated.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("unrelated process stopped: %v", err)
	}
	_, recordErr := os.Stat(r.teardownPath(handle.ID))
	t.Logf("Destroy error=%v, child=%d live=%v, cleanup record stat=%v; unrelated process survived", destroyErr, child, childLive, recordErr)

	if !childLive {
		if destroyErr != nil {
			t.Fatalf("verified exit returned error: %v", destroyErr)
		}
		return // The child was discovered before its parent's exit.
	}
	if destroyErr == nil || !strings.Contains(destroyErr.Error(), "cleanup unconfirmed") || recordErr != nil {
		t.Fatalf("live unanchored child lost cleanup evidence: %v, %v", destroyErr, recordErr)
	}
	restarted := New(Options{SocketName: r.socketName, Binary: r.binary, LegacyBinary: r.legacyBinary, Shell: "/bin/sh", RunFilePath: filepath.Join(filepath.Dir(r.cleanupDir), "running.json")})
	if err := restarted.Destroy(ctx, handle); err == nil || !strings.Contains(err.Error(), "cleanup unconfirmed") {
		t.Fatalf("restart forgot unconfirmed ownership: %v", err)
	}
	if err := restarted.requireCompletedTeardown(handle.ID); err == nil {
		t.Fatal("unconfirmed teardown permits runtime replacement")
	}
	// The fixture knows which child it created. The runtime cannot infer that
	// authority from a session number alone after a daemon restart.
	for _, p := range table {
		if p.PID == child {
			if err := signalOwnedProcess(ctx, p, true); err != nil {
				t.Fatal(err)
			}
		}
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		table, err := readOwnedProcesses(ctx)
		if err != nil {
			t.Fatal(err)
		}
		live := false
		for _, p := range table {
			if p.PID == child && !p.Stopped {
				live = true
			}
		}
		if !live {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("fixture child did not exit")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := restarted.Destroy(ctx, handle); err != nil {
		t.Fatalf("retry after child exit: %v", err)
	}
	if err := restarted.requireCompletedTeardown(handle.ID); err != nil {
		t.Fatalf("verified exit retained cleanup fence: %v", err)
	}

}

func waitForShutdownPID(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if data, err := os.ReadFile(path); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil && pid > 1 {
				return pid
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("fixture PID file missing: %s", path)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestShutdownForkProcessHelper(t *testing.T) {
	mode := os.Getenv("AO_SHUTDOWN_TEST_MODE")
	if mode == "" {
		return
	}
	dir := os.Getenv("AO_SHUTDOWN_TEST_DIR")
	signal.Ignore(syscall.SIGHUP)
	term := make(chan os.Signal, 1)
	if mode == "parent" {
		signal.Notify(term, syscall.SIGTERM)
	} else {
		signal.Ignore(syscall.SIGTERM)
	}
	if err := os.WriteFile(filepath.Join(dir, mode), []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
		t.Fatal(err)
	}
	if mode == "parent" {
		<-term
		child := exec.Command(os.Args[0], "-test.run=^TestShutdownForkProcessHelper$")
		child.Env = append(os.Environ(), "AO_SHUTDOWN_TEST_MODE=child")
		if err := child.Start(); err != nil {
			t.Fatal(err)
		}
		_ = waitForShutdownPID(t, filepath.Join(dir, "child"))
		os.Exit(0)
	}
	for {
		time.Sleep(time.Second)
	}
}
