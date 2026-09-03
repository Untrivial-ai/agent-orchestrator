//go:build windows

package conpty

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestIsBatchFile(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{`C:\Program Files\nodejs\claude.cmd`, true},
		{`C:\Program Files\nodejs\qwen.CMD`, true},
		{`C:\tools\run.bat`, true},
		{`C:\tools\run.Bat`, true},
		{`C:\Program Files\nodejs\claude.exe`, false},
		{`C:\tools\qwen`, false},
		{`claude`, false},
		{``, false},
	}
	for _, tt := range tests {
		if got := isBatchFile(tt.path); got != tt.want {
			t.Errorf("isBatchFile(%q) = %v, want %v", tt.path, got, tt.want)
		}
	}
}

// TestNewConPTY_BatchFileWithSpaceInPath reproduces the Windows launch failure
// for an agent CLI installed under a path containing spaces (e.g. an npm global
// shim at C:\Program Files\nodejs\claude.cmd). CreateProcess runs .cmd files
// through cmd.exe, and the unquoted program name was split at the first space,
// failing with "'C:\Program' is not recognized". The batch file lives in a
// directory whose name contains a space and must start and receive its
// arguments (including one with a space) intact.
func TestNewConPTY_BatchFileWithSpaceInPath(t *testing.T) {
	// A directory WITH a space in its name mirrors "C:\Program Files\...".
	dir := filepath.Join(t.TempDir(), "Program Files")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	batch := filepath.Join(dir, "echoer.cmd")
	// %~N strips the surrounding quotes cmd.exe adds, so the echoed line shows
	// the arguments exactly as the child received them.
	script := "@echo off\r\necho MARKER:%~1:%~2\r\n"
	if err := os.WriteFile(batch, []byte(script), 0o755); err != nil {
		t.Fatalf("write batch: %v", err)
	}

	conn, err := newConPTY(dir, batch, []string{"hello world", "second"})
	if err != nil {
		t.Fatalf("newConPTY: %v", err)
	}
	defer conn.Close()

	// Accumulate PTY output in the background. ConPTY's Read does not surface
	// EOF when the child exits (the pseudo-console pipe stays open), so exit is
	// observed via Done(), not a Read error.
	var mu sync.Mutex
	var acc []byte
	go func() {
		buf := make([]byte, 4096)
		for {
			n, readErr := conn.Read(buf)
			if n > 0 {
				mu.Lock()
				acc = append(acc, buf[:n]...)
				mu.Unlock()
			}
			if readErr != nil {
				return
			}
		}
	}()

	// Wait for the batch to exit.
	select {
	case <-conn.Done():
	case <-time.After(15 * time.Second):
		mu.Lock()
		got := string(acc)
		mu.Unlock()
		t.Fatalf("timeout waiting for batch to exit; output so far: %q", got)
	}

	// Poll briefly for the marker to land (output may trail Done by a moment).
	deadline := time.After(3 * time.Second)
	for {
		mu.Lock()
		got := string(acc)
		mu.Unlock()
		if strings.Contains(got, "MARKER:hello world:second") {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("batch output missing intact marker; got %q", got)
		case <-time.After(50 * time.Millisecond):
		}
	}
}
