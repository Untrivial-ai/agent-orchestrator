//go:build windows

package systemexec

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAgentShellCommandUsesNativePowerShellApplicationLookup(t *testing.T) {
	shell := filepath.Join(t.TempDir(), "powershell.exe")
	cmd, err := agentShellCommand(context.Background(), shell, "marker", []string{"codex", "claude"})
	if err != nil {
		t.Fatal(err)
	}
	if got := cmd.Args[:4]; strings.Join(got, "|") != shell+"|-NoLogo|-NonInteractive|-Command" {
		t.Fatalf("PowerShell prefix = %q", got)
	}
	if len(cmd.Args) != 5 || !strings.Contains(strings.Join(cmd.Env, "\n"), "AO_AGENT_PROBE_NAMES=codex;claude") {
		t.Fatal("PowerShell probe data must not be appended as command text")
	}
	if !strings.Contains(cmd.Args[4], "Get-Command -Name $name -CommandType Application") {
		t.Fatalf("PowerShell script does not restrict lookup to native applications: %q", cmd.Args[4])
	}
	if strings.Contains(cmd.Args[4], "wsl") {
		t.Fatalf("PowerShell script unexpectedly delegates lookup to WSL: %q", cmd.Args[4])
	}
}

func TestAgentShellProbeNativeWindowsFakeCLI(t *testing.T) {
	shell, err := exec.LookPath("pwsh.exe")
	if err != nil {
		shell, err = exec.LookPath("powershell.exe")
	}
	if err != nil {
		t.Skip("PowerShell unavailable")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "ao-discovery-fixture.cmd")
	if err := os.WriteFile(path, []byte("@exit /b 0\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	before := os.Getenv("PATH")
	started := time.Now()
	got, err := (AgentShellProbe{Shell: shell}).ProbeAgentShell(context.Background(), []string{"ao-discovery-fixture.cmd"})
	t.Logf("native shell %s completed in %s", filepath.Base(shell), time.Since(started))
	if err != nil {
		t.Fatal(err)
	}
	if got.Paths["ao-discovery-fixture.cmd"] != path {
		t.Fatalf("resolved = %v", got.Paths)
	}
	if os.Getenv("PATH") != before {
		t.Fatal("probe modified global PATH")
	}
}

func TestParseAgentShellSnapshotAcceptsWindowsAbsoluteApplication(t *testing.T) {
	output := "banner\r\nmarker\tPATH\tC:\\Tools;C:\\Windows\r\n" +
		"marker\tFOUND\tcodex\tC:\\Tools\\codex.exe\r\nmarker\tDONE\r\n"
	got, err := parseAgentShellSnapshot([]byte(output), "marker", []string{"codex"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Paths["codex"] != `C:\Tools\codex.exe` {
		t.Fatalf("codex = %q", got.Paths["codex"])
	}
}
