package tmuxbin

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestResolveWithPrefersBundledTmux(t *testing.T) {
	self := filepath.Join(string(filepath.Separator), "Applications", "Agent Orchestrator.app", "Contents", "Resources", "daemon", "ao")
	bundled := filepath.Join(filepath.Dir(self), "tmux")
	var lookups []string

	got, err := ResolveWith(func() (string, error) { return self, nil }, func(name string) (string, error) {
		lookups = append(lookups, name)
		switch name {
		case bundled:
			return bundled, nil
		case "tmux":
			return "/opt/homebrew/bin/tmux", nil
		default:
			return "", errors.New("not found")
		}
	})
	if err != nil {
		t.Fatalf("ResolveWith: %v", err)
	}
	if got.Path != bundled || got.Source != SourceBundled {
		t.Fatalf("resolution = %+v, want bundled %q", got, bundled)
	}
	if want := []string{bundled}; !reflect.DeepEqual(lookups, want) {
		t.Fatalf("lookups = %v, want %v", lookups, want)
	}
}

func TestResolveWithFallsBackToSystemTmux(t *testing.T) {
	self := filepath.Join(string(filepath.Separator), "Applications", "Agent Orchestrator.app", "Contents", "Resources", "daemon", "ao")
	bundled := filepath.Join(filepath.Dir(self), "tmux")
	var lookups []string

	got, err := ResolveWith(func() (string, error) { return self, nil }, func(name string) (string, error) {
		lookups = append(lookups, name)
		if name == "tmux" {
			return "/usr/local/bin/tmux", nil
		}
		return "", errors.New("not found")
	})
	if err != nil {
		t.Fatalf("ResolveWith: %v", err)
	}
	if got.Path != "/usr/local/bin/tmux" || got.Source != SourceSystem {
		t.Fatalf("resolution = %+v, want system tmux", got)
	}
	if want := []string{bundled, "tmux"}; !reflect.DeepEqual(lookups, want) {
		t.Fatalf("lookups = %v, want %v", lookups, want)
	}
}

func TestResolveWithReturnsErrorWhenTmuxIsUnavailable(t *testing.T) {
	_, err := ResolveWith(func() (string, error) { return "/usr/local/bin/ao", nil }, func(string) (string, error) {
		return "", errors.New("not found")
	})
	if err == nil {
		t.Fatal("ResolveWith error = nil, want missing tmux error")
	}
}

func TestResolveWithFollowsExecutableSymlinkIntoAppBundle(t *testing.T) {
	appDaemon := filepath.Join(t.TempDir(), "AO.app", "Contents", "Resources", "daemon")
	if err := os.MkdirAll(appDaemon, 0o755); err != nil {
		t.Fatal(err)
	}
	realAO := filepath.Join(appDaemon, "ao")
	if err := os.WriteFile(realAO, nil, 0o755); err != nil {
		t.Fatal(err)
	}
	shimDir := t.TempDir()
	shim := filepath.Join(shimDir, "ao")
	if err := os.Symlink(realAO, shim); err != nil {
		t.Fatal(err)
	}
	canonicalAO, err := filepath.EvalSymlinks(shim)
	if err != nil {
		t.Fatal(err)
	}
	bundled := filepath.Join(filepath.Dir(canonicalAO), "tmux")

	got, err := ResolveWith(func() (string, error) { return shim, nil }, func(name string) (string, error) {
		if name == bundled {
			return bundled, nil
		}
		return "", errors.New("not found")
	})
	if err != nil {
		t.Fatalf("ResolveWith: %v", err)
	}
	if got.Path != bundled || got.Source != SourceBundled {
		t.Fatalf("resolution = %+v, want bundled %q", got, bundled)
	}
}

func TestResolveWithDoesNotTreatSiblingSystemTmuxAsBundled(t *testing.T) {
	var lookups []string
	got, err := ResolveWith(func() (string, error) { return "/usr/local/bin/ao", nil }, func(name string) (string, error) {
		lookups = append(lookups, name)
		if name == "tmux" {
			return "/usr/local/bin/tmux", nil
		}
		return "", errors.New("not found")
	})
	if err != nil {
		t.Fatalf("ResolveWith: %v", err)
	}
	if got.Path != "/usr/local/bin/tmux" || got.Source != SourceSystem {
		t.Fatalf("resolution = %+v, want system tmux", got)
	}
	if want := []string{"tmux"}; !reflect.DeepEqual(lookups, want) {
		t.Fatalf("lookups = %v, want %v", lookups, want)
	}
}

func TestResolveWithFallsBackWhenExecutableIsUnavailable(t *testing.T) {
	tests := []struct {
		name       string
		executable func() (string, error)
	}{
		{name: "error", executable: func() (string, error) { return "", errors.New("no executable") }},
		{name: "empty", executable: func() (string, error) { return "", nil }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ResolveWith(tt.executable, func(name string) (string, error) {
				if name == "tmux" {
					return "/opt/homebrew/bin/tmux", nil
				}
				return "", errors.New("unexpected lookup")
			})
			if err != nil {
				t.Fatalf("ResolveWith: %v", err)
			}
			if got.Path != "/opt/homebrew/bin/tmux" || got.Source != SourceSystem {
				t.Fatalf("resolution = %+v, want system tmux", got)
			}
		})
	}
}
