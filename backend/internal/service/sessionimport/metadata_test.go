package sessionimport

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMetadataClaudeTailTitleNoUsageCache(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "projects", "repo", "11111111-1111-1111-1111-111111111111.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	body := `{"type":"user","cwd":"/repo","message":{"role":"user","content":"Original"}}` + "\n" + strings.Repeat("{}\n", 100000) + `{"type":"custom-title","customTitle":"Renamed old conversation"}` + "\n"
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	src := NewClaudeSourceAt(root)
	s, ok, err := src.ReadMetadata(context.Background(), path)
	if err != nil || !ok || s.Title != "Renamed old conversation" || s.TokenCount != -1 {
		t.Fatalf("%+v %v %v", s, ok, err)
	}
	if len(src.cache.entries) != 0 {
		t.Fatal("metadata warmed transcript cache")
	}
}
func TestMetadataReadFailureAndRootEscape(t *testing.T) {
	root := t.TempDir()
	src := NewClaudeSourceAt(root)
	if _, _, err := src.ReadMetadata(context.Background(), filepath.Join(root, "missing")); err == nil {
		t.Fatal("missing silently ignored")
	}
	other := filepath.Join(t.TempDir(), "transcript")
	if err := os.WriteFile(other, []byte("{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := src.ReadMetadata(context.Background(), filepath.Join(root, "escape")); err == nil {
		t.Fatal("root escape accepted")
	}
}
