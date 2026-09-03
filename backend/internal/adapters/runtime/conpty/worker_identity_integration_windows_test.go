//go:build windows

package conpty

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
	"unsafe"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/internal/workeridentity"
	"github.com/aoagents/agent-orchestrator/backend/internal/workerlauncher"
	"golang.org/x/sys/windows"
)

func TestWindowsWorkerIdentityIntegration(t *testing.T) {
	configPath := os.Getenv("AO_WINDOWS_SECURITY_INTEGRATION_CONFIG")
	if configPath == "" {
		t.Skip("set AO_WINDOWS_SECURITY_INTEGRATION_CONFIG and run elevated")
	}
	cfg, err := workeridentity.LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	git := cfg.Executables["git"]
	shell := cfg.Executables[workerlauncher.ExecutableShell]
	if git == "" || shell == "" {
		t.Fatal("integration config requires git and shell")
	}
	sessionID := fmt.Sprintf("p19a-%d", time.Now().UnixNano())
	worktree := filepath.Join(cfg.WorkspaceRoot, sessionID)
	other := filepath.Join(cfg.WorkspaceRoot, sessionID+"-other")
	for _, dir := range []string{worktree, other} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		_ = os.RemoveAll(worktree)
		_ = os.RemoveAll(other)
		_ = os.RemoveAll(filepath.Join(cfg.SessionProfileRoot, sessionID))
	})
	runHostGit(t, git, worktree, "init")
	if err := os.WriteFile(filepath.Join(other, "forbidden.txt"), []byte("host-only"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := protectFixture(filepath.Join(worktree, "host.dpapi")); err != nil {
		t.Fatal(err)
	}
	target := "AO_PHASE19A_" + strconv.FormatInt(time.Now().UnixNano(), 10)
	if err := writeCredential(target); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { deleteCredential(target) })
	hostHome, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	hostAppData := os.Getenv("APPDATA")
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	probePath := filepath.Join(worktree, "phase19a-probe.test.exe")
	if err := copyFile(self, probePath); err != nil {
		t.Fatal(err)
	}
	probeConfigPath := filepath.Join(worktree, "phase19a-probe.json")
	probeConfig := integrationProbeConfig{
		HostHome: hostHome, HostGitConfig: filepath.Join(hostHome, ".gitconfig"),
		HostGHConfig: filepath.Join(hostAppData, "gh"), HostGLabConfig: filepath.Join(hostAppData, "glab-cli"),
		HostSSH: filepath.Join(hostHome, ".ssh"), OtherSessionFile: filepath.Join(other, "forbidden.txt"),
		DPAPIPath: filepath.Join(worktree, "host.dpapi"), CredentialTarget: target,
	}
	probeJSON, _ := json.Marshal(probeConfig)
	if err := os.WriteFile(probeConfigPath, probeJSON, 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv(workerlauncher.IdentityConfigEnvironment, configPath)
	runtimeAdapter := New(Options{})
	handle, err := runtimeAdapter.Create(context.Background(), ports.RuntimeConfig{SessionID: domain.SessionID(sessionID),
		WorkspacePath: worktree, Argv: []string{shell, "/d", "/q"},
		Env: map[string]string{"AO_SESSION_ID": sessionID, "AO_PROJECT_ID": "phase19a-integration"}})
	if err != nil {
		t.Fatal(err)
	}
	stream, err := runtimeAdapter.Attach(context.Background(), handle, 30, 100)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	if err := stream.Resize(40, 120); err != nil {
		t.Fatal(err)
	}
	// ConPTY readiness means the host is listening, not that cmd.exe has
	// finished initializing its input loop. Wait for the first prompt so this
	// test does not classify a startup keystroke race as an identity failure.
	_ = readUntilIntegration(t, stream, ">", 10*time.Second)
	if err := runtimeAdapter.SendInput(context.Background(), handle, "echo P19A_READY\r"); err != nil {
		t.Fatal(err)
	}
	_ = readUntilIntegration(t, stream, "P19A_READY", 10*time.Second)

	command := `".\phase19a-probe.test.exe" -test.run=TestWindowsWorkerIdentityProbeHelper`
	if _, err := stream.Write([]byte(command + "\r")); err != nil {
		t.Fatal(err)
	}
	output := readUntilIntegration(t, stream, "P19A_RESULT:", 30*time.Second)
	if !strings.Contains(output, "中文验证") {
		t.Fatalf("UTF-8 Chinese output missing: %q", output)
	}
	result := parseIntegrationResult(t, output)
	if result.SID != cfg.AccountSID || result.ChildSID != cfg.AccountSID {
		t.Fatalf("worker SID=%s child SID=%s want=%s", result.SID, result.ChildSID, cfg.AccountSID)
	}
	if result.Admin {
		t.Fatal("worker token is administrator")
	}
	if result.HostHomeReadable || result.HostGitConfigReadable || result.HostGHConfigReadable || result.HostGLabConfigReadable || result.HostSSHReadable || result.OtherSessionReadable {
		t.Fatalf("ACL isolation failed: %+v", result)
	}
	if result.CredentialReadable || result.DPAPIDecrypted {
		t.Fatalf("credential isolation failed: %+v", result)
	}
	if result.SSHAuthSock != "" || result.SSHAgentReadable {
		t.Fatalf("SSH agent leaked: %+v", result)
	}
	if result.GitStatus != 0 || result.GitDiff != 0 || result.GitAdd != 0 || result.GitCommit != 0 || result.GitLog != 0 || result.GitBranch != 0 {
		t.Fatalf("local Git regression: %+v", result)
	}
	// B1 loads the dedicated AOAgentWorker Windows profile so Provider-native
	// sessions can persist across processes. It must remain distinct from the
	// host profile; per-session TEMP and ACL boundaries are verified separately.
	if strings.EqualFold(filepath.Clean(result.Profile), filepath.Clean(hostHome)) ||
		!strings.EqualFold(filepath.Base(filepath.Clean(result.Profile)), cfg.AccountName) {
		t.Fatalf("worker profile=%q host profile=%q account=%q", result.Profile, hostHome, cfg.AccountName)
	}

	interruptStarted := time.Now()
	if _, err := stream.Write([]byte("ping -n 8 127.0.0.1 >nul\r")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(500 * time.Millisecond)
	if err := runtimeAdapter.Interrupt(context.Background(), handle); err != nil {
		t.Fatal(err)
	}
	if _, err := stream.Write([]byte("echo P19A_INTERRUPT_OK\r")); err != nil {
		t.Fatal(err)
	}
	_ = readUntil(t, stream, "P19A_INTERRUPT_OK", 10*time.Second)
	if elapsed := time.Since(interruptStarted); elapsed > 3*time.Second {
		t.Logf("known ConPTY Interrupt P1 reproduced under isolated identity (fallback completion after %s)", elapsed.Round(time.Millisecond))
	}

	if _, err := stream.Write([]byte("set AO_PHASE19A_CHILD=1&& \".\\phase19a-probe.test.exe\" -test.run=TestWindowsWorkerIdentityProbeHelper\r")); err != nil {
		t.Fatal(err)
	}
	childOutput := readUntil(t, stream, "P19A_CHILD:", 10*time.Second)
	childPID := parsePID(t, childOutput)
	if !windowsPIDAlive(childPID) {
		t.Fatal("worker child did not start")
	}
	if !windowsPIDInAnyJob(childPID) {
		t.Fatal("worker child is not assigned to a Job Object")
	}
	if err := runtimeAdapter.Destroy(context.Background(), handle); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !windowsPIDAlive(childPID) {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("worker child %d survived session Job close", childPID)
}

func readUntilIntegration(t *testing.T, reader io.Reader, want string, timeout time.Duration) string {
	t.Helper()
	done := make(chan string, 1)
	progress := make(chan string, 1)
	go func() {
		var buf []byte
		tmp := make([]byte, 4096)
		for {
			n, err := reader.Read(tmp)
			if n > 0 {
				buf = append(buf, tmp[:n]...)
				latest := string(buf)
				select {
				case progress <- latest:
				default:
					select {
					case <-progress:
					default:
					}
					progress <- latest
				}
				if strings.Contains(latest, want) {
					done <- latest
					return
				}
			}
			if err != nil {
				done <- string(buf)
				return
			}
		}
	}()
	var latest string
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		select {
		case output := <-done:
			return output
		case latest = <-progress:
		case <-timer.C:
			t.Fatalf("timed out reading for %q; output=%q", want, latest)
		}
	}
}

type integrationResult struct {
	SID                    string `json:"sid"`
	ChildSID               string `json:"childSid"`
	Admin                  bool   `json:"admin"`
	Profile                string `json:"profile"`
	HostHomeReadable       bool   `json:"hostHomeReadable"`
	HostGitConfigReadable  bool   `json:"hostGitConfigReadable"`
	HostGHConfigReadable   bool   `json:"hostGHConfigReadable"`
	HostGLabConfigReadable bool   `json:"hostGLabConfigReadable"`
	HostSSHReadable        bool   `json:"hostSSHReadable"`
	OtherSessionReadable   bool   `json:"otherSessionReadable"`
	CredentialReadable     bool   `json:"credentialReadable"`
	DPAPIDecrypted         bool   `json:"dpapiDecrypted"`
	SSHAuthSock            string `json:"sshAuthSock"`
	SSHAgentReadable       bool   `json:"sshAgentReadable"`
	GitStatus              int    `json:"gitStatus"`
	GitDiff                int    `json:"gitDiff"`
	GitAdd                 int    `json:"gitAdd"`
	GitCommit              int    `json:"gitCommit"`
	GitLog                 int    `json:"gitLog"`
	GitBranch              int    `json:"gitBranch"`
	GitDiagnostic          string `json:"gitDiagnostic"`
}

type integrationProbeConfig struct {
	HostHome, HostGitConfig, HostGHConfig, HostGLabConfig, HostSSH string
	OtherSessionFile, DPAPIPath, CredentialTarget                  string
}

func TestWindowsWorkerIdentityProbeHelper(t *testing.T) {
	if os.Getenv("AO_PHASE19A_CHILD") == "1" {
		child := exec.Command("cmd.exe", "/d", "/c", "ping -n 300 127.0.0.1 >nul")
		if err := child.Start(); err != nil {
			t.Fatal(err)
		}
		fmt.Printf("P19A_CHILD:%d\r\n", child.Process.Pid)
		_ = child.Wait()
		return
	}
	if os.Getenv("AO_WORKER_IDENTITY") == "" {
		return
	}
	data, err := os.ReadFile("phase19a-probe.json")
	if err != nil {
		t.Fatal(err)
	}
	var cfg integrationProbeConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	token := windows.GetCurrentProcessToken()
	user, err := token.GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	adminSID, _ := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	admin, _ := token.IsMember(adminSID)
	result := integrationResult{
		SID: user.User.Sid.String(), Admin: admin, Profile: os.Getenv("USERPROFILE"),
		HostHomeReadable: canReadPath(cfg.HostHome), HostGitConfigReadable: canReadPath(cfg.HostGitConfig),
		HostGHConfigReadable: canReadPath(cfg.HostGHConfig), HostGLabConfigReadable: canReadPath(cfg.HostGLabConfig),
		HostSSHReadable: canReadPath(cfg.HostSSH), OtherSessionReadable: canReadPath(cfg.OtherSessionFile),
		CredentialReadable: credentialExists(cfg.CredentialTarget), DPAPIDecrypted: canUnprotect(cfg.DPAPIPath),
		SSHAuthSock: os.Getenv("SSH_AUTH_SOCK"),
	}
	childTokenProbe := exec.Command("cmd.exe", "/d", "/c", "ping -n 3 127.0.0.1 >nul")
	if err := childTokenProbe.Start(); err == nil {
		if process, openErr := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(childTokenProbe.Process.Pid)); openErr == nil {
			var childToken windows.Token
			if tokenErr := windows.OpenProcessToken(process, windows.TOKEN_QUERY, &childToken); tokenErr == nil {
				if childUser, userErr := childToken.GetTokenUser(); userErr == nil {
					result.ChildSID = childUser.User.Sid.String()
				}
				_ = childToken.Close()
			}
			_ = windows.CloseHandle(process)
		}
		_ = childTokenProbe.Process.Kill()
		_, _ = childTokenProbe.Process.Wait()
	}
	ssh := exec.Command(`C:\Windows\System32\OpenSSH\ssh-add.exe`, "-l")
	result.SSHAgentReadable = ssh.Run() == nil
	if err := os.WriteFile("worker-change.txt", []byte("changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	result.GitStatus, result.GitDiagnostic = runProbeGit("status", "--short")
	result.GitDiff, _ = runProbeGit("diff")
	result.GitAdd, _ = runProbeGit("add", "worker-change.txt")
	result.GitCommit, _ = runProbeGit("commit", "-m", "local-worker-commit")
	result.GitLog, _ = runProbeGit("log", "-1", "--oneline")
	result.GitBranch, _ = runProbeGit("branch", "--show-current")
	encoded, _ := json.Marshal(result)
	fmt.Printf("中文验证\r\nP19A_RESULT:%s\r\n", base64.StdEncoding.EncodeToString(encoded))
}

func runProbeGit(args ...string) (int, string) {
	cmd := exec.Command("git.exe", args...)
	output, err := cmd.CombinedOutput()
	diagnostic := strings.TrimSpace(string(output))
	if len(diagnostic) > 512 {
		diagnostic = diagnostic[:512]
	}
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			return exit.ExitCode(), diagnostic
		}
		return -1, diagnostic
	}
	return 0, diagnostic
}

func canReadPath(path string) bool {
	if path == "" {
		return false
	}
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	if info.IsDir() {
		entries, err := os.ReadDir(path)
		return err == nil && len(entries) >= 0
	}
	file, err := os.Open(path)
	if err == nil {
		_ = file.Close()
	}
	return err == nil
}

func canUnprotect(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil || len(data) == 0 {
		return false
	}
	in := windows.DataBlob{Size: uint32(len(data)), Data: &data[0]}
	var out windows.DataBlob
	if err := windows.CryptUnprotectData(&in, nil, nil, 0, nil, 1, &out); err != nil {
		return false
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	return true
}

func credentialExists(target string) bool {
	target16, _ := windows.UTF16PtrFromString(target)
	var credential uintptr
	r, _, _ := testAdvapi.NewProc("CredReadW").Call(uintptr(unsafe.Pointer(target16)), 1, 0, uintptr(unsafe.Pointer(&credential)))
	if r != 0 {
		testAdvapi.NewProc("CredFree").Call(credential)
		return true
	}
	return false
}

func copyFile(source, destination string) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(destination, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o700)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

var integrationResultRE = regexp.MustCompile(`P19A_RESULT:([A-Za-z0-9+/=]+)`)
var childPIDRE = regexp.MustCompile(`P19A_CHILD:(\d+)`)

func parseIntegrationResult(t *testing.T, output string) integrationResult {
	t.Helper()
	match := integrationResultRE.FindStringSubmatch(output)
	if match == nil {
		t.Fatalf("result missing: %q", output)
	}
	data, err := base64.StdEncoding.DecodeString(match[1])
	if err != nil {
		t.Fatal(err)
	}
	var result integrationResult
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	return result
}
func parsePID(t *testing.T, output string) int {
	t.Helper()
	match := childPIDRE.FindStringSubmatch(output)
	if match == nil {
		t.Fatalf("child PID missing: %q", output)
	}
	pid, _ := strconv.Atoi(match[1])
	return pid
}
func runHostGit(t *testing.T, git, cwd string, args ...string) {
	t.Helper()
	cmd := exec.Command(git, args...)
	cmd.Dir = cwd
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

func protectFixture(path string) error {
	plain := make([]byte, 32)
	if _, err := rand.Read(plain); err != nil {
		return err
	}
	in := windows.DataBlob{Size: uint32(len(plain)), Data: &plain[0]}
	var out windows.DataBlob
	if err := windows.CryptProtectData(&in, nil, nil, 0, nil, 1, &out); err != nil {
		return err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	return os.WriteFile(path, append([]byte(nil), unsafe.Slice(out.Data, out.Size)...), 0o600)
}

type testCredential struct {
	Flags, Type             uint32
	TargetName, Comment     *uint16
	LastWritten             windows.Filetime
	BlobSize                uint32
	Blob                    *byte
	Persist, AttributeCount uint32
	Attributes              uintptr
	TargetAlias, UserName   *uint16
}

var testAdvapi = windows.NewLazySystemDLL("advapi32.dll")
var testCredWrite = testAdvapi.NewProc("CredWriteW")
var testCredDelete = testAdvapi.NewProc("CredDeleteW")

func writeCredential(target string) error {
	target16, _ := windows.UTF16PtrFromString(target)
	user16, _ := windows.UTF16PtrFromString("phase19a")
	blob := make([]byte, 32)
	_, _ = rand.Read(blob)
	cred := testCredential{Type: 1, TargetName: target16, BlobSize: uint32(len(blob)), Blob: &blob[0], Persist: 1, UserName: user16}
	r, _, e := testCredWrite.Call(uintptr(unsafe.Pointer(&cred)), 0)
	if r == 0 {
		return e
	}
	return nil
}
func deleteCredential(target string) {
	target16, _ := windows.UTF16PtrFromString(target)
	testCredDelete.Call(uintptr(unsafe.Pointer(target16)), 1, 0)
}
func windowsPIDAlive(pid int) bool {
	h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)
	status, err := windows.WaitForSingleObject(h, 0)
	return err == nil && status == uint32(windows.WAIT_TIMEOUT)
}

func windowsPIDInAnyJob(pid int) bool {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)
	var inJob int32
	r, _, _ := windows.NewLazySystemDLL("kernel32.dll").NewProc("IsProcessInJob").Call(uintptr(h), 0, uintptr(unsafe.Pointer(&inJob)))
	return r != 0 && inJob != 0
}
