//go:build darwin

package conpty

import (
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
)

// TestMain lets the detached-spawn integration test re-exec this test binary
// through the same hidden pty-host entrypoint used by the production AO binary.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "pty-host" {
		os.Exit(RunHost(os.Args[2:], os.Stdout))
	}
	os.Exit(m.Run())
}

func TestDarwinPTYConnStreamsResizesAndReportsExit(t *testing.T) {
	conn, err := newConPTY(t.TempDir(), "/bin/sh", []string{
		"-c", `printf 'ready\n'; IFS= read -r line; printf 'received:%s\n' "$line"; exit 7`,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	darwinConn, ok := conn.(*darwinPTYConn)
	if !ok {
		t.Fatalf("connection type = %T", conn)
	}
	if err := conn.Resize(101, 43); err != nil {
		t.Fatalf("Resize: %v", err)
	}
	size, err := pty.GetsizeFull(darwinConn.pty)
	if err != nil {
		t.Fatalf("GetsizeFull: %v", err)
	}
	if size.Cols != 101 || size.Rows != 43 {
		t.Fatalf("PTY size = %dx%d, want 101x43", size.Cols, size.Rows)
	}
	if err := conn.Resize(70_000, 43); err == nil {
		t.Fatal("Resize accepted a column count that overflows the Darwin winsize")
	}

	outputC := make(chan []byte, 1)
	go func() {
		var output bytes.Buffer
		_, _ = io.Copy(&output, conn)
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
		text := strings.ReplaceAll(string(output), "\r", "")
		if !strings.Contains(text, "ready\n") || !strings.Contains(text, "received:hello\n") {
			t.Fatalf("PTY output = %q", text)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("PTY output reader did not finish")
	}
}

func TestDarwinDefaultSpawnHostEndToEnd(t *testing.T) {
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

func TestDarwinPTYCloseReapsTermIgnoringProcessGroup(t *testing.T) {
	conn, err := newConPTY(t.TempDir(), "/bin/sh", []string{
		"-c", `trap '' TERM; (trap '' TERM; printf 'child-ready\n'; sleep 30) & wait`,
	})
	if err != nil {
		t.Fatal(err)
	}
	pgid := conn.PID()
	if !darwinProcessGroupAlive(pgid) {
		t.Fatalf("process group %d was not alive after launch", pgid)
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
	for darwinProcessGroupAlive(pgid) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if darwinProcessGroupAlive(pgid) {
		t.Fatalf("process group %d survived PTY close", pgid)
	}
}
