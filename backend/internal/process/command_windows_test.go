//go:build windows

package process

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func TestCommandContextHidesConsoleWindow(t *testing.T) {
	cmd := CommandContext(context.Background(), "git", "--version")
	if cmd.SysProcAttr == nil {
		t.Fatal("SysProcAttr = nil, want hidden Windows process attributes")
	}
	if got := cmd.SysProcAttr.CreationFlags; got&windows.CREATE_NO_WINDOW == 0 {
		t.Fatalf("CreationFlags = %#x, want CREATE_NO_WINDOW", got)
	}
	if !cmd.SysProcAttr.HideWindow {
		t.Fatal("HideWindow = false, want true")
	}
}

func TestCommandContextForPathRunsQuotedBatchShim(t *testing.T) {
	dir := t.TempDir()
	for _, extension := range []string{".cmd", ".bat"} {
		t.Run(extension, func(t *testing.T) {
			shim := filepath.Join(dir, "goose helper"+extension)
			if err := os.WriteFile(shim, []byte("@echo off\r\necho batch-ok\r\n"), 0o600); err != nil {
				t.Fatal(err)
			}

			cmd := CommandContextForPath(context.Background(), shim, `safe & echo injected`)
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("run batch shim: %v\n%s", err, output)
			}
			if got := strings.TrimSpace(string(output)); got != "batch-ok" {
				t.Fatalf("output = %q, want quoted argument to remain inert", got)
			}
		})
	}
}
