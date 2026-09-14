package process

import (
	"os"
	"testing"
)

func TestResolveExecutableKeepsGitUsableDuringXcodeFirstLaunch(t *testing.T) {
	if _, err := os.Stat(commandLineToolsGit); err != nil {
		t.Skipf("Command Line Tools Git is unavailable: %v", err)
	}

	got := resolveExecutable("/usr/bin/git")
	if got != commandLineToolsGit {
		t.Fatalf("resolveExecutable(/usr/bin/git) = %q, want %q", got, commandLineToolsGit)
	}
}

func TestResolveExecutablePreservesExplicitTools(t *testing.T) {
	for _, name := range []string{"/opt/homebrew/bin/git", "xcodebuild"} {
		if got := resolveExecutable(name); got != name {
			t.Fatalf("resolveExecutable(%q) = %q, want unchanged", name, got)
		}
	}
}
