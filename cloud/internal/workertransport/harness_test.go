package workertransport

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/aoagents/agent-orchestrator/cloud/internal/worker"
)

func TestInspectHarnessesRejectsUnsupportedHarness(t *testing.T) {
	_, err := inspectHarnesses(context.Background(), worker.HarnessInspectRequest{Harnesses: []string{"opencode"}})
	if err == nil {
		t.Fatal("inspectHarnesses accepted an unsupported cloud reviewer")
	}
}

func TestInspectHarnessesFindsUserInstalledBinary(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	bin := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "codex"), []byte("#!/bin/sh\necho codex-test\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	response, err := inspectHarnesses(context.Background(), worker.HarnessInspectRequest{Harnesses: []string{"codex"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Harnesses) != 1 || response.Harnesses[0].Status != "ready" {
		t.Fatalf("harness response = %#v", response)
	}
}

func TestInstallHarnessRejectsUnsupportedHarness(t *testing.T) {
	_, err := installHarness(context.Background(), worker.HarnessInstallRequest{Harness: "opencode"})
	if err == nil {
		t.Fatal("installHarness accepted an unsupported cloud reviewer")
	}
}
