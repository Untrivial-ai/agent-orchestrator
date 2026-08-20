package tmuxbin

import (
	"errors"
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
