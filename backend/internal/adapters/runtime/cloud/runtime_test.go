package cloud

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestArchiveWorkspaceExcludesLocalGitAndDependencies(t *testing.T) {
	root := t.TempDir()
	for name, value := range map[string]string{"tracked.txt": "real code", ".git": "gitdir: elsewhere", "node_modules/pkg.js": "generated", ".ao/private": "secret"} {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	value, err := archiveWorkspace(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	gz, err := gzip.NewReader(bytes.NewReader(value))
	if err != nil {
		t.Fatal(err)
	}
	reader := tar.NewReader(gz)
	names := map[string]bool{}
	for {
		header, nextErr := reader.Next()
		if nextErr == io.EOF {
			break
		}
		if nextErr != nil {
			t.Fatal(nextErr)
		}
		names[header.Name] = true
	}
	if !names["tracked.txt"] {
		t.Fatal("tracked source missing")
	}
	for _, excluded := range []string{".git", "node_modules", "node_modules/pkg.js", ".ao", ".ao/private"} {
		if names[excluded] {
			t.Fatalf("archive contains %q", excluded)
		}
	}
}

func TestNewRequiresCompleteWorkspaceCapability(t *testing.T) {
	if _, err := New(Options{BaseURL: "https://cloud.example", Token: "token"}); err == nil {
		t.Fatal("missing workspace ID accepted")
	}
	if _, err := New(Options{BaseURL: "file:///tmp", Token: "token", WorkspaceID: "workspace"}); err == nil {
		t.Fatal("non-HTTP URL accepted")
	}
}
