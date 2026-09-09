//go:build linux

package tmux

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestLinuxProcessIdentityParsesParenthesesAndZombie(t *testing.T) {
	stat := "4242 (worker (copy)) Z 1 4242 4242 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 12345 0"
	p, err := parseLinuxProcess(4242, stat, "boot-id")
	if err != nil || p.Start != "boot-id:12345" || p.Parent != 1 || p.Session != "4242" || !p.Stopped {
		t.Fatalf("parsed %+v, %v", p, err)
	}
	if _, err := parseLinuxProcess(4242, "4242 malformed", "boot-id"); err == nil {
		t.Fatal("malformed inventory accepted")
	}
}

func TestLinuxSignalRejectsChangedBirthIdentity(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	table, err := readOwnedProcesses(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var expected ownedProcess
	for _, p := range table {
		if p.PID == cmd.Process.Pid {
			expected = p
		}
	}
	if expected.PID == 0 {
		t.Fatal("fixture absent")
	}
	expected.Start = "different-boot:1"
	if err := signalOwnedProcess(context.Background(), expected, true); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("unrelated generation was killed: %v", err)
	}
}

// A real pane owns a workload and a TERM-resistant background child in another
// process group. Teardown must stop both and leave an unrelated process alive.
func TestDestroyRealBackgroundChild(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux unavailable")
	}
	r := newIntegrationRuntime(t)
	r.reapGrace = 80 * time.Millisecond
	dir := t.TempDir()
	handle, err := r.Create(context.Background(), ports.RuntimeConfig{
		SessionID:     "owned-teardown",
		WorkspacePath: dir,
		Argv:          []string{os.Args[0], "-test.run=^TestTeardownProcessHelper$"},
		Env:           map[string]string{"AO_TEARDOWN_TEST_MODE": "parent", "AO_TEARDOWN_TEST_DIR": dir},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Destroy(context.Background(), handle) })
	var pids []int
	for _, name := range []string{"parent", "child"} {
		deadline := time.Now().Add(5 * time.Second)
		for {
			data, err := os.ReadFile(filepath.Join(dir, name))
			if err == nil {
				pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
				if err != nil {
					t.Fatal(err)
				}
				pids = append(pids, pid)
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("helper %s never ready: %v", name, err)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	parentGroup, err := syscall.Getpgid(pids[0])
	if err != nil {
		t.Fatal(err)
	}
	childGroup, err := syscall.Getpgid(pids[1])
	if err != nil {
		t.Fatal(err)
	}
	if childGroup == parentGroup {
		t.Fatal("fixture did not create a separate background process group")
	}
	unrelated := exec.Command("sleep", "30")
	if err := unrelated.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = unrelated.Process.Kill(); _ = unrelated.Wait() })
	if err := r.Destroy(context.Background(), handle); err != nil {
		t.Fatal(err)
	}
	table, err := readOwnedProcesses(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range table {
		for _, pid := range pids {
			if p.PID == pid && !p.Stopped {
				t.Fatalf("owned workload %d survived Destroy", pid)
			}
		}
	}
	if err := unrelated.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("unrelated workload stopped: %v", err)
	}
	if err := r.Destroy(context.Background(), handle); err != nil {
		t.Fatalf("repeat teardown: %v", err)
	}
}

func TestTeardownProcessHelper(t *testing.T) {
	mode := os.Getenv("AO_TEARDOWN_TEST_MODE")
	if mode == "" {
		return
	}
	signal.Ignore(syscall.SIGHUP, syscall.SIGTERM)
	dir := os.Getenv("AO_TEARDOWN_TEST_DIR")
	if mode == "parent" {
		child := exec.Command(os.Args[0], "-test.run=^TestTeardownProcessHelper$")
		child.Env = append(os.Environ(), "AO_TEARDOWN_TEST_MODE=child")
		child.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if err := child.Start(); err != nil {
			t.Fatal(err)
		}
		defer func() { _ = child.Process.Kill(); _ = child.Wait() }()
	}
	if err := os.WriteFile(filepath.Join(dir, mode), []byte(fmt.Sprint(os.Getpid())), 0o600); err != nil {
		t.Fatal(err)
	}
	for {
		time.Sleep(time.Second)
	}
}
