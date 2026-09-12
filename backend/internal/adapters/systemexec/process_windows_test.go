//go:build windows

package systemexec

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCommandContextWrapsBatchShimWithComSpec(t *testing.T) {
	dir := t.TempDir()
	shim := filepath.Join(dir, "agent tool.cmd")
	if err := os.WriteFile(shim, []byte("@echo off\r\necho %~1\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	comSpec := os.Getenv("ComSpec")
	if comSpec == "" {
		t.Fatal("ComSpec is not set")
	}

	cmd, err := commandContext(context.Background(), shim, "argument with spaces")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(cmd.Path, comSpec) {
		t.Fatalf("Path = %q, want ComSpec", cmd.Path)
	}
	if cmd.SysProcAttr == nil || !strings.Contains(cmd.SysProcAttr.CmdLine, `"agent tool.cmd" "argument with spaces"`) {
		t.Fatalf("CmdLine = %q, want quoted shim and argument", cmd.SysProcAttr.CmdLine)
	}
	configureProcessGroup(cmd)
	if cmd.SysProcAttr.CmdLine == "" {
		t.Fatal("configureProcessGroup discarded the batch command line")
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("run batch shim: %v\n%s", err, output)
	}
	if got := strings.TrimSpace(string(output)); got != "argument with spaces" {
		t.Fatalf("output = %q, want batch argument preserved", got)
	}
}

func TestMergeWindowsPathAddsRegistryEntriesWithoutDuplicates(t *testing.T) {
	got := mergeWindowsPath(
		`C:\Windows\System32;C:\Users\tester\AppData\Roaming\npm`,
		`C:\Program Files\Git\cmd;C:\WINDOWS\SYSTEM32`,
		`C:\Users\tester\AppData\Local\Microsoft\WinGet\Links`,
	)
	want := strings.Join([]string{
		`C:\Windows\System32`,
		`C:\Users\tester\AppData\Roaming\npm`,
		`C:\Program Files\Git\cmd`,
		`C:\Users\tester\AppData\Local\Microsoft\WinGet\Links`,
	}, ";")
	if got != want {
		t.Fatalf("mergeWindowsPath() = %q, want %q", got, want)
	}
}
