//go:build windows

package conpty

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestConPTYChildExitClosesOutputAndRejectsResize(t *testing.T) {
	cmdPath := filepath.Join(os.Getenv("SystemRoot"), "System32", "cmd.exe")
	conn, err := newConPTY(t.TempDir(), cmdPath, []string{
		"/d", "/s", "/c", "echo ao-conpty-exit",
	})
	if err != nil {
		t.Fatalf("newConPTY: %v", err)
	}
	defer conn.Close()

	type readResult struct {
		output []byte
		err    error
	}
	resultC := make(chan readResult, 1)
	go func() {
		output, readErr := io.ReadAll(conn)
		resultC <- readResult{output: output, err: readErr}
	}()

	select {
	case result := <-resultC:
		if result.err != nil {
			t.Fatalf("read ConPTY output: %v", result.err)
		}
		if !bytes.Contains(result.output, []byte("ao-conpty-exit")) {
			t.Fatalf("output %q does not contain completion marker", result.output)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ConPTY output did not close after the child exited")
	}
	if err := conn.Resize(120, 40); err == nil {
		t.Fatal("Resize succeeded after the pseudoconsole closed")
	}

	select {
	case <-conn.Done():
	case <-time.After(time.Second):
		t.Fatal("Done was not closed after the child exited")
	}
	if code, exited := conn.ExitCode(); !exited || code != 0 {
		t.Fatalf("ExitCode() = (%d, %v), want (0, true)", code, exited)
	}
}
