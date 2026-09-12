package sessionimport

import (
	"context"
	"encoding/json"
	"fmt"
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

func TestMetadataPreservesLongExplicitTitlesAndRecentDateOrder(t *testing.T) {
	root := t.TempDir()
	title := strings.Repeat("long title ", 20) + "distinctive suffix"
	claude := NewClaudeSourceAt(root)
	path := filepath.Join(root, "11111111-1111-1111-1111-111111111111.jsonl")
	raw, _ := json.Marshal(map[string]string{"type": "custom-title", "customTitle": title})
	if err := os.WriteFile(path, append(raw, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	s, ok, err := claude.ReadMetadata(context.Background(), path)
	if err != nil || !ok || s.Title != title {
		t.Fatal(s.Title, ok, err)
	}
	raw, _ = json.Marshal(map[string]string{"id": "id", "thread_name": title})
	if err = os.WriteFile(filepath.Join(root, "session_index.jsonl"), append(raw, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	codex := NewCodexSourceAt(root, false)
	if err = codex.VisitTitles(context.Background(), func(_, got string) error {
		if got != title {
			t.Fatalf("truncated title: %s", got)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for year := 1800; year < 1930; year++ {
		dir := filepath.Join(root, "sessions", fmt.Sprint(year), "01", "01")
		if err = os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(dir, "rollout-id.jsonl"), []byte("{}\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	first := ""
	if err = codex.VisitMetadata(context.Background(), func(path string, _ os.FileInfo) error {
		if first == "" {
			first = path
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(first, filepath.Join("1929", "01", "01")) {
		t.Fatal("first date was not newest", first)
	}
}
