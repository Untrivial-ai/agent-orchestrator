//go:build windows

package persistenthost

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestWindowsProviderBridge(t *testing.T) {
	if os.Getenv("AO_WINDOWS_JOB_BRIDGE") != "1" {
		return
	}
	child := exec.Command(os.Args[0], "-test.run=^TestWindowsProviderGrandchild$")
	child.Env = append(os.Environ(), "AO_WINDOWS_JOB_GRANDCHILD=1")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(os.Getenv("AO_WINDOWS_JOB_PID_FILE"), []byte(strconv.Itoa(child.Process.Pid)), 0o600); err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		if strings.TrimSpace(scanner.Text()) == "exit" {
			return
		}
	}
}

func TestWindowsProviderGrandchild(t *testing.T) {
	if os.Getenv("AO_WINDOWS_JOB_GRANDCHILD") != "1" {
		return
	}
	time.Sleep(30 * time.Second)
}

func TestWindowsProviderJobReapsGrandchild(t *testing.T) {
	for _, tc := range []struct {
		name         string
		providerExit bool
	}{
		{name: "explicit shutdown"},
		{name: "bridge exits first", providerExit: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dataDir := t.TempDir()
			pidFile := filepath.Join(dataDir, "grandchild.pid")
			cfg := Config{
				SessionID: "windows-job-" + strings.ReplaceAll(tc.name, " ", "-"),
				DataDir:   dataDir, Workdir: t.TempDir(), Protocol: ProtocolRaw,
				Env:  append(os.Environ(), "AO_WINDOWS_JOB_BRIDGE=1", "AO_WINDOWS_JOB_PID_FILE="+pidFile),
				Argv: []string{os.Args[0], "-test.run=^TestWindowsProviderBridge$"},
			}
			done := make(chan error, 1)
			go func() { done <- Run(context.Background(), cfg) }()
			t.Cleanup(func() {
				_ = Shutdown(context.Background(), dataDir, cfg.SessionID)
			})
			d := awaitDescriptor(t, dataDir, cfg.SessionID)
			var pid int
			deadline := time.Now().Add(3 * time.Second)
			for time.Now().Before(deadline) {
				raw, err := os.ReadFile(pidFile)
				if err == nil {
					pid, err = strconv.Atoi(string(raw))
					if err != nil {
						t.Fatal(err)
					}
					break
				}
				time.Sleep(20 * time.Millisecond)
			}
			if pid <= 0 {
				t.Fatal("grandchild did not publish its pid")
			}
			grandchild, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_TERMINATE,
				false, uint32(pid))
			if err != nil {
				t.Fatalf("open grandchild %d: %v", pid, err)
			}
			t.Cleanup(func() {
				if status, err := windows.WaitForSingleObject(grandchild, 0); err == nil && status == uint32(windows.WAIT_TIMEOUT) {
					_ = windows.TerminateProcess(grandchild, 1)
				}
				_ = windows.CloseHandle(grandchild)
			})
			if status, err := windows.WaitForSingleObject(grandchild, 0); err != nil || status != uint32(windows.WAIT_TIMEOUT) {
				t.Fatalf("grandchild was not running, pid=%d status=%d err=%v", pid, status, err)
			}
			if tc.providerExit {
				transport := awaitAttach(t, d)
				if _, err := fmt.Fprintln(transport.Stdin, "exit"); err != nil {
					t.Fatal(err)
				}
			} else if err := Shutdown(context.Background(), dataDir, cfg.SessionID); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-done:
				if err != nil && !(tc.providerExit && errors.Is(err, io.EOF)) {
					t.Fatal(err)
				}
			case <-time.After(8 * time.Second):
				t.Fatal("host did not exit")
			}
			if status, err := windows.WaitForSingleObject(grandchild, 2000); err != nil || status != windows.WAIT_OBJECT_0 {
				t.Fatalf("provider grandchild %d survived host exit: status=%d err=%v", pid, status, err)
			}
		})
	}
}
