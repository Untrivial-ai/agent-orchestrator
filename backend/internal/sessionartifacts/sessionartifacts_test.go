package sessionartifacts

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func TestDir_JoinsDataDirArtifactsAndID(t *testing.T) {
	got := Dir("/data", domain.SessionID("mer-1"))
	want := filepath.Join("/data", "artifacts", "mer-1")
	if got != want {
		t.Fatalf("Dir = %q, want %q", got, want)
	}
}

func TestDir_EmptyDataDirReturnsEmpty(t *testing.T) {
	if got := Dir("", domain.SessionID("mer-1")); got != "" {
		t.Fatalf("Dir with empty dataDir = %q, want empty", got)
	}
	if got := Dir("   ", domain.SessionID("mer-1")); got != "" {
		t.Fatalf("Dir with whitespace-only dataDir = %q, want empty", got)
	}
}

func TestList_ClassifiesByExtensionAndSortsByPath(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{"b.html": "<p>x</p>", "a.md": "# x", "c.bin": "x", "noext": "<!doctype html><html></html>"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	files, err := List(dir)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	got := map[string]domain.SessionArtifactKind{}
	var paths []string
	for _, f := range files {
		got[f.Path] = f.Kind
		paths = append(paths, f.Path)
	}
	if !sort.StringsAreSorted(paths) || len(paths) != 4 {
		t.Fatalf("paths = %v, want 4 sorted", paths)
	}
	if got["b.html"] != domain.SessionArtifactHTML || got["a.md"] != domain.SessionArtifactMarkdown || got["c.bin"] != domain.SessionArtifactGeneric || got["noext"] != domain.SessionArtifactGeneric {
		t.Fatalf("kinds = %v", got)
	}
}

func TestList_BoundsFileCount(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < MaxFiles+25; i++ {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("f%05d.txt", i)), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	files, err := List(dir)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(files) != MaxFiles {
		t.Fatalf("len = %d, want %d", len(files), MaxFiles)
	}
}

func TestList_SkipsUnreadableSubdirInsteadOfFailing(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod permissions are not enforced on Windows")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "ok.md"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	locked := filepath.Join(dir, "locked")
	if err := os.Mkdir(locked, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })
	files, err := List(dir)
	if err != nil {
		t.Fatalf("List returned error for unreadable subdir: %v", err)
	}
	if len(files) != 1 || files[0].Path != "ok.md" {
		t.Fatalf("files = %+v, want only ok.md", files)
	}
}
