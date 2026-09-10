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
		{Name: "personal", ConfigDir: filepath.Join(home, ".claude-personal")},
		{Name: "work", ConfigDir: filepath.Join(home, ".claude-work")},
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
	if err := os.MkdirAll(filepath.Join(dir, "projects"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(dir, filepath.Join(home, ".claude-alias")); err != nil {
		t.Skip(err)
	}
	got, err := discoverClaudeProfiles(context.Background(), home, filepath.Join(home, ".claude-alias"))
	if err != nil || len(got.Profiles) != 1 || got.Profiles[0].Name != "Default" || got.Profiles[0].ConfigDir != "" {
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

func TestDiscoverClaudeProfilesPreservesNamedDefaultTarget(t *testing.T) {
	home := t.TempDir()
	for _, name := range []string{".claude-perforce", ".claude-personal"} {
		dir := filepath.Join(home, name)
		if err := os.MkdirAll(filepath.Join(dir, "projects"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(home, ".claude-perforce"), filepath.Join(home, ".claude")); err != nil {
		t.Skip(err)
	}
	// An alphabetically earlier alias must not steal the real profile name.
	if err := os.Symlink(filepath.Join(home, ".claude-perforce"), filepath.Join(home, ".claude-aaa")); err != nil {
		t.Skip(err)
	}
	got, err := discoverClaudeProfiles(context.Background(), home, filepath.Join(home, ".claude"))
	want := []ClaudeProfile{
		{Name: "perforce", ConfigDir: filepath.Join(home, ".claude-perforce")},
		{Name: "personal", ConfigDir: filepath.Join(home, ".claude-personal")},
	}
	if err != nil || !reflect.DeepEqual(got.Profiles, want) {
		t.Fatalf("profiles = %#v, %v; want %#v", got, err, want)
	}
}

func TestDiscoverClaudeProfilesArbitraryNamesAndNonProfiles(t *testing.T) {
	home := t.TempDir()
	names := map[string]string{
		".claude-client-a":   "client-a",
		".claude_team":       "team",
		".claude.consulting": "consulting",
		".claudeResearch":    "Research",
	}
	for directory := range names {
		if err := os.MkdirAll(filepath.Join(home, directory, "projects"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, directory := range []string{".claude-backup", ".claude-cache", "other"} {
		if err := os.Mkdir(filepath.Join(home, directory), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(home, ".claude.json"), []byte("not a profile directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := discoverClaudeProfiles(context.Background(), home, "")
	if err != nil || len(got.Profiles) != len(names) {
		t.Fatalf("profiles = %#v, %v", got, err)
	}
	for _, profile := range got.Profiles {
		if want, ok := names[filepath.Base(profile.ConfigDir)]; !ok || profile.Name != want {
			t.Fatalf("unexpected profile: %#v", profile)
		}
	}
}

func TestDiscoverClaudeProfilesExplicitCustomPath(t *testing.T) {
	home := t.TempDir()
	custom := t.TempDir()
	got, err := discoverClaudeProfiles(context.Background(), home, custom)
	if err != nil || len(got.Profiles) != 1 || got.Profiles[0].ConfigDir != custom {
		t.Fatalf("profiles = %#v, %v", got, err)
	}
}
