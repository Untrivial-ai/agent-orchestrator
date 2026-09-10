package agentlaunch

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSharedWindowsInstallSelectsOnlyCanonicalAO(t *testing.T) {
	if os.Getenv("AO_TEST_PIN_CHILD") == "1" {
		exe, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		fmt.Println("IDENTITY=" + exe)
		return
	}
	dataDir := t.TempDir()
	shared, agent := t.TempDir(), t.TempDir()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	binary, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(shared, "ao.exe"), filepath.Join(shared, "node.exe"), filepath.Join(agent, "ao.exe"), filepath.Join(agent, "node.exe")} {
		if err := os.WriteFile(path, binary, 0o700); err != nil {
			t.Fatal(err)
		} //nolint:gosec // executable test fixture
	}
	canonical := filepath.Join(shared, "ao.exe")
	executable := func() (string, error) { return canonical, nil }
	path, err := PinnedPATH(executable, os.Getenv, map[string]string{"PATH": agent + ";" + shared + ";" + os.Getenv("PATH")}, dataDir)
	if err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"PATH": path}
	AugmentRuntimePATHForLaunchBinary(context.Background(), env, []string{filepath.Join(agent, "agent.exe")}, exec.LookPath, PinnedDir(executable, dataDir))
	cmd := exec.CommandContext(context.Background(), os.Getenv("ComSpec"), "/d", "/c", "call ao -test.run=^TestSharedWindowsInstallSelectsOnlyCanonicalAO$ & node -test.run=^TestSharedWindowsInstallSelectsOnlyCanonicalAO$")
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if !strings.EqualFold(key, "PATH") {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	cmd.Env = append(cmd.Env, "PATH="+env["PATH"], "AO_TEST_PIN_CHILD=1")
	cmd.Dir = t.TempDir()
	output, err := cmd.CombinedOutput()
	if err != nil || !containsSameExecutable(t, string(output), canonical) || !strings.Contains(string(output), "IDENTITY="+filepath.Join(agent, "node.exe")) {
		t.Fatalf("executable selection: %v\n%s", err, output)
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Fatal("Windows regression requires Git Bash: ", err)
	}
	bashCmd := exec.CommandContext(context.Background(), bash, "--noprofile", "--norc", "-c", "ao -test.run=^TestSharedWindowsInstallSelectsOnlyCanonicalAO$")
	bashCmd.Env = cmd.Env
	bashCmd.Dir = t.TempDir()
	bashOutput, err := bashCmd.CombinedOutput()
	if err != nil || !containsSameExecutable(t, string(bashOutput), canonical) {
		t.Fatalf("Git Bash AO selection: %v\n%s", err, bashOutput)
	}
}

func TestWindowsAOExecutableFallsBackToCopyAcrossFilesystems(t *testing.T) {
	if os.Getenv("AO_TEST_PIN_CHILD") == "1" {
		exe, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		fmt.Println("IDENTITY=" + exe)
		return
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	canonical := filepath.Join(t.TempDir(), "ao.exe")
	binary, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(canonical, binary, 0o700); err != nil { //nolint:gosec // executable test fixture
		t.Fatal(err)
	}
	shimDir := t.TempDir()
	linkCalls := 0
	if err := ensureWindowsAOExecutableWithLink(shimDir, canonical, func(string, string) error {
		linkCalls++
		return errors.New("cross-volume link")
	}); err != nil {
		t.Fatal(err)
	}
	if linkCalls != 1 {
		t.Fatalf("link calls = %d, want forced cross-volume attempt", linkCalls)
	}
	shim := filepath.Join(shimDir, "ao.exe")
	shimBinary, err := os.ReadFile(shim)
	if err != nil || !bytes.Equal(shimBinary, binary) {
		t.Fatalf("copied AO executable: %v, identical=%v", err, bytes.Equal(shimBinary, binary))
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Fatal("Windows regression requires Git Bash: ", err)
	}
	cmd := exec.CommandContext(context.Background(), bash, "--noprofile", "--norc", "-c", "ao -test.run=^TestWindowsAOExecutableFallsBackToCopyAcrossFilesystems$")
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if !strings.EqualFold(key, "PATH") {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	cmd.Env = append(cmd.Env, "PATH="+shimDir+";"+os.Getenv("PATH"), "AO_TEST_PIN_CHILD=1")
	output, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(strings.ToLower(string(output)), strings.ToLower("IDENTITY="+shim)) {
		t.Fatalf("Git Bash copied AO selection: %v\n%s", err, output)
	}
}

func containsSameExecutable(t *testing.T, output, canonical string) bool {
	t.Helper()
	for _, line := range strings.Split(output, "\n") {
		path, ok := strings.CutPrefix(strings.TrimSpace(line), "IDENTITY=")
		if !ok {
			continue
		}
		got, gotErr := os.Stat(path)
		want, wantErr := os.Stat(canonical)
		return gotErr == nil && wantErr == nil && os.SameFile(got, want)
	}
	return false
}
