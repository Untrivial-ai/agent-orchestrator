//go:build windows

package editor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The bug this guards: exec.Command("code.cmd", ...) fails on Windows with
// "%1 is not a valid Win32 application" — CreateProcess cannot execute a
// batch file directly, even though exec.LookPath resolves it without error.
func TestLaunchCommandRunsWindowsBatchShim(t *testing.T) {
	dir := t.TempDir()
	shim := filepath.Join(dir, "code editor.cmd")
	if err := os.WriteFile(shim, []byte("@echo off\r\necho %~1\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	output, err := launchCommand(shim, "argument with spaces").CombinedOutput()
	if err != nil {
		t.Fatalf("run batch shim: %v\n%s", err, output)
	}
	if got := strings.TrimSpace(string(output)); got != "argument with spaces" {
		t.Fatalf("output = %q, want batch argument preserved", got)
	}
}

func TestLaunchCommandRunsANativeExecutableDirectly(t *testing.T) {
	// cmd.exe itself is a native (non-batch) executable resolvable via
	// exec.LookPath, so it exercises the non-.cmd branch of launchCommand
	// without needing a bespoke .exe fixture.
	shell := os.Getenv("COMSPEC")
	if shell == "" {
		shell = "cmd.exe"
	}
	output, err := launchCommand(shell, "/d", "/c", "echo", "hello").CombinedOutput()
	if err != nil {
		t.Fatalf("run native executable: %v\n%s", err, output)
	}
	if got := strings.TrimSpace(string(output)); got != "hello" {
		t.Fatalf("output = %q, want %q", got, "hello")
	}
}
