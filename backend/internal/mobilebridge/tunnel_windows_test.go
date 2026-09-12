//go:build windows

package mobilebridge

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// Run the test executable as a console-subsystem cloudflared stand-in so the
// production discovery and runner paths can be checked without network access.
func TestMain(m *testing.M) {
	if report := os.Getenv("AO_TEST_CLOUDFLARED_CONSOLE"); report != "" {
		console, _, _ := windows.NewLazySystemDLL("kernel32.dll").NewProc("GetConsoleWindow").Call()
		if err := os.WriteFile(report, []byte(strconv.FormatUint(uint64(console), 10)), 0o600); err != nil {
			os.Exit(2)
		}
		if len(os.Args) == 2 && os.Args[1] == "--version" {
			fmt.Println("cloudflared version 2026.7.2")
			os.Exit(0)
		}
		if len(os.Args) > 1 && os.Args[1] == "tunnel" {
			fmt.Fprintln(os.Stderr, cloudflaredStartup)
			for {
				time.Sleep(time.Second)
			}
		}
		os.Exit(2)
	}
	os.Exit(m.Run())
}

func TestCloudflaredVersionHasNoConsole(t *testing.T) {
	if runInDetachedProcess(t) {
		return
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	report := filepath.Join(t.TempDir(), "console")
	t.Setenv("AO_TEST_CLOUDFLARED_CONSOLE", report)
	version, ok := LocalCloudflaredLookup(t.TempDir()).Version(binary)
	if !ok || version != (CloudflaredVersion{2026, 7, 2}) {
		t.Fatalf("version = %+v, ok = %v", version, ok)
	}
	assertNoCloudflaredConsole(t, report)
}

func TestCloudflaredRunnerHasNoConsoleAndStops(t *testing.T) {
	if runInDetachedProcess(t) {
		return
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	report := filepath.Join(dir, "console")
	t.Setenv("AO_TEST_CLOUDFLARED_CONSOLE", report)
	runner := &TunnelRunner{
		Binary: binary, LocalPort: 3002, PIDPath: filepath.Join(dir, "tunnel.pid"),
		Runtime: &TunnelRuntime{}, SettleDelay: time.Millisecond,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	done := make(chan error, 1)
	go func() { done <- runner.runOnce(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("cloudflared did not stop after cancellation")
		}
		if runner.Runtime.Snapshot().Running {
			t.Error("stopped cloudflared is still reported as running")
		}
		if _, ok := ReadTunnelPID(runner.PIDPath); ok {
			t.Error("stopped cloudflared left its PID record behind")
		}
	})
	for runner.Runtime.Endpoint() == nil {
		select {
		case <-ctx.Done():
			t.Fatal("cloudflared did not become ready")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if _, ok := ReadTunnelPID(runner.PIDPath); !ok {
		t.Fatal("running cloudflared has no PID record")
	}
	assertNoCloudflaredConsole(t, report)
}

// Electron starts the daemon detached. Reproduce that parent process state:
// an ordinary go test console can otherwise mask the unwanted window.
func runInDetachedProcess(t *testing.T) bool {
	t.Helper()
	if os.Getenv("AO_TEST_DETACHED_TUNNEL") == "1" {
		return false
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "-test.run=^"+t.Name()+"$", "-test.v")
	cmd.Env = append(os.Environ(), "AO_TEST_DETACHED_TUNNEL=1")
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.DETACHED_PROCESS}
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("detached daemon test: %v\n%s", err, output)
	}
	return true
}

func assertNoCloudflaredConsole(t *testing.T, report string) {
	t.Helper()
	data, err := os.ReadFile(report)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "0" {
		t.Fatalf("cloudflared has console window handle %s; want no console", data)
	}
}
