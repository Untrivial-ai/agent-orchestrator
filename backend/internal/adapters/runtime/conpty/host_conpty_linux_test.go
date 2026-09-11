//go:build linux

package conpty

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// TestMain lets the detached-spawn integration test re-exec this test binary
// through the same hidden pty-host entrypoint used by the production AO binary.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "pty-host" {
		os.Exit(RunHost(os.Args[2:], os.Stdout))
	}
	os.Exit(m.Run())
}

func TestLinuxPTYConnStreamsResizesAndReportsExit(t *testing.T) {
	conn, err := newConPTY(t.TempDir(), "/bin/sh", []string{
		"-c", `printf 'ready\n'; IFS= read -r line; printf 'received:%s\n' "$line"; exit 7`,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	linuxConn, ok := conn.(*linuxPTYConn)
	if !ok {
		t.Fatalf("connection type = %T", conn)
	}
	if err := conn.Resize(101, 43); err != nil {
		t.Fatalf("Resize: %v", err)
	}
	size, err := pty.GetsizeFull(linuxConn.pty)
	if err != nil {
		t.Fatalf("GetsizeFull: %v", err)
	}
	if size.Cols != 101 || size.Rows != 43 {
		t.Fatalf("PTY size = %dx%d, want 101x43", size.Cols, size.Rows)
	}
	if err := conn.Resize(70_000, 43); err == nil {
		t.Fatal("Resize accepted a column count that overflows the Linux winsize")
	}

	reader := bufio.NewReader(conn)
	ready, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("waiting for PTY readiness: %v", err)
	}
	if normalized := strings.ReplaceAll(ready, "\r", ""); normalized != "ready\n" {
		t.Fatalf("PTY readiness output = %q", normalized)
	}

	outputC := make(chan []byte, 1)
	go func() {
		var output bytes.Buffer
		_, _ = io.Copy(&output, reader)
		outputC <- output.Bytes()
	}()
	if _, err := conn.Write([]byte("hello\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	select {
	case <-conn.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("PTY child did not exit")
	}
	code, exited := conn.ExitCode()
	if !exited || code != 7 {
		t.Fatalf("ExitCode = (%d, %v), want (7, true)", code, exited)
	}

	select {
	case output := <-outputC:
		text := strings.ReplaceAll(ready+string(output), "\r", "")
		if !strings.Contains(text, "ready\n") || !strings.Contains(text, "received:hello\n") {
			t.Fatalf("PTY output = %q", text)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("PTY output reader did not finish")
	}
}

func TestLinuxDefaultSpawnHostEndToEnd(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	addr, hostPID, err := defaultSpawnHost(ctx, "spawn-e2e", t.TempDir(), []string{
		"env", "AO_PREFIX_VALUE=prefix", "/bin/sh", "-c",
		`printf '\033[c'; sleep 0.05; printf 'ready:%s:%s\n' "$AO_DIRECT_PTY_TEST" "$AO_PREFIX_VALUE"; IFS= read -r line; printf 'received:%s\n' "$line"; sleep 30`,
	}, map[string]string{"AO_DIRECT_PTY_TEST": "works"})
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	// The request context owns startup only. A host that reported READY must
	// stay alive after that request ends so daemon restarts cannot kill agents.
	cancel()
	t.Cleanup(func() {
		_ = clientKill(addr)
		if pidAlive(hostPID) {
			if process, findErr := os.FindProcess(hostPID); findErr == nil {
				_ = process.Kill()
			}
		}
	})

	if err := clientSendInput(addr, "hello\n"); err != nil {
		t.Fatalf("send input: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		output, outputErr := clientGetOutput(context.Background(), addr, 20)
		if outputErr != nil {
			t.Fatalf("get output: %v", outputErr)
		}
		text := strings.ReplaceAll(output, "\r", "")
		if strings.Contains(text, "ready:works:prefix") && strings.Contains(text, "received:hello") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for host output: %q", text)
		}
		time.Sleep(10 * time.Millisecond)
	}

	if err := clientKill(addr); err != nil {
		t.Fatalf("kill host: %v", err)
	}
	deadline = time.Now().Add(3 * time.Second)
	for pidAlive(hostPID) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if pidAlive(hostPID) {
		t.Fatalf("detached pty-host pid %d survived kill", hostPID)
	}
}

func TestLinuxPTYCloseReapsTermIgnoringProcessGroup(t *testing.T) {
	conn, err := newConPTY(t.TempDir(), "/bin/sh", []string{
		"-c", `trap '' TERM; (trap '' TERM; printf 'child-ready\n'; sleep 30) & wait`,
	})
	if err != nil {
		t.Fatal(err)
	}
	linuxConn, ok := conn.(*linuxPTYConn)
	if !ok {
		t.Fatalf("connection type = %T", conn)
	}
	leaderPID := linuxConn.leaderPID
	leaderStartTime := linuxConn.leaderStartTime
	if !linuxSessionAlive(leaderPID, leaderStartTime) {
		t.Fatalf("session %d was not alive after launch", leaderPID)
	}
	ready := make([]byte, 128)
	n, err := conn.Read(ready)
	if err != nil || !strings.Contains(string(ready[:n]), "child-ready") {
		t.Fatalf("waiting for process-group fixture readiness: output=%q err=%v", ready[:n], err)
	}

	if err := conn.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for linuxSessionAlive(leaderPID, leaderStartTime) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if linuxSessionAlive(leaderPID, leaderStartTime) {
		t.Fatalf("session %d survived PTY close", leaderPID)
	}
}

func TestLinuxPTYCloseReapsOrphanedBackgroundJobWhenLeaderExitsEarly(t *testing.T) {
	conn, err := newConPTY(t.TempDir(), "/bin/sh", []string{
		"-c", `printf 'bg-ready\n'; (trap '' HUP; sleep 30) & sleep 0.05; exit 0`,
	})
	if err != nil {
		t.Fatal(err)
	}
	linuxConn, ok := conn.(*linuxPTYConn)
	if !ok {
		t.Fatalf("connection type = %T", conn)
	}
	leaderPID := linuxConn.leaderPID
	leaderStartTime := linuxConn.leaderStartTime

	ready := make([]byte, 128)
	n, err := conn.Read(ready)
	if err != nil || !strings.Contains(string(ready[:n]), "bg-ready") {
		t.Fatalf("waiting for background job readiness: output=%q err=%v", ready[:n], err)
	}

	select {
	case <-conn.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("leader process did not exit early as expected")
	}

	if !linuxSessionAlive(leaderPID, leaderStartTime) {
		t.Fatalf("session %d had no live background processes after leader exit", leaderPID)
	}

	if err := conn.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for linuxSessionAlive(leaderPID, leaderStartTime) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if linuxSessionAlive(leaderPID, leaderStartTime) {
		t.Fatalf("orphaned background process in session %d survived PTY close", leaderPID)
	}
}

func TestLinuxPTYCloseReapsDescendantWithOwnProcessGroup(t *testing.T) {
	conn, err := newConPTY(t.TempDir(), "/bin/bash", []string{
		"-c", `set -m; (trap '' TERM HUP; printf 'pg-child-ready\n'; sleep 30) & wait`,
	})
	if err != nil {
		t.Fatal(err)
	}
	linuxConn, ok := conn.(*linuxPTYConn)
	if !ok {
		t.Fatalf("connection type = %T", conn)
	}
	leaderPID := linuxConn.leaderPID
	leaderStartTime := linuxConn.leaderStartTime

	ready := make([]byte, 128)
	n, err := conn.Read(ready)
	if err != nil || !strings.Contains(string(ready[:n]), "pg-child-ready") {
		t.Fatalf("waiting for job-control descendant readiness: output=%q err=%v", ready[:n], err)
	}

	procs := linuxFindSessionProcesses(leaderPID, leaderStartTime)
	foundSeparatePGID := false
	for _, p := range procs {
		if p.pgrp != leaderPID {
			foundSeparatePGID = true
			break
		}
	}
	if !foundSeparatePGID {
		t.Fatalf("expected at least one process with distinct pgid in session %d, found: %+v", leaderPID, procs)
	}

	if err := conn.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for linuxSessionAlive(leaderPID, leaderStartTime) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if linuxSessionAlive(leaderPID, leaderStartTime) {
		t.Fatalf("descendant with separate process group in session %d survived PTY close", leaderPID)
	}
}

func TestLinuxPTYCloseDoesNotKillUnrelatedProcessAfterPIDReuse(t *testing.T) {
	// Start an independent dummy process representing an unrelated process running on the host.
	dummy := exec.Command("/bin/sh", "-c", "sleep 30")
	if err := dummy.Start(); err != nil {
		t.Fatal(err)
	}
	dummyPID := dummy.Process.Pid
	t.Cleanup(func() {
		if dummy.Process != nil {
			_ = dummy.Process.Kill()
			_ = dummy.Wait()
		}
	})

	dummyInfo, err := readLinuxProcInfo(dummyPID)
	if err != nil {
		t.Fatalf("reading dummy proc info: %v", err)
	}

	// Create a simulated linuxPTYConn whose leaderPID matches dummyPID,
	// but whose recorded leaderStartTime is from an earlier epoch (simulating a recycled PID).
	conn := &linuxPTYConn{
		leaderPID:       dummyPID,
		leaderStartTime: dummyInfo.startTime - 1000, // mismatch: earlier time
		doneC:           make(chan struct{}),
	}
	close(conn.doneC)

	// Close() must detect the start-time mismatch and send NO signals to dummyPID.
	if err := conn.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Verify the dummy process is still running and unharmed.
	if !pidAlive(dummyPID) {
		t.Fatalf("unrelated process %d was killed by Close() despite PID identity mismatch", dummyPID)
	}
}

func TestLinuxPTYCloseDelayedAfterFullSessionExit(t *testing.T) {
	conn, err := newConPTY(t.TempDir(), "/bin/sh", []string{
		"-c", `printf 'done\n'; exit 0`,
	})
	if err != nil {
		t.Fatal(err)
	}
	linuxConn, ok := conn.(*linuxPTYConn)
	if !ok {
		t.Fatalf("connection type = %T", conn)
	}
	leaderPID := linuxConn.leaderPID

	ready := make([]byte, 128)
	n, err := conn.Read(ready)
	if err != nil || !strings.Contains(string(ready[:n]), "done") {
		t.Fatalf("waiting for exit: output=%q err=%v", ready[:n], err)
	}

	select {
	case <-conn.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("process did not exit")
	}

	// Ensure the leader process has exited
	deadline := time.Now().Add(2 * time.Second)
	for pidAlive(leaderPID) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}

	// Delayed close on an already fully-dead session must succeed cleanly without error.
	if err := conn.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestLinuxPTYCloseFailsClosedWhenLeaderStartTimeIsZero(t *testing.T) {
	// Start an independent dummy process representing an innocent process on the host.
	dummy := exec.Command("/bin/sh", "-c", "sleep 30")
	if err := dummy.Start(); err != nil {
		t.Fatal(err)
	}
	dummyPID := dummy.Process.Pid
	t.Cleanup(func() {
		if dummy.Process != nil {
			_ = dummy.Process.Kill()
			_ = dummy.Wait()
		}
	})

	// Create a simulated linuxPTYConn whose leaderPID matches dummyPID,
	// but whose recorded leaderStartTime is 0 (simulating failed identity capture).
	conn := &linuxPTYConn{
		leaderPID:       dummyPID,
		leaderStartTime: 0, // identity unavailable: must fail closed
		doneC:           make(chan struct{}),
	}
	close(conn.doneC)

	// Close() must fail closed: send NO signals to dummyPID or its process group.
	if err := conn.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Verify the dummy process is still running and unharmed.
	if !pidAlive(dummyPID) {
		t.Fatalf("unrelated process %d was killed by Close() when leaderStartTime was zero", dummyPID)
	}
}

func TestLinuxRuntimeDestroyReapsTermIgnoringProcessTreeEndToEnd(t *testing.T) {
	isolateRegistry(t)
	runtime := New(Options{Spawner: defaultSpawnHost})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	handle, err := runtime.Create(ctx, ports.RuntimeConfig{
		SessionID:     "term-reap-e2e",
		WorkspacePath: t.TempDir(),
		Argv: []string{
			"/bin/sh", "-c", `trap '' TERM; (trap '' TERM; printf 'term-trap-ready\n'; sleep 30) & wait`,
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Wait for process readiness output.
	deadline := time.Now().Add(3 * time.Second)
	for {
		out, outErr := runtime.GetOutput(context.Background(), handle, 10)
		if outErr == nil && strings.Contains(out, "term-trap-ready") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for term-trap-ready output: %q (err: %v)", out, outErr)
		}
		time.Sleep(10 * time.Millisecond)
	}

	sess, err := runtime.resolveWithEvidence(context.Background(), handle.ID)
	if err != nil {
		t.Fatalf("resolve session: %v", err)
	}
	if sess == nil {
		t.Fatal("session not found in runtime")
	}
	hostPID := sess.pid

	// Destroy the session through the runtime.
	if err := runtime.Destroy(context.Background(), handle); err != nil {
		t.Fatalf("Destroy: %v", err)
	}

	// Ensure pty-host is gone.
	if pidAlive(hostPID) {
		t.Fatalf("pty-host pid %d survived Destroy", hostPID)
	}
}

func TestLinuxPTYCloseDoesNotKillRecycledPIDDuringGracePeriod(t *testing.T) {
	// Start an independent innocent dummy process representing an unrelated process.
	dummy := exec.Command("/bin/sh", "-c", "sleep 30")
	if err := dummy.Start(); err != nil {
		t.Fatal(err)
	}
	dummyPID := dummy.Process.Pid
	t.Cleanup(func() {
		if dummy.Process != nil {
			_ = dummy.Process.Kill()
			_ = dummy.Wait()
		}
	})

	dummyInfo, err := readLinuxProcInfo(dummyPID)
	if err != nil {
		t.Fatalf("reading dummy proc info: %v", err)
	}

	// Start a real TERM-ignoring process that will keep the grace-period wait alive
	// and force escalation to Phase 2 (SIGKILL).
	termIgnore := exec.Command("/bin/sh", "-c", "trap '' TERM; sleep 30")
	if err := termIgnore.Start(); err != nil {
		t.Fatal(err)
	}
	termIgnorePID := termIgnore.Process.Pid
	t.Cleanup(func() {
		if termIgnore.Process != nil {
			_ = termIgnore.Process.Kill()
			_ = termIgnore.Wait()
		}
	})
	termIgnoreInfo, err := readLinuxProcInfo(termIgnorePID)
	if err != nil {
		t.Fatalf("reading termIgnore proc info: %v", err)
	}

	// Simulate a candidate list containing a recycled PID (stale start time) alongside
	// the active TERM-ignoring process.
	procs := []linuxProcInfo{
		{pid: dummyPID, startTime: dummyInfo.startTime - 1000}, // recycled/mismatched identity
		{pid: termIgnorePID, startTime: termIgnoreInfo.startTime},
	}

	// 1. Pre-signal validation must reject dummyPID on SIGTERM
	signaled := signalProcessIfIdentityMatches(dummyPID, dummyInfo.startTime-1000, syscall.SIGTERM)
	if signaled {
		t.Fatal("signalProcessIfIdentityMatches signaled dummyPID despite start time mismatch")
	}

	// 2. Waiting on identities must recognize dummyPID as dead/unmatched while waiting for termIgnore
	if waitForProcIdentitiesExit(procs, 100*time.Millisecond) {
		t.Fatal("waitForProcIdentitiesExit returned true while termIgnore was still running")
	}

	// 3. Pre-signal validation must reject dummyPID on SIGKILL escalation
	signaledKill := signalProcessIfIdentityMatches(dummyPID, dummyInfo.startTime-1000, syscall.SIGKILL)
	if signaledKill {
		t.Fatal("signalProcessIfIdentityMatches signaled dummyPID on SIGKILL escalation despite start time mismatch")
	}

	// Verify dummy is still running and unharmed
	if !pidAlive(dummyPID) {
		t.Fatalf("unrelated process %d was killed during grace period escalation", dummyPID)
	}
}

func TestLinuxPTYCloseReapsLateChildSpawnedInTERMHandler(t *testing.T) {
	// A parent process traps TERM: on receiving TERM, it spawns a background sleep job
	// in the same session and exits immediately (0.05s).
	conn, err := newConPTY(t.TempDir(), "/bin/bash", []string{
		"-c", `trap 'sleep 30 & exit 0' TERM; printf 'trap-ready\n'; while true; do sleep 1; done`,
	})
	if err != nil {
		t.Fatal(err)
	}
	linuxConn, ok := conn.(*linuxPTYConn)
	if !ok {
		t.Fatalf("connection type = %T", conn)
	}
	leaderPID := linuxConn.leaderPID
	leaderStartTime := linuxConn.leaderStartTime

	ready := make([]byte, 128)
	n, err := conn.Read(ready)
	if err != nil || !strings.Contains(string(ready[:n]), "trap-ready") {
		t.Fatalf("waiting for trap readiness: output=%q err=%v", ready[:n], err)
	}

	// Close() will send SIGTERM. The parent's trap handler will spawn a late child
	// in the session and exit. Close() must discover and reap this late child.
	if err := conn.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for linuxSessionAlive(leaderPID, leaderStartTime) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if linuxSessionAlive(leaderPID, leaderStartTime) {
		t.Fatalf("late child spawned during TERM handler in session %d survived Close()", leaderPID)
	}
}
