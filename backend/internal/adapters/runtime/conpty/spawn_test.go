package conpty

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestManagedHostProcessWaitsAndReleasesPipesAfterExit(t *testing.T) {
	runManagedProcessHelperIfRequested()

	beforeFDs, canCountFDs := countOpenFDs()

	cmd := exec.Command(os.Args[0], "-test.run=TestManagedHostProcessWaitsAndReleasesPipesAfterExit")
	cmd.Env = append(os.Environ(), "AO_CONPTY_MANAGED_PROCESS_HELPER=exit")
	stderr := &boundedBuffer{max: maxCapturedStderr}
	proc, err := startManagedHostProcess(cmd, stderr)
	if err != nil {
		t.Fatalf("startManagedHostProcess: %v", err)
	}

	scanner := bufio.NewScanner(proc.stdout)
	if !scanner.Scan() {
		t.Fatalf("read READY line: %v", scanner.Err())
	}
	if got, want := strings.TrimSpace(scanner.Text()), "READY:123 456"; got != want {
		t.Fatalf("READY line = %q, want %q", got, want)
	}
	proc.closeStdout()

	select {
	case err := <-proc.exitC:
		if err != nil {
			t.Fatalf("wait: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("managed process did not report exit")
	}

	if _, ok := <-proc.exitC; ok {
		t.Fatal("exit channel still open after wait result")
	}
	if got := stderr.String(); !strings.Contains(got, "managed-helper-stderr") {
		t.Fatalf("stderr = %q, want helper diagnostic", got)
	}

	if canCountFDs {
		eventually(t, 2*time.Second, func() bool {
			afterFDs, ok := countOpenFDs()
			return ok && afterFDs <= beforeFDs
		}, "pipe file descriptors were not released")
	}
}

func TestManagedHostProcessKillAndWaitReleasesPipes(t *testing.T) {
	runManagedProcessHelperIfRequested()

	beforeFDs, canCountFDs := countOpenFDs()

	cmd := exec.Command(os.Args[0], "-test.run=TestManagedHostProcessKillAndWaitReleasesPipes")
	cmd.Env = append(os.Environ(), "AO_CONPTY_MANAGED_PROCESS_HELPER=sleep")
	stderr := &boundedBuffer{max: maxCapturedStderr}
	proc, err := startManagedHostProcess(cmd, stderr)
	if err != nil {
		t.Fatalf("startManagedHostProcess: %v", err)
	}

	scanner := bufio.NewScanner(proc.stdout)
	if !scanner.Scan() {
		t.Fatalf("read READY line: %v", scanner.Err())
	}
	if got, want := strings.TrimSpace(scanner.Text()), "READY:123 456"; got != want {
		t.Fatalf("READY line = %q, want %q", got, want)
	}

	_ = proc.killAndWait()
	if _, ok := <-proc.exitC; ok {
		t.Fatal("exit channel still open after killAndWait")
	}

	if canCountFDs {
		eventually(t, 2*time.Second, func() bool {
			afterFDs, ok := countOpenFDs()
			return ok && afterFDs <= beforeFDs
		}, "pipe file descriptors were not released after kill")
	}
}

func TestManagedHostProcessSurvivesStartupContextCancellation(t *testing.T) {
	runManagedProcessHelperIfRequested()

	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.Command(os.Args[0], "-test.run=TestManagedHostProcessSurvivesStartupContextCancellation")
	cmd.Env = append(os.Environ(), "AO_CONPTY_MANAGED_PROCESS_HELPER=sleep")
	stderr := &boundedBuffer{max: maxCapturedStderr}
	proc, err := startManagedHostProcess(cmd, stderr)
	if err != nil {
		t.Fatalf("startManagedHostProcess: %v", err)
	}

	scanner := bufio.NewScanner(proc.stdout)
	if !scanner.Scan() {
		t.Fatalf("read READY line: %v", scanner.Err())
	}
	if got, want := strings.TrimSpace(scanner.Text()), "READY:123 456"; got != want {
		t.Fatalf("READY line = %q, want %q", got, want)
	}
	proc.closeStdout()

	cancel()
	if ctx.Err() == nil {
		t.Fatal("startup context was not canceled")
	}
	select {
	case err := <-proc.exitC:
		t.Fatalf("managed process exited after startup context cancellation: %v", err)
	case <-time.After(150 * time.Millisecond):
	}

	_ = proc.killAndWait()
}

func TestStripEnvAssignments(t *testing.T) {
	tests := []struct {
		name            string
		argv            []string
		wantAssignments []string
		wantRest        []string
	}{
		{
			name:            "no env prefix returns argv unchanged",
			argv:            []string{"opencode", "--agent", "ao-x"},
			wantAssignments: nil,
			wantRest:        []string{"opencode", "--agent", "ao-x"},
		},
		{
			name:            "env prefix is split from the real command",
			argv:            []string{"env", "OPENCODE_CONFIG=C:/cfg.json", "opencode", "--agent", "ao-x"},
			wantAssignments: []string{"OPENCODE_CONFIG=C:/cfg.json"},
			wantRest:        []string{"opencode", "--agent", "ao-x"},
		},
		{
			name:            "env with no command left is untouched",
			argv:            []string{"env", "A=1", "B=2"},
			wantAssignments: nil,
			wantRest:        []string{"env", "A=1", "B=2"},
		},
		{
			name:            "a binary merely starting with env is not treated as a prefix",
			argv:            []string{"envoy", "--config", "x"},
			wantAssignments: nil,
			wantRest:        []string{"envoy", "--config", "x"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotAssignments, gotRest := stripEnvAssignments(tt.argv)
			if !reflect.DeepEqual(gotAssignments, tt.wantAssignments) {
				t.Errorf("assignments = %#v, want %#v", gotAssignments, tt.wantAssignments)
			}
			if !reflect.DeepEqual(gotRest, tt.wantRest) {
				t.Errorf("rest = %#v, want %#v", gotRest, tt.wantRest)
			}
		})
	}
}

func runManagedProcessHelperIfRequested() {
	switch os.Getenv("AO_CONPTY_MANAGED_PROCESS_HELPER") {
	case "exit":
		fmt.Fprintln(os.Stdout, "READY:123 456")
		fmt.Fprintln(os.Stderr, "managed-helper-stderr")
		os.Exit(0)
	case "sleep":
		fmt.Fprintln(os.Stdout, "READY:123 456")
		time.Sleep(30 * time.Second)
		os.Exit(0)
	}
}

func countOpenFDs() (int, bool) {
	for _, dir := range []string{"/proc/self/fd", "/dev/fd"} {
		entries, err := os.ReadDir(dir)
		if err == nil {
			return len(entries), true
		}
	}
	return 0, false
}

func eventually(t *testing.T, timeout time.Duration, fn func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if fn() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal(msg)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
