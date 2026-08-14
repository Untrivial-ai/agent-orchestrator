package binaryutil

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestResolveBinaryPrefersPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("PATH lookup shape differs on windows")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "widget")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)

	got, err := ResolveBinary(context.Background(), BinarySpec{Label: "widget", Names: []string{"widget"}})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got != bin {
		t.Fatalf("got %q, want %q", got, bin)
	}
}

func TestDefaultFNMDirDarwin(t *testing.T) {
	home := filepath.Join(string(filepath.Separator), "Users", "tester")
	want := filepath.Join(home, "Library", "Application Support", "fnm")
	if got := defaultFNMDir(home, "", "darwin"); got != want {
		t.Fatalf("defaultFNMDir() = %q, want %q", got, want)
	}
}

func TestDefaultFNMDirPrefersXDGDataHome(t *testing.T) {
	xdg := filepath.Join(string(filepath.Separator), "custom", "share")
	want := filepath.Join(xdg, "fnm")
	if got := defaultFNMDir("/home/tester", xdg, "linux"); got != want {
		t.Fatalf("defaultFNMDir() = %q, want %q", got, want)
	}
}

func TestResolveBinaryFallsBackToUnixPathFloorDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix PATH floor does not apply on windows")
	}
	pathDir := t.TempDir()
	floorDir := t.TempDir()
	t.Setenv("PATH", pathDir)
	oldFloor := unixPathFloorDirs
	unixPathFloorDirs = []string{floorDir}
	t.Cleanup(func() { unixPathFloorDirs = oldFloor })
	bin := filepath.Join(floorDir, "widget")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := ResolveBinary(context.Background(), BinarySpec{
		Label: "widget",
		Names: []string{"widget"},
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got != bin {
		t.Fatalf("got %q, want %q", got, bin)
	}
}

func TestResolveBinaryPrefersPathBeforeUnixPathFloorDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix PATH floor does not apply on windows")
	}
	pathDir := t.TempDir()
	floorDir := t.TempDir()
	t.Setenv("PATH", pathDir)
	pathBin := filepath.Join(pathDir, "widget")
	floorBin := filepath.Join(floorDir, "widget")
	if err := os.WriteFile(pathBin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(floorBin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := ResolveBinary(context.Background(), BinarySpec{
		Label:        "widget",
		Names:        []string{"widget"},
		UnixPathDirs: []string{floorDir},
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got != pathBin {
		t.Fatalf("got %q, want PATH binary %q", got, pathBin)
	}
}

func TestLookPathUsesUnixPathFloor(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix PATH floor does not apply on windows")
	}
	pathDir := t.TempDir()
	floorDir := t.TempDir()
	t.Setenv("PATH", pathDir)
	oldFloor := unixPathFloorDirs
	unixPathFloorDirs = []string{floorDir}
	t.Cleanup(func() { unixPathFloorDirs = oldFloor })
	bin := filepath.Join(floorDir, "widget")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := LookPath("widget")
	if err != nil {
		t.Fatalf("LookPath: %v", err)
	}
	if got != bin {
		t.Fatalf("got %q, want %q", got, bin)
	}
}

func TestResolveBinaryFallsBackToHomeCandidate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix home candidate shape")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", t.TempDir()) // empty of the binary
	bin := filepath.Join(home, ".local", "bin", "widget")
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := ResolveBinary(context.Background(), BinarySpec{
		Label:         "widget",
		Names:         []string{"widget"},
		UnixHomePaths: [][]string{{".local", "bin", "widget"}},
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got != bin {
		t.Fatalf("got %q, want %q", got, bin)
	}
}

func TestResolveBinaryFallsBackToNodeManagerCandidate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix node-manager candidate shape")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", t.TempDir()) // empty of the binary
	t.Setenv("VOLTA_HOME", "")
	t.Setenv("FNM_DIR", "")
	bin := filepath.Join(home, ".nvm", "versions", "node", "v22.23.1", "bin", "widget")
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := ResolveBinary(context.Background(), BinarySpec{
		Label:       "widget",
		Names:       []string{"widget"},
		NodeManaged: true,
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got != bin {
		t.Fatalf("got %q, want %q", got, bin)
	}
}

func TestResolveBinarySkipsNodeManagerCandidatesUnlessExplicit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix node-manager candidate shape")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", t.TempDir()) // empty of the binary
	t.Setenv("VOLTA_HOME", "")
	t.Setenv("FNM_DIR", "")
	bin := filepath.Join(home, ".nvm", "versions", "node", "v22.23.1", "bin", "widget")
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	_, err := ResolveBinary(context.Background(), BinarySpec{
		Label: "widget",
		Names: []string{"widget"},
	})
	if !errors.Is(err, ports.ErrAgentBinaryNotFound) {
		t.Fatalf("want ErrAgentBinaryNotFound, got %v", err)
	}
}

func TestResolveBinaryPrefersHomeCandidateOverNodeManagerCandidate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix node-manager candidate shape")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", t.TempDir()) // empty of the binary
	t.Setenv("VOLTA_HOME", "")
	t.Setenv("FNM_DIR", "")
	homeBin := filepath.Join(home, ".local", "bin", "widget")
	nvmBin := filepath.Join(home, ".nvm", "versions", "node", "v22.23.1", "bin", "widget")
	for _, bin := range []string{homeBin, nvmBin} {
		if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	got, err := ResolveBinary(context.Background(), BinarySpec{
		Label:         "widget",
		Names:         []string{"widget"},
		UnixHomePaths: [][]string{{".local", "bin", "widget"}},
		NodeManaged:   true,
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got != homeBin {
		t.Fatalf("got %q, want %q", got, homeBin)
	}
}

func TestUnixNodeManagerBinCandidatesSortsVersionsSemantically(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix node-manager candidate shape")
	}
	home := t.TempDir()
	t.Setenv("VOLTA_HOME", filepath.Join(home, ".volta"))
	t.Setenv("FNM_DIR", filepath.Join(home, ".local", "share", "fnm"))
	for _, version := range []string{"v22.9.0", "v22.23.1", "v9.99.0"} {
		bin := filepath.Join(home, ".nvm", "versions", "node", version, "bin", "widget")
		if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	got, err := UnixNodeManagerBinCandidates(context.Background(), home, "widget")
	if err != nil {
		t.Fatalf("UnixNodeManagerBinCandidates: %v", err)
	}
	want := []string{
		filepath.Join(home, ".nvm", "versions", "node", "v22.23.1", "bin", "widget"),
		filepath.Join(home, ".nvm", "versions", "node", "v22.9.0", "bin", "widget"),
		filepath.Join(home, ".nvm", "versions", "node", "v9.99.0", "bin", "widget"),
	}
	if len(got) != len(want) {
		t.Fatalf("got %d candidates, want %d: %#v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("candidate %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestUnixNodeManagerBinCandidatesPreferNVMDefaultAlias(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix node-manager candidate shape")
	}
	home := t.TempDir()
	t.Setenv("VOLTA_HOME", filepath.Join(home, ".volta"))
	t.Setenv("FNM_DIR", filepath.Join(home, ".local", "share", "fnm"))
	for _, version := range []string{"v22.23.1", "v20.11.1"} {
		bin := filepath.Join(home, ".nvm", "versions", "node", version, "bin", "widget")
		if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	alias := filepath.Join(home, ".nvm", "alias", "default")
	if err := os.MkdirAll(filepath.Dir(alias), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(alias, []byte("v20.11.1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := UnixNodeManagerBinCandidates(context.Background(), home, "widget")
	if err != nil {
		t.Fatalf("UnixNodeManagerBinCandidates: %v", err)
	}
	want := filepath.Join(home, ".nvm", "versions", "node", "v20.11.1", "bin", "widget")
	if len(got) == 0 || got[0] != want {
		t.Fatalf("first candidate = %#v, want %q first", got, want)
	}
}

func TestUnixNodeManagerBinCandidatesHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := UnixNodeManagerBinCandidates(ctx, t.TempDir(), "widget")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
}

func TestResolveBinaryFallsBackToVoltaCandidate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix node-manager candidate shape")
	}
	home := t.TempDir()
	voltaHome := filepath.Join(home, "volta-home")
	t.Setenv("HOME", home)
	t.Setenv("PATH", t.TempDir())
	t.Setenv("VOLTA_HOME", voltaHome)
	t.Setenv("FNM_DIR", "")
	bin := filepath.Join(voltaHome, "bin", "widget")
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := ResolveBinary(context.Background(), BinarySpec{
		Label:       "widget",
		Names:       []string{"widget"},
		NodeManaged: true,
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got != bin {
		t.Fatalf("got %q, want %q", got, bin)
	}
}

func TestResolveBinaryFallsBackToFNMCandidate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix node-manager candidate shape")
	}
	home := t.TempDir()
	fnmDir := filepath.Join(home, "fnm")
	t.Setenv("HOME", home)
	t.Setenv("PATH", t.TempDir())
	t.Setenv("VOLTA_HOME", filepath.Join(home, ".volta"))
	t.Setenv("FNM_DIR", fnmDir)
	bin := filepath.Join(fnmDir, "node-versions", "v22.23.1", "installation", "bin", "widget")
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := ResolveBinary(context.Background(), BinarySpec{
		Label:       "widget",
		Names:       []string{"widget"},
		NodeManaged: true,
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got != bin {
		t.Fatalf("got %q, want %q", got, bin)
	}
}

func TestResolveBinaryNotFound(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	_, err := ResolveBinary(context.Background(), BinarySpec{
		Label:    "widget",
		Names:    []string{"widget-does-not-exist"},
		WinNames: []string{"widget-does-not-exist.exe"},
	})
	if !errors.Is(err, ports.ErrAgentBinaryNotFound) {
		t.Fatalf("want ErrAgentBinaryNotFound, got %v", err)
	}
}

func TestResolveBinaryHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := ResolveBinary(ctx, BinarySpec{Label: "widget", Names: []string{"widget"}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
}
