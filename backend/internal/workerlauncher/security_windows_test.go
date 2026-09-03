//go:build windows

package workerlauncher

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/workeridentity"
	"golang.org/x/sys/windows"
)

func TestWindowsPathCanonicalizationRejectsUNCADSAndJunction(t *testing.T) {
	root := t.TempDir()
	realDir := filepath.Join(root, "real-directory")
	if err := os.Mkdir(realDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := CanonicalContainedDirectory(`\\localhost\C$\Windows`, root); err == nil {
		t.Fatal("UNC path accepted")
	}
	executable := filepath.Join(root, "tool.exe")
	if err := os.WriteFile(executable, []byte("fixture"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := CanonicalExecutable(executable + ":payload"); err == nil {
		t.Fatal("alternate data stream accepted")
	}
	junction := filepath.Join(root, "junction")
	mklink := exec.Command("cmd.exe", "/d", "/c", "mklink", "/J", junction, realDir)
	if out, err := mklink.CombinedOutput(); err != nil {
		t.Skipf("directory junction unavailable: %v: %s", err, out)
	}
	if _, err := CanonicalContainedDirectory(junction, root); err == nil {
		t.Fatal("junction alias accepted")
	}
}

func TestVerifyExecutableIntegrity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trusted.exe")
	if err := os.WriteFile(path, []byte("trusted fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := verifyExecutableIntegrity(path, "1983b07b93a3e7104c9b137310c0a4f66f487e6b33a5d64675ef1d89e213639f"); err != nil {
		t.Fatal(err)
	}
	if err := verifyExecutableIntegrity(path, strings.Repeat("0", 64)); err == nil {
		t.Fatal("tampered executable was accepted")
	}
}

func TestProviderArguments(t *testing.T) {
	tests := []struct {
		action, provider string
		opts             ProviderCommandOptions
		want             string
	}{{"status", ExecutableClaude, ProviderCommandOptions{}, "auth status"},
		{"login", ExecutableClaude, ProviderCommandOptions{}, "auth login"},
		{"run", ExecutableClaude, ProviderCommandOptions{SessionID: "session-1", Prompt: "prompt-a"}, "--print --output-format json --permission-mode acceptEdits --session-id session-1 prompt-a"},
		{"resume", ExecutableClaude, ProviderCommandOptions{SessionID: "session-1", Prompt: "prompt-b"}, "--print --output-format json --permission-mode acceptEdits --resume session-1 prompt-b"},
		{"status", ExecutableCodex, ProviderCommandOptions{}, "login status"},
		{"login", ExecutableCodex, ProviderCommandOptions{}, "login"}}
	for _, tc := range tests {
		if got := strings.Join(providerArguments(tc.action, tc.provider, tc.opts), " "); got != tc.want {
			t.Fatalf("%s/%s: got %q, want %q", tc.provider, tc.action, got, tc.want)
		}
	}
}

func TestProviderEnvironmentAllowlistExcludesSCMCredentials(t *testing.T) {
	allowed := AllowedEnvironmentKeys()
	for _, key := range []string{"ANTHROPIC_BASE_URL", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_MODEL"} {
		if _, ok := allowed[key]; !ok {
			t.Fatalf("Provider environment key %q is not allowed", key)
		}
	}
	for _, key := range []string{"GITHUB_TOKEN", "GH_TOKEN", "GITLAB_TOKEN", "SSH_AUTH_SOCK"} {
		if _, ok := allowed[key]; ok {
			t.Fatalf("SCM environment key %q must not be allowed", key)
		}
	}
}

func TestBuildWorkerEnvironmentInjectsOnlySelectedProvider(t *testing.T) {
	manifest := Manifest{
		CanonicalWorktree: `D:\workspace\task-a`,
		WorkerProfileRoot: `D:\profiles\task-a`,
		AllowedEnvironmentKeys: []string{
			"ANTHROPIC_BASE_URL", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_MODEL",
		},
	}
	cfg := workeridentity.Config{
		PTYHostPath:  `D:\runtime\ao.exe`,
		LauncherPath: `D:\runtime\ao-worker-launcher.exe`,
		Executables:  map[string]string{ExecutableClaude: `D:\runtime\claude.exe`},
		AccountSID:   "S-1-5-21-worker",
	}

	taskA := environmentValues(buildWorkerEnvironment(manifest, cfg, `C:\Users\AOAgentWorker`, map[string]string{
		"ANTHROPIC_BASE_URL":   "https://provider-a.invalid/anthropic",
		"ANTHROPIC_AUTH_TOKEN": "task-a-provider-secret",
		"ANTHROPIC_MODEL":      "model-a",
		"GITHUB_TOKEN":         "host-scm-secret",
		"KIMI_API_KEY":         "unselected-provider-secret",
	}))
	wantRuntimeDir := filepath.Clean(`D:\runtime`)
	pathEntries := strings.Split(taskA["PATH"], string(os.PathListSeparator))
	foundRuntimeDir := false
	for _, entry := range pathEntries {
		if strings.EqualFold(filepath.Clean(entry), wantRuntimeDir) {
			foundRuntimeDir = true
			break
		}
	}
	if !foundRuntimeDir {
		t.Fatalf("Worker PATH does not contain the trusted executable directory: %q", taskA["PATH"])
	}
	taskB := environmentValues(buildWorkerEnvironment(manifest, cfg, `C:\Users\AOAgentWorker`, map[string]string{
		"ANTHROPIC_BASE_URL":   "https://provider-b.invalid/anthropic",
		"ANTHROPIC_AUTH_TOKEN": "task-b-provider-secret",
		"ANTHROPIC_MODEL":      "model-b",
	}))

	for _, key := range []string{"ANTHROPIC_BASE_URL", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_MODEL"} {
		if taskA[key] == "" || taskB[key] == "" || taskA[key] == taskB[key] {
			t.Fatalf("task-selected Provider value %q was not isolated", key)
		}
	}
	for _, env := range []map[string]string{taskA, taskB} {
		if env["HOME"] != manifest.WorkerProfileRoot || env["USERPROFILE"] != manifest.WorkerProfileRoot || env["CLAUDE_CONFIG_DIR"] != filepath.Join(manifest.WorkerProfileRoot, ".claude") {
			t.Fatalf("Claude Worker profile is not unified: %#v", env)
		}
		for _, key := range []string{"GITHUB_TOKEN", "GH_TOKEN", "GITLAB_TOKEN", "SSH_AUTH_SOCK", "KIMI_API_KEY"} {
			if _, ok := env[key]; ok {
				t.Fatalf("unselected or SCM credential %q reached Worker", key)
			}
		}
	}
}

type jobHelperResult struct {
	ChildPID, GrandchildPID int
	BreakawayBlocked        bool
}

func TestJobObjectHelper(t *testing.T) {
	if os.Getenv("AO_JOB_HELPER") != "1" {
		return
	}
	job, err := createSessionJob()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	defer job.Close()
	breakaway := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", "exit 0")
	breakaway.SysProcAttr = &windows.SysProcAttr{CreationFlags: windows.CREATE_BREAKAWAY_FROM_JOB, HideWindow: true}
	breakawayBlocked := breakaway.Start() != nil
	if !breakawayBlocked {
		_ = breakaway.Process.Kill()
		_, _ = breakaway.Process.Wait()
	}
	script := `$g=Start-Process -FilePath powershell.exe -ArgumentList '-NoProfile','-NonInteractive','-Command','Start-Sleep -Seconds 300' -WindowStyle Hidden -PassThru; [Console]::Out.WriteLine("$PID $($g.Id)"); [Console]::Out.Flush(); Start-Sleep -Seconds 300`
	child := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script)
	out, err := child.StdoutPipe()
	if err != nil {
		os.Exit(3)
	}
	if err := child.Start(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(4)
	}
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil {
		os.Exit(5)
	}
	fields := strings.Fields(line)
	if len(fields) != 2 {
		os.Exit(6)
	}
	childPID, _ := strconv.Atoi(fields[0])
	grandchildPID, _ := strconv.Atoi(fields[1])
	_ = json.NewEncoder(os.Stdout).Encode(jobHelperResult{ChildPID: childPID, GrandchildPID: grandchildPID, BreakawayBlocked: breakawayBlocked})
	_ = child.Wait()
	os.Exit(0)
}

func TestJobObjectKillsChildAndGrandchildAndBlocksBreakaway(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=TestJobObjectHelper")
	cmd.Env = append(os.Environ(), "AO_JOB_HELPER=1")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	var result jobHelperResult
	if err := json.NewDecoder(stdout).Decode(&result); err != nil {
		_ = cmd.Process.Kill()
		t.Fatal(err)
	}
	if !result.BreakawayBlocked {
		_ = cmd.Process.Kill()
		t.Fatal("CREATE_BREAKAWAY_FROM_JOB unexpectedly succeeded")
	}
	if !pidAlive(result.ChildPID) || !pidAlive(result.GrandchildPID) {
		_ = cmd.Process.Kill()
		t.Fatal("child process tree was not alive before job close")
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_, _ = cmd.Process.Wait()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !pidAlive(result.ChildPID) && !pidAlive(result.GrandchildPID) {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("job descendants survived close: child=%v grandchild=%v", pidAlive(result.ChildPID), pidAlive(result.GrandchildPID))
}

func pidAlive(pid int) bool {
	h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)
	status, err := windows.WaitForSingleObject(h, 0)
	return err == nil && status == uint32(windows.WAIT_TIMEOUT)
}
