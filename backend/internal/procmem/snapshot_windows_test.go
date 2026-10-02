//go:build windows

package procmem

import (
	"context"
	"os"
	"testing"
)

// TestSnapshotFindsTheRunningTestProcess is the one thing that can only be
// checked on real Windows: that CreateToolhelp32Snapshot plus the per-process
// calls actually produce a usable table, not just that the package compiles.
// It asserts against this very test binary rather than a fixture, since
// there is no text format here to fake the way ps output is faked elsewhere.
func TestSnapshotFindsTheRunningTestProcess(t *testing.T) {
	table, err := Snapshot(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	pid := os.Getpid()
	self, ok := table.byPID[pid]
	if !ok {
		t.Fatalf("this process (pid %d) is not in its own snapshot", pid)
	}
	if self.RSSBytes == 0 {
		t.Fatalf("self = %+v, want non-zero working set", self)
	}
	if self.PPID <= 0 {
		t.Fatalf("self.PPID = %d, want a real parent pid", self.PPID)
	}
	if _, parentOK := table.byPID[self.PPID]; !parentOK {
		t.Fatalf("parent pid %d is not itself in the snapshot", self.PPID)
	}
	// The tree walk that every session reading depends on must also work
	// against a live table, not just the Parse fixtures the Unix path tests.
	tree := table.Tree(pid)
	if tree.RSSBytes == 0 || len(tree.Processes) == 0 {
		t.Fatalf("tree for self = %+v", tree)
	}
}

func TestSnapshotIgnoresACancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Snapshot(ctx, nil); err == nil {
		t.Fatal("a cancelled context must be reported, not silently ignored")
	}
}
