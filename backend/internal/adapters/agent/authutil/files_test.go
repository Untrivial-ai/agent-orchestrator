package authutil

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func writeFixture(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestReadFileBoundsAndFileTypes(t *testing.T) {
	root := t.TempDir()
	regular := filepath.Join(root, "credential")
	writeFixture(t, regular, "fixture-secret")
	link := filepath.Join(root, "link")
	if err := os.Symlink(regular, link); err != nil {
		t.Fatal(err)
	}
	large := filepath.Join(root, "large")
	writeFixture(t, large, strings.Repeat("x", MaxFileSize+1))
	for _, tt := range []struct {
		name, path string
		valid      bool
	}{
		{"regular", regular, true}, {"symlink", link, false}, {"directory", root, false}, {"oversize", large, false}, {"missing", filepath.Join(root, "secret-name"), false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ReadFile(context.Background(), Dependencies{}, tt.path)
			if (err == nil) != tt.valid {
				t.Fatalf("read success = %v; want %v", err == nil, tt.valid)
			}
			if tt.valid && string(got) != "fixture-secret" {
				t.Fatal("did not return file content")
			}
			if err != nil && (strings.Contains(err.Error(), root) || strings.Contains(err.Error(), "fixture-secret")) {
				t.Fatal("error leaked path or contents")
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ReadFile(ctx, Dependencies{}, regular); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled read = %v", err)
	}
}

func TestReadFileChecksInjectedDataLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credential")
	writeFixture(t, path, "small")
	deps := Dependencies{ReadFile: func(string) ([]byte, error) { return []byte(strings.Repeat("x", MaxFileSize+1)), nil }}
	if _, err := ReadFile(context.Background(), deps, path); err == nil {
		t.Fatal("accepted growing file beyond limit")
	}
	deps.ReadFile = func(string) ([]byte, error) { return nil, errors.New("fixture-secret") }
	if _, err := ReadFile(context.Background(), deps, path); err == nil || strings.Contains(err.Error(), "fixture-secret") {
		t.Fatalf("unsafe read error: %v", err)
	}
}

func TestReadStructuredFiles(t *testing.T) {
	for _, tt := range []struct {
		name, body string
		read       func(context.Context, Dependencies, string, any) error
		valid      bool
	}{
		{"json", "{\"api_key\":\"fixture-key\"}", ReadJSON, true},
		{"toml", "api_key = 'fixture-key'", ReadTOML, true},
		{"yaml", "api_key: fixture-key", ReadYAML, true},
		{"json malformed", "{\"api_key\":\"fixture-key\"", ReadJSON, false},
		{"json multiple documents", "{\"api_key\":\"fixture-key\"} {}", ReadJSON, false},
		{"toml malformed", "api_key = 'fixture-key", ReadTOML, false},
		{"yaml malformed", "api_key: [fixture-key", ReadYAML, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config")
			writeFixture(t, path, tt.body)
			var parsed map[string]any
			err := tt.read(context.Background(), Dependencies{}, path, &parsed)
			if (err == nil) != tt.valid {
				t.Fatalf("decode success = %v; want %v", err == nil, tt.valid)
			}
			if tt.valid && parsed["api_key"] != "fixture-key" {
				t.Fatalf("decoded %#v", parsed)
			}
			if err != nil && strings.Contains(err.Error(), "fixture-key") {
				t.Fatal("parse error exposed secret")
			}
		})
	}
}

func TestFindUpwardNearestFirst(t *testing.T) {
	root := t.TempDir()
	child := filepath.Join(root, "project", "nested")
	if err := os.MkdirAll(child, 0o700); err != nil {
		t.Fatal(err)
	}
	first := filepath.Join(child, ".auth-fixture")
	second := filepath.Join(root, "project", ".auth-fixture")
	writeFixture(t, first, "child")
	writeFixture(t, second, "parent")
	if err := os.Symlink(second, filepath.Join(root, ".auth-fixture")); err != nil {
		t.Fatal(err)
	}
	got, err := FindUpward(context.Background(), Dependencies{}, child, ".auth-fixture")
	if err != nil || !reflect.DeepEqual(got, []string{first, second}) {
		t.Fatalf("upward search = %v, %v", got, err)
	}
	if _, err := FindUpward(context.Background(), Dependencies{}, child, "../outside"); err == nil {
		t.Fatal("accepted escaping filename")
	}
}

func TestFindUpwardFindsNestedProjectConfigAndStopsAtRoot(t *testing.T) {
	root := t.TempDir()
	start := filepath.Join(root, "project", "nested")
	if err := os.MkdirAll(start, 0o700); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, "project", ".agent", "settings.json")
	writeFixture(t, want, "{}")
	seen := make(map[string]bool)
	deps := Dependencies{Lstat: func(path string) (os.FileInfo, error) {
		if seen[path] {
			t.Fatal("revisited an ancestor")
		}
		seen[path] = true
		return os.Lstat(path)
	}}
	got, err := FindUpward(context.Background(), deps, start, ".agent/settings.json")
	if err != nil || !reflect.DeepEqual(got, []string{want}) {
		t.Fatalf("nested search = %v, %v", got, err)
	}
	if !seen[filepath.Join(filepath.VolumeName(start)+string(filepath.Separator), ".agent", "settings.json")] {
		t.Fatal("search did not reach filesystem root")
	}
}
