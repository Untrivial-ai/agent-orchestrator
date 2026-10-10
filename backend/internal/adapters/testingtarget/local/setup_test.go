package local

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestProbeRejectsRevisionDefaultDaemonInsteadOfOwnedBinary(t *testing.T) {
	f := fixture(t)
	f.ready["executablePath"] = filepath.Join(f.s.frontend, "daemon", "ao")
	if err := f.a.Probe(context.Background(), f.s.target); err == nil || !strings.Contains(err.Error(), "identity mismatch") {
		t.Fatalf("checkout daemon was admitted: %v", err)
	}
}

func TestUnsupportedRevisionFailsBeforeElectronAndCleansSetup(t *testing.T) {
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	base := filepath.Join(home, ".ao", "dev", "agentic-target")
	checkout := filepath.Join(base, "checkout")
	if err := os.MkdirAll(filepath.Join(checkout, "frontend"), 0o700); err != nil {
		t.Fatal(err)
	}
	a := New()
	a.ops.home = func() (string, error) { return home, nil }
	a.ops.tmuxBinary = func(string) (string, error) { return "/fixture/tmux", nil }
	a.ops.prepare = func(_ context.Context, _, _, root string) error {
		if err := os.Mkdir(filepath.Join(root, "setup"), 0o700); err != nil {
			return err
		}
		return errors.New("unsupported_revision: original launch contract error")
	}
	a.ops.start = func(string, string, []string, *os.File) (int, error) {
		t.Fatal("unsupported revision launched Electron")
		return 0, nil
	}
	_, err = a.Start(context.Background(), ports.TestingTargetSpec{AttemptID: "unsupported", Generation: 1, CheckoutPath: checkout, CommitSHA: strings.Repeat("a", 40), Deadline: time.Now().Add(time.Minute)})
	if err == nil || err.Error() != "unsupported_revision: original launch contract error" {
		t.Fatalf("preflight cause lost: %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(base, "unsupported"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("private setup left behind: %v, %v", entries, err)
	}
}

func TestOwnedDaemonCommandHandlesQuotedPaths(t *testing.T) {
	if got := daemonCommand("/owned path/it's ao"); got != "exec '/owned path/it'\"'\"'s ao' daemon" {
		t.Fatalf("daemon override = %q", got)
	}
}

func TestOwnedBuildKeepsCompilerError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("local macOS target uses POSIX tools")
	}
	root := t.TempDir()
	frontend := filepath.Join(root, "checkout", "frontend")
	if err := os.Mkdir(filepath.Join(root, "attempt"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "checkout", "backend"), 0o700); err != nil {
		t.Fatal(err)
	}
	tool := filepath.Join(root, "go")
	if err := os.WriteFile(tool, []byte("#!/bin/sh\necho 'compiler diagnostic from pinned revision' >&2\nexit 19\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
	err := buildOwnedDaemon(context.Background(), frontend, filepath.Join(root, "attempt", "daemon", "ao"))
	if err == nil || !strings.Contains(err.Error(), "compiler diagnostic from pinned revision") || !strings.Contains(err.Error(), "exit status 19") {
		t.Fatalf("compiler cause was lost: %v", err)
	}
}
