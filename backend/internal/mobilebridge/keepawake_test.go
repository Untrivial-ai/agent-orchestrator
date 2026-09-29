//go:build !windows

package mobilebridge

import (
	"os"
	"path/filepath"
	"testing"
)

// A stand-in for caffeinate that just stays alive, so the test exercises the
// process lifecycle without touching the machine's power state.
func fakeCaffeinate(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "caffeinate")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexec sleep 60\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestKeepAwakeStartStop(t *testing.T) {
	k := &KeepAwake{binary: fakeCaffeinate(t), ownerPID: os.Getpid()}
	if k.Active() {
		t.Fatal("active before Start")
	}
	if err := k.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	first := k.cmd
	if err := k.Start(); err != nil || k.cmd != first {
		t.Fatal("Start should be idempotent")
	}
	if !k.Active() {
		t.Fatal("not active after Start")
	}
	k.Stop()
	if k.Active() {
		t.Fatal("still active after Stop")
	}
	k.Stop() // idempotent
}

func TestKeepAwakeStartFailsForMissingBinary(t *testing.T) {
	k := &KeepAwake{binary: filepath.Join(t.TempDir(), "missing"), ownerPID: os.Getpid()}
	if err := k.Start(); err == nil {
		t.Fatal("expected Start to fail")
	}
	if k.Active() {
		t.Fatal("active after a failed Start")
	}
}
