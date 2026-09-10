package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestDiscoverClaudeProfiles(t *testing.T) {
	home := t.TempDir()
	for _, name := range []string{".claude", ".claude-personal", ".claude-work", ".claude-backup", "unrelated"} {
		if err := os.Mkdir(filepath.Join(home, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{".claude-personal", ".claude-work", "unrelated"} {
		if err := os.WriteFile(filepath.Join(home, name, ".claude.json"), []byte("private, unreadable JSON"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	got, err := discoverClaudeProfiles(context.Background(), home, filepath.Join(home, ".claude-work"))
	if err != nil {
		t.Fatal(err)
	}
	want := []ClaudeProfile{
		{Name: "Default", ConfigDir: ""},
		{Name: "Environment (.claude-work)", ConfigDir: filepath.Join(home, ".claude-work")},
		{Name: "personal", ConfigDir: filepath.Join(home, ".claude-personal")},
	}
	if !reflect.DeepEqual(got.Profiles, want) {
		t.Fatalf("profiles = %#v, want %#v", got.Profiles, want)
	}
	if err := os.RemoveAll(filepath.Join(home, ".claude-personal")); err != nil {
		t.Fatal(err)
	}
	got, err = discoverClaudeProfiles(context.Background(), home, "")
	if err != nil || len(got.Profiles) != 2 {
		t.Fatalf("rediscovery = %#v, %v", got, err)
	}
}

func TestDiscoverClaudeProfilesDeduplicatesSymlinks(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".claude")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(dir, filepath.Join(home, ".claude-alias")); err != nil {
		t.Skip(err)
	}
	got, err := discoverClaudeProfiles(context.Background(), home, filepath.Join(home, ".claude-alias"))
	if err != nil || len(got.Profiles) != 1 {
		t.Fatalf("profiles = %#v, %v", got, err)
	}
}

func TestDiscoverClaudeProfilesCancellationAndEmpty(t *testing.T) {
	home := t.TempDir()
	got, err := discoverClaudeProfiles(context.Background(), home, "relative")
	if err != nil || got.Profiles == nil || len(got.Profiles) != 0 {
		t.Fatalf("profiles = %#v, %v", got, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := discoverClaudeProfiles(ctx, home, ""); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
}
