package agentlaunch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteTempDirStaysInsideSessionTemp(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "proj-1", "ses-1", "tmp")
	path, err := WriteTempDir(dir, Spec{WorkspacePath: "/ws/ses-1", Argv: []string{"agent", "run"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(path, filepath.Clean(dir)+string(os.PathSeparator)) {
		t.Fatalf("spec path = %q, want inside %q", path, dir)
	}
	spec, err := ReadAndRemove(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(spec.Argv) != 2 || spec.WorkspacePath != "/ws/ses-1" {
		t.Fatalf("spec = %+v, want round-tripped argv", spec)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("spec file was not removed: %v", err)
	}
}

func TestWriteTempDirRefusesBlankDir(t *testing.T) {
	if _, err := WriteTempDir("  ", Spec{Argv: []string{"agent"}}); err == nil {
		t.Fatal("expected error for blank directory")
	}
}
