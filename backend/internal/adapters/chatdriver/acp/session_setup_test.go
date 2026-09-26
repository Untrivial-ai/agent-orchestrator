package acp

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestNormalizeAdditionalDirectoriesUnsupportedAgentOmitsChildrenUnderCWD(t *testing.T) {
	root := t.TempDir()
	children := []string{
		filepath.Join(root, "api"),
		filepath.Join(root, "web", "..", "web"),
	}

	got, err := normalizeAdditionalDirectories(root, children, false)
	if err != nil {
		t.Fatalf("normalizeAdditionalDirectories: %v", err)
	}
	if got != nil {
		t.Fatalf("additional directories = %#v, want omitted for descendants of cwd", got)
	}
}

func TestNormalizeAdditionalDirectoriesUnsupportedAgentRejectsExternalRoot(t *testing.T) {
	root := t.TempDir()
	external := t.TempDir()

	_, err := normalizeAdditionalDirectories(root, []string{external}, false)
	if err == nil || !strings.Contains(err.Error(), "does not support additional workspace directories") {
		t.Fatalf("error = %v, want unsupported additional-directories error", err)
	}
}

func TestNormalizeAdditionalDirectoriesCapableAgentPreservesExternalAndChildRoots(t *testing.T) {
	root := t.TempDir()
	child := filepath.Join(root, "api")
	external := t.TempDir()

	got, err := normalizeAdditionalDirectories(root, []string{child, external, child, root}, true)
	if err != nil {
		t.Fatalf("normalizeAdditionalDirectories: %v", err)
	}
	want := []string{filepath.Clean(child), filepath.Clean(external)}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("additional directories = %#v, want %#v", got, want)
	}
}

func TestNormalizeAdditionalDirectoriesSingleRepoNeedsNoCapability(t *testing.T) {
	got, err := normalizeAdditionalDirectories(t.TempDir(), nil, false)
	if err != nil || got != nil {
		t.Fatalf("single-repo directories = %#v, err = %v; want nil, nil", got, err)
	}
}
