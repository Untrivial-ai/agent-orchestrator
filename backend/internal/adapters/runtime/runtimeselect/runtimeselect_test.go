package runtimeselect

import "testing"

func TestLegacyPrivateTmuxSocketPathMatchesShippedIdentity(t *testing.T) {
	got := legacyPrivateTmuxSocketPath("/Users/test/.ao/running.json")
	want := "/Users/test/.ao/tmux-d1b1af1706dfc76b016015a5e85a12dd.sock"
	if got != want {
		t.Fatalf("legacy socket path = %q, want %q", got, want)
	}
}

func TestLegacyPrivateTmuxSocketPathRejectsEmptyRunFile(t *testing.T) {
	if got := legacyPrivateTmuxSocketPath(""); got != "" {
		t.Fatalf("legacy socket path = %q, want empty", got)
	}
}
