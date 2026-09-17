package agentlaunch

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSharedInstallKeepsAgentNodeAndCanonicalAO(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shebang selection; Windows has a separate command test")
	}
	stateDir := t.TempDir()
	t.Setenv("AO_DATA_DIR", "data")
	shared, agent := filepath.Join(t.TempDir(), "shared install's bin"), t.TempDir()
	if err := os.MkdirAll(shared, 0o700); err != nil {
		t.Fatal(err)
	}
	write := func(path, body string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	ao := filepath.Join(shared, "ao")
	write(ao, "#!/bin/sh\necho CANONICAL\n")
	write(filepath.Join(shared, "node"), "#!/bin/sh\necho OLD_NODE\n")
	write(filepath.Join(agent, "node"), "#!/bin/sh\necho AGENT_NODE\n")
	write(filepath.Join(agent, "ao"), "#!/bin/sh\necho FOREIGN\n")
	launcher := filepath.Join(agent, "agent")
	write(launcher, "#!/usr/bin/env node\n")
	executable := func() (string, error) { return ao, nil }
	base := agent + string(os.PathListSeparator) + shared + string(os.PathListSeparator) + "/usr/bin:/bin"
	worktree := t.TempDir()
	t.Chdir(worktree)
	path, err := PinnedPATH(executable, os.Getenv, map[string]string{"PATH": base}, stateDir)
	if err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"PATH": path}
	AugmentRuntimePATHForLaunchBinary(context.Background(), env, []string{launcher}, func(string) (string, error) { return filepath.Join(agent, "node"), nil }, PinnedDir(executable, stateDir))
	cmd := exec.CommandContext(context.Background(), "/bin/sh", "-c", "ao; exec \"$1\"", "sh", launcher)
	cmd.Env = []string{"PATH=" + env["PATH"]}
	output, err := cmd.CombinedOutput()
	if err != nil || string(output) != "CANONICAL\nAGENT_NODE\n" {
		t.Fatalf("executable selection: %v: %s", err, output)
	}
	if got := strings.Split(env["PATH"], string(os.PathListSeparator))[0]; got == shared {
		t.Fatal("shared install directory still takes priority")
	} else if !filepath.IsAbs(got) || !strings.HasPrefix(got, stateDir+string(os.PathSeparator)) {
		t.Fatalf("shim directory = %q, want absolute path below resolved data directory %q", got, stateDir)
	}
}

func TestSharedInstallRequiresResolvedAbsoluteDataDir(t *testing.T) {
	shared := t.TempDir()
	names := []string{"ao", "node"}
	if runtime.GOOS == "windows" {
		names = []string{"ao.exe", "node.exe"}
	}
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(shared, name), []byte("#!/bin/sh\n"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	executable := func() (string, error) { return filepath.Join(shared, names[0]), nil }
	if _, err := PinnedPATH(executable, os.Getenv, nil, "data"); err == nil || !strings.Contains(err.Error(), "must be absolute") {
		t.Fatalf("PinnedPATH error = %v, want absolute data-directory error", err)
	}
}

func TestConfiguredPATHWindowsUsesExactProtectedSpelling(t *testing.T) {
	configured := map[string]string{"Path": "project", "PATH": "ao-pinned"}
	for range 1000 {
		if got := configuredPATH(configured, true); got != "ao-pinned" {
			t.Fatalf("configured PATH = %q, want exact protected value", got)
		}
	}
	if got := configuredPATH(map[string]string{"Path": "project"}, true); got != "project" {
		t.Fatalf("case-insensitive configured PATH = %q, want project", got)
	}
}
