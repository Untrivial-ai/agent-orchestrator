package shellterm

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func writeTestExecutable(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte("test"), 0o755); err != nil {
		t.Fatalf("write executable %s: %v", path, err)
	}
}

func TestResolveWindowsShellHonorsNamedSelections(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"pwsh.exe", "powershell.exe", "cmd.exe"} {
		writeTestExecutable(t, filepath.Join(dir, name))
	}
	t.Setenv("PATH", dir)
	t.Setenv("ComSpec", "")

	tests := []struct {
		preference string
		executable string
		args       []string
	}{
		{preference: "pwsh", executable: "pwsh.exe", args: []string{"-NoLogo"}},
		{preference: "powershell", executable: "powershell.exe", args: []string{"-NoLogo"}},
		{preference: "cmd", executable: "cmd.exe"},
	}
	for _, tt := range tests {
		t.Run(tt.preference, func(t *testing.T) {
			argv, usedFallback := resolveWindowsShell(tt.preference)
			if usedFallback {
				t.Fatal("used automatic fallback for an available named shell")
			}
			want := append([]string{filepath.Join(dir, tt.executable)}, tt.args...)
			if !reflect.DeepEqual(argv, want) {
				t.Fatalf("argv = %#v, want %#v", argv, want)
			}
		})
	}
}

func TestResolveWindowsShellFindsGitBashFromGitInstall(t *testing.T) {
	root := filepath.Join(t.TempDir(), "Git")
	gitPath := filepath.Join(root, "cmd", "git.exe")
	bashPath := filepath.Join(root, "bin", "bash.exe")
	writeTestExecutable(t, gitPath)
	writeTestExecutable(t, bashPath)
	t.Setenv("PATH", filepath.Dir(gitPath))

	argv, usedFallback := resolveWindowsShell("git-bash")

	if usedFallback {
		t.Fatal("used automatic fallback despite Git Bash being installed")
	}
	want := []string{bashPath, "--login", "-i"}
	if !reflect.DeepEqual(argv, want) {
		t.Fatalf("argv = %#v, want %#v", argv, want)
	}
}

func TestResolveWindowsShellSupportsCustomExecutable(t *testing.T) {
	customPath := filepath.Join(t.TempDir(), "bash.exe")
	writeTestExecutable(t, customPath)

	argv, usedFallback := resolveWindowsShell(customPath)

	if usedFallback {
		t.Fatal("used automatic fallback for an available custom executable")
	}
	want := []string{customPath, "--login", "-i"}
	if !reflect.DeepEqual(argv, want) {
		t.Fatalf("argv = %#v, want %#v", argv, want)
	}
}

func TestResolveWindowsShellFallsBackWhenSelectionIsUnavailable(t *testing.T) {
	dir := t.TempDir()
	pwshPath := filepath.Join(dir, "pwsh.exe")
	writeTestExecutable(t, pwshPath)
	t.Setenv("PATH", dir)

	argv, usedFallback := resolveWindowsShell(filepath.Join(dir, "missing.exe"))

	if !usedFallback {
		t.Fatal("did not report the automatic fallback")
	}
	want := []string{pwshPath, "-NoLogo"}
	if !reflect.DeepEqual(argv, want) {
		t.Fatalf("argv = %#v, want %#v", argv, want)
	}
}

func TestResolveWindowsShellCmdHonorsComSpecWhenCmdIsNotOnPath(t *testing.T) {
	dir := t.TempDir()
	pwshPath := filepath.Join(dir, "pwsh.exe")
	comSpecPath := filepath.Join(dir, "custom-cmd.exe")
	writeTestExecutable(t, pwshPath)
	writeTestExecutable(t, comSpecPath)
	t.Setenv("PATH", dir)
	t.Setenv("ComSpec", comSpecPath)

	argv, usedFallback := resolveWindowsShell("cmd")

	if usedFallback {
		t.Fatal("used automatic fallback for Command Prompt with a valid ComSpec")
	}
	want := []string{comSpecPath}
	if !reflect.DeepEqual(argv, want) {
		t.Fatalf("argv = %#v, want %#v", argv, want)
	}
}
