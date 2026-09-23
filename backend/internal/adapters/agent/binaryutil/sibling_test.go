package binaryutil

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// A second executable shipped beside a CLI must be found next to the binary AO
// already resolved, not by a fresh PATH search that could hit another install.
func TestSiblingBinaryPrefersTheNeighbouringExecutable(t *testing.T) {
	spec := BinarySpec{
		Names:    []string{"tool-acp"},
		WinNames: []string{"tool-acp.cmd", "tool-acp.exe", "tool-acp"},
	}
	dir := t.TempDir()
	name := "tool-acp"
	if runtime.GOOS == "windows" {
		name = "tool-acp.cmd"
	}
	want := filepath.Join(dir, name)
	if err := os.WriteFile(want, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write sibling: %v", err)
	}
	if got := SiblingBinary(filepath.Join(dir, "tool"), spec); got != want {
		t.Fatalf("sibling = %q, want %q", got, want)
	}
}

func TestSiblingBinaryReportsAbsenceRatherThanGuessing(t *testing.T) {
	spec := BinarySpec{Names: []string{"tool-acp"}, WinNames: []string{"tool-acp.exe"}}
	dir := t.TempDir()
	if got := SiblingBinary(filepath.Join(dir, "tool"), spec); got != "" {
		t.Fatalf("sibling = %q, want empty so the caller falls back to ResolveBinary", got)
	}
	// A directory of the right name is not an executable.
	if err := os.Mkdir(filepath.Join(dir, "tool-acp"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if got := SiblingBinary(filepath.Join(dir, "tool"), spec); got != "" {
		t.Fatalf("sibling = %q, want empty for a directory", got)
	}
}
