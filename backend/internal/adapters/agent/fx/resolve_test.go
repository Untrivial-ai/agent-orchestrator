package fx

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestResolveFXBinaryPrefersPATH(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fx has no native Windows support")
	}
	pathDir := t.TempDir()
	home := t.TempDir()
	pathBinary := writeExecutable(t, filepath.Join(pathDir, "fx"))
	writeExecutable(t, filepath.Join(home, ".local", "bin", "fx"))
	t.Setenv("PATH", pathDir)
	t.Setenv("HOME", home)

	got, err := ResolveFXBinary(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != pathBinary {
		t.Fatalf("binary = %q, want PATH binary %q", got, pathBinary)
	}
}

func TestResolveFXBinaryFallsBackToLocalBin(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fx has no native Windows support")
	}
	home := t.TempDir()
	homeBinary := writeExecutable(t, filepath.Join(home, ".local", "bin", "fx"))
	t.Setenv("PATH", t.TempDir())
	t.Setenv("HOME", home)

	got, err := ResolveFXBinary(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != homeBinary {
		t.Fatalf("binary = %q, want home binary %q", got, homeBinary)
	}
}

func TestResolveFXBinaryReportsMissing(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fx has no native Windows support")
	}
	t.Setenv("PATH", t.TempDir())
	t.Setenv("HOME", t.TempDir())

	_, err := ResolveFXBinary(context.Background())
	if !errors.Is(err, ports.ErrAgentBinaryNotFound) {
		t.Fatalf("error = %v, want ErrAgentBinaryNotFound", err)
	}
}

func TestResolveFXBinaryRejectsNativeWindowsWithGuidance(t *testing.T) {
	_, err := resolveFXBinary(context.Background(), "windows")
	if !errors.Is(err, ports.ErrAgentBinaryNotFound) {
		t.Fatalf("error = %v, want ErrAgentBinaryNotFound", err)
	}
	if !strings.Contains(err.Error(), "WSL") {
		t.Fatalf("error = %q, want WSL guidance", err)
	}
}

func TestResolveFXBinaryHonorsCancellationBeforePlatformCheck(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := resolveFXBinary(ctx, "windows")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func writeExecutable(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}
