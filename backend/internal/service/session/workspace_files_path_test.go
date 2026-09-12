package session

import (
	"runtime"
	"testing"
)

// Backslash is a path separator only on Windows. On Linux and macOS it is an
// ordinary filename character, so folding it into "/" made a file named
// `weird\name.txt` unopenable and silently resolved it to `weird/name.txt` —
// a different path that may exist and hold something else entirely.
func TestCleanWorkspaceRelativePathKeepsBackslashOnPOSIX(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("backslash is a separator on Windows; this pins the POSIX behaviour")
	}

	got, err := cleanWorkspaceRelativePath(`dir/weird\name.txt`)
	if err != nil {
		t.Fatalf("cleanWorkspaceRelativePath: %v", err)
	}
	if want := `dir/weird\name.txt`; got != want {
		t.Fatalf("got %q, want %q; a backslash must stay part of the filename on this platform", got, want)
	}
}

// The traversal guards must keep working on a name that contains a backslash,
// so the fix above cannot be read as "stop validating these".
func TestCleanWorkspaceRelativePathStillRejectsEscapes(t *testing.T) {
	for _, raw := range []string{"", "..", "../outside.txt", "/abs/path.txt"} {
		if _, err := cleanWorkspaceRelativePath(raw); err == nil {
			t.Fatalf("cleanWorkspaceRelativePath(%q) = nil error, want rejection", raw)
		}
	}
}
