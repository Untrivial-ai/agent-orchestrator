package cursoracp

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestParseCursorVersion(t *testing.T) {
	tests := map[string]cursorVersion{
		"2026.08.11-e8db854":      {2026, 8, 11},
		"cursor-agent 2027.01.02": {2027, 1, 2},
	}
	for input, want := range tests {
		got, ok := parseCursorVersion(input)
		if !ok || got != want {
			t.Errorf("parseCursorVersion(%q) = %v, %v; want %v, true", input, got, ok, want)
		}
	}
}

func TestCursorVersionFloor(t *testing.T) {
	minimum, ok := parseCursorVersion(minimumCursorVersion)
	if !ok {
		t.Fatalf("minimum version %q is invalid", minimumCursorVersion)
	}
	for _, test := range []struct {
		version string
		less    bool
	}{
		{"2026.08.10", true},
		{"2026.08.11-e8db854", false},
		{"2026.09.01", false},
	} {
		got, ok := parseCursorVersion(test.version)
		if !ok || got.less(minimum) != test.less {
			t.Errorf("version %q less minimum = %v, %v; want %v, true",
				test.version, got.less(minimum), ok, test.less)
		}
	}
}

func TestParseCursorVersionRejectsUnknownOutput(t *testing.T) {
	for _, input := range []string{"", "cursor-agent", "2026.8.11", "v1.2.3"} {
		if _, ok := parseCursorVersion(input); ok {
			t.Errorf("parseCursorVersion(%q) succeeded", input)
		}
	}
}

func TestVersionProbeUsesStandardInstallerSymlinkWithoutLaunchingCLI(t *testing.T) {
	root := t.TempDir()
	versionDir := filepath.Join(root, "versions", "2026.08.11-e8db854")
	if err := os.MkdirAll(versionDir, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(versionDir, "cursor-agent")
	if err := os.WriteFile(target, []byte("not an executable"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "cursor-agent")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := versionProbe(context.Background(), link); err != nil {
		t.Fatalf("versionProbe: %v", err)
	}
}
