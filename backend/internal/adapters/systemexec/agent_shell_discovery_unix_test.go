//go:build !windows

package systemexec

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAgentShellProbeReadsLoginProfileAndIgnoresBanners(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash unavailable")
	}
	home := t.TempDir()
	bin := filepath.Join(home, "profile-bin")
	if err := os.Mkdir(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	tool := filepath.Join(bin, "fixture-agent")
	if err := os.WriteFile(tool, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	profile := "printf profile-banner\nexport PATH=" + shellSingleQuote(bin) + ":$PATH\n"
	if err := os.WriteFile(filepath.Join(home, ".bash_profile"), []byte(profile), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)

	got, err := (AgentShellProbe{Shell: bash}).ProbeAgentShell(context.Background(), []string{"fixture-agent", "missing-agent"})
	if err != nil {
		t.Fatalf("ProbeAgentShell: %v", err)
	}
	if got.Paths["fixture-agent"] != tool {
		t.Fatalf("fixture-agent = %q, want %q", got.Paths["fixture-agent"], tool)
	}
	if _, ok := got.Paths["missing-agent"]; ok {
		t.Fatalf("missing-agent unexpectedly present: %#v", got.Paths)
	}
	if !strings.HasPrefix(got.Path, bin+string(os.PathListSeparator)) {
		t.Fatalf("PATH = %q, want prefix %q", got.Path, bin)
	}
}

func TestAgentShellProbeRejectsInvalidCommandNamesBeforeStartingShell(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "started")
	shell := writeExecutable(t, "sh", "#!/bin/sh\ntouch "+shellSingleQuote(marker)+"\n")
	for _, names := range [][]string{{"../agent"}, {"agent name"}, {"-agent"}, {""}, {"agent", "agent"}} {
		if _, err := (AgentShellProbe{Shell: shell}).ProbeAgentShell(context.Background(), names); err == nil {
			t.Fatalf("ProbeAgentShell(%q) error = nil", names)
		}
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("shell started for invalid input: %v", err)
	}
}

func TestAgentShellProbeRejectsMalformedDuplicateUnexpectedIncompleteAndNonExecutableRecords(t *testing.T) {
	cases := map[string]string{
		"malformed":  `printf '%s\tFOUND\tonly-two-fields\n' "$marker"; printf '%s\tDONE\n' "$marker"`,
		"duplicate":  `printf '%s\tFOUND\tagent\t/bin/agent\n' "$marker"; printf '%s\tFOUND\tagent\t/bin/agent\n' "$marker"; printf '%s\tDONE\n' "$marker"`,
		"unexpected": `printf '%s\tFOUND\tother\t/bin/other\n' "$marker"; printf '%s\tDONE\n' "$marker"`,
		"incomplete": `printf '%s\tFOUND\tagent\t/bin/agent\n' "$marker"`,
		"alias":      `printf '%s\tFOUND\tagent\talias agent=/bin/agent\n' "$marker"; printf '%s\tDONE\n' "$marker"`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			shell := framedFakeShell(t, body)
			_, err := (AgentShellProbe{Shell: shell}).ProbeAgentShell(context.Background(), []string{"agent"})
			want := name
			if name == "alias" {
				want = "non-executable"
			}
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("ProbeAgentShell = %v, want %s", err, want)
			}
		})
	}
}

func TestAgentShellProbeTimesOutAndKillsDescendant(t *testing.T) {
	if testing.Short() {
		t.Skip("timeout behavior")
	}
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	shell := writeExecutable(t, "sh", "#!/bin/sh\n(sleep 30) &\necho $! > "+shellSingleQuote(pidFile)+"\nwait\n")
	start := time.Now()
	_, err := (AgentShellProbe{Shell: shell}).ProbeAgentShell(context.Background(), []string{"agent"})
	if err == nil {
		t.Fatal("ProbeAgentShell error = nil")
	}
	if elapsed := time.Since(start); elapsed > 4500*time.Millisecond {
		t.Fatalf("elapsed = %s, want <= 4.5s", elapsed)
	}
	pidBytes, readErr := os.ReadFile(pidFile)
	if readErr != nil {
		t.Fatalf("read child pid: %v", readErr)
	}
	pid := strings.TrimSpace(string(pidBytes))
	deadline := time.Now().Add(time.Second)
	for processAlive(pid) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if processAlive(pid) {
		t.Fatalf("descendant %s still alive", pid)
	}
}

func TestAgentShellProbeCancelsAndRejectsCombinedOutputOverflow(t *testing.T) {
	t.Run("cancellation", func(t *testing.T) {
		shell := writeExecutable(t, "sh", "#!/bin/sh\nsleep 30\n")
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := (AgentShellProbe{Shell: shell}).ProbeAgentShell(ctx, []string{"agent"})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want context canceled", err)
		}
	})
	t.Run("overflow", func(t *testing.T) {
		shell := writeExecutable(t, "sh", "#!/bin/sh\nwhile :; do printf 'xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx'; done\n")
		start := time.Now()
		_, err := (AgentShellProbe{Shell: shell}).ProbeAgentShell(context.Background(), []string{"agent"})
		if err == nil || !strings.Contains(err.Error(), "64 KiB") {
			t.Fatalf("error = %v, want output limit", err)
		}
		if elapsed := time.Since(start); elapsed > 2*time.Second {
			t.Fatalf("overflow cleanup took %s", elapsed)
		}
	})
}

func framedFakeShell(t *testing.T, body string) string {
	t.Helper()
	return writeExecutable(t, "sh", "#!/bin/sh\nmarker=$4\nprintf '%s\\tPATH\\t/bin\\n' \"$marker\"\n"+body+"\n")
}

func writeExecutable(t *testing.T, name, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(contents), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func shellSingleQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func processAlive(pid string) bool {
	return exec.Command("sh", "-c", "kill -0 "+pid+" 2>/dev/null").Run() == nil
}

func TestAgentShellProbeRejectsUnsupportedShell(t *testing.T) {
	shell := writeExecutable(t, "fish", "#!/bin/sh\nexit 0\n")
	t.Setenv("SHELL", shell)
	if _, err := agentShellCommand(context.Background(), "", "marker", []string{"agent"}); err == nil {
		t.Fatal("unsupported default shell accepted")
	}
	if _, err := agentShellCommand(context.Background(), shell, "marker", []string{"agent"}); err == nil {
		t.Fatal("unsupported configured shell accepted")
	}
}

func TestAgentShellProbeReapsChildWhenProfileParentExits(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	shell := writeExecutable(t, "sh", "#!/bin/sh\nsleep 30 &\necho $! > "+shellSingleQuote(pidFile)+"\nexit 0\n")
	_, _ = (AgentShellProbe{Shell: shell}).ProbeAgentShell(context.Background(), []string{"agent"})
	raw, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid := strings.TrimSpace(string(raw))
	t.Cleanup(func() { _ = exec.Command("kill", pid).Run() })
	deadline := time.Now().Add(time.Second)
	for processAlive(pid) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if processAlive(pid) {
		t.Fatalf("orphaned descendant %s", pid)
	}
}
